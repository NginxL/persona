# A bounded Refract pilot for a native settings surface (#9620)

**Decision: #9694 authorizes one bounded, Android-only consumer experiment, not production integration or rollout.**
The audit below retains the original boundaries and the evidence that remains open. The experiment adds no production
screen selection, build dependency, setting, generated navigation route or default change. Refract's production-readiness
evidence has not been accepted for Persona, and no release or three-platform readiness is claimed.

The audience of the existing surface is anyone checking which Persona build is installed. The audience of this
engineering evaluation is a maintainer deciding whether one implementation can reduce native UI duplication without
adding product complexity. No generator terminology belongs in the existing settings UI.

## Candidate and non-goals

The candidate is the **three-row version-information group inside Android Settings › About and updates**, currently
`AboutSettingsPage` in `android/feature/settings/src/main/kotlin/persona/feature/settings/DeviceSettingsPages.kt`.
Its rows are Version, Build and Commit. The corresponding iOS integration point is the first section of
`SettingsAboutPage` in `apple/Persona/Views/Settings/SettingsNavigation.swift`.

This is deliberately smaller than replacing a whole settings page:

- It has three read-only rows, no network lifecycle, no account scope and no persisted drafts.
- It exercises a typed model, nullable and absent build metadata, localization, long values, native design components
  and accessibility without putting an important workflow at risk.
- It can be called from the existing native page. The surrounding title, back navigation, scrolling, update checker,
  diagnostics and failure recovery stay handwritten.
- The existing `AboutBuildCommitShotsTest` already covers the Android group in en, zh-Hans and ja, all three styles,
  light/dark and present/unknown commit states. Those are Native JVM Compose component captures, not device or
  performance evidence.

Why not the other small surfaces: Appearance, Language and Notifications alter preferences and require rollback of
actual user choices. Diagnostics contains private data and live connection state. Privacy and license notices are
not equivalent on these clients: HarmonyOS opens bundled notices in its native sheet, not an Android-style About
page. Choosing one of them would silently change content or product behavior rather than test generation.

The accepted shared surface is the **Version and Build rows**, with a native Commit row beside them. iOS has no
standalone build-number row: its existing Version value combines the short version and bundle build number. The
mapping preserves that native presentation rather than inventing a third row to make all screenshots identical.
`BuildCommit` remains Android-native; the existing iOS diagnostics Commit stays folded in its native section and is
not moved into the shared surface. The exact component boundary must be checked against the accepted generator's
typed native-component support before implementation.

HarmonyOS currently has no corresponding version-information group. `SettingsPage.ets` has `privacyPage()` and
`licenseNoticesForm()`, and `LicenseNotices.ets` owns the restricted ArkWeb notice reader. Those are not substitutes
for About. A pilot that needs a HarmonyOS host route must first have its own reviewed issue and acceptance evidence
for **where this existing information belongs**, preferably the existing Privacy destination, without adding a
feature or changing visible content. That follow-up is not authorized here; an experimental host must not invent a
production destination merely to make the generator look cross-platform.

### Explicit non-goals

- No new visual style, theme color, setting, feature switch, primary destination or user-facing experiment label.
- No replacement of the complete Android About page, update checks, Dev Mode, New Chat diagnostics, app-update
  actions, pairing, secrets, credentials or private data.
- No change to the existing SwiftUI, Compose or ArkUI production implementations in this audit.
- No web, watch, visionOS, native Mac or desktop backend.
- No translation of business logic, package-manager access, clipboard implementation or update installation.
- No opaque generator binary, runtime interpreter, reflection bridge, global initialization or runtime dependency.
- No generator CLI or generated source in this audit. A later integration may check in deterministic generated source
  only after the ownership contract below is met; a vendored generator remains out of scope.
- No default rollout. Even a successful experiment is not evidence for broader adoption.

## Current native boundaries

### Android

`SettingsScreen.kt` places `AboutSettingsPage()` as one item in the existing `JListPage` for `about`. The same page
owns update checking and its results. The candidate therefore receives no navigator, `Context`, `PackageManager`,
`Resources` or `AppUpdateState`.

