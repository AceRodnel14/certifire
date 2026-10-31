> Source code, full documentation and issues: https://github.com/AceRodnel14/certifire

# CertiFire

A small self-hosted web app that creates self-signed SSL certificates for homelab apps.
Choose a name, hosts (DNS names, wildcards, IPs), RSA size and validity; CertiFire runs
`openssl` and keeps the `.crt`, `.key` and `.cnf` for download. Per-user accounts, one-click
renewal, and Discord or Microsoft Teams webhook reminders before certificates expire.
Private keys are encrypted; certificates and webhook URLs are stored in plaintext.

## Quick start
```yaml
services:
  app:
    image: <dockerhub-user>/certifire:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    environment:
      BASE_URL: http://localhost:8080
      DATABASE_URL: postgres://certifire:<password>@db:5432/certifire?sslmode=disable
    depends_on: [db]
  db:
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: certifire
      POSTGRES_PASSWORD: <password>
      POSTGRES_DB: certifire
    volumes: ["pg-data:/var/lib/postgresql"]
volumes:
  pg-data:
```
`docker compose up -d`, then open `http://<host>:8080`. The first visit creates the
`admin` user and shows the master key once: save it. Reset a password with
`docker compose exec app /app reset-password`. For HTTPS, put a reverse proxy in front
and set `BASE_URL` to the public URL.

## Configuration
| Variable | Default | Description |
| :- | :- | :- |
| `PORT` | `8080` | HTTP port inside the container |
| `BASE_URL` | `http://localhost:8080` | Public URL as users see it |
| `DATABASE_URL` | none | PostgreSQL connection URL |
| `LOG_LEVEL` | `info` | Log level |
| `BACKUP_ALL_ALLOWED_IPS` | empty (off) | IPs/CIDRs allowed to call `POST /api/backup-all` directly (master key); see the full docs |

Data lives in PostgreSQL (the `pg-data` volume); the app container stores no files.
Health: `/healthz` (liveness), `/readyz` (database reachable).

## Tags
`latest`, `vX.Y`, `vX.Y.Z`. Platforms: `linux/amd64`, `linux/arm64`.
