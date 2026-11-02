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
    image: eacer/certifire:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    env_file: .env
    environment:
      DATABASE_URL: postgres://certifire:${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD}@db:5432/certifire?sslmode=disable
    depends_on: [db]
  db:
    image: postgres:18.6-alpine3.24
    restart: unless-stopped
    environment:
      POSTGRES_USER: certifire
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD}
      POSTGRES_DB: certifire
    volumes: ["pg-data:/var/lib/postgresql"]
volumes:
  pg-data:
```
**Before the first start**, create `.env` and set `POSTGRES_PASSWORD` (required; letters and digits) and `BASE_URL`;
then `docker compose up -d`, then open `http://<host>:8080`. The first visit creates the
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
| `REMINDER_TZ` | `UTC` | Time zone for the daily reminder check |
| `REMINDER_TIME` | `00:00` | Time of the check, `HH:MM` in `REMINDER_TZ` |
| `REMINDER_STEPS` | `7,5,3,1,0` | Days before expiry on which a reminder is sent |
| `REMINDER_LOCAL_CRON` | `true` | Set `false` to disable the built-in timer and run `/app remind` from a Kubernetes CronJob |

Data lives in PostgreSQL (the `pg-data` volume); the app container stores no files.
Health: `/healthz` (liveness), `/readyz` (database reachable).

## Tags
`latest` (default, latest stable), `main` (default branch), `vX.Y`, `vX.Y.Z` (releases). Platforms: `linux/amd64`, `linux/arm64`.
