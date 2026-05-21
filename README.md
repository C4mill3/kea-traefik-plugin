# Kea Traefik Plugin
An Identity aware middleware for traefik using netbird as a source

Feel free to contrib


Configuration uses only two middleware arguments:

- `configPath`: path to a YAML file, typically mounted as a Docker secret
- `inlineConfig`: optional inline settings object (useful for catalog validation or environments without mounted secrets)
- `allowGroups`: NetBird groups allowed for the route
- `appUrl`: optional URL/domain this route protects. When set, the route is recorded in a global registry so routes with `accessHeaders` enabled can advertise it.
- `accessHeaders`: optional bool (default `false`). When `true`, this route injects the `X-Kea-Allowed-Urls` request header into the backend, listing every registered `appUrl` the caller's IP is allowed to access (comma-separated).

If both are set, `inlineConfig` has priority over `configPath`.

## Access headers (identity-aware homepage)

To let a backend (e.g. a homepage) render only the apps a visitor can reach,
give every protected route an `appUrl`, and enable `accessHeaders` on the route
serving the homepage:

```yaml
labels:
  # each protected app declares its URL
  - "traefik.http.middlewares.kea-sonarr.plugin.kea.configPath=/run/secrets/kea-conf.yml"
  - "traefik.http.middlewares.kea-sonarr.plugin.kea.allowGroups=homelab"
  - "traefik.http.middlewares.kea-sonarr.plugin.kea.appUrl=https://sonarr.example.com"

  # the homepage route gets the computed list as a request header
  - "traefik.http.middlewares.kea-home.plugin.kea.configPath=/run/secrets/kea-conf.yml"
  - "traefik.http.middlewares.kea-home.plugin.kea.allowGroups=homelab, All"
  - "traefik.http.middlewares.kea-home.plugin.kea.accessHeaders=true"
```

The homepage backend then reads `X-Kea-Allowed-Urls` (a comma-separated list of
allowed `appUrl`s) off the incoming request and renders accordingly. The header
is always overwritten on routes with `accessHeaders=true`, so a client cannot
spoof it.


Example dynamic config in traefik file:

```yaml
http:
  middlewares:
    kea-homelab:
      plugin:
        kea:
          configPath: /run/secrets/kea-conf.yml
          allowGroups:
            - homelab
            - All
```

Example dynamic config with `inlineConfig` (no external file):

```yaml
http:
  middlewares:
    kea-homelab:
      plugin:
        kea:
          inlineConfig:
            netbirdUrl: https://api.netbird.io
            token: your-netbird-token
            refreshSeconds: 300
            logLevel: Err # Optional: None, Err, or Info
            groups:
              homelab:
                - "192.168.1.0/24"
                - "172.21.0.27/32"
          allowGroups:
            - homelab
            - All
```

Example label config:
```yaml
labels:
  - "traefik.http.middlewares.kea-homelab.plugin.kea.configPath=/run/secrets/kea-conf.yml"
  - "traefik.http.middlewares.kea-homelab.plugin.kea.allowGroups=homelab, All"
  - "traefik.http.routers.homelab.middlewares=kea-homelab@docker"
```

Example of config file (outside traefik):

```yaml
Settings:
  NetbirdUrl: https://api.netbird.io OR https://netbird.io/api
  Token: your-netbird-token
  RefreshSeconds: 300
  LogLevel: Err # Optional: None, Err, or Info

Groups: #Optional, create custom group and/or add ip to already existing netbird group
  homelab: 
   - "192.168.1.0/24" #Allow request from local network
   - "172.21.0.27/32"
```
