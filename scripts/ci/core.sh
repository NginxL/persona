#!/usr/bin/env bash
# The shared Rust core (native/app-core, docs/plans/plan-rust-core.md): formatting, lints, unit tests, the shared
# fixtures it implements, the generated client codecs up to date, the HarmonyOS binding's lints and notices, and the
# release library. Requires rustup; the pinned toolchain and the HarmonyOS Rust target are installed when missing.
# No private config, SDKs or devices.
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_rust_toolchain
cd "$CI_ROOT/native/app-core"
ci_run 'Rust formatting' 300 cargo fmt --all --check
ci_run 'Rust lints' 1200 cargo clippy --locked --all-targets -- -D warnings
ci_run 'Rust tests and shared fixtures' 1200 cargo test --locked
ci_run 'Generated codecs up to date' 900 cargo run --locked --quiet -p persona-core-typegen -- --check
# The NAPI binding exists only behind the `ohos` feature, which the host build above never compiles. Checking it for
# the device target needs the Rust target but no SDK: nothing is linked, so OHOS_NDK_HOME (read by napi-build-ohos
# for its link directories) only has to be set.
ci_run 'HarmonyOS Rust target' 600 rustup target add aarch64-unknown-linux-ohos
ci_run 'HarmonyOS binding lints' 1200 env OHOS_NDK_HOME="$CI_ROOT/native/app-core/target/no-ohos-sdk" cargo clippy --locked --target aarch64-unknown-linux-ohos -p persona-core-ohos --features ohos -- -D warnings
# Writes the notices the HAP packages (an ignored build output) from Cargo.lock and the registry sources the step
# above unpacked. It fails when a locked package has no notice or a vendored upstream notice changed.
ci_run 'HarmonyOS core licenses' 300 python3 -I scripts/android-licenses.py --workspace . --registry-src "${CARGO_HOME:-$HOME/.cargo}/registry/src" --output "$CI_ROOT/harmony/build/core-licenses" --crate persona-core-ohos --target aarch64-unknown-linux-ohos --features ohos --label HarmonyOS
ci_run 'Release library' 1200 cargo build --locked --release -p persona-core-ffi
