#!/usr/bin/env bash
# F6 CLI reset-password, O15 healthcheck subcommand.
source "$(dirname "$0")/lib.sh"
MK=$(cat "$OUT/.mk"); N=/dev/null; PN="qa-Cli-reset-4"; echo "$PN" >> "$OUT/secrets-to-grep.txt"
# C1: argv forms are not accepted
$DC exec -T app /app reset-password "$MK" >/dev/null 2>&1 </dev/null && bad C1 "accepted master key as argv" || ok "C1 argv master key refused"
# C4: wrong master key / unknown user / mismatch -> non-zero, no change
python3 -I "$QA/helpers/cli_reset.py" "$DC" "WRONGWRONGWRONGWRONG" alice "$PN" > "$OUT/cli_wrong.txt" 2>&1 && bad C4a "wrong master key exit 0" || ok "C4 wrong master key non-zero"
python3 -I "$QA/helpers/cli_reset.py" "$DC" "$MK" nosuchuser "$PN" > "$OUT/cli_nouser.txt" 2>&1 && bad C4b "unknown user exit 0" || ok "C4 unknown user non-zero"
python3 -I "$QA/helpers/cli_reset.py" "$DC" "$MK" alice "$PN" "different-confirm" > "$OUT/cli_mismatch.txt" 2>&1 && bad C4c "mismatch exit 0" || ok "C4 mismatch non-zero"
check C4d 401 $N POST /api/login "{\"username\":\"alice\",\"password\":\"$PN\"}"
# C2/C3: success
SESS=$OUT/u1c.jar
python3 -I "$QA/helpers/cli_reset.py" "$DC" "$MK" alice "$PN" > "$OUT/cli_ok.txt" 2>&1 && ok "C2 reset exit 0" || bad C2 "$(tail -5 $OUT/cli_ok.txt)"
grep -q '^PASS no secret echoed' "$OUT/cli_ok.txt" && ok "C2 prompts hidden" || bad C2h "secret echoed"
check C3a 200 $OUT/u1e.jar POST /api/login "{\"username\":\"alice\",\"password\":\"$PN\"}"
check C3b 401 $SESS GET /api/certs   # old sessions revoked
ID1=$(cat "$OUT/.id1")
# ID1's key_enc was swapped by E5 in cases_02 (tamper test); use a fresh cert for the decrypt check
st $OUT/u1e.jar POST /api/certs '{"name":"cli","hosts":["cli.example.com"],"bits":2048,"days":30,"subject":{}}' >/dev/null; CC=$(j .id)
curl -s -b $OUT/u1e.jar $BASE/api/certs/$CC/files/key | grep -q 'PRIVATE KEY' && ok "C3 key decrypts after CLI reset (new cert)" || bad C3k
# keys created BEFORE reset: pick any alice cert untouched by tamper
OLD=$(psql_q "select id from certs where user_id=(select id from users where username='alice') and id<>'$ID1' order by created_at limit 1 offset 3")
[ -n "$OLD" ] && { curl -s -b $OUT/u1e.jar $BASE/api/certs/$OLD/files/key | grep -q 'PRIVATE KEY' && ok "C3 pre-reset cert key decrypts" || bad C3o; }
$DC logs app 2>&1 | grep -qF "$MK" && bad C5 "master key in logs" || ok "C5 master key not in logs"
# O15 healthcheck subcommand
$DC exec -T app /app healthcheck >/dev/null 2>&1 && ok "O15 healthcheck exit 0 when up" || bad O15
# C6: CLI is not throttled: 7 wrong-master-key attempts never lock out, and a correct run still works afterwards
for i in 1 2 3 4 5 6 7; do python3 -I "$QA/helpers/cli_reset.py" "$DC" "WRONGWRONGWRONGWRONG" alice "$PN" > "$OUT/cli_t$i.txt" 2>&1; grep -qiE 'too many|throttl|locked|429' "$OUT/cli_t$i.txt" && bad C6 "throttled at attempt $i"; done; ok "C6 no CLI throttling in 7 attempts (no FAIL above)"
python3 -I "$QA/helpers/cli_reset.py" "$DC" "$MK" alice "$PN" > "$OUT/cli_after.txt" 2>&1 && ok "C6 correct run still works" || bad C6b "$(tail -3 $OUT/cli_after.txt)"
