# To-do ask answer fixtures

What an answer to one of a to-do's own asks does (#10465; `docs/design/proposals/todos-redesign.md` 3.7 and 7.3
"Asks"): given what waits on the to-do, the ask's id as its view lists it and the answer
(`POST /api/v1/todos/{id}/asks/{ask}/answer`, `TodoAskAnswer` in `schema/wire/todo-view.schema.json`), the status, the
error code, the answer (`TodoAskAnswered`) and what changed. The manager replays every case against its route
(`server/internal/assistant/todo_asks_routes_test.go`), `scripts/test/test_wire_schema.py` holds every answer and reply
to the schema, and the generated Go types read each back unchanged (`server/internal/assistantwire/todo_view_gen_test.go`).
Clients answer through the route only while the hub lists `todo_asks` (`docs/protocol/protocol.md`, "Health check and
feature flags"). The operation's id is the `X-Persona-Op-Id` header, never a body field.

| File | What it pins | Cases |
| --- | --- | --- |
| `answers.json` | What waits, an ask id and an answer → status, code, reply and its effect | 28: every answer each kind of ask takes (an option, the recommendation, a fixed answer, words, Later, Don't handle and its Undo, a retry, an acknowledgement), the answers each refuses, a round that no longer asks, a step's ask, a run's ask, an id of no known form |

## Format

`{"version": 1, "cases": [...]}`. A case is `{name, description, source, input, expected}`; the descriptions are
English. All data is made up (the prototype's trip to Kyoto, `docs/design/prototypes/todos/data/todos.js`).

- `input.state`: what waits on the to-do whose row the path names, one of the states below.
- `input.ask`: the ask's id, as the row's `view.asks` lists it (`spec/fixtures/todo-view/README.md` "asks"), sent as one
  path segment with its `/` encoded.
- `input.answer`: the body, a `TodoAskAnswer`.
- `expected.status`: the HTTP status.
- `expected.code`: the error body's `code`, for a refusal.
- `expected.answered`: the reply of a 200, a `TodoAskAnswered` without its `todo` (the row the replay reads anyway). A
  `receipt` here says only that one comes back; its value is the hub's.
- `expected.row` (optional): true when the refusal carries the row as it is now under `todo` (409 `ask_stale`), so a
  client redraws the ask without another read.
- `expected.sent` (optional): whether the to-do's Agent was told something.
- `expected.started` (optional): whether an Agent start went out for the to-do.
- `expected.waits` (optional): whether the row's `view.asks` still lists the ask that waits in the state (the one
  whose id has the round `2026-11-10T09:00:00Z`) after the answer.

The replay puts the hub's own values in place of these made-up ones, wherever they appear: the round
`2026-11-10T09:00:00Z` (the time in the id of the ask that waits now: a decision's `asked_at`, a failed start's
`done_at`), the step id `todo_step` and the receipt `dismiss_receipt`. Any other value is taken as written: an earlier
round (`2026-11-09T09:00:00Z`) is a screen that is behind, and `todo_other` is no step of the to-do.

## States

| State | What waits |
| --- | --- |
| `decision` | A to-do a conversation opened, supervised and reviewed `needs_user`: the decision "Which ryokan?" (`why_owner` `priority`, so it can be undone), options Ryokan Hanami (recommended), Kaede and Tsuki; its Agent is live |
| `decision_no_agent` | The same decision, and no Agent can receive an answer (its session is gone) |
| `decision_no_origin` | The same decision on a to-do no conversation opened, supervised by the main conversation |
| `decision_no_origin_no_agent` | The same decision on a to-do no conversation opened, and no Agent can receive an answer |
| `decision_declined` | The decision after Don't handle; `dismiss_receipt` is the decline's receipt |
| `cannot` | A conclusion that says the work cannot be done, which the supervisor did not pass itself, waiting for the owner to see it (`kind` `decision` with no options) |
| `failed_start` | A to-do a conversation opened whose Agent failed to start (`todo:retry@<done_at>`) |
| `step_decision` | A to-do with one step (`todo_step`, its `part_of` the to-do) on which the `decision` waits: the row lists `step:todo_step/todo:decision@…` |

## Rules

- **Every own ask, one route.** `todo:decision@<round>` takes `option` (a label the ask lists; the recommended one is
  the plain OK), `response` (`decide_yourself`, `not_what_i_asked`, `explain_first`), `text` (the owner's words, sent to
  the Agent as they are), `outcome` `later`, `decline` or `undo` with `receipt`; a conclusion that says the work cannot
  be done takes no answer (acknowledged), `later` and, while a review waits on the owner, `decline`.
  `todo:retry@<done_at>` takes no answer (started again) and `later`. `step:<step>/<id>` is the step's own ask `<id>`,
  answered on the step. Words are at most 4000 characters (400 beyond).
- **Answered is not always gone.** `explain_first` answers `answered` while the ask still waits for the supervisor's
  explanation; the reply's `todo` says what waits, so a client draws that rather than dropping the ask.
- **Answered elsewhere first.** The id names the round; once it is over (taken, answered on another device, a newer
  review, a retry started or on its way) the answer is 409 `ask_stale` and takes nothing, with the row as it is now.
- **Nothing a client cannot undo by mistake.** A decision has no one action, so an empty answer is 400 rather than its
  recommendation; one answer per call (400 otherwise); a receipt goes only with `undo`.
- **Not delivered is still answered.** Words or an option no Agent can receive are recorded and handed to the to-do's
  supervisor, answering `delivered: "none"`; any other failure to send that typed nothing takes nothing (502
  `accept_not_taken`, 423 when a node refused with its own code, both `delivered: "none"`), as on the accept route. A
  send that may have reached the Agent, or did before the decision could be recorded, is 502 `send_in_doubt` instead
  (`spec/fixtures/todo-writes`).
- **A run's ask is its session's**: 409 `answer_in_session`; its card answers it (`POST /api/v1/interactions/{id}/resolve`).
- **An id of no known form** (or naming a to-do that is no step of this one) is 404 `ask_not_found`.

Not here, since one call cannot show them: two answers at once make one effect, and the first wins
(`TestTodoAskAnswersOnce`, `TestTodoAskRetriesOnce`); a decline's receipt undoes it through the route
(`TestTodoAskDeclineThenUndo`); the same `X-Persona-Op-Id` gets the first reply back, and one cut off on the way is
kept as in doubt (the hub's receipt, `server/internal/hub/todo_receipts_test.go`).