The current implementation reads `versionName` and `longVersionCode` through `PackageManager` inside a remembered
value. An experimental adapter must move that lookup off the UI hot path: load the immutable snapshot once before
presenting the experiment, or on a lifecycle dispatcher appropriate to the host. Generated view code must not call
Android framework APIs. The fallback keeps its current behavior; this is not a license to rewrite it during the
audit.

`AboutBuildCommit.kt` owns `BuildCommit.parse`, the resource lookup, the 12-character presentation and the existing
copy action with localized feedback. Keep `BuildCommit.parse` and `AboutBuildCommitRow` handwritten. In particular,
do not turn absent, `unknown` and malformed resource values into a displayed commit. The model carries the parsed
value, never a raw value that generated view code interprets.

The reusable native mapping is:

| Shared meaning | Android host | iOS host |
| --- | --- | --- |
| Group container | `JGroup` / the two shared rows' current `JGroupItem` positions | `SettingsForm`'s existing `Section` |
| Read-only row | `JRow` with title and value/trailing text | `LabeledContent` |
| Version value | the existing `JRow` value | the existing combined short-version/build `LabeledContent` value |
| Build-number value | `PersonaTheme.type.micro`, `ink3`, as today | folded into Version, not a duplicate row |
| Build commit | native `AboutBuildCommitRow`, kept in its current third position | native diagnostics commit view; outside the shared surface |

`AboutSettingsPage` is a group, not a page with its own title. Its generated counterpart must remain a component
inside `JListPage`; it must not generate another `JListPage`, top bar, navigation action or scroll container.

### iOS

`SettingsAboutPage` already reads immutable bundle fields as stored properties and displays the combined short
version and bundle build number in its first `Section`. A future shared source may supply that equivalent row
through a native adapter. `DevModeSection`, the Diagnostics disclosure, the build commit and `NewChatCheck` remain
native. In particular, the `.task` reading `NewChatCheck.defaultsKey` is outside this pilot and does not become
generated state.

The iOS compiler consumes the shared SwiftUI subset directly, without a Refract library or startup hook. Any
required app-facing model adapter lives beside the existing native About implementation; it must not add an
`@Observable`, polling task or broad `AppModel` read to a static row. Keep the current `SettingsForm` and
`.themedGroupedPage()` host.

### HarmonyOS

The eventual host must call an ordinary generated ArkUI `@Component` from an explicitly reviewed native route,
with typed properties and callbacks. It must use the existing native `Tokens`, `Colors`, `ComposeListItemV2` /
`ContentItemV2` or an approved equivalent, localized `tr(...)` resources and a 48 vp minimum response area.
`WithTheme` supplies the current scheme; there is no second color observer or copied style palette.

There is no About model to adapt today. The native host owner must provide immutable version metadata through the
platform's application metadata API, with the load and failure behavior specified before implementation. Do not
replace `LicenseNotices`, modify its Web restrictions or reuse its controller as a build-information model.

## Typed model contract (proposal, not implemented)

The source boundary is a small immutable snapshot, not a general settings model. The exact protocol spelling must
match the accepted Refract subset; the following is the semantic contract, not a claim that the current generator
accepts it.

```swift
struct AboutVersionSnapshot {
    let versionName: String?
    let buildNumber: String?
    let commit: BuildCommit?
}

protocol AboutVersionModel: RefractModel {
    var snapshot: AboutVersionSnapshot { get }
    func copyCommit()
}
```

| Field / action | Required behavior |
| --- | --- |
| `versionName` | Display the existing localized Version label and supplied value; preserve the current localized Not provided fallback. Never read `PackageManager` or `Bundle` in generated code. |
| `buildNumber` | Preserve the numeric Android build representation and the existing micro/code typography. Null is Not provided, not `0` or an empty fabricated value. |
| `commit` | Pass the parsed value to the handwritten native commit component. Keep its 12-character short display, full-value copy and unknown state unchanged. |
| `copyCommit()` | Delegate to the existing Android copy behavior with its localized confirmation and accessibility action. No-op for an absent commit. |

