#!/usr/bin/env bash
# F24 date-based reminders: REMINDER_TZ / TIME / STEPS / LOCAL_CRON and `app remind`. Runs after cases_05 (last).
# Discord is the tested path; Teams has one smoke case (TM1). Uses the local TLS stub; no real webhooks.
# Time control without app changes: every case sets certs.not_after relative to "today" in the zone under test (psql),
# so it runs on any day. Config is switched through QA_REMINDER_* (compose.qa.yml) with --force-recreate (serve) or
# `docker compose run` (remind). Evidence: $OUT/rem_*.out
source "$(dirname "$0")/lib.sh"
W=$OUT/w.jar
TOK=$(printf 'A%.0s' {1..64}); DID=123456789012345678
DU="https://discord.com/api/webhooks/$DID/$TOK"
TU="https://qa.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/abc/triggers/manual/paths/invoke?api-version=1&sig=FAKESIG1234"
stub_mode() { curl -s -X POST -d "$1" $STUBCTL/__mode >/dev/null; }
stub_reset() { curl -s -X POST $STUBCTL/__reset >/dev/null; }
stub_n()    { curl -s $STUBCTL/__requests | jq 'length'; }
stub_host() { curl -s $STUBCTL/__requests | jq "[.[]|select(.host|startswith(\"$1\"))]|length"; }
titles()    { curl -s $STUBCTL/__requests | jq -r '.[].body | fromjson | (.embeds[0].title // "")'; }
descs()     { curl -s $STUBCTL/__requests | jq -r '.[].body | fromjson | (.embeds[0].description // "")'; }
put() { jq -nc --arg u "$1" '{url:$u}'; }
wait_up() { for _ in $(seq 1 40); do [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/readyz")" = 200 ] && return 0; sleep 1; done; return 1; }
snap() { $DC logs --no-color app >> "$OUT/rem_logs.txt" 2>&1; }
# serve KEY=VAL...: recreate the app with the given QA_REMINDER_* values (none = defaults)
serve() { snap; env "$@" $DC up -d --force-recreate --no-deps app >/dev/null 2>&1; wait_up || bad "serve $*" "app not ready"; sleep 3; }
# rem KEY=VAL...: one `app remind` run (own container, same image/DB); exit code in $RC, output in $OUT/rem_N.out
RN=0
rem() { RN=$((RN+1)); RO=$OUT/rem_$RN.out; env "$@" $DC run --rm --no-deps -T app remind >"$RO" 2>&1; RC=$?; }
tick_line() { grep -o '"msg":"reminder tick"[^}]*' "$RO" | tail -1; }
UID_=$(psql_q "select id from users where username='hooky'")
mkc() { st $W POST /api/certs "{\"name\":\"$1\",\"hosts\":[\"$1.example.com\"],\"bits\":2048,\"days\":825,\"subject\":{}}" >/dev/null; j .id; }
# setz ID ZONE N HH:MM[:SS] : not_after = (today in ZONE + N days) at that local time
setz() { psql_q "update certs set not_after = (((now() at time zone '$2')::date + ($3)) + time '$4') at time zone '$2' where id='$1'" >/dev/null; }
reset() { stub_reset; stub_mode '{"status":204}'; psql_q "delete from certs where user_id=$UID_" >/dev/null; psql_q "delete from reminders_sent" >/dev/null
  psql_q "update webhooks set broken_at=null, broken_reason=null where user_id=$UID_" >/dev/null; }
sdate() { psql_q "select (now() at time zone '$1')::date + ($2)"; }   # date in zone, offset days
marks() { psql_q "select coalesce(string_agg(mark, ',' order by mark), '') from reminders_sent"; }

# session + Discord-only webhook (Teams is added only in TM1)
st $W GET /api/account/webhooks >/dev/null
[ "$STATUS" = 200 ] || check F24login 200 $W POST /api/login '{"username":"hooky","password":"qa-Hook-pass-1"}'
check F24w1 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"
psql_q "delete from webhooks where user_id=$UID_ and kind='teams'" >/dev/null
serve   # defaults (UTC, 00:00, 7,5,3,1,0, local cron on)

