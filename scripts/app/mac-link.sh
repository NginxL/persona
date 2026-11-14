#!/bin/bash
# Independently installable Persona Link: persona-link, persona-node and Persona Screen, no desktop UI required.
# The bundle id is PERSONA_LINK_BUNDLE_ID, else mac.link_bundle_id in ~/.config/persona/config.json, else the app's
# registered identity (PERSONA_BUNDLE_ID, else app.bundle_id) + ".link". Never a placeholder: macOS keeps Persona
# Screen's Screen Recording grant per bundle id, so a later build with another id has to be allowed again (#7569).
# UNIVERSAL=1 builds for Apple silicon and Intel together.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
ID=${PERSONA_LINK_BUNDLE_ID:-$(python3 scripts/persona_config.py get mac.link_bundle_id 2>/dev/null || true)}
if [ -z "$ID" ]; then
  APP_ID=${PERSONA_BUNDLE_ID:-$(python3 scripts/persona_config.py get app.bundle_id 2>/dev/null || true)}
  [ -n "$APP_ID" ] || { echo "Persona Link needs a bundle id: set mac.link_bundle_id or app.bundle_id in ~/.config/persona/config.json, or PERSONA_LINK_BUNDLE_ID (docs/operations/config.md)" >&2; exit 2; }
  ID=$APP_ID.link
fi
PKG="$ROOT/apple/PersonaLink"
OUT="$PKG/build"
APP="$OUT/Persona Link.app"

build() {
  local version archs=() helpers=() nodes=()
  version="$(git describe --tags --match 'v[0-9]*' --always --dirty)-$(date +%Y%m%d%H%M)"
  if [ "${UNIVERSAL:-}" = 1 ]; then archs=(arm64 x86_64); else archs=("$(uname -m)"); fi
  # Persona Screen, the live screen's video helper (#3954): a scheme of its own in the package.
  (cd "$PKG" && xcodebuild -quiet -scheme PersonaScreen -configuration Release -derivedDataPath build/dd \
    -destination 'generic/platform=macOS' ARCHS="${archs[*]}" ONLY_ACTIVE_ARCH=NO build)
  mkdir -p "$OUT"
  for a in "${archs[@]}"; do
    local goarch=$a
    [ "$a" = x86_64 ] && goarch=amd64
    (cd server && GOOS=darwin GOARCH=$goarch CGO_ENABLED=0 go build -buildvcs=false -tags "$(cat build-tags.txt)" -trimpath \
      -ldflags "-s -w -X main.version=$version" -o "$OUT/persona-link-$a" ./cmd/persona-link)
    helpers+=("$OUT/persona-link-$a")
    # The resident node the independent installer can set up (persona-link node install --node-bin).
    (cd server && GOOS=darwin GOARCH=$goarch CGO_ENABLED=0 go build -buildvcs=false -tags "$(cat build-tags.txt)" -trimpath \
      -ldflags "-s -w -X main.version=$version" -o "$OUT/persona-node-$a" ./cmd/persona-node)
    nodes+=("$OUT/persona-node-$a")
  done
  rm -rf "$APP"
  mkdir -p "$APP/Contents/Helpers"
  lipo -create "${helpers[@]}" -output "$APP/Contents/Helpers/persona-link"
  lipo -create "${nodes[@]}" -output "$APP/Contents/Helpers/persona-node"
  # Persona Screen.app (docs/extensions/computer-use.md "Live screen video (#3954)"): its own bundle owns the
  # Screen Recording grant, not the PC App or the node. Build it the same way so the grant survives Link updates.
  local screen="$APP/Contents/Helpers/Persona Screen.app"
  mkdir -p "$screen/Contents/MacOS"
  cp "$OUT/dd/Build/Products/Release/PersonaScreen" "$screen/Contents/MacOS/PersonaScreen"
  sed -e "s/__SCREEN_BUNDLE_ID__/$ID.screen/" "$PKG/PersonaScreen-Info.plist" > "$screen/Contents/Info.plist"
  # Ad-hoc signatures keep local development self-contained; production signing is a separate authorized step.
  codesign --force --sign - --identifier "$ID.helper" "$APP/Contents/Helpers/persona-link"
  codesign --force --sign - --identifier "$ID.persona-node" "$APP/Contents/Helpers/persona-node"
  codesign --force --sign - "$screen"
  codesign --verify --strict "$APP/Contents/Helpers/persona-link" "$APP/Contents/Helpers/persona-node" "$screen"
  echo "built $APP ($ID, persona-link $version, no menu bar UI)"
}

case "${1:-build}" in
  build) build ;;
  install)
    build
    rm -rf "/Applications/Persona Link.app"
    ditto "$APP" "/Applications/Persona Link.app"
    # The helpers have stable paths for the node's Persona Screen lookup and the desktop's separately installed Link.
    # The bundle is an install location, not a launchable UI: launchd owns the connector and its login startup.
    "$APP/Contents/Helpers/persona-link" install --node-bin "$APP/Contents/Helpers/persona-node"
    echo "installed independent Persona Link at ~/.persona/bin/persona-link (launchd com.persona.link; no menu bar UI)" ;;
  *) echo "usage: $0 [build|install]" >&2; exit 2 ;;
esac
