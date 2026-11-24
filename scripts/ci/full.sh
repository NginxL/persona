#!/bin/bash
# full.sh — every portable Linux lane: the server build and its three test shards, web and scripts
# (docs/operations/testing.md, "Affected checks"). For main after a merge and the nightly run; a PR runs
# scripts/ci/affected.sh, which comes here only when the change touches what selects or runs the checks.
#
#   bash scripts/ci/full.sh                  # all six lanes
#   bash scripts/ci/full.sh server-node web  # some of them
#
# Each lane keeps about eight CPUs busy by itself (the Go partitions, -p, vitest's workers and the Python suites in
# parallel), so the lanes run side by side only as far as the CPUs this process may use allow: one lane per eight,
# at least one. All six at once on eight CPUs took each lane two to three times as long and timed out the tests that
# measure their own deadlines. PERSONA_CI_FULL_JOBS sets how many lanes run at once. For a runner that spreads the lanes
# over several machines or slots, PERSONA_CI_FULL_INCLUDE runs only the lanes it names and PERSONA_CI_FULL_EXCLUDE
# leaves out the lanes it names (lane names, separated by spaces or commas; both may be set). Each lane's log is
# printed whole when it ends, in the order they end, then a summary line per lane; the exit code is the first failed
# lane's in the listed order.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 2
LANES=(server-build server-managers server-node server-other web scripts)
[ "$#" -eq 0 ] || LANES=("$@")
include=" ${PERSONA_CI_FULL_INCLUDE:-} " exclude=" ${PERSONA_CI_FULL_EXCLUDE:-} "
include=${include//,/ } exclude=${exclude//,/ }
ALL_LANES=" server-build server-managers server-node server-other web scripts "
# shellcheck disable=SC2086
for name in $include $exclude; do
  case "$ALL_LANES" in *" $name "*) ;; *) printf 'full.sh: unknown lane %s in PERSONA_CI_FULL_INCLUDE or PERSONA_CI_FULL_EXCLUDE\n' "$name" >&2; exit 2 ;; esac
done
if [ -n "${include// /}" ]; then
  kept=()
  for lane in "${LANES[@]}"; do
    case "$include" in *" $lane "*) kept+=("$lane") ;; *) printf 'full.sh: %s left out (not in PERSONA_CI_FULL_INCLUDE)\n' "$lane" ;; esac
  done
  LANES=("${kept[@]}")
fi
if [ -n "${exclude// /}" ]; then
  kept=()
  for lane in "${LANES[@]}"; do
    case "$exclude" in *" $lane "*) printf 'full.sh: %s left out (PERSONA_CI_FULL_EXCLUDE)\n' "$lane" ;; *) kept+=("$lane") ;; esac
  done
  LANES=("${kept[@]}")
fi
jobs=${PERSONA_CI_FULL_JOBS:-$(( $(nproc) / 8 ))}
case "$jobs" in ''|*[!0-9]*) printf 'full.sh: PERSONA_CI_FULL_JOBS must be a number, not %s\n' "$jobs" >&2; exit 2 ;; esac
[ "$jobs" -ge 1 ] || jobs=1
echo "full.sh: ${#LANES[@]} lanes, $jobs at a time ($(nproc) CPUs)"
work="$(mktemp -d "${TMPDIR:-/tmp}/persona-full.XXXXXX")"
trap 'rm -rf "${work:?}"' EXIT
command_for() {
  case "$1" in
    server-build) echo "scripts/ci/server.sh build" ;;
    server-managers) echo "scripts/ci/server.sh managers" ;;
    server-node) echo "scripts/ci/server.sh node" ;;
    server-other) echo "scripts/ci/server.sh other" ;;
    web) echo "scripts/ci/web.sh" ;;
    scripts) echo "scripts/ci/scripts.sh" ;;
    *) return 1 ;;
  esac
}
for lane in "${LANES[@]}"; do
  command_for "$lane" >/dev/null || { printf 'full.sh: unknown lane %s (one of server-build server-managers server-node server-other web scripts)\n' "$lane" >&2; exit 2; }
done
declare -A pid_lane=() started=() status=() took=()
next=0
while [ "$next" -lt "${#LANES[@]}" ] || [ "${#pid_lane[@]}" -gt 0 ]; do
  while [ "$next" -lt "${#LANES[@]}" ] && [ "${#pid_lane[@]}" -lt "$jobs" ]; do
    lane=${LANES[$next]}
    next=$((next + 1))
    # shellcheck disable=SC2046
    TMPDIR="$work" bash $(command_for "$lane") > "$work/$lane.log" 2>&1 &
    pid_lane[$!]=$lane
    started[$lane]=$SECONDS
  done
  wait -n -p done_pid "${!pid_lane[@]}"
  rc=$?
  lane=${pid_lane[$done_pid]}
  unset "pid_lane[$done_pid]"
  status[$lane]=$rc
  took[$lane]=$((SECONDS - started[$lane]))
  echo "::group::$lane (exit $rc, ${took[$lane]}s)"
  cat "$work/$lane.log"
  echo "::endgroup::"
done
first=0
[ "${#LANES[@]}" -gt 0 ] || { echo 'full.sh: no lanes left to run'; exit 0; }
for lane in "${LANES[@]}"; do
  if [ "${status[$lane]}" -eq 0 ]; then verdict=PASS; elif [ "${status[$lane]}" -eq 124 ]; then verdict=TIMEOUT; else verdict=FAIL; fi
  printf '%s lane %s (%ss, exit %s)\n' "$verdict" "$lane" "${took[$lane]}" "${status[$lane]}"
  if [ "$first" -eq 0 ] && [ "${status[$lane]}" -ne 0 ]; then first=${status[$lane]}; fi
done
exit "$first"
