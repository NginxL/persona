#!/bin/sh
# Print GOFLAGS with -trimpath added, for the scripts that build and test Go (#10614):
#   flags=$(sh scripts/test/go_trimpath.sh) && export GOFLAGS="$flags"
# Without -trimpath, Go's build cache key includes each package's absolute directory, so every worktree compiles and
# caches every package again; with it, the same package at the same commit is one cache entry in all of them.
# The base is the effective GOFLAGS (the environment, else `go env -w`), kept as it is. A value that already sets the
# flag, -trimpath=false included, is printed unchanged. docs/operations/testing.md, "Go build cache across worktrees".
flags=$(go env GOFLAGS 2>/dev/null) || flags=${GOFLAGS-}
set -f
for flag in $flags; do
  case $flag in
    -trimpath | -trimpath=* | --trimpath | --trimpath=*) printf '%s\n' "$flags"; exit 0 ;;
  esac
done
printf '%s\n' "${flags:+$flags }-trimpath"
