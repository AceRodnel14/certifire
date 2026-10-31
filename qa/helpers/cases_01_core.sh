#!/usr/bin/env bash
# F1-F3, F7-F9, F11-F16 core API cases. Needs run.sh env (BASE, OUT, DC, QA). Test secrets are throwaway.
source "$(dirname "$0")/lib.sh"
ADM=$OUT/adm.jar; U1=$OUT/u1.jar; U2=$OUT/u2.jar; NOJAR=/dev/null
APW="qa-Admin-pass-1"; P1="qa-User1-pass-1"; P2="qa-User2-pass-1"
echo "$APW $P1 $P2" | tr ' ' '\n' > "$OUT/secrets-to-grep.txt"   # E3 greps logs for these
# ---- F1 setup
st $ADM GET /api/state >/dev/null; [ "$(j .setupDone)" = false ] && ok "S1 fresh" || bad S1 "$BODY"
check S5 409 $NOJAR POST /api/register '{"username":"early","password":"abcdefgh1","confirm":"abcdefgh1"}'
check S2a 400 $ADM POST /api/setup '{"password":"short","confirm":"short"}'
check S2b 400 $ADM POST /api/setup "{\"password\":\"$APW\",\"confirm\":\"x$APW\"}"
st $ADM POST /api/setup "{\"password\":\"$APW\",\"confirm\":\"$APW\"}" >/dev/null
MK=$(j .masterKey); check S3l 200 $ADM POST /api/login "{\"username\":\"admin\",\"password\":\"$APW\"}"; [ ${#MK} = 20 ] && ok "S3 master key len 20" || bad S3 "len ${#MK}"
echo "$MK" | grep -qE '[0Ool1I]' && bad S3b "ambiguous chars in master key" || ok "S3b unambiguous alphabet (basic)"
echo "$MK" > "$OUT/.mk"; echo "$MK" >> "$OUT/secrets-to-grep.txt"
check S4 409 $ADM POST /api/setup "{\"password\":\"$APW\",\"confirm\":\"$APW\"}"
check S7a 409 $NOJAR POST /api/register "{\"username\":\"ADMIN\",\"password\":\"$P1\",\"confirm\":\"$P1\"}"
# ---- F2/F3 users
check R1a 201 $U1 POST /api/register "{\"username\":\"alice\",\"password\":\"$P1\",\"confirm\":\"$P1\"}"
check R1b 201 $U2 POST /api/register "{\"username\":\"bob\",\"password\":\"$P2\",\"confirm\":\"$P2\"}"
check R2 409 $NOJAR POST /api/register "{\"username\":\"Alice\",\"password\":\"$P1\",\"confirm\":\"$P1\"}"
for n in ab "$(printf 'a%.0s' {1..33})" "a b" "a/b" "a;b" "é1x" "<script>"; do
  check "R3 [$n]" 400 $NOJAR POST /api/register "$(jq -nc --arg u "$n" --arg p "$P1" '{username:$u,password:$p,confirm:$p}')"; done
check R4 400 $NOJAR POST /api/register '{"username":"carol","password":"1234567","confirm":"1234567"}'
S=$(curl -s -o /dev/null -w '%{http_code}' -b $U1 "$BASE/api/admin/users"); [[ $S =~ ^(403|404)$ ]] && ok "R5 non-admin admin API ($S)" || bad R5 "got $S"
check D6 401 $NOJAR GET /api/certs
# ---- headers (O8)
H=$(curl -s -D- -o /dev/null "$BASE/")
echo "$H" | grep -qi "content-security-policy: default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'" && ok "O8 CSP" || bad O8 "$H"
echo "$H" | grep -qi 'x-content-type-options: nosniff' && ok "O8 nosniff" || bad O8n
echo "$H" | grep -qi 'referrer-policy: no-referrer' && ok "O8 referrer" || bad O8r
echo "$H" | grep -qi '^access-control' && bad O8c "CORS header present" || ok "O8 no CORS"
S=$(curl -s -o /dev/null -w '%{http_code}' -b $U1 -X POST -H 'Content-Type: application/json' -H 'Origin: https://evil.example.test' -d '{}' "$BASE/api/logout")
[[ $S =~ ^(400|403)$ ]] && ok "L8 foreign Origin rejected ($S)" || bad L8 "got $S"
# ---- F7 create
mk() { jq -nc --arg n "$1" --argjson h "$2" --argjson b "${3:-4096}" --argjson d "${4:-825}" --argjson s "${5:-{\}}" '{name:$n,hosts:$h,bits:$b,days:$d,subject:$s}'; }
H1='["app.example.com","*.k8s.example.test","192.0.2.10","2001:db8::10"]'
st $U1 POST /api/certs "$(mk web1 "$H1" 4096 825 '{"C":"us","O":"Home Lab","OU":"IT"}')" >/dev/null; ID1=$(j .id)
[ -n "$ID1" ] && [ "$ID1" != null ] && ok "G1 create 4096/825" || bad G1 "$BODY"
bash "$QA/helpers/certcheck.sh" $U1 "$ID1" "$OUT/c1" > "$OUT/c1.txt" 2>&1 && ok "G1d key matches, CA:TRUE pathlen:0, self-verify" || bad G1d "$(cat $OUT/c1.txt)"
T=$OUT/c1/text.txt
grep -q 'DNS:app.example.com' $T && grep -q 'DNS:\*.k8s.example.test' $T && grep -q 'IP Address:192.0.2.10' $T && grep -qi 'IP Address:2001:DB8:0:0:0:0:0:10' $T && ok "G1a SANs" || bad G1a "$(grep -A1 'Alternative' $T)"
grep -q 'Subject:.*CN *= *app.example.com' $T && grep -q 'C *= *US' $T && ok "G1 CN and C uppercased" || bad G1cn "$(grep Subject: $T)"
grep -q 'Digital Signature, Key Encipherment, Certificate Sign' $T && grep -q 'TLS Web Server Authentication' $T && grep -q 'Subject Key Identifier' $T && ok "G1b KU/EKU/SKI" || bad G1b
grep -B0 -A0 'Basic Constraints: critical' $T >/dev/null && ok "G1b BC critical" || bad G1bc
grep -q 'Public-Key: (4096 bit)' $T && ok "G1c 4096" || bad G1c
NA=$(openssl x509 -in $OUT/c1/c.crt -noout -enddate | cut -d= -f2); D=$(( ($(date -u -d "$NA" +%s)-$(date -u +%s))/86400 ))
[[ $D -ge 824 && $D -le 825 ]] && ok "G1c notAfter ~825d ($D)" || bad G1c "$D"
grep -q 'attachment; filename="web1.crt"' $OUT/c1/crt.hdr && grep -q 'attachment; filename="web1.key"' $OUT/c1/key.hdr && grep -q 'attachment; filename="web1.cnf"' $OUT/c1/cnf.hdr && ok "D3 filenames" || bad D3
grep -qi 'cache-control: no-store' $OUT/c1/key.hdr && ok "D3 no-store" || bad D3ns
grep -qE '^(ST|L) ' $OUT/c1/c.cnf && bad G5 "empty subject fields not omitted" || ok "G5 empty subject omitted"
diff <(grep -E '^(DNS|IP)\.' $OUT/c1/c.cnf | sed 's/ *= */=/') <(printf 'DNS.1=app.example.com\nDNS.2=*.k8s.example.test\nIP.1=192.0.2.10\nIP.2=2001:db8::10\n') >/dev/null && ok "G5 alt_names numbering" || bad G5a "$(grep -E '^(DNS|IP)' $OUT/c1/c.cnf)"
for b in 2048 3072; do st $U1 POST /api/certs "$(mk "k$b" '["a.example.com"]' $b 30)" >/dev/null; i=$(j .id)
  bash "$QA/helpers/certcheck.sh" $U1 "$i" "$OUT/c$b" >/dev/null 2>&1; grep -q "Public-Key: ($b bit)" $OUT/c$b/text.txt && ok "G2 $b" || bad G2 "$b"; done
st $U1 POST /api/certs "$(mk dd '[" A.Example.com ","a.example.com","B.example.com","2001:DB8::0010"]' 2048 5)" >/dev/null; i=$(j .id)
st $U1 GET /api/certs/$i >/dev/null
[ "$(jq -c .hosts <<<"$BODY")" = '["a.example.com","b.example.com","2001:db8::10"]' ] && ok "G6/G3 normalise+dedupe" || bad G6 "$(jq -c .hosts <<<"$BODY")"
check G7a 201 $U1 POST /api/certs "$(mk h1 '["*.homelab"]' 2048 5)"
check G7b 201 $U1 POST /api/certs "$(mk h2 '["*.k8s.homelab"]' 2048 5)"
check G9 201 $U1 POST /api/certs "$(mk web1 '["a.example.com"]' 2048 5)"
# ---- F9 rejections
CNT0=$(psql_q "select count(*) from certs")
rej() { check "$1" 400 $U1 POST /api/certs "$2"; }
L64=$(printf 'a%.0s' {1..65}); L63=$(printf 'a%.0s' {1..63})
H254="$(printf 'a%.0s' {1..60}).$(printf 'b%.0s' {1..60}).$(printf 'c%.0s' {1..60}).$(printf 'd%.0s' {1..60}).$(printf 'e%.0s' {1..8}).com"
rej V1a "$(mk n '[]')"
rej V1b "$(mk n "$(jq -nc '[range(101)|"h\(.).example.com"]')")"
for h in 'a.*.b' '*foo.b' '**.b' '*' 'foo*.b' '*.' 'foo_bar.example.com' "$L64.example.com" 'a..b' '-a.com' 'a-.com' 'a.com.' 'a b.com' 'bücher.example' $'a.com\nDNS.2=evil' 'a.com;rm' '$(id).com' '${ENV::HOME}' 'a.com#' 'a"b.com' '../x' 'a.com/..' '[x]' '-config'; do
  rej "V2/3/5 [$h]" "$(mk n "$(jq -nc --arg h "$h" '[$h]')" 2048 5)"; done
rej V3-254 "$(mk n "$(jq -nc --arg h "$H254" '[$h]')" 2048 5)"
LONGCN="$L63.$(printf 'b%.0s' {1..63}).example.com"
rej V4 "$(mk n "$(jq -nc --arg h "$LONGCN" '[$h]')" 2048 5)"
for n in '' '.hidden' 'a/b' '../x' 'a b' 'a;b' "$(printf 'a%.0s' {1..65})" '-x' $'a\nb' 'x"y'; do rej "V6 [$n]" "$(mk "$n" '["a.com"]' 2048 5)"; done
for f in C ST L O OU; do for v in '$x' '$(id)' '${x}' '#x' 'a\b' 'a"b' 'a;b' 'a=b' $'a\nb' '[ v3_req ]' "$L64" '..\x'; do
  rej "V7 $f [$v]" "$(mk n '["a.com"]' 2048 5 "$(jq -nc --arg f $f --arg v "$v" '{($f):$v}')")"; done; done
for c in U USA 1A ü; do rej "V7 C=$c" "$(mk n '["a.com"]' 2048 5 "$(jq -nc --arg v "$c" '{C:$v}')")"; done
for b in 1024 8192 -1 '"2048"' 2048.5; do rej "V8 bits $b" "$(jq -nc --argjson b "$b" '{name:"n",hosts:["a.com"],bits:$b,days:5,subject:{}}')"; done
for d in 826 -1 1.5 '"10"'; do rej "V9 days $d" "$(jq -nc --argjson d "$d" '{name:"n",hosts:["a.com"],bits:2048,days:$d,subject:{}}')"; done
rej V10a '{"name":"n","hosts":"a.com","bits":2048,"days":5}'; rej V10b '{"name":5,"hosts":["a.com"],"bits":2048,"days":5}'; rej V10c 'null'
check V12a 404 $U1 POST /api/certs "$(jq -c '.renewOf="00000000-0000-4000-8000-000000000000"' <<<"$(mk n '["a.com"]' 2048 5)")"
CNT1=$(psql_q "select count(*) from certs"); [ "$CNT0" = "$CNT1" ] && ok "V rejected input created no rows" || bad Vrows "$CNT0 -> $CNT1"
check V1c 201 $U1 POST /api/certs "$(mk n "$(jq -nc '[range(100)|"h\(.).example.com"]')" 2048 5)"
check V3-63 201 $U1 POST /api/certs "$(mk n "$(jq -nc --arg h "$L63.example.com" '["a.com",$h]')" 2048 5)"
# ---- F11 list
st $U1 GET /api/certs >/dev/null; [ "$(j '.[0].id')" != "$ID1" ] && ok "D1 newest first" || bad D1
jq -e --arg id $ID1 '.[]|select(.id==$id)|(.daysLeft>=824 and .bits==4096)' <<<"$BODY" >/dev/null && ok "D1 fields" || bad D1f
# ---- F13 renew
st $U1 POST /api/certs/$ID1/renew >/dev/null; RN=$(j .id)
[ "$(j .name)" = web1 ] && ok "N1 renew keeps name" || bad N1 "$BODY"
bash "$QA/helpers/certcheck.sh" $U1 $RN "$OUT/r1" >/dev/null 2>&1
[ "$(pubhash_cert $OUT/r1/c.crt)" != "$(pubhash_cert $OUT/c1/c.crt)" ] && ok "N1 new key" || bad N1key
st $U1 GET /api/certs/$ID1 >/dev/null; [ "$(j .renewedBy)" = "$RN" ] && ok "N1 old renewedBy set" || bad N1rb "$BODY"
check N2 409 $U1 POST /api/certs/$ID1/renew
check N4 409 $U1 POST /api/certs "$(jq -c --arg o $ID1 '.renewOf=$o' <<<"$(mk web1 '["a.com"]' 2048 5)")"
# ---- F14 Edit & Renew
st $U1 POST /api/certs "$(mk e1 '["a.example.com"]' 2048 10)" >/dev/null; E1=$(j .id)
st $U1 POST /api/certs "$(jq -c --arg o $E1 '.renewOf=$o' <<<"$(mk e1 '["z.example.com","a.example.com"]' 3072 20)")" >/dev/null; E2=$(j .id)
st $U1 GET /api/certs/$E2 >/dev/null; [ "$(j .bits)" = 3072 ] && [ "$(j '.hosts|length')" = 2 ] && ok "N3 edited values used" || bad N3 "$BODY"
st $U1 GET /api/certs/$E1 >/dev/null; [ "$(j .renewedBy)" = "$E2" ] && ok "N3 old marked" || bad N3m
# ---- F15 delete
check N5 204 $U1 DELETE /api/certs/$E2
st $U1 GET /api/certs/$E1 >/dev/null; [ "$(j .renewedBy)" = null ] && ok "N5 old un-marked" || bad N5u "$BODY"
check N6 404 $U1 DELETE /api/certs/$E2
# ---- F16 isolation (U2 against U1's cert)
for ep in "GET /api/certs/$ID1" "GET /api/certs/$ID1/files/crt" "GET /api/certs/$ID1/files/key" "GET /api/certs/$ID1/files/cnf" "POST /api/certs/$ID1/renew" "DELETE /api/certs/$ID1"; do
  check "ISO [$ep]" 404 $U2 ${ep%% *} ${ep#* }; done
check ISO-renewOf 404 $U2 POST /api/certs "$(jq -c --arg o $ID1 '.renewOf=$o' <<<"$(mk web1 '["a.com"]' 2048 5)")"
check ISO-admin 404 $ADM GET /api/certs/$ID1
st $U2 GET /api/certs >/dev/null; jq -e --arg id $ID1 'map(.id)|index($id)|not' <<<"$BODY" >/dev/null && ok "ISO list" || bad ISOl
echo "$ID1" > "$OUT/.id1"
