# permission-modes

The standalone name every client gives each permission mode (#10363), using the wording settled in #5378. Keys are
the mode IDs `default`, `acceptEdits`, `plan`, `auto` and `bypassPermissions`; every entry has exactly `en`, `zh-Hans`
and `ja`. These are names, not descriptions, approval-button actions, launcher-default choices, raw-ID handoff formats
or unset/default-behavior labels.

Clients keep their normal localization resources; they do not load this JSON at runtime. A check for each client reads
this table and its resources so the names cannot drift:

- Web: `web/src/i18n/permission-mode-names.test.ts` checks the picker, answered-plan and planned-task labels, plus
  Full access confirmations.
- Apple: `scripts/test/test_apple_permission_mode_names.py` resolves the picker, answered-plan and planned-task keys
  from their call sites, then checks `Localizable.xcstrings` and Full access confirmations. Permission labels must not
  repurpose the generic `Auto` key shared with thinking effort and other features, or an approval-button action key.
- Android: `android/feature/chat/src/test/kotlin/persona/feature/chat/PermissionModeNamesTest.kt` checks the chat,
  New Agent, assistant, routine, answered-plan and planned-task resources, plus Full access confirmations. An unset
  routine keeps its own label, separate from the actual `default` mode.
- HarmonyOS: `harmony/tests/permission-mode-names.test.mjs` checks the permission-specific labels used by session
  settings and planned tasks. Generic IDs remain available to effort options.

Add a mode here first, then register every standalone localized alias in each client check. Update the table and all
clients together. Run the four checks above; the web, Apple and HarmonyOS commands are direct invocations of those
files, and Android runs `:feature:chat:test` through `android/gradlew`.
