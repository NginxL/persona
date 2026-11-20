# To-do push fixtures

What a decision's push carries (#10465; `docs/design/proposals/todos-redesign.md` 3.7 "Outside the app" and 5
decision 4 (a)): the `todo` a manager sends with a `decide` push (`assistantwire.NotifyTodo`, `notify.request` on the
bus) → the push's category, whether it sets `aps.mutable-content`, and its top-level `todo` (`TodoDecidePush` in
`schema/wire/todo-view.schema.json`), which the WebSocket `notify` carries too. The hub answers every case exactly
(`TestDecisionPushFixtures`, `server/internal/hub/push_decide_test.go`); `scripts/test/test_wire_schema.py` holds each
expected `todo` to the schema, and the generated Go, Swift and Kotlin types read it back unchanged
(`server/internal/assistantwire/todo_view_gen_test.go`, `apple/PersonaTests/TodoViewWireTests.swift`,
`android/core/contracts/src/test/kotlin/persona/core/contracts/TodoViewWireTest.kt`). The app's notification service
extension decodes the same `todo` through the generated Swift type once it offers the options (#10465).

| File | What it pins | Cases |
| --- | --- | --- |
| `todo_decide_push.json` | A manager's decision push → its category, mutable-content and `todo` | 6: an older manager's push unchanged, a safe decision with its options, a decision only the app answers, a rule this hub does not know, the options as forwarded, a safe decision without options |

## Format

`{"version": 1, "cases": [...]}`; a case is `{name, description, source, input, expected}`.

- `input.owner`: the manager whose to-do it is (the hub writes it as `todo.manager`).
- `input.todo`: the `todo` of the manager's `decide` notification: `id`, `asked_at`, `recommend`, and as a manager
  sends them `options` (labels), `responses`, `ask` and `lockscreen`.
- `expected.category`: `aps.category`. `expected.mutable_content`: whether `aps.mutable-content` is 1 (else absent).
  `expected.todo`: the payload's `todo`. Compare as JSON values. The alert's texts are the manager's, untouched here.

All data is made up.

## The rules the cases pin

- **An older manager's push is unchanged**: without `lockscreen` the push is today's, byte for byte (`TODO_DECIDE`,
  Take Recommendation; `todo` carries `id`, `asked_at`, `recommend` and `manager` only), though the manager sends its
  options.
- **`lockscreen` decides the category**: `answer` keeps `TODO_DECIDE`; `open_only`, and any value this hub does not
  know (written `open_only`, the safer rule), sends `TODO_DECIDE_OPEN`, a category no app gives actions, so every
  app, older ones too, shows none and a tap opens the app. The manager says `open_only` exactly when answering the
  decision is `open` (`spec/fixtures/todo-view/README.md` "Risk and the lock screen").
- **Options only for `answer`**, with `mutable-content` so the app's notification service extension runs and can offer
  them as actions: whole labels (never clipped), no empty one or repeat, the recommended first, at most 4. With none
  to offer there is no `mutable-content`.
- **`ask`** is the decision's ask id in the to-do's view (`todo:decision@<asked_at>`), which an answer from the
  notification names; it comes with `lockscreen`.
- **Too big to send** (not a case here: it depends on the transport's limit): the hub shortens the alert first, down
  to 500 characters, then drops `todo.options` and `mutable-content` and fits the full alert again, never the
  category, `lockscreen` or `ask` (`docs/assistant/todos.md` "Waiting for the owner").
