#!/usr/bin/env bash
# F18 validation, F19 send test, F20 reminders. Uses the local TLS stub (compose.qa.yml); no real webhooks.
source "$(dirname "$0")/lib.sh"
W=$OUT/w.jar; X=$OUT/wx.jar
PW="qa-Hook-pass-1"; printf '%s\n' "$PW" >> "$OUT/secrets-to-grep.txt"
check W0 201 $W POST /api/register "{\"username\":\"hooky\",\"password\":\"$PW\",\"confirm\":\"$PW\"}"
check W0b 201 $X POST /api/register "{\"username\":\"other\",\"password\":\"$PW\",\"confirm\":\"$PW\"}"
TOK=$(printf 'A%.0s' {1..64}); DID=123456789012345678
DU="https://discord.com/api/webhooks/$DID/$TOK"
TU="https://qa.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/abc/triggers/manual/paths/invoke?api-version=1&sig=FAKESIG1234"
stub_mode() { curl -s -X POST -d "$1" $STUBCTL/__mode >/dev/null; }
stub_reset() { curl -s -X POST $STUBCTL/__reset >/dev/null; }
stub_n()    { curl -s $STUBCTL/__requests | jq 'length'; }
put() { jq -nc --arg u "$1" '{url:$u}'; }
rejw() { check "$1" 400 $W PUT /api/account/webhooks/$2 "$(put "$3")"; }
# ---- F18 accepted
check WH1 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"; echo "$BODY" | grep -q "$TOK" && bad WH1s "token echoed" || ok "WH1 token not echoed"
[ "$(j .masked)" = "https://discord.com/…AAAA" ] && ok "WH1 masked format" || bad WH1m "$(j .masked)"
for h in discordapp.com ptb.discord.com canary.discord.com; do
  check "WH2 $h" 200 $W PUT /api/account/webhooks/discord "$(put "https://$h/api/v10/webhooks/$DID/$TOK?thread_id=42")"
  [ "$(psql_q "select url from webhooks where kind='discord' and user_id=(select id from users where username='hooky')")" = "https://discord.com/api/v10/webhooks/$DID/$TOK?thread_id=42" ] && ok "WH2 $h rewritten" || bad WH2r "$h"; done
check WH3 200 $W PUT /api/account/webhooks/teams "$(put "$TU")"
st $W GET /api/account/webhooks >/dev/null; echo "$BODY" | grep -q 'FAKESIG\|'"$TOK" && bad WH3s "secret in GET" || ok "WH3 GET has no full URL"
# ---- F18 rejected
for u in "http://discord.com/api/webhooks/$DID/$TOK" "https://discord.com.evil.test/api/webhooks/$DID/$TOK" "https://evildiscord.com/api/webhooks/$DID/$TOK" \
  "https://user@discord.com/api/webhooks/$DID/$TOK" "https://discord.com:8443/api/webhooks/$DID/$TOK" "https://discord.com/api/webhooks/$DID/$TOK/extra" \
  "https://discord.com/api/webhooks/123/$TOK" "https://discord.com/api/webhooks/$DID/short" "https://discord.com/api/webhooks/$DID/$TOK?foo=1" \
  "https://discord.com/api/webhooks/$DID/$TOK#frag" "https://192.0.2.1/api/webhooks/$DID/$TOK" "https://[2001:db8::1]/api/webhooks/$DID/$TOK" "" \
  "https://discord.com/api/webhooks/$DID/$(printf 'a%.0s' {1..2100})"; do rejw "WH4 [${u:0:60}]" discord "$u"; done
for u in "${TU/&sig=FAKESIG1234/}" "${TU/powerautomate/other}" "https://environment.api.powerplatform.com/powerautomate/x?sig=1" \
  "https://x.logic.azure.com/workflows/abc?sig=1" "https://x.webhook.office.com/webhookb2/abc" "https://outlook.office.com/webhook/abc" "https://qa.environment.api.powerplatform.com:8443/powerautomate/x?sig=1"; do rejw "WH5 [${u:0:60}]" teams "$u"; done