# ---- D1 defaults valid, empty DB
reset; rem; [ $RC = 0 ] && ok "D1 remind with defaults, no certs: exit 0" || bad D1 "rc=$RC $(tail -3 $RO)"
tick_line | grep -q '"sent":0' && ok "D1 tick logged (sent 0)" || bad D1t "$(tick_line)"
[ "$(stub_n)" = 0 ] && ok "D1 no messages" || bad D1n

# ---- D2/D3 steps (UTC): 7,5,3,1,0 sent; 2,4,6,8 not
reset; declare -A C
for d in 7 5 3 1 0 2 4 6 8; do C[$d]=$(mkc "s$d"); setz ${C[$d]} UTC $d 12:00; done
rem; [ $RC = 0 ] && ok "D2 remind exit 0" || bad D2 "rc=$RC"
[ "$(stub_n)" = 5 ] && ok "D2 5 messages (steps 7,5,3,1,0), none for 2,4,6,8" || bad D2n "got $(stub_n)"
[ "$(marks)" = "0,1,3,5,7" ] && ok "D2 marks are the step values" || bad D2m "$(marks)"
T=$(titles)
for want in 'expires in 7 days' 'expires in 5 days' 'expires in 3 days' 'expires in 1 day$' 'expire[sd] today'; do
  grep -Eq "\`s[0-9]\` $want" <<<"$T" && ok "D3 title '$want'" || bad "D3 $want" "$T"; done
grep -q 'expires in 1 days' <<<"$T" && bad D3pl "plural on 1" || ok "D3 singular 'day' for 1"
D=$(descs); grep -q "It expires on $(sdate UTC 7) (UTC)\." <<<"$D" && ok "D3 body date + zone name (UTC)" || bad D3b "$D"
rem; [ $RC = 0 ] && [ "$(stub_n)" = 5 ] && ok "D2 rerun: no repeats" || bad D2r "rc=$RC n=$(stub_n)"
tick_line | grep -q '"sent":0' && ok "D2 rerun tick sent 0" || bad D2rt "$(tick_line)"

# ---- D3b body: only first 5 hosts, "and N more", expired wording
reset; c=$(st $W POST /api/certs '{"name":"many","hosts":["h1.example.com","h2.example.com","h3.example.com","h4.example.com","h5.example.com","h6.example.com","h7.example.com"],"bits":2048,"days":825,"subject":{}}' ; j .id)
setz $c UTC 3 12:00; rem
D=$(descs); grep -q 'h5.example.com` and 2 more' <<<"$D" && ! grep -q 'h6.example.com' <<<"$D" && ok "D3 hosts: first 5 + 'and 2 more'" || bad D3h "$D"

# ---- D4 day 0: before and after not_after -> one message on the expiry date, never two (skipped within 2 min of UTC midnight)
H=$(date -u +%H%M)
if [ "$H" -ge 2358 ] || [ "$H" -le 0001 ]; then echo "INFO D4 skipped: too close to UTC midnight" | tee -a "$OUT/results.txt"; else
reset; a=$(mkc d0a); setz $a UTC 0 23:59:30; rem
[ "$(stub_n)" = 1 ] && titles | grep -q 'expires today' && ok "D4 day 0 before not_after: 'expires today' once" || bad D4a "n=$(stub_n) $(titles)"
setz $a UTC 0 00:00:30; rem; serve; sleep 2
[ "$(stub_n)" = 1 ] && ok "D4 same cert after not_after passes: no second message (rem + startup tick)" || bad D4b "n=$(stub_n)"
reset; b=$(mkc d0b); setz $b UTC 0 00:00:30; rem
[ "$(stub_n)" = 1 ] && titles | grep -q 'expired today' && descs | grep -q 'It expired on' && ok "D4 day 0 after not_after: 'expired today', 'It expired on'" || bad D4c "n=$(stub_n) $(titles) $(descs)"
rem; [ "$(stub_n)" = 1 ] && ok "D4 rerun same day: none" || bad D4d; fi

