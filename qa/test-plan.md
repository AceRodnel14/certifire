# CertiFire QA test plan

Owner: qa. Source: `design/spec.md` section 13 (feature numbers F1..F22 below), requirements.md.
Run in an isolated stack: `qa/run.sh` (project `certifire-qa`, torn down with `down -v` on exit).
Evidence goes to the run's `$OUT` folder (under the session scratchpad). Everything below uses
placeholders only: `app.example.com`, `192.0.2.x`, `2001:db8::x`, fake webhook ids/tokens.

## 0. Environment and tooling
- Stack: the app's own `docker-compose.yml` + `.env.example` copied into a fresh run folder,
  plus `qa/compose.qa.yml` (override, QA only, never shipped): adds a `stub` service (TLS webhook stub)
  with network aliases `discord.com` and `qa.environment.api.powerplatform.com`, and mounts the
  stub CA into `app` via `SSL_CERT_FILE`. This lets the real webhook validation (https, host
  allow-list, no port) pass while traffic stays local. App code is not modified.
- App base URL: `docker compose port app 8080` (never a fixed host port). DB is not published;
  DB access via `docker compose exec -T db psql`.
- Tools: curl, jq, openssl (host), python3 (stub), Playwright via `uv run --with playwright`.
- Pass criteria: every case below passes, or a finding is filed (steps, expected, actual, owner).
- Note on `Secure` cookie: `BASE_URL` in `.env.example` is http, so cookies are not Secure (check once with `BASE_URL=https://...` variant: cookie has `Secure`; curl cookie jar then needs `-k`/https, so check the Set-Cookie header only).

## 1. API cases (scripts: `qa/helpers/cases_0*.sh`, driven by `qa/run.sh`)
Notation: `A`=admin session jar, `U1`,`U2`=user jars, `H`=headers.

### F1 Setup
| ID | Steps | Expected |
|---|---|---|
| S1 | `GET /api/state` fresh DB | `setupDone:false`, `user:null`; `GET /` returns HTML |
| S2 | `POST /api/setup` password<8 / mismatched confirm | 400 JSON error, setup still not done |
| S3 | `POST /api/setup` valid | 201 `{masterKey}` 20 chars, unambiguous alphabet (no 0/O/1/l/I) |
| S4 | Second `POST /api/setup` | 409 |
| S5 | `POST /api/register` before setup (separate fresh DB, or checked before S3) | 409 |
| S6 | `GET /api/state` after | `setupDone:true`; master key never returned again by any endpoint (grep all responses) |
| S7 | Register username `admin` / `ADMIN` after setup | rejected (name taken) |

### F2 Login, throttle, logout, restart
| ID | Steps | Expected |
|---|---|---|
| L1 | Login valid | 200, `Set-Cookie: certifire_session` HttpOnly, SameSite=Lax, Path=/ ; no Secure on http BASE_URL |
| L2 | Wrong password vs unknown user | same status and same body (generic 401) |
| L3 | 6 failures for one username within 5 min | attempts 1-5 = 401, 6th = 429; correct password during lock also 429; a different username unaffected |
| L4 | `POST /api/logout`, reuse old cookie | 204; then `/api/state` shows `user:null`; protected endpoint 401 |
| L5 | Login, `docker compose restart app`, reuse cookie | still logged in; certs still downloadable (DEK recovered from session) |
| L6 | Case-insensitive login (`ADMIN`) | works if usernames are case-insensitive per spec |
| L7 | Malformed JSON, wrong content-type, body > 64 KiB | 4xx/413, no 500, no panic in logs |
| L8 | Mutating request with foreign `Origin` header | rejected (sameOrigin); same-origin accepted; no `Access-Control-*` headers on any response |

### F3 Register
| ID | Steps | Expected |
|---|---|---|
| R1 | Valid user `alice`, `bob` | 201 + session |
| R2 | `Alice` after `alice` | 409 |
| R3 | Invalid: `ab`, 33 chars, space, `a/b`, `a;b`, unicode, empty, `<script>` | 400 |
| R4 | Password 7 chars; confirm mismatch | 400 |
| R5 | Newly registered user is not admin; `GET /api/admin/users` -> 403 or 404 |

### F4 Change password
| ID | Steps | Expected |
|---|---|---|
| P1 | Wrong `currentPassword` | 401; repeated 5x then 429 |
| P2 | Pre: user has cert C and two sessions (S_a, S_b). Change via S_a | 204 |
| P3 | Old password login | 401 |
| P4 | New password login | 200 |
| P5 | S_b after change | 401 (signed out); S_a still valid |
| P6 | Download C key via new login, compare pubkey with cert | still decrypts, match |
| P7 | Short / mismatched new password | 400 |

