#!/usr/bin/env bash
# usage: certcheck.sh JAR CERT_ID DIR  -> downloads files, prints openssl -text, returns 0 if key matches cert
set -eu; JAR=$1; ID=$2; DIR=$3; mkdir -p "$DIR"
for e in crt key cnf; do curl -s -b "$JAR" -D "$DIR/$e.hdr" -o "$DIR/c.$e" "$BASE/api/certs/$ID/files/$e"; done
openssl x509 -in "$DIR/c.crt" -noout -text > "$DIR/text.txt"
[ "$(openssl x509 -in "$DIR/c.crt" -pubkey -noout | openssl pkey -pubin -pubout -outform DER | sha256sum)" = \
  "$(openssl pkey -in "$DIR/c.key" -pubout -outform DER | sha256sum)" ] && echo "KEYMATCH ok" || { echo "KEYMATCH FAIL"; exit 1; }
grep -q 'CA:TRUE, pathlen:0' "$DIR/text.txt" && echo "BC ok"
openssl verify -CAfile "$DIR/c.crt" "$DIR/c.crt" >/dev/null && echo "SELFVERIFY ok"
