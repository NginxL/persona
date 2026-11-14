# Persona's shared client core

One Rust library under the iOS, Android and HarmonyOS clients' native UIs. Design, decisions and phases:
[the plan](../../docs/plans/plan-rust-core.md) (#6793). Phase 1 is #6871.

## Layout

| Crate | What it is |
| --- | --- |
| `persona-core` | Pure logic: no IO, no clock, no FFI, no `unsafe`. Today it holds `continuity` (a chat's items, how far they are complete, and what to fetch), ported from the rules every client implements (`spec/fixtures/continuity`). `transport` decodes the hub's continuity payloads from raw bytes into `continuity`'s inputs (see "Transport decoding" below). |
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

## Reproducing phase-1 evidence

These are experiment entry points, not claims that every cell has run. A host or simulator result never substitutes
for a physical-device result. Raw library bytes, stripped bytes, an app's packaged increment and store-download size
are separate measurements; gzip is not a store-download report. `docs/plans/plan-rust-core.md` keeps the measured
values and the open cells for #6871.

| Evidence | Command / entry point | What still has to be supplied or measured |
| --- | --- | --- |
| Core tests, codec drift, HarmonyOS binding lints and notices, release FFI | `scripts/ci/core.sh` | Rustup only; no phone or platform SDK. The lane adds the `aarch64-unknown-linux-ohos` Rust target and lints `persona-core-ohos` with its `ohos` feature, which the host build never compiles. Nothing is linked, so this is not evidence of a device library. |
| Real host-JNI boundary | `cd android && ./gradlew :core:rustcore:test` | JDK 21; host JVM tests do not need the Android NDK or a device. |
| Android ABIs and packaged bytes | Build the APK, then run `native/app-core/scripts/android-package-report.py` with the exact arguments in `android/core/rustcore-android/README.md`. | x86_64 Linux, JDK 21, SDK 37 and NDK 29.0.14206865. Reports raw/stripped/gzip/APK bytes and ELF/APK alignment, not store downloads. |
| Android real packaged-JNI replay | `native/app-core/scripts/android-device-test.py --serial <owned serial> --suite replay --apk </absolute/path/to/debug androidTest.apk> --output <report directory>` | An explicitly owned emulator or device; no hub is needed. The existing host and software-emulator passes do not establish physical arm64 or a 16 KiB kernel. |
| Android optimized benchmark | The same runner with `--suite benchmark` and the release androidTest APK | Physical arm64 / 16 KiB-kernel evidence is **blocked: no physical device** (maintainer decision, 2026-11-14); do not wait for it. If an arm64 Android 15+ device with a 16 KiB kernel later becomes available, rerun this existing command and retain its named device/build and raw samples. Keep the original 100 warm-ups, 1,000 samples and 50 µs / 1 ms budgets; an emulator failure stays a failure. |
| Apple XCFramework and real-C XCTest | `native/app-core/scripts/build-apple.sh`, then `xcrun swift test --package-path apple/Packages/PersonaCore -Xswiftc -warnings-as-errors` | macOS and Xcode; no physical phone is needed. `DEVELOPER_DIR=<Xcode 26.0.1>/Contents/Developer scripts/ci/apple.sh packages` checks the minimum compiler when that Xcode is installed. Other Xcode versions must be labelled "26.0 not verified". |
| Apple launch, stream, scroll and ten-open/close memory | `scripts/app/perf-test.sh` covers only its listed frame/hitch suites. A launch and ten-open/close memory entry is not yet implemented; do not infer those results from it. | Physical-iPhone evidence is **blocked: no physical device** (maintainer decision, 2026-11-14); do not wait for it. An owned simulator and named optimized build may still supply reference data. App Launch / Allocations / Animation Hitches are the procedures for a later device; an XCFramework or archive is not a download-size measurement. |
| HarmonyOS API 26 library | `OHOS_SDK_NATIVE=<API 26 native SDK> native/app-core/scripts/build-ohos.sh` | An installed API 26 native SDK, the Rust OHOS target and populated offline Cargo cache are still required. The script derives `OHOS_NDK_HOME` (which `napi-build-ohos` reads) from the directory that holds the SDK's `native` directory. With a stub SDK layout the build compiles the release closure and stops at the link step; an actual link has not been run. Host registry tests do not link the device NAPI. |
| HarmonyOS real-NAPI replay | `devecocli build --modules entry@ohosTest`, then install and run `aa test` with `coreReplay 1`; exact arguments are in `harmony/README.md`. | **Blocked: no physical device** (maintainer decision, 2026-11-14); do not wait for it. A future API 26/HDC environment must also have DevEco CLI and the native SDK. HAP linking, real NAPI callbacks, device timing and packaged increment remain unmeasured. |

Physical-device cells are no longer prerequisites for the next milestones. They stay explicitly blocked above rather than
being marked passed or supplied by a simulator. Xcode 26.0.1 minimum-compiler verification remains a separate Mac
toolchain task, not a physical-device requirement. Device selection, signing, SDK installation and license acceptance
belong to the maintainer's environment if it later becomes available; none is silently substituted by an emulator or
a release upload.

## Host-only boundary diagnostics

On an x86_64 Linux host, `cd android && ./gradlew :core:rustcore:boundaryCostTest` builds a separate optimized
`test-support` FFI library and runs the segmented Runtime/JNI/codec measurements. Ordinary `:core:rustcore:test`
excludes these measurement classes and keeps its debug host-JNI replay. The dedicated task writes raw nanoseconds and
metadata to `core/rustcore/build/reports/boundary-cost/boundary-cost.json` and
`core/rustcore/build/reports/codec-comparison/codec-comparison.json` (paths relative to `android/`). A missing native
library or measurement nonce fails the task rather than silently skipping evidence.

Each run uses 100 warm-ups and 1,000 retained samples per segment, with the same bounded item/action and a real
nonempty patch per frame. The frame is exactly JNI drain + Kotlin decode + list apply on the same patch bytes;
OpenChat and flush are excluded. `PERSONA_MEASUREMENT_DIAGNOSTICS=0` disables runtime diagnostics entirely; do not
interpret the absent native counters in that mode as zero cost. With diagnostics enabled, the `/proc` reads and lock
observation can dominate the measured spans. The registry lease, queue send, patch clone/encode and Kotlin decode
are reported separately and overlap rather than forming an additive wall-clock decomposition.

The codec comparison is an allocation microbenchmark with strict UTF-8 and matching validation on its fixed input,
not a general codec-equivalence or frame-performance proof. Host and emulator values are diagnostic references only:
they do not replace the original Android benchmark, the 50 µs / 1 ms budgets, or physical-device evidence.

## Rules

- **A wire type change ships with its regenerated codecs.** Never edit generated files by hand.
- **Fixtures come first.** A rule changes in `spec/fixtures/<domain>`, then here; expectations are never generated from
  this code.
- **Text is out of scope.** No user-facing text: the core returns codes and enums, and each client words them from its
  own localization resources.
- **The main thread stays cheap.** Calls a client makes on its main thread (`dispatch`, `drain`) never do work that grows
  with a chat; that work belongs to the worker.

## Transport decoding

`persona_core::transport` turns the bytes the host received into the values `continuity` takes (the plan's decision 2:
JSON is decoded once, in Rust). It is pure decoding: no boundary action carries bytes yet, and no client calls it.

| Function | Payload | iOS type it follows |
| --- | --- | --- |
| `decode_latest` | the latest page or an older page of `GET …/items` | `APIClient.ItemsResponse` |
| `decode_newer` | a catch-up page (`after_seq`) | `APIClient.NewerResponse` |
| `decode_range` | a page of an opened fold | `Range` in `APIClient.foldedWork` |
| `decode_frame` | a WebSocket frame: `Frame::Items`, or `Frame::Other` for any other type | `ServerEvent.decode` and its `Envelope` |

- Each item's JSON is kept byte for byte; only the keys the core's rules use are read.
- A refusal is a `DecodeError`: a reason code (`invalid_utf8`, `truncated_json`, `malformed_json`, `not_object`,
  `missing_key`, `wrong_type`, `bad_item`) and a detail made of positions and protocol key names. It never repeats
  payload content. One refused item refuses its page, named by the index of the first one.
- A key that appears more than once keeps its first value, as Foundation's keyed container does.
- `cargo test` replays the 17 wire cases these decoders own (`spec/fixtures/wire`: `items.json`, `item_events.json`
  and the `items` events of `compatibility.json`), all of them accepted payloads, and rebuilds every payload step of
  the continuity corpus from bytes, comparing the step and the state with the hand-built batch. Of the 79 expected
  fields of those cases, 52 are values the decoder produced; 25 are item fields the core does not read, compared with
  the item's kept JSON, and 2 are the frame's type.
- The refusal rules are tested by the crate's unit tests: every key a decoder reads, with every kind of wrong value.

It does not match iOS everywhere. The module's documentation has the full list; in short:

- **Keys only other events use.** iOS's `Envelope` is one struct for every event type, so iOS also drops an `items`
  frame when such a key (`rows`, `features`, `fleet`, …) has the wrong type. The core checks the keys an `items`
  frame carries and ignores the rest, so it does not follow every unrelated addition to the Swift struct.
- **Nested structs the core does not read.** iOS refuses a payload when an item's `image`, `task`, `compact`,
  `injected` or `model_refusal`, or a fold's `images` or `tasks`, fails to decode. The core accepts it.
- **Number spellings the hub never writes.** The core takes integer literals only and refuses `8.0` or `8e0`, which
  Foundation reads as 8. Reading an integer through a double exactly needs `serde_json`'s `float_roundtrip`
  reader, which added 33,328 B to the stripped x86_64 host library (741,032 B to 774,360 B) when measured.
- **Encoding and kinds.** The core takes only UTF-8 without a byte order mark, and keeps an unknown item `kind` as
  the hub wrote it.

Nothing here was executed on iOS. The rules were read from the Swift sources and from swift-foundation's
`JSONDecoder`; the wire cases' expectations are derived from the iOS sources as well (`derived: true`) and are checked
by the hub's golden test and the Android replay, and no iOS test reads those cases yet.
