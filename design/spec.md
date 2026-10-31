# CertiFire: build spec

Owner: reviewer. This file is the single spec that backend, frontend, infra and qa build from.
Sources: `design/requirements.md` (incl. "User decisions, round 2"), agentic-config standards
(core, apps, go, frontend, containers, gitlab-ci), `research/self-signed-tls-chrome.md`,
`research/discord-teams-webhooks.md`, passman (concepts only).
Anything not in this spec or the requirements is an extra: propose it to the orchestrator, don't build it.

## 0. Ground rules
- Start from agentic-config `templates/app/` + `templates/app-web/` overlay. Module path `certifire`;
  `<repo-name>`=certifire, `<github-space>`=AceRodnel14, `<dockerhub-user>` stays a placeholder,
  `<model>`=Claude Opus 5.5. Layout `cmd/app` + `internal/`; binary `/app`.
- No `CLAUDE.md`. README ends with `---` + `_Developed with assistance from Claude (Claude Opus 5.5)._`.
- No git remote yet: work locally. After clean review the first commit goes on `ai/initial-app`
  with `AI-ed: ` messages (never `main`). No push, no MR.
- amd64 + arm64 everywhere. Tests and dev tooling never in the image or binary.
- Never put real hostnames, IPs, domains or webhook URLs in code, tests, docs or logs: use
  `app.example.com`, `*.k8s.example.test`, `192.0.2.x`, `2001:db8::x`, fake webhook ids/tokens.

## 1. Versions
| Item | Version |
|---|---|
| Go | 1.26 (template; local go1.26.7). Deps only: `github.com/jackc/pgx/v5`, `golang.org/x/crypto` (argon2), `golang.org/x/term`. stdlib router (`net/http` patterns), stdlib webhooks client. `CGO_ENABLED=0`. |
| Web build stage | `node:24-alpine` (user decision) |
| Web | template: React ^19.3, Vite ^8.3, TypeScript ^7.0, Vitest ^5.0; **add ESLint** (flat config, typescript-eslint, eslint-plugin-react-hooks) and an `npm run lint` script (template gap; required by frontend.md and CI). No router, UI or chip library. |
| Runtime image | pinned current stable `alpine:3.x` (verify amd64+arm64 with `docker buildx imagetools inspect`) + `apk add --no-cache openssl`, non-root user. Deviation from distroless: openssl CLI is required. No psql, no curl/wget. |
| Postgres | `postgres:18-alpine` |

