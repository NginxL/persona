#!/bin/bash
# Decides whether a version tag publishes desktop packages and whether Darwin CLI signing is available.
#
# A release never ships an unsigned or ad-hoc package as if it were final: each platform is built only when every
# signing credential it needs is configured. The caller passes "true"/"false" per credential (HAS_*), never a value.
# Missing credentials are not a failure: the hub image and GitHub Release are published, with each blocked signing
# result reported separately. Darwin CLI availability is independent of the desktop package version and credential policy.
set -euo pipefail

tag="${RELEASE_TAG:?RELEASE_TAG is required}"
version="${tag#v}"
has() { [[ "${!1:-false}" == true ]]; }
out() { printf '%s=%s\n' "$1" "$2" >> "${GITHUB_OUTPUT:-/dev/stdout}"; }
say() {
  local message="$1" title="${2:-Desktop packages}"
  printf '%s\n' "$message"
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then printf -- '- %s\n' "$message" >> "$GITHUB_STEP_SUMMARY"; fi
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then printf '::notice title=%s::%s\n' "$title" "$message"; fi
}

missing_for() {
  local name missing=()
  for name in "$@"; do
    has "HAS_$name" || missing+=("$name")
  done
  printf '%s' "${missing[*]:-}"
}

darwin_cli=false
missing="$(missing_for MAC_CSC_LINK MAC_CSC_KEY_PASSWORD)"
if [[ -n "$missing" ]]; then
  say "Darwin CLI signing blocked: Environment secrets not configured: $missing." "Darwin CLI signing"
elif [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ || "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.-]+$ ]]; then
  darwin_cli=true
  say "Darwin CLI signing: Developer ID certificate is configured for $version." "Darwin CLI signing"
else
  say "Darwin CLI signing blocked: tag $tag is not a semantic version." "Darwin CLI signing"
fi

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
out darwin_cli "$darwin_cli"
