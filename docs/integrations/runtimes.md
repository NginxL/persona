# Integrating Agent runtimes

Agents in Persona come from two kinds of places, integrated differently:

| | Host | Runtime |
| --- | --- | --- |
| What it is | Where the terminal lives: herdr, Herdr App, Orca, Tern (`docs/integrations/engines.md`) | An Agent runtime that manages its own sessions and has no terminal: DSH (`docs/integrations/dsh.md`), later the pi extension, platforms built on DSH… |
| Where sessions come from | node reads panes, recognizes sessions by process, reads Claude / Codex transcripts | The runtime reports its sessions and provides their content |
| Interactions (questions / plans / permissions) | Claude Code hooks | The runtime asks itself (`interaction.create`) |
| Where the code lives | One host implementation in Persona (`internal/host`, `node/host*.go`) | **No code added to Persona**: the runtime implements the same protocol with the SDK |
| Per machine | Exactly one engine | Any number, alongside the engine |
| How the App knows what it can do | The host's capabilities (terminal, keys…) | Sessions carry `source_kind: "runtime"` and `capabilities` (the same fields as Codex app-server sessions), computed from the `caps` the runtime declares |

Paseo is a bit of both: it has no terminal and connects over the runtime protocol, yet it's an engine a machine can choose (`docs/integrations/paseo.md`).

This doc specifies the second kind: how a new runtime plugs in, what it must implement, and how it's accepted. The wire protocol is in `docs/protocol/protocol.md` ("Runtime ↔ node");
node's implementation is `server/internal/node/runtimes.go`.

## How multiple machines are aggregated

A runtime only connects to the **local** personad node (`~/.persona/runtime.sock`). Aggregating machines isn't the runtime's job; it's Persona's existing structure:

```
 phone / watch / web / Mac ── hub (one, always on) ── node (one per machine, over ssh or tailcat)
                                                 ├─ engine: Claude, Codex in herdr / Herdr App / Orca
                                                 └─ runtime.sock ◄── DSH, other runtimes (on this machine)
```

So "DSH on ten machines" = ten machines each with a node, each DSH plugin connected to its own node, and the hub merges them into one list (grouped by machine → space).
The runtime doesn't need to know where the hub is, open ports or authenticate (a 0600 socket for the same system user); networking (NAT, hole punching) is all handled on the node ↔ hub leg.

A runtime that can't reach the local socket (it's a long-running service: another system user, in a container, on another machine) connects the other way round and is called a **daemon** (`docs/integrations/daemons.md`):
it opens a WebSocket endpoint and node connects to it per `~/.persona/daemons.json`, with a token. Once connected it speaks the same protocol, and the SDK driver doesn't change a line
(`createRuntime(driver, {listen: {port, token}})`). The name comes from config and session ids are namespaced, so a daemon can't touch other sessions on the machine, and it has no `mcp.*`.

## What to implement

With the SDK (`integrations/runtime-sdk`, Node 22+, no dependencies) you only write a driver. Two methods are required:

