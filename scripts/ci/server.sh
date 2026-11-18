#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
lane="${1:-all}"
[ "$#" -le 1 ] || { printf 'Usage: scripts/ci/server.sh [all|node|managers|other|build]\n' >&2; exit 2; }
case "$lane" in all|node|managers|other|build) ;; *) printf 'Unknown server lane: %s\n' "$lane" >&2; exit 2 ;; esac
# Go's build cache key holds each package's directory unless -trimpath is set: add it, so checkouts and worktrees
# share compiled packages (#10614). The helper keeps the caller's GOFLAGS; vet clears -ldflags on its command line.
GOFLAGS=$(sh scripts/test/go_trimpath.sh)
export GOFLAGS

if [ "$lane" = all ] || [ "$lane" = build ]; then
  ci_run 'Go formatting' 120 python3 scripts/ci/gofmt.py server sdk/go
  ci_run 'Server vet' 600 bash -c 'cd server && go vet -ldflags= ./...'
  ci_run 'Server build' 600 bash -c 'cd server && go build ./...'
  ci_run 'Windows node build and vet' 600 bash -c 'cd server && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet -ldflags= ./...'
  # The release's tags (server/build-tags.txt, server/internal/buildtags): the programs build with them on Linux and
  # macOS, and the tunnel works with the tailscale.com subset they leave.
  ci_run 'Server programs with build-tags.txt' 600 bash -c 'cd server && t="$(cat build-tags.txt)" && go build -tags "$t" ./cmd/... && GOOS=darwin GOARCH=arm64 go build -tags "$t" ./cmd/...'
  ci_run 'Tunnel tests with build-tags.txt' 300 bash -c 'cd server && go test -tags "$(cat build-tags.txt)" -count=1 -timeout=4m ./internal/buildtags/... ./internal/tunnel/...'
  ci_run 'Extension schema and SDK bundle' 300 bash -c 'python3 scripts/test/test_extension_schema.py && python3 scripts/test/test_sdk_bundle.py'
  ci_run 'Go SDK tests' 300 bash -c 'cd sdk/go && go test -count=1 -timeout=4m ./...'
fi
if [ "$lane" = build ]; then exit 0; fi

fixture="$(mktemp -d "${TMPDIR:-/tmp}/persona-ci-db.XXXXXX")"
container="persona-ci-db-$(basename "$fixture" | tr '[:upper:]' '[:lower:]')"
cleanup() {
  docker rm -fv "$container" >/dev/null 2>&1 || true
  rm -f "${fixture:?}/database.json"
  rmdir "$fixture" 2>/dev/null || true
}
trap cleanup EXIT
ci_run 'Postgres 18 fixture' 180 docker run --detach --name "$container" \
  -e POSTGRES_USER=persona -e POSTGRES_PASSWORD=ci-only -e POSTGRES_DB=persona_test \
  -p 127.0.0.1::5432 postgres:18-alpine
ci_run 'Postgres readiness' 90 python3 scripts/ci/postgres.py "$container" "$fixture/database.json"
if [ "$lane" = all ] || [ "$lane" = node ]; then
  # The real-browser package has its own hosted runner, without build or other-package contention.
  ci_run 'Browser runner resources' 60 bash -e -o pipefail -c 'google-chrome --version; getconf _NPROCESSORS_ONLN; free -m; df -h /dev/shm'
fi
for shard in node managers other; do
  if [ "$lane" = all ] || [ "$lane" = "$shard" ]; then
    ci_run "Server $shard tests with Postgres" 600 python3 scripts/ci/server-shards.py "$shard" --database-file "$fixture/database.json"
  fi
done