# ---- D5 expired on day+1, day+2: one per day, text, next day, until renewed/deleted
reset; e1=$(mkc ex1); e2=$(mkc ex2); setz $e1 UTC -1 12:00; setz $e2 UTC -2 12:00; rem
[ "$(stub_n)" = 2 ] && ok "D5 two expired certs: one message each" || bad D5 "got $(stub_n)"
T=$(titles); grep -q 'ex1` expired 1 day ago' <<<"$T" && grep -q 'ex2` expired 2 days ago' <<<"$T" && ok "D5 'expired 1 day ago' / 'expired 2 days ago'" || bad D5t "$T"
[ "$(marks)" = "expired:$(sdate UTC 0),expired:$(sdate UTC 0)" ] && ok "D5 mark expired:<today UTC>" || bad D5m "$(marks)"
rem; [ "$(stub_n)" = 2 ] && ok "D5 same day rerun: none" || bad D5r
psql_q "update reminders_sent set mark='expired:'||to_char((now() at time zone 'utc')::date-1,'YYYY-MM-DD')" >/dev/null; rem
[ "$(stub_n)" = 4 ] && ok "D5 next day (simulated): one more each" || bad D5n "got $(stub_n)"
st $W POST /api/certs/$e1/renew >/dev/null; psql_q "delete from reminders_sent" >/dev/null; stub_reset; rem
[ "$(stub_n)" = 1 ] && ok "D6 renewed cert stops (only the still-expired one reminds)" || bad D6 "got $(stub_n)"
check D6d 204 $W DELETE /api/certs/$e2; stub_reset; psql_q "delete from reminders_sent" >/dev/null; rem
[ "$(stub_n)" = 0 ] && ok "D6 deleted cert stops; renewal (365d) not due" || bad D6e "got $(stub_n)"

# ---- T time zones
# T1: instant that is UTC date D but Manila date D+1 (17:00Z = 01:00 next day in Manila)
reset; t=$(mkc tz1); psql_q "update certs set not_after = (((now() at time zone 'utc')::date + 0) + time '17:00') at time zone 'utc' where id='$t'" >/dev/null
rem; N_UTC=$(stub_n); T_UTC=$(titles); stub_reset; psql_q "delete from reminders_sent" >/dev/null
rem QA_REMINDER_TZ=Asia/Manila; N_MNL=$(stub_n); T_MNL=$(titles); D_MNL=$(descs)
DM=$(psql_q "select (not_after at time zone 'Asia/Manila')::date - (now() at time zone 'Asia/Manila')::date from certs where id='$t'")
[ "$N_UTC" = 1 ] && grep -q 'tz1` expire[sd] today' <<<"$T_UTC" && ok "T1 UTC: expiry date is today" || bad T1u "n=$N_UTC $T_UTC"
if [ "$DM" = 0 ]; then WANT='expire[sd] today'; elif [ "$DM" = 1 ]; then WANT='expires in 1 day$'; else WANT=X; fi
[ "$N_MNL" = 1 ] && grep -Eq "tz1\` $WANT" <<<"$T_MNL" && ok "T1 Manila: same instant is $DM day(s) away (${T_MNL})" || bad T1m "n=$N_MNL d=$DM $T_MNL"
grep -q "$(psql_q "select (not_after at time zone 'Asia/Manila')::date from certs where id='$t'") (Asia/Manila)" <<<"$D_MNL" && ok "T1 body date is the Manila date + zone name" || bad T1b "$D_MNL"
# T2: 23:59 and 00:01 local on consecutive dates are counted on their own local dates (steps 4,3)
reset; a=$(mkc tz2a); b=$(mkc tz2b); setz $a Asia/Manila 3 23:59; setz $b Asia/Manila 4 00:01
rem QA_REMINDER_TZ=Asia/Manila QA_REMINDER_STEPS=4,3; T=$(titles)
[ "$(stub_n)" = 2 ] && grep -q 'tz2a` expires in 3 days' <<<"$T" && grep -q 'tz2b` expires in 4 days' <<<"$T" && ok "T2 Manila 23:59 (+3) and 00:01 (+4) by local date" || bad T2 "$T"
# T3: LA evening is already next UTC date; text shows the LA date
reset; l=$(mkc tz3); setz $l America/Los_Angeles 3 22:00
rem QA_REMINDER_TZ=America/Los_Angeles; T=$(titles); D=$(descs)
[ "$(stub_n)" = 1 ] && grep -q 'in 3 days' <<<"$T" && grep -q "$(sdate America/Los_Angeles 3) (America/Los_Angeles)" <<<"$D" && ok "T3 LA: step by LA date, text in LA zone" || bad T3 "$T $D"
echo "INFO T4 DST arithmetic is unit-tested (dateOf counts calendar days); not exercised here (needs a movable clock)" | tee -a "$OUT/results.txt"

