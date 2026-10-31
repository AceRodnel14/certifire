#!/usr/bin/env bash
# Shared helpers. Sourced by run.sh and the case scripts. Needs: curl jq openssl.
# Env: BASE (http://127.0.0.1:PORT), OUT (evidence dir), DC (docker compose cmd array string).
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "PASS $1" | tee -a "$OUT/results.txt"; }
bad()  { FAIL=$((FAIL+1)); echo "FAIL $1 :: ${2:-}" | tee -a "$OUT/results.txt"; }
# expect ID WANT_STATUS curl-args... : runs curl, stores body in $BODY, compares status
expect() { local id=$1 want=$2; shift 2
  STATUS=$(curl -s -o "$OUT/body.tmp" -D "$OUT/hdr.tmp" -w '%{http_code}' "$@"); BODY=$(cat "$OUT/body.tmp")
  if [ "$STATUS" = "$want" ]; then ok "$id (HTTP $STATUS)"; else bad "$id" "want $want got $STATUS body=${BODY:0:200}"; fi; }
j() { jq -r "$1" <<<"$BODY"; }
# st JAR METHOD PATH [JSON] -> prints status only, body in $BODY
st() { local jar=$1 m=$2 p=$3 d=${4:-}
  if [ -n "$d" ]; then STATUS=$(curl -s -o "$OUT/body.tmp" -w '%{http_code}' -b "$jar" -c "$jar" -X "$m" -H 'Content-Type: application/json' -H "Origin: $BASE" -d "$d" "$BASE$p")
  else STATUS=$(curl -s -o "$OUT/body.tmp" -w '%{http_code}' -b "$jar" -c "$jar" -X "$m" -H "Origin: $BASE" "$BASE$p"); fi
  BODY=$(cat "$OUT/body.tmp"); }
check() { local id=$1 want=$2; shift 2; st "$@"; [ "$STATUS" = "$want" ] && ok "$id ($STATUS)" || bad "$id" "want $want got $STATUS body=${BODY:0:200}"; }
psql_q() { $DC exec -T db sh -c 'psql -qtAX -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$1"' _ "$1"; }
pubhash_cert() { openssl x509 -in "$1" -pubkey -noout | openssl pkey -pubin -pubout -outform DER | sha256sum | cut -d' ' -f1; }
pubhash_key()  { openssl pkey -in "$1" -pubout -outform DER | sha256sum | cut -d' ' -f1; }
