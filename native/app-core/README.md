# Persona's shared client core

One Rust library under the iOS, Android and HarmonyOS clients' native UIs. Design, decisions and phases:
[the plan](../../docs/plans/plan-rust-core.md) (#6793). Phase 1 is #6871.

## Layout

| Crate | What it is |
| --- | --- |
| `persona-core` | Pure logic: no IO, no clock, no FFI, no `unsafe`. Today it holds `continuity` (a chat's items, how far they are complete, and what to fetch), ported from the rules every client implements (`spec/fixtures/continuity`). |
| `persona-core-runtime` | Runs `persona-core` on one worker thread. `dispatch` only queues an action's bytes. The worker applies actions, sends requests to the client and builds each chat's patch. `changed` wakes the client once per batch, and the client pulls the patch with `drain`. `wire` holds every type that crosses the boundary. |
| `persona-core-ffi` | The entry points, built as `libpersona_core`: C functions for Swift (`include/persona_core.h`) and JNI functions for Android's `persona.core.rustcore.NativeBridge`. Each catches panics and returns its failure value. |
| `persona-core-ohos` | HarmonyOS's single NAPI library. Its default host build tests the close-safe runtime registry and all shared fixtures; the `ohos` feature adds ohos-rs's NAPI entry points and thread-safe callbacks, supplied by the device runtime. |
| `persona-core-typegen` | Generates the clients' bincode codecs for the `wire` types: Kotlin to android/core/rustcore, Swift to apple/Packages/PersonaCore and ArkTS to harmony/entry/src/main/ets/core/rustcore. All are committed and checked for drift. |

The hub's item fields are preserved in `ListOp::Insert` / `ListOp::Update`, including supervision promotion retained by
iOS. An updated item keeps its stable list identity instead of becoming a remove/insert pair.

## Build and test

Needs rustup; `rust-toolchain.toml` pins the compiler.

```sh
cargo test --locked                                # unit tests and the shared fixtures, directly and through the runtime
cargo run --locked -p persona-core-typegen          # regenerate the clients' codecs after changing a wire type
cargo run --locked -p persona-core-typegen -- --check
cargo build --locked --release -p persona-core-ffi # libpersona_core.{a,so,dylib}
```

`scripts/ci/core.sh` runs all of these, plus formatting and clippy. Android's `:core:rustcore` tests build a host copy of
the library and replay the same fixtures through the real JNI binding:

```sh
cd android && ./gradlew :core:rustcore:test
```

Android's production packaging lives in `:core:rustcore-android`, leaving the JVM/JNI replay above intact. Its pinned
NDK 29.0.14206865 / direct LLVM build, ABI sizes, uncompressed 16 KB packaging and real device fixture replay are
described in `android/core/rustcore-android/README.md`. `scripts/build-android.sh` produces both ABIs without
`cargo-ndk`; the Gradle task declares the core inputs so unchanged sources do not recompile Rust.

## Rules

- **A wire type change ships with its regenerated codecs.** Never edit generated files by hand.
- **Fixtures come first.** A rule changes in `spec/fixtures/<domain>`, then here; expectations are never generated from
  this code.
- **Text is out of scope.** No user-facing text: the core returns codes and enums, and each client words them from its
  own localization resources.
- **The main thread stays cheap.** Calls a client makes on its main thread (`dispatch`, `drain`) never do work that grows
  with a chat; that work belongs to the worker.
