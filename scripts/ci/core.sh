#!/usr/bin/env bash
# The shared Rust core (native/app-core, docs/plans/plan-rust-core.md): formatting, lints, unit tests, the shared
# fixtures it implements, the generated client codecs up to date, and the release library. Requires rustup; the
# pinned toolchain is installed when missing. No private config, SDKs or devices.
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_rust_toolchain
cd "$CI_ROOT/native/app-core"
ci_run 'Rust formatting' 300 cargo fmt --all --check
ci_run 'Rust lints' 1200 cargo clippy --locked --all-targets -- -D warnings
ci_run 'Rust tests and shared fixtures' 1200 cargo test --locked
ci_run 'Generated codecs up to date' 900 cargo run --locked --quiet -p persona-core-typegen -- --check
ci_run 'Release library' 1200 cargo build --locked --release -p persona-core-ffi
