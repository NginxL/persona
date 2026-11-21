# Managed Agents

Sessions of a Managed Agents service appear in **Agents** like any other agent's: listed and grouped by the agent they
run, their conversation readable (messages, thinking, tool calls and results, errors), a message sent from any client,
a running turn stopped, a new session started on a configured agent, and what the agent waits on a person for answered
on the phone: a tool call its policy asks about as a permission card, the AskQuestion tool as a question card and
ExitPlanMode as a plan card.

Managed Agents is the API family of [Volcengine Ark Managed Agents](https://www.volcengine.com/docs/82379) (MA), which
follows Claude Managed Agents: agents, environments, sessions, and a session's event log. The runtime talks to either
kind of deployment:

- **Cloud**: a hosted endpoint. Ark by default (`https://ark.<region>.volces.com/api/v3`), or Claude's (`api: "claude"`).
- **Self**: an endpoint you run yourself (a private region, a gateway in front of MA, a local or self-hosted server
  speaking the same API), at its own address, path prefix and auth header.

Keys are your own (bring your own key): the MA API key, and optionally the credential MA forwards to your model provider
on each message. The configuration names where each key is, never the key (see "Keys" below).

Audience: Agents, for people who work with agents directly. Nothing changes in Assistant, and nothing appears until
someone sets the runtime up on one of their computers.

Supported since 2026-11-20 (#11929).

## Shape

It is a runtime (`docs/integrations/runtimes.md`): `integrations/managed-agents/`, built on `integrations/runtime-sdk`,
with no code in the hub, the node or the apps. Sessions are listed with `source_kind: "runtime"`, so the apps already
know what they can do with them (read, send, stop, answer cards; no terminal).

```
 App ── hub ── node ──┬── ~/.persona/runtime.sock ◄── managed-agents companion (this computer) ──► MA REST API (cloud / self)
                      └── daemons.json ──► ws://<companion>/persona/runtime  (the companion elsewhere, as a daemon)
```

- **On a computer with a node** (the usual case, cloud or self): `node integrations/managed-agents/companion.mjs`
  connects to the node's `runtime.sock` and calls the MA API over HTTPS. Keep it running with launchd or systemd, like
  the Grok and pi companions.
- **Next to a self-hosted MA that the node cannot share a user with** (another container, another machine):
  `PERSONA_DAEMON_TOKEN=<token> node integrations/managed-agents/companion.mjs --listen <port>` and an entry in the
  node's `~/.persona/daemons.json` (`docs/integrations/daemons.md`). The node then names it from that entry, prefixes its
  session ids, and offers one launcher (the first profile with `start`).

Why not a hub extension or node code: the runtime protocol already carries everything this needs, and runtimes are meant
to add no code to Persona (`docs/integrations/runtimes.md`, "What to implement"). A hosted service needs no local
process of its own, but the node is the one place that turns sessions, items and cards into what every app shows.

## Configuration

`~/.persona/managed-agents.json` (`--config` for another file), read again within 10 seconds of a change. Keep it 0600:
it holds no key, but says where they are.

```json
{
  "profiles": [
    {
      "id": "ark",
      "name": "Ark",
      "mode": "cloud",
      "region": "cn-beijing",
      "api_key": "keychain:ark",
      "model_headers": {"Authorization": "keychain:ark-model"},
      "agents": ["agent-<id>"],
      "start": {"agent": "agent-<id>", "environment_id": "env-<id>", "label": "Research helper"}
    },
    {
      "id": "lab",
      "name": "Lab",
      "mode": "self",
      "base_url": "https://ma.example.com",
      "api_prefix": "/api/v3",
      "auth": "bearer",
      "api_key": "file:~/.config/persona/ma-lab.key",
      "sessions": ["sesn-<id>"]
    }
  ]
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `runtime` / `name` (top level) | `managed-agents` / `Managed Agents` | The runtime id and what the apps call it. Change both to run two companions on one computer |
| `id` | required | Lowercase letters, digits and `-`, at most 24; the prefix of this profile's session ids (`<id>.<MA session id>`) |
| `name` | `id` | Shown after the agent's name in a group's title when there are several profiles |
| `mode` | required | `cloud` or `self`. A `self` profile needs `base_url` |
| `api` | `ark` | Which API family: `ark` (`/api/v3`, `Authorization: Bearer`, `X-Ark-Beta`) or `claude` (`/v1`, `x-api-key`, `anthropic-version` / `anthropic-beta`) |
| `region`, `beta` | `cn-beijing`, the family's | Ark's region host; the beta header's value |
| `base_url`, `api_prefix` | the family's | Where the API is. `http://` only to a loopback address unless `allow_http: true`; no user name, password, query or fragment |
| `auth` | the family's | `bearer` or `x-api-key`: the header the key goes in |
| `api_key` | required | A reference to the MA key (see "Keys"). A key written here is refused |
| `headers` | none | More headers for every request (a project header a gateway wants). Credentials are refused here |
| `model_headers`, `model_header_prefix` | none, `X-User-Model-Header-` | BYOK model credentials: header name → key reference. Each goes only on event POSTs, as `<prefix><name>` (Ark's Direct providers) |
| `project` | none | `project_name` on list and create (Ark projects) |
| `agents` | all | List only the sessions of these agents |
| `sessions` | none | Sessions to list even when the server has no session list |
| `list` | `{path: "/sessions", limit: 50}` | The session list route; `enabled: false` turns it off |
| `start` | none | `{agent, environment_id?, label?, launcher?, body?}`: offer New Agent on this agent. `launcher` defaults to `ma-<id>`; `body` is more of the create request (resources, vault ids) |
| `question_tools`, `plan_tools` | `["AskQuestion", "AskUserQuestion"]`, `["ExitPlanMode"]` | Custom tools answered on the phone as question and plan cards |
| `stream` | `true` | Follow a watched session's event stream for live text; polling alone otherwise |
| `poll_seconds`, `recent_minutes` | `3`, `30` | How often running, waiting, watched and recently active sessions are read again |
| `images` | `base64` | `none` sends a message's pictures as nothing (servers without image blocks) |
| `disabled` | `false` | Keep the profile, list nothing from it |

`node integrations/managed-agents/companion.mjs --check` reads the config, resolves each key and lists each profile's
newest sessions once: it prints counts and errors, never a key or a conversation.

## Keys

The config holds **references**, resolved when a request needs them, kept in memory for a minute at most, and dropped
when the server answers 401 or 403 (a key you rotated is read again):

| Reference | Where the value is |
| --- | --- |
| `keychain:<account>` or `keychain:<service>/<account>` | The macOS Keychain (service `persona-managed-agents` by default): `security add-generic-password -s persona-managed-agents -a ark -w` asks for the key without echoing it |
| `file:<path>` | A file holding only the key; a warning while others can read it. Reread at each use, so rotating needs no restart (as `daemons.json`'s `token_file`) |
| `env:<NAME>` | An environment variable of the companion's process (a service manager's secret injection) |
| `extension:<name>` | Persona's sealed extension secrets, when the companion runs as a Persona extension's program and gets them in `PERSONA_EXTENSION_SECRETS` (`docs/extensions/extensions.md`, "How config and secrets reach the extension") |

- A value in place of a reference is refused, and the refusal does not repeat the text (it may be the key).
- Keys never go on a command line, into a file the companion writes, into an item, or into a log line: every error and
  log line passes through a redaction of every value resolved so far and of anything shaped like a bearer token.
- Requests never follow a redirect, so a key cannot be carried to another address.
- The MA key goes on every request; model credentials only on event POSTs (a message, an answer), never on reads or the
  stream. Ark keeps no model credential: each message brings it again.
- The only file the companion writes is `managed-agents-state.json` beside the config (0600): the ids of sessions started
  from Persona, so a server without a session list still lists them after a restart. No content, no key.

## Mapping

| Persona | Managed Agents |
| --- | --- |
| Session list | `GET <prefix>/sessions` (newest first), plus `sessions` and the ones started from Persona read with `GET <prefix>/sessions/{id}`; archived sessions and other agents' (with `agents`) left out. Rows through `rt.recent` (7 days, 80; running and waiting always) |
| Grouping | Space per MA agent (`<profile>:<agent id>`), named by the agent |
| Status | From the log: a pending ask → `blocked` (Needs you), running → `working`, idle → `done`, terminated → `offline`; the session's own `status` before its log is read |
| Conversation | `GET <prefix>/sessions/{id}/events?order=asc&limit=100`, following `next_page`, deduped by event id (the server's `after` is a hint, not a filter). seq = the event's position in the log; history `ma:<session>` |
| Items | `user.message` → user text and images; `agent.message` → reply; `agent.thinking` → thinking; `agent.tool_use` / `agent.mcp_tool_use` / `agent.custom_tool_use` → tool call (names mapped to the apps' set: `bash` → `Bash`…; MCP as `mcp__<server>__<tool>`); `agent.tool_result` / `agent.mcp_tool_result` / `user.custom_tool_result` → result; a denial → an error result; `user.interrupt` → interrupted notice; `session.error` (not while it is `retrying`) → error notice; `agent.thread_context_compacted` → compaction divider. Status, span, usage and other events take a seq and show nothing |
| Live text | While a session is watched: `GET <prefix>/sessions/{id}/events/stream?event_deltas=agent.message&event_deltas=agent.thinking`; reply deltas → `session.delta`, ended when the full event lands. A server without the stream is polled |
| Send | `POST <prefix>/sessions/{id}/events` `{events: [{type: "user.message", content}]}` (text, base64 image blocks), with the model credentials; deduped by `client_msg_id` in the SDK; `{queued: true}` while a turn runs |
| Interrupt | `user.interrupt`; settled once the log says the turn ended (10 s at most) |
| Start | `POST <prefix>/sessions` `{agent, environment_id?, project_name?, title, …body}`, then the first message |
| Permission card | A tool call with `evaluated_permission: "ask"` that the latest `requires_action` names and nothing answered → `user.tool_confirmation` `allow` / `deny` (with the denial's words as `deny_message`) |
| Question / plan card | A configured custom tool call → `user.custom_tool_result` whose text is JSON: `{answers: {question: label}}`, or `{decision: "approved" \| "changes_requested", executionMode, feedback}` |
| Answered elsewhere | A card whose call was answered in the MA console or another client is withdrawn from the phone; one handed back from the phone is not asked again |

Not mapped: subagent threads as separate rows (their events appear in the session's log), files and memory stores,
renaming, closing and archiving (no data-plane call for them is relied on), and the assistant (a daemon cannot be one,
and MA runs its own model and tools).

## Assumptions (made configurable)

The public material settles the event log, sending and the stream. These are not settled, so each is a setting:

- **A data-plane session list.** Ark lists sessions through its control plane (`ListSessions`, signed with the account's
  access keys, not an API key). The runtime calls `GET <prefix>/sessions` with the API key; a server that answers 404 or
  405 is remembered, and only `sessions` and sessions started from Persona are listed (`list`).
- **The BYOK model header.** Ark forwards `X-User-Model-Header-<Name>`; another server may differ (`model_header_prefix`).
- **What answers a question or plan tool.** AskQuestion and ExitPlanMode are custom tools an agent declares; their result
  format is the agent's. The JSON above follows the published AskQuestion / Plan Mode tool setup; other tools can be
  named in `question_tools` / `plan_tools`.
- **Image blocks** in `user.message` (`{type: "image", source: {type: "base64"}}`); `images: "none"` where refused.

## Verification

- `cd integrations/managed-agents && npm test`: config (cloud / self defaults, refusals that never repeat a key), key
  references (environment, file and its mode, Keychain, extension secrets, redaction), event mapping, pending asks and
  answers, and the runtime against the SDK's fake node and a fake MA server: listing and grouping, paging a long log, the
  MA key on every request and the model key only on sends, sending once per `client_msg_id`, live text, permission,
  question and plan cards, a card withdrawn when answered elsewhere, starting without a list route, interrupt, a wrong
  key reported without the key.
- `PERSONAD=<personad> node integrations/managed-agents/e2e/run.mjs`: a real `personad node --stdio` with the script as
  the hub and the fake MA server, once on `runtime.sock` and once as a daemon dialed from `daemons.json`: the launcher,
  an existing session listed, grouped and read, a message sent once and answered with live text and thinking, a session
  started from the phone, its permission and question cards answered on the phone and back in MA, terminal calls
  refused, no key in the node's or the runtime's logs, its sessions leaving with it.
- Live (opt-in, read-only): `node integrations/managed-agents/companion.mjs --check` with a real profile.
