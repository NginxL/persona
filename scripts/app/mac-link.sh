#!/bin/bash
# Independently installable Persona Link: persona-link, persona-node, Persona Screen and Persona Input, no desktop UI
# required.
# The bundle id is PERSONA_LINK_BUNDLE_ID, else mac.link_bundle_id in ~/.config/persona/config.json, else the app's
# registered identity (PERSONA_BUNDLE_ID, else app.bundle_id) + ".link". Never a placeholder: macOS keeps Persona
# Screen's Screen Recording grant per bundle id, so a later build with another id has to be allowed again (#7569).
# UNIVERSAL=1 builds for Apple silicon and Intel together. `verify-screen` and `verify-input` check that Persona Screen
# and Persona Input build to the same bits from two clean copies at different paths (no bundle id needed).
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
link_id() {
  ID=${PERSONA_LINK_BUNDLE_ID:-$(python3 scripts/persona_config.py get mac.link_bundle_id 2>/dev/null || true)}
  if [ -z "$ID" ]; then
    APP_ID=${PERSONA_BUNDLE_ID:-$(python3 scripts/persona_config.py get app.bundle_id 2>/dev/null || true)}
    [ -n "$APP_ID" ] || { echo "Persona Link needs a bundle id: set mac.link_bundle_id or app.bundle_id in ~/.config/persona/config.json, or PERSONA_LINK_BUNDLE_ID (docs/operations/config.md)" >&2; exit 2; }
    ID=$APP_ID.link
  fi
}
PKG="$ROOT/apple/PersonaLink"
OUT="$PKG/build"
APP="$OUT/Persona Link.app"

# Persona Screen (#3954) and Persona Input (#7775, docs/extensions/computer-use.md "Pixel scrolling"): helper apps of
# their own, whose Screen Recording and Accessibility permissions macOS ties to their code while they are signed ad hoc.
# A Persona Link update must not change their bits unless their own sources changed (#7927): each executable is built
# with `swift build -c release` (linked with -S: no debug symbols, so no paths of this checkout), kept by a hash of its
# sources, the one narrow kit it uses (PersonaScreenCore, PersonaInputKit), its Info.plist template and the
# architectures, and reused byte for byte whatever builds Persona Link later (another checkout path, another Xcode).
# `verify-screen` / `verify-input` check that two clean copies at different paths build the same bits.
# With mac.link_signing_identity (or PERSONA_LINK_SIGNING_IDENTITY) the two helpers are signed with that certificate
# instead, and their permissions follow the certificate across any change (docs/operations/persona-link-install.md).
SCREEN_PKG="$PKG"
SCREEN_SOURCES=(apple/PersonaLink/Sources/PersonaScreen apple/PersonaLink/Package.swift apple/PersonaLink/PersonaScreen-Info.plist
                apple/Packages/PersonaScreenCore)
