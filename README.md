# CertiFire

CertiFire is a small self-hosted web app that creates self-signed SSL certificates for
homelab apps. Pick a name, the hosts (DNS names, wildcards, IPv4/IPv6 addresses), the RSA
size and the validity, and it runs the `openssl` CLI for you and keeps the `.crt`, `.key`
and `.cnf` for download. Each user only sees their own certificates, renewing is one click,
and Discord or Microsoft Teams webhooks warn you before a certificate expires.

## Self-hosting

### Requirements
- Docker with the Compose plugin, on `linux/amd64` or `linux/arm64`.
- Optional: a reverse proxy in front for HTTPS. The app itself only serves plain HTTP
  on port `8080`; set `BASE_URL` to the public URL (for example `https://app.example.com`).
- PostgreSQL is part of the Compose stack (`postgres:18.6-alpine3.24`); no separate setup.

### Quick start
> **Create `.env` from `.env.example` and fill it in before the first start.** Secrets are
> empty in the example: set `POSTGRES_PASSWORD` (letters and digits; Compose refuses to start
> without it, and it is fixed once the volume exists) and `BASE_URL`.

```bash
mkdir certifire && cd certifire
curl -fsSLO https://raw.githubusercontent.com/AceRodnel14/certifire/main/docker-compose.yml
curl -fsSL -o .env https://raw.githubusercontent.com/AceRodnel14/certifire/main/.env.example
# edit .env now: set POSTGRES_PASSWORD and BASE_URL
docker compose pull
docker compose up -d
```
Open `http://<host>:8080`. Check health with `curl -f http://<host>:8080/healthz`.
To build from source instead, see [Development](#development).

### First run: admin account and master key
1. On a fresh database the page shows **Setup**. Choose a password for the `admin` user.
2. CertiFire then shows the **master key once**. Copy it and store it somewhere safe
   (a password manager). It is never shown again and cannot be recovered.
3. Other people sign up with the **Register** tab.

The master key can reset any user's password without losing their stored private keys. It
is asked for in the admin screen (the Users page) and by the command below.

### Reset a password
```bash
docker compose exec app /app reset-password
```
It prompts (hidden input, never as a command-line argument) for the master key, the username,
the new password and its confirmation. The user's other sessions are signed out and their
certificates stay readable.

### Expiry reminders (webhooks)
Each user can set a Discord and/or a Microsoft Teams webhook under **Account**. Once a day
CertiFire checks every user's certificates by **calendar date** in `REMINDER_TZ` (default
`UTC`) and sends one message per step: by default when a certificate expires in 7, 5, 3 and
1 day(s) and on the expiry day itself (`REMINDER_STEPS=7,5,3,1,0`), then one per day once
the expiry date has passed, until it is renewed or deleted. Renewed certificates get no
reminders. Use **Send test** to try a webhook.

**When the check runs**
- **Always-on setups (Docker / Compose)**: keep `REMINDER_LOCAL_CRON=true` (default). CertiFire
  checks at startup, every hour and at `REMINDER_TIME`, and sends a day's reminders from `REMINDER_TIME` (in
  `REMINDER_TZ`, default `00:00`) on. The hourly checks retry failed sends and catch up after a
  restart the same day; a step missed for a whole day is skipped, not sent late. Example for
  8 AM Philippine time: `REMINDER_TZ=Asia/Manila`, `REMINDER_TIME=08:00`.
- **Scale-to-zero (Kubernetes + KEDA)**: set `REMINDER_LOCAL_CRON=false` on the app and run
  the check from a Kubernetes CronJob instead: see [`k8s_cron/`](k8s_cron/README.md). The
  CronJob runs the same image with `remind`, which checks once and exits (non-zero if a send
  failed, so the Job retries it the same day). The web app stays scaled to zero.
- **Cron on a plain Docker host**: `docker compose run --rm app remind` does one check (use it
  with `REMINDER_LOCAL_CRON=false` if you schedule it yourself).

Sending twice is safe: each reminder is recorded, so overlapping checks (several replicas, a
CronJob plus the app, a manual run) send it once.

- **Discord**: Server Settings, Integrations, Webhooks, New Webhook, Copy Webhook URL
  (`https://discord.com/api/webhooks/<id>/<token>`). A successful send is *delivered*.
