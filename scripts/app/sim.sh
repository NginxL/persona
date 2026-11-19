#!/bin/bash
# Build, install and launch Persona in the iPhone simulator, paired with the hub of the config (docs/operations/config.md).
#   ./scripts/app/sim.sh [--with-watch] [extra launch args...]
# A development build (#10713, docs/operations/testing.md "Development builds"): the iPhone app without the watch app and
# the share extension, and the Rust core's simulator slice only. --with-watch builds and embeds both, as CI and release do.
# A lean build prints one line naming that check when the branch changes apple/Shared/, the watch app or the share
# extension (scripts/app/lean-build-note.sh).
set -euo pipefail
cd "$(dirname "$0")/../.."
LEAN=YES
ARGS=()
for arg in "$@"; do
  case "$arg" in
    --with-watch) LEAN=NO ;;
    *) ARGS+=("$arg") ;;
  esac
done
eval "$(python3 scripts/persona_config.py env)"
python3 scripts/persona_config.py xcconfig
if [ -x "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" ]; then
  "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" native/app-core/scripts/build-apple.sh --simulator
else
  native/app-core/scripts/build-apple.sh --simulator
fi
RESOLVER=$(python3 scripts/persona_identity.py --service hub --prefix -)
cd apple
SIM="${SIM:-$PERSONA_SIM}"
[ -n "$SIM" ] || { echo "set SIM=<udid or name> or \"simulator\" in the config (docs/operations/config.md)" >&2; exit 2; }
# boots it slim (SIM_PROFILE picks the simslim profile; a booted one is used as it is), docs/operations/testing.md "Simulators"
SIM=$(../scripts/app/sim-boot.sh "$SIM") || exit 1
# apple/lean-dev-build.yml leaves the watch app and the share extension out when this is YES.
PERSONA_LEAN_DEV_BUILD=$LEAN xcodegen generate -q
APP=../build/DerivedData/Build/Products/Debug-iphonesimulator/Persona.app
if [ "$LEAN" = YES ]; then
  ../scripts/app/lean-build-note.sh
  # Xcode leaves in place the watch app a --with-watch build embedded (Xcode 26.3; the share extension went): take
  # both out before the build, which signs the app again.
  rm -rf "$APP/Watch" "$APP/PlugIns/PersonaShare.appex"
fi
# In a machine-wide xcode slot, so parallel worktrees queue instead of thrashing (#10714,
# docs/operations/testing.md "Heavy build slots"). Only xcodebuild's output goes through the filter, which prints
# nothing until the build ends: the slot's own lines reach stderr while it waits.
python3 ../scripts/heavy_slot.py run --kind xcode -- sh -c 'exec "$@" 2>&1' sh \
  xcodebuild -project Persona.xcodeproj -scheme Persona -destination "id=$SIM" -derivedDataPath ../build/DerivedData build | grep -E "error:|BUILD (SUCCEEDED|FAILED)" | sort -u
xcrun simctl uninstall "$SIM" "$PERSONA_BUNDLE_ID" 2>/dev/null || true
xcrun simctl install "$SIM" "$APP"
TOKEN=$(ssh "$PERSONA_HUB_SSH" "$RESOLVER
python3 -c 'import json,os;print(json.load(open(os.path.join(os.environ[\"PERSONA_HOME\"],\"hub.json\")))[\"token\"])'")
URL=$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1],safe=""))' "$PERSONA_HUB_URL")
xcrun simctl terminate "$SIM" "$PERSONA_BUNDLE_ID" 2>/dev/null || true
xcrun simctl launch "$SIM" "$PERSONA_BUNDLE_ID" -pairURL "persona://pair?url=$URL&token=$TOKEN" ${ARGS[@]+"${ARGS[@]}"} >/dev/null