rejw WH6 teams "https://example.com/x"
check WH7 404 $W PUT /api/account/webhooks/slack "$(put "$DU")" || true
# ---- F19 send test
check WH8 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"
stub_reset; stub_mode '{"status":204}'
sleep 11; check T1 200 $W POST /api/account/webhooks/discord/test; [ "$(j .outcome)" = delivered ] && ok "T1 delivered" || bad T1 "$BODY"
R=$(curl -s $STUBCTL/__requests | jq -r '.[0].body'); jq -e '.username=="CertiFire" and .allowed_mentions.parse==[] and (.embeds|length==1) and (.embeds[0]|has("title") and has("description") and has("color"))' <<<"$R" >/dev/null && ok "T1 discord payload" || bad T1p "$R"
check T5 429 $W POST /api/account/webhooks/discord/test
stub_reset; stub_mode '{"status":202}'; sleep 11
check T2 200 $W POST /api/account/webhooks/teams/test; [ "$(j .outcome)" = accepted ] && ok "T2 accepted" || bad T2 "$BODY"
R=$(curl -s $STUBCTL/__requests | jq -r '.[0].body'); jq -e '.type=="message" and (.attachments[0].content.version=="1.4") and ([.attachments[0].content.body[]|select(.type=="TextBlock")]|length==2)' <<<"$R" >/dev/null && ok "T2 teams card" || bad T2p "$R"
stub_reset; stub_mode '{"status":404}'; sleep 11
check T3 200 $W POST /api/account/webhooks/discord/test; [ "$(j .outcome)" = broken ] && ok "T3 broken" || bad T3 "$BODY"
st $W GET /api/account/webhooks >/dev/null; [ "$(j .discord.broken)" = true ] && ! j '.discord.brokenReason' | grep -q "$TOK" && ok "T3 broken+reason, no URL" || bad T3b "$BODY"
check T6 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"; [ "$(j .broken)" = false ] && ok "T6 re-save clears broken" || bad T6 "$BODY"
stub_mode '{"status":429,"retryAfter":7}'; sleep 11; check T4 200 $W POST /api/account/webhooks/discord/test; [ "$(j .outcome)" = rate_limited ] && [ "$(j .retryAfter)" = 7 ] && ok "T4 retry info" || bad T4 "$BODY"
stub_mode '{"status":500}'; sleep 11; check T8 200 $W POST /api/account/webhooks/discord/test; [ "$(j .outcome)" = error ] && ok "T8 error not broken" || bad T8 "$BODY"
st $W GET /api/account/webhooks >/dev/null; [ "$(j .discord.broken)" = false ] && ok "T8 not marked broken" || bad T8b
stub_reset; stub_mode '{"redirect":true}'; sleep 11; st $W POST /api/account/webhooks/discord/test >/dev/null; [ "$(j .outcome)" != delivered ] && ok "T9 redirect not followed ($(j .outcome))" || bad T9
stub_reset; stub_mode '{"status":204,"bigBody":true}'; sleep 11; check T10 200 $W POST /api/account/webhooks/discord/test
check T7 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"
check T11 404 $X POST /api/account/webhooks/discord/test
st $X GET /api/account/webhooks >/dev/null; [ "$(j .discord)" = null ] && ok "WH iso other user sees none" || bad WHiso "$BODY"
# ---- F20 reminders
UID_=$(psql_q "select id from users where username='hooky'")
mkc() { st $W POST /api/certs "{\"name\":\"$1\",\"hosts\":[\"$1.example.com\"],\"bits\":2048,\"days\":825,\"subject\":{}}" >/dev/null; j .id; }
setna() { psql_q "update certs set not_after = now() + interval '$2' where id='$1'" >/dev/null; }
restart() { $DC restart app >/dev/null 2>&1; for i in $(seq 1 40); do [ "$(curl -s -o /dev/null -w '%{http_code}' $BASE/readyz)" = 200 ] && break; sleep 1; done; sleep 4; }
psql_q "delete from certs where user_id=$UID_" >/dev/null; psql_q "delete from reminders_sent" >/dev/null
stub_reset; stub_mode '{"status":204}'
check M0 200 $W PUT /api/account/webhooks/teams "$(put "$TU")"   # both kinds configured
declare -A CID
for d in 7 5 3 2 1; do CID[$d]=$(mkc "r$d"); setna "${CID[$d]}" "$d days 1 hour"; done
for d in 0 4 6 8; do c=$(mkc "n$d"); case $d in 0) setna $c "10 hours";; 4) setna $c "4 days 1 hour";; 6) setna $c "6 days 1 hour";; 8) setna $c "8 days 1 hour";; esac; done
restart
# 4/6 are not thresholds in this scheme: expect no message. 5 certs x 2 kinds = 10 messages.
[ "$(stub_n)" = 10 ] && ok "M1/M9 10 messages (5 certs x 2 kinds)" || bad M1 "got $(stub_n)"
for d in 7 5 3 2 1; do curl -s $STUBCTL/__requests | jq -r '.[].body' | grep -q "r$d.example.com" || bad M1d "no message for ${d}d cert"; done
curl -s $STUBCTL/__requests | jq -r '.[].body' | grep -q 'expires in 7 day' && ok "M1 text 'expires in N day(s)'" || bad M1t
curl -s $STUBCTL/__requests | jq -r '.[].body' | grep -q 'PRIVATE KEY' && bad M13 "key in message" || ok "M13 no key material"
restart; [ "$(stub_n)" = 10 ] && ok "M2 no resend after restart" || bad M2 "got $(stub_n)"
[ "$(psql_q "select count(*) from reminders_sent")" = 10 ] && ok "M2 reminders_sent rows" || bad M2r
# M4 expired
stub_reset; psql_q "delete from reminders_sent" >/dev/null; psql_q "delete from certs where user_id=$UID_" >/dev/null
c=$(mkc exp); setna $c "- 2 days"; restart
[ "$(stub_n)" = 2 ] && ok "M4 expired: one per kind" || bad M4 "got $(stub_n)"; curl -s $STUBCTL/__requests | jq -r '.[].body' | grep -q 'expired' && ok "M4 text expired" || bad M4t
restart; [ "$(stub_n)" = 2 ] && ok "M4 same UTC day: no repeat" || bad M4r
psql_q "update reminders_sent set mark='expired:'||to_char((now() at time zone 'utc')::date-1,'YYYY-MM-DD')" >/dev/null; restart
[ "$(stub_n)" = 4 ] && ok "M4 next UTC day: one more" || bad M4n "got $(stub_n)"
# M5 renewed / deleted
stub_reset; psql_q "delete from reminders_sent" >/dev/null; psql_q "delete from certs where user_id=$UID_" >/dev/null
a=$(mkc ren); setna $a "3 days 1 hour"; st $W POST /api/certs/$a/renew >/dev/null; b=$(j .id); setna $b "500 days"; restart
[ "$(stub_n)" = 0 ] && ok "M5 renewed cert: none" || bad M5 "got $(stub_n)"
check M5d 204 $W DELETE /api/certs/$b; restart; [ "$(stub_n)" = 2 ] && ok "M5 renewal deleted: reminders resume" || bad M5r "got $(stub_n)"
# M6 5xx retried
stub_reset; psql_q "delete from reminders_sent" >/dev/null; stub_mode '{"status":500}'; restart
[ "$(psql_q "select count(*) from reminders_sent")" = 0 ] && ok "M6 claim released on 5xx" || bad M6c
stub_reset; stub_mode '{"status":204}'; restart; [ "$(stub_n)" = 2 ] && ok "M6 retried and delivered once per kind" || bad M6 "got $(stub_n)"
# M7 permanent 4xx
stub_reset; psql_q "delete from reminders_sent" >/dev/null; stub_mode '{"status":404}'; restart
st $W GET /api/account/webhooks >/dev/null; [ "$(j .discord.broken)" = true ] && ok "M7 marked broken" || bad M7 "$BODY"
[ "$(psql_q "select count(*) from reminders_sent")" -ge 1 ] && ok "M7 claim kept" || bad M7c
stub_reset; restart; [ "$(stub_n)" = 0 ] && ok "M7 broken webhook skipped" || bad M7s "got $(stub_n)"
# M10 other user without webhook gets nothing; M11 sequential many
c=$(curl -s -X POST -b $X -c $X -H 'Content-Type: application/json' -H "Origin: $BASE" -d '{"name":"x","hosts":["x.example.com"],"bits":2048,"days":825,"subject":{}}' $BASE/api/certs | jq -r .id); setna $c "3 days 1 hour"
psql_q "delete from reminders_sent" >/dev/null; check M7r 200 $W PUT /api/account/webhooks/discord "$(put "$DU")"; check M7r2 200 $W PUT /api/account/webhooks/teams "$(put "$TU")"
stub_reset; stub_mode '{"status":204}'; psql_q "delete from certs where user_id=$UID_" >/dev/null
for i in $(seq 1 20); do c=$(mkc "m$i"); setna $c "5 days 1 hour"; done; restart; sleep 10
[ "$(stub_n)" = 40 ] && ok "M11 20 certs x 2 kinds = 40, no duplicates" || bad M11 "got $(stub_n)"
[ "$(psql_q "select count(*) from reminders_sent")" = 40 ] && ok "M11 rows match" || bad M11r
curl -s $STUBCTL/__requests | jq -r '.[].body' | grep -q 'x.example.com' && bad M10 "other user cert notified" || ok "M10 user without webhook gets none"
$DC logs app 2>&1 | grep -qF "$TOK" && bad E3w "webhook token in logs" || ok "E3 webhook token not in logs"
$DC logs app 2>&1 | grep -q 'FAKESIG' && bad E3t "teams sig in logs" || ok "E3 teams sig not in logs"
