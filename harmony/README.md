# Persona for HarmonyOS

Native ArkTS / ArkUI client for HarmonyOS 7 phones and foldables (API 26). See the [product, interaction and architecture guide](../docs/harmony/harmony.md).

Open this directory in DevEco Studio with the HarmonyOS 7 SDK installed, resolve the pinned OHPM dependencies, and select a phone, foldable or wide-foldable emulator. Build the pinned ARM64 SSH core first using [the native build instructions](native/ssh/README.md). With that library and DevEco CLI available:

```sh
devecocli build
devecocli device list
devecocli run --skip-build --device '<device serial>'
```

The checked-in bundle identity is a development example. Production signing and the registered application identity come from the maintainer's private configuration; no credentials or signing material belong in this directory. Local toolchain configuration, signing files, OHPM installations and build outputs are ignored.

Portable model and protocol tests require Node.js 24 or newer:

```sh
node --test tests/*.test.mjs
node tests/run-domain.mjs
```

Native SDK tests use the `entry@ohosTest` target and `OpenHarmonyTestRunner`; they exercise actual platform storage, credentials, URL handling, TLS/WebSocket transport and image preview. The transport fixture is separate from a real Hub; see [verification](../docs/harmony/harmony.md#verification). The [native extension fixture](tests/native-extension-fixture/README.md) reproduces legacy and streamed component updates, focus retention and the Hub confirmation gate on a disposable Hub.

The application icon reuses Persona's approved artwork. Run `python3 scripts/export-icons.py` from this directory to regenerate its layers, startup image and provenance manifest. The system applies its own launcher mask.