### F5 Admin
| ID | Steps | Expected |
|---|---|---|
| AD1 | `GET /api/admin/users` as admin | list with id, username, isAdmin, createdAt, certCount (counts correct) |
| AD2 | Same as user / anonymous | 403/404 / 401 |
| AD3 | `POST /api/admin/users/{id}/password` with correct master key | 204; user old password fails, new works; user's old sessions revoked; user's cert key still decrypts and matches |
| AD4 | Wrong master key | 403; 5 wrong then throttled (429) |
| AD5 | Non-admin posts reset (even own id, even with right master key) | 403/404 |
| AD6 | Reset non-existent id | 404 |
| AD7 | Admin resets own password via admin endpoint | works; admin certs still decrypt |

### F6 CLI reset-password
| ID | Steps | Expected |
|---|---|---|
| C1 | `docker compose exec -T app /app reset-password extra-arg` and with master key as argv | refused / not accepted |
| C2 | Interactive via pty (`script`/python pty helper `qa/helpers/cli_reset.py`): master key, username, password, confirm | prompts hidden (typed secrets not in captured output), exit 0 |
| C3 | After C2: login with new password, certs decrypt, old sessions revoked | pass |
| C4 | Wrong master key / unknown user / mismatch | non-zero exit, DB unchanged |
| C5 | `docker compose logs app` | contains no master key or passwords |

### F7, F8, F9 Create and validation
Helper `qa/helpers/certcheck.sh <id>`: downloads crt/key/cnf, runs `openssl x509 -noout -text`.
| ID | Steps | Expected |
|---|---|---|
| G1 | Defaults (omit bits/days if allowed, else 4096/825), hosts `app.example.com`, `*.k8s.example.test`, `192.0.2.10`, `2001:db8::10` | 201 |
| G1a | `openssl x509 -text` | SAN has DNS:app.example.com, DNS:*.k8s.example.test, IP:192.0.2.10, IP:2001:DB8:0:0:0:0:0:10; CN = first host |
| G1b | Extensions | `Basic Constraints: critical CA:TRUE, pathlen:0`; KU critical Digital Signature, Key Encipherment, Certificate Sign; EKU TLS Web Server Authentication; SKI present |
| G1c | Validity | notAfter - notBefore ~ days; `Public-Key: (4096 bit)`; sig sha256WithRSAEncryption |
| G1d | Key/cert match | `openssl x509 -pubkey -noout \| sha256sum` equals `openssl pkey -pubout \| sha256sum`; `openssl verify -CAfile crt crt` succeeds |
| G2 | bits 2048, 3072; days 1, 90, 825 | each correct in `-text` |
| G3 | IPv6 given in non-canonical form (`2001:DB8::0010`) | stored canonical, no dup |
| G4 | Optional subject C,ST,L,O,OU all set / none set | subject in cert matches; empty omitted from `.cnf` and cert; C lowercase `us` -> `US` |
| G5 | `.cnf` equals the requirements template with only given fields; DNS.n/IP.n numbering sequential | compare with the requirements template by eye plus the alt_names diff in cases_01 |
| G6 | Host input trimmed, uppercased, duplicates | lowercased, deduped keeping first order |
| G7 | `*.homelab` | 201 (server accepts) ; `*.k8s.homelab` 201 |
| G8 | Concurrency: 4 simultaneous 4096 creates | at most 2 run, others 429 "busy"; no 5xx, no leftover temp dirs in the container (`ls /tmp`) |
| G9 | Names reusable: create twice with same name | both 201 (non-unique) |

Rejections (each must be 400 with `{"error":...}`, no cert row, no 5xx; check `SELECT count(*) FROM certs` unchanged):
| ID | Input |
|---|---|
| V1 | hosts `[]`, missing hosts, 101 hosts (100 ok) |
| V2 | `a.*.b`, `*foo.b`, `**.b`, `*`, `foo*.b`, `*.` |
| V3 | `foo_bar.example.com`, label of 64 chars (63 ok), host 254 chars (253 ok), leading/trailing hyphen label, empty label `a..b`, trailing dot, space inside, Unicode `bücher.example`, `xn--` ok? (IDN input rejected only if Unicode) |
| V4 | first host (CN) > 64 chars while <= 253 | 400 with reorder hint |
| V5 | injection in hosts: `a.com\nDNS.2=evil`, `a.com;rm`, `$(id).com`, `${ENV::HOME}`, `a.com#`, `a"b`, `../x`, `a.com/..`, `[x]`, NUL byte, `-config` |
| V6 | name: empty, `.hidden`, `a/b`, `../x`, `a b`, `a;b`, 65 chars (64 ok), `-x`? (leading `-` must be rejected per regex), newline, `x\"y`, `CON`-style fine |
| V7 | subject injections in each of C/ST/L/O/OU: `$`, `$(id)`, `${x}`, `#`, `\`, `"`, `;`, `=`, newline, `[ v3_req ]`, 65 chars, `../`; C = `U`, `USA`, `1A`, `ü` |
| V8 | bits 1024, 8192, 0, -1, "2048" (string), 2048.5 | 400 |
| V9 | days 0, 826, -1, 1.5, "10" | 400 |
| V10 | Types: hosts as string, name as number, extra unknown fields, null body | 400, no 500 |
| V11 | Verify that no injection created extra SANs/extensions: for accepted weird-but-valid inputs `-text` shows exactly the input SANs |
| V12 | `renewOf` with random uuid / other user's id / garbage | 404 / 404 / 400 |

