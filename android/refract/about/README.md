# The single Android About Version consumer (#9694)

This directory belongs to a **bounded engineering experiment**, not a product feature. It serves the maintainer
reviewing one compile-time native generator against one existing Persona row. The everyday-facing Settings › About
and updates screen has no generator terminology, setting, switch or new visual behavior.

## Frozen scope and rollback

- `AboutVersionRow.swift` owns one read-only Version row and a flat typed `AboutVersionModel`. `refract.json` maps
  that source directly to Persona's existing `JRow` and `settings_about_version` / `settings_not_provided` resources.
  It adds no component, token, style, runtime, opaque target source, clipboard or build/commit behavior.
- `AboutVersionSnapshot.kt` owns package metadata and localized missing-version resolution. The immutable model
  adapter initializes its read-only Compose `State` before presentation; generated UI reads no Android framework
  API. Build and the parsed `BuildCommit`, its short value and its copy action remain in the untouched native files.
- `DeviceSettingsPages.kt` is deliberately unchanged: `AboutSettingsPage` is still the ordinary production and
  rollback implementation. `AboutVersionRefractTest` calls the generated row for comparison, not as an alternate
  production route. No user preference, hidden flag, build flavor, global initialization or new application is added.
- Removing this directory, the generated Kotlin, model adapter/test and the explicit CI check leaves the current
  screen intact. No migration or persisted experiment state exists.

This is an **Android-only consumer experiment**, not a claim of three-platform readiness, performance parity or a
complete Persona migration. The CLI's schema requires both platforms: its second output is generated only into a
temporary directory and discarded. `harmony-resource-validation-only/` is therefore a schema-input fixture, not a
HarmonyOS destination, native component, application, compiler claim or committed ArkTS output. There is no iOS host
or target registration for this pilot.

## Reproducible source and generation

`toolchain.json` pins public Refract source commit `e32413fba1f13ff0652dedea5f96fc49ec4a8c96`, the raw Git-tree
and extracted-archive-tree SHA-256 values (the archive honors `Fixtures/** -text`, preserving fixture CRLF), its
source-archive SHA-256, mapping schema 1, and SwiftSyntax 600.0.1 at revision `0687f71944021d616d34d922343dcef086855920`.
This is an experimental source snapshot, **not a supported release** and not production-readiness acceptance. The
upstream scalar-model fix it contains was separately reviewed and merged through Refract's exact-head workflow.

```sh
# Read-only, offline with an already-built CLI: check its source identity and reject Kotlin drift.
python3 scripts/app/refract_about_version.py check --cli <path-to-pinned-refract>

# Explicitly opt into rebuilding the pinned public source in an isolated temporary toolchain.
python3 scripts/app/refract_about_version.py check --download

# The only command allowed to update the reviewed generated Kotlin. Run from Persona's root and review its diff.
python3 scripts/app/refract_about_version.py generate --cli <path-to-pinned-refract>
```

Without `--download` no network request is made. `PERSONA_REFRACT_CLI` or an untracked `.toolchain/refract` may
supply that already-built CLI; otherwise the command fails with a clear prerequisite rather than claiming generator
verification. Its `refract --version` must report the pinned complete source commit. Swift is needed only for an
explicit download/build, not for ordinary Gradle builds or the offline check.

The command validates the lock, exact Git commit, complete source tree and archived SwiftSyntax revision. It uses
the pinned packager's canonical metadata and Git-blob source selection to build a deterministic source archive twice;
both archives must match SHA-256 `522ec930a5b5de652acc090f1734e7d3440b18ad404a437927f713b2c3ad23fa` and each
other before extraction. The upstream packaging implementation is in the locked source repository's packaging script.
This avoids treating a download cache's changing gzip container as stable source. No dirty checkout or unverified
archive is accepted. It never downloads or commits an opaque binary, accepts arbitrary generated target text, or
patches Kotlin afterward. The same invocation
is repeated for byte-identical output. `scripts/ci/android.sh` explicitly selects `--download` for this read-only
check in its lint/build lane; a missing Swift toolchain, download failure or source mismatch fails the check rather
than silently skipping it. An ordinary Gradle build does not fetch or run the generator.

Generated `android/feature/settings/src/test/kotlin/persona/feature/settings/generated/AboutVersionRow.kt` imports
only Compose and Persona code. Both it and `AboutVersionSnapshot.kt` live only in `:feature:settings`'s test source
set; ordinary debug/release production source sets contain neither the generated row nor its adapter. Its model is
`State<String>` and its native row is `JRow`, using the same typography, colors, semantics and localization as the
unchanged implementation. Any visual difference in the comparison matrix blocks this pilot rather than justifying a
style change.

## Evidence and limits

`AboutVersionRefractTest` compares actual generated versus handwritten pixels across Glass, Brief and Board, light
and dark, en/zh-Hans/ja, and standard/2x text; it also exercises the missing-version fallback, the unchanged
three-row rollback and the existing native Commit tests. Captures go to build output or `-Dpersona.shots=<dir>` and
are attached to the PR, never committed. These are **Native JVM Compose component captures**, not device/emulator,
system-bar, navigation, accessibility-service or performance evidence. No such broader result is claimed by this
consumer.
