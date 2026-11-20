# Plan: connect Lark through lark-cli when there is no Lark gateway

Status: proposal (#273). Items the user decided are under "Decided"; work follows the "Step-by-step plan", with finished steps checked off. When everything is done, merge the design parts into
`docs/extensions/feishu.md`, `docs/assistant/knowledge.md`, `docs/assistant/inbox.md` and delete this plan.

## Background

Today all of persona's Lark features are built on the Lark gateway (`docs/extensions/feishu.md`): the Inbox, knowledge base archiving, live events, the AI twin, `/agent` in Lark,
and the assistant's Lark gateway skill. A hub without the Lark gateway has no Lark at all.

Lark's official [lark-cli](https://github.com/larksuite/cli) (npm `@larksuite/cli`, MIT) reads and writes Lark with an Open Platform app plus user authorization, and anyone can install it.
Goal: a hub that only installs lark-cli, without a Lark gateway, can still give Persona and the AI twin Lark context. The Lark gateway's existing behavior stays the same; this is just one more source.

Not doing: no browsing pages for Lark (`docs/extensions/feishu.md` ("Persona is not a second Lark")); not replacing the Lark gateway.

## What lark-cli can do (tested on 1.0.72, 2026-11-02)

Reads and most writes use the user identity (`--as user`); sending and replying to messages use the app's bot (`--as bot`, see "Send" in the table below); output is JSON (`--format json`).

| Capability | Command | Notes |
| --- | --- | --- |
| Conversation list | `im +chat-list --types p2p,group --sort active_time` | Has `chat_id`, name, type, the other party of a direct chat; **no unread count, last message, or @me marker** |
| Read messages | `im +chat-messages-list`, `im +threads-messages-list`, `im +messages-mget` | `message_id`, `create_time`, sender (with name), `content` (Open Platform JSON), topic position |
| Search messages | `im +messages-search` | Works without a keyword: given only `--start`, it returns every message I can see in that period; `--is-at-me`, `--chat-type p2p`, `--sender`, `--query` can be combined; results include the conversation name, `mentions`, `thread_id` |
| Send | `im +messages-send`, `im +messages-reply` (can reply in a topic, markdown), `im reactions create / delete` | Uses the bot identity (`--as bot`): the user identity needs the app to have `im:message.send_as_user`, which the default app lacks (tested 2026-11-04: `missing_scope`); the bot can only message people who can see this app, and groups it is in |
| Documents | `docs +fetch --doc-format markdown`, `drive +search`, comments (`drive`), `sheets`, `base` | No JSON node tree, `header` or `ai-summary` like the Lark gateway has |
| Calendar / meetings / Minutes | `calendar +agenda`, `vc +search / +detail`, `minutes +search / +detail` | |
| People | `contact` | Search by name, look up by id |
| Live events | `event consume <EventKey>` | IM events (`im.message.receive_v1` etc.) **are pushed to bots only**; the user identity only gets approvals, Minutes, meetings and tasks |
| Risk level | Each command's `--help` prints `Risk: read / write / high-risk-write` (about 50 ms); `schema <method> --json` includes `risk`, `danger` | Usable by the gate directly |
| Login | `auth login` (Device Flow; works on machines without a browser, finished by tapping a link on the phone), `auth status`, `whoami` (JSON) | Access token lasts 2 hours, refresh token 7 days (`refreshExpiresAt` in `auth status`): unused for a week means logging in again |
| Domain guides | `lark-cli skills list / read <name>` | Skills are compiled into the CLI and versioned with it; Agents read them on demand, no need to copy them into the repo |

## Comparison: Lark gateway capabilities in use today

The "Needs" column: unread = Lark's unread / read position; feed = the conversation list's unread / @me / done markers; events = live push. The Open Platform has none of these.

### Inbox (`hub/inbox*.go`)

| Today | lark-cli | Degraded |
| --- | --- | --- |
| Candidate conversations: unread conversations + @me anywhere (`conversations?scope=unread / mentions`, needs feed, unread) | One `+messages-search --start <last>` per poll, filtered in the hub: mentions me, direct chats, new replies in topics I take part in (see "What the Inbox collects") | Collects @me, direct chats, and replies in topics I take part in; unread group messages outside these three do not enter the Inbox. "Handled" is still recorded only in Persona |
| Item context, whether I replied (`Messages` / `threads` + `isMine`) | `+chat-messages-list`, `+threads-messages-list`, sender is me | Works |
| Document notifications: @me, comments, shares (`documents/notifications`) | Watched document comments (`drive +list-comments` / `+list-replies`), #2691 | Shares and unconfigured documents remain out of scope |
| Reading and writing docs, sheets, Base, and replying to comments (`documents/*`) | `docs`, `sheets`, `base`, `drive` comments | Works, with a rewritten adapter layer |
| Moments (`moments/*`) | None | Not doing |
| Sending confirmed replies (`Send` / `Reply`) | `+messages-send` / `+messages-reply` | Works |

### Knowledge base (`hub/kb*.go`)

| Today | lark-cli | Degraded |
| --- | --- | --- |
| The 100 most recently active conversations (`conversations?scope=chats`) | `+chat-list --sort active_time` | Works |
| New messages, backfill (`Messages` by position) | One `+messages-search --start <cursor>` per poll for all new messages; backfill with `+chat-messages-list` paging back by time | The cursor changes from position to `create_time` + `message_id` |
| Attachments (`media`) | `+messages-resources-download` | Works |
| People, groups (from senders and conversation names) | Same, completed with `contact` | Works |
| Calendar, meetings, document titles (`calendar/week`, `meetings`, `documents/read`) | `calendar +agenda`, `vc +search`, `minutes +search`, `docs +fetch` | Works |
| Moments | None | Not doing |

### Live events (formerly the hub's lark_events.go, now provided by the Lark extension)

| Today | lark-cli | Degraded |
| --- | --- | --- |
| The `/events` WebSocket kicks the Inbox, knowledge base and `/agent`, and pushes `lark` to the App | None | Polling: Inbox and archiving every 2 minutes (same as today's fallback interval), `/agent` every 20 seconds; the App gets no `lark` push and fetches when a page opens |

### `/agent` in Lark (formerly the hub's lark_agent*.go, now provided by the Lark extension)

| Today | lark-cli | Degraded |
| --- | --- | --- |
| Recognize my own `/agent` from events (needs events) | `+messages-search --sender <me> --query /agent --start <10 minutes ago>` every 20 seconds, deduplicated by `message_id` | Up to about 20 seconds of delay, plus search-index delay (to be measured) |
| OnIt / DONE reactions | `reactions create / delete` | Works |
| Context: topic, previous 40 messages, quoted message, documents | `+threads-messages-list`, `+chat-messages-list`, `+messages-mget`, `docs +fetch` | Works |
| Reply (markdown, in the topic) | `+messages-reply` | Works |
| `/agent` in document comments (`documents/recent` + comments) | No "recently opened documents" | Not in the first version |

### Assistant (`node/larkgate.go`, `assistant/tools.go`)

| Today | lark-cli | Degraded |
| --- | --- | --- |
| The Lark gateway skill: `<command> METHOD PATH` | `lark-cli <domain> <command>`; Persona ships a short skill that points the way, and the Agent reads the details with `lark-cli skills read` | Works |
| Writes go through the `ol` gate with phone confirmation | The gate recognizes `lark-cli` and decides by the risk level it reports (see "Gate" below) | Works |
| `lark_chats` (unread / @me / recent) | Recent: `+chat-list`; @me: search | No "unread" filter |
| `lark_messages`, `lark_search` (messages, docs, people, calendar, Minutes, tasks, mail) | `+chat-messages-list`; `+messages-search`, `drive +search`, `contact`, `calendar`, `minutes`, `task`, `mail` | Works |
| Snapshot: Inbox conversations, this week's calendar | Recent @me / direct chats + `calendar +agenda` | No unread counts |

### The App's Lark routes (formerly the hub's lark.go, now provided by the Lark extension)

| Today | lark-cli | Degraded |
| --- | --- | --- |
| Conversation page: messages, topics, send, reply, reactions, members, media | All have matching commands | Adapted to today's JSON; the App does not change |
| Unread counts, labels, pinning, read positions, `read-states` | None | Not shown for this source |
| Document reader (`raw/documents/*`) | `docs +fetch --doc-format markdown` | Markdown only, no JSON tree or summary |
| Moments, `url-preview`, emoji list, personal Agent bridge (`agent-bridge`) | None | Not doing; emoji use a local static table |

## Shape

- **Connector**: a built-in connector `larkcli` on the framework in `docs/plans/plan-connectors.md`. The framework already has `Accounts`, `ReadTools`, `Actions`, `Inbox`, `Archive`, `Runner`;
  it implements these as needed, and the hub wires it into the knowledge base, Inbox, assistant and App automatically; anything the framework lacks (such as `Items`) is added in the step that needs it, per the connector plan. The Lark gateway pipelines stay untouched until M1–M6 of the connector plan move them; no `if gateway … else …` in each pipeline.
- **Both sources can coexist** (user decision 2026-11-03): the Lark gateway (`lark.token` / `lark.container`) and lark-cli (`lark.cli`) can be configured at the same time. Rules:
  - **One primary source per capability**: `lark.prefer` (default: the Lark gateway) is used first; if it lacks the capability (document comments, Minutes… whatever the implementation actually covers), cannot connect, or has lost its Lark login, the other is used.
    Switches are logged and shown on the settings page, never silent.
  - **The Inbox and knowledge base collect from only one source at a time**; the other takes over only when the primary is unavailable, so the same message does not come in twice.
    The two sides use different ids (Open Platform uses `oc_` / `om_`, the Lark gateway uses feed ids and positions): the knowledge base prefixes ids by source (the Lark gateway as before, lark-cli with `larkcli`), so they never overwrite each other;
    while switching sources, the same message is deduplicated by "conversation + sender + send time + body". Whether one conversation's ids on the two sides can be matched (whether the Lark gateway's conversation info carries the Open Platform `chat_id`) is checked first in L3;
    if they match, they merge into one conversation.
  - **The assistant gets both**: when both the Lark gateway skill and lark-cli are installed, the prompt explains what each is good at (Lark gateway: unread, feed, Moments; lark-cli: Open Platform docs, calendar, Minutes, approvals…), and the gate recognizes both.
- **Call the CLI, not the Open Platform directly**: the hub runs `lark-cli … --as user --format json` as a subprocess. Credentials and refresh stay in lark-cli's own storage; the hub never touches the app secret or tokens;
  one call takes 100–300 ms, enough for the polling volume. All calls and JSON adaptation live in one package (`internal/larkcli`), contract-tested against recorded JSON, with a pinned minimum supported version.
- **Where it is installed**: the hub machine (also where the assistant runs). The user runs `lark-cli auth login` on that machine; the hub only reads `auth status`.
- **lark-cli first**: official, MIT, risk levels built into its commands, skills compiled into the CLI. If someone wants the community feishu-cli later, add another adapter; the connector stays the same.

## Configuration (`hub.json`)

```json
"lark": {
  "cli": { "bin": "lark-cli", "profile": "", "inbox_poll_seconds": 120, "inbox_page_limit": 5 },
  "prefer": ""
}
```

- `lark.cli.bin`: command name or absolute path, looked up on the login shell's PATH by default (npm global installs often live in the nvm directory). Without `lark.cli` it is not enabled.
- `lark.cli.profile`: optional explicit lark-cli profile (`--profile`); it always wins. When empty and the built-in Lark bot has credentials, the manager uses managed profile `persona`: it reads `lark-cli --profile persona config show`, and only when `appId` is missing or differs runs `lark-cli config init --name persona --app-id <id> --app-secret-stdin --brand <feishu|lark>` with the bot secret on stdin. The secret never enters argv, logs, errors or status. With no bot credentials, empty uses lark-cli's default profile. The effective profile is passed to every hub call, the connector runner and the assistant wrapper.
- `lark.cli.inbox_poll_seconds`: optional Inbox poll interval; defaults to 120 seconds and is clamped to a minimum of 60 seconds.
- `lark.cli.inbox_page_limit`: optional maximum `+messages-search` pages per Inbox round; remaining pages continue in later rounds (default 5).
- `lark.prefer`: which source is primary when both are configured: unset (default, the Lark gateway) or `cli`; any other value is passed as-is to the extension that provides Lark (see "Shape").
- App credentials do not go into `hub.json`: lark-cli uses the app bound in its own `config`.
- **Enable every permission that can be enabled** (historical user decision 2026-11-03): Settings › Connectors › Lark used to start `lark-cli auth login --domain all` as a blocking background process. The current app rule is narrower: sign-in passes `--scope` with the connector's exported read-as-me scope list, covering only the commands the connector uses (messages, the message Inbox path, user display lookup when needed, document comments, and Wiki discovery). The App polls until it exits. The terminal command remains a secondary hint, and `--domain all` is still an optional manual step for people who want the assistant wrapper to reach more of Lark. Permissions that need admin approval are unavailable until approved: the settings page lists what is missing with `auth check`, and a missing feature says which permission it needs, instead of being reported as a connection failure. Missing scopes include the bound app's Open Platform permission page.
- Settings › Connectors › Lark: shows the status of each source, which one is currently primary, the account lark-cli is logged in as, when the token expires, and missing permissions; the lark-cli status lives in the Lark group, and when it has expired, it says to run `lark-cli auth login` on the hub machine.

## What the Inbox collects (lark-cli source)

User decision 2026-11-03: **@me, my direct chats, and new replies in topics I take part in within topic groups**. The Open Platform has no unread state, so the decision is made from the messages themselves, with one search per poll:

1. `+messages-search --start <cursor> --as user --page-all` gets every new message I can see since last time (no keyword); the cursor is the newest `create_time` of the previous round, searching 1 extra minute back and deduplicating by `message_id` to cover search-index delay.
2. Drop my own messages and keep:
   - those whose `mentions` include me (or @all);
   - direct chats (`chat_type` is `p2p`);
   - those whose `thread_id` is in "topics I take part in": topics where I posted, replied or was @mentioned. The set is built from history search (`--sender <me>`) and the archive, then extended whenever I am seen speaking in a topic;
     both topic groups (`chat_mode` is `topic`) and thread replies in regular groups count.
3. Consecutive messages in the same conversation or topic are grouped into one item (one row per item, like today's Inbox), with context from `+threads-messages-list` / `+chat-messages-list`.
4. If I replied within the item (a message from me comes after), it counts as replied, the same as today's `isMine` check.

One poll's search may page through a lot (tenants with many large groups): `--page-limit` caps it, the rest continues in the next round, and the volume is measured in L4.

## Gate (done in the first step)

Every Bash call from the assistant goes through the PreToolUse gate in `node/larkgate.go`, which today only recognizes `ol`. Add a branch that recognizes `lark-cli`:

| Command | Handling |
| --- | --- |
| `--help`, `--version`, `schema`, `skills`, `--dry-run`, `whoami`, `doctor`, `auth status / check / scopes / list`, `event list / schema / status` | Allow |
| `auth login / logout`, `config …`, `profile …`, `update`, switching `--profile`, reading lark-cli's credential files | Deny: logging in, switching apps and changing config are done by the user |
| `api GET / HEAD …` | Allow |
| Other `api` methods | Phone confirmation; the card shows the method, path and `--data` |
| Domain commands (`im +messages-send` etc.) | Ask lark-cli for the risk level by running the same words plus `--help` (cached per command): `read` is allowed; `write`, `high-risk-write`, or no answer all need phone confirmation, with the full command on the card. A command lark-cli does not have (its `--help` is the parent group's, whose `Available Commands` lack the last word) runs, and lark-cli answers "unknown subcommand" (#10845) |
| `event consume` | Allow (read-only); the prompt requires `--max-events` / `--timeout` |
| A command the gate cannot parse (loops, `$(…)`) containing a lark-cli write | Phone confirmation for the whole command (same as `ol`) |

The `lark_gate` rules in `hub.json` still match first; lark-cli calls are written as `{"method": "CLI", "path": "/im/+messages-send", "action": "allow"}` (the command's words joined with `/`).
Codex app-server approvals use the same `larkCheck`.

## Step-by-step plan

- [ ] **L0 Proposal**: this document (this PR).
- [x] **L1 Assistant**: the gate recognizes lark-cli (`node/larkcligate.go`); config `lark.cli`, `lark.prefer`; a `lark-cli` wrapper on the assistant's PATH; prompts (one more section in CLAUDE.md,
  no separate built-in skill: for domain guides the Agent reads the ones built into the CLI with `lark-cli skills read`) say which source to use based on what is configured; `docs/extensions/feishu.md`, `docs/operations/config.md`,
  `docs/assistant/assistant.md` ("Lark: lark-cli"), documenting `auth login --domain all`.
  Acceptance: the assistant can look up recent messages, documents and calendar; sending a message shows a confirmation card and nothing is sent after denial; hubs with the Lark gateway behave the same (existing goldens unchanged).
- [x] **L2 Connector skeleton**: added the subprocess-only `internal/larkcli` adapter, version and auth/status checks, and the `larkcli` connector with Settings status plus `larkcli_chats`, `larkcli_messages`, and `larkcli_search` read tools.
- [ ] **L3 Knowledge base**: `Archive` (conversations, messages, people, groups: search polling + backfill; attachments), `Items` (calendar, meetings, Minutes, document titles). Acceptance: the knowledge base has Lark conversations and people, in the same format as the Lark gateway source.
- [x] **L4 Inbox**: `Inbox`: @me, direct chats, replies in topics I take part in ("What the Inbox collects"); bounded `messages-search` polling, Postgres-backed cursor/topic state, grouping and reply detection; collection follows `lark.prefer` and falls back when the gateway is unavailable. Inbox replies use `messages-reply` / `messages-send` as the bot identity. Messages from the app bound to lark-cli are consumed to prevent bot feedback loops; Inbox kinds match the gateway (`dm`, `bot`, `mention`, `group`) and expose chat and sender mute tags.
  - #2691 adds watched document comments (`kind: "doc"`) to the same lark-cli Inbox lifecycle; the document poller has separate watch state, follows the same `lark.prefer` rule, and remains read-only.
- [ ] **L5 `/agent`**: recognize requests by search polling, reactions, context, replies; `/agent` in document comments is not done.
- [ ] **L6 App**: the conversation page's messages, sending, replies, reactions and media wired to lark-cli (same JSON); the document reader uses markdown; unread, labels and Moments are hidden for this source.

One PR per step; all existing tests for hubs with the Lark gateway pass.

## Decided (by the user, 2026-11-03)

| Question | Decision |
| --- | --- |
| One source per hub, or both side by side | **Both may coexist**: one primary source per capability (`lark.prefer`), switching to the other when the primary is unavailable; the Inbox and knowledge base collect from only one source at a time ("Shape") |
| What the Inbox collects from the lark-cli source | **@me, my direct chats, and new replies in topics I take part in within topic groups** ("What the Inbox collects") |
| Polling frequency | **As proposed for now**: Inbox and archiving every 2 minutes, `/agent` every 20 seconds; adjust after measuring Open Platform rate limits in L3 |
| Permissions | **Enable everything possible**: `lark-cli auth login --domain all` (or `lark-cli --profile <profile> auth login --domain all` for a configured profile); permissions needing admin approval are listed on the settings page ("Configuration") |
| Whose identity sends messages (2026-11-04) | **The bot**: sending and replying use `--as bot`, and the phone card says "as the bot"; all other reads and writes still use the user identity |

## Risks

- **Search-index delay**: `/agent` and the Inbox both rely on search; how soon new messages become searchable is untested, so measure it first in L4 / L5.
- **Rate limits**: polling plus the assistant's calls may hit Open Platform rate limits; back off and batch requests.
- **Output format changes with lark-cli upgrades**: a single adapter layer, contract tests, a pinned minimum version.
- **Different message content formats**: Open Platform `content` is JSON per `msg_type` (text / post / interactive …) and must be converted to the knowledge base's current text format.
- **Duplicates when both sources coexist**: while the primary source switches, the same message may arrive from both sides; content-based dedup can misjudge (the same person sending two identical messages in the same second); verify with real data in L3 / L4.
- **The token expires after a week**: the hub calls at least once a day to keep it refreshed; if it really expires, the settings page and the Inbox ask you to log in again, never failing silently.