# ---- G gating / catch-up (serve)
reset; g=$(mkc g1); setz $g UTC 3 12:00
MIN=$(date -u -d '+105 seconds' +%H:%M); H=$(date -u +%H%M)
if [ "$H" -ge 2357 ]; then echo "INFO G1 skipped: too close to UTC midnight" | tee -a "$OUT/results.txt"; else
serve QA_REMINDER_TIME=$MIN
[ "$(stub_n)" = 0 ] && ok "G1 REMINDER_TIME=$MIN not reached: startup tick sends nothing" || bad G1 "n=$(stub_n)"
$DC logs --no-color app 2>&1 | grep -q 'reminder tick' && bad G1t "tick before REMINDER_TIME" || ok "G1 no tick before REMINDER_TIME"
while [ "$(date -u +%H:%M)" \< "$MIN" ]; do sleep 2; done
for _ in $(seq 1 15); do [ "$(stub_n)" = 1 ] && break; sleep 2; done   # timer may fire a few seconds late (WSL clock); an hourly tick would be up to 60 min late
$DC logs --no-color --tail 30 app > "$OUT/g1_app.log" 2>&1; date -u +%T > "$OUT/g1_time.txt"
[ "$(stub_n)" = 1 ] && ok "G1 no restart: the app woke at REMINDER_TIME=$MIN and sent (not at the next hourly tick)" || bad G1b "n=$(stub_n)"
$DC logs --no-color app 2>&1 | grep -q '"msg":"reminder tick","sent":1' && ok "G1 tick at REMINDER_TIME logged sent=1" || bad G1c
serve QA_REMINDER_TIME=$MIN
[ "$(stub_n)" = 1 ] && ok "G1 restart after REMINDER_TIME: startup tick, no repeat" || bad G1d "n=$(stub_n)"
echo "INFO G1 hourly in-app retry timing: Go unit tests (injectable Sleep/Now); optional 65 min soak not run" | tee -a "$OUT/results.txt"; fi
serve; [ "$(stub_n)" = 1 ] && ok "G3 restart same day: no repeat" || bad G3 "n=$(stub_n)"
reset; serve; [ "$(stub_n)" = 0 ] && ok "G2 defaults, nothing due: nothing sent" || bad G2
g=$(mkc g2); setz $g UTC 1 12:00; serve; [ "$(stub_n)" = 1 ] && ok "G2 REMINDER_TIME=00:00: startup tick sends at once" || bad G2b "n=$(stub_n)"
$DC logs --no-color app 2>&1 | grep -q '"msg":"reminder tick","sent":1' && ok "G2 'reminder tick' logged with counts" || bad G2c
# G4 no backfill: a missed step day is not sent later (cert at day 2 -> not a step; day-4 step never existed)
reset; g=$(mkc g4); setz $g UTC 4 12:00; psql_q "insert into reminders_sent(cert_id,kind,mark) values ('$g','discord','5')" >/dev/null; rem
[ "$(stub_n)" = 0 ] && ok "G4 missed step (5) not backfilled at 4 days" || bad G4 "n=$(stub_n)"

