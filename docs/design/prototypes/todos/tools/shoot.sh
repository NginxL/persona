#!/bin/bash
# Usage: shoot.sh <html file> <hash, e.g. "v=KIT&f=iphone/list&theme=dark"> <width> <height> <out.png> [scale]
# Takes one screenshot of a static prototype with headless Chrome; the window size must equal the device size.
# Adds shot=1 to the hash when it is missing (no harness strip, no timed steps).
# Prints a one-line verdict: ok / timeout / failed. CHROME overrides the browser path; SHOOT_TIMEOUT the limit (s).
set -u
html="$1"; hash="$2"; w="$3"; h="$4"; out="$5"; scale="${6:-2}"
chrome="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
limit="${SHOOT_TIMEOUT:-60}"
run_limited() {
  if command -v timeout >/dev/null 2>&1; then timeout "$limit" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then gtimeout "$limit" "$@"
  else perl -e '$SIG{ALRM}=sub{kill "TERM", $pid; exit 124}; alarm shift; $pid=fork; if(!$pid){exec @ARGV} waitpid $pid,0; exit($?>>8)' "$limit" "$@"
  fi
}
profile="$(mktemp -d "${TMPDIR:-/tmp}/shoot-profile.XXXXXX")"
url="file://$(cd "$(dirname "$html")" && pwd)/$(basename "$html")"
# shot=1 hides frame.html's harness strip and stops timed steps (virtual time would otherwise run them).
case "&$hash&" in *"&shot="*) ;; *) [ -n "$hash" ] && hash="$hash&shot=1" ;; esac
[ -n "$hash" ] && url="$url#$hash"
mkdir -p "$(dirname "$out")"
run_limited "$chrome" --headless=new --disable-gpu --hide-scrollbars --no-first-run --no-default-browser-check \
  --user-data-dir="$profile" --force-device-scale-factor="$scale" --window-size="$w,$h" \
  --virtual-time-budget=3000 --screenshot="$out" "$url" >/dev/null 2>&1
rc=$?
if [ $rc -eq 0 ] && [ -s "$out" ]; then echo "ok: $out (${w}x${h}@${scale}x)"; elif [ $rc -eq 124 ]; then echo "timeout: $out"; else echo "failed($rc): $out"; fi
