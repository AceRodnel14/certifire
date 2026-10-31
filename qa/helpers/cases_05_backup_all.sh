#!/usr/bin/env bash
# F23 POST /api/backup-all. Runs last. Requests arrive from the Docker gateway (172.x) inside the container.
# The allow-list is switched by recreating the app container (DB volume stays): QA_BACKUP_ALL_IPS=... $DC up -d --no-deps app
source "$(dirname "$0")/lib.sh"
MK=$(cat "$OUT/.mk"); BP=$(openssl rand -hex 12)   # 24 chars, throwaway per run
printf '%s\n' "$BP" >> "$OUT/secrets-to-grep.txt"
BKD=$OUT/bk; mkdir -p "$BKD"; PWU="qa-Backup-pass-1"
wait_up() { for _ in $(seq 1 40); do [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/readyz")" = 200 ] && return 0; sleep 1; done; return 1; }
snap() { $DC logs --no-color app >> "$OUT/bk_logs.txt" 2>&1; }   # recreate drops container logs; keep them
setips() { snap; QA_BACKUP_ALL_IPS=$1 $DC up -d --no-deps app >/dev/null 2>&1; wait_up || bad "BK setips" "app not ready with '$1'"; }
# bk METHOD CT BODY [extra curl args] -> STATUS, BODY, headers in $OUT/bkh.tmp (no Origin header, like a CronJob)
bk() { local m=$1 ct=$2 d=$3; shift 3
  STATUS=$(curl -s -o "$OUT/bkb.tmp" -D "$OUT/bkh.tmp" -w '%{http_code}' -X "$m" ${ct:+-H "Content-Type: $ct"} ${d:+-d "$d"} "$@" "$BASE/api/backup-all"); BODY=$(cat "$OUT/bkb.tmp"); }
want() { [ "$STATUS" = "$2" ] && ok "$1 ($STATUS)" || bad "$1" "want $2 got $STATUS body=${BODY:0:200}"; }
good() { jq -nc --arg k "$MK" --arg p "$BP" '{masterKey:$k,backupPassword:$p}'; }
J=application/json

# ---- fixtures: the E5 tamper in cases_02 left one cert undecryptable; remove it (FK clears renewed_by). Then known users.
[ -f "$OUT/.id1" ] && psql_q "delete from certs where id='$(cat "$OUT/.id1")'" >/dev/null
check BK0a 201 $OUT/bkf.jar POST /api/register "{\"username\":\"bkfull\",\"password\":\"$PWU\",\"confirm\":\"$PWU\"}"
check BK0b 201 $OUT/bke.jar POST /api/register "{\"username\":\"bkempty\",\"password\":\"$PWU\",\"confirm\":\"$PWU\"}"
printf '%s\n' "$PWU" >> "$OUT/secrets-to-grep.txt"
mkc() { st $OUT/bkf.jar POST /api/certs "{\"name\":\"$1\",\"hosts\":[\"$1.example.com\"],\"bits\":2048,\"days\":30,\"subject\":{}}" >/dev/null; j .id; }
OLD=$(mkc dup); mkc solo >/dev/null
st $OUT/bkf.jar POST /api/certs/$OLD/renew >/dev/null; NEW=$(j .id)
[ -n "$NEW" ] && [ "$NEW" != null ] && ok "BK0c fixtures: dup renewed ($OLD -> $NEW)" || bad BK0c "$BODY"

