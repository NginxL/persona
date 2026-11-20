# To-do write outcome fixtures

How a to-do's write that carries the owner's words ends (#10465; `docs/protocol/protocol.md` "Message feedback
(#5258)", its "To-do writes"): a message on `POST /api/v1/assistant/conversations/{id}/route {todo_id, text}` or an
answer on `POST /api/v1/todos/{id}/asks/{ask}/answer`. Given what happens on its way to the to-do's Agent, the manager's
answer (its status, `code` and `delivered`) and what the hub makes of that answer under an `X-Persona-Op-Id`: the final
`todo_send_state` and the operation's receipt. The manager replays every case against its routes
(`server/internal/assistant/todo_write_outcome_test.go`); the hub relays each answer to an app that sent the write with
an op id and checks the final state and the receipt (`server/internal/hub/todo_send_state_test.go`). The two packages
never import each other (the hub's architecture guard), so this file is the seam between them.

| File | What it pins | Cases |
| --- | --- | --- |
| `outcomes.json` | What happens to the words → the manager's status, code and `delivered` → the hub's final state and receipt | 24: taken, refused before anything went out, refused by the node, never sent, in doubt (no answer, the node not sure, a new Agent's start), typed and then not recorded (by the Agent that took them, or by a new one that started with them) or refused once the node had them, and a 200 for words recorded for the supervisor |

## Format

`{"version": 1, "cases": [...]}`. A case is `{name, description, source, input, expected}`; `source` lists the code
that decides it (`<repository path> <function>`), the descriptions are English, and all data is made up (the
prototype's trip to Kyoto, `docs/design/prototypes/todos/data/todos.js`).

- `input.route`: `message` (the conversation route, from the conversation that opened the to-do) or `answer` (the asks
  route, on the decision that waits: "Which ryokan?", Ryokan Hanami recommended, as in `spec/fixtures/todo-asks`).
- `input.answer`: an answer's body, a `TodoAskAnswer`.
- `input.what`: what happens, one of the scenes below.
- `expected.status`: the manager's HTTP status.
- `expected.code` (optional): its error body's `code`.
- `expected.delivered` (optional): its body's `delivered`; absent when the body has none.
- `expected.state`: the final `todo_send_state` the hub sends for it (`delivered`, `failed` or `unknown`).
- `expected.receipt`: the state the hub records for the op id (`done`, `failed` or `unknown`).

## Scenes

| `what` | What happens |
| --- | --- |
| `taken` | The to-do's Agent takes the words |
| `no_agent` | No Agent can take them (the to-do's session is gone) |
| `not_this_conversation` | The message names a to-do of another conversation |
| `refused` | The Agent's computer refuses them with a code of its own (`input_draft`), typing nothing |
| `not_sent` | The send never leaves the manager (`rpc.ErrNotSent`) |
| `in_doubt` | The node's answer to the send never comes back (the wait runs out) |
| `node_not_sure` | The node says typing stopped part way (`delivered: "unknown"`) |
| `start_in_doubt` | Every Agent of the to-do has stopped, and the start of the new one the message starts never answers |
| `start_refused` | Every Agent of the to-do has stopped, and the new one's start is refused before it runs |
| `start_then_not_kept` | Every Agent of the to-do has stopped, a new one starts with the words as its first, then the conversation's routed item cannot be written |
| `typed_then_not_kept` | The Agent takes the words, then the conversation's routed item cannot be written |
| `typed_then_deleted` | The node takes the words, then the Agent's conversation is marked deleted before the manager answers |
| `typed_then_gone` | The Agent takes the words, then the to-do is gone before the decision they answer is recorded |
| `round_over` | The answer names a round of the decision that no longer waits |
| `no_such_todo` | The answer names a to-do that is not there |

## Rules

- **Failed only when nothing was typed.** A refusal before anything went out, a node's refusal that typed nothing and
  a send that never left say `delivered: "none"`: the hub's final state and receipt are `failed`, and a phone may send
  the words again under a new op id.
- **In doubt is never failed.** Words that may be with the Agent (`delivered: "unknown"`: the send's or the new Agent's
  start's answer never came, or the node is not sure) or are (`delivered: "yes"`: what followed failed) are
  `send_in_doubt`, never retryable, in words that say to look at the to-do before sending again: `unknown` on the hub,
  and a phone checks the op id instead of sending again (and shows `yes` as delivered, with no run named). The
  conversation route answers them 409, as its other refusals; the asks route 502, since apps read its 409s as
  "answered elsewhere".
- **Never a 2xx in doubt.** A 200 is delivered on the hub, `delivered: "none"` included: the words were recorded and
  handed to the supervisor, and answering again would only be stale.
- **An older manager** answers 409 `conflict` or 502 `accept_not_taken` with no `delivered`; the hub reads those as
  `unknown` (`server/internal/hub/todo_receipts.go`, `todoSendOutcome`), the cautious reading these cases replace.
