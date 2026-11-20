# Hub wire schemas

JSON Schemas for the wire shapes of domains the shared fixtures (`spec/fixtures/`) already pin (#7891 stage 5, #9117).
Each `<domain>.schema.json` generates the clients' types with `scripts/gen/extension_schema.py`, the same generator and
subset as `schema/extension/README.md`; the file's `x-out` names the generated TypeScript (`ts`), Swift (`swift`), Go
(`go`) and Kotlin (`kotlin`) files it wants. Swift and Kotlin types carry no prefix. Go and Kotlin take the package of
the directory they are written to (the generator checks that the files already there say the same); a Go file gets no
package doc and names its registry of definitions after the schema (`todoViewDefs`), since it shares its package with
hand-written code. Android and Go are generated only for a schema whose `x-out` names them.

```sh
python3 scripts/gen/extension_schema.py           # after changing a schema
python3 scripts/gen/extension_schema.py --check   # compare only (also part of scripts/ci/guards.sh)
python3 scripts/test/test_wire_schema.py          # --check, fixture samples hold to the schema, no hand-written copy
```

| File | Domain | Generated |
| --- | --- | --- |
| `recents.schema.json` | `RecentSetup`, New Agent's recent setups (`spec/fixtures/recents`) | `web/src/lib/recents-wire.ts`, `apple/Packages/PersonaWire/Sources/PersonaWire/RecentsWire.swift` |
| `todo-view.schema.json` | To-dos (#10226): a row's view (`TodoSummary`) and its asks (`OwnerAsk`), the list's counts (`TodoCounts`) and places (`TodoPlaces`) (`spec/fixtures/todo-view`, `spec/fixtures/todo-counts`); a to-do's conversation in rounds (`TodoTimeline`), its results (`TodoResult`) and what changed since the owner looked (`TodoRecap`) (#10465, `spec/fixtures/todo-rounds`), and what `GET /api/v1/todos/{id}` carries of them beside the row (`TodoItemDetail`); a decision push's `todo` (`TodoDecidePush`, #10465, `spec/fixtures/todo-push`) | `web/src/lib/todo-view-wire.ts`, `apple/Packages/PersonaWire/Sources/PersonaWire/TodoViewWire.swift`, `server/internal/assistantwire/todo_view_gen.go`, `android/core/contracts/src/main/kotlin/persona/core/contracts/generated/TodoViewWire.kt` |

Every sample a fixture holds for a definition must name exactly the schema's fields, at any depth, and every definition
must be reached by some sample (`test_wire_schema.py`). The clients' tests write each sample back through the generated
type and compare: Apple (`apple/PersonaTests/RecentSetupsTests.swift`, `apple/PersonaTests/TodoViewWireTests.swift`) the
bytes, Go (`server/internal/assistantwire/todo_view_gen_test.go`) and Kotlin
(`android/core/contracts/src/test/kotlin/persona/core/contracts/TodoViewWireTest.kt`) the JSON values; Go also holds
what it writes to the schema with `server/internal/jsonsubset`, as the tests of the code that builds these values
should, since a nil slice or map writes `null`. A sample writes an optional boolean only when it is true and leaves an
empty list or map out, as Go's `omitempty` does, and a time as Go writes it.

Hand-written copies of a generated type may not return: the test fails when a tracked file declares one of a schema's
names outside its generated files, in every language its `x-out` names and in those its `x-guard` adds (`arkts`,
`rust`: languages a copy could be written in before they are generated, or never are). `x-reserved` lists names a later
stage will define, guarded from now on. The rule is per schema, so `recents.schema.json` guards TypeScript and Swift
only (its Kotlin and Rust copies predate it). A decoder that reads the JSON key by key declares no type (HarmonyOS's
`Wire.text(obj, 'key')`), so `x-guard-keys` lists keys and values distinctive enough that the test also fails on a
quoted one in a Swift, Kotlin or ArkTS source the schema guards, outside its generated files and the tests. A key goes
there only while no client source quotes it for another reason; TypeScript and Go are not scanned for keys, since plain
code there compares the generated types' values.
