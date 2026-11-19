# opencode

[opencode](https://opencode.ai) sessions show up in **Agents**: opencode in a herdr pane is a herdr session (state, terminal, keys and tabs all belong to herdr).
With Persona's opencode plugin installed, it also has chat content (text, thinking, tool calls and results, images), streaming output, sending messages from the phone, interrupt, rename,
and approving opencode's permission requests on the phone. Without the plugin it is still listed, terminal only.

Supported since 2026-11-05 (issue #3070, related #1178). opencode is the third Agent inside herdr integrated per the runtime spec (`docs/integrations/runtimes.md`; before it came pi: `docs/integrations/pi.md`):
`integrations/opencode/` is an opencode plugin built on `integrations/runtime-sdk/`; the node side is `server/internal/node/opencode.go`.

| Where opencode runs | What it is in Persona | Who handles what |
| --- | --- | --- |
| **In a herdr pane** (opencode's TUI, connected to the shared `opencode serve`) | A herdr session, same as Claude in herdr | Pane, space, tab, state, terminal view, keys: herdr; chat content, sending, interrupt, rename, cards, streaming: the Persona plugin in the server process |
| Not in herdr (web, `opencode run`, another terminal connected to the same server) | A session of runtime `opencode-<pid>` (`source_kind: "runtime"`) | The plugin; no terminal view |
| In a herdr pane, plugin not installed | A herdr session, terminal only | The chat is a single explanatory note, sending is rejected (`opencode_no_extension`), interrupt presses Esc |

## Structure

```
 App ── hub ── node ── ~/.persona/runtime.sock ◄── Persona plugin (one connection per opencode server process, runtime `opencode-<pid>`)
                │                                      · list, chat: read-only opencode.db
                │                                      · live: the server process's event stream
                │                                      · control: session.prompt / interrupt / update, permission.reply in the plugin context
                └─ herdr ── opencode TUI in the pane (herdr's own opencode plugin reports state, session id, title)
```

- **One connection per server process**: opencode attaches every TUI to the same `opencode serve --service`. The plugin runs in that process and **is loaded once per project directory (location)**
  (only when the first request for that directory arrives). So one process holds many plugin instances, each carrying only its own directory's context. The plugin keeps a host in `globalThis`,
  and each directory's instance registers its context there. The event stream is server-wide (every instance receives the same events), so only one of them (the primary) reads it; when it goes away the next one takes over, and events are deduplicated by id.
- **How a herdr row matches a plugin row**: herdr's opencode plugin reports `agent_session` in the pane (that is opencode's session id, `ses_…`), with the title `OC | <title>`.
  node merges the herdr row and the plugin's row into one by session id (`mergeOpencodeRow`): title (the plugin's is complete, herdr's gets truncated), last line, model, context usage and revision come from the plugin;
  state comes from herdr (when the plugin says `blocked`, it also counts as "Needs you"). Panes for which herdr reports no session id (older integration; the plugin in the server process does not know about panes) do not disappear:
  they are listed as terminal-only rows `opencode-<pane>` (same as "Terminal rows for other agents" below).
  This row belongs to herdr (empty `source_kind`, has a terminal); the plugin's row no longer appears separately. Calls are routed by type: `session.keys` / `screen` / terminal / tabs go to herdr,
  everything else (content, sending, interrupt, rename, cards) goes to the plugin.
- **Title**: herdr's `OC | ` prefix is stripped; before a title is generated, opencode's terminal title is just `OpenCode`, which is not treated as a title.
- **Notifications**: state comes from herdr; deciding "Needs you" / "done" and pushing use the hub's existing logic (driven by session state changes, same as Claude's herdr sessions), nothing separate for opencode.

## Terminal rows for other agents

node used to drop every agent herdr reported that it did not recognize (`gemini`, `amp`…) (`if a.Agent != "claude" { continue }`), so the pane was invisible on the phone.
Now, when herdr reports an agent but Persona has no way to read its chat, it is **listed as a terminal-only row** (`otherAgent`): pane, space, tab, state and title are herdr's,
the session id is `<agent>-<pane>` (the agent's own session id is of no use; this row is the pane), the chat is a single note "Persona can't read <agent>'s chat yet", sending is rejected (`terminal_only`; no blind typing into an unknown TUI),
and the terminal view and keys work as usual (herdr's). Reason: whatever agent the user opened in herdr, the phone should show whether it is waiting for you, not let it quietly disappear; its state is reported by herdr anyway, so the cost is just one more row.
Panes for which herdr reports no agent (`agent: ""`) and botmux panes are still not listed. This row cannot be closed (there is no session to resume; `session.retire` refuses).

## Where the chat comes from

opencode's plugin API provides the session's context (what remains after compaction) and the event stream, **but no session list and no full message read**. opencode's own database `opencode.db`
(SQLite, WAL) has everything: `session_v2` (sessions: directory, title, model, `parent_id`, `revert`…) and `session_message` (messages: `type`, `seq` from the event log, `data` JSON). So:

- The list and the chat **only read** this database (`file:…?mode=ro`, `PRAGMA query_only`, a 300 ms busy timeout, every query uses the `(session_id, seq)` index): `bun:sqlite` inside opencode's own process,
  `node:sqlite` in tests. Never writes, never holds it for long. Location is `$XDG_DATA_HOME/opencode/opencode.db` (default `~/.local/share/opencode`); `PERSONA_OPENCODE_DB` can point elsewhere.
  If the database cannot be opened, the list is empty and pages are empty; sending / interrupt / rename / cards still work (they go through the plugin API).
  Pages for sessions not in the database return `not_found`: node asks each runtime in turn about sessions nobody has listed (`session.items {limit: 1}`) and assigns the session to whichever answers, so returning an empty page would make DSH and pi sessions look like opencode's.
- Live events (a turn starting / ending, text, permissions) come from the plugin's event stream (`ctx.event.subscribe`): `session.execution.started/succeeded/failed/interrupted`,
  `session.inbox.enqueued`, `session.text.delta`, `session.step.ended`, `session.tool.success`, `permission.asked/replied`, `form.created/replied`, `session.renamed/deleted`.
- Control goes through the plugin context: `ctx.session.prompt({sessionID, text, files})`, `interrupt({sessionID})`, `update({sessionID, title})`, `ctx.permission.reply({sessionID, requestID, decision})`.
  These functions take a single object that combines path parameters and the request body (same names as the HTTP API). The HTTP API needs a password; the plugin does not touch it.

### seq and "a message still being written"

`session_message.seq` is the sequence number in opencode's event log: unique within a session, only increasing, **with gaps** (the log has other events too), so it is used directly as the item's seq.

- An assistant message is **rewritten in place** during its step (tool calls complete one by one) and its seq does not change, while the SDK's `append` no longer pushes batches with `toSeq <= already pushed`.
  So **only finalized messages are pushed**: a message counts once `time.completed` is set (or it has `finish`); in-between state is covered by streaming text and "working". Only the finalized prefix counts as the session's content: `to_seq` and pagination both stop before the first unfinalized message.
  An interrupted step (no `completed`) counts as finalized 1.5 seconds after the turn ends (`SETTLE_MS`); so does one the server process shows as running but has not updated for 60 seconds (a missed event).
  Cost: tool calls in a step only appear in the chat once the step ends (while a long command runs, the phone shows "Working…" and the streaming text).
- An item's id is `<seq>#<part index×2>` and a tool result's is `<seq>#<part index×2+1>`, so when a result completes, the ids of later parts in the same message do not shift.
- The first time a session is seen (its first event), the current end is recorded as a baseline, and afterwards only messages finalized after the baseline are pushed. Items are only generated for sessions the phone is viewing; when nobody is viewing, only the revision in the list is updated.
- `history` = `opencode:<session id>`; when the user reverts in opencode (`revert` non-empty), its hash is appended and the app refetches the whole page.

### Mapping

`integrations/opencode/lib/map.js`, `session_message` → Persona item:

| opencode | Persona |
| --- | --- |
| `user`: `text`, `files` | user `text`; images are placeholders (`lazy`, data via `session.image`), other files become one line `［附件 名字］` ("[attachment name]") |
| `assistant`'s `reasoning` / `text` / `tool` | `thinking` / `text` / `tool_use` (+ `tool_result` once completed or failed, with `tool_use_id`, `is_error`) |
| Tool names and arguments (drawn the Claude way) | `shell`→`Bash` (`command`, `description`), `read`→`Read` (`path`→`file_path`), `write`→`Write`, `edit`→`Edit` (`oldString`/`newString`→`old_string`/`new_string`), `grep`→`Grep`, `glob`→`Glob`, `subagent`→`Agent`, `webfetch`→`WebFetch`, `websearch`→`WebSearch`, `skill`→`Skill`; others (`patch`, `execute`…) keep their names |
| `finish: error` / interrupted (`error.type: aborted`) | notice `出错了：…` ("Error: …") / `已中断` ("Interrupted") |
| `finish: length` / `content-filter` (output hit the limit / blocked by the content filter) | notice `输出达到上限，这一轮停了` ("Output hit the limit; this turn stopped") / `这一轮被拦下了` ("This turn was blocked") |
| `idle` (`outcome: interrupted`) | notice `已中断` ("Interrupted") |
| `compaction` | notice `已压缩上下文` ("Context compacted") |
| Commands the user runs with `!` (`shell`) | A `Bash` `tool_use` + `tool_result` pair |
| `system`, `synthetic`, model / agent / directory switches | No item (seq still advances) |

Sub-agents (child sessions opened by the `subagent` tool, non-empty `parent_id`) are not listed as separate sessions; their permission requests count toward the root session (the card and "Needs you" are on the root session), and their other events are ignored.

## Permissions and questions

- **Permissions** (`permission.asked`: `action` is `shell` / `edit` / `read`…, `resources` is the command or path, `save` is the rule "always allow" would record):
  if the phone answers first (`answerFirst`: `auto` = to the phone first if it drove this session within the last 6 hours, otherwise to opencode's TUI first; `phone` / `local` pin one side),
  the card belongs to the runtime itself (`agent: "opencode"`), with tool names drawn the Claude way (`Bash`, `Edit`…). Allow = `once`; a card with `save` has "Always allow <rule>" = `always`;
  a deny with a reason passes the reason to opencode as well (`message`). The dialog in the TUI stays up, and **whoever answers first wins**: if the TUI answers first, the card on the phone is withdrawn when `permission.replied` arrives;
  tapping "Hand back to computer" on the phone does not answer. While waiting, the plugin reports `blocked` for the session's "Needs you" state.
- **Questions** (`form.created`, opencode's question tool): the plugin API cannot answer forms, so it is **handed back to the terminal**: the session reports `blocked` ("Needs you") and you answer in herdr's terminal.
  Answering a form needs the HTTP API (which needs the server's password), and the plugin does not read the password file, so this is not done. To be added once opencode exposes it in the plugin API.

## Installation

On the computer that runs opencode (it must be in the hub's machine list, and node must be able to start).

1. `node <Persona repo>/integrations/opencode/install.mjs`: in `plugins/` under the opencode config directory (`$XDG_CONFIG_HOME/opencode`, default `~/.config/opencode`, changeable with `--config-dir`),
   it writes a `persona.js` with a single line `export { default } from "<Persona repo>/integrations/opencode/index.js"` (so updating the checkout updates the plugin).
   It refuses if a file with the same name exists that it did not write; `--force` overwrites, `--uninstall` removes it. opencode loads `plugins/*.js` by itself, so `opencode.json` needs no change.
2. Restart the opencode server (the `opencode serve --service` process) once: the plugin runs in the server process, and a server that is already running will not load a new plugin by itself. Restarting the server briefly disconnects the TUIs attached to it; restart it the way you usually do.
3. herdr's opencode integration (`herdr integration install opencode`): reports state, session id and title. Leave it alone if already installed.

Other opencode plugins (notification ones) can subscribe to the event stream at the same time without interfering.

### Configuration (optional)

`<opencode config directory>/persona.json`:

| Field | Default | Description |
| --- | --- | --- |
| `answerFirst` | `auto` | Who gets the permission card first (see above) |
| `listDays` | `7` | How many recent days of non-running sessions to list (sessions the phone touched, running ones and waiting ones are always listed) |
| `maxSessions` | `80` | Maximum list length |
| `socket` | `~/.persona/runtime.sock` | node's runtime socket; `PERSONA_RUNTIME_SOCKET` (or its old name: [rolling-upgrade-compatibility.md](../protocol/rolling-upgrade-compatibility.md)) also works |

Environment variables: `PERSONA_OPENCODE_DB` (location of opencode.db), `PERSONA_OPENCODE_DEBUG=1` (plugin logs to stderr, visible in the opencode server's output).

## Token usage

The node meters opencode's token usage from its database (the same file this runtime reads, `PERSONA_OPENCODE_DB` included), with the system `sqlite3`: each finished step and
compaction, reasoning counted as output, under the agent `opencode` (`docs/server/machines.md`, "Token usage"). It does not need the runtime or a running opencode; without
`sqlite3` on the computer opencode's usage is not counted.

## Limits and later

- Only sessions in server processes that have the plugin installed. A server process's list is the most recent root sessions in the database (sessions run by other server processes are in the database too and get listed as idle;
  node picks the runtime that is actually running it; usually there is only one shared server).
- Tool calls in a step only appear in the chat once the step ends (see "seq" above).
- Questions (`form.created`) and opencode's plan mode cannot be answered on the phone; they are handed back to the terminal.
- Cannot create an opencode session / open a new pane in herdr from the phone (no `start`); a stopped herdr session cannot be continued from the phone either (no `resume_argv`).
- Permission mode, thinking effort, model switching: the plugin has no `modes`, so these pickers on the phone reply "not supported" for opencode sessions.
- Auto-update (bundling the package into personad and shipping new versions with node, as pi does) is not done: the plugin is a file in the checkout; after `git pull`, restart the server to pick it up. When this gets done, register it in `runtimepkg.Packages` and the SDK's `PACKAGES`.

## Pitfalls

- **`setup` must return immediately.** opencode makes requests wait on "Location plugins did not finish activating"; `await`ing the event loop inside `setup` makes every request for that directory return 503. Run the event loop in the background (`void`).
- **`setup` must return a cleanup function**, and the event subscription must abort with it (`ctx.event.subscribe({signal})`): on SIGINT the server process waits for plugins to clean up, and if the subscription does not stop it hangs there until `kill -9`.
- **The event stream is server-wide; every directory's instance receives every event** (repeated once per instance): dedupe by event `id`; don't process it in every instance.
- The plugin context's functions are generated from the HTTP API, and take **a single object** (path parameters + query + body merged), not positional arguments.
- If `ctx.session.get`'s `time.idle` is older than `time.updated`, it is running (only used as a guess when a session is first seen).
- The default public models return 403 in some regions (`This model is not available in your country`): end-to-end tests use a scripted fake model (`e2e/mock-llm.mjs`), configured as a custom provider.
- v2's config keys are `providers` / `permissions` / `plugins` (not v1's `provider`), and the plugin type package `@opencode-ai/plugin` 1.x has no v2 `session` / `event` / `permission`: trust runtime probing.

## Verification

- `cd integrations/opencode && npm test`: mapping (tool names and arguments, ids not shifting with results, error / interrupt / compaction / user shell), list (root sessions, archived, expired, model window, context usage), pagination (by turn, backward and forward, truncation at finalized messages),
  sending (once per `client_msg_id`, images, picking the context by directory), interrupt, rename, push continuity and streaming, event dedup and primary handover, permission cards (allow / always / deny with reason / hand back / TUI answers first), sub-agent permissions, no database, install script.
- node: `server/internal/node/opencode_test.go` (fake herdr): a single herdr session row, state from herdr / plugin adds blocked, identifying the session from the plugin's pane, plugin not installed, terminal rows for other agents, title prefix.
- End to end: `PERSONAD=<personad> node integrations/opencode/e2e/run.mjs`: a real `opencode serve` (temporary config / data / state directories, plugin installed by `install.mjs`), a scripted fake model,
  a real `personad node` acting as hub: the plugin connects and lists sessions, sending a message gets an answer, a resend goes in only once, the permission card reaches the phone and is allowed / denied with a reason, streaming, interrupt, rename, pagination, sessions disappear after the server exits. 2026-11-05: 16/16 (opencode 2.0.32).
- Web: fake hub with `MOCK_OPENCODE=1` + `scripts/test/web_opencode_e2e.py`: opencode sessions in the sidebar and terminal rows for plugin-less / unknown agents, tool calls in the chat, permission cards (wide / narrow × light / dark).
- Not verified: a real TUI in herdr running together with herdr's opencode plugin (tests use the shape of the data herdr reports, not a live one), the user's own `opencode serve --service` (never installed into its config),
  long chats with a real model (compaction, very long tool output).
