#!/bin/bash
# Share › Persona from Safari, Photos and Files (#2282): seeds a picture into Photos and a PDF into Files' On My iPhone,
# starts its own mock hub, and runs PersonaUITests/ShareFromAppsTests on a stock simulator (the slim profile leaves
# Safari's and Files' services out). docs/apple/ios.md "Share to Persona (#4586, #1460)".
#
#   scripts/app/share-from-apps.sh [light|dark] [screenshot dir] [test name | Suite | Suite/test]
#
# SIM: the simulator's name or udid (default "Persona Share"); PORT: the mock hub's port (default a free one).
# The full xcodebuild log is kept in $TMPDIR/share-from-apps-<appearance>.log. Prints one verdict line last: share-from-apps: passed|failed <appearance>.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 2
APPEARANCE="${1:-light}"
SHOTS="${2:-}"
# A test name runs that test of ShareFromAppsTests; a suite (…Tests) or Suite/test runs that against the same mock hub.
# An argument that already names the bundle (PersonaUITests/…) is used as it is, never prefixed twice.
only_testing() {
  case "${1:-}" in
    "") echo "PersonaUITests/ShareFromAppsTests" ;;
    PersonaUITests/*) echo "$1" ;;
    */*|*Tests) echo "PersonaUITests/$1" ;;
    *) echo "PersonaUITests/ShareFromAppsTests/$1" ;;
  esac
}
if [ "${1:-}" = "--only-testing" ]; then only_testing "${2:-}"; exit 0; fi # prints what a third argument selects
ONLY=$(only_testing "${3:-}")
case "$APPEARANCE" in light|dark) ;; *) echo "share-from-apps: appearance is light or dark" >&2; exit 2 ;; esac
eval "$(python3 scripts/persona_config.py env)"
python3 scripts/persona_config.py xcconfig >/dev/null
SIM=$(scripts/app/sim-boot.sh --profile stock "${SIM:-Persona Share}") || { echo "share-from-apps: failed (no simulator)"; exit 1; }
PORT="${PORT:-$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')}"
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then echo "share-from-apps: port $PORT is taken" >&2; exit 2; fi
WORK=$(mktemp -d)
PORT="$PORT" python3 scripts/mock/mock_hub.py >"$WORK/mock.log" 2>&1 &
MOCK=$!
trap 'kill "$MOCK" 2>/dev/null; rm -rf "$WORK"' EXIT
for _ in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT/mock/sent" >/dev/null && break
  sleep 0.5
done
curl -sf "http://127.0.0.1:$PORT/mock/sent" >/dev/null || { echo "share-from-apps: failed (mock hub did not start)"; exit 1; }

# A made-up picture for Photos and a small PDF for Files (the name ShareFromAppsTests.seededFile looks for).
python3 - "$WORK/Share picture.png" <<'EOF'
import struct, sys, zlib
w, h = 1200, 800
row = b"\x00" + bytes([40, 150, 160]) * w
def chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(row * h)) + chunk(b"IEND", b"")
open(sys.argv[1], "wb").write(png)
EOF
xcrun simctl addmedia "$SIM" "$WORK/Share picture.png"
LOCAL=$(xcrun simctl get_app_container "$SIM" com.apple.DocumentsApp groups | awk -F'\t' '$1 == "group.com.apple.FileProvider.LocalStorage" {print $2}')
[ -n "$LOCAL" ] || { echo "share-from-apps: failed (no Files local storage on $SIM)"; exit 1; }
mkdir -p "$LOCAL/File Provider Storage"
printf '%%PDF-1.4\n%%Persona share test\n%%%%EOF\n' >"$LOCAL/File Provider Storage/Share test.pdf"

xcrun simctl ui "$SIM" appearance "$APPEARANCE"
[ -z "$SHOTS" ] || mkdir -p "$SHOTS"
# The whole project: the tests share into the share extension, which a lean build leaves out (apple/lean-dev-build.yml).
unset PERSONA_LEAN_DEV_BUILD
cd apple && xcodegen generate -q
TEST_RUNNER_PERSONA_MOCK_HUB="http://127.0.0.1:$PORT" TEST_RUNNER_PERSONA_SHOTS="$SHOTS" TEST_RUNNER_PERSONA_SHOTS_APPEARANCE="$APPEARANCE" \
  xcodebuild test -project Persona.xcodeproj -scheme Persona -destination "id=$SIM" -derivedDataPath ../build/DerivedData \
  -parallel-testing-enabled NO -only-testing:"$ONLY" >"$WORK/test.log" 2>&1
STATUS=$?
cp "$WORK/test.log" "${TMPDIR:-/tmp}/share-from-apps-$APPEARANCE.log"
grep -E "error:|Test Case|XCTAssert|TEST (SUCCEEDED|FAILED)" "$WORK/test.log" | tail -40
if [ "$STATUS" = 0 ]; then echo "share-from-apps: passed $APPEARANCE"; else echo "share-from-apps: failed $APPEARANCE"; fi
exit "$STATUS"