SCREEN_CACHE=${PERSONA_SCREEN_CACHE:-$HOME/.persona/cache/persona-screen}
INPUT_PKG="$PKG/Input"
INPUT_SOURCES=(apple/PersonaLink/Input apple/Packages/PersonaInputKit)
INPUT_CACHE=${PERSONA_INPUT_CACHE:-$HOME/.persona/cache/persona-input}
# sources_key "<archs>" <paths…>: a hash of those paths' Swift files and plists (not tests or build output) and the
# architectures.
sources_key() {
  local archs=$1
  shift
  { (cd "$ROOT" && find "$@" -type f \( -name '*.swift' -o -name '*.plist' \) \
      -not -path '*/.build/*' -not -path '*/Tests/*' | LC_ALL=C sort | while IFS= read -r f; do shasum -a 256 "$f"; done)
    echo "$archs"; } | shasum -a 256 | cut -c1-24
}
# swift_build_helper <package dir> <scratch dir> <product> <archs…>: builds it and prints its executable's path.
swift_build_helper() {
  local pkg=$1 scratch=$2 product=$3 flags=()
  shift 3
  for a in "$@"; do flags+=(--arch "$a"); done
  xcrun swift build -c release --package-path "$pkg" --scratch-path "$scratch" --product "$product" "${flags[@]}" >&2
  echo "$(xcrun swift build -c release --package-path "$pkg" --scratch-path "$scratch" --product "$product" "${flags[@]}" --show-bin-path)/$product"
}
# cached_helper <cache> <key> <package dir> <product> <archs…>: prints the kept executable, building it first if the key
# is new.
cached_helper() {
  local cache=$1 key=$2 pkg=$3 product=$4 dir scratch bin
  shift 4
  dir="$cache/$key"
  if [ ! -x "$dir/$product" ]; then
    scratch=$(mktemp -d "${TMPDIR:-/tmp}/$product.XXXXXX")
    bin=$(swift_build_helper "$pkg" "$scratch" "$product" "$@")
    mkdir -p "$cache"
    rm -rf "$dir.part"
    mkdir "$dir.part"
    cp "$bin" "$dir.part/$product"
    mv "$dir.part" "$dir"
    rm -rf "$scratch"
  fi
  echo "$dir/$product"
}
screen_binary() { cached_helper "$SCREEN_CACHE" "$(sources_key "$*" "${SCREEN_SOURCES[@]}")" "$SCREEN_PKG" PersonaScreen "$@"; }
input_binary() { cached_helper "$INPUT_CACHE" "$(sources_key "$*" "${INPUT_SOURCES[@]}")" "$INPUT_PKG" PersonaInput "$@"; }
# The certificate the helpers are signed with (its name as `security find-identity` prints it, or its SHA-1); "-",
# ad hoc, without one.
signing_identity() {
  local id=${PERSONA_LINK_SIGNING_IDENTITY:-$(python3 scripts/persona_config.py get mac.link_signing_identity 2>/dev/null || true)}
  echo "${id:--}"
}
# helper_bundle <bundle> <executable> <product> <Info.plist template> <placeholder> <bundle id> <identity>
helper_bundle() {
  mkdir -p "$1/Contents/MacOS"
  cp "$2" "$1/Contents/MacOS/$3"
  sed -e "s/$5/$6/" "$4" > "$1/Contents/Info.plist"
  # No timestamp: the signature is for this Mac's permissions, not a distribution, and a local build needs no network.
  codesign --force --sign "$7" --timestamp=none "$1"
}
screen_bundle() { helper_bundle "$1" "$2" PersonaScreen "$PKG/PersonaScreen-Info.plist" __SCREEN_BUNDLE_ID__ "$3" "${4:--}"; }
input_bundle() { helper_bundle "$1" "$2" PersonaInput "$INPUT_PKG/PersonaInput-Info.plist" __INPUT_BUNDLE_ID__ "$3" "${4:--}"; }
# verify_helper <screen|input>: two clean builds from copies at two different paths, ad-hoc signed: the same executable
# and the same signature.
verify_helper() {
  local which=$1 work arch out=() hashes=() name product pkg sources
  case "$which" in
    screen) name="Persona Screen"; product=PersonaScreen; pkg=apple/PersonaLink; sources=("${SCREEN_SOURCES[@]}") ;;
    input) name="Persona Input"; product=PersonaInput; pkg=apple/PersonaLink/Input; sources=("${INPUT_SOURCES[@]}") ;;
  esac
  work=$(mktemp -d "${TMPDIR:-/tmp}/$product-verify.XXXXXX")
  arch=$(uname -m)
  for copy in first "second copy"; do
    local root="$work/$copy/repo" path bin
    for path in "${sources[@]}"; do
      mkdir -p "$root/$(dirname "$path")"
      rsync -a --exclude .build --exclude build "$ROOT/$path" "$root/$(dirname "$path")/"
    done
    bin=$(swift_build_helper "$root/$pkg" "$work/$copy/scratch" "$product" "$arch")
    "${which}_bundle" "$work/$copy/$name.app" "$bin" "example.persona.link.$which"
    out+=("$(shasum -a 256 < "$bin" | cut -c1-16)")
    hashes+=("$(codesign -dvvv "$work/$copy/$name.app" 2>&1 | sed -n 's/^CDHash=//p')")
  done
  rm -rf "$work"
  if [ "${out[0]}" = "${out[1]}" ] && [ "${hashes[0]}" = "${hashes[1]}" ] && [ -n "${hashes[0]}" ]; then
    echo "verify-$which: same executable ${out[0]} and cdhash ${hashes[0]} from two paths"
  else
    echo "verify-$which: DIFFERENT executable ${out[0]} / ${out[1]}, cdhash ${hashes[0]} / ${hashes[1]}"
    return 1
  fi
}

build() {
  local version archs=() helpers=() nodes=()
  link_id
  version="$(git describe --tags --match 'v[0-9]*' --always --dirty)-$(date +%Y%m%d%H%M)"
  if [ "${UNIVERSAL:-}" = 1 ]; then archs=(arm64 x86_64); else archs=("$(uname -m)"); fi
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
  # Ad-hoc signatures keep local development self-contained; production signing is a separate authorized step.
  codesign --force --sign - --identifier "$ID.helper" "$APP/Contents/Helpers/persona-link"
  codesign --force --sign - --identifier "$ID.persona-node" "$APP/Contents/Helpers/persona-node"
  # Persona Screen.app (docs/extensions/computer-use.md "Live screen video (#3954)") and Persona Input.app: their own
  # bundles own the Screen Recording and Accessibility permissions, not the PC App or the node. Kept by their sources
  # (above), so the permissions survive Link updates; or signed with this Mac's certificate when one is set.
  local identity screen="$APP/Contents/Helpers/Persona Screen.app" input="$APP/Contents/Helpers/Persona Input.app"
  identity=$(signing_identity)
  screen_bundle "$screen" "$(screen_binary "${archs[@]}")" "$ID.screen" "$identity"
  input_bundle "$input" "$(input_binary "${archs[@]}")" "$ID.input" "$identity"
  codesign --verify --strict "$APP/Contents/Helpers/persona-link" "$APP/Contents/Helpers/persona-node" "$screen" "$input"
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
  verify-screen) verify_helper screen ;;
  verify-input) verify_helper input ;;
  *) echo "usage: $0 [build|install|verify-screen|verify-input]" >&2; exit 2 ;;
esac