### F11, F12 List and detail
| ID | Steps | Expected |
|---|---|---|
| D1 | `GET /api/certs` | newest first; fields id,name,hosts,bits,days,createdAt,notAfter,daysLeft,renewedBy; notAfter equals `openssl x509 -enddate`; `daysLeft = floor((notAfter-now)/24h)` (825-day cert -> 824 or 825 per timing; 1-day cert -> 0) |
| D2 | `GET /api/certs/{id}` | crt, key, cnf present; `Cache-Control: no-store` |
| D3 | `GET /files/crt|key|cnf` | `Content-Disposition: attachment; filename="<name>.<ext>"`; Content-Type `application/x-pem-file` / `text/plain`; `no-store`; bodies identical to D2 |
| D4 | `/files/pem`, `/files/..%2f`, unknown id format | 404/400 |
| D5 | Name chosen so filename could break header (validated away) | no header injection |
| D6 | Anonymous access to any cert endpoint | 401 |

### F13, F14, F15 Renew, Edit & Renew, Delete
| ID | Steps | Expected |
|---|---|---|
| N1 | `POST /certs/{id}/renew` | 201; same name/hosts/bits/days/subject; different key (pubkey hash differs) ; old `renewedBy` = new id; new cert's notAfter ~ now+days |
| N2 | Renew old again | 409 |
| N3 | Edit & Renew: `POST /certs` with `renewOf` and changed hosts/days/bits | 201 uses edited values; old marked renewed; same-transaction (409 on second attempt, no stray cert created: count unchanged) |
| N4 | `renewOf` already renewed | 409, no new row |
| N5 | Delete renewal | 204; old cert `renewedBy` becomes null; old can be renewed again |
| N6 | Delete cert twice / foreign cert | 404 |
| N7 | Delete the OLD cert of a renewal pair | works; the new cert is intact |
| N8 | Renew of foreign cert | 404 |

### F16 Isolation (U1 owns cert X, U2 attacks)
`GET /certs/X`, `/files/{crt,key,cnf}`, `POST /certs/X/renew`, `DELETE /certs/X`, `POST /certs {renewOf:X}`: all 404 (never 403, body identical to a nonexistent id). U2 list lacks X. Admin also gets 404 for U1's cert (admin sees counts only). Admin user list shows U1 certCount but no names. After U2's reset by admin, U1 unaffected.

### F17 At rest and logs
| ID | Steps | Expected |
|---|---|---|
| E1 | `psql -c "select key_enc from certs"` | bytes, `key_enc::text` / `encode(key_enc,'escape')` has no `PRIVATE KEY`, no `BEGIN`; length > key size; different for two equal certs |
| E2 | `pg_dump` of whole DB grep `PRIVATE KEY` / known key base64 fragment | none (cert_pem public part only) |
| E3 | `docker compose logs app` after the full run | no `PRIVATE KEY`, no passwords used in tests, no master key, no session tokens, no full webhook URLs/tokens (grep for each test secret) |
| E4 | users row: `auth_hash`, `wrapped_dek` not plaintext; passwords absent from dump |
| E5 | Tamper: swap `key_enc` between two certs of same user via SQL | download fails (AAD binds cert id), clean error not panic; restore |
| E6 | Temp files: after create (and a failing create), `docker compose exec app ls /tmp` has no leftover openssl dir; app runs as non-root (`id`) |
| E7 | Sessions table: `wrapped_dek` present, token not stored in clear (token_hash only) |