| Driver method | Returns |
| --- | --- |
| `listSessions()` | Session rows: `session_id`, `title`, `cwd`, `status` (`working` / `blocked` / `done` / `idle` / `offline`), `updated_at`, `rev` (changes whenever content changes; the App fetches new content based on it), `last_text`, `space_id` / `space_name` (sessions in the same space are grouped together in the App), `model`; optionally `permission_mode` / `effort` (the current mode), `transcript` (this session's chat is a local Claude transcript: the content belongs to node, see "Runtimes that run Claude Code" below), `codex_thread` (this session's chat is a local Codex thread, see "Runtimes that run Codex" below), `is_subagent` / `parent_session_id` (another of the runtime's sessions started this one, see "Sub-Agents and background tasks" below), `started_at` (when the session began: a child's card counts its running time from it) and `started_by_persona` (the hub started this session through the runtime or had it take the session over: the manager finds a start whose answer it lost among these, so say it after a restart too; a daemon's is dropped, #8130). A session `start` answered before it really began (see below) is listed with `starting: true` until it has, and once it failed as `offline` with `start_error` (why) for a while, as the node lists its own starts (`docs/protocol/protocol.md` ("New session")) |
| `items(params)` | One page of items (same params and result as `session.items`: from the end backwards by `turns` / `limit`, `before_seq` to page back, `after_seq` to catch up, `from_seq` + `through_seq` for a range), with `history` and `to_seq` |

Optional (for each one present, the SDK declares the capability in `runtime.hello`; for those missing, node answers "not supported" for you and the App displays as usual):

| Driver method | Capability |
| --- | --- |
| `cursor(sessionId)` | The current `to_seq`, or `{to_seq, history}` (with history, a rewrite with the same item count can still be detected; if omitted, the `to_seq` and `history` of a `turns: 1` page are used) |
| `image(params)` / `item(params)` | Raw image data / the full text of a truncated item |
| `send({session_id, text, images, client_msg_id})` | Sending a message from the phone. **Dedup by `client_msg_id`**: the App resends on reconnect. Resolve to `{queued: true}` when the message only waits behind the running turn, so that nobody takes it for read (#8097) |
| `interrupt({session_id})` | Stop the running turn. Resolve to `{settled: true}` once the turn has actually ended, and throw when it could not be stopped; resolving to nothing means "asked to stop, not confirmed" (#8097). A turn waiting on the user (a dialog, a card) must end too |
| `rename` / `close` / `start({cwd, prompt})` | Interrupt, rename, close, create from the phone (with `start` and a configured `launcher`, the new-session form gains a launcher field). The apps offer Rename and Close on its rows from `capabilities.rename` / `capabilities.close`, which carry `caps.rename` (not on a sub-Agent's row) and `caps.close` (#6294) |
| `hasLocalClient(sessionId)` | Whether this runtime's own UI is open for that session (decides who gets permission requests first, see below) |
| `unlistedTranscripts()` | The Claude session ids of sessions of its own that `listSessions()` no longer lists (archived in the runtime), or nothing while it cannot say yet; no capability is declared. See "Runtimes that run Claude Code" below |
| `modes({session_id, permission_mode, effort, model})` | Switching permission mode / reasoning effort from the phone (`caps.modes`; optional model changes require `createRuntime` option `modelSwitch: true`, advertised as `caps.model`; values are Persona's: `default` / `acceptEdits` / `plan` / `auto` / `bypassPermissions`, `low` … `max`). The apps show the mode chips on its rows whose agent is `claude` or the runtime's own, never Paseo's catch-all `paseo` (`capabilities.modes`, `docs/protocol/protocol.md` "Codex sources and capabilities"). A row may take modes and model away from its own session in its `capabilities` (`modes: false`, `model: false`: Paseo's archived agents, #8133), and a row whose agent is not `claude` may list the levels `effort` takes for it (`efforts`, weakest first), which the apps offer alone on it, and the permission modes it takes (`capabilities.permission_modes`, #8261: `default`, its own approval, and `bypassPermissions` where it has an Allow all), between which the apps offer a two-state switch, "Launcher default" and Allow all; node drops both where the runtime declared no `caps.modes` or the row took modes away, and `permission_modes` where the Claude Code chips stand |
| `rewind({session_id, prompt, occurrence, prompts_after, restore})` | Rewinding a conversation to before one of the user's messages (`caps.rewind`): find the message by its text, its occurrence counting from the end and how many of the user's came after it, and refuse (`rewind_mismatch`) rather than cut elsewhere. Its rows may narrow it per session in their own `capabilities` (`rewind: false`), and only a row's `rewind_code: true` lets `restore: "code_and_conversation"` through (`docs/protocol/protocol.md` "Codex sources and capabilities") |
| `models({launcher, refresh})` / `check({launcher, cwd, prompt})` | The launcher's models as the runtime has them now (`caps.models`, the pickers' refresh) / Settings › Models "Test" run through the runtime with its own login (`caps.check`): one real prompt in the node's empty check directory, answered as `{ok, model, answer, error}` |
| `seen({session_id})` | The user saw the session in Persona (`caps.seen`, #6299): a runtime that keeps an unread mark of its own clears it, so the session is not unread in its own app too. The manager tells the node only while the runtime lists the session as `done` (unread), best effort |
| `archive({session_id})` / `unarchive({session_id})` | The runtime's own reversible archive (`caps.archive`, declared only when both exist): when the user archives a session in Persona, it is archived in the runtime too, and restored with it. Archive an idle session without stopping or closing anything (Persona refuses busy sessions first) and keep it restorable; both must be idempotent (already archived / restored is success). A runtime whose archive also stops the session, or that has no way back, leaves them out: the archive is then Persona's alone (`docs/protocol/protocol.md`, "Agent session archives") |
| `createWorkspace({cwd, workspace_id, branch, create_branch, base, name, key})` / `prepareWorkspace({cwd, repo_root})` | Workspaces of its own (`caps.workspaces`, declared only when both exist: the node asks where the worktrees go before each one it has made): a worktree it makes as a workspace of its own / the folder it puts a repository's worktrees in. With them it lists its workspaces through `rt.workspacesChanged(list)`. Only an engine's are used; see "Workspaces" below |
| `renameWorkspace({workspace_id, title})` / `renameWorkspace({space_id, title})`, `archiveWorkspace({workspace_id})` | With `caps.workspaces`: a workspace's title (resolves to `{workspace}`, its row now) or a space's name (resolves to `{space_name}`, the name it shows now) / the workspace archived, a worktree it made removed from the disk (its branch kept). Without them those requests are `unsupported`; see "Workspaces" below |
| `watchChanged(id, watching, {transcript})` | The phone starts / stops watching a session (when a new node connects or the runtime stops, all stop): for runtimes that only follow a session while someone is watching (Paseo subscribes to the timeline per session). A watch is the one sign that someone has the chat open: every App watches a chat it opens, the manager watches only the turn of a message a channel delivered there, and background reads never watch. A runtime whose engine would start a stopped agent just to read it (Paseo loads an agent it let go, relaunching its provider, #8132) reads such a session only while it is watched, and answers other reads from what it already has or refuses them with `RpcError(…, 'not_loaded')` (`items`, `item`) |

In `createRuntime`'s options, a runtime that runs several Agents can also pass `launchers: [{name, model, agent, models, auto_accept}]`: besides `launcher`, the new-session form then has these entries, one per provider,
each with the Agent and models it starts; when they change later, call `rt.launchersChanged(list)`. `auto_accept: true` marks an entry whose Agents ask for approval
unless a switch is on, which its `start` turns on for `permission_mode: "bypassPermissions"` (Paseo's Auto Accept): the manager starts its own Agents there in
`bypassPermissions`, as it starts Claude Code's (#8141). An entry may say what the apps call it (`display_name`, Paseo's label for the provider) and, when its
agent is not Claude Code, the permission modes a start on it takes (`permission_modes`, #8261): New Agent then offers those ("Launcher default", sent as no
mode, and Allow all with two of them) and only the efforts its model lists, rather than Claude Code's five modes or none. `resume: true`: `start` accepts `{resume: <Claude session id>}` to take over a Claude session it doesn't have
and keep that id (`caps.resume`, for runtimes acting as the engine to continue "Not running" sessions). `start` also receives the form's choices (`launcher`, `model`, `permission_mode`, `effort`,
`workspace_id`) and the id the hub named for the new session (`session_id`, #8130): give the session that id when it is valid and no
session of the runtime's has it, so that a start whose answer the hub never got is found by it; otherwise pick one's own. See
`docs/protocol/protocol.md` ("Runtime ↔ node"). A `start` that takes longer than the node waits (30 s) may answer with the row the session
will have, `starting: true`, and finish in the background: keep listing that row until the session runs, and should it fail, list it
as `offline` with `start_error` for a while rather than letting it vanish; a message sent to it meanwhile waits for it (resolve to
`{queued: true}` when it goes later than the call's wait), and is refused once the start failed (Paseo, `docs/integrations/paseo.md`).
The node does not take a session it saw listed only as starting or as failed for one of the runtime's once it leaves the list: a
Claude session whose takeover failed is the machine's again, sent to as a session not running. `readonly: true` (`caps.readonly`): a `start` with `permission_mode: "plan"` (an ordinary
plan-mode start, or a read-only task on a non-Claude runtime, #3238) runs in a read-only mode before its first prompt, or throws `readonly_unsupported` — never falling back to a writable
provider, and a takeover that cannot set the mode refuses too. Without it the node refuses plan starts for the runtime itself. Claude task dispatches use `bypassPermissions` and do not require this capability.
A launcher entry may carry `prompt_optional: true`: a Codex one the runtime starts with no first message too, so the apps do not ask for one.
`sameKind: true` (`caps.same_kind`, #8140, Paseo): its starts run only its own launchers. A `start` naming a launcher it does not have carries
`agent_type`, the kind of agent that launcher starts (`claude`, `codex`…): the runtime starts one of its own launchers of that kind, or throws
`launcher_kind_missing`, never another kind of agent nor a default of its own. Whatever the start asked for that it does not run with (that
launcher, a model its stand-in does not offer, an effort the model does not offer, lowered to the nearest it does) its answer says as
`instead: {launcher?, model?, effort?}`, the row saying what it runs with; the node passes it on, and the hub and the manager tell the user.
Where such a runtime is the machine's engine, its launchers are then all the new-session form lists for a start there (`launchers.list` with
that host), and a name it shares with a launcher of the login shell or of Persona is its own there (`docs/integrations/paseo.md` "A launcher
Paseo does not have"). Without it, a launcher it does not have reaches it as `shell_launcher` (it picks its own of that name, or its default).

Call the SDK when things happen:

| Call | When |
| --- | --- |
| (no call needed) | The list only needs recent sessions: node still recognizes older unlisted sessions as yours (ones listed before are remembered; ones never listed get one `items` query), and calls are still routed to you, so `send` / `items` must handle sessions not in the list |
| `rt.sessionsChanged()` | Sessions added or removed, a turn started / ended, a rename |
| `rt.append(id, {items, fromSeq, toSeq, history, update})` | New content was persisted (when no one is watching, the SDK just has the list refresh `rev`). `items` can be empty: content not shown in the chat must still advance seq. Set `update: true` to replace existing items by id without advancing seq; a batch keeps the flag if any append sets it. This flag stays inside the runtime-to-node protocol: App `items` events omit it because clients already merge by id and reserve `update` for the hub-update object |
| `rt.stream(id, text)` / `rt.streamEnd(id)` | The full text being generated / that segment ended (the SDK throttles to at most once every 120 ms) |
| `rt.workspacesChanged(list)` | Its workspaces changed (one made, renamed or archived, a branch moved): the whole list, which the SDK also tells every node it connects to later. Only with `createWorkspace` and `prepareWorkspace` ("Workspaces" below) |
| `rt.ask(interaction, {signal})` | A person must decide: the card goes to the phone and waits for an answer. Returns a `Resolution`; `{dismiss: true}` = handed back to the computer (tapped on the phone, expired, no hub), for the runtime's own UI. Meanwhile the session shows "Needs you" automatically |
| `rt.phoneFirst(id)` | Whether to ask the phone or the local UI first |
| `rt.tools()` / `rt.callTool({name, args, session_id})` | The hub's tools (the phone's `phone_*`, `mail_*`) for your own Agent: list them and register as your own tools; calls go through node to the hub (`createRuntime`'s `onHub` is called after hello and whenever the hub comes and goes, which is the right time to fetch tools) |
| Throw `RpcError(…, 'handover', {delivered: 'none'})` on send | A disk-only companion process: the session doesn't run here, and the program that can run it has been started; node hands the message to the runtime taking over (`requires: ['handover']`, DSH's `dsh-files`) |

`rt.streamEnd(id)` retires a published live text even after the final viewer leaves. It cancels pending text and
sends the empty boundary to the node while a stream entry exists, so the Hub clears its cached reply before a later
viewer subscribes. Call it whenever the generated segment ends, including while the session is unwatched.

What the SDK handles (no runtime needs to write it again): connect / reconnect / `runtime.hello` (with this process's `instance`: if another process uses the same runtime id, node refuses the connection; the SDK logs one line and retries every 30 seconds, without taking over), list pushes, watch backfill and continuity (marking honestly when `from_seq` / `to_seq` don't line up,
so the phone fills the gap), byte-by-byte streaming, the session turning "Needs you" while asking the phone, asking again with the same id for the same interaction when node restarts or its hub link drops (`interactionId(...)`; it waits up to 2 minutes from when the node or hub went away, however long the card was up before, #8128; a card under an id the runtime gives is resumable on the hub, so the phone keeps the card meanwhile and an answer given then is not lost),
withdrawing retracted interactions from the phone, handing the same `client_msg_id` to the driver only once (retries get the first result; failed ones can be resent), the "who answers first" policy (when node says there's no hub, always the local UI first, `rt.hubConnected`), and reconnecting quickly after a drop (one attempt per second while the socket file exists).

Things shared across runtimes but not part of the protocol are in the SDK too, for runtimes to use directly:

| What | Does what |
| --- | --- |
| `rt.recent(rows, {days, max, pinned})`, `rt.touched(id)` | Picks the sessions to list: running / waiting for an answer (`pinned`) and ones the phone touched (last 50) are always listed; the rest within `days` days fill up to `max`, newest first. A session open on the phone must stay listed |
| `page(items, params, {history, toSeq})` | Paging for `session.items` (from the end by turns / count, backwards, catch-up, by range) |
| `appToolName(name, own)` | Maps tool names to the set the App draws (`bash`→`Bash`, `read`→`Read`…); the runtime adds its own (DSH's `ask_user_question`, pi's `find`); parameter names are converted by each runtime |
| `scriptedModel()` in `testing/model.mjs` | A scriptable fake model for end-to-end tests (DSH's llm-mock-server, switching scripts by phase) |
| `conformance()` in `testing/conformance.mjs` | Generic end-to-end acceptance: text turns (thinking, reply, turn end, paging), approval, denial with a reason, questions, streaming, interrupt, push continuity; a runtime only passes its own tool parameters and then checks what's specific to it |

Not in the SDK (used by only one runtime, or the two read different things): each one's way of reading its session storage, and keeping companion processes alive.

## Content

- **seq**: each session has one monotonically increasing sequence number, starting at 1 (0 means "nothing yet"). An item's seq is the number of the record that produced it; one record can yield several items,
  with ids `<seq>#<index>`. DSH's event seq starts at 0, so it maps to seq + 1. **Ids are unique only within a session**: across sessions, and especially across two branches split from the same session,
  the same id is different content. Anything a client caches by id (render results, parse results) must key on the session too (#372).
- **history**: the identity of the session's content (`docs/protocol/protocol.md` ("Continuity of items")). It changes when content is rewritten (branch, reset), and the App refetches the whole page; it stays the same on pure appends. A batch with a new history doesn't need a larger seq (switching back to a branch with the same count, shorter after a rollback); the SDK pushes it anyway and continues from its `toSeq`.
  An explicit `fromSeq: 0` also resets the cursor, even with the same history; it promises that the batch contains the complete range through `toSeq`. Node and Hub retain the new cursor for reconnects. Catch-up pages report the actual cursor even below `after_seq`. An empty rewind notification retains its positive `from_seq` so clients reload the missing history. Node and Hub pass the known history in internal `session.watch` requests, so a same-length rewrite is reported after reconnect too. An unchanged idle watch sends no empty batch.
- **Item kinds** follow what the App already has: `text` (user / assistant), `thinking`, `tool_use` (`tool_name`, `tool_input`, `tool_use_id`), `tool_result`
  (`tool_use_id`, `is_error`), `image` (`lazy: true` placeholder, data via `image`), `notice` (system: interrupt, error, compaction).
- **Tool names match Claude's**: the App draws commands, diffs and files for `Bash`, `Read`, `Edit`, `Write`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `TodoWrite`,
  and parameter names are Claude's too (`command`, `file_path`, `old_string` / `new_string`, `pattern`). Unrecognized names are shown as is.
- **Length**: in a page, at most 24 KB of text per item and 4 KB of tool output, cut with the SDK's `light(item)` (with the "truncated" note the App recognizes and `full_size`); the full text goes via `item`.
  Tool input objects use `clipInput`, which follows the hub's `clipJSON` in three passes. First keep at most 12 KB per string at any
  depth, including arrays such as MultiEdit's `edits[]`; if the input still exceeds 24 KB, share the remaining budget among strings
  longer than 256 bytes, accounting for JSON escapes and the truncation note. Cuts respect UTF-8 boundaries and the second pass only
  cuts when the string becomes strictly shorter as JSON. A plain ASCII tail of at most the note's encoded size stays whole (37 bytes
  for a three-digit original length); a longer or more heavily escaped tail may be cut.
  If the result is still over budget, a third pass drops tail items from the currently longest arrays (the most retained items,
  preorder breaking ties), then trailing fields from the currently widest objects the same way. Object children use Unicode
  code-point order for removal and preorder ties, matching the hub's UTF-8 byte-wise sort rather than JavaScript's UTF-16 or
  integer-first enumeration order. Surviving array items keep their order; surviving object fields keep each implementation's
  own output order and value types. For every top-level object accepted by the JSON decoder, the final encoded input is at
  most **24,576 bytes**, including the marker: one collision-safe top-level field named `zz_persona_clipped` (suffixed `_2`,
  `_3`, … if needed), whose value is an object of the form `{"note":"Omitted X array items and Y object fields.\n…（已截断，共 N 字节）"}`.
  The note counts direct removals and ends with the usual cut note and the original whole-input byte size. The object wrapper
  is deliberate: old and new clients' "first string argument" subject and summary fallbacks cannot mistake the marker for the
  call's subject. Typed tool projections ignore the unknown root field; generic argument and Raw Input views show it.
  Inputs at or under budget are untouched. Each implementation measures its own wire encoding: both leave `<>&` raw, but
  `JSON.stringify` also leaves U+2028/U+2029 raw while the hub's Go encoder escapes them, so their shares and output bytes can differ.
  Malformed input, trailing JSON and nesting the decoder rejects (deeper than 10,000 levels) pass through unchanged as before.
  A valid number outside float64 range, such as `1e1000`, is retried with `UseNumber` so its object is still clipped. Top-level
  arrays and other non-object inputs stay unchanged.
- **Only what people need to see**: context injected for the model (time, skill list, system instructions) and internal scheduling events don't become items.

## Interactions

- Three kinds: `question` (`questions[{question, header, options[{label, description}], multi_select}]`; the answer is "question text → option text", multi-select joined with `, `,
  anything not among the options is what the user typed), `plan` (`plan` body; answer `approve` + `feedback`), `permission` (`tool_name`, `tool_input`, `summary`; answer `allow`).
- **Who answers first** (`answerFirst`): `auto` (default) = the phone first if the phone sent this session a message or created it in the last 6 hours, or the local UI isn't open; otherwise the local UI first.
  `phone` / `local` pin one side. "Hand back to computer" on the phone and expiry both return `{dismiss: true}`, and the runtime then hands it to the local UI, so it never ends up unattended on both sides.
  A session the hub started through another runtime counts as phone-driven here too: the node says so with `runtime.driven` (#8097).
- **Waiting in its own UI**: while a session waits on the user in the runtime's own dialog, call `rt.waitingOnUser(sessionId)` and the returned
  release when it ends: the session lists as `blocked` meanwhile, as for a card on the phone. A dialog nobody may be looking at (a web page
  with no browser open) must also be withdrawable: pass the turn's abort signal to it, so that an interrupt ends it (#8097).
- **Cards belong to the runtime**: node tags cards it asks with `agent` (the Agent the runtime runs, or the one the session's row names) and `agent_label` (its name: the row's own `agent_label` whenever it has one, else the runtime's `name`; a runtime running several Agents under its own id names each in its rows, as Paseo's providers, #8142), and the App draws them as "another Agent's card"
  (the same kind as Codex's: "DSH Needs Your Permission", Approve / Deny, `reason` above the command). **Always Approve** only appears when the card has `always_label` (saying exactly what is always approved);
  don't send it if the runtime can't do it. `always_label` itself is just text; "always" is enforced by the runtime (opencode's save rules and Paseo's suggestions are both narrower than the whole tool).
  If a runtime can't remember session rules itself, it declares `caps.keep_always: true` in hello (the SDK's `keepAlways`; DSH does this): only then, when its `permission` card carries `always_label`,
  does node write `always_keys: ["runtime:<runtime id>:tool:<tool_name>"]`; the hub remembers it per session id, and later cards for the same tool in the same session are answered by the hub directly with "allow + always" (#3974).
  `always_keys` sent by a runtime are always cleared; no key is written for runtimes without `keep_always`, the health space, or cards with `fields`. When the runtime runs Claude Code (`agent: "claude"`), cards are drawn as Claude's.
- **The text written on deny**: `Resolution.message`. If the runtime's permission can carry a reason, pass it; otherwise deny first, then steer that sentence into the turn (DSH does this).
- **"Needs you" doesn't mean there's a card**: a session waiting for a person in its local UI reports `blocked`, the phone shows "Needs you", and the hub doesn't read the screen to build a terminal card (never for sessions without terminal capability).
- Use the runtime's own "timed question" if it has one: if the local side times out first, the card on the phone can stay, and a late answer is handed back to the runtime (DSH's `userQuestions.answer`).

## Naming

- `runtime` id: lowercase, `[A-Za-z0-9][A-Za-z0-9_.-]*`; it's also the `space_kind` and the sessions' default `agent`. `name` is what the App calls it (sessions carry `agent_label`).
- If the runtime runs an Agent the App knows (say, a platform running Claude Code), set `agent` to that (`claude`) and the App displays it accordingly; otherwise leave it unset.
- A runtime id can be used by only one process per machine: to run the same program twice on one machine (two DSH profiles), make the id, `name` and `launcher` configurable;
  node rejects a second process with the same id (`runtime_conflict`). If a runtime changes its id but still runs the same kind of Agent, set `agent` to that Agent (a renamed DSH sets `dsh`), and session rows carry `agent_label`.
- Session ids are unique on the machine, at most 128 characters, and can't be paths. `launcher` names shouldn't clash with launchers in `~/.zshrc` (on a clash the shell's wins and the runtime's isn't listed, except where every start goes to the runtime as an engine with `caps.same_kind`: there its own wins);
  if two runtimes' launchers clash, only the one that connected first provides it.
  A local `agy` launcher supplies the model catalog and starts in a pane when the selected host is herdr (#10237),
  including when herdr is the default and the node detects the local CLI. The connected companion serves Assistant
  and terminal-less Agent requests (the node RPC can target `host: "agy"`). A shell alias, function or helper definition
  named `agy` still has precedence over automatic companion selection (#9181).

## Minimal example

`integrations/runtime-sdk/examples/echo-runtime.mjs`: sessions in memory; it "thinks" briefly and then echoes back whatever it receives, asking for a permission first if the message contains `ask`.
About a hundred lines, and it's the skeleton for a new runtime: replace `sessions` with your session storage, `reply` with your Agent, and the item mapping with your own
(see `integrations/dsh/lib/map.js`).

```js
import { createRuntime, light } from '<Persona>/integrations/runtime-sdk/index.js'

const rt = createRuntime({
  listSessions: async () => [...],          // session rows
  items: async (params) => ({ items, history, to_seq }),
  send: async ({ session_id, text, client_msg_id }) => { /* hand to the Agent, dedup by client_msg_id */ },
}, { runtime: 'acme', name: 'Acme', launcher: 'acme' })
rt.start()
// When content is persisted: rt.append(id, { items: [light(item)], fromSeq, toSeq, history })
// When a person must decide: const res = await rt.ask({ id, kind: 'permission', session_id, tool_name, tool_input })
```

## Runtimes that run Claude Code (optional)

When a runtime runs Claude Code (via the SDK, e.g. Paseo, `docs/integrations/paseo.md`), its sessions already have a local Claude transcript. If the session row carries
`transcript: <Claude session id>`, node serves the content from that transcript: items, images, watch, `session.info`, sub-Agents, the row's last line and "what it's doing",
with the same parsing and the same look as Claude sessions in herdr; the transcript's own "Not running" row is no longer listed. The runtime only handles state, sending, cards and modes;
on watch it receives `transcript: true` and only pushes streaming text (the SDK automatically drops `append` for such sessions). Sessions whose transcript hasn't been written yet get content from the runtime as usual,
and node switches over automatically once the file appears. A daemon can't do this (it would read other sessions on the machine).

Three things stay the runtime's word on such a row. Its own `background` list is kept beside the background tasks the transcript shows (#4494): work the runtime started for the
session outside Claude Code. If both sources name the same task ID, it counts once and the transcript keeps its richer local details
(including its output file, #9320). Its `model` decides the context window when it names a 1M variant (`…[1m]`, #4496): Claude Code writes the id without the suffix into the
transcript, and a gateway writes an id of its own, so the transcript alone reads as 200K, or as no window. A window the transcript records itself keeps the transcript's word,
and so does a session answering as another model: after a refusal's fallback, or when the reply came from a Claude model that is not the one the runtime names. And its `title`
(#8133): where the transcript records no title (a custom or an AI one), node takes a terminal session's title for its first prompt (`title_source: "prompt"`, which Persona's
automatic naming may replace), but a runtime's title is the first prompt only when it is what Paseo makes of that prompt (`server/internal/node/runtimetitle.go`). Any other
title (a rename in the runtime, one another agent gave) has no known provenance, and nothing replaces it.

**Sessions the runtime no longer lists** (#4521). A runtime that starts Claude Code's command line itself (Paseo does; a host on the Agent SDKs does not) writes transcripts
that say `sdk-cli`, like a script's one-shot `claude -p` run, and node leaves those out of the sessions not running. While the runtime lists a session, its row is the session.
Once it stops listing it (archived in the runtime), only the transcript is left and nothing in it says whose it was. So the runtime names them: `unlistedTranscripts()` returns
their Claude session ids, the SDK sends them with every list (`unlisted_transcripts` beside `sessions`, the feature `unlisted_transcripts`), and node lists those transcripts as
sessions not running, under the usual limits (7 days, the 80 newest). Name only the user's own sessions: not the ones another session started, not ones the user deleted. At
most 2000 count, the last ones named; a transcript that a row also names is that row. A named transcript is node's from then on, even when the runtime listed it before under
that id: node reads its content, and a message to it starts it in the engine again (`start({resume})`), never a call to the runtime about a session it no longer has (#8131).

- An empty list says there are none. While the runtime cannot say (it was just started and has not read its archive), `unlistedTranscripts()` returns nothing, the SDK leaves
  the field out, and node keeps what the runtime named before. If the method throws, the list still goes out, with the ones named last.
- After the runtime disconnects, node keeps them for as long as its list of sessions not running goes back (7 days), and with them the runtime's rows that name a transcript
  here (#8131): those stay listed under their ids, not running, read from their transcripts, with no controls but reading and sending, until the runtime lists again. A
  message, a rename or closing one is refused as `runtime_away`, nothing sent. So a runtime that restarts does not take its sessions off the list and bring them back, and
  one that stays away (its program stopped, the computer moved to another engine) leaves its Claude conversations readable meanwhile. Rows that name no transcript here
  leave with it. Node remembers none of this across its own restart.
- Name only a session whose id is its transcript's. Node lists a named transcript under the transcript's id, so a session the runtime lists under an id of its own (a session
  Persona started before Claude Code had a session id, as Paseo's `paseo-<agent id>`) would come back under another id once archived, and everything Persona keeps under
  its id (a to-do's link, a pin, an archive mark, its read state) would be left behind (#4771). Keep listing such a session yourself instead: an `offline` row under the same
  id, with `transcript`, bounded like node's own list (Paseo: the latest 80 of the last 7 days, beside its other rows). Node reads its content from the transcript and lists
  no second row for it, as for any row naming a transcript, so this needs nothing new from node. A message to it reaches the runtime, which continues it under that id.
- A row under an id of its own also names, for the manager, the other ids its conversation went by: node puts the transcript and the sessions it was forked from (each
  transcript's first record names its source in `forkedFrom`) in the row's `aliases`, lists none of them beside it, and the manager shows no archive record under one
  (`docs/protocol/protocol.md` "Agent session archives", #8131).

## Runtimes that run Codex (optional)

When a runtime runs Codex itself (e.g. Paseo), each of its sessions is also a Codex thread with a rollout on this computer, which node finds on its own and would list a second
time as Codex history. If the session row carries `codex_thread: <thread id>` (the feature `codex_thread`, #4495), node lists the chat once, as the runtime's session: the
thread's history row is not listed, and the thread's own sub-Agents (Codex thread spawns) carry the runtime's session as `parent_session_id` and sit in its space. Content,
sending and state stay the runtime's. When two rows name one thread, the one that is not `offline` counts, then the one used last. A thread is matched by its id, in whichever
Codex home its rollout is. A daemon can't do this: its threads are on another computer.

While the runtime's session is `offline`, another program may take the thread up (the Codex App runs it, a shared app-server has it loaded). That program's row is then listed
as well, with the thread's sub-Agents under it, so a message from the phone goes where the thread is open. Once the runtime's session runs again, the chat is that session alone.

## Sub-Agents and background tasks (optional)

When an Agent hands work off and ends the turn (background sub-Agents, workflows, commands running in the background), the session is "done" after the turn while the work isn't. The runtime reports them on the session row,
with the same fields as Claude sessions (`Session.subagents` / `background` in `docs/protocol/protocol.md`), which the App and hub already understand:

- `subagents`: `[{id, description, type, running, tool_use_id, started_at, updated_at, last_text}]`. `id` is the sub-Agent's id in the runtime (same character set);
  **its content is read by session id `<session>@<id>`**: node routes `session.items` / `item` / `image` / `watch` for that id to the runtime owning the parent session,
  and the runtime also uses that id when pushing `session.items` / `session.delta`; node always rewrites `session_id` to `<session>@<id>`, whatever the runtime writes.
  `tool_use_id` is the tool call that started it, and the App opens it from that call (the call name must map to `Agent`, parameters `{description, prompt}`). Sub-Agents aren't listed as separate sessions.
- `background`: tasks still running that will come back after the turn ends, `[{id, kind, description, command, started_at}]`; `kind` is `shell` (`command` is the command) or `agent`.
  `output_file` is a local file; runtimes don't provide it (node clears it). Foreground calls that are waiting for their result don't count.
- While any are running, the session counts as Working (#2228), and the hub's "done" push waits until they finish and the turn in which the session is woken up ends. Re-push `runtime.sessions` when they start and end.
- At most 50 sub-Agents and 50 background tasks per row, text cut to 500 characters; invalid ids are dropped. A daemon's sub-Agent ids are prefixed the same way: `<daemon>.<session>@<id>`.
- Controls such as messaging or renaming a sub-Agent: if the runtime doesn't support them, it returns `unsupported` (DSH doesn't: sub-Agents are managed by the parent session).
- **A session of its own that another session started** (Paseo's Agents created by another Agent, #4494) is not a `subagents` entry: it is opened, sent to and can need the user
  like any session. List it as its own row with `is_subagent: true` and `parent_session_id: <the starting session's id>`; node fills in `root_session_id`, and a daemon's
  parent ids get the daemon's prefix like its session ids. The hub sends no "done" push for it and holds its parent's while it runs or has background work. It is its parent's
  subtask: an App that folds subtasks lists it under its parent, and the fleet counts (the Live Activity, a phone's notification and widget) count the parent, not it.
  - While its `status` is `blocked` (it waits for the user) node lists it without its parent, so it is a row of its own in every App that folds subtasks by those fields: it
    shows it and counts it under Needs you, whatever it folds. Node names the parent in `asking_parent_session_id` meanwhile, so the Apps that show children as cards in
    their parent's chat keep it there (`docs/protocol/protocol.md` ("Session hierarchy")). It is listed under its parent again once it was answered.
  - List every session between a listed one and the session the line starts at: a row whose parent is not listed hangs nowhere.
  - Give the parent's row a `background` entry of kind `agent` for each one that has not finished (running, or waiting for an answer), so the parent reads Working until they
    report back. A parent that is `offline` meanwhile is left out of lists of what runs, and its subtasks with it: do not list it as `offline` while they run.

DSH's approach is in `docs/integrations/dsh.md` ("Subagents and background tasks").

## Workspaces (optional)

A runtime with workspaces of its own (Paseo's checkouts, worktrees and folders, #6295) can make them the computer's: the
workspace catalog lists them with or without sessions in them, its sessions sit in them, and "New worktree…" makes a
worktree as one of them. It implements `createWorkspace` and `prepareWorkspace` (the SDK then declares `caps.workspaces`)
and calls `rt.workspacesChanged(list)` with the whole list whenever it changes (`docs/protocol/protocol.md` ("Runtime ↔ node")).

- **A row** is `{workspace_id, title?, cwd, kind, branch?, space_id?, space_name?, repo_root?, owned?, pinned_at?}`. `workspace_id` is
  what its sessions name in their own `workspace_id` (the session id rule); `cwd` is absolute; `kind` is `checkout` (a
  repository's main checkout), `worktree` (a linked worktree) or `folder` (no repository; anything else counts as one);
  `space_id` / `space_name` are the space its sessions are grouped in; `repo_root` is the repository's main checkout;
  `owned` says the runtime made the checkout and removes it with the workspace. Optional `pinned_at` is the host's native
  workspace pin time (RFC3339), separate from Persona's session/project pins; absent, cleared or invalid means unpinned.
  A changed timestamp or an unpin is part of the next full push. At most 2000 rows; invalid ones are dropped.
- **Only an engine's count**: the node uses the list while the runtime is one of the computer's enabled engines (Paseo) on
  its own socket; a runtime that is connected but not enabled makes, lists and deletes no workspaces. Its rows are then `workspaces.list` rows whose `source` is the runtime id (`project_id` its `space_id`, `is_main`
  true for a checkout, `workspace_kind` `git` or `folder`), and its session rows keep their `workspace_id`. Any other
  runtime's `workspace_id` is cleared, as before, and a daemon never gets the feature.
- **Starting in one**: `start` gets the catalog row's id as `workspace_id`, sessions in it or not. One the runtime no longer
  has is refused (`workspace_missing`) rather than the session going elsewhere; a space id or a folder names no workspace.
- **Making a worktree** (`workspace.create`: "New worktree…", a new Agent's own worktree and the assistant's `agent_start`
  with a worktree): the node first checks what it checks for a terminal host (the source in a repository with a commit,
  a valid branch not checked out in another worktree, a folder that holds nothing), then asks for `{cwd, workspace_id?,
  branch?, create_branch, base?, name?, key}`: an existing branch checked out as it is (`create_branch: false`), else a new
  one cut from `base` (the source checkout's branch, or its commit when detached), `name` the folder's name; with neither
  branch nor name, the runtime names both. `key` is the same for a retried call. It answers `{workspace}`, the new
  workspace's row with the folder and branch it actually has, once the checkout exists; the node lists it at once.
  `name` comes as Paseo names a folder (`[a-z0-9]` runs joined by single hyphens, at most 50 characters), and the node
  has checked that nothing is at `<checkouts>/<name>`, an empty folder included: make the worktree there, never beside it,
  so the path the user was shown is the one made. The node gives the whole create one deadline (145 s, inside the hub's
  150 s), the time `workspace.prepare` took included.
- **Where** (`workspace.prepare {cwd, repo_root}` → `{checkouts}`): the folder the runtime puts that repository's worktrees
  in. The dialog's path must be in it; only the worktree's own folder name can be chosen.
- **Deleting one** (`workspace.archive`, #6297): only a worktree the runtime made (`owned`, which the catalog row carries
  too), and only when nothing could be lost that the user did not confirm: the node refuses while one of the workspace's
  sessions is at work (starting, working, waiting for the user, a state it does not say) or an Agent of another engine
  runs in the folder, and checks the changes the preview showed. Archive the workspace and its sessions, remove the
  folder, keep the branch; answer once it is done. A closed Agent's own worktree (`worktree/remove`) goes the same way,
  refused while a session in the workspace still runs.
- **Renaming** (`workspace.rename`, #6297): "Rename" on a catalog row sends `{workspace_id, title}` (its title only), and
  "Rename space" `{space_id, title}`: the space its sessions are grouped in, also when the apps named it by one of its
  workspaces. Answer `{workspace}` / `{space_name}`, and list the new names (rows and workspaces) at once.
- **Deleting** (`workspace.archive {workspace_id}`, #6297): sent only for a worktree the runtime made (`owned`), once the
  user confirmed the node's preview (the changes it showed, the sessions in it, the checkout's identity). Archive the
  workspace and its sessions, remove the worktree from the disk and keep its branch; answer once that is done. The node
  counts it deleted when the workspace has left the list and the folder is gone. Closing a workspace (archiving it and
  keeping its folder) is not asked of a runtime.

## Auto-update (optional)

When a runtime is **a plugin installed inside another program** (the DSH plugin), that program loads it once and keeps it; replacing the files has no effect until someone reloads it inside that program.
To avoid going to the computer and clicking every time, the plugin is split into two layers, and node brings the code over (#619):

- **Loader**: the file the host loads (DSH's `integrations/dsh/index.js`) only does "pick a version, load it, swap versions", using the SDK's `createLoader`
  (`runtime-sdk/lib/loader.js`), and is no longer changed. The **implementation** (`dsh/lib/plugin.js` and everything it imports) is stored per version under
  `<store>/<hash>/`, and the loader imports from there. The bundled copy is also copied there first and then loaded, because the host's module cache is keyed by path: when the plugin is reloaded in the same process,
  an unchanged path means the old code. The store sits next to node's socket: `~/.persona/runtime-packages/<package name>/`, with `current.json` recording which one is running.
- **Package**: a set of files and their hash (sorted by path, one `path\0sha256 of content\n` per file, then a sha256 over the whole). Which files belong to a package is listed twice, in Go
  (`Packages` in `server/internal/runtimepkg`) and JS (`PACKAGES` in `runtime-sdk/lib/package.js`), and the two must match
  (guarded by `TestPackagesMatchTheLoader`). Before building personad, run `go generate ./internal/runtimepkg`, which packs these files from `integrations/` and embeds them in the binary
  (gzipped, a directory several packages share stored once; a package is unpacked the first time node needs it)
  (the Dockerfile, `deploy.sh` and `publish.sh` all run it), so node always carries the plugin from the same build as itself. A build that skipped generation carries no package and offers nothing.
- **Flow**: hello carries `package: {name, hash}`; when node carries a package with the same name and a different hash, the reply includes `update: {name, hash, version}`.
  The SDK passes it to `onUpdate`; the loader fetches the files with `runtime.package`, checks paths, sizes and hashes, and writes them to `<store>/<hash>/` (written alongside and then renamed, so there's never a half-written version).
  It then **waits until the phone has no cards from this runtime** (`rt.busy()`, at most 2 hours), stops the old one, imports the new one and starts it. If the new one fails to start, it goes back to the old one
  and doesn't retry this hash in this process. When the new one connects to node, hello reports the new hash and node stops offering it. If the `current.json` receipt cannot be saved, the loader stops the new version
  before restarting the previous one and doesn't retry the failed hash in this process. Removing older stored versions is best-effort: a cleanup failure is logged and the new version stays running.
- **Which wins**: whatever node carries is what runs; versions aren't compared as newer/older (if node rolls back, the plugin rolls back too). One exception: if the bundled files were replaced by someone (a new copy installed by hand),
  their hash differs from what `current.json` records, so the hand-installed copy runs first, and node offers its own if different.
- **Security**: only runtimes connected over the local `runtime.sock` (same user as node) can get packages. Daemons can't (the link doesn't forward `package`,
  and `runtime.package` returns `unsupported`), and the DSH plugin in daemon mode doesn't accept updates by default either (`autoUpdate`); to change its version, restart it.
- **The first time**: an old plugin without a loader has to be reloaded once by hand in the host to switch to the loader; after that it's always "release → hub → node → plugin". Changing the loader itself also needs one manual reload, so it should be as thin as possible.
- **Another kind of host: one that reloads by itself (pi)**: pi reads extensions from disk on reload and doesn't cache modules, so it doesn't need the loader layer. The extension is installed from a fixed path in the store,
  `<store>/current/…` (`current` is a symlink to `<hash>`, replaced by rename via `installCurrent`). On an offer it fetches the package, installs it as the new `current`,
  and when idle asks the host to reload (pi uses a command the extension registers itself); the new version's host connects to node with the same process identity (`createRuntime`'s `instance`) and replaces the old one.
  A long-running standalone process (pi's and Antigravity's `companion.mjs`) exits once idle after installing and is brought back up by launchd / systemd (`--no-update` opts out). `selfPackage(import.meta.url, name)` tells a module which package it's running from
  and whether it's inside the store (if not, it doesn't update). Details in `docs/integrations/pi.md` ("Automatic updates") and "Antigravity CLI" below.
- An implementation module that can be swapped exports `api` (`LOADER_API`, currently 1) and `start(host)`, returning `{stop, busy, fetchPackage}` (the methods of the same name on `createRuntime`).
  `host` holds the loader's `context` (for DSH, `{ctx, config}`) plus the `package` and `onUpdate` to pass to `createRuntime`. To add such a runtime: register its files in both lists,
  and change the entry point the host loads to `createLoader`.

## Acceptance

A connected runtime must pass these (`integrations/runtime-sdk/e2e/echo.mjs` is the template: a real `personad node`, with the script acting as the hub;
`integrations/runtime-sdk/testing/node-harness.mjs` starts node, and `testing/fake-node.js` is for unit tests):

- [ ] After connecting, its launcher (if any) is in `launchers.list`, and its sessions appear in `sessions.list` with `agent`, `agent_label`, `space_kind`, `source_kind: "runtime"` and `capabilities`, and no `pane_id`
- [ ] Create from the phone, send messages (resending the same `client_msg_id` gets in only once), interrupt
- [ ] Content: the person's messages, thinking, replies, tool calls and results are all there; paging back from the end and catching up both work
- [ ] Every batch watch pushes continues the previous one (`from_seq` = the previous batch's `to_seq`)
- [ ] Streaming output
- [ ] Permissions / questions / plans reach the phone and answers get back to the Agent; when the phone hands it back, the local UI pops up as usual
- [ ] Terminal-type calls return `unsupported`; after the runtime exits, its sessions disappear from the list
- [ ] (With sub-Agents / background tasks) when the turn ends while they're still running, the session row's `subagents` / `background` list them; `<session>@<sub-Agent>` can page and watch; when they're done, the row says so
- [ ] (Installed inside another program, using the loader) when node carries another version, it swaps it in and keeps working, and after the host restarts it runs the swapped-in one (`integrations/dsh/e2e/update.mjs` is the template)

These checks are `testing/conformance.mjs`: the end-to-end tests for DSH (`integrations/dsh/e2e/run.mjs`, a real `dsh web`) and pi (`integrations/pi/e2e/run.mjs`,
`herdr.mjs`) are all "start the host + open a session + `conformance()` + a few checks of its own".

## Assistant (optional)

A runtime can be the backend of the "Persona" tab's assistant, the AI twin, Lark `/agent`, and scheduled tasks (all of which go through node's `assistant.send`), alongside Claude Code and Codex:
`createRuntime(…, {assistant: true})` (the driver must have `start` and `send`) declares `caps.assistant` in hello; its launcher in `launchers.list` then carries
`assistant: true`, the App's assistant launchers include it (`Launcher.runsAssistant`), and the hub accepts it as `assistant.launcher`. The assistant layer stays in node
(`server/internal/node/assistant_runtime.go`, which names no runtime in code): chat state, the live snapshot at the start of each message, permission mode, shared memory,
Lark tokens and gates. node drives it with calls the protocol already has:

| node sends | With (all optional fields; runtimes that don't declare `caps.assistant` never receive them) |
| --- | --- |
| `session.start {cwd, assistant: true, supervision_instructions?}` | `cwd` is the assistant directory (`~/.persona/assistant`, containing `AGENTS.md` / `CLAUDE.md` and `.agents/skills/`); no `prompt`. The runtime reports this `cwd` for the chat's whole life (`runtime.sessions`, its cards), not where a tool's `cd` left the shell: the hub places assistant and health chats by that directory exactly (#2368). `supervision_instructions` is sent only when nonempty and the runtime also declared `caps.supervision_policy` (#11173) |
| `session.send {session_id, text, images, client_msg_id, assistant: true, context, mode, memory_dir, supervision_instructions?}` | `context`: a `<persona-context>` block given to the model before the user's text (the live snapshot; the first message of a new chat also has memory and Lark notes), not shown in the chat, and left out of a message that is only a slash command (`/compact`, one line, no picture) so the runtime can run it: the snapshot goes with the next message, and after `/compact` in full; `mode`: Persona's permission mode (`default` / `acceptEdits` / `auto` / `plan` / `bypassPermissions`); `memory_dir`: the memory directory; `client_msg_id`: the id the message came with (the App's, a channel's), absent when it came without one: put it on the message's item, or leave the item without one, never another (`docs/protocol/protocol.md` "Matching pending messages"); `supervision_instructions`: sent only when the runtime declared `caps.supervision_policy` (always present, including explicit `""` to clear a prior role on demotion) |
| `session.interrupt` | Stop on the phone; closing the assistant chat = stopping the running turn, the chat stays in the runtime |

For sessions it starts, the node restores persisted ownership and launcher metadata before publishing runtime rows.
The original assistant or health directory identifies the conversation after rediscovery and reconnects. Runtime
status, model, permissions and capabilities continue to describe the running session.

A runtime that declares `caps.assistant` must:

1. For **assistant chats** (started with `assistant: true`, or whose directory is the assistant directory), always send permissions and questions to the phone first (when there's a hub), regardless of whether the local UI is open.
2. Attach the tools from `mcp.tools {assistant: true}` to the model (the phone's and mail's, plus assistant-only ones like `ask_user`, `schedule_followup`… and the tools declared by extension command gates);
   assistant-only tools go only to assistant chats, called as `mcp.call {…, assistant: true}`. node treats a call as the assistant's only when "this runtime declared the assistant, the session is its, and it's an assistant chat";
   otherwise as a regular tool (with no assistant-only tools).
   A runtime that gives an assistant chat Persona's MCP server instead (`personad mcp` with `PERSONA_ASSISTANT=1` and the chat's `PERSONA_SESSION_ID`, as the Antigravity runtime does)
   must start that server in its own process tree: node takes the server's word only from a descendant of the process connected to its runtime socket with `caps.assistant` (`node/clicall.go` `mcpAssistantOf`),
   and only for a session id of the runtime's own shape (not empty, not a Claude or Codex uuid) that, once the runtime lists it, is this runtime's assistant chat.
3. Lark always goes through tools declared by extension command gates (`proto.CommandGate.Tool`): node first passes the Lark gate (reads allowed, writes others will see confirmed on the phone, configured rules deny), then runs
   the skill's script on node; credentials live only in node's proxy, never in the runtime. The tools carry `shell` (the command name and the skill script's path); when the runtime sees them in the assistant chat's shell
   it blocks them and says which tool to use (DSH's `onPreExecute`).
4. Give `context` to the model before the user's text and don't show it in the chat (a `<persona-context>` block without attributes; context quoted from the phone has attributes and is shown as usual).
5. Set its own permissions per `mode` (plan = read-only); changes inside `memory_dir` aren't asked about outside plan mode.
6. **Optional supervision policy channel (`supervisionPolicy: true` → `caps.supervision_policy`)** (#11173): only declare this if the runtime can apply `supervision_instructions` on an authoritative channel separate from user message text, persisting across compaction and isolated per session (never rewriting shared `~/.persona/assistant/AGENTS.md` and never leaking policy into concurrent ordinary chats). The runtime must refuse a policy change while the session is busy with `code: "busy"`, clear its per-session policy when `supervision_instructions` is `""` or the session closes, and fail closed with `code: "assistant_supervision_runtime"` (never sending the prompt) if the policy cannot be applied. Runtimes that do not declare `caps.supervision_policy` never receive `supervision_instructions` and keep the existing refusal boundary for supervision deliveries (`docs/assistant/assistant.md` "Role-scoped supervision instructions (#8251)").

The assistant launcher's entry in `launchers` may also carry `assistant_for`: the name of an Agent launcher on the same computer that runs the same agent with the same
account but can't run the assistant itself (a Paseo provider; the Grok companion's `--assistant-for grok`, `integrations/grok/README.md`). The person setting the runtime up
declares it; nothing guesses it from names. `launchers.list` passes it on only for the launcher that runs the assistant, and the main chat's picker then points that
launcher at this one (`unavailable[].instead`, [model-config.md](../assistant/model-config.md) "A conversation's own model"). Nothing is ever run on one in place of the other.

Model and reasoning effort are up to the runtime (DSH picks them in its Web GUI); Persona's model config doesn't pick a specific model for such launchers. DSH's approach is in `docs/integrations/dsh.md` ("Assistant"),
end to end in `integrations/dsh/e2e/assistant.mjs`.

## Existing runtimes

| Runtime | Where | Doc |
| --- | --- | --- |
| DSH (`dsh`, configurable) | `integrations/dsh`: a Cordis plugin for `dsh web`; companion process `companion.mjs` (`dsh-files`) for when DSH isn't running | `docs/integrations/dsh.md` |
| pi (`pi-<pid>`, `pi-web`, `pi-files`) | `integrations/pi`: pi extension (one connection per pi process) + pi-web companion process | `docs/integrations/pi.md` |
| omp (`omp-<pid>`) | `integrations/omp`: the pi host under omp's identity (agent and name `omp`); no companion and no auto-update | `docs/integrations/omp.md` |
| opencode (`opencode-<pid>`) | `integrations/opencode`: opencode plugin (one connection per opencode server process; list and chats read from opencode.db, live updates over the event stream, control through the plugin API) | `docs/integrations/opencode.md` |
| Grok CLI (`grok-chat`) | `integrations/grok/companion.mjs`: optional standalone ACP companion with assistant tools, permission hooks, native renaming and restart recovery; also lists conversations started in a terminal (read-only while the terminal holds them) | `integrations/grok/README.md` |
| Antigravity CLI (`antigravity`) | `integrations/antigravity/companion.mjs`: companion process; reads CLI transcripts and runs terminal-less Agent and assistant sessions through `agy acp` | "Antigravity CLI" below |
| echo (example) | `integrations/runtime-sdk/examples/echo-runtime.mjs` (with `--listen <port>` it's a daemon) | "Minimal example" in this doc, `docs/integrations/daemons.md` |

### Antigravity Desktop (macOS, experimental)

With Antigravity Desktop open and the Persona node running, start the desktop companion from the checkout:

```sh
node integrations/antigravity/desktop.mjs
```

It discovers the local desktop hub through the current user's private descriptor under
`~/.gemini/antigravity/daemon/`, lists existing conversations, watches exported transcript items,
and forwards text replies and interrupts to the selected Cascade. Start each conversation with a message in the desktop app so replies can reuse its model and tool configuration.
Handle approvals there as well. The desktop and CLI companions can run together. Override the desktop data directory
with `--desktop-dir` or the Persona runtime socket with `--socket`. The desktop RPC is private and version dependent.

### Antigravity CLI

The session's `last_text` preview contains assistant response text after the latest explicit user input. A new user
input clears the previous reply preview, including input with no visible text. Thinking and tool records leave it empty
until the assistant writes response text. This keeps user-authored questions out of awaiting-reply detection while
preserving genuine assistant questions. User messages and tool records remain available in the conversation items.

Native model errors can be omitted from the JSONL export even when ACP reports a completed prompt. When an idle
summary has unexported steps, the companion reads at most the final eight native steps with `GetCascadeTrajectorySteps`.
The result is cached against the summary and exported branch. Discovery opens the native service through read-only
session listing. An omitted terminal error uses the shared assistant-text `API Error:` representation for the existing error card
and populates the session's `turn_error`
fields, including a subscription reset timestamp supplied in native quota metadata. New input and rewinds invalidate
that result. Rejected ACP prompts also publish their failure through the session's error state.
The history token includes the error format so connected clients refresh items cached before this representation.

The CLI companion runs the **Antigravity CLI (`agy`)**. When `personad node`
runs (`--attach` or `--stdio`), `agy` is installed, Node.js 22.5+ (`node:sqlite`) is available, and no external `agy`
runtime is connected on `runtime.sock`, the node automatically writes out the embedded `persona-antigravity-runtime`
package and supervises `antigravity/companion.mjs` (passing `--no-update --parent-stdin`), yielding whenever an external
`agy` runtime connects (`server/internal/node/agybridge.go`). You can also start
`node integrations/antigravity/companion.mjs` manually from the repository root, or install into the runtime package store
with `node integrations/antigravity/install.mjs` and supervise
`node ~/.persona/runtime-packages/persona-antigravity-runtime/current/antigravity/companion.mjs` under launchd / systemd.
When run from that store without `--no-update`, the companion reports `persona-antigravity-runtime` in `runtime.hello`,
installs the version `personad` carries as the new `current` ("Auto-update (optional)" above), waits for active ACP turns,
availability probes, rewinds and phone permission cards to finish, then stops `agy acp` and exits so its supervisor restarts
it on the new code; direct launches outside the store leave self-update off. The companion connects to the node's runtime
socket, lists CLI transcripts, and uses `agy acp` for terminal-less session creation, sending and interruption. It can also
serve as an assistant backend through the optional assistant contract above. It requires an installed, authenticated `agy`
with ACP support; `--agy-bin`, `--agy-dir`, `--socket`, `--no-update` and `--parent-stdin` override the executable, CLI data
directory, node socket, automatic updates and parent-pipe shutdown.
Models come from authenticated `agy models` discovery and ACP session metadata. The companion refreshes the catalog every
five minutes, and the picker's refresh requests a fresh catalog through optional `models({launcher, refresh})`
(`caps.models`, `launchers.models`). Discovery never sends a prompt. The native settings file determines the advertised
default. New sessions and availability checks explicitly apply that model through ACP, since ACP may ignore the
settings file when choosing its initial model. An explicit caller model takes precedence, and resumed sessions retain
their native selection. An unavailable catalog returns the native error instead of advertising a built-in model list.
A CLI whose stored sign-in expired long ago does not fail `agy models`: it hangs. The companion kills it after 25 seconds
and reports that the sign-in may have expired and that running `agy` once on the computer signs in again; each
discovery in a row that times out doubles the wait before the next one, up to an hour, and one that answers restores
the five-minute pace (#11106). Whether a call that sends no prompt can refresh such a sign-in is not established: a
`agy models` run refreshed a token close to its expiry, but one expired for months stayed hung until a prompt in print
mode signed in silently.
One `agy acp` process can keep the model list it had when the account's plan lapsed or the CLI signed out (#10368), so
a session's model list replaces the catalog only when its ACP process started after the latest successful discovery
began. When ACP rejects a model as unknown and a fresh discovery lists it, the companion replaces the ACP process and
runs the start, model change or availability check once more. Every runtime call, turn and native read that uses ACP
holds a lease for its whole run, and the process is replaced only when no lease other than the caller's, ACP request
or permission card is open. That last check and the replacement run without an await between them, and calls that
arrive during a replacement wait for the old process to exit and start on the new one. A replacement that rejects the
same model is not replaced again while the catalog stays the same. A successful discovery after one that found the CLI
signed out replaces the ACP process the same way: at once when nothing uses it, otherwise before the next start, send,
model change or check that finds it idle.

Idle sessions can switch model, thinking level and permission mode through ACP. Native configuration updates determine
the displayed model and effort. Each accepted field is published even if another field fails, and failures reach the caller.
`auto` keeps native safe-operation approval, while Persona's explicit full bypass answers permission requests automatically.
External session models stay unspecified until native metadata is available. Session discovery does not resume them.

Availability checks use optional `check({launcher, cwd, prompt})` (`caps.check`) through the companion's authenticated
ACP client. They run the existing one-word probe, cancel tool permission requests, and close their temporary session
on success, failure or timeout. Completed probe workspaces stay out of the user inventory after companion restarts.
The runtime bounds native calls to a 75-second budget, followed by bounded session cleanup. Older runtimes keep the
node's shell check. Shell and managed launchers keep their own execution context.

A chat gets Persona's MCP server once, named `persona`, through ACP `mcpServers`. `agy` defers the tools over its
declaration budget, largest first, and has no `call_mcp_tool` for servers passed through ACP, so an assistant chat's
server runs behind `integrations/antigravity/mcp-proxy.mjs`, which adds a small `persona_call` tool that calls any
Persona tool by name (#7333). The proxy declares only the tools larger than `persona_call`, so it is always the smallest
declared and never the one deferred, however many small tools connectors add (sizes are what agy declares of a tool: its
name, description and input schema). The others stay callable through it: `persona_call` with no name lists every tool,
and those left out with their description and arguments (#10756).

Implementation: `integrations/antigravity/lib/runtime.js`, `integrations/antigravity/lib/acp.js` and `integrations/antigravity/lib/policy.js`.

For assistant supervision (`caps.supervision_policy`, #11173), the companion stores each supervised session's policy in an isolated directory `<agyDir>/persona-supervision/<rawId>` outside `~/.persona/assistant` and attaches it to that session alone through ACP `additionalDirectories` (verified against `initialize`'s `agentCapabilities.sessionCapabilities.additionalDirectories`). `agy` loads both `AGENTS.md` and `GEMINI.md` from `additionalDirectories` directly into `<user_rules>` on every turn (24,000-byte per-file cap), so policies above 24,000 bytes split cleanly across `AGENTS.md` and `GEMINI.md` up to 48,000 bytes, while shared `~/.persona/assistant/AGENTS.md` and concurrent ordinary chats stay untouched. Policy updates and demotions (`""`) synchronize atomically and reload the session via `session/resume`; busy role changes are refused with `busy`; missing ACP `additionalDirectories` support, unwritable directories or oversized policies fail closed with `assistant_supervision_runtime` before any prompt runs; and `session.close` removes the session's policy directory.

Live text belongs to the ACP prompt currently executing. Queued prompts acquire the stream when they start, with an
empty boundary. A tool call ends the current text message, so the next message replaces its words in the live slot.
Each prompt clears its live text on completion, including when another prompt is queued.

While a conversation is watched, the companion polls its native export every 400 ms. Persona-owned ACP turns reuse
their existing poll. Unwatch, disconnection and shutdown release the watched poll. Native records that update an
existing step carry a stable content revision in the history token, so a response completed at the same sequence
replaces retained content. Ordinary appends preserve that revision and continue through incremental item batches.

Session inventory, watch cursors, item pages and live item pushes read `transcript_full.jsonl` when available.
Antigravity declares `caps.rewind` for idle Agent conversations. The selected message, duplicate occurrence from the
end, and following user-input count are checked against `GetCascadeTrajectory`. The owned ACP process's private log
identifies its loopback native service. `RevertToCascadeStep` receives `conversationOnly: true` and the preceding step
index, including `-1` for an empty conversation. Native refusal propagates to the caller. The bridge uses the current
native summary and replacement step indices to discard the abandoned export tail, then publishes a changed history
token. This survives companion restarts without modifying native transcript files.

The compact `transcript.jsonl` export can contain native `<truncated N bytes>` placeholders and can be ahead of
the complete export. Use one source for sequences and content so a watch cannot advance past unread complete items.
Older histories fall back to the compact export when the complete file is unavailable. Persona applies the SDK's
normal page limits to complete native records and serves larger items through the full-item endpoint. The history
scope includes `:full` to invalidate previously cached compact pages. Parsed files are cached by file identity, size
and nanosecond modification/change times, bounded to 16 files and 64 MiB of source data. Files modified within the
last second bypass the cache because rapid same-size writes can share a filesystem timestamp tick.

### Runtime token usage

For an agent without local token records, call `rt.usage({session_id, ts, model, request_id?, input, output, cache_read, cache_write, reasoning?})` once per completed model request. `ts` is an RFC3339 timestamp; counts are nonnegative safe integers. Input excludes cache reads and writes; output already includes reasoning. Reasoning is an optional part of output, never added again. All-zero reports are ignored.

The node advertises `usage` in hello features. The SDK returns false while disconnected, without a hub or with an older node; it does not queue or replay reports. A true result means the event was written, not acknowledged or durably saved. Reports can be lost during a reconnect, node restart or storage failure; there is no backfill. Upgrade the Hub storage support before using the new node feature.

Only currently listed, owned sessions count. The row's agent must match the runtime's registered default agent or an advertised launcher agent. The node supplies the recorded launcher name. Claude Code, Codex, traex, pi, omp and opencode remain metered from their local records, including when hosted in Paseo. For other agents, a nonempty request ID deduplicates within one runtime connection and session for 30 minutes. Without an ID each report counts. The cache retains at most 65,536 request IDs and evicts its oldest entry when full. Reports outside the last 26 hours of minute buckets, more than a minute in the future, or with any token count over 1,000,000,000 are rejected. The file meter's 10-second tick sends the buffered per-minute totals. The manager restores persisted runtime minute buckets at startup.

Daemon links allow the same event and prefix its session ID as usual. Runtime usage goes to the same token totals and agent buckets as file usage. It is added, rather than replacing a minute's total, and full file scans preserve these unreconstructable buckets.

### Shared runtime behavior

Antigravity item identities include native creation time and the replacement branch's generation (#9410). Native
step indices remain pagination sequences, not globally reusable item or tool-call IDs: after rewind a repeated input
must not inherit the abandoned branch's pending-send binding or reply cache. Retained prefix records and same-step
completion updates keep their identities. The history scope includes `:step-ids-v2` to discard older numeric-only
item caches; full-item lookup resolves only the current branch's exact ID. Identity metadata is computed with parsed
records, kept outside native JSON and reused by list, push, page and full-item reads.

When two runtimes list the same session (pi's companion lists it from disk while pi-web or a pi terminal is running it), node makes the one running it responsible (ones listing it as `offline` go after),
and on a tie the newer connection wins. Listing a session as `offline` tells node "I can open it, but it isn't running here".

Paging uses the SDK's `page(items, params, {history, toSeq})`: the runtime just needs its items sorted by seq.

A runtime that depends on a new node capability uses `createRuntime(…, {requires: ['prefer_running']})`: with an old node that doesn't support it, it doesn't connect (logs one line),
and retries when the socket file is replaced (the resident node restarted, e.g. after an upgrade) or after 10 minutes; `rt.nodeHas(feature)` checks the current node. Sessions started via node that run elsewhere (pi's companion creating one in pi-web) are reported as `offline`,
and `session.start` returns the row of the runtime actually running it.

**An Agent in a host paired with a runtime plugin** (pi and its family, omp, opencode): panes, state and terminal belong to the host (herdr), chat and control to the runtime. When the runtime's session row has the same `session_id` as the session id the host reports,
node merges the two rows into one (`mergePiRow`, `mergeOpencodeRow`: title, last line, model, context usage and revision come from the runtime, state from the host, and the runtime saying `blocked` counts as "Needs you").
Without the runtime, the row is terminal-only: the chat is a single note and sending is refused (`terminalOnly`, `server/internal/node/opencode.go`). Agents herdr reports that Persona has no way to read are listed as terminal-only rows too.
Don't put host pane information in the runtime id: panes are matched on node's side, and `pane_id` in a runtime row is only an optional hint.

Platforms built on DSH that don't run `dsh web` (multi-tenant, running DSH in containers) implement the same protocol on the platform side and connect as a daemon (`docs/integrations/daemons.md`),
reusing the SDK and `integrations/dsh/lib/map.js` directly, with no code added on the Persona side.
