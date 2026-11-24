#!/bin/bash
# Writes what a Debug build was built from into its built Info.plist, under the dictionary PersonaBuildInfo, for
# Settings › About and its Debug info section (apple/Persona/App/BuildInfo.swift, #12926). Called by the "Stamp the
# debug build info" phase in apple/project.yml; Release builds keep "Stamp the commit" alone, as before.
#
#   stamp_debug_build_info.sh <built Info.plist> <executable> <marker file> <checkout>
#
# It never fails the build: anything it cannot learn (no git, a detached HEAD, no Xcode Info.plist) is left out and the
# app shows Unknown for it. It writes only when the code was linked again, the Info.plist was written again or HEAD
# moved since its marker, so a build with nothing changed rewrites nothing and signs nothing again (#10713). A Debug
# build keeps its code in <executable>.debug.dylib (previews are on), so that file is the one compared when it exists.
#
# Optional environment: PERSONA_BUILD_MACHINE names the build machine in place of the Mac's computer name (for captures
# shown in public); CONFIGURATION, DEVELOPER_DIR and XCODE_PRODUCT_BUILD_VERSION come from Xcode.
set -u
plist=${1:?Info.plist} executable=${2:?executable} marker=${3:?marker} checkout=${4:?checkout}
[ -f "$plist" ] || exit 0
binary=$executable
[ -f "$executable.debug.dylib" ] && binary=$executable.debug.dylib
headlog=$(git -C "$checkout" rev-parse --path-format=absolute --git-path logs/HEAD 2>/dev/null) || headlog=

newer() { [ -e "$1" ] && [ "$1" -nt "$marker" ]; }
if [ -f "$marker" ] && ! newer "$plist" && ! newer "$binary" && { [ -z "$headlog" ] || ! newer "$headlog"; }; then
  exit 0
fi

commit=$(git -C "$checkout" rev-parse --short=10 HEAD 2>/dev/null) || commit=
branch=$(git -C "$checkout" symbolic-ref --short -q HEAD 2>/dev/null) || branch=
dirty=
if [ -n "$commit" ]; then
  # Same question as the Release stamp's "+": tracked files differ from HEAD. 1 = they do; anything else but 0 = unknown.
  git -C "$checkout" diff --quiet HEAD -- 2>/dev/null
  case $? in 0) dirty=false ;; 1) dirty=true ;; esac
fi
machine=${PERSONA_BUILD_MACHINE:-$(scutil --get ComputerName 2>/dev/null || hostname -s 2>/dev/null || true)}
xcode=
if [ -n "${DEVELOPER_DIR:-}" ]; then
  xcode=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$DEVELOPER_DIR/../Info.plist" 2>/dev/null) || xcode=
fi
[ -n "$xcode" ] && [ -n "${XCODE_PRODUCT_BUILD_VERSION:-}" ] && xcode="$xcode ($XCODE_PRODUCT_BUILD_VERSION)"
swift=$(xcrun swiftc --version 2>/dev/null | sed -n 's/.*Swift version \([0-9][0-9.]*\).*/\1/p' | head -1) || swift=

key=PersonaBuildInfo
plutil -remove "$key" "$plist" >/dev/null 2>&1
plutil -insert "$key" -dictionary "$plist" >/dev/null 2>&1 || exit 0
put() { [ -n "$2" ] && plutil -insert "$key.$1" -string "$2" "$plist" >/dev/null 2>&1; return 0; }
put commit "$commit"
put branch "$branch"
[ -n "$dirty" ] && plutil -insert "$key.dirty" -bool "$dirty" "$plist" >/dev/null 2>&1
put builtAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
put builder "$machine"
put xcode "$xcode"
put swift "$swift"
put configuration "${CONFIGURATION:-}"
mkdir -p "$(dirname "$marker")" && touch "$marker"
exit 0
