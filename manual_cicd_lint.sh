#!/usr/bin/env bash
# manual_cicd_lint.sh - run the CI lint/test jobs locally, one throwaway container per job.
#
# Mirrors: gitlab-actions jobs/lint.yml + templates/app.yml @ 81e7cd1
# RULE: update this script together with those files whenever they change (image tags,
#       scripts, job list, rules:exists globs), and bump the sha above.
#
# How it works: the file list is `git ls-files -co --exclude-standard` (tracked plus
# untracked-not-ignored, so uncommitted work is checked; node_modules, dist, .env and
# caches are excluded). It is streamed into `docker run -i --rm <CI image>` as a tar,
# extracted to /w inside the container and the CI script runs there. Nothing is
# bind-mounted and nothing is written to the host. build_check and compose_config run on
# the host docker against a temporary export of the same file list (removed on exit).
#
# Knobs (same names as CI; read from the top-level `variables:` of .gitlab-ci.yml, an
# environment variable of the same name wins):
#   RUFF_FORMAT=false         skip `ruff format --check`        (lint_python)
#   GO_PREPARE="<cmd>"        eval'd before Go jobs             (lint_go, test_go)
#   HADOLINT_THRESHOLD=<lvl>  hadolint --failure-threshold      (lint_dockerfile)
# Not mirrored: KUBECONFORM_IGNORE (lint_kubernetes is not part of app.yml) and
# LINT_ON_BRANCHES / LINT_ON_DEFAULT_BRANCH (pipeline rules; every job runs locally).
# compose_config sets a placeholder POSTGRES_PASSWORD (unless already set) because
# compose files commonly require it; CI itself has no such variable.
#
# Usage: ./manual_cicd_lint.sh [--list] [--only a,b] [--skip a,b] [-h]
# Exit:  0 all selected jobs passed or skipped, 1 a job failed, 2 usage error / no docker or git.

# shellcheck disable=SC2329  # script_*/host_* are called indirectly
set -euo pipefail

# ---------------------------------------------------------------------------
# Job table: name  image  entrypoint  applies-if
#   image       "host" = run with the host docker on a temp export (no container)
#   entrypoint  "reset" = --entrypoint "" (as CI does), "-" = image default
#   applies-if  ERE matched against the file list (CI rules:exists); "ALWAYS" = no check
# ---------------------------------------------------------------------------
JOB_TABLE=(
  'lint_python     ghcr.io/astral-sh/uv:python3.12-alpine  reset  \.py$'
  'test_python     ghcr.io/astral-sh/uv:python3.12-alpine  reset  ^tests/.*\.py$|(^|/)test_[^/]*\.py$'
  'lint_go         golangci/golangci-lint:v2.15.0          -      (^|/)go\.mod$'
  'test_go         golang:1.26-alpine                      -      (^|/)go\.mod$'
  'lint_node       node:22-alpine                          -      (^|/)package\.json$'
  'lint_dockerfile hadolint/hadolint:v2.14.0-alpine        -      (^|/)Dockerfile(\..*)?$|\.Dockerfile$'
  'lint_shell      alpine:3.24                             -      \.sh$'
  'lint_yaml       alpine:3.24                             -      \.ya?ml$'
  'lint_opentofu   alpine:3.24                             -      \.tf$'
  'lint_secrets    zricethezav/gitleaks:v8.30.1            reset  ALWAYS'
  'build_check     host                                    -      ^Dockerfile$'
  'compose_config  host                                    -      ^docker-compose\.yml$'
)
KNOBS=(RUFF_FORMAT GO_PREPARE HADOLINT_THRESHOLD)

# ---------------------------------------------------------------------------
# Job scripts (from lint.yml; each runs under `sh -e` in /w)
# ---------------------------------------------------------------------------
script_lint_python() {
  cat <<'JOB'
uvx ruff check .
if [ "$RUFF_FORMAT" != "false" ]; then uvx ruff format --check .; fi
JOB
}

script_test_python() {
  cat <<'JOB'
apk add --no-cache git
if [ -f uv.lock ]; then
  uv sync --frozen && uv run pytest
else
  uv run --with pytest pytest
fi
JOB
}

script_lint_go() {
  cat <<'JOB'
eval "${GO_PREPARE:-true}"
golangci-lint run ./...
JOB
}

script_test_go() {
  cat <<'JOB'
eval "${GO_PREPARE:-true}"
go test ./...
JOB
}

script_lint_node() {
  cat <<'JOB'
for pkg in $(find . -name package.json -not -path '*/node_modules/*'); do
  dir=$(dirname "$pkg")
  echo "===== $dir ====="
  if [ -f "$dir/package-lock.json" ]; then npm ci --prefix "$dir"; else npm install --prefix "$dir"; fi
  for script in lint typecheck test build; do
    npm run --if-present --prefix "$dir" "$script"
  done
done
JOB
}