### F18 Webhook URL validation (`PUT /api/account/webhooks/{kind}`)
Accepted (200, response `masked` like `https://discord.com/…abcd`; response/list/GET never contain the full URL or the token; grep body):
- discord `https://discord.com/api/webhooks/<18 digits>/<64 chars>`; `/api/v10/webhooks/..`; with `?thread_id=123`; `discordapp.com`, `ptb.`, `canary.` -> check DB row rewritten to `discord.com`.
- teams `https://qa.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/x/triggers/manual/paths/invoke?api-version=1&sig=fake`.
Rejected 400: `http://`, `discord.com.evil.test`, `evildiscord.com`, `discord.com@evil.test`, userinfo, `:8443`, `:443` ok, extra path segment, wrong id length, short token, `?foo=1`, `#frag`, IP literal `https://192.0.2.1/...`, `[2001:db8::1]`, Teams without `sig`, Teams path not `/powerautomate/`, host exactly `environment.api.powerplatform.com` (no label), `logic.azure.com` / `webhook.office.com` / `outlook.office.com` (retired message), URL > 2048, kind `slack` (404/400), empty url.
Also: `DELETE` removes (GET shows null); DELETE twice; kind isolation between users (U2 cannot see U1's masked value); logs never contain URL (E3).

### F19 Send test (stub control: `qa/helpers/stub.py`, control port; modes `204`, `202`, `404`, `429`, `500`)
| ID | Steps | Expected |
|---|---|---|
| T1 | discord, stub 204 | `outcome:delivered`; stub received one POST: JSON embed with title/description/color, `username:"CertiFire"`, `allowed_mentions.parse == []` |
| T2 | teams, stub 202 | `outcome:accepted`, message says no delivery confirmation; payload `type:"message"`, attachment Adaptive Card 1.4, 2 TextBlocks, no actions |
| T3 | stub 404 | `outcome:broken`; GET webhooks shows `broken:true`, reason like "Discord 404 ..." without URL; |
| T4 | stub 429 + Retry-After 7 | `outcome:rate_limited`, `retryAfter` 7 |
| T5 | second test < 10 s later | 429 |
| T6 | re-save the URL after T3 | broken cleared |
| T7 | successful test after broken | clears broken |
| T8 | stub 500 / stub down | `outcome:error`, not broken |
| T9 | stub redirects (302 to other host) | not followed (response treated as non-2xx), no request to target |
| T10 | stub response body 1 MiB | handled (4 KiB cap), no crash |
| T11 | test with unconfigured kind | 404 |

### F20 Reminders (`qa/helpers/cases_03_webhooks_reminders.sh`)
Setup: webhooks for both kinds on user W pointing at the stub; certs created via API then `UPDATE certs SET not_after = now() + interval ... ` in DB. Trigger a tick by `docker compose restart app` (job runs at startup) - hourly tick not waited for. Because `daysLeft = floor`, set not_after = `now() + N days + 1 hour`.
| ID | Case | Expected |
|---|---|---|
| M1 | Cert with daysLeft 7, 5, 3, 2, 1 (five certs, one at a time or distinct names) | exactly one message per cert on each configured kind, text "expires in N day(s)", hosts, UTC date |
| M2 | Restart app again | no resend (reminders_sent has rows, stub count unchanged) |
| M3 | daysLeft 0 not expired; 4; 6; 8 | no message |
| M4 | Expired (not_after = now - 2 days) | one message "expired N day(s) ago"; restart same UTC day: none; set `reminders_sent.mark` / clear to `expired:<yesterday>` simulating next day: one more |
| M5 | Renewed cert (renewed_by set) with threshold date | none; delete cert -> none; delete the renewal -> old cert resumes reminders |
| M6 | Stub 500 on first tick | claim deleted (`reminders_sent` empty); next restart with stub 204 delivers once |
| M7 | Stub 404 | webhook marked broken, claim kept, subsequent ticks send nothing to that webhook; re-save clears; sends resume at next tick for new marks |
| M8 | Stub 429 Retry-After short (1s) then 204 | one delivery; Retry-After > 60 s -> claim removed, retried next tick |
| M9 | Both kinds configured | both receive; only discord configured -> only discord |
| M10 | Cert of user without webhook / broken webhook | none, other users unaffected |
| M11 | Many certs (20) due | all delivered sequentially, no duplicates (count rows == messages) |
| M12 | SIGTERM during send | graceful exit < 10 s, exit code 0 |
| M13 | Message content never includes key material |

### F21 Theme
| ID | Steps | Expected |
|---|---|---|
| TH1 | `PUT /account/theme` modes auto/light/dark, hue 0, 359, null | 204; `GET /api/state` returns the saved theme |
| TH2 | Invalid mode `blue`, hue 360, -1, 1.5, "x" | 400 |
| TH3 | Per-user: U1 dark/hue 200, U2 default; log out/in both | each retains its own |
| TH4 | Anonymous PUT | 401 |
(Visual effect in UI walkthrough.)

### F22 Ops
| ID | Steps | Expected |
|---|---|---|
| O1 | `/healthz` without auth | 200 `ok` |
| O2 | `/readyz` | 200 |
| O3 | `docker compose stop db`; poll `/healthz` & `/readyz` over 10 s; app container stays running | healthz 200, readyz 503, container not restarted, HEALTHCHECK status healthy |
| O4 | `docker compose start db` | readyz returns 200 within ~30 s without restarting app; login and certs work again |
| O5 | Cold start with DB started after app (`up app` then `up db`) | app waits with backoff, then ready |
| O6 | Liveness path report | path `/healthz`, container port 8080 (PORT), status 200 unauth; report to infra |
| O7 | Unknown `/api/nope` | JSON 404; SPA fallback `/some/route` -> HTML |
| O8 | Response headers on `/`, `/api/*`, errors | CSP exactly per spec, nosniff, Referrer-Policy no-referrer; built `index.html` has no inline scripts/styles (no CSP console errors in UI run) |
| O9 | `docker compose exec app openssl version`; `app id` non-root; no `curl`/`wget`/`psql` in image | as spec |
| O10 | Multi-arch: `docker buildx imagetools inspect <image>` lists amd64+arm64; `docker run --platform linux/arm64 --entrypoint openssl <img> version` (qemu) ; arm64 creates a 2048 cert through the full path if qemu available | pass; 4096 on qemu may be slow, use 2048 |
| O11 | Fresh-copy run: copy repo without `.git`/`node_modules`, `cp .env.example .env`, `docker compose up --build -d` | works first time |
| O12 | `docker compose down` (no -v) then `up`: data persists (certs, users, session); `down -v`: clean fresh Setup | pass |
| O13 | SIGTERM: `docker compose stop app` time < 10 s, exit 0 |
| O14 | Image size, no test files / source maps / `design/` / `.env` in image (`docker run --entrypoint sh ... find`) | none |
| O15 | `app healthcheck` command exit 0 up; exit non-zero when app is down |
| O16 | Idempotent restart: schema `IF NOT EXISTS` - restart app 3x, no errors, no data loss |
| O17 | Load smoke: `qa/helpers/load.sh` (bash+curl workers; k6 if installed): 20 req/s on `/api/certs` list + `/healthz` for 30 s with 5 users, 0 errors; p95 reported |

### F23 Backup-all (`qa/helpers/cases_05_backup_all.sh`, runs last)
`POST /api/backup-all`: IP allow-list `BACKUP_ALL_ALLOWED_IPS`, master key, AES zip of per-user zips. Requests go through the published port, so the app sees the Docker gateway (172.x). The allow-list is switched by recreating only the app (`QA_BACKUP_ALL_IPS=... $DC up -d --no-deps app`, DB volume stays; in-memory limiters reset). The master key comes from the setup flow (`$OUT/.mk`); the backup password is generated per run. The E5 tamper leaves one undecryptable cert, so the script first deletes that row and creates fixtures (`bkfull`: `dup` renewed once, `solo`; `bkempty`: no certs).
| ID | Steps | Expected |
|---|---|---|
| BK1 | Allow-list empty: POST valid, GET, POST empty | 404, body identical to an unknown `/api/*` path |
| BK2 | Allow-list `192.0.2.0/24`: POST valid, GET | 404 |
| BK3a | Allow-list `172.16.0.0/12`: GET, PUT | 404 (not 405) |
| BK3b | POST valid with each of X-Forwarded-For/-Host/-Proto, X-Real-Ip, Forwarded | 404 each |
| BK3c | Content-Type text/plain, and none | 415 |
| BK3d-h | Bad JSON; missing masterKey; blank masterKey; password 15 chars / missing / 15 multibyte runes | 400 |
| BK3i | 16 multibyte runes (rune count, not bytes) | 200 |
| BK3j | Body over 64 KiB | 400/413 |
| BK4 | 5 wrong master keys, then a 6th, then the right key | 401 x5, then 429, 429 |
| BK5 | Recreate app, right key | 200, `application/zip`, `Content-Disposition: attachment; filename="certifire-backup-all_YYYY-MM-DD.zip"` |
| BK6a-b | `7z x` with wrong then right password | wrong fails, right extracts |
| BK6c-d | Outer content; inner `certifire-all.zip` | outer holds exactly `certifire-all.zip`; inner is a plain zip |
| BK6e-h | Inner entries vs `select username from users` | one `<user>.zip` per account including `bkempty` (empty zip) |
| BK6i-k | Per user: `.cnf/.crt/.key` per cert vs DB count; `pubhash_cert` = `pubhash_key`; key is unencrypted PEM | all match |
| BK6l-m | Renewed pair | newest `dup.*`, older `dup_<uuid>.*`, certs differ |
| BK7 | `docker compose logs app` | `backup-all` lines with result=ok/denied/bad-request/bad-key/throttled and `ip=172.x`; no master key, backup password, wrong key text or "PRIVATE KEY" |
| BK8 | Two parallel valid requests | one 200 and one 429 (best effort, informational if the first finishes early) |
| BK9 | Reset to disabled; POST | 404 |
Not covered by the suite (Go unit tests): IPv4-mapped peer, per-IP vs global limiter counted separately, fileBases table (three dups, a cert literally named `<name>_<uuid>`), config parse errors. Regression: all cases 01-04 run unchanged before this one.

### F24 Date-based reminders (REMINDER_TZ / TIME / STEPS / LOCAL_CRON, `remind` subcommand)
Supersedes the day-count cases M1, M3, M4 and M6 above for steps 7,5,3,1,0 (2 is no longer a step); M2, M5, M7, M10, M11 stay and are rerun unchanged. All run through the real image with the webhook stub (fake ids/tokens, no external calls), compose project `certifire-qa`, evidence in `$OUT` only.

**Time control without app code changes**
- The app's clock is the container's wall clock, so every case is relative to "now": the script computes `today` in the zone under test with `TZ=<zone> date`, then sets `certs.not_after` by `psql` to an absolute instant (e.g. `timestamptz '2026-11-08 16:30+00'`, computed as "today in zone + N days at hh:mm"). Cases are never tied to fixed calendar dates, so the suite can run on any day.
- "Next day" is simulated as before: shift `not_after` one day earlier and/or rewrite `reminders_sent.mark` to yesterday's value; no clock change.
- `REMINDER_TZ`, `REMINDER_TIME`, `REMINDER_STEPS`, `REMINDER_LOCAL_CRON` are set per phase in the run copy's `.env` plus `docker compose up -d` (recreate); `REMINDER_TIME` gating is tested with "now+3 min" (not yet due) and "00:00" (due), computed in `REMINDER_TZ`.
- `remind` is run as `docker compose run --rm --no-deps app remind` (same image, own container, DB already up) or `exec app /app remind`; exit code and stub request count are asserted.
- Agreed with backend: every tick (in-app and `remind`) logs `reminder tick` with sent, failed and skipped counts, which the suite greps to assert "no ticks" and "one message". There is no QA-only interval variable, so the hourly retry is covered by the Go unit tests (injectable Sleep/Now) plus one optional 65-minute soak marked INFO; cases that needed a later tick (G1, R1, R2) assert the startup/`remind` behaviour in the suite and are INFO for the timed part. Still to come from backend: (c) the exact `reminders_sent.kind/mark` format for the new messages (the plan reads rows only to count them and to simulate the next day).

**Scope decision (user):** Discord is the supported, tested path. All F24 cases run with only a Discord webhook configured, so N certs give N messages (expected counts in the cases below that say "x 2" or "per kind" mean x 1 here). Teams stays in the app untouched: it gets one smoke case (TM1) and the existing Teams validation/send-test cases (F18/F19) stay as they are. The older M-cases that configure both kinds keep doing so.

**Cases** (Discord only unless stated)
| ID | Steps | Expect |
|---|---|---|
| D1 | Defaults only (empty `REMINDER_*`): start, check `/readyz`, `remind` on empty DB | app starts; `remind` exits 0, 0 messages (CI smoke) |
| D2 | TZ=UTC: five certs whose expiry date in UTC is today+7, +5, +3, +1, +0 (not_after later today) plus controls today+2, +4, +6, +8 | one message per cert with a step, none for controls (2 is not a step) |
| D3 | Each step text | "expires in N days" (N=1 singular "1 day"), day 0 "expires today"; hosts and dates shown |
| D4 | Day 0 before not_after (now < not_after) and after (not_after = 2 h ago, same TZ date) | exactly one message each; a cert that gets "expires today" at startup gets no second message after crossing not_after (restart, ticks): never two on the expiry date |
| D5 | Expired: not_after on date-1 and date-2 (TZ date) | one "expired ..." message per day per cert, text "expired today"/"expired 1 day ago"/"2 days ago" per the final wording; same-day restart sends none; simulated next day sends one more; continues daily until renewed or deleted |
| D6 | Expired cert is renewed, then deleted (M5 variants) | reminders stop; delete the renewal: resume |
| T1 | not_after = 2026-11-08T16:30Z shape, REMINDER_TZ=Asia/Manila: relative form: instant that is date D in UTC and D+1 in Manila | step chosen from the Manila date; the same cert under UTC is one day further away (two runs, different step) |
| T2 | Manila near-midnight edge: not_after 23:59 and 00:01 Manila local on consecutive dates | counted on their own local dates, not UTC |
| T3 | Message text with REMINDER_TZ=Asia/Manila and America/Los_Angeles | dates in that zone (plus tz name/offset if the wording has one), never the UTC date when they differ |
| T4 | A DST-change zone (America/New_York across a change, using a not_after near it) | step arithmetic is by calendar date, not 24 h blocks (informational if the date is not within 8 days of a change) |
| G1 | LOCAL_CRON=true, REMINDER_TIME = now+3 min in tz | startup tick sends nothing; after the time passes and a tick runs (needs hook (b)) the day's reminders go out once |
| G2 | REMINDER_TIME=00:00 | startup tick sends the due reminders immediately (same-day catch-up on restart) |
| G3 | Restart after sends, same day | no repeats (claims kept) |
| G4 | No backfill: cert whose step day was yesterday and was never sent (insert after the day) | nothing sent for the missed step; only today's step/expiry rules apply |
| R1 | Stub 500 at the first tick, then 204; claims released; later tick (hook (b), else Go test) | retried and delivered once per kind; no duplicate afterwards |
| R2 | 429 with Retry-After | treated as delayed: claim released, retried |
| R3 | Permanent 4xx (404): webhook marked broken, claim kept, later ticks skip it |  |
| L0 | LOCAL_CRON=false, serve for 70 s | no `tick` lines in app logs; zero stub requests even with due certs; `/healthz`, `/readyz` still 200 |
| C1 | `remind` with due certs, stub 204 | exit 0, one message per cert/kind, tick line printed, container exits (no server) |
| C2 | Rerun immediately | exit 0, 0 new messages |
| C3 | Stub 500 | exit non-zero; claims released; rerun with stub 204 sends each once and exits 0 |
| C4 | Stub 429 and a network failure (stub down) | non-zero, then recovery as C3 |
| C5 | Stub 404 | webhook marked broken, exit 0; next `remind` skips it |
| C6 | `remind` with REMINDER_TIME set to a future time | `remind` ignores REMINDER_TIME: it sends whatever is due now and exits 0 (the CronJob schedule sets the time) |
| C7 | Empty DB / no webhooks / user without webhook | exit 0, no messages |
| C8 | Two `remind` runs started together (same due certs) | total messages = one per cert/kind (claims); both exit 0; no duplicate key errors in logs |
| C9 | `remind` and a fresh `app` start at the same time against an empty database (new volume) | no schema race error (advisory lock), both succeed, tables created once |
| C10 | `remind` with REMINDER_LOCAL_CRON=true and =false | identical behaviour: one tick, exit; the flag only gates the in-app scheduler |
| B1 | Bad values, each in serve and in `remind`: REMINDER_TZ=Mars/Base, REMINDER_TIME=25:00 / 7:5 / empty-ish "abc", REMINDER_STEPS=7,,3 / -1 / abc / 400 / duplicates / empty, REMINDER_LOCAL_CRON=maybe | exit 1 before serving or sending; one clear message naming the variable and the bad value; no secrets/DATABASE_URL in it; compose shows restart loop (informational) |
| B2 | Valid edge values: TZ=Etc/UTC, TIME=23:59, STEPS=0 and STEPS=30,7,0 | accepted; window follows the largest step: a cert 30 days out is found (candidate window), 31 is not |
| B3 | Step list "1, 3,7,0" (spaces, any order) | accepted, stored descending (messages identical to "7,3,1,0"); duplicates, empty entries ("7,,3"), non-integers and values outside 0-365 exit 1 (366 and -1 rejected, 0 and 365 accepted) |
| TM1 | Teams smoke: add a Teams webhook (stub) next to Discord, one cert due today, run `remind` (or startup tick) | the reminder reaches the Teams stub as well; a 202 response counts as sent (claim kept, no retry, exit 0); Discord still gets exactly one. No further Teams date/step/failure cases. |
| X1 | Embedded tzdata | run the image with no `/usr/share/zoneinfo` (`docker run --rm --entrypoint sh ... -c 'ls /usr/share/zoneinfo'` shows none under distroless/alpine variant) and `REMINDER_TZ=Asia/Manila` still works |
| X2 | Message and logs | no webhook URL/token, private key or password in messages, app logs or `remind` output; CSP/other suite cases unchanged |
| X3 | k8s_cron/ (when infra delivers) | `docker run --rm -v ...:/k koalaman/kubeconform` (or `kubectl --dry-run=client` not available: use kubeconform only) on the CronJob; checks: `command/args` = `/app remind`, same image and env secret refs, `concurrencyPolicy`, `restartPolicy: OnFailure` or `backoffLimit` so non-zero retries, `startingDeadlineSeconds`; README matches the exit-code contract. No cluster access; static review only |

**Existing suite updates (cases_03):** M1 uses steps 7,5,3,1,0 (replace the `2` cert with `0`, keep 4/6/8 controls and add 2 as a control); M3 adds the 2-day control; M4 uses the TZ-date marks; message text assertions change to the new wording once backend fixes it. The whole suite runs with defaults (UTC, 00:00, LOCAL_CRON true) and its restart-driven approach is unchanged. New F24 cases go in a new script `qa/helpers/cases_06_reminders_dates.sh` (plus edits to cases_03), run last because they recreate the app with other env values and restore defaults at the end.

**Open items:** final message wording and the `reminders_sent.kind/mark` scheme for date-based days (backend will report). Resolved: tick log line, `remind` ignores REMINDER_TIME and REMINDER_LOCAL_CRON, step-list rules (B3).

## 2. UI walkthrough (visible WSL Chrome)
Script `qa/helpers/ui_walkthrough.py` (Playwright, `launch_persistent_context` on a fresh profile dir
under the run folder, `executable_path=/usr/bin/google-chrome`, `headless=False`, slow_mo 300).
No real credentials: the QA users and passwords are throwaway values generated per run. It takes
screenshots into `$OUT/ui/`. Steps (each asserts and screenshots):
1. U1 `/` shows Setup; set password; master key shown once with Copy; "I saved it" required; reload does not show it again.
2. Login/Register tabs; wrong login shows generic error; register `qa_user1`; invalid usernames show errors.
3. Create form: chips: Enter adds, comma adds, paste several (comma/newline separated), x removes, Backspace on empty input removes last, `a.*.b` stays in input with inline error; `*.homelab` chip shows Chrome warning; `*.k8s.homelab` shows none; duplicate collapses; CN>64 error; bits/days bounds; subject fields validation (C 2 letters).
4. Create cert (2048 for speed) -> list: name, hosts first few + "+N", bits, created, expires, days left match API.
5. Detail: `.crt/.key/.cnf` sections; Copy button (clipboard content equals text); Download button saves `<name>.crt|key|cnf` (Playwright download events); verify downloaded files with openssl.
6. Renew -> new row, old shows "renewed" badge linking to new; Renew button gone or 409 message shown. Edit & Renew: title "Renew <name>", prefilled, edit hosts, submit, old marked.
7. Delete with confirm dialog (cancel keeps, confirm deletes); deleting a renewal un-badges the old one.
8. Account: change password (wrong current error, success keeps session); second browser context was signed out. Theme: mode auto/light/dark and hue slider change colours (compare computed `--` variables / screenshots); reload and re-login keep it; no flash of wrong theme at load (check first-paint `data-theme`/variables before app JS: screenshot on throttled load).
9. Webhooks: save masked display, no full URL in DOM (assert page text/HTML lacks token), Send test shows outcome per stub mode (Teams 202 explanation, broken badge with reason, rate limit), Remove.
10. Admin (as admin): users list with counts; reset with wrong then right master key; non-admin has no Admin nav and the route redirects/denies.
11. Loading/error states: stop app mid-use (or block with route) -> error message visible, not a blank page. Console has no errors or CSP violations on any page. Window resized to phone width (390px): no horizontal scroll.
12. Keyboard: Tab order and visible focus on the chip input and dialogs; Escape closes dialog.
13. Logo shown in header and as favicon.

## 3. Out of scope / user checks
- Real Discord/Teams delivery and the Teams URL pattern (user, with own URLs).
- Kubernetes / HPA: not requested by the spec; no simulation needed.
- Browser trust of the generated cert (import to Chrome, visit a stub with it) is optional: `qa/helpers/` has no automation; mention in report if done.

## 4. Reviewer decisions (answered)
- Stub approach approved: TLS on 443, only `ca.crt` mounted into app (`/qa/ca.crt`, world-readable), stub image pinned `python:3.13-alpine`, fake ids/tokens only. F19/F20 stay on the stub; backend httptest is extra.
- `bits`/`days` optional: omitted/null/0 -> 4096/825 (add cases G10: omit, null, 0 each -> 201 with defaults); other out-of-range -> 400.
- `GET /api/info` -> 200 `{"base_url":"<BASE_URL>"}` exact key (case O7i).
- Throttle key = lowercased username + TCP peer IP; X-Forwarded-For ignored (case L9: 5 failures, then 6th with a different `X-Forwarded-For` still 429; also does not reset the lock). Lock applies to change password and admin reset; CLI not throttled (case C6: 6+ wrong CLI attempts never 429/lock). Locked correct password -> 429 (L3b). Successful logins do not count.

## 5. (old) Open questions
1. Webhook host allow-list prevents pointing the app at a plain local stub; plan uses a QA-only compose override (network alias + `SSL_CERT_FILE`), which requires Go to honour `SSL_CERT_FILE` (it does on Linux) and the alpine image to run as non-root able to read a mounted file. If the backend prefers `httptest` coverage only, F19/F20 would rely on its unit tests.
2. Are `bits`/`days` optional in `POST /api/certs` (defaults applied server side) or required? Plan tests both; expectation unclear in spec.
3. Is `GET /api/info` payload defined? Only checked for 200 JSON.
4. Login throttle key is "username+client": which client identifier behind Docker NAT (remote addr)? Plan assumes remote IP.
