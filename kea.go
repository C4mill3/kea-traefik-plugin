package kea_traefik_plugin

// Important: Traefik creates one plugin instance per route.
// We rely on globals to share the cache across all instances so that
// only one refresh runs regardless of how many routes use this middleware.
// NetbirdURL, Token, and RefreshSeconds take effect only from
// the first middleware instance that is created.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Global shared state (set once from first instance)
var (
	globalMu         sync.RWMutex
	globalCache      GroupPeers
	globalGroups     groups
	globalLastFetch  time.Time
	globalConfigPath string
	globalURL        string
	globalToken      string
	globalInterval   time.Duration
	globalLogLevel   = logLevelErr
	globalOnce       sync.Once
)

// Config is the Traefik plugin config (per-route, set via labels or dynamic config)
type Config struct {
	ConfigPath  string   `json:"configPath,omitempty"`
	AllowGroups []string `json:"allowGroups,omitempty"`
}

// fileConfig is the structure of the YAML secret file at ConfigPath
type fileConfig struct {
	Settings settings `yaml:"Settings"`
	Groups   groups   `yaml:"Groups"`
}

type settings struct {
	NetbirdURL     string `yaml:"NetbirdUrl"`
	Token          string `yaml:"Token"`
	RefreshSeconds int    `yaml:"RefreshSeconds"`
	LogLevel       string `yaml:"LogLevel"`
}

// groups maps a group name to a list of CIDR ranges used as a local whitelist
type groups map[string][]string

// CreateConfig returns a Config with sensible defaults.
func CreateConfig() *Config {
	return &Config{}
}

// NetbirdIPGuard is the middleware handler.
type NetbirdIPGuard struct {
	next        http.Handler
	name        string
	allowGroups []string
}

// New creates a new Kea middleware instance.
func New(_ context.Context, next http.Handler, cfg *Config, name string) (http.Handler, error) {
	if cfg.ConfigPath == "" {
		return nil, fmt.Errorf("configPath is required")
	}

	var initErr error
	globalOnce.Do(func() { // Define global used by all instance
		globalConfigPath = cfg.ConfigPath

		shared, err := loadSharedConfig(cfg.ConfigPath)
		if err != nil {
			initErr = err
			return
		}

		globalURL = shared.Settings.NetbirdURL
		globalToken = shared.Settings.Token
		if shared.Settings.RefreshSeconds > 0 {
			globalInterval = time.Duration(shared.Settings.RefreshSeconds) * time.Second
		} else {
			globalInterval = 300 * time.Second // Default
		}
		parsedLogLevel, err := parseLogLevel(shared.Settings.LogLevel)
		if err != nil {
			initErr = err
			return
		}
		globalLogLevel = parsedLogLevel
		globalGroups = shared.Groups
		infof("initialized: configPath=%s url=%s refreshInterval=%s localGroups=%d logLevel=%s",
			globalConfigPath, globalURL, globalInterval, len(globalGroups), logLevelName(globalLogLevel))
	})
	if initErr != nil {
		return nil, initErr
	}
	infof("new instance: me=%s allowGroups=%v", name, cfg.AllowGroups)

	return &NetbirdIPGuard{
		next:        next,
		name:        name,
		allowGroups: cfg.AllowGroups,
	}, nil
}

func (g *NetbirdIPGuard) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if err := refreshIfNeeded(); err != nil {
		errf("refreshing peer list failed: %v", err)
		http.Error(rw, "Error, please contact admin", http.StatusInternalServerError)
		return
	}

	srcIP := extractIP(req)
	ip := net.ParseIP(srcIP)

	// Copy global var
	globalMu.RLock()
	localCache := globalCache
	localGroups := globalGroups
	globalMu.RUnlock()

	for _, group := range g.allowGroups { // check for each group allowed on this route

		// Check for local CIDR ranges from config file (Priority)
		for _, cidr := range localGroups[group] {
			_, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				errf("invalid CIDR %q in group %q: %v", cidr, group, err)
				continue
			}
			if ip != nil && ipNet.Contains(ip) {
				infof("ALLOW ip=%s group=%s (local CIDR %s) route=%s", srcIP, group, cidr, g.name)
				g.next.ServeHTTP(rw, req)
				return
			}
		}

		// 2. Check NetBird API peer list
		for _, peerIP := range localCache[group] {
			if peerIP == srcIP {
				infof("ALLOW ip=%s group=%s (netbird) route=%s", srcIP, group, g.name)
				g.next.ServeHTTP(rw, req)
				return
			}
		}
	}

	infof("DENY ip=%s route=%s allowGroups=%v", srcIP, g.name, g.allowGroups)
	http.Error(rw, "Forbidden", http.StatusForbidden)
}

func refreshIfNeeded() error {
	globalMu.RLock()
	stale := globalCache == nil || time.Since(globalLastFetch) >= globalInterval
	globalMu.RUnlock()

	if !stale {
		return nil
	}

	globalMu.Lock()
	defer globalMu.Unlock()

	// Re-check after acquiring write lock (another goroutine may have refreshed).
	if globalCache != nil && time.Since(globalLastFetch) < globalInterval {
		return nil
	}

	infof("refreshing peer cache from %s", globalURL)
	peers, err := FetchNetbirdPeerGroups(globalURL, globalToken)
	if err != nil {
		errf("fetching peers failed: %v", err)
		return err
	}
	globalCache = peers
	globalLastFetch = time.Now()
	infof("peer cache refreshed: %d groups loaded", len(peers))
	return nil
}

func parseLogLevel(value string) (logLevel, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "err":
		return logLevelErr, nil
	case "none":
		return logLevelNone, nil
	case "info":
		return logLevelInfo, nil
	default:
		return logLevelErr, fmt.Errorf("invalid Settings.LogLevel %q: allowed values are None, Err, Info", value)
	}
}

func loadSharedConfig(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading configPath %q: %w", path, err)
	}

	var cfg fileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing configPath %q: %w", path, err)
	}
	if cfg.Settings.NetbirdURL == "" {
		return nil, fmt.Errorf("configPath %q: Settings.NetbirdUrl is required", path)
	}
	if cfg.Settings.Token == "" {
		return nil, fmt.Errorf("configPath %q: Settings.Token is required", path)
	}
	if _, err := parseLogLevel(cfg.Settings.LogLevel); err != nil {
		return nil, fmt.Errorf("configPath %q: %w", path, err)
	}

	return &cfg, nil
}

// extractIP returns the real client IP from the request.
func extractIP(req *http.Request) string {
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the leftmost address, which is the original client.
		if idx := strings.Index(xff, ","); idx != -1 {
			xff = xff[:idx]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if xri := req.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}
