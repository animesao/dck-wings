# dck-wings — Container Management Agent

**dck-wings** is a lightweight REST API daemon for managing containers via [dck](https://github.com/animesao/dck). It runs as a systemd service on your VDS and provides a HTTP API that frontends (like dck-panel) use to create, start, stop, and monitor containers.

## Quick Start

```bash
bash <(curl -sfL https://raw.githubusercontent.com/animesao/dck-wings/main/install.sh)
```

Or manually:

```bash
# Download
curl -sfL https://github.com/animesao/dck-wings/releases/latest/download/dck-wings-linux-amd64 -o /usr/local/bin/dck-wings
chmod +x /usr/local/bin/dck-wings

# Install as service
dck-wings --install

# Edit config
nano /etc/dck-wings/config.toml

# Start
systemctl enable --now dck-wings
```

## API

All requests require `Authorization: Bearer <api_key>` header or `?api_key=` query param.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/health` | Health check |
| GET | `/api/containers` | List containers (`?all=1` for stopped) |
| POST | `/api/containers` | Create container |
| POST | `/api/containers/:id/start` | Start container |
| POST | `/api/containers/:id/stop` | Stop container |
| POST | `/api/containers/:id/restart` | Restart container |
| DELETE | `/api/containers/:id` | Remove container (`?force=1`) |
| GET | `/api/containers/:id/logs` | Get logs (`?follow=1&tail=50`) |
| GET | `/api/containers/:id/stats` | Live resource stats |
| GET | `/api/containers/:id/state` | Container state |
| POST | `/api/containers/:id/exec` | Execute command (`{"cmd":["sh","-c","..."]}`) |
| GET/WS | `/api/containers/:id/console` | WebSocket interactive console |
| GET/POST/DELETE | `/api/containers/:id/files/*` | File manager operations |
| GET/POST/DELETE | `/api/images*` | Image management |

### Create Container

```json
POST /api/containers
{
  "image": "nginx:alpine",
  "name": "web",
  "ports": ["80:80"],
  "volumes": ["./html:/usr/share/nginx/html"],
  "env": ["FOO=bar"],
  "detach": true,
  "memory": "512m",
  "cpus": 1.0,
  "startup_script": "#!/bin/sh\necho 'Custom startup'\n# your commands here"
}
```

## Configuration

```toml
# /etc/dck-wings/config.toml
port = 8080
host = "0.0.0.0"
api_key = "your-secret-key"
dck_bin = "/usr/local/bin/dck"
data_dir = "/var/lib/dck-wings"
log_dir = "/var/log/dck-wings"
```

## Systemd

```bash
systemctl status dck-wings
journalctl -u dck-wings -f
```

## Integration with dck-panel

Add to dck-panel `.env`:

```
DECK_WINGS_API_KEY=your-key
DECK_WINGS_URL=http://<vds-ip>:8080
```
