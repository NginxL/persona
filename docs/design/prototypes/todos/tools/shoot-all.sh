#!/bin/bash
# Usage: shoot-all.sh <variant | all> <out dir> [scale=2] [themes="light dark"]
# Screenshots every registered frame of a variant in each theme, at its exact device size.
# Reads the frame list from frame.html#list=<variant> through headless Chrome's --dump-dom, then runs shoot.sh
# per frame and theme (JOBS in parallel, default 4). Every Chrome run is time-limited. Ends with one verdict line.
# Files: <out dir>/<variant>/<frame id with / turned into ->-<theme>.png (a state keeps its ~: iphone-list~calm-light.png).
# Every hash carries shot=1, so frame.html hides its harness strip and never runs a frame's timed step.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
kit="$(cd "$here/.." && pwd)"
variant="${1:-}"; outdir="${2:-}"; scale="${3:-2}"; themes="${4:-light dark}"
if [ -z "$variant" ] || [ -z "$outdir" ]; then
  echo "usage: $(basename "$0") <variant|all> <out dir> [scale] [themes]" >&2
  echo "verdict: failed (missing arguments)"; exit 2
fi
chrome="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
limit="${SHOOT_TIMEOUT:-60}"
run_limited() {
  if command -v timeout >/dev/null 2>&1; then timeout "$limit" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then gtimeout "$limit" "$@"
  else perl -e '$SIG{ALRM}=sub{kill "TERM", $pid; exit 124}; alarm shift; $pid=fork; if(!$pid){exec @ARGV} waitpid $pid,0; exit($?>>8)' "$limit" "$@"
  fi
}

profile="$(mktemp -d "${TMPDIR:-/tmp}/shoot-list.XXXXXX")"
dom="$(run_limited "$chrome" --headless=new --disable-gpu --no-first-run --no-default-browser-check \
  --user-data-dir="$profile" --virtual-time-budget=2000 --dump-dom "file://$kit/frame.html#list=$variant" 2>/dev/null)"
rc=$?
if [ $rc -ne 0 ] || [ -z "$dom" ]; then echo "verdict: failed (could not read the frame list, chrome exit $rc)"; exit 1; fi

# <pre id="frames-tsv">: variant TAB id TAB device TAB w TAB h, one frame per line.
list="$(printf '%s\n' "$dom" | awk '/<pre id="frames-tsv">/{on=1; sub(/.*<pre id="frames-tsv">/, "")} on{ if (sub(/<\/pre>.*/, "")) { if (length($0)) print; on=0 } else print }')"
count="$(printf '%s\n' "$list" | grep -c . || true)"
if [ "$count" -eq 0 ]; then echo "verdict: failed (no frames registered for '$variant')"; exit 1; fi
echo "frames: $count × themes: $(echo $themes | wc -w | tr -d ' ') → $outdir"

jobs_file="$(mktemp "${TMPDIR:-/tmp}/shoot-jobs.XXXXXX")"
while IFS=$'\t' read -r v id device w h; do
  [ -z "$id" ] && continue
  name="$(printf '%s' "$id" | tr '/' '-')"
  for t in $themes; do
    # shot=1: no harness strip, no timed steps, no transitions; the frame renders statically.
    printf '%s\t%s\t%s\t%s\t%s\n' "v=$v&f=$id&theme=$t&shot=1" "$w" "$h" "$outdir/$v/$name-$t.png" "$scale" >> "$jobs_file"
  done
done <<< "$list"

results_file="$(mktemp "${TMPDIR:-/tmp}/shoot-results.XXXXXX")"
jobs_max="${JOBS:-4}"; n=0
while IFS=$'\t' read -r hash w h out sc; do
  [ -z "$hash" ] && continue
  SHOOT_TIMEOUT="$limit" CHROME="$chrome" "$here/shoot.sh" "$kit/frame.html" "$hash" "$w" "$h" "$out" "$sc" < /dev/null >> "$results_file" &
  n=$((n + 1))
  if [ $((n % jobs_max)) -eq 0 ]; then wait; fi
done < "$jobs_file"
wait
results="$(cat "$results_file")"
printf '%s\n' "$results"
ok="$(printf '%s\n' "$results" | grep -c '^ok:' || true)"
total="$(grep -c . "$jobs_file" || true)"
failed=$((total - ok))
if [ "$failed" -eq 0 ]; then echo "verdict: ok $ok / failed 0"; else echo "verdict: ok $ok / failed $failed"; exit 1; fi