## 2. Configuration (env)
`PORT` (8080), `BASE_URL` (cookies `Secure` when it starts with `https://`), `DATABASE_URL`,
`LOG_LEVEL` (info). Compose-only: `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `IMAGE`,
`APP_BIND`, `APP_PORT`. Nothing else (session TTL, thresholds, reminder time are constants).

## 3. Data model (embedded schema SQL, `CREATE ... IF NOT EXISTS`, applied at startup)
- `meta(key TEXT PK, value BYTEA)`: escrow public key, master salt, escrow private key sealed with the master key.
- `users`: `id BIGSERIAL PK, username TEXT` (unique index on `lower(username)`), `is_admin BOOL`,
  `auth_salt, auth_hash, kek_salt, wrapped_dek, escrow_dek BYTEA`, `theme_mode TEXT DEFAULT 'auto'`,
  `theme_hue SMALLINT NULL`, `created_at, password_changed_at TIMESTAMPTZ`.
- `sessions`: `token_hash BYTEA PK, user_id FK CASCADE, wrapped_dek BYTEA, created_at, expires_at`.
  The DEK is wrapped with a key derived from the cookie token (as passman). TTL constant 12 h.
- `certs`:
  `id UUID PK, user_id FK CASCADE, name TEXT, hosts TEXT[], bits INT, days INT, subject JSONB`
  (`{C,ST,L,O,OU}`, empty keys omitted), `cnf TEXT, cert_pem TEXT, key_enc BYTEA`
  (AES-256-GCM with the user's DEK, AAD `cert-key:<user_id>:<cert_id>`),
  `not_before, not_after, created_at TIMESTAMPTZ`,
  `renewed_by UUID NULL REFERENCES certs(id) ON DELETE SET NULL`.
  Names are **not unique** (renewals reuse the name). Index on `(user_id, created_at)`.
- `webhooks`: `user_id FK CASCADE, kind TEXT CHECK (kind IN ('discord','teams')), url TEXT`
  (normalised, plaintext so the job runs without a logged-in user; documented in README),
  `broken_at TIMESTAMPTZ NULL, broken_reason TEXT NULL` (short, no URL), `updated_at`;
  `PK(user_id, kind)`.
- `reminders_sent`: `cert_id FK CASCADE, kind TEXT, mark TEXT, sent_at`; `PK(cert_id, kind, mark)`.
  `mark` = `"7"|"5"|"3"|"2"|"1"` or `"expired:YYYY-MM-DD"` (UTC).

## 4. API
JSON in/out under `/api`; unknown `/api/*` → JSON 404; SPA at `/`. Errors: `{"error": "<message>"}`.
Auth = session cookie `certifire_session`. Another user's cert id → **404** (never 403).
Mutating requests: JSON bodies only, same-origin check (as passman's `sameOrigin`), no CORS headers.

| Method & path | Auth | Body → response |
|---|---|---|
| `GET /healthz` | none | 200 `ok`, no dependencies (also used by `app healthcheck`) |
| `GET /readyz` | none | 200 if DB ping ok, else 503 |
| `GET /api/info` | none | 200 `{"base_url": "<BASE_URL>"}` (template body, unchanged) |
| `GET /api/state` | none | `{setupDone, user: null \| {username, isAdmin, theme}}` |
| `POST /api/setup` | none | `{password, confirm}` → 201 `{masterKey}` (shown once); 409 if done |
| `POST /api/register` | none | `{username, password, confirm}` → 201 + session; 409 setup not done / name taken |
| `POST /api/login` | none | `{username, password}` → 200 + session; 401 generic; 429 throttled |
| `POST /api/logout` | user | → 204 |
| `POST /api/account/password` | user | `{currentPassword, password, confirm}` → 204. Verifies current, re-wraps the same DEK with the new KEK (escrow unchanged), revokes the user's other sessions, keeps this one. 401 wrong current; throttled like login |
| `PUT /api/account/theme` | user | `{mode: auto\|light\|dark, hue: 0..359\|null}` → 204 |
| `GET /api/account/webhooks` | user | → `{discord: W\|null, teams: W\|null}`, `W = {masked, broken, brokenReason, brokenAt, updatedAt}`. Full URL is never returned |
| `PUT /api/account/webhooks/{kind}` | user | `{url}` → 200 `W`. Validates (section 6), normalises, clears broken status |
| `DELETE /api/account/webhooks/{kind}` | user | → 204 |
| `POST /api/account/webhooks/{kind}/test` | user | no body → 200 `{outcome: delivered\|accepted\|broken\|rate_limited\|error, message, retryAfter?}`. 404 if not configured; 429 if called < 10 s after the user's last test |
| `GET /api/certs` | user | → `[{id, name, hosts, bits, days, createdAt, notAfter, daysLeft, renewedBy}]`, newest first |
| `POST /api/certs` | user | `{name, hosts[], bits?, days?, subject{C?,ST?,L?,O?,OU?}, renewOf?}` → 201 cert. `bits`/`days` omitted, null or 0 → defaults 4096/825; any other out-of-range value → 400. With `renewOf` (Edit & Renew): same as create, then sets the old cert's `renewed_by` in the same transaction; 404 unknown/foreign id, 409 already renewed |
| `POST /api/certs/{id}/renew` | user | no body → 201 new cert with the old one's name, hosts, bits, days, subject; old `renewed_by` = new id. 409 if already renewed |
| `GET /api/certs/{id}` | user | → list fields + `{subject, notBefore, crt, key, cnf}`; `subject` is always an object (`{}` when none was given; Edit & Renew prefills from it); key decrypted with the session DEK. `Cache-Control: no-store` |
| `GET /api/certs/{id}/files/{crt\|key\|cnf}` | user | download, `Content-Disposition: attachment; filename="<name>.<ext>"`, `application/x-pem-file` (crt/key) or `text/plain` (cnf), `no-store` |
| `DELETE /api/certs/{id}` | user | → 204. If it was the renewal of another cert, that one's `renewed_by` becomes NULL (its reminders resume) |
| `GET /api/admin/users` | admin | → `[{id, username, isAdmin, createdAt, certCount}]` |
| `POST /api/admin/users/{id}/password` | admin | `{masterKey, password, confirm}` → 204; revokes that user's sessions; 403 wrong master key (throttled) |

`daysLeft = floor((notAfter - now) / 24h)` (negative once expired). Server and UI use the same rule.

CLI subcommands of `/app`: `healthcheck` (GET `http://127.0.0.1:$PORT/healthz`), and
`reset-password`, which prompts for master key (hidden; **not** argv), username, new password and
confirm, then re-wraps that user's DEK via escrow and revokes their sessions. Usage:
`docker compose exec app /app reset-password`.

## 5. Certificate generation
- Validation (server is authoritative; UI mirrors it):
  - **name** `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`. Used only for the download filename.
  - **hosts** 1..100 entries; trim, lowercase, dedupe (keep first order). Each entry is either
    - an IP: `netip.ParseAddr` ok and no zone → `IP.n` (IPv4 and IPv6, written in canonical form), or
    - a DNS name ≤ 253 chars, labels `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$` → `DNS.n`, or
    - a wildcard: `*` as the **whole leftmost label** plus ≥ 1 valid label after it. `*.homelab`
      (single label after `*`) is **accepted** (user decision); the UI warns that Chrome rejects it.
      Rejected: `*` anywhere else (`a.*.b`, `*foo.b`, `**.b`), bare `*`, underscores, spaces, Unicode/IDN.
  - **CN** = first host; must be ≤ 64 chars (else 400 with a message to reorder the hosts).
  - **bits** ∈ {2048, 3072, 4096} (default 4096). **days** 1..825 (default 825).
  - **subject**: C = exactly 2 letters (uppercased); ST/L/O/OU 1..64 chars from
    `[A-Za-z0-9 .,'()&/+-]`, trimmed; empty = omitted. Blocks `$` (cnf expansion), `#`, `\`, `"`, `;`, `=`, newlines.
- `.cnf`: exactly the template in requirements.md (decided "Chrome-tested root", no nameConstraints),
  `[ dn ]` holds the given subject fields then `CN`. Rendered from validated data only.
- openssl:
  - `exec.CommandContext(ctx, "openssl", "req", "-x509", "-nodes", "-days", D, "-newkey", "rsa:"+B,
    "-keyout", "cert.key", "-out", "cert.crt", "-config", "cert.cnf")`, 60 s timeout. Never a shell.
  - `cmd.Dir` = fresh `os.MkdirTemp` dir (0700), files 0600, fixed internal filenames,
    `defer os.RemoveAll`. Minimal env (`PATH` only; no inherited `OPENSSL_CONF`).
  - At most 2 generations at a time (semaphore); over that → 429 "busy, retry".
  - Afterwards parse with `crypto/x509`: SANs must equal the input, key must match the cert,
    take `not_before`/`not_after` from the cert. On mismatch → 500, nothing stored.
  - Log openssl stderr (it holds no secrets); return a generic error to the client.
- Private key plaintext exists only in the temp file and memory; never logged; stored only as `key_enc`.

## 6. Webhooks (rnd note `research/discord-teams-webhooks.md`, adopted)
- Validation on save **and before every send**: https, no port or `:443`, no userinfo, no fragment,
  length ≤ 2048.
  - **discord**: host `discord.com` (`discordapp.com`, `ptb.discord.com`, `canary.discord.com`
    accepted and rewritten to `discord.com`), path `^/api(/v\d+)?/webhooks/\d{17,20}/[A-Za-z0-9_-]{60,100}$`,
    query empty or only `thread_id=<digits>`.
  - **teams**: host ends with `.environment.api.powerplatform.com` with ≥ 1 label before it, path
    starts with `/powerautomate/`, query contains `sig`.
  - `*.webhook.office.com`, `outlook.office.com`, `*.logic.azure.com` → 400 "retired URL, re-copy it
    from the Teams Workflows app ('Send webhook alerts to a channel', 'Anyone' trigger)". Any other host → 400.
- Client: one shared `http.Client`, 10 s timeout, `CheckRedirect` returns `http.ErrUseLastResponse`,
  response body read capped at 4 KiB. Wrap errors so the URL never reaches logs (Go's `url.Error`
  contains it); log `kind` + `scheme://host/…` + status only.
- Payloads: Discord = one embed (`title`, `description`, `color`), `username: "CertiFire"`,
  `allowed_mentions: {"parse": []}`. Teams = `{"type":"message","attachments":[Adaptive Card 1.4]}`
  with two TextBlocks, no actions. Text: "Certificate `<name>` expires in N day(s)" /
  "expired N day(s) ago", hosts (first 5 + "and N more"), expiry date (UTC).
- Response handling (test and job): 2xx → done (Discord 204 = delivered; Teams 202 = accepted,
  "no delivery confirmation, check the workflow run history if nothing appears").
  429 → honour `Retry-After`/`retry_after`. 5xx / network error → retry later.
  Other 4xx → permanent: set `broken_at`/`broken_reason` (e.g. "Discord 404: webhook deleted"),
  shown in the UI; the job skips broken webhooks until the URL is re-saved or a test succeeds.
- Masked display: `scheme://host/…` + last 4 chars of the URL, e.g. `https://discord.com/…abcd`.

## 7. Reminder job (in-process)
- Runs once at startup (after DB ready), then every hour. Hourly tick + dedupe table = "daily"
  without missing a day across restarts.
- Candidates: certs with `renewed_by IS NULL` whose owner has a non-broken webhook.
  - `daysLeft ∈ {7,5,3,2,1}` → mark = that number. Missed thresholds are not backfilled.
  - `notAfter <= now` → mark = `expired:<UTC date>` (one per day until renewed or deleted).
  - `daysLeft = 0` but not yet expired: nothing.
- Per (cert, kind, mark): claim with `INSERT ... ON CONFLICT DO NOTHING`; send only if the insert
  claimed the row. 5xx/network/429-after-one-wait (cap 60 s) → delete the claim (retry next tick).
  Permanent 4xx → keep the claim, mark the webhook broken. Send sequentially per webhook.
- Uses a background context cancelled on SIGTERM.

## 8. Security baseline (blocking in review)
- Crypto as passman (concepts, rewritten): Argon2id (t=3, m=64 MiB, p=2) for `auth_hash` and the KEK;
  random 32-byte DEK wrapped by the KEK (AES-256-GCM) and escrowed to an X25519 key whose private
  half is sealed with the master-key-derived key; AAD binds every ciphertext to its row; constant-time
  compares; HKDF info strings say `certifire ...`. Master key: 20 chars from the unambiguous alphabet.
- Passwords ≥ 8 chars; usernames `^[A-Za-z0-9._-]{3,32}$` case-insensitive; `admin` is created by setup only.
- Throttling: failed-attempt limiter, key = lowercased username + client IP, where client IP is the
  host part of `r.RemoteAddr` (never `X-Forwarded-For`; behind a proxy or Docker NAT all clients share
  one IP, accepted). 5 failures in a sliding 5 min window lock that key: every further attempt returns
  429 without checking the password, **even a correct one**, until the oldest failure leaves the window.
  Successful attempts don't count. Applies to login, change password and admin reset (the CLI is local,
  not throttled). Argon2 concurrency semaphore; generic auth errors.
- Cookie: HttpOnly, SameSite=Lax, Path=/, `Secure` when `BASE_URL` is https. Never read `X-Forwarded-*`.
- Headers on every response: CSP `default-src 'self'; img-src 'self' data:; style-src 'self';
  frame-ancestors 'none'` (no inline scripts; the build must comply), `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`. Body limit 64 KiB (`http.MaxBytesReader`).
- Never log passwords, master key, private keys, DEKs, session tokens or webhook URLs.

## 9. Operability
- HTTP server starts even when Postgres is down: `/healthz` 200, `/readyz` 503, DB connect + schema
  retried with backoff. This keeps CI's standalone `test_build` smoke run working (no `SMOKE_RUN=false`).
- Graceful shutdown on SIGTERM (stop job, drain HTTP, close pool). slog to stdout.
- Image `HEALTHCHECK CMD ["/app","healthcheck"]` (template timings). No `VOLUME /data` (no app files).
- `docker-compose.yml`: `app` (template shape, `depends_on: [db]` without condition) + `db`
  (`postgres:18-alpine`, named volume `pg-data`, not published on the host). Healthchecks commented
  out; **no** db healthcheck/`service_healthy`. `docker compose up --build` works from a fresh clone
  with `.env` copied from `.env.example` (placeholder `POSTGRES_PASSWORD=change-me`).
- `.gitlab-ci.yml`: only the `templates/app.yml` include.

## 10. UI (frontend)
- Views: Setup (then master key shown once with copy button and "I saved it"), Login / Register tabs,
  Cert list, Create form (also used for Edit & Renew, prefilled, title "Renew <name>"), Cert detail,
  Account (theme, change password, webhooks), Admin (users list + reset with master key; admin only).
- Logo: the user-approved SVG `design/logo.svg` as `web/src/components/Logo.tsx` (lock in `currentColor`, flame gradient) and `web/public/favicon.svg` (light/dark via its own `prefers-color-scheme` style). No PNG logo. Theme: auto/light/dark + hue slider
  (0..359, or default) driving CSS custom properties for accent and background tints, saved via
  `PUT /api/account/theme`, applied before first paint from `/api/state`.
- Accent colouring (user request, like passman): the hue also colours the top bar (accent gradient,
  white text and icons with readable contrast), modal/dialog headers and the login/setup page background.
  Implemented with derived CSS custom properties (`--accent-glow`, `--accent-hi`, `--accent-lo`) for light
  and dark, set through the CSSOM like the other theme vars; the default (no hue) uses the flame orange.
- Info tooltips (user request) on the create/Edit & Renew form fields (name, hosts, RSA size, days,
  each subject field): an ⓘ button next to the label explaining the field with examples
  (e.g. hosts: `app.example.com`, `*.k8s.example.test`, `192.0.2.10`; days: max 825, Apple's limit).
  Opens on hover, keyboard focus and tap/click; closes on Escape, blur and outside tap; linked with
  `aria-describedby` (role `tooltip`). Own component, no library, no inline styles or scripts (CSP).
- Chip input: Enter (and comma/paste of several) adds, × or Backspace on empty input removes; invalid
  entries stay in the input with an inline error; chips for `*.<single-label>` show the Chrome warning.
- List: name, hosts (first few + "+N"), bits, created, expires, days left (colour at ≤ 7 and expired),
  "renewed" badge linking to the newer cert. Actions: open, Renew, Edit & Renew, delete (confirm dialog).
- Detail: tabs/sections for `.crt`, `.key`, `.cnf` in monospace with Copy and Download buttons.
- Webhooks: per kind a URL field (masked when saved), Save, Remove, Send test (shows the outcome
  message; Teams explains 202), broken badge with reason.
- Every request shows loading and error states. One typed client `web/src/api.ts` matching section 4.

## 11. File ownership (one owner per file)
| Owner | Files |
|---|---|
| reviewer | `design/spec.md` |
| backend | `go.mod`, `go.sum`, `cmd/app/**` (incl. `web.go`), `internal/**` (incl. `_test.go`), `.golangci.yml` |
| frontend | `web/**` (incl. `web/embed.go`, `package.json`, `package-lock.json`, ESLint config, `public/`) |
| infra | `Dockerfile`, `docker-compose.yml`, `.dockerignore`, `.env.example`, `.gitignore`, `.gitlab-ci.yml`, `README.md` (env table from backend), `dockerdesc.md` |
| user | `design/requirements.md`, `design/*.png` (nobody edits) |

`.dockerignore` excludes `.git`, `design/`, `dockerdesc.md`, `.env*`, `web/node_modules`, `web/dist`, `**/testdata`. (`_test.go` files never reach the binary anyway.)

## 12. Validation before hand-off
- backend: `gofmt -l .` (empty), `go vet ./...`, `go test ./...`, `GOARCH=arm64 go build ./...`,
  `golangci-lint run` (install on trusted WSL or run the pinned `golangci/golangci-lint` image).
  DB tests read `TEST_DATABASE_URL` and skip if unset; run them against
  `docker compose -p certifire-test up -d db` (no fixed host port), then `docker compose -p certifire-test down -v`.
  Table tests for every validation rule (incl. injection strings), the cnf renderer, openssl generation,
  webhook URL validation and response handling (`httptest`), reminder marks, crypto round trips, `/healthz`.
- frontend: `npm --prefix web ci`, `npm --prefix web run lint`, `npm --prefix web run typecheck`,
  `npm --prefix web test`, `npm --prefix web run build` (check bundle size). Vitest for the chip input
  validation and the API client.
- infra: `hadolint Dockerfile` (or `docker run --rm -i hadolint/hadolint < Dockerfile`),
  `docker build --check .`, `docker compose config`,
  `docker buildx build --platform linux/amd64,linux/arm64 .` (no push),
  `docker run --rm --platform linux/arm64 --entrypoint openssl <image> version`,
  smoke: `docker compose -p certifire-test up -d --build`, `/healthz` 200, `/readyz` 200, then `down -v`.

## 13. QA feature list
1. Fresh DB: `/` shows Setup; setup creates `admin`, shows the master key once; second setup → 409.
2. Login ok / generic error; 6th failure within 5 min → 429; logout; session survives app restart.
3. Register: valid username works; invalid or duplicate (case-insensitive) rejected.
4. Change own password: wrong current → 401; success → old password fails, new works, other sessions
   signed out, current stays, existing keys still decrypt.
5. Admin: user list; reset with master key → user logs in with the new password and keys still
   decrypt; wrong master key rejected; non-admin gets 403/404 on admin endpoints.
6. CLI `reset-password`: same outcomes; master key not echoed, not accepted as argv.
7. Create with defaults (825 d, 4096) and with 2048/3072, custom days. `openssl x509 -text` shows
   SANs (DNS, wildcard, IPv4, IPv6), CN = first host, `CA:TRUE, pathlen:0` critical, keyUsage/EKU as
   the template, SKI, correct notAfter; key matches cert (equal pubkey hashes).
8. `*.homelab` accepted by the server, UI shows the Chrome warning; `*.k8s.homelab` no warning.
9. Rejected: no hosts, `a.*.b`, `*foo.b`, `*`, `foo_bar`, label > 63, host > 253, CN > 64, spaces,
   `$`, `;`, newline, `../` in name/hosts/subject, bits 1024/8192, days 0/826, C not 2 letters,
   > 100 hosts. Duplicates deduped. Name with `/` or leading `.` rejected.
10. Chip input: Enter adds, × removes, Backspace removes last, invalid entry shows an inline error.
11. List: name, hosts, size, created, expires, days left match the cert's notAfter.
12. Detail: view, copy and download `.crt`/`.key`/`.cnf`; filenames `<name>.crt|key|cnf`; .cnf
    matches the template with empty subject fields omitted.
13. Renew: new cert with same name/hosts/bits/days/subject and a new key; old one marked "renewed"
    with link; renewing it again → 409.
14. Edit & Renew: form prefilled; edited values used; old marked renewed.
15. Delete: removes the cert; deleting a renewal un-marks the old cert.
16. Isolation: user B gets 404 on all cert endpoints for user A's ids and can't see A's list.
17. DB at rest: `key_enc` has no `PRIVATE KEY` text; private keys never appear in logs.
18. Webhooks: save/remove; masked display; full URL never returned by the API; `discordapp.com`
    rewritten to `discord.com`; rejected: http, look-alike host, userinfo, port 8443, extra path,
    unexpected query, IP literal, Teams without `sig`, `logic.azure.com`, `webhook.office.com`.
19. Send test (QA uses a local stub, not real webhooks, or the backend's `httptest`): delivered /
    accepted (Teams message) / 404 → broken badge with reason / 429 → retry info; second test
    within 10 s → 429. Re-saving the URL clears broken.
20. Reminders (QA sets `not_after` in the DB, stub endpoint): one message each at 7, 5, 3, 2, 1
    days; no resend after restart; expired → one per UTC day; renewed or deleted certs get none;
    5xx → retried next tick; permanent 4xx → broken, no further sends; both kinds when both configured.
    Real Discord/Teams delivery: user check with their own URLs (Teams URL pattern must be verified
    against one real Workflows URL before release).
21. Theme: auto/light/dark + hue changes background tints; persists per user across logins.
22. Ops: `/healthz` 200 and `/readyz` 503 with DB stopped; app recovers when DB returns; container
    healthy; image runs on amd64 and arm64 with openssl; `docker compose up --build` from a fresh copy.
23. Info tooltips on every create-form field: open on hover, Tab focus and tap; close on Escape, blur
    and outside tap; screen-reader text via `aria-describedby`; no CSP violations in the console.
24. Accent hue colours the top bar (white text readable), dialog headers and the login/setup background
    in light and dark; changing the hue updates them live; default hue = flame orange.
