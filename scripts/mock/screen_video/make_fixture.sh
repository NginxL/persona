#!/bin/bash
# Makes scripts/mock/screen_video/fixture.bin again (the mock hub's live-screen video, #3954): builds make_fixture.swift
# with PersonaScreenKit's sources on this Mac and runs it. Needs Xcode (VideoToolbox's HEVC encoder, else H.264).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
xcrun swiftc -O -parse-as-library -o "$tmp/make_fixture" "$here/make_fixture.swift" \
  "$root"/apple/Packages/PersonaScreenKit/Sources/PersonaScreenKit/*.swift
"$tmp/make_fixture" "$here/fixture.bin"
