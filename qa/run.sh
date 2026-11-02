#!/usr/bin/env bash
# QA driver. usage: qa/run.sh [path-to-app-repo]   (default: parent of this dir)
# Copies the repo into a fresh run folder, brings up project certifire-qa, runs case scripts, always tears down.
# Set KEEP=1 to keep the run folder (evidence) after teardown.
set -u
SRC=$(cd "${1:-$(dirname "$0")/..}" && pwd); QA=$SRC/qa
RUN=$(mktemp -d "${TMPDIR:-/tmp}/certifire-qa.XXXXXX"); OUT=$RUN/out; mkdir -p "$OUT" "$RUN/app" "$RUN/stub"
P=certifire-qa
# copy without git/design/node_modules/qa (simulates a fresh clone)
tar -C "$SRC" --exclude=.git --exclude=node_modules --exclude=qa --exclude=design --exclude=.env -cf - . | tar -C "$RUN/app" -xf -
cp "$QA/helpers/stub.py" "$QA/helpers/fwd.py" "$RUN/stub/"; cp "$QA/compose.qa.yml" "$RUN/app/"
cp "$RUN/app/.env.example" "$RUN/app/.env"
# POSTGRES_PASSWORD is empty (required) in .env.example: set a throwaway one in the run copy only (never printed or logged)
PW=$(openssl rand -hex 24)
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$PW/" "$RUN/app/.env"
grep -q "^POSTGRES_PASSWORD=$PW" "$RUN/app/.env" || { echo "could not set POSTGRES_PASSWORD"; exit 1; }
APORT=$(python3 -c "import socket;s=socket.socket();s.bind(('127.0.0.1',0));print(s.getsockname()[1])")
printf "APP_BIND=127.0.0.1\nAPP_PORT=%s\nBASE_URL=http://127.0.0.1:%s\nIMAGE=certifire-qa:local\n" "$APORT" "$APORT" >> "$RUN/app/.env"
# stub TLS cert for the two webhook hostnames (fake CA, lives only in the run folder)
openssl req -x509 -nodes -newkey rsa:2048 -days 2 -keyout "$RUN/stub/ca.key" -out "$RUN/stub/ca.crt" -subj "/CN=qa-stub-ca" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign" 2>/dev/null
openssl req -nodes -newkey rsa:2048 -keyout "$RUN/stub/stub.key" -out "$RUN/stub/stub.csr" -subj "/CN=discord.com" 2>/dev/null
printf 'subjectAltName=DNS:discord.com,DNS:qa.environment.api.powerplatform.com\nextendedKeyUsage=serverAuth\n' > "$RUN/stub/ext"
openssl x509 -req -in "$RUN/stub/stub.csr" -CA "$RUN/stub/ca.crt" -CAkey "$RUN/stub/ca.key" -CAcreateserial -days 2 -extfile "$RUN/stub/ext" -out "$RUN/stub/stub.crt" 2>/dev/null
chmod 644 "$RUN"/stub/*   # readable by the non-root app user inside the container
export QA_STUB_DIR=$RUN/stub
cd "$RUN/app" || exit 1
DC="docker compose -p $P -f docker-compose.yml -f compose.qa.yml"; export DC OUT QA RUN
cleanup() {
  cd "$RUN/app" 2>/dev/null && $DC down -v --remove-orphans >"$OUT/teardown.txt" 2>&1
  echo "teardown done; leftover containers / volumes for $P:"
  docker ps -a --filter "label=com.docker.compose.project=$P" -q | wc -l
  docker volume ls -q --filter "label=com.docker.compose.project=$P" | wc -l
  if [ -z "${KEEP:-}" ]; then find "$RUN" -mindepth 1 -delete; rmdir "$RUN"; else echo "kept $RUN (results: $OUT/results.txt)"; fi
}
trap cleanup EXIT
$DC up -d --build >"$OUT/up.txt" 2>&1 || { echo "compose up failed"; tail -30 "$OUT/up.txt"; exit 1; }
for _ in $(seq 1 60); do
  PORT=$($DC port gw 8080 2>/dev/null | sed 's/.*://'); BASE=http://127.0.0.1:$PORT
  [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/readyz")" = 200 ] && break; sleep 2
done
export BASE; echo "BASE=$BASE out=$OUT"
SCTL=$($DC port gw 9000 | sed 's/.*://'); export STUBCTL=http://127.0.0.1:$SCTL
for s in "$QA"/helpers/cases_*.sh; do echo "== $(basename "$s")"; bash "$s"; done
echo "TOTAL pass=$(grep -c ^PASS "$OUT/results.txt") fail=$(grep -c ^FAIL "$OUT/results.txt")"
grep ^FAIL "$OUT/results.txt"
# evidence stays in $OUT (run folder, removed on exit unless KEEP=1); nothing is written into the repo
F=$(grep -c ^FAIL "$OUT/results.txt"); [ "$F" -eq 0 ]
