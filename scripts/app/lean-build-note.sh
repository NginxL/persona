#!/bin/bash
# The one line scripts/app/sim.sh prints after it generates a lean development build (#10713, docs/operations/testing.md
# "Development builds") when this branch changes what that build leaves out: apple/Shared/, which the watch app also
# compiles for watchOS 10 and the share extension as an app extension, the watch app and its widgets, or the share
# extension. No hosted CI runs on a PR, so a watchOS-only API or an extension-unsafe call would otherwise surface first on
# main. Compares the worktree, uncommitted and new files included, with its merge base with origin/main. Prints nothing
# when none of them changed or git cannot tell (no origin/main), and always exits 0.
#   scripts/app/lean-build-note.sh
cd "$(dirname "$0")/../.." || exit 0
WATCH=(apple/PersonaWatch apple/PersonaWatchWidgets)
WATCH_AND_SHARE=(apple/Shared apple/PersonaShare)
base=$(git merge-base HEAD origin/main 2>/dev/null) || exit 0
changed=$({
  git diff --name-only "$base" -- "${WATCH[@]}" "${WATCH_AND_SHARE[@]}" &&
    git ls-files --others --exclude-standard -- "${WATCH[@]}" "${WATCH_AND_SHARE[@]}"
} 2>/dev/null) || exit 0
[ -n "$changed" ] || exit 0
folders=$(printf '%s\n' "$changed" | cut -d/ -f1-2 | sort -u | sed 's|$|/|' | paste -sd ' ' -)
if printf '%s\n' "$changed" | grep -qE '^apple/(Shared|PersonaShare)/'; then
  # The share extension has no scheme of its own: the whole build is the check.
  check='scripts/app/sim.sh --with-watch'
else
  # The watch alone: the scheme is in the lean project too, and build/DerivedData has sim.sh's package checkouts.
  check="xcodebuild -project apple/Persona.xcodeproj -scheme PersonaWatch -destination 'generic/platform=watchOS Simulator' -derivedDataPath build/DerivedData build"
fi
printf 'sim.sh: this lean build leaves out the watch app (watchOS 10) and the share extension, and this branch changes %s; before the PR, check them with %s\n' \
  "$folders" "$check"
exit 0
