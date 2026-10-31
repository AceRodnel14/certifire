#!/usr/bin/env bash
# O17 short load smoke without extra tools: N parallel workers hit a path for S seconds; prints counts and p95.
# usage: load.sh BASE PATH [WORKERS=5] [SECONDS=20] [COOKIE_JAR]
BASE=$1; P=$2; W=${3:-5}; S=${4:-20}; JAR=${5:-/dev/null}
TMP=$(mktemp); END=$(( $(date +%s)+S ))
worker() { while [ "$(date +%s)" -lt "$END" ]; do curl -s -o /dev/null -b "$JAR" -w '%{http_code} %{time_total}\n' "$BASE$P"; done; }
for _ in $(seq 1 "$W"); do worker >> "$TMP" & done; wait
TOTAL=$(wc -l < "$TMP"); ERR=$(awk '$1>=500||$1==0' "$TMP" | wc -l)
P95=$(awk '{print $2}' "$TMP" | sort -n | awk '{a[NR]=$1} END{print a[int(NR*0.95)]}')
echo "requests=$TOTAL errors5xx=$ERR p95_s=$P95"; : > "$TMP"; [ "$ERR" = 0 ]
