# Independent test-only About Build generation (#13001)

This directory is an engineering capability probe for the maintainer evaluating #9620, not a product feature.
It leaves the existing handwritten About screen and the original Version consumer (`../about/`) byte-for-byte unchanged.
No generator terminology appears in the product UI.

## Scope and rollback

- Persona owns `AboutBuildRow.swift`, this mapping, the immutable `AboutBuildSnapshot.kt` test input/adapter and
  `AboutBuildRefractTest.kt` / `AboutBuildSemanticsTest.kt`. They cover only a Build row, never a whole page or a
  production variant.
- The current subset does **not** accept a general nullable model protocol. This consumer uses its supported
  optional scalar **view input**, `let buildNumber: String?`, and explicit `if let` / `else` branches.
  This evidence is not a claim of nullable domain-object or `StateFlow` support.
- The generated Kotlin directly calls the existing `JRow` and Material `Text`, with existing
  `PersonaTheme.type.micro` and `PersonaTheme.palette.ink3`. The trailing `Text` is a typed mapped native parameter,
  and `JRow`'s lambda is explicitly mapped as `trailing = { ... }`. The initial `trailingClosure` mapping produced a
  positional lambda that Kotlin rejected against `JRow`'s `Int` subtitleLines parameter; the generator output was
  corrected through the mapping, not a post-generation edit. Arbitrary styled `Text` children are not relied on.
- Both the Build title and missing-value text are `stringKey` mappings. A plain string parameter would display
  `settings.not.provided` literally, so the absent branch deliberately uses a separate typed fallback component.
- The generated Kotlin and test adapter live only in `:feature:settings`'s test source set. #13076 adds one
  optional `buildRow` slot to the production `AboutSettingsPage`; its null default preserves the handwritten row and
  the production route passes no slot. The original Version generated file and generator lock are untouched. There
  is no variant, flag, route, runtime, preference or default choice. To remove the consumer while retaining the
  inactive host, delete this directory, `AboutBuildSnapshot.kt`, `AboutBuildRefractTest.kt`,
  `AboutBuildSemanticsTest.kt`, `AboutBuildHostTest.kt`, the generated Kotlin, and
  `scripts/app/refract_about_build.py` with its tests and CI registration. To restore the pre-#13076 tree, also
  remove `buildRow` and its conditional from `AboutSettingsPage`. No migration is involved in either rollback.

## Exact generator ownership

`toolchain.json` pins public Refract main commit `811db385e4a38a4d5df720c795197368efe3602c` as inspected on
2026-11-22, its complete raw Git-tree and canonical archive-tree hashes, canonical archive SHA-256, schema 1 and
SwiftSyntax 600.0.1 at `0687f71944021d616d34d922343dcef086855920`. This remains an experimental snapshot, not a
supported release or Gate A acceptance. The reviewed generated file's standard header names its Swift source;
the adjacent lock supplies its exact generator version. No executable or generator source is committed.

The initial real generation used an unmodified detached checkout at that commit, built with Swift 6.4.2 and two jobs
under CPUQuota=200%, MemoryHigh=5G, MemoryMax=6G and TasksMax=512. A direct Git build reports
`refract 0.1.0 ($Format:%H$)` because upstream has no `--source-revision` option. It must not be represented as a
source-identified upstream CLI. For offline drift checks, a **test-tooling-only** source archive equivalent changes
only `Sources/RefractCLI/Version.swift`, using upstream's canonical `version_source` substitution to stamp that
same commit; its binary differs from the direct Git binary. This difference is not hidden or presented as an upstream
source revision change. `check --download` creates that canonical archive from the exact public Git tree and verifies
all lock hashes before building. Normal Gradle builds never invoke or download the generator.

```sh
# Read-only, with a source-identified test CLI; two generations must produce identical reviewed Kotlin.
python3 scripts/app/refract_about_build.py check --cli <path-to-canonical-refract>

# Explicitly fetch/build the exact public source in a disposable toolchain, then check drift.
python3 scripts/app/refract_about_build.py check --download

# The only permitted update path; review the generated diff, never hand-patch it.
python3 scripts/app/refract_about_build.py generate --cli <path-to-canonical-refract>
```

The CLI's required second backend is emitted into a temporary directory and discarded. The mapping and JSON resource
fixture are schema inputs, **not** a HarmonyOS component, source implementation, destination or compiler result.
No iOS or HarmonyOS evidence is claimed.

## Actual evidence and limits

`AboutBuildRefractTest` compares generated and handwritten Build rows across all three existing styles, light/dark,
en/zh-Hans/ja, standard/2x text, and present/absent values. It asserts exact pixels, localized fallback semantics,
absence of a leaked resource key and the unchanged handwritten production About row. Captures stay in build output or
`-Dpersona.shots=<dir>`, never in the repository.

#13030 adds a test-only `PackageInfo.longVersionCode` adapter, preserving decimal numeric values including zero.
Android supplies a non-null numeric build field; null coverage applies only to the generated component's optional
input and must not imply a native missing-package-metadata state. `AboutBuildSemanticsTest` checks actual Robolectric
package metadata and generated/native text, descriptions, click/custom actions and `Tokens.MinTouch.android` touch
bounds in en/zh-Hans/ja for present and absent optional inputs. It does not alter the generated source or repeat the
pixel matrix.

#13076 connects the generated component to the existing native `AboutSettingsPage` through its optional `buildRow`
parameter. It defaults to null, preserving the existing handwritten `JRow` and remembered PackageInfo lookup; the
production route never passes it. `AboutBuildHostTest` supplies `buildNumber` from the real PackageInfo →
`AboutBuildSnapshot` adapter directly to the generated slot. Its handwritten comparison value differs from that
real snapshot, and the test checks handwritten default → generated → handwritten default under the same remembered
PackageInfo snapshot. It retains one Build row, the native Version and the full-value Commit copy action. This is
component-hosting/rollback evidence, not dynamic package-metadata refresh, production selection or a copied page.
The checker pins this default and strips Kotlin comments and string contents before rejecting code references to
`AboutBuildRow` / `AboutBuildSnapshot` under production source sets, including `src/main`, `src/debug` and
`src/release`; `src/test` and `src/androidTest` remain allowed. Mentions in comments and string literals remain
allowed; references inside string interpolation remain code and are rejected. Fixtures cover real production
references, allowed test-source references and prose. The two rollback choices above distinguish retaining the
inactive host from restoring the pre-#13076 host; no persisted state or migration is involved.

#13061 adds isolated temporary-file rollback checks for the Build generation entrypoint. Nondeterministic successive
outputs restore the original reviewed bytes and return nonzero. If restoration itself fails, the original generator
diagnostic remains first and the restoration error is explicitly secondary; no successful rollback is claimed.
These tests do not regenerate or edit the reviewed Kotlin.

These are Native JVM Compose comparisons, not a physical device,
TalkBack, accessibility-service or performance result. This local generation, Kotlin compilation and JVM comparison
does not complete any part of Gates A–D, their required visual/device acceptance, or the separately unauthorized
HarmonyOS destination. It must not be cited as a Gate B/C pass or a readiness decision.
