#!/usr/bin/env bash
# F2 throttle/restart, F4 change password, F5 admin reset, F17 at rest, F21 theme, F22 ops. Runs after cases_01.
source "$(dirname "$0")/lib.sh"
ADM=$OUT/adm.jar; U1=$OUT/u1.jar; U2=$OUT/u2.jar; N=/dev/null
P1="qa-User1-pass-1"; P1N="qa-User1-new-2"; P2="qa-User2-pass-1"; P2N="qa-User2-reset-3"; PD="qa-Dave-pass-1"
MK=$(cat "$OUT/.mk"); ID1=$(cat "$OUT/.id1"); printf '%s\n' "$P1N" "$P2N" "$PD" >> "$OUT/secrets-to-grep.txt"
wait_ready() { for i in $(seq 1 40); do [ "$(curl -s -o /dev/null -w '%{http_code}' $BASE/readyz)" = 200 ] && return 0; sleep 1; done; return 1; }
getkeyhash() { curl -s -b "$1" "$BASE/api/certs/$ID1/files/key" > "$OUT/kk.pem"; pubhash_key "$OUT/kk.pem" 2>/dev/null; }
WANT=$(pubhash_cert "$OUT/c1/c.crt")
# ---- F2 login, throttle on a dedicated user so alice/bob stay usable
check L0 201 $OUT/dave.jar POST /api/register "{\"username\":\"dave\",\"password\":\"$PD\",\"confirm\":\"$PD\"}"
check L1 204 $OUT/dave.jar POST /api/logout
curl -s -D- -o /dev/null -X POST -H 'Content-Type: application/json' -H "Origin: $BASE" -d "{\"username\":\"dave\",\"password\":\"$PD\"}" "$BASE/api/login" > "$OUT/login.hdr"
grep -qi 'set-cookie: certifire_session=.*HttpOnly' "$OUT/login.hdr" && grep -qi 'samesite=lax' "$OUT/login.hdr" && ok "L1 cookie flags" || bad L1 "$(grep -i set-cookie "$OUT/login.hdr")"
grep -qi 'set-cookie:.*secure' "$OUT/login.hdr" && bad L1s "Secure on http BASE_URL" || ok "L1 no Secure on http"
st $N POST /api/login '{"username":"dave","password":"wrong-pass-1"}'; L2a=$STATUS; B1=$BODY
st $N POST /api/login '{"username":"nobody","password":"wrong-pass-1"}'; L2b=$STATUS
[ "$L2a" = 401 ] && [ "$L2b" = 401 ] && ok "L2 generic 401" || bad L2 "$L2a/$L2b"
[ "$B1" = "$BODY" ] && ok "L2 identical body" || bad L2b "$B1 vs $BODY"
for i in 1 2 3 4; do st $N POST /api/login '{"username":"dave","password":"wrong-pass-1"}' >/dev/null; done   # failures 3-6 incl. earlier; see note
st $N POST /api/login '{"username":"dave","password":"wrong-pass-1"}'; S=$STATUS; [ "$S" = 429 ] && ok "L3 429 after 5 failures" || bad L3 "got $S (note: 5 failures then 429 expected; 6th = 429)"
check L3b 429 $N POST /api/login "{\"username\":\"dave\",\"password\":\"$PD\"}"
check L3c 200 $OUT/bobx.jar POST /api/login "{\"username\":\"bob\",\"password\":\"$P2\"}"
check L7a 400 $N POST /api/login '{bad json'
S=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "Origin: $BASE" -d "$(head -c 70000 /dev/zero | tr '\0' a)" "$BASE/api/login"); [[ $S =~ ^(400|413)$ ]] && ok "L7b body >64KiB ($S)" || bad L7b "$S"
# ---- F2 restart
$DC restart app >/dev/null 2>&1; wait_ready
check L5a 200 $U1 GET /api/certs
[ "$(getkeyhash $U1)" = "$WANT" ] && ok "L5 key decrypts after restart" || bad L5
# ---- F4 change password: session A=$U1 (existing), B=$SB
SB=$OUT/u1b.jar; check P0 200 $SB POST /api/login "{\"username\":\"alice\",\"password\":\"$P1\"}"
check P1 401 $U1 POST /api/account/password "{\"currentPassword\":\"nope-nope-1\",\"password\":\"$P1N\",\"confirm\":\"$P1N\"}"
check P7a 400 $U1 POST /api/account/password "{\"currentPassword\":\"$P1\",\"password\":\"short\",\"confirm\":\"short\"}"
check P7b 400 $U1 POST /api/account/password "{\"currentPassword\":\"$P1\",\"password\":\"$P1N\",\"confirm\":\"x$P1N\"}"
check P2 204 $U1 POST /api/account/password "{\"currentPassword\":\"$P1\",\"password\":\"$P1N\",\"confirm\":\"$P1N\"}"
check P3 401 $N POST /api/login "{\"username\":\"alice\",\"password\":\"$P1\"}"
check P4 200 $OUT/u1c.jar POST /api/login "{\"username\":\"alice\",\"password\":\"$P1N\"}"
check P5a 401 $SB GET /api/certs
check P5b 200 $U1 GET /api/certs
[ "$(getkeyhash $OUT/u1c.jar)" = "$WANT" ] && ok "P6 key decrypts with new login" || bad P6
# ---- F5 admin
st $ADM GET /api/admin/users >/dev/null; jq -e '.[]|select(.username=="alice")|.certCount>0' <<<"$BODY" >/dev/null && ok "AD1 list with certCount" || bad AD1 "$BODY"
BID=$(jq -r '.[]|select(.username=="bob")|.id' <<<"$BODY")
check AD2 401 $N GET /api/admin/users
check AD4a 403 $ADM POST /api/admin/users/$BID/password "{\"masterKey\":\"WRONGWRONGWRONGWRONG\",\"password\":\"$P2N\",\"confirm\":\"$P2N\"}"
S=$(curl -s -o /dev/null -w '%{http_code}' -b $U2 -X POST -H 'Content-Type: application/json' -H "Origin: $BASE" -d "{\"masterKey\":\"$MK\",\"password\":\"$P2N\",\"confirm\":\"$P2N\"}" "$BASE/api/admin/users/$BID/password")
[[ $S =~ ^(403|404)$ ]] && ok "AD5 non-admin cannot reset ($S)" || bad AD5 "$S"
check AD6 404 $ADM POST /api/admin/users/999999/password "{\"masterKey\":\"$MK\",\"password\":\"$P2N\",\"confirm\":\"$P2N\"}"
# bob gets a cert to verify decryption after reset
st $U2 POST /api/certs '{"name":"bobcert","hosts":["b.example.com"],"bits":2048,"days":30,"subject":{}}' >/dev/null; BC=$(j .id)
curl -s -b $U2 "$BASE/api/certs/$BC/files/crt" > "$OUT/bob.crt"; BW=$(pubhash_cert "$OUT/bob.crt")
check AD3 204 $ADM POST /api/admin/users/$BID/password "{\"masterKey\":\"$MK\",\"password\":\"$P2N\",\"confirm\":\"$P2N\"}"
check AD3a 401 $N POST /api/login "{\"username\":\"bob\",\"password\":\"$P2\"}"
check AD3b 200 $OUT/bob2.jar POST /api/login "{\"username\":\"bob\",\"password\":\"$P2N\"}"
check AD3c 401 $U2 GET /api/certs
curl -s -b $OUT/bob2.jar "$BASE/api/certs/$BC/files/key" > "$OUT/bob.key"; [ "$(pubhash_key $OUT/bob.key)" = "$BW" ] && ok "AD3 bob key decrypts after admin reset" || bad AD3k
# ---- F21 theme
for t in '{"mode":"dark","hue":200}' '{"mode":"light","hue":0}' '{"mode":"auto","hue":359}' '{"mode":"auto","hue":null}'; do check "TH1 $t" 204 $OUT/u1c.jar PUT /api/account/theme "$t"; done
check TH1x 204 $OUT/u1c.jar PUT /api/account/theme '{"mode":"dark","hue":200}'
for t in '{"mode":"blue","hue":1}' '{"mode":"dark","hue":360}' '{"mode":"dark","hue":-1}' '{"mode":"dark","hue":1.5}' '{"mode":"dark","hue":"x"}'; do check "TH2 $t" 400 $OUT/u1c.jar PUT /api/account/theme "$t"; done
check TH4 401 $N PUT /api/account/theme '{"mode":"dark","hue":1}'
check TH3a 200 $OUT/u1d.jar POST /api/login "{\"username\":\"alice\",\"password\":\"$P1N\"}"
st $OUT/u1d.jar GET /api/state >/dev/null; [ "$(j .user.theme.mode)" = dark ] && ok "TH3 alice dark persists" || bad TH3 "$BODY"
st $OUT/bob2.jar GET /api/state >/dev/null; [ "$(j .user.theme.mode)" != dark ] && ok "TH3 bob unaffected" || bad TH3b "$BODY"
# ---- F17 at rest
KE=$(psql_q "select count(*) from certs where encode(key_enc,'escape') like '%PRIVATE KEY%' or encode(key_enc,'escape') like '%BEGIN%'")
[ "$KE" = 0 ] && ok "E1 key_enc has no PEM text" || bad E1 "$KE rows"
DISTINCT=$(psql_q "select count(distinct key_enc) = count(*) from certs"); [ "$DISTINCT" = t ] && ok "E1 key_enc all distinct" || bad E1d
$DC exec -T db sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' > "$OUT/dump.sql" 2>/dev/null
grep -q 'PRIVATE KEY' "$OUT/dump.sql" && bad E2 "PRIVATE KEY in dump" || ok "E2 no private key text in pg_dump"
KB=$(sed -n '2p' "$OUT/kk.pem" | cut -c1-40); grep -qF "$KB" "$OUT/dump.sql" && bad E2b "key base64 fragment in dump" || ok "E2 no key fragment in dump"
for s in "$P1" "$P1N" "$P2" "$P2N" "$PD" "$(cat $OUT/secrets-to-grep.txt | head -1)" "$MK"; do grep -qF "$s" "$OUT/dump.sql" && bad E4 "plaintext secret in dump" ; done; ok "E4 done (no FAIL above = secrets absent from dump)"
# E5 tamper
A=$(psql_q "select id from certs where user_id=(select id from users where username='alice') order by created_at limit 2" | head -2); I1=$(sed -n 1p <<<"$A"); I2=$(sed -n 2p <<<"$A")
psql_q "create temp table t as select 1" >/dev/null 2>&1
psql_q "update certs set key_enc=(select key_enc from certs where id='$I2') where id='$I1'" >/dev/null
S=$(curl -s -o /dev/null -w '%{http_code}' -b $OUT/u1c.jar "$BASE/api/certs/$I1/files/key"); [[ $S =~ ^(500|4..)$ ]] && [ "$S" != 200 ] && ok "E5 swapped key_enc not served ($S)" || bad E5 "got $S"
$DC logs app 2>&1 | grep -qi 'panic' && bad E5p "panic in logs" || ok "E5 no panic"
# (E5 leaves DB tampered for that one row; run is throwaway)
# logs
$DC logs app > "$OUT/app.log" 2>&1
grep -q 'PRIVATE KEY' "$OUT/app.log" && bad E3a "PRIVATE KEY in logs" || ok "E3 no private key in logs"
while read -r s; do [ -n "$s" ] && grep -qF -- "$s" "$OUT/app.log" && bad E3b "secret in logs"; done < "$OUT/secrets-to-grep.txt"; ok "E3 secrets absent from logs (no FAIL above)"
# E6/O9
$DC exec -T app sh -c 'id -u; ls /tmp | wc -l; openssl version; which curl wget psql 2>&1 | wc -l' > "$OUT/img.txt" 2>&1
[ "$(sed -n 1p $OUT/img.txt)" != 0 ] && ok "E6 non-root" || bad E6u; [ "$(sed -n 2p $OUT/img.txt)" = 0 ] && ok "E6 no leftover /tmp" || bad E6t "$(cat $OUT/img.txt)"
sed -n 3p "$OUT/img.txt" | grep -qi openssl && ok "O9 openssl present" || bad O9
# ---- F22 ops
check O1 200 $N GET /healthz; check O2 200 $N GET /readyz; st $N GET /api/info >/dev/null; echo "$BODY" | jq . >/dev/null 2>&1 && ok "O7 info json" || bad O7i
check O7 404 $N GET /api/nope; jq -e .error <<<"$BODY" >/dev/null && ok "O7 json error" || bad O7j "$BODY"
curl -s $BASE/some/route | grep -qi '<html' && ok "O7 SPA fallback" || bad O7s
CID=$($DC ps -q app); $DC stop db >/dev/null 2>&1; sleep 5
a=$(curl -s -o /dev/null -w '%{http_code}' $BASE/healthz); b=$(curl -s -o /dev/null -w '%{http_code}' $BASE/readyz)
[ "$a" = 200 ] && [ "$b" = 503 ] && ok "O3 healthz 200 / readyz 503 with DB down" || bad O3 "$a/$b"
[ "$(docker inspect -f '{{.State.Running}} {{.RestartCount}}' $CID)" = "true 0" ] && ok "O3 app not restarted" || bad O3r
$DC start db >/dev/null 2>&1; wait_ready && ok "O4 recovers after DB returns" || bad O4
[ "$(docker inspect -f '{{.State.Running}} {{.RestartCount}}' $CID)" = "true 0" ] && ok "O4 no app restart needed" || bad O4r
check O4b 200 $OUT/u1c.jar GET /api/certs
for i in 1 2 3; do $DC restart app >/dev/null 2>&1; wait_ready || bad O16 "not ready after restart $i"; done; ok "O16 3 restarts idempotent"
T0=$(date +%s); $DC stop app >/dev/null 2>&1; T1=$(date +%s); [ $((T1-T0)) -lt 10 ] && ok "O13 graceful stop $((T1-T0))s" || bad O13 "$((T1-T0))s"
EC=$(docker inspect -f '{{.State.ExitCode}}' $CID); [ "$EC" = 0 ] && ok "O13 exit 0" || bad O13e "exit $EC"
$DC start app >/dev/null 2>&1; wait_ready
# ---- reviewer-agreed extras
for b in '{"name":"dflt","hosts":["d.example.com"],"subject":{}}' '{"name":"dflt","hosts":["d.example.com"],"bits":null,"days":null,"subject":{}}' '{"name":"dflt","hosts":["d.example.com"],"bits":0,"days":0,"subject":{}}'; do
  st $OUT/u1c.jar POST /api/certs "$b" >/dev/null; DD=$(j .id)
  st $OUT/u1c.jar GET /api/certs/$DD >/dev/null; [ "$(j .bits)" = 4096 ] && [ "$(j .days)" = 825 ] && ok "G10 defaults applied" || bad G10 "$BODY"; done
st $N GET /api/info >/dev/null; [ "$(j .base_url)" != null ] && ok "O7i /api/info base_url" || bad O7i "$BODY"
for i in 1 2 3 4 5; do curl -s -o /dev/null -X POST -H 'Content-Type: application/json' -H "Origin: $BASE" -d '{"username":"Erin","password":"wrong-pass-1"}' $BASE/api/login; done
S=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "Origin: $BASE" -H 'X-Forwarded-For: 203.0.113.9' -d '{"username":"erin","password":"wrong-pass-1"}' $BASE/api/login)
[ "$S" = 429 ] && ok "L9 X-Forwarded-For does not bypass lock" || bad L9 "got $S"