- **Microsoft Teams** (best-effort: Discord is the supported and tested path for now): in the
  Teams Workflows app use the template **Send webhook alerts
  to a channel**, trigger "Anyone", and copy the HTTP POST URL it gives you. The old
  Office 365 connector URLs (`*.webhook.office.com`) are retired and are rejected. Teams
  answers *accepted* (HTTP 202), which is not the same as *delivered*: there is no delivery
  confirmation, so if nothing shows up in the channel check the workflow's run history.

### Security notes
- Registration is open by design: anyone who can reach the app can create an account. Keep it on a trusted network or behind your proxy's access control.
- You stay signed in for 14 days from login (fixed, not extended by use) on a device you trust;
  logging out or a password change/reset ends it sooner. The app runs the `openssl` CLI as a child process (the image includes it).
- Private keys are encrypted at rest with a per-user key that is wrapped by the user's
  login password (Argon2id), with an escrow copy that only the master key can open.
- Certificates (`.crt`), `.cnf` files, host lists, expiry dates and **webhook URLs are
  stored in plaintext** in the database. That is deliberate: the reminder job must run
  without anyone being logged in. A webhook URL is a secret, so protect the database and
  its backups, and never put the URL in the screenshots or issues. The app never returns
  the full URL (only a masked form).
- **`POST /api/backup-all` exports every private key over the network.** Anyone who has the
  master key **and** can reach CertiFire from an address in `BACKUP_ALL_ALLOWED_IPS` can download
  every user's certificates and decrypted keys (inside an AES zip whose password they choose). It is
  off unless that variable is set; keep the list as tight as possible, or leave it unset.
- Generated certificates are CA-capable (`CA:TRUE, pathlen:0`) so browsers accept them as
  a trusted root once imported. Import them only where you intend to trust them.

### Configuration
<!-- ENV-TABLE-START -->
| Variable | Default | Description |
| :- | :- | :- |
| `PORT` | `8080` | HTTP listen port |
| `BASE_URL` | `http://localhost:$PORT` | Public URL; cookies get `Secure` when it starts with `https://` |
| `DATABASE_URL` | none | Postgres DSN, e.g. `postgres://<user>:<password>@db:5432/certifire?sslmode=disable` (Compose builds it). Empty or unreachable: the app still starts, `/healthz` is 200, `/readyz` is 503, and it retries with backoff |
| `LOG_LEVEL` | `info` | `slog` level: `debug`, `info`, `warn`, `error` |
| `BACKUP_ALL_ALLOWED_IPS` | empty (disabled) | Comma-separated IPs or CIDRs (a bare IP is one host) allowed to call `POST /api/backup-all`, checked against the direct TCP peer. Empty disables the endpoint; an invalid entry stops startup |
| `REMINDER_TZ` | `UTC` | IANA time zone for reminder dates and `REMINDER_TIME`, e.g. `Asia/Manila`. An unknown zone stops startup |
| `REMINDER_TIME` | `00:00` | `HH:MM` (24 h, in `REMINDER_TZ`) from which a day's reminders go out; used only with `REMINDER_LOCAL_CRON=true` |
| `REMINDER_STEPS` | `7,5,3,1,0` | Days before the expiry date to send a reminder (`0` = the expiry day); integers 0-365, no duplicates. After the expiry date one reminder goes out per day |
| `REMINDER_LOCAL_CRON` | `true` | `true`: the app checks hourly by itself. `false`: no in-app checks; run `remind` from a Kubernetes CronJob or host cron |
<!-- ENV-TABLE-END -->

Compose-only variables (read by `docker-compose.yml`, not by the app):

| Variable | Default | Description |
| :- | :- | :- |
| `POSTGRES_USER` | `certifire` | Database user |
| `POSTGRES_PASSWORD` | none (required) | Database password; letters and digits only, it is embedded in `DATABASE_URL`. Required: Compose refuses to start without one (`set POSTGRES_PASSWORD`). Set it before the first start |
| `POSTGRES_DB` | `certifire` | Database name |
| `IMAGE` | `eacer/certifire:latest` | Image (and tag) Compose runs or builds |
| `APP_BIND` / `APP_PORT` | `0.0.0.0` / `8080` | Host address and port to publish on |

### Data and backups
All data lives in PostgreSQL, in the `pg-data` volume; the app container stores no files.
Back up the database (this holds the encrypted keys, so keep the dump private):
```bash
docker compose exec db sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' > certifire-backup.sql
```
Restore into an empty database with `docker compose exec -T db sh -c 'psql -U "$POSTGRES_USER" "$POSTGRES_DB"' < certifire-backup.sql`.
Keep the master key separately: without it, lost passwords cannot be reset.

