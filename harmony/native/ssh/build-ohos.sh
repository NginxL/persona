#!/bin/sh
set -eu

: "${OHOS_SDK_NATIVE:?Set OHOS_SDK_NATIVE to the API 26 SDK native directory}"
SSH_PROJECT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ ! -f "$SSH_PROJECT_DIR/Cargo.lock" ]; then
  printf '%s\n' 'Cargo.lock is required. Generate and review it offline before building.' >&2
  exit 1
fi
for SSH_REQUIRED in llvm/bin/clang llvm/bin/clang++ llvm/bin/llvm-ar build/cmake/ohos.toolchain.cmake; do
  if [ ! -f "$OHOS_SDK_NATIVE/$SSH_REQUIRED" ]; then
    printf 'Missing SDK component: %s\n' "$SSH_REQUIRED" >&2
    exit 1
  fi
done
export OHOS_SDK_NATIVE
# Homebrew can expose rustup without rustc shims on PATH. Resolve the pinned compiler explicitly.
if [ -z "${RUSTC:-}" ]; then RUSTC=$(rustup which --toolchain 1.98.0 rustc); export RUSTC; fi
if [ -z "${RUSTDOC:-}" ]; then RUSTDOC=$(rustup which --toolchain 1.98.0 rustdoc); export RUSTDOC; fi
export CARGO_NET_OFFLINE=true
export CARGO_TARGET_AARCH64_UNKNOWN_LINUX_OHOS_LINKER="$SSH_PROJECT_DIR/toolchain/clang-ohos.sh"
export CC_aarch64_unknown_linux_ohos="$SSH_PROJECT_DIR/toolchain/clang-ohos.sh"
export CXX_aarch64_unknown_linux_ohos="$SSH_PROJECT_DIR/toolchain/clangxx-ohos.sh"
export AR_aarch64_unknown_linux_ohos="$OHOS_SDK_NATIVE/llvm/bin/llvm-ar"
export CMAKE_TOOLCHAIN_FILE_aarch64_unknown_linux_ohos="$OHOS_SDK_NATIVE/build/cmake/ohos.toolchain.cmake"
# Target-generated AWS-LC bindings must use the SDK's Clang and OHOS system headers.
if [ -z "${LIBCLANG_PATH:-}" ]; then LIBCLANG_PATH="$OHOS_SDK_NATIVE/llvm/lib"; export LIBCLANG_PATH; fi
if [ -z "${CLANG_PATH:-}" ]; then CLANG_PATH="$OHOS_SDK_NATIVE/llvm/bin/clang"; export CLANG_PATH; fi
if [ -z "${BINDGEN_EXTRA_CLANG_ARGS_aarch64_unknown_linux_ohos:-}" ]; then
  BINDGEN_EXTRA_CLANG_ARGS_aarch64_unknown_linux_ohos="--target=aarch64-linux-ohos --sysroot=\"$OHOS_SDK_NATIVE/sysroot\" -D__MUSL__"
  export BINDGEN_EXTRA_CLANG_ARGS_aarch64_unknown_linux_ohos
fi
export PATH="$OHOS_SDK_NATIVE/build-tools/cmake/bin:$OHOS_SDK_NATIVE/build-tools/ninja:$PATH"
cd "$SSH_PROJECT_DIR"
exec rustup run 1.98.0 cargo build --locked --offline --release --target aarch64-unknown-linux-ohos