# ---- L0 LOCAL_CRON=false
reset; l=$(mkc l0); setz $l UTC 3 12:00
serve QA_REMINDER_LOCAL_CRON=false; sleep 10
$DC logs --no-color app 2>&1 | grep -q 'reminder tick' && bad L0 "tick with LOCAL_CRON=false" || ok "L0 no ticks in the app logs"
[ "$(stub_n)" = 0 ] && ok "L0 no messages although a cert is due" || bad L0n "n=$(stub_n)"
[ "$($DC logs --no-color app 2>&1 | grep -c 'scheduler off')" = 1 ] && ok "L0 'scheduler off' logged once" || bad L0l
[ "$(curl -s -o /dev/null -w '%{http_code}' $BASE/healthz)$(curl -s -o /dev/null -w '%{http_code}' $BASE/readyz)" = 200200 ] && ok "L0 healthz/readyz 200" || bad L0h
rem QA_REMINDER_LOCAL_CRON=false; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "C10 remind ignores LOCAL_CRON=false: sends" || bad C10 "rc=$RC n=$(stub_n)"
serve   # back to defaults

# ---- C remind
reset; for d in 3 1; do c=$(mkc "c$d"); setz $c UTC $d 12:00; done
rem; [ $RC = 0 ] && [ "$(stub_n)" = 2 ] && ok "C1 remind: exit 0, 2 sent" || bad C1 "rc=$RC n=$(stub_n)"
tick_line | grep -q '"sent":2' && ok "C1 tick line sent=2" || bad C1t "$(tick_line)"
rem; [ $RC = 0 ] && [ "$(stub_n)" = 2 ] && ok "C2 rerun: exit 0, nothing new" || bad C2 "rc=$RC n=$(stub_n)"
# C6 remind ignores REMINDER_TIME (set to a time later today in UTC; if it is the last minute, use 23:59 anyway)
reset; c=$(mkc c6); setz $c UTC 3 12:00; rem QA_REMINDER_TIME=23:59; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "C6 remind ignores REMINDER_TIME" || bad C6 "rc=$RC n=$(stub_n)"
# C3 5xx
reset; c=$(mkc c3); setz $c UTC 3 12:00; stub_mode '{"status":500}'; rem
[ $RC != 0 ] && ok "C3 5xx: non-zero exit (rc=$RC)" || bad C3 "rc=0"
[ "$(marks)" = "" ] && ok "C3 claim released (row deleted)" || bad C3m "$(marks)"
tick_line | grep -q '"delayed":1' && ok "C3 tick delayed=1" || bad C3t "$(tick_line)"
stub_reset; stub_mode '{"status":204}'; rem; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "C3 rerun sends it once, exit 0" || bad C3r "rc=$RC n=$(stub_n)"
rem; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "C3 second rerun: still once" || bad C3rr
# C4 429 (retry-after beyond the wait cap is also delayed) and network failure
reset; c=$(mkc c4); setz $c UTC 3 12:00; stub_mode '{"status":429,"retryAfter":1}'; rem
[ $RC != 0 ] && [ "$(marks)" = "" ] && ok "C4 429: non-zero, claim released (rc=$RC)" || bad C4 "rc=$RC marks=$(marks)"
stub_reset; stub_mode '{"status":204}'
# network failure: discord.com is pinned to 127.0.0.1 (nothing listens) in a one-off container on the compose network.
# Never stop the stub for this: the alias would vanish and discord.com would resolve to the real internet.
PGPW=$($DC exec -T db printenv POSTGRES_PASSWORD); PGU=$($DC exec -T db printenv POSTGRES_USER); PGD=$($DC exec -T db printenv POSTGRES_DB)
RN=$((RN+1)); RO=$OUT/rem_$RN.out
docker run --rm --network "$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$($DC ps -q db)")" --add-host discord.com:127.0.0.1 \
  -e DATABASE_URL="postgres://$PGU:$PGPW@db:5432/$PGD?sslmode=disable" "$(docker inspect -f '{{.Config.Image}}' "$($DC ps -q app)")" remind >"$RO" 2>&1; RC=$?