Kotlin and ArkTS adapters implement the generator's exact generated interface once that interface exists. The
proposed `BuildCommit` field means the generator must type this domain value across the boundary; passing an opaque
`Any` is not an alternative. They do not subclass Compose or ArkUI state holders, expose `StateFlow` fields as
generated view properties or pass platform-specific objects through `Any`. If narrow observation cannot express
this immutable snapshot without unnecessary recomposition/re-render, that is a generator readiness failure, not a
reason to introduce a dynamic fake model. Commit formatting and parsing stay in native code; if the accepted subset
cannot type the nullable value object and optional action faithfully, keep the whole commit row native and narrow
the candidate further rather than weakening the boundary.

The source snapshot must record the accepted generator's semantic version and full commit, source lock, supported
subset version and mapping schema version. These values must be supplied when the integration is proposed; they are
intentionally not guessed from an early development checkout.

## Generator and generated-code ownership

Refract's stated architecture is one Swift source compiled as ordinary SwiftUI on iOS and translated at build time
to reviewed Kotlin and ArkTS. Persona adopts that only after the following contract has evidence:

1. **Source of truth:** Persona owns the shared Swift source, model adapter and `refract.json` mapping. The mapping
   resolves only existing Persona components, tokens and localized resources. It cannot create a parallel design
   system or contain secrets, account values or local paths.
2. **Reproducible toolchain:** pin a released generator version, full source commit, source archive checksum and
   Swift/SwiftSyntax lock. Build the CLI from public source in a disposable toolchain. Never commit an opaque
   executable or download one implicitly in a normal application build.
3. **Deterministic output:** the same source, mapping, lock and generator produce byte-identical Kotlin and ArkTS.
   Unsupported constructs fail generation; no silent omission or fallback renderer.
4. **Reviewed ownership:** each generated file names its Swift source, mapping and exact generator version and says
   not to edit it by hand. Persona reviews generated source like other code. CI regenerates it and rejects drift.
   Only the designated regeneration command changes generated files; build output and developer-local generator
   checkouts stay out of source control.
5. **No runtime:** generated output imports only the platform SDK and Persona's own native code. Check dependency,
   startup and package-size evidence for the absence of a Refract runtime, interpreter, bridge and initialization.
6. **Exit:** deleting the opt-in build wiring leaves a fully buildable handwritten app. A later retirement may keep
   the reviewed generated source as an explicitly hand-owned native implementation; this must not silently become
   the production fallback while the original still exists.

Until an immutable release satisfying these points is cited in the implementation PR, the dependency remains
unselected. A local commit of an early generator is not production-readiness evidence.

## Reversible experimental build design (future only)

No flag is added now. A later implementation PR may introduce an **internal build variant only**, with generated
sources in their own source set and a distinct test application identity supplied through existing private build
configuration. The ordinary debug and release variants do not include the generator or generated component.

The variant's About host chooses between two ordinary native calls:

```text
existing Android About item
  = existing hand-written version group (unchanged production default)
  OR generated version group (experimental variant only)
```

The existing handwritten source remains compiled in the experimental variant and directly selectable by a build
comparison switch used by tests. There is no user preference, hub flag, remote rollout, background switch or
production screen selection. The experimental application is separate from production, so rollback is uninstalling
that test app or rebuilding the normal variant; no data migration or persisted rollback state is required.

For iOS, the shared component is included only in a separate internal test configuration while the original
`SettingsAboutPage` remains available for paired captures. HarmonyOS likewise needs an explicit internal test
host/build after its destination is approved. A generated file's existence, a successful `refract generate`, or an
Android-only compile never enables any host route.

### Rollback checks required with the implementation

- Build every ordinary variant with the experiment source set absent; show no dependency, resource or UI diff.
- Select the handwritten component in the experimental host and exercise present/unknown commit, long version,
  three languages, all styles and light/dark. Its captures match the untouched base under the same renderer.
- Switch only the experimental component, then back, without changing navigation, scroll, update state or clipboard.
- Rebuild after removing only the generator wiring and generated source-set registration. The old screen still
  compiles and its original tests pass.