# ---- 1 disabled
setips ""
LEN=$(curl -s "$BASE/api/nope"); LEN_S=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/nope")
bk POST $J "$(good)"; want BK1a 404 ; [ "$BODY" = "$LEN" ] && [ "$LEN_S" = 404 ] && ok "BK1a body equals unknown /api path" || bad BK1a "body '$BODY' vs '$LEN'"
bk GET "" ""; want BK1b 404
bk POST "" ""; want BK1c 404
# ---- 2 not allowed
setips "192.0.2.0/24"
bk POST $J "$(good)"; want BK2a 404
bk GET "" ""; want BK2b 404
# ---- 3 allowed (Docker bridge ranges)
setips "172.16.0.0/12"
bk GET "" ""; want BK3a 404
bk PUT $J "$(good)"; want BK3a2 404
for h in X-Forwarded-For X-Forwarded-Host X-Forwarded-Proto X-Real-Ip Forwarded; do bk POST $J "$(good)" -H "$h: 203.0.113.9"; want "BK3b $h" 404; done
bk POST text/plain "$(good)"; want BK3c 415
bk POST "" "$(good)"; want BK3c2 415
bk POST $J '{bad'; want BK3d 400
bk POST $J "$(jq -nc --arg p "$BP" '{backupPassword:$p}')"; want BK3e 400
bk POST $J "$(jq -nc --arg k "   " --arg p "$BP" '{masterKey:$k,backupPassword:$p}')"; want BK3e2 400
bk POST $J "$(jq -nc --arg k "$MK" '{masterKey:$k,backupPassword:"123456789012345"}')"; want BK3f 400
bk POST $J "$(jq -nc --arg k "$MK" '{masterKey:$k}')"; want BK3g 400
bk POST $J "$(jq -nc --arg k "$MK" '{masterKey:$k,backupPassword:"ééééééééééééééé"}')"; want BK3h 400   # 15 runes, 30 bytes
bk POST $J "$(jq -nc --arg k "$MK" '{masterKey:$k,backupPassword:"éééééééééééééééé"}')" ; [ "$STATUS" = 200 ] && ok "BK3i 16 runes accepted (rune count, not bytes)" || bad BK3i "got $STATUS"
head -c 70000 /dev/zero | tr '\0' a > "$OUT/big.tmp"
STATUS=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Content-Type: $J" --data-binary @"$OUT/big.tmp" "$BASE/api/backup-all"); [[ $STATUS =~ ^(400|413)$ ]] && ok "BK3j body over 64 KiB rejected ($STATUS)" || bad BK3j "got $STATUS"
# ---- 4 wrong key, throttle
WK=$(jq -nc --arg p "$BP" '{masterKey:"WRONGWRONGWRONGWRONG",backupPassword:$p}')
for n in 1 2 3 4 5; do bk POST $J "$WK"; want "BK4 wrong key #$n" 401; done
bk POST $J "$WK"; want "BK4 6th wrong key" 429
bk POST $J "$(good)"; want "BK4b right key while throttled" 429
# ---- 5 recovery
snap; QA_BACKUP_ALL_IPS="172.16.0.0/12" $DC up -d --force-recreate --no-deps app >/dev/null 2>&1; wait_up
bk POST $J "$(good)"; want BK5a 200; cp "$OUT/bkb.tmp" "$BKD/backup.zip"
grep -qi '^content-type: application/zip' "$OUT/bkh.tmp" && ok "BK5b content-type application/zip" || bad BK5b "$(cat "$OUT/bkh.tmp")"
grep -qiE '^content-disposition: attachment; filename="certifire-backup-all_[0-9]{4}-[0-9]{2}-[0-9]{2}\.zip"' "$OUT/bkh.tmp" && ok "BK5c content-disposition" || bad BK5c "$(grep -i disposition "$OUT/bkh.tmp")"
grep -qi '^cache-control:.*no-store' "$OUT/bkh.tmp" && ok "BK5d no-store" || echo "NOTE BK5d no Cache-Control: no-store (informational)"
# ---- 6 contents
BK=$BKD/backup.zip; X1=$BKD/x1; X2=$BKD/x2; mkdir -p "$X1" "$X2"
7z x -p"WRONGPASSWORD-123456" -o"$BKD/xw" "$BK" >/dev/null 2>&1 && bad BK6a "wrong password opened archive" || ok "BK6a wrong password fails"
7z x -p"$BP" -o"$X1" "$BK" >/dev/null 2>&1 && ok "BK6b right password extracts" || bad BK6b "7z failed"
[ "$(ls -A "$X1")" = "certifire-all.zip" ] && ok "BK6c outer holds exactly certifire-all.zip" || bad BK6c "$(ls -A "$X1")"
7z x -o"$X2" "$X1/certifire-all.zip" >/dev/null 2>&1 && ok "BK6d inner is a plain zip (no password)" || bad BK6d
GOT=$(ls "$X2" | sed 's/\.zip$//' | sort); WANT=$(psql_q "select username from users order by username" | sort)
[ "$GOT" = "$WANT" ] && ok "BK6e one <user>.zip per account" || bad BK6e "got [$GOT] want [$WANT]"
echo "$GOT" | grep -qx bkempty && ok "BK6f user with no certs present" || bad BK6f
for u in $GOT; do mkdir -p "$X2/_$u"; 7z x -o"$X2/_$u" "$X2/$u.zip" >/dev/null 2>&1 || bad "BK6g $u" "unzip failed"; done
[ -z "$(ls -A "$X2/_bkempty")" ] && ok "BK6h bkempty zip is empty" || bad BK6h
TOTAL=0; BADK=0; BADI=0
for u in $GOT; do WN=$(psql_q "select count(*) from certs where user_id=(select id from users where username='$u')"); NC=$(ls "$X2/_$u"/*.crt 2>/dev/null | wc -l); NK=$(ls "$X2/_$u"/*.key 2>/dev/null | wc -l); NF=$(ls "$X2/_$u"/*.cnf 2>/dev/null | wc -l)
  [ "$WN" = "$NC" ] && [ "$WN" = "$NK" ] && [ "$WN" = "$NF" ] || { BADI=$((BADI+1)); bad "BK6i $u" "db=$WN crt=$NC key=$NK cnf=$NF"; }; TOTAL=$((TOTAL+WN))
  for c in "$X2/_$u"/*.crt; do [ -e "$c" ] || continue; b=${c%.crt}; [ "$(pubhash_cert "$c")" = "$(pubhash_key "$b.key" 2>/dev/null)" ] || { BADK=$((BADK+1)); bad "BK6j $u/$(basename "$b")" "key does not match crt"; }; done; done
[ "$BADK" = 0 ] && ok "BK6j all $TOTAL keys match their certs" ; [ "$BADI" = 0 ] && ok "BK6i .cnf/.crt/.key counts match DB for all users"
grep -q 'BEGIN' "$X2/_bkfull/solo.key" && ! grep -q 'ENCRYPTED' "$X2/_bkfull/solo.key" && ok "BK6k key is decrypted PEM" || bad BK6k
[ -f "$X2/_bkfull/dup.crt" ] && [ -f "$X2/_bkfull/dup_$OLD.crt" ] && ok "BK6l renewal dup.* + older dup_<uuid>.*" || bad BK6l "$(ls "$X2/_bkfull")"
[ "$(pubhash_cert "$X2/_bkfull/dup.crt")" != "$(pubhash_cert "$X2/_bkfull/dup_$OLD.crt")" ] && ok "BK6m dup.* is the newest, the two certs differ" || bad BK6m
# ---- 7 logs
snap; LG=$(cat "$OUT/bk_logs.txt"); grep backup-all "$OUT/bk_logs.txt" | head -3 | sed "s/^/LOGSAMPLE /" | cut -c1-250
echo "$LG" | grep -q 'backup-all' && ok "BK7a backup-all log lines present" || bad BK7a
for r in ok denied bad-request bad-key throttled; do echo "$LG" | grep 'backup-all' | grep -qE "result[=\":]+\"?$r" && ok "BK7b result=$r logged" || bad "BK7b $r" "missing"; done
echo "$LG" | grep 'backup-all' | grep -qE 'ip"?[=:]"?172\.' && ok "BK7c peer IP logged (172.x)" || bad BK7c
for s in "$MK" "$BP" "PRIVATE KEY" "WRONGWRONGWRONGWRONG"; do echo "$LG" | grep -qF "$s" && bad "BK7d secret in logs" "[$s]" || ok "BK7d logs lack '${s:0:12}'"; done
# ---- 8 single run (best effort)
snap; QA_BACKUP_ALL_IPS="172.16.0.0/12" $DC up -d --force-recreate --no-deps app >/dev/null 2>&1; wait_up
for n in 1 2; do ( curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Content-Type: $J" -d "$(good)" "$BASE/api/backup-all" > "$OUT/par$n.txt" ) & done; wait
PAR=$(cat "$OUT"/par1.txt "$OUT"/par2.txt | sort | tr '\n' ' ')
[ "$PAR" = "200 429 " ] && ok "BK8 parallel: one 200, one 429" || echo "NOTE BK8 parallel result '$PAR' (best effort; run may have finished before the second arrived)"
# ---- tidy: back to disabled so the stack matches defaults
setips ""; bk POST $J "$(good)"; want BK9 404
