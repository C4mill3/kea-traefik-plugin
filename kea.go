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
	globalMu        sync.RWMutex
	globalCache     GroupPeers
	globalIPUserMap IPUserMap
	globalUserCache UserIDMap
	globalGroups    groups
	globalLastFetch time.Time
	globalConfigRef string
	globalURL       string
	globalToken     string
	globalInterval  time.Duration
	globalLogLevel  = logLevelErr
	globalOnce      sync.Once
)

// Config is the Traefik plugin config (per-route, set via labels or dynamic config)
type Config struct {
	ConfigPath   string        `json:"configPath,omitempty"`
	InlineConfig *inlineConfig `json:"inlineConfig,omitempty"`
	AllowGroups  []string      `json:"allowGroups,omitempty"`
	// AppURL is the URL/domain this route protects. When set, the route is
	// recorded in a global registry so that routes with AccessHeaders enabled
	// can list it among the URLs a client is allowed to access.
	AppURL string `json:"appUrl,omitempty"`
	// AccessHeaders, when true, makes this route inject a request header
	// (accessHeaderName) listing every registered AppURL the caller's IP is
	// allowed to access. Defaults to false so other routes are unaffected.
	AccessHeaders bool `json:"accessHeaders,omitempty"`
	// UserHeader specifies the header name for the authenticated user's name.
	// Defaults to "Remote-User" if not set.
	UserHeader string `json:"userHeader,omitempty"`
	// EmailHeader specifies the header name for the authenticated user's email.
	// Defaults to "Remote-Email" if not set.
	EmailHeader string `json:"emailHeader,omitempty"`
}

