#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
target="${1:?usage: scripts/ci/desktop-package.sh mac|win}"
case "$target" in
  mac) platform=mac ;;
  win) platform=win ;;
  *) printf 'usage: scripts/ci/desktop-package.sh mac|win\n' >&2; exit 2 ;;
esac
if [[ -n "${CSC_LINK:-}" ]]; then
  ci_run 'Desktop signed package' 2400 npm --prefix desktop run dist -- --"$platform" --publish never
else
  printf 'desktop-package: no signing certificate supplied; producing an explicitly unsigned development package\n'
  ci_run 'Desktop unsigned package' 2400 npm --prefix desktop run dist:unsigned -- --"$platform" --publish never
fi
