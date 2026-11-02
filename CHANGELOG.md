# Changelog

## 1.0.0

First major release: a lot changed at once, some of it breaking for existing setups.

- Breaking: the default image is `eacer/certifire:latest` (Compose `IMAGE` default, README, `dockerdesc.md`); pin a
  release with `IMAGE=eacer/certifire:vX.Y` / `vX.Y.Z`.
- Breaking: `.env.example` leaves `POSTGRES_PASSWORD` empty and marks it required; set it in `.env` before the first
  start (Postgres 18 refuses a blank password). The README Quick start and `dockerdesc.md` say so up front, and the
  `dockerdesc.md` compose example reads `.env`. Compose stops with `set POSTGRES_PASSWORD` when it is blank or
  unset, instead of starting a database that can't initialise.
- Images pinned to their minor version, incl. the Alpine variant: `node:24.22-alpine3.24`, `golang:1.26-alpine3.24`,
  `alpine:3.24`, `postgres:18.6-alpine3.24`; `openssl~=3.5`.
- The version is built into the binary (`main.version`, Dockerfile `ARG VERSION`) and logged at startup.
- MIT `LICENSE`.
- Expiry reminders work by calendar date in a configurable zone and can run outside the app, for scale-to-zero
  (KEDA) setups:
  - New variables: `REMINDER_TZ` (default `UTC`), `REMINDER_TIME` (`00:00`, in that zone), `REMINDER_STEPS`
    (`7,5,3,1,0`; `0` is the expiry day) and `REMINDER_LOCAL_CRON` (`true`: the app checks hourly and sends from
    `REMINDER_TIME`; `false`: no in-app checks). Invalid values stop startup.
  - Steps changed from 7, 5, 3, 2, 1 days to 7, 5, 3, 1 days plus the expiry day; after the expiry date one
    reminder per day, as before. Message dates are shown in `REMINDER_TZ` (were UTC).
  - New `remind` subcommand: one check, then exit (non-zero when a send failed, so a Kubernetes Job retries it).
    Reference CronJob in `k8s_cron/`; on a Docker host, `docker compose run --rm app remind`.
  - The schema is applied under a database lock, so several pods starting together are safe.
  - Upgrade note: on the upgrade day a certificate that is already expired may get one extra or one missing daily
    reminder when `REMINDER_TZ` is not UTC (earlier marks used UTC dates).
- QA suite (`qa/run.sh`): sets a throwaway database password for its isolated stack, keeps results only in its temp
  run folder (no more `qa/evidence-last/` in the repo) and exits non-zero only when a case fails.