script_lint_dockerfile() {
  cat <<'JOB'
find . \( -name Dockerfile -o -name 'Dockerfile.*' -o -name '*.Dockerfile' \) -not -path './.git/*' -print0 | xargs -0 hadolint --failure-threshold "${HADOLINT_THRESHOLD:-error}"
JOB
}

script_lint_shell() {
  cat <<'JOB'
apk add --no-cache shellcheck
find . -name '*.sh' -not -path './.git/*' -not -path '*/node_modules/*' -print0 | xargs -0 shellcheck -S warning
JOB
}

script_lint_yaml() {
  cat <<'JOB'
apk add --no-cache yamllint
if ls .yamllint .yamllint.yml .yamllint.yaml > /dev/null 2>&1; then
  yamllint .
else
  yamllint -d '{extends: relaxed, ignore: [node_modules/, .git/], rules: {new-lines: disable, new-line-at-end-of-file: disable, line-length: disable, trailing-spaces: {level: warning}}}' .
fi
JOB
}

script_lint_opentofu() {
  cat <<'JOB'
apk add --no-cache opentofu
tofu fmt -check -recursive -diff
for dir in $(find . -name '*.tf' -not -path '*/.terraform/*' -exec dirname {} \; | sort -u); do
  echo "===== $dir ====="
  tofu -chdir="$dir" init -backend=false -input=false > /dev/null && tofu -chdir="$dir" validate
done
JOB
}

# The allowlist path is the same regex as CI's, written with a bracket so this file
# never contains the literal mapping-file name.
script_lint_secrets() {
  cat <<'JOB'
if [ -f .gitleaks.toml ]; then
  gitleaks dir --redact --no-banner .
else
  cat > /tmp/gitleaks.toml <<'EOF'
[extend]
useDefault = true

[allowlist]
description = "GitLab-only mapping used by sync_to_github_remove_sensitive"
paths = ['''(^|/)\.substitut[e]$''']
EOF
  gitleaks dir --redact --no-banner --config /tmp/gitleaks.toml .
fi
JOB
}

# Host jobs: run in the temp export (cwd), like docker.yml's .docker_validate_*.
host_build_check() {
  docker build --check .
}

host_compose_config() {
  if [ -f docker-compose.yml ]; then
    POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-ci-placeholder}" docker compose config -q && echo "OK"
  else
    echo "No docker-compose.yml found, skipping"
  fi
}

# ---------------------------------------------------------------------------
# Plumbing
# ---------------------------------------------------------------------------
usage() {
  cat <<'USAGE'
Usage: ./manual_cicd_lint.sh [--list] [--only a,b] [--skip a,b] [-h]
  --list         show every job, whether it applies to this repo, and its image
  --only a,b     run only these jobs
  --skip a,b     run everything except these jobs
  -h, --help     this help
Exit: 0 pass, 1 a job failed, 2 usage error or docker/git missing.
USAGE
}

die_usage() {
  echo "manual_cicd_lint.sh: $*" >&2
  exit 2
}

job_names() {
  local row name
  for row in "${JOB_TABLE[@]}"; do
    read -r name _ <<<"$row"
    echo "$name"
  done
}

validate_names() { # comma-separated job names
  local csv=$1 n
  local -a names
  IFS=',' read -r -a names <<<"$csv"
  for n in "${names[@]}"; do
    job_names | grep -qx -- "$n" || die_usage "unknown job '$n' (see --list)"
  done
}

in_csv() { # name csv
  [[ ",$2," == *",$1,"* ]]
}

