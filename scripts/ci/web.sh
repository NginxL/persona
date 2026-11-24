#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_run 'Web install' 600 bash -c 'cd web && npm ci --no-audit --no-fund'
ci_run 'Web lint' 300 bash -c 'cd web && npm run lint'
# The typecheck and the build (whose own tsc -b then has the typecheck's build info) run beside the tests rather than
# after them: neither reads what the other writes (the build writes web/dist, ../dist/public-share and public/pake,
# which npm test does not read). Their output follows the tests'; the exit code is the first failed step's, in this order.
log="$(mktemp "${TMPDIR:-/tmp}/persona-web-build.XXXXXX")"
trap 'rm -f "$log"' EXIT
(
  set -e
  ci_run 'Web typecheck' 300 bash -c 'cd web && npm run typecheck'
  ci_run 'Web build and privacy' 600 bash -c 'cd web && npm run build'
) > "$log" 2>&1 &
build_pid=$!
tests_rc=0
ci_run 'Web tests' 600 bash -c 'cd web && npm test' || tests_rc=$?
build_rc=0
wait "$build_pid" || build_rc=$?
cat "$log"
if [ "$build_rc" -ne 0 ]; then exit "$build_rc"; fi
if [ "$tests_rc" -ne 0 ]; then exit "$tests_rc"; fi
# Persona Core alone (#13932, docs/web/web.md "Modules"): built and tested with zero feature modules. Fails when a file
# outside web/src/modules reaches a module's files, or a core test only passes with a module registered.
ci_run 'Web core-only build' 600 bash -c 'cd web && npm run build:core-only'
ci_run 'Web core-only tests' 900 bash -c 'cd web && npm run test:core-only'