[ $RC != 0 ] && [ "$(marks)" = "" ] && [ "$(stub_n)" = 0 ] && ok "C4 network failure (connection refused): non-zero, claim released, no request reached the stub (rc=$RC)" || bad C4n "rc=$RC marks=$(marks)"
rem; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "C4 stub back: rerun delivers once, exit 0" || bad C4r "rc=$RC n=$(stub_n)"
# C5 permanent 4xx
reset; c=$(mkc c5); setz $c UTC 3 12:00; stub_mode '{"status":404}'; rem
[ $RC = 0 ] && ok "C5 404: exit 0" || bad C5 "rc=$RC"
st $W GET /api/account/webhooks >/dev/null; [ "$(j .discord.broken)" = true ] && ok "C5 webhook marked broken" || bad C5b "$BODY"
[ "$(marks)" != "" ] && ok "C5 claim kept" || bad C5k
stub_reset; rem; [ $RC = 0 ] && [ "$(stub_n)" = 0 ] && ok "C5 broken webhook skipped on rerun" || bad C5s
check C5re 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"
# C7 user without webhook
reset; psql_q "delete from webhooks where user_id=$UID_" >/dev/null; c=$(mkc c7); setz $c UTC 3 12:00; rem
[ $RC = 0 ] && [ "$(stub_n)" = 0 ] && ok "C7 no webhook: exit 0, nothing sent" || bad C7 "rc=$RC"
check C7re 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"
# C8 two concurrent remind runs: one message per cert
reset; for i in 1 2 3 4 5 6; do c=$(mkc "cc$i"); setz $c UTC 3 12:00; done
RN=$((RN+1)); (env $DC run --rm --no-deps -T app remind >"$OUT/rem_${RN}a.out" 2>&1; echo $? > "$OUT/rem_${RN}a.rc") &
(env $DC run --rm --no-deps -T app remind >"$OUT/rem_${RN}b.out" 2>&1; echo $? > "$OUT/rem_${RN}b.rc") &
wait
[ "$(cat $OUT/rem_${RN}a.rc)$(cat $OUT/rem_${RN}b.rc)" = 00 ] && ok "C8 both concurrent runs exit 0" || bad C8rc "$(cat $OUT/rem_${RN}a.rc) $(cat $OUT/rem_${RN}b.rc)"
[ "$(stub_n)" = 6 ] && [ "$(psql_q 'select count(*) from reminders_sent')" = 6 ] && ok "C8 6 certs: exactly 6 messages across both runs" || bad C8 "n=$(stub_n)"
grep -qi 'duplicate\|error' "$OUT/rem_${RN}a.out" "$OUT/rem_${RN}b.out" && bad C8e "error text in output" || ok "C8 no errors in either run"

# ---- C9 schema race: remind x2 + app start together on empty databases
for i in 1 2 3; do
  psql_q "create database racedb$i" >/dev/null
  U="postgres://$PGU:$PGPW@db:5432/racedb$i?sslmode=disable"
  ( $DC run --rm --no-deps -T -e DATABASE_URL="$U" app remind >"$OUT/race${i}a.out" 2>&1; echo $? >"$OUT/race${i}a.rc" ) &
  ( $DC run --rm --no-deps -T -e DATABASE_URL="$U" app remind >"$OUT/race${i}b.out" 2>&1; echo $? >"$OUT/race${i}b.rc" ) &
  $DC run -d --no-deps --name qa-race-app$i -e DATABASE_URL="$U" -e PORT=8099 app >/dev/null 2>&1
  wait; sleep 4
  R="$(cat $OUT/race${i}a.rc)$(cat $OUT/race${i}b.rc)"; docker logs qa-race-app$i >"$OUT/race${i}app.out" 2>&1
  docker rm -f qa-race-app$i >/dev/null 2>&1
  [ "$R" = 00 ] && grep -q 'database ready' "$OUT/race${i}app.out" && ! grep -qi 'already exists\|duplicate\|unique' "$OUT/race${i}"*.out && ok "C9 round $i: remind x2 + app on a fresh DB, no schema race" || bad C9 "round $i rc=$R $(tail -2 $OUT/race${i}app.out)"
  psql_q "drop database racedb$i" >/dev/null
