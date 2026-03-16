# Kea Traefik Plugin
An Identity aware middleware for traefik using netbird as a source

Feel free to contrib


Configuration uses only two middleware arguments:

- `configPath`: path to a YAML file, typically mounted as a Docker secret
- `inlineConfig`: optional inline settings object (useful for catalog validation or environments without mounted secrets)
- `allowGroups`: NetBird groups allowed for the route

If both are set, `inlineConfig` has priority over `configPath`.


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
