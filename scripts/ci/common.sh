#!/bin/bash
set -euo pipefail
CI_IMPLEMENTATION_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# A trusted verifier may keep its executable CI implementation separate from
# the exact source checkout under test. Default CI behavior is unchanged.
if [ -n "${PERSONA_CI_SOURCE_ROOT:-}" ]; then
  case "$PERSONA_CI_SOURCE_ROOT" in
    /*) CI_ROOT="$PERSONA_CI_SOURCE_ROOT" ;;
    *) printf 'ci: FAIL PERSONA_CI_SOURCE_ROOT must be absolute\n' >&2; exit 2 ;;
  esac
else
  CI_ROOT="$CI_IMPLEMENTATION_ROOT"
fi
cd "$CI_ROOT"
ci_run() {
  local label="$1" seconds="$2"
  shift 2
  CI_IMPLEMENTATION_ROOT="$CI_IMPLEMENTATION_ROOT" \
  python3 "$CI_IMPLEMENTATION_ROOT/scripts/ci/run.py" --label "$label" --timeout "$seconds" -- "$@"
}
# ci_run resolves ci_python/ci_node from this implementation directory. The
# source checkout never chooses a different checker through a shell function.
# Installs the shared Rust core's pinned toolchain (native/app-core/rust-toolchain.toml) when it is missing.
ci_rust_toolchain() {
  local channel
  channel=$(sed -n 's/^channel = "\(.*\)"$/\1/p' "$CI_ROOT/native/app-core/rust-toolchain.toml")
  [ -n "$channel" ] || { printf 'ci: FAIL cannot read native/app-core/rust-toolchain.toml\n' >&2; return 1; }
  command -v rustup >/dev/null || { printf 'ci: FAIL rustup is required (https://rustup.rs)\n' >&2; return 1; }
  rustup run "$channel" cargo --version >/dev/null 2>&1 ||
    rustup toolchain install "$channel" --profile minimal --component rustfmt,clippy
}
