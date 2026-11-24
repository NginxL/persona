#!/bin/bash
# affected.sh — a PR's gate: only the checks its change affects (scripts/ci/affected.py; docs/operations/testing.md,
# "Affected checks"). The full lanes (scripts/ci/full.sh) stay for main after a merge and the nightly run.
#
#   bash scripts/ci/affected.sh                        # against origin/main's merge base
#   CI_AFFECTED_BASE=<ref> bash scripts/ci/affected.sh # against another ref's
#
# The Go packages the change reaches run against a disposable Postgres, as a server shard does, with the repo-wide
# guard tests; their go vet for Linux and Windows and check-fast's plan for everything else in the same diff run
# beside them. A change to what selects or runs the checks runs full.sh instead.
set -euo pipefail
source "$(dirname "$0")/common.sh"
GOFLAGS=$(sh scripts/test/go_trimpath.sh)
export GOFLAGS
base="${CI_AFFECTED_BASE:-origin/main}"
work="$(mktemp -d "${TMPDIR:-/tmp}/persona-affected.XXXXXX")"
container="persona-ci-db-$(basename "$work" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9\n' '-')"
cleanup() {
  docker rm -fv "$container" >/dev/null 2>&1 || true
  rm -rf "${work:?}"
}
trap cleanup EXIT

ci_run 'Affected plan' 300 python3 scripts/ci/affected.py plan --base "$base"
python3 scripts/ci/affected.py plan --base "$base" --json > "$work/plan.json"
read -r full nothing < <(python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); print(int(p["full"]), int(not p["changed"]))' "$work/plan.json")
if [ "$full" = 1 ]; then
  trap - EXIT; cleanup
  exec bash scripts/ci/full.sh
fi
[ "$nothing" = 0 ] || exit 0
python3 -c 'import json,sys; print("\n".join(json.load(open(sys.argv[1]))["go_packages"]))' "$work/plan.json" > "$work/packages.txt"
guards=()
if python3 -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1]))["go_guards"] else 1)' "$work/plan.json"; then
  guards=(--guards)
fi

# check-fast's part runs beside the Go tests; its output follows theirs. Its own heavy-build slots are for parallel
# worktrees on one person's machine: the gate already gives this run its own cores.
PERSONA_HEAVY_SLOTS=0 bash scripts/test/check-fast.sh --base "$base" --no-server-go > "$work/check-fast.log" 2>&1 &
fast_pid=$!

# go vet for Linux and Windows runs beside the tests (it is CPU while they mostly wait); its output follows theirs.
vet_pid=
if [ -s "$work/packages.txt" ]; then
  # shellcheck disable=SC2016  # $1 is bash -c's own argument
  (
    set -e
    ci_run 'Go vet (affected packages, Linux)' 300 bash -c 'cd server && xargs go vet -ldflags= < "$1"' _ "$work/packages.txt"
    # The packages that build for Windows (go list leaves out one whose files all carry other build constraints).
    (cd server && GOOS=windows GOARCH=amd64 xargs go list -e -f '{{if and (not .Error) .GoFiles}}{{.ImportPath}}{{end}}' \
      < "$work/packages.txt" > "$work/windows.txt")
    if [ -s "$work/windows.txt" ]; then
      ci_run 'Go vet (affected packages, Windows)' 300 bash -c \
        'cd server && GOOS=windows GOARCH=amd64 xargs go vet -ldflags= < "$1"' _ "$work/windows.txt"
    fi
  ) > "$work/vet.log" 2>&1 &
  vet_pid=$!
fi

server_rc=0
if [ -s "$work/packages.txt" ] || [ "${#guards[@]}" -gt 0 ]; then
  (
    set -e
    ci_run 'Postgres 18 fixture' 180 docker run --detach --name "$container" \
      -e POSTGRES_USER=persona -e POSTGRES_PASSWORD=ci-only -e POSTGRES_DB=persona_test \
      -p 127.0.0.1::5432 postgres:18-alpine \
      -c fsync=off -c synchronous_commit=off -c full_page_writes=off
    ci_run 'Postgres readiness' 90 python3 scripts/ci/postgres.py "$container" "$work/database.json"
    ci_run 'Affected Go tests with Postgres' 600 python3 scripts/ci/server-shards.py affected \
      --packages-file "$work/packages.txt" "${guards[@]}" --database-file "$work/database.json"
  ) || server_rc=$?
fi

vet_rc=0
if [ -n "$vet_pid" ]; then
  wait "$vet_pid" || vet_rc=$?
  cat "$work/vet.log"
fi

fast_rc=0
wait "$fast_pid" || fast_rc=$?
echo "::group::check-fast --no-server-go (exit $fast_rc)"
cat "$work/check-fast.log"
echo "::endgroup::"
if [ "$fast_rc" -eq 0 ]; then echo "PASS check-fast --no-server-go"; else echo "FAIL check-fast --no-server-go (exit $fast_rc)"; fi
if [ "$server_rc" -ne 0 ]; then exit "$server_rc"; fi
if [ "$vet_rc" -ne 0 ]; then exit "$vet_rc"; fi
exit "$fast_rc"
