# Native extension UI fixture

Use this fixture only with a disposable local Hub and synthetic data. It contains no external service calls, credentials or file writes; its counters and notes live in the extension process. It uses the repository's TypeScript SDK rather than a copied SDK.

From this directory, build the runtime binary for the test Hub's platform. For an ARM64 Mac with Bun installed:

```sh
bun build --compile app.ts --outfile bin/harmony-native-check-darwin-arm64
```

Use the equivalent manifest command path for another platform. Install this directory through `personad extension install` while pointing `PERSONA_HOME` at the owned test Hub. Pair the native HarmonyOS app with that Hub, then open the native root navigation menu → **Apps → Native UI Check** (or **Native Stream Check**). Its installation details remain under **Settings → Extensions**. Do not install it on a normal personal Hub.

The legacy **Native UI Check** page exercises a Slider, Switch, note field, confirmed submit, unkeyed row, code, table, negative chart values and two independent diff files. Keep the note field focused and change Level. The count, button caption, unkeyed value, code, table and chart must update without replacing the field or losing its draft. Apply asks once and saves the current note. Diff context should open the requested range first, keep each file independent and expose adjacent bounded pages.

**Native Stream Check** rejects a snapshot-only request: it can open only through the native stream transport. Type a draft, press Advance, and verify that the count and button caption change while the same field retains focus. Save note asks for confirmation; its stream result must show the exact saved note. The client's consent completes only the matching Hub permission gate, without generic Agent approval. Cancel must not change the count. A cold application restart must reopen the page without requiring a manual retry after capabilities arrive.

Keep the main HAP build and test HAP build separate, and preserve the artifact hash used for screenshots. Portable tests cover protocol and ownership logic; these steps verify actual ArkUI observation, native focus and host permission delivery.
