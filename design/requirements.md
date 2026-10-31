# CertiFire: requirements (from the user, 2026-10-29)

CertiFire ("certifire") is a small self-hosted web app that creates self-signed SSL certificates
for the user's homelab apps. Personal tool, new app, repo `~/certifire` (no git remote yet; the
user handles the repo later, so no MR for now: work locally, the reviewer still reviews everything).

## Certificate generation (decided)
- Run the openssl CLI exactly as the user does by hand:
  `openssl req -x509 -nodes -days <days> -newkey rsa:<bits> -keyout <name>.key -out <name>.crt -config <name>.cnf`
- Days and RSA bits are configurable per cert (defaults: 825 days, 4096 bits; 825 is Apple's max).
- Output: `<name>.key`, `<name>.crt`, `<name>.cnf` (the .cnf is the one actually used; shown for reference).
- Only this format for now (self-signed, RSA, PEM).
- `.cnf` template (decided: "Chrome-tested root" from research/self-signed-tls-chrome.md):
  ```ini
  [ req ]
  default_bits       = <bits>
  prompt             = no
  default_md         = sha256
  distinguished_name = dn
  x509_extensions    = v3_req

  [ dn ]
  CN = <first host>
  # optional C/ST/L/O/OU when the user fills them in

  [ v3_req ]
  subjectAltName       = @alt_names
  basicConstraints     = critical, CA:TRUE, pathlen:0
  keyUsage             = critical, digitalSignature, keyEncipherment, keyCertSign
  extendedKeyUsage     = serverAuth
  subjectKeyIdentifier = hash

  [ alt_names ]
  DNS.1 = <host1>
  DNS.2 = *.k8s.<domain>   # wildcards allowed
  IP.1  = <ip>             # if the entry is an IP address
  ```
  (nameConstraints from the research note is left out: hosts can be under any domain.)
- Inputs and validation must be strict: hostnames/wildcards/IPs go into a file and a command line,
  so no injection (validate names, never build a shell string, run openssl with an args list in a temp dir).

## Create form
- Generic certificate name (used as `<name>` of the files).
- Hosts/alt names: a tag/chip input ("token input"): type, press Enter to add a chip, x to remove.
  See `design/tag-input-example.png`. Accepts wildcards like `*.k8s.homelab`.
- RSA size (e.g. 2048 / 3072 / 4096) and validity days.
- Optional subject fields (C, ST, L, O, OU), empty = omitted.

## Certificate list
- Flat list of the user's certificates (no folders): name, hosts, size, created, expires, days left.
- View/download `.crt`, `.key`, `.cnf` per cert; delete.

## Users and admin (same approach as ~/passman)
- Login per user; each user only sees their own certificates.
- First run: setup page creates `admin` with a password, then shows a master key once.
- Master key resets user passwords (admin UI and a `reset-password` CLI in the container), like passman.
- Register tab for new users, like passman.

## Storage
- PostgreSQL (like passman). PEM and .cnf are TEXT columns.
- Private keys encrypted per user like passman: per-user data key wrapped by the login password
  (Argon2id) plus an escrow copy unlockable with the master key. Certs, .cnf, hosts and expiry stay
  plaintext so reminders run without a logged-in user.

## Expiry reminders
- Webhooks per user: Discord and/or Microsoft Teams.
- Send one message when a cert reaches 7, 5, 3, 2 and 1 day(s) left, then one per day once
  expired, until it is renewed or deleted. Don't resend the same threshold (survives restarts).

## Look and feel
- Modern UI; inspiration: passman (`~/passman/web`), including its per-user theme
  (auto/light/dark and accent hue that changes the background colours).
- Logo: `design/logo.png` (white padlock with a fire flame, black background).
- Preview in the WSL Google Chrome, shown to the user.

## User decisions, round 2 (2026-10-29)
- Renewal: two actions on a cert.
  - **Renew**: one click, creates a new cert with the same name, hosts, bits, days and subject.
  - **Edit & Renew**: opens the create form prefilled from the cert; the user edits, then creates.
  - Either way the new cert is a new row; the old one stays in the list marked "renewed" (link to
    the new one), its reminders stop, and the user can delete it. (Orchestrator default.)
- Extras approved: webhook **Send test** button, **Change own password** (needs current password,
  re-wraps the data key), **Copy to clipboard** buttons on .crt/.key/.cnf views.
- Wildcard directly under a single-label domain (`*.homelab`): **allowed with a warning**
  (server accepts it; UI shows a warning that Chrome rejects it). Other wildcard rules stay strict.
- Web build stage: **node:24-alpine**.
- Webhooks: see the agentic-config research note `discord-teams-webhooks.md` (rnd): Discord +
  Teams Workflows only, exact host/path allowlist, no-redirect client, 4xx (not 429) marks the
  webhook broken (shown in UI), URLs never logged and masked in the UI.
