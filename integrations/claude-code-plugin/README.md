# Persona plugin for Claude Code

A Claude Code plugin that lets the owner's own messages from the Persona apps reach a Claude Code session the way
typed input does, in sessions Persona has no terminal for: Claude's desktop app, a terminal herdr / Orca / Tern do not
run, a background session. Without it the node can only use Claude Code's peer inbox there, which tells the model the
words come from another session and that they are not the person's approval (`docs/integrations/claude-integration.md`,
"Owner input through the Persona plugin").

The plugin polls the local Persona node while its session lives: it submits the owner's messages with
`$.prompt.submit({ text, asUser: true })` (immediately when idle, or into Claude Code's prompt queue while a turn runs),
cancels the active main-loop turn via `$.turn.abort({ turnId })` when the owner presses Stop, and applies session-scoped
model / effort picks on main-loop `turn.step` calls when `settings` is advertised. It reads no file but its own manifest
and stays quiet when no node is running.

## Packaging

- Plugin name: `persona`. Version: `version` in `.claude-plugin/plugin.json` (semver). The installer compares it; the
  plugin reports it to the node at run time, read from that same file.
- The folder is its own marketplace: `.claude-plugin/marketplace.json` names the marketplace `persona` and lists the
  plugin with `"source": "./"`. Installing it with Claude Code's own commands:

  ```sh
  claude plugin marketplace add <folder>
  claude plugin install persona@persona --scope user
  ```

  A marketplace that is a folder is read from that folder itself, so the installer keeps the folder at a stable path
  (not a temporary one) and replaces its files to update; `claude plugin update persona@persona`, then
  `/reload-plugins` in a running session (or its next start), picks the new version up. A user-scope plugin installed
  from a terminal also loads in the local sessions Claude's desktop app starts.
- Files: `.claude-plugin/plugin.json`, `.claude-plugin/marketplace.json`, `hooks/hooks.json`, `hooks/register.ts`,
  `hooks/bridge.ts`; `tests/` is for `claude plugin test` only. A node in standby (Persona Link while its manager is
  away) or an older node gives no HTTP answer; the plugin backs off and tries again.
- Needs a Claude Code with function hooks and `$.prompt.submit`'s `asUser` (measured on 2.1.291, 2.1.299 and 2.1.308).
  An older Claude Code does not load the module; the node then keeps using the peer inbox.

Check it: `claude plugin validate integrations/claude-code-plugin` and `claude plugin test integrations/claude-code-plugin`.
Try it in one session without installing anything: `claude --plugin-dir integrations/claude-code-plugin`.

## Contract with the node (protocol 1)

The plugin talks HTTP/1.1 to the node's own socket, the one hooks and `personad ask` use: `$PERSONA_HOME/node.sock`
when `PERSONA_HOME` is set (resolved as the node resolves it), else `~/.persona/node.sock`
(`%USERPROFILE%\.persona\node.sock` on Windows). The node tells HTTP from its JSON-line requests by the first line.
Every request carries the session id from `$.session.id()`. The node answers only the claude process that has that
session open: it asks the system for the process id at the socket's other end (`SO_PEERCRED`, `LOCAL_PEERPID`,
`SIO_AF_UNIX_GETPEERPID`) and looks it up in Claude Code's `sessions/<pid>.json`. Any other process, or a request for
another session, gets `403`.

`POST /claude-plugin/v1/poll`

```json
{"session_id": "<uuid>", "plugin_version": "0.2.0", "engine_version": "2.1.326", "capabilities": ["busy_input", "interrupt", "settings"], "wait_ms": 25000}
```

- Held until work comes or `wait_ms` passes (at most 25 s; Claude Code's fetch gives up at 30 s), then asked
  again at once. A newer poll for the same session answers the older one at once, empty. The plugin polls whether the
  session works or rests; when `busy_input` is advertised, owner prompts are also handed over while a turn runs so
  Claude Code queues them natively without holding them at the computer in `bypassPermissions` or `auto` mode.
  `settings` overrides can be delivered to any active poll.
- `200`: `{"messages": [{"id": "<id>", "text": "...", "deadline_ms": <epoch ms>}], "interrupt": {"id": "<id>", "deadline_ms": <epoch ms>}, "settings": {"id": "<id>", "model": "...", "effort": "..."}, "retry_ms": <ms>}`.
  `messages` is empty when nothing came; `interrupt` and `settings` are omitted when none are pending. `retry_ms`, when
  present, is how long to wait before the next poll: a session the node does not deliver to this way (one in a Persona
  terminal, the assistant's own) is told to come back much later.
- `403 {"error": "not_this_session"}`: the caller is not the claude that has this session open. `404`, or an answer
  that is not HTTP: a node without this protocol. The plugin backs off (up to a minute) and tries again.

`POST /claude-plugin/v1/ack`

```json
{"session_id": "<uuid>", "id": "<id>", "submitted": true}
```

`submitted: false` with a `reason` (`late`, `invalid`, `command`, `dropped`, `failed`) when the plugin did not submit it.
The plugin submits a message only before its `deadline_ms`; after that the node may send it through the inbox instead.

Registration is the poll itself: a session counts as having the plugin while its claude process lives and a poll from
that process waits, or ended less than 90 s ago. The node lists such a session with `capabilities.plugin_input: true`.

### Status for installers and diagnostics

On the same socket, the JSON-line request `{"kind": "claude-plugin.status"}` answers
`{"protocol": 1, "sessions": [{"session_id", "pid", "plugin_version", "engine_version", "polling", "seen_at"}]}`,
one entry per live session whose plugin has polled this node since the node started. An empty list means no running
session has loaded the plugin, or none has polled yet (a session polls as it starts, then without a break).

## What goes this way

Text the owner typed in a Persona app (`owner_input` on `session.send`, idle or busy when `busy_input` is advertised),
owner-attested turn interrupts (`interrupt`, `$.turn.abort`), and session-scoped model / effort overrides (`settings`).
Everything else keeps the peer inbox: messages from the Persona assistant, extensions and channels (Lark, WeChat),
relays between conversations, slash commands, the answer to a question the session asked, and non-owner interrupts.
Details and fallback rules:
`docs/integrations/claude-integration.md`.