# KEY: value from the top-level `variables:` block of .gitlab-ci.yml, quotes stripped.
ci_var() {
  local key=$1
  [ -f .gitlab-ci.yml ] || return 0
  awk -v key="$key" '
    /^variables:[[:space:]]*$/ { inblk = 1; next }
    inblk && /^[^[:space:]#]/ { inblk = 0 }
    inblk {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      if (index(line, key ":") != 1) next
      val = substr(line, length(key) + 2)
      sub(/^[[:space:]]+/, "", val)
      sub(/[[:space:]]+$/, "", val)
      if (val ~ /^".*"$/ || val ~ /^'"'"'.*'"'"'$/) val = substr(val, 2, length(val) - 2)
      print val
      exit
    }' .gitlab-ci.yml
}

load_knobs() {
  local k v
  for k in "${KNOBS[@]}"; do
    if [ -n "${!k+x}" ]; then
      export "${k?}"
    else
      v=$(ci_var "$k")
      if [ -n "$v" ]; then
        export "$k=$v"
      fi
    fi
  done
}

fmt_dur() {
  printf '%dm%02ds' "$(($1 / 60))" "$(($1 % 60))"
}

run_container_job() { # name image entry
  local name=$1 image=$2 entry=$3 script k
  local -a opts=(run -i --rm)
  if [ "$entry" = "reset" ]; then opts+=(--entrypoint ""); fi
  for k in "${KNOBS[@]}"; do opts+=(-e "$k"); done
  script=$("script_$name")
  tar -c --null -T "$LIST" |
    docker "${opts[@]}" "$image" sh -c "set -e; mkdir /w; tar x -C /w; cd /w; $script"
}

run_host_job() { # name
  (
    cd "$EXPORT"
    "host_$1"
  )
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
LIST_ONLY=0
ONLY=""
SKIP=""
while [ $# -gt 0 ]; do
  case "$1" in
    --list) LIST_ONLY=1 ;;
    --only) [ $# -ge 2 ] || die_usage "--only needs a value"; ONLY=$2; shift ;;
    --skip) [ $# -ge 2 ] || die_usage "--skip needs a value"; SKIP=$2; shift ;;
    -h | --help) usage; exit 0 ;;
    *) usage >&2; die_usage "unknown argument '$1'" ;;
  esac
  shift
done
if [ -n "$ONLY" ]; then validate_names "$ONLY"; fi
if [ -n "$SKIP" ]; then validate_names "$SKIP"; fi

command -v git >/dev/null 2>&1 || die_usage "git not found"
ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || die_usage "not inside a git repository"
if [ "$LIST_ONLY" -eq 0 ]; then
  command -v docker >/dev/null 2>&1 || die_usage "docker not found"
  docker info >/dev/null 2>&1 || die_usage "docker daemon not reachable"
fi
cd "$ROOT"

WORK=$(mktemp -d)
LIST="$WORK/files"
EXPORT="$WORK/export"
trap 'rm -rf "$WORK"' EXIT

# Tracked + untracked-not-ignored, minus tracked files deleted in the working tree.
git ls-files -co --exclude-standard -z --deduplicate |
  while IFS= read -r -d '' f; do
    if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\0' "$f"; fi
  done >"$LIST"

load_knobs

# Applicability (rules:exists equivalent), computed once.
declare -A APPLIES_JOB=()
FILES_TXT=$(tr '\0' '\n' <"$LIST")
for row in "${JOB_TABLE[@]}"; do
  read -r name _ _ re <<<"$row"
  if [ "$re" = "ALWAYS" ] || grep -Eq -- "$re" <<<"$FILES_TXT"; then
    APPLIES_JOB[$name]=yes
  else
    APPLIES_JOB[$name]=no
  fi
done

if [ "$LIST_ONLY" -eq 1 ]; then
  printf '%-16s %-8s %s\n' JOB APPLIES IMAGE
  for row in "${JOB_TABLE[@]}"; do
    read -r name image _ <<<"$row"
    printf '%-16s %-8s %s\n' "$name" "${APPLIES_JOB[$name]}" "$image"
  done
  exit 0
fi

declare -a SUM_NAME=() SUM_RES=() SUM_DUR=()
FAILED=0
for row in "${JOB_TABLE[@]}"; do
  read -r name image entry _ <<<"$row"
  if [ -n "$ONLY" ] && ! in_csv "$name" "$ONLY"; then continue; fi
  if [ -n "$SKIP" ] && in_csv "$name" "$SKIP"; then continue; fi
  if [ "${APPLIES_JOB[$name]}" != "yes" ]; then
    SUM_NAME+=("$name"); SUM_RES+=(SKIP); SUM_DUR+=("-")
    continue
  fi
  echo
  echo "=============== $name ($image) ==============="
  start=$SECONDS
  rc=0
  if [ "$image" = "host" ]; then
    if [ ! -d "$EXPORT" ]; then
      mkdir "$EXPORT"
      tar -c --null -T "$LIST" | tar -x -C "$EXPORT"
    fi
    run_host_job "$name" || rc=$?
  else
    run_container_job "$name" "$image" "$entry" || rc=$?
  fi
  dur=$((SECONDS - start))
  SUM_NAME+=("$name"); SUM_DUR+=("$(fmt_dur "$dur")")
  if [ "$rc" -eq 0 ]; then
    SUM_RES+=(PASS)
  else
    SUM_RES+=(FAIL)
    FAILED=1
  fi
done

echo
echo "=============== summary ==============="
printf '%-16s %-6s %s\n' JOB RESULT TIME
for i in "${!SUM_NAME[@]}"; do
  printf '%-16s %-6s %s\n' "${SUM_NAME[$i]}" "${SUM_RES[$i]}" "${SUM_DUR[$i]}"
done
exit "$FAILED"