### Backup API (all users)
`POST /api/backup-all` returns every user's certificates for unattended backups. There is no
button for it in the UI. It ignores cookies and is authenticated with the master key in a JSON body
(send `Content-Type: application/json`):
```bash
curl -fsS -X POST http://<host>:8080/api/backup-all \
  -H 'Content-Type: application/json' \
  -d "{\"masterKey\":\"$MASTER_KEY\",\"backupPassword\":\"$BACKUP_PASSWORD\"}" \
  -o certifire-backup-all.zip
```
- `backupPassword` must be at least 16 characters (400 otherwise). The response is always an
  AES-256 zip (`certifire-backup-all_YYYY-MM-DD.zip`), never plain data.
- Layout: the AES zip holds one entry, `certifire-all.zip` (a plain zip, so usernames and sizes stay
  hidden), which holds one `<username>.zip` per user (empty if they have no certificates). Each
  `<username>.zip` holds `<name>.cnf`, `<name>.crt` and `<name>.key` (decrypted PEM) per certificate.
  A renewal keeps its name, so older certificates with the same name are `<name>_<certificate-id>.*`.
- Unpack (`unzip` can't open AES zips; 7-Zip can):
  ```bash
  7z x -p'<backup password>' certifire-backup-all_*.zip
  unzip certifire-all.zip
  unzip <username>.zip
  ```
- It is disabled (404 for everyone) unless `BACKUP_ALL_ALLOWED_IPS` is set. It is for **direct
  connections only**: the client address is the TCP peer, forwarding headers are never trusted, and a
  request carrying `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Real-Ip` or
  `Forwarded` gets 404, so nothing that came through a reverse proxy can use it. Clients outside the
  list and non-POST methods also get 404.
- A wrong master key gives 401. After 5 failures in 15 minutes per client IP, or 5 from all clients
  together, it answers 429 for the rest of the window (anyone inside the allowed range can block
  backups for 15 minutes this way; a restart clears it). One backup runs at a time (429 "Busy, try
  again." otherwise). Every attempt is logged with its result and client IP, never the keys.

Deployment notes:
- **Never allow a proxy.** Don't put a reverse proxy or TCP proxy's address inside
  `BACKUP_ALL_ALLOWED_IPS`: every client behind it would look like an allowed address.
- **Docker:** publish CertiFire on loopback (`APP_BIND=127.0.0.1`) and allow the Docker network,
  e.g. `BACKUP_ALL_ALLOWED_IPS=172.16.0.0/12`. Traffic to a published port can reach the container
  from the network gateway, not the original client, so the loopback binding (not the allow-list)
  is what keeps the LAN out.
- **Kubernetes:** allow the pod CIDR and call the Service directly from a CronJob, never through the
  Ingress (it adds `X-Forwarded-*`, so those requests are refused). Keep the master key in a Secret
  and use a NetworkPolicy to limit who can reach the Service.

### Upgrading
`latest` is the latest stable image and what Compose uses by default; `main` follows the default branch. Release tags
`vX.Y` and `vX.Y.Z` are pushed when a release is tagged: pin `IMAGE` to `vX.Y` for patch
updates only, then `docker compose pull && docker compose up -d`. The database schema is
applied by the app at startup.

### Health endpoints
- `/healthz`: the process is up (liveness); does not touch the database.
- `/readyz`: the database is reachable (readiness); 503 while it is down.

The app starts even when PostgreSQL is not up yet and keeps retrying the connection.

## Development
```bash
cp .env.example .env         # then set POSTGRES_PASSWORD in .env
docker compose up --build   # the whole app, as self-hosters run it
go test ./...
go run ./cmd/app            # backend on :8080 (needs DATABASE_URL)
```
`./manual_cicd_lint.sh` runs the CI lint/test jobs locally in throwaway containers (`--list`, `--only`, `--skip`).
Web UI (`web/`): `npm --prefix web install`, then `npm --prefix web run dev`
(UI on :5173, API proxied to :8080) next to `go run ./cmd/app`; `npm --prefix web test`.
`go run`/`go test` need `web/dist`: run `npm --prefix web run build` once, or create
an empty `web/dist/.keep`. Database tests read `TEST_DATABASE_URL` and skip when it is unset.
CI (gitlab-actions `templates/app.yml`): lint and tests on branches and merge requests;
on the default branch and release tags a test build with a smoke run, the multi-arch
image push to Docker Hub and the GitHub sync.

---
_Developed with assistance from Claude (Claude Opus 5.5, Claude Sonnet 5.5)._
