# Kubernetes reminder CronJob

> Reference example only. Production manifests belong in the `kubernetes-manifests` repo;
> copy `cronjob.yaml` there and replace the placeholders.

## Purpose
When the app runs behind KEDA (scale to zero), its built-in daily reminder timer cannot be
relied on. Set `REMINDER_LOCAL_CRON=false` on the app Deployment and let this CronJob run
`/app remind` instead: it does one reminder check against the database and exits.

## Configure
1. Replace `<namespace>` and `<app-secret>` in `cronjob.yaml`; pin `image` to your release.
2. `schedule` and `timeZone` set the time the Job runs; `remind` ignores `REMINDER_TIME`, so
   it is not used by the CronJob. Keep `timeZone` equal to `REMINDER_TZ` so the dates line up.
   `timeZone` needs Kubernetes 1.27+.
3. The Secret `<app-secret>` is read with `envFrom` and needs:
   - `DATABASE_URL`: PostgreSQL connection URL (same as the app);
   - `REMINDER_TZ` (default `UTC`) and `REMINDER_STEPS` (default `7,5,3,1,0`): the same
     values as the app Deployment.
   `REMINDER_LOCAL_CRON` is only for the Deployment (`false`).

## Exit codes and retries
- `0`: check done, including a broken webhook (it is logged, not retried).
- non-zero: a send failed temporarily (5xx, network error or rate limit) and was not sent, or
  there was a config or database error. The Job then retries up to `backoffLimit: 2`.
  A new run is not started while one is active (`concurrencyPolicy: Forbid`) and a missed
  start is still run up to `startingDeadlineSeconds` (1 h) late.

## Check a run
```bash
kubectl get cronjob,jobs -n <namespace>
kubectl logs job/<job-name> -n <namespace>
```
To run it now: `kubectl create job --from=cronjob/certifire-remind certifire-remind-manual -n <namespace>`.