- Verify that uninstalling the experimental application does not modify the production application's preferences,
  pairing or diagnostics.

## Readiness and platform evidence gates

The gates are ordered. **A later phase cannot substitute its evidence for an earlier one.** All required evidence
belongs in the implementation PR; this audit supplies none of the missing native evidence.

### Gate A: accepted Refract readiness

A maintainer must explicitly accept, on the integration issue, the immutable generator version and its evidence for:

- the Phase 1 vertical slice in Refract's roadmap: iOS/Android/HarmonyOS compilation, paired native screenshots,
  hand-written-reference performance comparisons and replacing/restoring a handwritten example screen;
- the supported typed model/null/optional action subset and native-component calls used above;
- deterministic regenerate-and-diff, useful diagnostics for unsupported constructs and exact source/version headers;
- native design-system mapping and resource localization, without a runtime or opaque binary.

At the audit's inspected source snapshot, Refract described itself as early development. Its roadmap's Phase 1 exit
requires all three platform builds, paired screenshots, no-regression benchmarks and reversible adoption. The
inspected documentation alone does not establish those results for Persona. **Gate A is open, so no experimental
production integration is permitted.**

### Gate B: host and generated-source compilation

These are required after the implementation exists, on the same exact source and mapping:

- Android: compile the experimental host and the generated Kotlin in `:feature:settings`, compile the ordinary app,
  and run `scripts/ci/android.sh` for affected checks. Run existing `AboutBuildCommitTest` and
  `AboutBuildCommitShotsTest`, plus new adapter/fallback tests. Report the actual commands and results.
- iOS: run `scripts/ci/apple.sh` and the required project generation/build with the minimum supported Xcode 26.0
  toolchain. Run relevant settings, localization, token and performance tests. Linux Swift parsing is not an iOS
  build; absence of macOS is recorded as **not verified**, never as a pass.
- HarmonyOS: build the actual application and `entry@ohosTest` with API 26 and the locked native prerequisites as
  documented in `harmony/README.md`, using `devecocli build --build-mode release`. Run portable behavior tests and
  the native SDK test target. A TypeScript parser, portable model test or a generated ArkTS text fixture is not HAP
  compilation.
- Inspect the generated imports and packaged dependency graph. No runtime library, runtime code generation or
  generator invocation on a user's device.

### Gate C: actual visual and interaction evidence

Attach actual light/dark, handwritten/generated comparisons at the same fixture, OS and build settings. Keep images
in the PR, not in the repository. A person reviews the evidence when a new host destination or other large UI change
is proposed (`docs/design/DESIGN_SYSTEM.md`, section 1.4).

| Platform | Minimum matrix | Interaction evidence |
| --- | --- | --- |
| Android | Glass, Brief and Board; light/dark; en/zh-Hans/ja; compact and regular widths; standard and largest supported text; Standard/Compact density | Present/unknown commit; long version and 64-character full commit; TalkBack labels; copy the full value; localized confirmation; 48 dp touch area; update checker and Back unchanged |
| iOS | Current default style plus every style the shared component claims to support; light/dark; en/zh-Hans/ja; phone and regular width; xxxLarge interface text | Version and native diagnostics remain distinct; VoiceOver reads labels/values; commit copy/select behavior remains native; 44 pt touch area; New Chat diagnostics and update/Dev Mode behavior unchanged |
| HarmonyOS | System and every explicitly supported existing style; light/dark; en/zh-Hans/ja; straight phone, conventional foldable and wide foldable with actual window classes; default/2x/3.2x text | Native route/back; long values; screen-reader labels; 48 vp targets; sheet/navigation unchanged; approved About metadata reads and its unavailable state |

Robolectric Native JVM Compose captures are useful component evidence and must be labelled as such. They do not
prove system bars, navigation, Web behavior, glass rendering, device accessibility or performance. They cannot
satisfy the device part of this gate. A simulator image is labelled as simulator evidence; a prototype is not a
production HAP.

### Gate D: performance and packaging

Measure handwritten against generated on the same device/simulator, optimized build, synthetic fixture and run
count. Include median and range, at least three runs per side, and identify every device and build. Do not claim a
performance improvement from static inspection.

