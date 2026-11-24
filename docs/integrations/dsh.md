# DSH (DeepSeek Harness)

DSH sessions appear in "Agents": list and state, chat content (text, reasoning, tool calls and results, images), streaming output, sending messages, interruption, session creation,
closing (archiving) from the phone, and answering DSH tool authorizations, questions, and plan approvals on phone / Watch. DSH Agents can use phone (`phone_*`) and mail tools.
When DSH is not running, a companion process lists its sessions from disk; sending a message to one wakes DSH to continue. Back at the computer, the user continues the same session in DSH's Web GUI.

Supported since 2026-10-31 (issue #169). DSH is the first integration following the "runtime" specification (`docs/integrations/runtimes.md`): the node uses generic `server/internal/node/runtimes.go`,
and DSH uses the `integrations/dsh/` plugin built on generic `integrations/runtime-sdk/`; only DSH-specific pieces remain in the plugin (session listing, pagination, event mapping, and two response chains).
2026-11-02 added phone tools, closing, and listing while DSH is not running; the assistant and AI twin can also run on DSH (issue #282, see "Assistant" below).

## Why not herdr

DSH has no TUI, emits no OSC 7501, compresses session logs with zstd by default, and its Claude Code-compatible hooks cannot answer questions or plan approvals.
Thus "herdr pane + transcript + hooks" cannot integrate it, and it should not be a host either (the host determines where terminals open, `docs/integrations/engines.md`). It is a **runtime**:
it manages its own sessions without a terminal, connects to the node's `~/.persona/runtime.sock`, reports sessions, pushes content, and asks the phone (specification `docs/integrations/runtimes.md`, protocol `docs/protocol/protocol.md`, "Runtime ↔ node").
On the DSH side, a plugin installed into `dsh web` uses the same services as the Web GUI, without changing a line of DSH core.

```
 App ── hub ── node ──┬── herdr (Claude / Codex / a custom Codex fork)
                      └── ~/.persona/runtime.sock ◄── persona-dsh-runtime plugin (inside the dsh web process)
                                                    ├─ sessionController: list / page / prompt / cancel / create / rename
                                                    ├─ session/event, agent/assistant-stream, api-session/*
                                                    ├─ approval/request, user-questions/request response chains
                                                    └─ tools.register: hub tools (phone_*, mail_*) for DSH Agents
                                        ◄── persona DSH companion process companion.mjs (runtime `dsh-files`, lists disk sessions when DSH is not running)
```

## Installation

Install on the computer running `dsh web`, which must appear in the hub's machine list (the hub starts its node over ssh; herdr is unnecessary). The plugin and node must run as the same system user
(`runtime.sock` is 0600).

- **Install into a profile** (long-term use): install the `integrations/dsh` bundle in DSH (Web GUI plugin management, or ask the DSH Agent to use `plugin_manager`
  `install_bundle`, with target set to this directory's absolute path). The bundle includes `cordis.patch.yml`, which inserts the `persona-dsh-runtime` entry on installation.
  The plugin uses the repository's `integrations/runtime-sdk/` via relative paths, so install from a Persona repository checkout (local directory installs are symlinks, not copies); do not copy `integrations/dsh` alone.
- **Subsequent updates are automatic**: `index.js` is only a loader; node supplies the plugin code ("Automatic updates" below). Reload once in DSH after installation
  (or upgrading from an old version without a loader); later hub updates and following node updates switch the plugin automatically, without clicking in DSH again.
- **Try without installing**: `dsh --profile web --patch <file>`, with an insertion in that file; set `name` to the absolute path of `index.js`:

  ```yaml
  - insert:
      - id: persona-dsh-runtime
        name: /<Persona repository>/integrations/dsh/index.js
        config:
          interactions: auto
  ```

The plugin works only in the web profile (requires `sessionController`); without a listening node it keeps retrying (once per second while the socket file exists, at most every 30 seconds otherwise), without affecting DSH.
The persistent node's `runtime.sock` is independent of its hub connection: the plugin stays connected during hub reconnection, and questions go directly to the Web GUI while the hub is absent.

For a second DSH on the same computer (another profile or `DSH_HOME`), configure distinct `runtime`, `name`, and `launcher`: node rejects two processes with the same runtime id
(the later connection fails with `runtime_conflict` in the log, `docs/protocol/protocol.md`, "Runtime ↔ node"); if both launchers are named `dsh`, only the first connected one remains.

### When DSH is not running (companion process)

The plugin lives inside `dsh web`: when DSH stops, its sessions disappear from Persona. To keep them visible, run a persistent companion on the same computer (kept alive by launchd / systemd, like pi):

```
node <Persona repository>/integrations/dsh/companion.mjs --dsh <DSH installation directory or built checkout> [--home <DSH_HOME>] [--wake '<command to start DSH>']
```

- Use DSH's own session storage (`@deepseek-ai/dsh-session-persistence-jsonl`, loaded from the DSH given by `--dsh`) **read-only** to list recent sessions in `$DSH_HOME/sessions`
  (the same 7-day / 80-session limits as the plugin, excluding subagents and archived sessions), taking title and last line from DSH's projection cache and marking all `offline`. Pagination reads session logs with the same mapping (`map.js`)
  and `history` as the plugin, so the phone shows the same chat before and after takeover. It writes nothing in `DSH_HOME` (`storages/*.json` lacks cross-process locking; a second writer would corrupt it).
- While connected, the plugin also lists those sessions; node prefers the running one (`prefer_running`), relegating companion rows to the fallback.
- **Send to an offline session**: the companion runs `--wake` once (the configured way to start DSH on this computer, such as `launchctl kickstart gui/<uid>/<service-name>` or `systemctl --user start <service>`;
  the command should return quickly and is not repeated within a minute), then **hands the message back** to node (`handover`). Node waits for the plugin to list that session as running, or asks the plugin whether it has it, then hands the message to the plugin to send,
  waiting up to 45 seconds. Without `--wake`, it still hands back: if DSH is running, delivery succeeds; otherwise it waits until timeout and reports "Timed out waiting for takeover," without sending the message.
- The companion cannot create, interrupt, or rename sessions (the plugin handles those once DSH is running), and images are available only after DSH starts. Requires node support for `prefer_running` and `handover`; it does not connect to old nodes.

### Configuration

| Field | Default | Description |
| --- | --- | --- |
| `interactions` | `auto` | Who answers authorizations and questions: `phone` tries the phone first; `browser` never uses the phone; `both` offers each request to the phone and Web GUI at once; `auto` is described below |
| `socket` | `~/.persona/runtime.sock` | Node runtime socket; environment variable `PERSONA_RUNTIME_SOCKET` also works |
| `listDays` | `7` | List sessions active within this many days (running or waiting-for-user sessions are always listed) |
| `maxSessions` | `80` | Maximum count; sessions messaged or created from the phone (most recent 50) bypass both limits and stay listed |
| `runtime` / `name` / `launcher` | `dsh` / `DSH` / `dsh` | This DSH's runtime id, display name, and launcher name in Persona. A second DSH on the same computer needs distinct values; sessions with a changed id still display as DSH Agents (`agent: "dsh"`) |
| `phoneTools` | `true` | Expose hub tools (`phone_*`, `mail_*`) to DSH Agents; `false` disables them |
| `assistant` | `true` | Allow the DSH launcher to run Persona's assistant ("Assistant" below); `false` disables this |
| `presets` | See "Assistant" | Maps Persona permission modes in assistant chats to DSH permission presets, e.g. `{default: workspace-write, plan: read-only}` |
| `autoUpdate` | `true` (`false` as a daemon) | Accept plugin versions supplied by node ("Automatic updates") |
| `listen` | Unset | Daemon mode (`docs/integrations/daemons.md`): `{port, host?, path?, token \| token_file}`; instead of connecting to `socket`, expose a WebSocket endpoint for node to connect (default `127.0.0.1`, path `/persona/runtime`). Use when DSH runs under another user, in a container, or on another machine; node's `daemons.json` specifies the same address and token, with names and launchers determined there. Daemons have no phone tools and cannot run the assistant |

### Automatic updates

The plugin has two layers (generic approach in `docs/integrations/runtimes.md`, "Auto-update"): DSH loads `index.js`, a stable loader; each version of plugin code (`lib/plugin.js`, `lib/dsh.js`… and
`runtime-sdk`) lives in `~/.persona/runtime-packages/persona-dsh-runtime/<hash>/`, imported by the loader. personad embeds these files in its binary at build time.
The plugin reports its hash when connecting to node; if node ships a different hash, it offers the package. The plugin fetches and verifies it, waits until it has no cards on the phone, stops the old version, and loads the new one. DSH sessions and Web GUI are unaffected
(the plugin is only an observer; the phone list pauses for a few seconds during switching). This bypasses DSH plugin management and hot reload entirely: symlinked plugins are outside the hot-reload watch directory, so file changes do not take effect;
plugin management inside phone-driven sessions also reports `HMR transactions cannot be nested` (when the plugin starts inside a plugin-management transaction, that transaction context follows its connection; a DSH problem).

- Running version: node log `runtime dsh <version> #N connected`, plugin logs `persona: persona-dsh-runtime <hash> (shipped / from the node)` and `switched to …`;
  `current.json` records the node-supplied version.
- A manually installed new version (changed checkout files) has a hash differing from `current.json` and runs first on the next load; if node supplies another version, it switches again.
- To disable automatic updates: `autoUpdate: false`. Daemon mode (`listen`) defaults to no updates; restart DSH to switch versions.

## Mapping

| Persona | DSH |
| --- | --- |
| Session list | `sessionController.list`; exclude archived (`workspaceRegistry.archivedSessionIds`) and empty, unstarted sessions (except those created on the phone). Subagent sessions are not separate rows; attach them to the parent's `subagents` (see "Subagents and background tasks"). Title uses the `title` projection, falling back to directory name; space = directory |
| State | Pending authorization / question (held by the plugin, or still open in the `userQuestions` projection) → `blocked`; running → `working`; otherwise `done` (hub distinguishes "Unread / Seen") |
| Chat content | `sessionController.page` (does not activate cold sessions). For `user/message`, take only `source.kind = user` (hide DSH-injected time, skills, and instruction context); `assistant/message` `reasoning` → reasoning, `text` → reply, `tool-call` → tool call; `tool/result` → result (do not repeat results restored by compaction); interrupted / error / output-limit turns and context compaction → notices. seq = DSH seq + 1, `history` = `dsh:<session id>` |
| Tool names | `bash` → `Bash`, `read` → `Read`, `edit` → `Edit`, `write` → `Write`, likewise for `grep`, `glob`, `web_fetch`, `web_search`, `todo_write`, `ask_user_question`; `subagent` / `subagent_fork` → `Agent` (arguments `{description, prompt}`, rendered as a subtask in the app, tap to inspect the launched subagent). Parameter names already match Claude (`command`, `file_path`, `old_string`…), so the app renders commands, diffs, and files normally. Other tools display their original names |
| Streaming | `text-delta` from `agent/assistant-stream`; send the first chunk immediately, then at most once every 120 ms (whole text) |
| Send message | `sessionController.prompt`: `requestId` = app `client_msg_id` (DSH deduplicates, so retries do not duplicate); `queue` when idle (new turn), `steer` while working (insert into the current turn, like Claude queued input). Images carry base64 directly |
| Interrupt | `sessionController.cancel` |
| Create | Select launcher `dsh` when creating in the app (available only while DSH is connected). Resolve the selected directory with `workspaceRegistry.resolveByPath`: an exact canonical-path match uses `sessionController.create {workspaceId}` so DSH attaches the session; otherwise use `{cwd}`. The two fields are mutually exclusive. A missing directory (`ENOENT`) or a profile without workspace lookup retains cwd-only creation; DSH creates missing directories. Other lookup failures stop before creating a session. When a model is chosen, DSH's configured model groups (read from `sessionController.modelCatalog`, `{groups: [{id, name, models}]}`, with `modelRegistry` / `models` / `listModels` as fallbacks) are advertised as launchers/models, and the plugin applies it via `sessionController.selectModel` after creation and before the first prompt; unknown models refuse creation. |
| Rename | Session name → `sessionController.rename`; ignore tab names |
| Close | `workspaceRegistry.archiveSession(id, {stopActivity: true})`: same as Web GUI archiving; stop the current turn, subagents, background tasks, and scheduled tasks together; then omit from the list. Can be unarchived in DSH |
| Phone tools | Hub tools (`mcp.tools` forwarded by node) register in DSH as `mcp__persona__<name>`; calls go through node `mcp.call` to the hub with the calling session attached. Result text becomes tool output; error results become tool failures |
| Agent supervision | Regular Agent sessions get their own Persona tools (such as `report_to_supervisor`) even when assistant support is enabled. At step preparation, DSH checks the calling session with the node and offers `task_plan` and `task_review` only to an active task leader. |

Markdown in assistant text uses the session computer's existing file machinery for local destinations: absolute paths and paths relative to the session cwd open in the file viewer, and valid local images render in the reply. Missing, denied, and unreadable files show the file API's reason; image retries happen only when requested. HTTP(S) destinations keep their existing behavior. These reads use the machine-file API and its normal path guards, not a DSH-specific file-read RPC.

## Authorization, questions, and plan approval

The plugin goes first (`prepend`) on DSH's two response chains, `approval/request` and `user-questions/request`. When its turn comes:

- **Whether to use the phone** (`interactions: auto`): a message sent from the phone to this session within the last 6 hours, or a phone-created session → phone first; no browser connected to DSH
  (`typertGateway.hasLiveClient()` is false) → phone first; otherwise pass to the next responder (Web GUI), with no card visible on the phone.
- **Authorization**: the card carries tool name, arguments (from this step's `tool-call`), and command to run (`summary`); DSH's reason goes in `reason`. Node labels it
  `agent: "dsh"`, `agent_label: "DSH"`; the app renders it as an "other Agent" card ("DSH needs authorization," Allow / Deny, like Codex cards),
  with "Don't ask again for <tool> in this session (main Agent and subagents)" (`always_label`; subagent cards attach to the parent, sharing authorization). DSH itself supports only "Allow once,"
  so the plugin declares `caps.keep_always` in hello and the hub remembers it: node sets `always_keys: ["runtime:dsh:tool:<tool>"]` (ignoring runtime-supplied keys). Tapping Always stores it in `assistant_always` for this chat;
  subsequent cards for this chat and tool are directly allowed by the hub, while DSH still receives `allowed-once` (#3974). Scope is only this chat + tool; not offered in health space or on cards requiring fields.
  Approve → `allowed-once`, deny → `rejected` (even Always + deny rejects only once, without remembering). If a denial includes text,
  deny first, then steer that text into the active turn (DSH authorizations have no reason field; Codex works the same way), so the Agent can read why.
- **Questions**: `ask_user_question` questions, options, and multi-select go unchanged to the phone; match answers by option text back to DSH `{id, selected, custom}`, with non-option text as `custom`.
- **Plan approval**: questions with `intent.kind = plan-review` render as plan cards, with plan body in `detail`; approve selects its `approve` option, send back selects the other option, revision comments go in `custom`.
- **Both phone and Web GUI** (`interactions: both`): while the hub is connected, start `rt.ask()` and `next()` together for every session. The first valid answer wins; a phone answer aborts the Web GUI branch, and a Web GUI answer withdraws the phone card. An unavailable downstream responder leaves the phone card active. Request cancellation withdraws both branches, and timed questions retain the late-answer path.
- **Return to computer / expiry / no hub** (`auto` and `phone`): call `next()`, and the Web GUI prompts normally. While the phone holds a card in these modes, the Web GUI cannot see it (the response chain is a chain, not a broadcast),
  the same trade-off as Claude hooks intercepting dialogs so they do not appear in the computer TUI.
- **Timed questions**: `ask_user_question` waits only 45 seconds by default. At timeout, DSH first reports "Not answered yet" to the model, without withdrawing the phone card; a later answer goes through `userQuestions.answer` (late answer).
- **Node restart, hub link drop**: interaction ids derive from session + tool call; after connecting to the next node (or once its hub is back), the plugin asks again with the same id (waiting at most 2 minutes from the drop), preserving the same phone card: the hub keeps a card under the runtime's own id through the gap (#8128).

## Phone tools

While connected to the hub (after hello, when `runtime.hub` announces the hub's return, then every 5 minutes), the plugin requests hub tools from node, re-registering only when changed so the tool list seen by DSH Agents
does not change needlessly. When the hub is absent, retain the previous list; calls report that the hub is disconnected. Tools register globally in DSH and are available to all its Agents (including subagents). DSH calls do not use DSH's own authorization;
operations acting on the user's behalf (creating events, sending mail) are confirmed by the hub on the phone, just as when Claude uses these tools. Images in tool results are not passed to DSH Agents (the result explains this).

## Assistant

The assistant in the "Persona" tab, AI twin, Lark `/agent`, and scheduled tasks can all run on DSH: select its launcher `dsh` under Settings › Assistant (or the top bar, scheduled task, or model configuration)
(available only while DSH is connected, with `assistant: true` in `launchers.list`). Integration follows the generic runtime assistant contract (`docs/integrations/runtimes.md`, "Assistant"); node uses
`server/internal/node/assistant_runtime.go`, with no DSH-specific code. DSH uses the plugin's assistant mode (`config.assistant`, enabled by default).

The assistant computer (hub computer, or `assistant.machine`) must run persistent `dsh web --no-open` (launchd / systemd, Node 22+ required) with the plugin installed.

| Capability | DSH |
| --- | --- |
| Streaming output | Already in the plugin: `agent/assistant-stream` → `session.delta` |
| Continue / after restart | A chat is a DSH session (`session-<uuid>`), persisted by DSH; node restarts do not affect it (recognized as an assistant session by directory), and the next message after a DSH restart automatically resumes it |
| Stop | `session.interrupt` → `sessionController.cancel`; closing a chat stops only the active turn, leaving the session in DSH |
| Interject during a reply | Already in the plugin: steer into the active turn |
| Persona MCP tools | Hub assistant tools (`ask_user`, `notify_user`, `schedule_*`, `agents_*`, `kb_*`…) register as `mcp__persona__<name>`, only for assistant chats (`tools.register` `when`); calls carry assistant identity through node, which checks that the session belongs to DSH and is an assistant chat |
| ask_user | Same tool; session id is already known |
| DSH questions / plans | Already in the plugin: question chain, including plan cards and late answers; assistant chats always use phone first |
| Command / file-edit approval | `approval/request` chain, always phone first for assistant chats (DSH asks only for cases such as leaving the sandbox) |
| Lark write confirmation | Tool declared by the extension command gate (`proto.CommandGate.Tool`, name chosen by the extension; node `gateTools` lists it only if the skill is on this machine): arguments are words after the command. Node first checks the gate (`larkCheck`: allow reads, confirm writes on the phone, deny under configured rules), then runs the skill script on node; credentials stay only on node (on the node gate proxy since #1317). Credentials never enter DSH; direct shell commands cannot get them or bypass the node gate proxy. Since #1571, node no longer embeds this tool; the Lark extension declares it (`docs/extensions/lark-agent.md`) |
| Permission mode | Sent with every message; the plugin sets DSH permission presets (`permissionPresets.set`): Default / Auto-accept edits / Auto → `workspace-write`, Plan → `read-only`, Allow all → `danger-full-access` (the Lark gate still applies: it runs on node, independent of DSH authorization); customizable through `config.presets` |
| Reasoning effort, model | Choose in DSH (Web GUI or DSH default model); Persona model configuration does not select a specific model for `dsh` |
| Images | Already in the plugin: base64 image blocks |
| Instructions | DSH reads the bounded assistant-directory `AGENTS.md` / `CLAUDE.md` entry (`assistant.prepare` writes both, loading only one if identical); node carries the exact remainder in `context` on every non-command message, plus the user's `$DSH_HOME/AGENTS.md` |
| Extension skills (such as those from the Lark extension) | Assistant directory `.agents/skills/` (DSH reads here); commands run through the gate-declared tool above |
| Memory | **The same files** as Claude and Codex: the first message of a new chat is prefixed with memory directory, format, and current index (`prompts/assistant/runtime_instructions.md`); edits in that directory do not prompt outside Plan mode |
| Background tasks | DSH's own tasks (`job_*`), with running ones listed in session `background` (see "Subagents and background tasks"); whether completion wakes the chat follows DSH rules, with no separate node bookkeeping |
| Warmup | Unnecessary: `dsh web` is persistent, and new chats call `sessionController.create` directly |
| Live snapshots | Attached to each message like Claude and Codex (`<persona-context>`, changed sections only), hidden in the chat (`map.js` strips attribute-free `<persona-context>` blocks) |

Differences from the Claude version: persistent `dsh web` required, and a `dsh web` restart interrupts the active turn; assistant chats also appear in DSH's Web GUI (continue viewing on the computer).
DSH authorization has only "Allow once"; the Lark gate's "Always allow in this chat" is still remembered by node; denial reasons are steered to the model; switching a chat to Claude / Codex requires a new chat.

## Subagents and background tasks

DSH Agents often delegate work before ending a turn: background subagents (the `subagent` tool returns results to the parent and wakes it on completion), workflows, and background commands.
The parent session itself is "Complete" when its turn ends, but the work is not finished. The plugin attaches these to the parent row using the same fields as Claude sessions (`docs/integrations/runtimes.md`, "Sub-Agents and background tasks"):

| Persona | DSH |
| --- | --- |
| `subagents` | Child sessions whose `parentSessionId` in `sessionController.list` matches the parent (subagents and workflow-dispatched Agents). `id` = child session id, `running` = child is running (list `running`, `api-session/status`), `description` = delegation `description` (parent `subagent/catalog`; workflows use `tool-workflow/agent-start` `label`), `tool_use_id` = the launching `subagent` call (result "started subagent <id>"), plus `started_at` / `updated_at` and `last_text`. List all running children and recent others, at most 20. For children predating plugin startup, recover names and calls from parent `subagentCatalog` projection and events (read at most once every 5 minutes per parent) |
| `background` | DSH background tasks still running under the parent: `job/registered` / `job/settled` events, supplemented by `ctx.jobs.list(session)` for tasks predating plugin startup. Tool calls still waiting for results do not count (foreground work); `kind`: `bash` → `shell` (`command` is the command), others (workflow…) → `agent` |
| Subagent content | id `<parent-session>@<child-session>`: paginate with the DSH subagent address `{kind: "subagent", parentSessionId, childSessionId, mode: "unknown"}` (DSH does not allow reading children as ordinary sessions), `history` = `dsh:<parent>@<child>`; watch and streaming also push under this id |
| State changes | Republish the list when subagents start / finish or background tasks register / finish |

Thus, when the parent's turn ends but a subagent still runs, the hub treats it as Working (#2228). Open the parent to see subagents and background tasks, then open the launching call to inspect the subagent's progress.
The hub sends "Complete" notifications only after those finish and the parent's wake-up turn ends (`backgroundRunning` in `assistant/fleet.go`). When a subagent finishes, the parent displays a "Subagent complete" notice (`map.js`).

Subagents are read-only, with no sending or renaming (returns `unsupported`): they are controlled by the parent, so give instructions there.

Authorization and questions: when DSH delegates, it pins the child authorization policy to `never`; operations requiring authorization are rejected directly, and `ask_user_question` fails with
`DELEGATED_CALLER`. These requests never enter a response chain, reach neither phone nor Web GUI, and do not block waiting for a user. If a child policy is changed back to `ask` in DSH,
its authorization cards attach to the **parent session** on the phone (which lists the parent), with a `reason` prefix identifying the child ("Subagent ‘<name>’: …"); denial text is not steered (subagents do not accept interjections).

Offline sessions listed by the companion (when DSH is not running) omit subagents and background tasks: without DSH, none can be running.

## Limitations

- No terminal (DSH has no TUI); the app hides the terminal entry.
- Subagents are read-only, without messages or renaming (see "Subagents and background tasks"); older children predating DSH's `subagent/descriptor` cannot be paginated (DSH rejects their address), but still appear in the list.
- Tool-result images are not passed to DSH Agents; images in companion-listed offline sessions are visible only after DSH starts.

## Verification

- `go test ./internal/node -run 'TestRuntime|TestPollAgentsPublishesWithoutHerdr|TestLauncherModelsKnowsRuntimeLaunchers'`, `./internal/assistant -run TestRuntimeLauncherKeepsItsSource`
  (the DSH launcher keeps its `dsh` source in the new-chat picker and is not listed under Settings › Models).
- `cd integrations/dsh && npm test`: mapping, pagination, sending, watch continuity, streaming, authorization / questions / plans / late answers, reconnection; archiving and closing, hello / card ids after renaming,
  phone-tool registration and calls (`dsh.test.js`); companion offline lists, pagination, wakeup and handover, rejection of old nodes (`files.test.js`).
- Workspace creation contract: `DSH_API_ROOT=<isolated DSH backend package root> node integrations/dsh/e2e/workspace.mjs` loads published DSH registry/controller/session-storage packages and sends Persona runtime RPCs. It checks exact and symlinked directories, normalization, unmatched/child directories, new-directory creation, non-ENOENT lookup failure without partial creation, and durable workspace membership. The Agent factory and node peer are fixtures; no model, browser or existing DSH instance is used. Tested with the DSH backend packages at `0.1.2-alpha.2`; this is not a full DSH application/browser acceptance run. The script removes its fixture by default; `KEEP=1` retains the synthetic storage for inspection.
- Node: `go test ./internal/node -run 'TestRuntime'` (runtime id conflicts, same-process reconnection, single-owner launchers, three handover outcomes: `runtimes_identity_test.go`;
  subagent/background rows, `<session>@<child>` calls and pushes routed to the parent's runtime: `TestRuntimeSubagentsAndBackground`; daemon prefixes: `TestDaemonSubagentIDs`).
  Plugin: subagent / workflow / background task start and finish, pagination and watch using parent addresses, child authorizations and questions attached to the parent, recovering preexisting children from catalogs and job tables (last three in `dsh.test.js`).
- End-to-end: `DSH_CHECKOUT=<built dsh source> PERSONAD=<personad> node integrations/dsh/e2e/run.mjs`. Starts real `dsh web` in a temporary directory
  (independent `DSH_HOME`, model played by DSH's `llm-mock-server`) and real `personad node --stdio`, with the script acting as hub: create a session from the "phone," send messages,
  inspect reasoning and replies, paginate history, approve a command (actually executed), answer a question, inspect streaming, interrupt, check push continuity, then have the DSH Agent call a phone tool (hub receives call and session,
  result returns to Agent), delegate a background subagent (parent turn ends; child is running in `subagents` with its launching call, not separately listed; paginate/watch through `<parent>@<child>`;
  after completion the list reports finished and the parent wakes), run a background command (listed in `background` after the turn ends, removed on exit), then close from the phone (removed from list, present in DSH archive).
  Child and parent requests interleave; the mock model separates them using prompt markers (`scriptedModel().scriptWhen`). On 2026-11-02, 31/31 passed; daemon mode (`PERSONA_E2E_DAEMON=1`) also 31/31.
  The script installs a test-only DSH rule (commands containing `PERSONA_E2E_ASK` require authorization) and an "unsandboxed but requires authorization" permission preset:
  DSH cannot start its own sandbox inside another sandbox (such as an Agent shell).
- Old sessions dropped from the list: `node integrations/dsh/e2e/older-session.mjs` (same two environment variables). Limit the plugin to 1 session, create A and B,
  restart DSH so the plugin forgets phone interaction with A, then message A; require delivery to DSH, reply push, and no herdr routing. 2026-11-01: new version 5/5, deployed old version 3/5.

- When DSH is not running: `node integrations/dsh/e2e/files.mjs` (same two environment variables). Run one turn in real `dsh web`, stop it, have the companion list the session from disk as offline and paginate its content;
  no files in `DSH_HOME` change before/after. Send again from the "phone"; the companion starts DSH through a wake script, node hands the message to the plugin, the reply returns, and the plugin owns the session again. On 2026-11-02, 7/7 passed.

- Automatic updates: `node integrations/dsh/e2e/update.mjs` (same two environment variables). Node packages come from a directory (`PERSONA_RUNTIME_PACKAGES`), avoiding another personad build:
  with matching node/plugin versions, node offers nothing and the plugin runs its bundled copy from the package directory. Switch to a node shipping a new plugin (as a hub update would); the plugin is offered, fetches files, and switches itself;
  a new session messaged from the "phone" replies normally. After DSH restarts, it directly runs the node-supplied copy without fetching again. On 2026-11-02, 7/7 passed. Loader itself: `cd integrations/runtime-sdk && npm test`
  (`loader.test.js`: version switches, no switch on hash mismatch, fallback if startup fails, waiting for phone cards, priority for manual installs, no daemon switches).

- Assistant: `node integrations/dsh/e2e/assistant.mjs` (same two environment variables). The "hub" selects launcher `dsh` through `assistant.send` to create a chat (in the assistant directory,
  with snapshot and memory in the first message but only typed text visible in chat), `ask_user` reaches the hub as an assistant call, Plan mode sets the DSH preset, interruption, and continued sending after node restart. Passed on 2026-11-02; the test also covered node's built-in Lark tool at the time,
  but since #1571 it is declared by the extension command gate, so that portion was removed from the script.

## Future work

Platforms built on DSH that do not run `dsh web` can implement a runtime on their side using the same SDK (`integrations/runtime-sdk`), reuse `integrations/dsh/lib/map.js` mappings,
and connect as daemons (`docs/integrations/daemons.md`), without additional Persona code.

Daemon end-to-end: run the same `e2e/run.mjs` with `PERSONA_E2E_DAEMON=1` (plugin configured with `listen`, node connects through `daemons.json`). Checks stay the same except for two differences: session ids carry the `dsh.` prefix; the phone-tools case becomes "daemon cannot access phone tools; calls cannot reach the hub."
On 2026-11-02, 21/21 passed (socket mode also 21/21).
