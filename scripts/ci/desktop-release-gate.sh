#!/bin/bash
# Decides whether a version tag publishes desktop packages, and says why when it does not.
#
# A release never ships an unsigned or ad-hoc package as if it were final: each platform is built only when every
# signing credential it needs is configured. The caller passes "true"/"false" per credential (HAS_*), never a value.
# Missing credentials are not a failure: the hub image and the GitHub Release are published without desktop packages.
set -euo pipefail

tag="${RELEASE_TAG:?RELEASE_TAG is required}"
version="${tag#v}"
has() { [[ "${!1:-false}" == true ]]; }
out() { printf '%s=%s\n' "$1" "$2" >> "${GITHUB_OUTPUT:-/dev/stdout}"; }
say() {
  printf '%s\n' "$1"
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then printf -- '- %s\n' "$1" >> "$GITHUB_STEP_SUMMARY"; fi
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then printf '::notice title=Desktop packages::%s\n' "$1"; fi
}

missing_for() {
  local name missing=()
  for name in "$@"; do
    has "HAS_$name" || missing+=("$name")
  done
  printf '%s' "${missing[*]:-}"
}

mac=false
win=false
# electron-builder and the updater compare versions as semver; a tag that is not one cannot be a desktop version.
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-beta\.[0-9]+)?$ ]]; then
  say "Desktop packages skipped: tag $tag is not v<major>.<minor>.<patch> or v<major>.<minor>.<patch>-beta.<N>."
else
  # electron-updater's default stable channel is latest.yml; a prerelease version chooses beta.yml. Both are
  # immutable-tag artifacts, and the stable channel never opts into prereleases. No certificate, no package.
  if [[ "$version" == *-* ]]; then
    say "Desktop prerelease packages use electron-updater's beta channel; stable latest.yml is not replaced."
  fi
  missing="$(missing_for PERSONA_DESKTOP_APP_ID MAC_CSC_LINK MAC_CSC_KEY_PASSWORD APPLE_API_KEY APPLE_API_KEY_ID APPLE_API_ISSUER)"
  if [[ -z "$missing" ]]; then
    mac=true
    say "macOS desktop packages: building signed and notarized, version $version."
  else
    say "macOS desktop packages skipped: Environment secrets not configured: $missing."
  fi
  missing="$(missing_for PERSONA_DESKTOP_APP_ID WIN_CSC_LINK WIN_CSC_KEY_PASSWORD)"
  if [[ -z "$missing" ]]; then
    win=true
    say "Windows desktop package: building signed, version $version."
  else
    say "Windows desktop package skipped: Environment secrets not configured: $missing."
  fi
fi
out version "$version"
out mac "$mac"
out win "$win"