done

# ---- B bad config: serve and remind exit 1 with a clear message
chkbad() { # VAR VALUE
  local v=$1 val=$2
  for mode in remind serve; do
    if [ $mode = remind ]; then timeout 40 env QA_$v="$val" $DC run --rm --no-deps -T app remind >"$OUT/bad.out" 2>&1; rc=$?
    else timeout 40 env QA_$v="$val" $DC run --rm --no-deps -T app >"$OUT/bad.out" 2>&1; rc=$?; fi
    [ $rc = 1 ] && grep -q "${v#REMINDER_}" "$OUT/bad.out" && ok "B1 $mode $v='$val' exit 1 + message" || bad "B1 $mode $v='$val'" "rc=$rc $(tail -2 $OUT/bad.out)"
    grep -q "postgres://" "$OUT/bad.out" && bad B1s "DATABASE_URL leaked ($mode)"
  done; }
chkbad REMINDER_TZ Mars/Base
for v in 25:00 7:5 abc 12:60; do chkbad REMINDER_TIME "$v"; done
for v in "7,,3" -1 abc 366 "7,7" "7,x"; do chkbad REMINDER_STEPS "$v"; done
chkbad REMINDER_LOCAL_CRON maybe
# B2/B3 valid values: exit 0 from remind; window follows the largest step
reset; for d in 30 31; do c=$(mkc "w$d"); setz $c UTC $d 12:00; done
rem QA_REMINDER_STEPS=30,7,0; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && titles | grep -q 'w30` expires in 30 days' && ok "B2 STEPS=30,7,0: day 30 sent, day 31 not (window = largest step)" || bad B2 "rc=$RC n=$(stub_n)"
reset; c=$(mkc w365); setz $c UTC 365 12:00; rem QA_REMINDER_STEPS=365; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "B2 STEPS=365: day 365 found" || bad B2b "rc=$RC n=$(stub_n)"
reset; c=$(mkc w0); setz $c UTC 0 12:00; rem QA_REMINDER_STEPS=0; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "B2 STEPS=0 only: expiry day" || bad B2c
reset; c=$(mkc w7); setz $c UTC 7 12:00; rem "QA_REMINDER_STEPS= 7 ,3 ,0 "; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "B3 spaces/any order accepted" || bad B3 "rc=$RC $(tail -2 $RO)"
reset; c=$(mkc w3); setz $c UTC 3 12:00; rem QA_REMINDER_STEPS=0,3,7; [ $RC = 0 ] && [ "$(stub_n)" = 1 ] && ok "B3 ascending order accepted" || bad B3o
rem QA_REMINDER_TIME=23:59 QA_REMINDER_TZ=Etc/UTC QA_REMINDER_LOCAL_CRON=true; [ $RC = 0 ] && ok "B2 TIME=23:59, TZ=Etc/UTC, LOCAL_CRON=true valid" || bad B2d

# ---- TM1 Teams smoke: reminder reaches the Teams stub, 202 counts as sent
reset; c=$(mkc tm1); setz $c UTC 3 12:00
check TM1a 200 $W PUT /api/account/webhooks/teams "$(put "$TU")"
stub_mode '{"status":202}'; rem
[ $RC = 0 ] && [ "$(stub_host discord)" = 1 ] && [ "$(stub_host qa.environment)" = 1 ] && ok "TM1 one reminder to Discord and one to Teams (202 = sent)" || bad TM1 "rc=$RC d=$(stub_host discord) t=$(stub_host qa.environment)"
[ "$(psql_q "select count(*) from reminders_sent where kind='teams'")" = 1 ] && ok "TM1 teams claim kept" || bad TM1c
rem; [ $RC = 0 ] && [ "$(stub_n)" = 2 ] && ok "TM1 rerun: no repeat" || bad TM1r
psql_q "delete from webhooks where user_id=$UID_ and kind='teams'" >/dev/null