// inlineConfig can be used directly in plugin config when no file secret is mounted.
type inlineConfig struct {
	NetbirdURL     string `json:"netbirdUrl,omitempty"`
	Token          string `json:"token,omitempty"`
	RefreshSeconds int    `json:"refreshSeconds,omitempty"`
	LogLevel       string `json:"logLevel,omitempty"`
	Groups         groups `json:"groups,omitempty"`
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

// accessHeaderName is the request header injected into the backend by routes
// with AccessHeaders enabled. It holds a comma-separated list of the AppURLs
// the caller's IP is allowed to access.
const accessHeaderName = "X-Kea-Allowed-Urls"

// routeEntry records what a single kea-protected route grants.
type routeEntry struct {
	url         string
	allowGroups []string
}

// Global registry of every route that declared an AppURL, keyed by middleware
// instance name to deduplicate re-registrations on config reload.
var (
	globalRoutesMu sync.RWMutex
	globalRoutes   = map[string]routeEntry{}
)

// CreateConfig returns a Config with sensible defaults.
func CreateConfig() *Config {
	return &Config{}
}

// NetbirdIPGuard is the middleware handler.
type NetbirdIPGuard struct {
	next          http.Handler
	name          string
	allowGroups   []string
	accessHeaders bool
	userHeader    string
	emailHeader   string
}

// New creates a new Kea middleware instance.
func New(_ context.Context, next http.Handler, cfg *Config, name string) (http.Handler, error) {
	var initErr error
	globalOnce.Do(func() { // Define global used by all instance
		shared, configRef, err := loadSharedFromPluginConfig(cfg)
		if err != nil {
			initErr = err
			return
		}
		globalConfigRef = configRef

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

		infof("initialized: config=%s url=%s refreshInterval=%s localGroups=%d logLevel=%s",
			globalConfigRef, globalURL, globalInterval, len(globalGroups), logLevelName(globalLogLevel))
	})
	if initErr != nil {
		return nil, initErr
	}
	if cfg.AppURL != "" {
		globalRoutesMu.Lock()
		globalRoutes[name] = routeEntry{url: cfg.AppURL, allowGroups: cfg.AllowGroups}
		globalRoutesMu.Unlock()
	}

	userHeader := "Remote-User"
	emailHeader := "Remote-Email"
	if cfg.UserHeader != "" {
		userHeader = cfg.UserHeader
	}
	if cfg.EmailHeader != "" {
		emailHeader = cfg.EmailHeader
	}

	infof("new instance: me=%s allowGroups=%v appUrl=%q accessHeaders=%t userHeader=%q emailHeader=%q", name, cfg.AllowGroups, cfg.AppURL, cfg.AccessHeaders, userHeader, emailHeader)

	return &NetbirdIPGuard{
		next:          next,
		name:          name,
		allowGroups:   cfg.AllowGroups,
		accessHeaders: cfg.AccessHeaders,
		userHeader:    userHeader,
		emailHeader:   emailHeader,
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
	localIPUserMap := globalIPUserMap
	localUserCache := globalUserCache
	globalMu.RUnlock()

	// Inject the access header before forwarding. We always Set (overwrite) so a
	// client cannot spoof the header by sending it themselves.
	if g.accessHeaders {
		urls := allowedURLsForIP(ip, srcIP, localGroups, localCache)
		req.Header.Set(accessHeaderName, strings.Join(urls, ","))
		infof("access header set ip=%s route=%s urls=%d", srcIP, g.name, len(urls))
	}

	for _, group := range g.allowGroups { // check for each group allowed on this route
		if matched, src := ipInGroup(ip, srcIP, group, localGroups, localCache); matched {
			infof("ALLOW ip=%s group=%s (%s) route=%s", srcIP, group, src, g.name)
			addForwardedHeaders(req, srcIP, localIPUserMap, localUserCache, g.userHeader, g.emailHeader)
			g.next.ServeHTTP(rw, req)
			return
		}
	}

	infof("DENY ip=%s route=%s allowGroups=%v", srcIP, g.name, g.allowGroups)
	http.Error(rw, "Forbidden", http.StatusForbidden)
}

// ipInGroup reports whether srcIP/ip belongs to group, checking local CIDR
// ranges first (priority) then the NetBird peer cache. The returned string
// names which source matched, for logging.
func ipInGroup(ip net.IP, srcIP, group string, localGroups groups, localCache GroupPeers) (bool, string) {
	for _, cidr := range localGroups[group] {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			errf("invalid CIDR %q in group %q: %v", cidr, group, err)
			continue
		}
		if ip != nil && ipNet.Contains(ip) {
			return true, "local CIDR " + cidr
		}
	}
	for _, peerIP := range localCache[group] {
		if peerIP == srcIP {
			return true, "netbird"
		}
	}
	return false, ""
}

// allowedURLsForIP returns, across all registered routes, the AppURLs whose
// allowGroups admit srcIP/ip. Order follows registry iteration; duplicates are
// removed.
func allowedURLsForIP(ip net.IP, srcIP string, localGroups groups, localCache GroupPeers) []string {
	globalRoutesMu.RLock()
	entries := make([]routeEntry, 0, len(globalRoutes))
	for _, e := range globalRoutes {
		entries = append(entries, e)
	}
	globalRoutesMu.RUnlock()

	var urls []string
	seen := make(map[string]bool)
	for _, e := range entries {
		if e.url == "" || seen[e.url] {
			continue
		}
		for _, group := range e.allowGroups {
			if matched, _ := ipInGroup(ip, srcIP, group, localGroups, localCache); matched {
				urls = append(urls, e.url)
				seen[e.url] = true
				break
			}
		}
	}
	return urls
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
	peers, ipUserMap, err := FetchNetbirdPeerGroups(globalURL, globalToken)
	if err != nil {
		errf("fetching peers failed: %v", err)
		return err
	}

	// Fetch user information
	users, err := FetchNetbirdUsers(globalURL, globalToken)
	if err != nil {
		errf("fetching users failed: %v", err)
		return err
	}

	globalCache = peers
	globalIPUserMap = ipUserMap
	globalUserCache = users
	globalLastFetch = time.Now()
	infof("peer cache refreshed: %d groups loaded, %d users", len(peers), len(users))
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
	if err := validateSharedConfig(&cfg, fmt.Sprintf("configPath %q", path)); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func loadSharedFromPluginConfig(cfg *Config) (*fileConfig, string, error) {
	if cfg.InlineConfig != nil {
		shared := &fileConfig{
			Settings: settings{
				NetbirdURL:     cfg.InlineConfig.NetbirdURL,
				Token:          cfg.InlineConfig.Token,
				RefreshSeconds: cfg.InlineConfig.RefreshSeconds,
				LogLevel:       cfg.InlineConfig.LogLevel,
			},
			Groups: cfg.InlineConfig.Groups,
		}
		if err := validateSharedConfig(shared, "inlineConfig"); err != nil {
			return nil, "", err
		}
		return shared, "inlineConfig", nil
	}

	if cfg.ConfigPath == "" {
		return nil, "", fmt.Errorf("configPath or inlineConfig is required")
	}

	shared, err := loadSharedConfig(cfg.ConfigPath)
	if err != nil {
		return nil, "", err
	}
	return shared, fmt.Sprintf("configPath(%s)", cfg.ConfigPath), nil
}

func validateSharedConfig(cfg *fileConfig, source string) error {
	if cfg.Settings.NetbirdURL == "" {
		return fmt.Errorf("%s: NetbirdUrl is required", source)
	}
	if cfg.Settings.Token == "" {
		return fmt.Errorf("%s: Token is required", source)
	}
	if _, err := parseLogLevel(cfg.Settings.LogLevel); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	return nil
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

// addForwardedHeaders adds the forwarded headers when the IP is allowed
func addForwardedHeaders(req *http.Request, srcIP string, localIPUserMap map[string]string, localUserCache map[string]UserInfo, userHeader string, emailHeader string) {

	// Remove any incoming values so they can never reach the backend unverified.
	req.Header.Del(userHeader)
	req.Header.Del(emailHeader)

	
	// Get user ID for this IP
	userID, exists := localIPUserMap[srcIP]
	if !exists {
		return
	}

	// Get user information
	user, userExists := localUserCache[userID]
	if !userExists {
		return
	}

	// Set headers with actual user information
	req.Header.Set(userHeader, user.Name)
	req.Header.Set(emailHeader, user.Email)
}