- iOS follows P1–P12: no framework/IO work in `body` or on the main actor, no localization/parse/formatter work in
  `body`, narrow reads, cheap state initialization, no timers for this static group and no work before first frame.
  Page-open blocks stay below 100 ms, interaction work stays within the 16 ms frame budget, scroll hitch is below
  5 ms/s where scrolling is measured, and ten open/close cycles finish within 5 MB of the starting memory. Report
  `MainThreadWatchdog`, redraw and allocation results appropriate to the actual route. A performance claim requires
  optimized-build evidence (P12).
- Android measures open/render, recomposition count, long-value layout, scrolling and memory for both components,
  including Glass and enlarged text. Keep one stable identity per row, no collection derivation in composable view
  bodies and no framework reads in composition. Compare release/package contents and startup; no new runtime or
  launch work.
- HarmonyOS follows the physical procedure in `docs/harmony/device-validation.md`: record device/API/build/hash,
  refresh rate and profiler, use at least 30 measured samples after warm-up and report distribution/p95. At 60 Hz,
  p95 is at most 16.7 ms with fewer than 5% of frames over two frame intervals; at 120 Hz use 8.3 ms. Keep network
  readiness separate. Do not extrapolate these physical targets from an emulator.

No gate is met by this document. Only after A–D pass may a maintainer consider enabling the internal experimental
variant. Default rollout is a separate product and engineering decision, with fresh evidence and an unchanged
handwritten rollback available. Failure of any gate stops the pilot; it does not justify weakening a baseline or
claiming partial native evidence as readiness.

## #9694: one generated Android Version row

The audit's candidate is narrowed to **one read-only Version row**, rather than its proposed Version and Build pair.
`AboutVersionRow.swift` and `refract.json` under `android/refract/about/` map directly to the existing `JRow` and
localized resources. `AboutVersionSnapshot.kt` supplies a typed immutable app-owned model. Build and Commit, including
parsing, short-hash display and full-value copy, stay native. `AboutSettingsPage` and its production route are unchanged.

The generated Kotlin and its model adapter are compiled only in `:feature:settings`'s test source set, never in an
ordinary debug/release source set, and exercised only by the comparison test; no application route chooses them. `AboutVersionRefractTest` compares it with the unchanged native row in all three Android styles,
light/dark, en/zh-Hans/ja and standard/2x text. These captures are Native JVM component evidence, not device evidence.
`scripts/app/refract_about_version.py` pins a complete public source commit/archive checksum and the SwiftSyntax lock;
it is the only regeneration command, with a read-only drift check in the Android CI lane. No generator runtime or
opaque binary enters an app. The CLI's second-backend output is temporary schema validation, not a HarmonyOS
implementation or destination. `android/refract/about/README.md` records the exact invocation and rollback.

This is a consumer experiment, not the audit's production integration: the general Gates A–D and the other open
readiness decisions below remain open. An Android-only compile and paired Native JVM captures do not satisfy the
three-platform build, device or performance gates.

## Audit verification and limits

The implementation proposal is based on the actual native boundaries above and the design, accessibility and Apple
performance rules. The only code verification in this PR is of the **unchanged** Android host and its existing About
tests, to establish a rollback baseline. No generated source is compiled and no visual or performance result is
claimed for Refract. Current macOS/iOS and HarmonyOS toolchains are unavailable in this environment; Node.js is too
old for the documented portable HarmonyOS tests, so those are not represented as passing either.

The acceptance checklist for #9620 therefore remains open. Closing the audit does not authorize a rollout or imply
that its host-compilation and cross-platform evidence items have been completed.

## Follow-ups, separately owned

1. Refract supplies immutable production-readiness evidence for Gate A.
2. A Persona/HarmonyOS owner proposes and reviews a corresponding version-information destination and native model;
   until then there is no HarmonyOS integration to build.
3. Only after those decisions, an implementation issue claims the narrow adapter, internal experimental build and
   full A–D evidence. It must not widen into a full-page replacement or a default rollout.