# ---- X
IMG=certifire-qa:local   # IMAGE set by run.sh
docker run --rm --entrypoint sh "$IMG" -c 'test ! -e /usr/share/zoneinfo/Asia/Manila' && ok "X1 image has no system zoneinfo for Asia/Manila (tzdata is embedded; T1 passed on it)" || bad X1 "zoneinfo present: embedded test inconclusive"
{ cat "$OUT"/rem_*.out "$OUT"/race*.out "$OUT/rem_logs.txt" 2>/dev/null; $DC logs --no-color app 2>&1; } > "$OUT/f24_all.txt"
grep -q "$TOK" "$OUT/f24_all.txt" && bad X2w "webhook token in logs/output" || ok "X2 webhook token not in logs or remind output"
grep -q 'FAKESIG' "$OUT/f24_all.txt" && bad X2t "teams sig in logs" || ok "X2 teams sig not in logs"
grep -q 'PRIVATE KEY' "$OUT/f24_all.txt" && bad X2k "key material in logs" || ok "X2 no key material"
grep -qF "$PGPW" "$OUT/f24_all.txt" && bad X2p "db password in logs" || ok "X2 db password not in logs/output"

# X3 k8s_cron static checks (no cluster)
K=$(cd "$(dirname "$0")/../.." 2>/dev/null && pwd)/k8s_cron; [ -d "$K" ] || K=$RUN/app/k8s_cron
if [ -f "$K/cronjob.yaml" ]; then
  for pat in 'kind: CronJob' 'args: \["remind"\]' 'concurrencyPolicy: Forbid' 'restartPolicy: Never' 'backoffLimit:' 'startingDeadlineSeconds:' 'activeDeadlineSeconds:' 'timeZone:' 'secretRef' 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'automountServiceAccountToken: false'; do
    grep -Eq "$pat" "$K/cronjob.yaml" && ok "X3 cronjob.yaml has '$pat'" || bad "X3 $pat" "missing"; done
  grep -Eq 'image: .*:latest$' "$K/cronjob.yaml" && bad X3i "image uses :latest" || ok "X3 image pinned (not :latest)"
  grep -Eqi 'postgres://|password:|[^a-z]token:|apikey|BEGIN' "$K/cronjob.yaml" && bad X3s "inline secret-looking value" || ok "X3 no inline secrets"
  grep -q 'REMINDER_LOCAL_CRON' "$K/README.md" && grep -q 'remind' "$K/README.md" && grep -Eq 'non-zero|exit' "$K/README.md" && ok "X3 README documents LOCAL_CRON=false, remind, exit codes" || bad X3r
  docker run --rm -v "$K":/k:ro ghcr.io/yannh/kubeconform:v0.8.0 -strict -summary -kubernetes-version 1.30.0 /k/cronjob.yaml >"$OUT/kubeconform.out" 2>&1 \
    && ok "X3 kubeconform: valid CronJob (k8s 1.30 schema)" || bad X3k "$(tail -3 "$OUT/kubeconform.out")"
else bad X3 "k8s_cron/cronjob.yaml not found"; fi
# X4 the QA stack has no route to the internet (internal network): DNS and direct-IP probes from the app and db containers fail
for svc in app db; do
  $DC exec -T $svc sh -c 'nslookup example.com >/dev/null 2>&1' && bad "X4 $svc resolved an external name" || ok "X4 $svc: external DNS fails"
  $DC exec -T $svc sh -c 'wget -T 3 -q -O /dev/null http://1.1.1.1 >/dev/null 2>&1' && bad "X4 $svc reached 1.1.1.1" || ok "X4 $svc: direct IP 1.1.1.1 unreachable"
done
[ "$(docker network inspect -f '{{.Internal}}' "$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$($DC ps -q app)" | cut -d' ' -f1)")" = true ] && ok "X4 app's network is internal" || bad X4n
[ "$($DC exec -T app sh -c 'ip route 2>/dev/null | grep -c "^default"')" = 0 ] && ok "X4 app has no default route" || bad X4r
serve   # leave the app on defaults
