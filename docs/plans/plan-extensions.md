# Plan: extension foundation (#1101)

> 2026-11-05: "plugin" was renamed "extension" across the board, aligning with VS Code (umbrella issue #1843, naming and compatibility layer #1844, see `docs/extensions/extensions.md` ("Naming and compatibility")). The phases after the rename (two extension repositories, signing, unified components, SDK, extension model, fine-grained tokens, sandbox, observability, system-level locations) are tracked in #1843.

Status: **the server side of step 1 (the foundation) is done** (1a, 2026-11-04). The spec (manifest, installation, protocol, endpoints, command line) is written in `docs/extensions/extensions.md`, which is authoritative;
this document records the design trade-offs, where the implementation diverged from the plan, and the steps not yet done. Issue #1101 set the direction: one unified foundation + extension points opened by each module, with the skeleton modeled on VS Code (manifest + `contributes` + on-demand activation + separate processes),
permissions modeled on Chrome (explicit declaration, shown at install time), and from WordPress only event notifications, not filters.

## 1. Current state

| | hub extensions (`docs/extensions/extensions.md`) | Connector protocol v2 (`docs/protocol/connector-protocol.md`) |
| --- | --- | --- |
| Where they come from | `extensions[]` in hub.json, written by hand by the owner | Only Telegram: `personad-telegram` is registered whenever it sits next to personad, hard-coded |
| Process | Started by the hub, 2 s to start, restarted with backoff up to 60 s; or only a `socket` is given and it runs elsewhere | Started by `connector/proc`, 5 s to start, backoff up to 5 minutes; stopped when the connector is turned off |
| How they talk | The extension serves HTTP on a unix socket, and the hub forwards `/api/v1/extensions/<id>/…` as is; `GET /health` checks liveness | One JSON per line on stdin / stdout (`internal/rpc`), `init` → manifest → methods; conformance tests |
| Config | `config` (arbitrary JSON) passed in the environment variable `PERSONA_EXTENSION_CONFIG` | `init` carries its section of hub.json |
| Secrets | Handled by itself (`key_file`, `key_command`); also receives the hub's full token | `secrets.*`: the hub stores them encrypted in `connector_secrets` and gives them only to that connector |
| Permissions | None; equivalent to the hub itself | Writes only through `actions.do`, past the hub's gate |
| State | In memory | Switches and options stored under `connectorSettingsKey` in the `settings` table |

Each has half: hub extensions have generic process management and HTTP forwarding, but no manifest, secrets or permissions; connectors have a manifest, secrets and a protocol, but only recognize one hard-coded program. The foundation combines the two.

## 2. Key decisions

1. **One registry, three sources**: built-in (shipped with personad, manifest compiled into the binary), hub.json (the existing `extensions[]`, compatible as is), and installed (package on the hub machine, registered in the database).
   The App and API see a single list.
2. **The manifest is the single source of truth**: capabilities, config fields, secrets, permissions and how to start the process are all in `persona-extension.json`; the hub can list an extension, render its config form and show its permissions without running it.
   The manifest a program reports at handshake can only be a subset of it; on mismatch, the one accepted at install time wins (same as connectors do today).
3. **The process-extension protocol is connector v2 generalized**: the same `internal/rpc` with one JSON per line, the handshake renamed `initialize`, and methods namespaced by extension point.
   Connector v2 methods move into the `connector.` namespace as is (`status` → `connector.status`), with not a single field or meaning changed, and the existing conformance tests keep running.
4. **HTTP forwarding stays, as the second outlet for process extensions**: voice needs to stream an audio WebSocket directly between the phone and the extension, which JSON-RPC does not suit. Extensions with `runtime.http: true` in the manifest
   serve HTTP on a unix socket, and the hub forwards as before. So the existing voice extension is a valid extension without changing a line (HTTP only, no RPC).
5. **Installation happens only on the hub machine and in the admin UI, never from the phone**: installing an extension means running a new program on the hub machine. The phone's device credentials can only view the list, toggle, edit config and enter secrets;
   install, upgrade and uninstall need the hub's master token (only in hub.json on the hub machine; the command line `personad extension …` uses it over loopback) or an admin UI login session.
6. **Step 1 of permissions is "declare + show + record", not a sandbox**: process extensions run as the hub's system user and can read and write everything that user can; the hub cannot control network domains.
   At install time, this sentence is shown together with the declared permissions, and the accepted set is stored; capabilities the hub itself provides (handing out the hub token, later knowledge-base reads and writes, etc.) are allowed per the accepted permissions and refused otherwise.
   Real isolation (bubblewrap / network namespaces on Linux, sandbox-exec on Mac, or egress through a hub proxy) comes later as separate work, see "6".
7. **Config supports only a subset of JSON Schema**: anything outside the subset will not install, which guarantees every extension's config can be drawn as a form in the App, instead of discovering after installation that some field cannot be edited.
8. **Changing config, secrets or the on/off switch restarts the extension process**: step 1 does no hot reload, which removes one state. A connector's own "options" (`connector.Options`) are unaffected and still change live.

## 3. Where the implementation diverged from the plan (1a)

- **The admin UI's install page came later (#1208)**: in 1a, install, upgrade, rollback and uninstall only accepted the master token over loopback (`personad extension`); later the admin-session path was added
  (`/api/v1/admin/extensions…`, CSRF, preview one at a time + 10 per 10 minutes, audited as `admin UI`), plus browser upload (`upload:<file name>`) and `added_permissions` in the upgrade preview.
- **The protocol gained a basic method `manifest`**: `initialize` returns only the id, version and contributions, but installing an extension that runs elsewhere needs the full manifest (permissions, config, secrets), so `manifest` was added
  (callable before `initialize` too; extensions the hub starts from a package may skip it). Remote HTTP-only extensions answer `GET /manifest`.
- **An extension that contributes a connector is held by the connector's switch**: its `on_call` does not count (otherwise "sign in while off" would start the program, unlike current behavior); `on_startup` still keeps it running.
- **Connector programs can get the secrets the user entered**: `Init.Declared` in `connector/serve.MainWith`; the original `serve.Main` is unchanged.
- **Built-in Telegram's version is the hub's version** (not semver): built-in extensions skip `Parse`, and `engines` is replaced by the `version -v` check.
- **The list gained `can_install`** (whether the hub has a database), and `features` gained `extension_manager`.
- **Automatic rollback on a failed upgrade only covers extensions that would start on their own** (`on_startup`, or the connector is on): on-demand extensions are not started just to check during an upgrade.
- The migration number used is `0291_plugins.sql` (avoiding other lanes' number ranges).

## 4. Steps

- [x] **1a: server foundation**: `internal/extension` (manifest, schema subset, permissions, version ranges, package staging and unpacking, registry), `extension/host` (process host, on-demand activation, backoff, timeouts, ping, HTTP, remote socket),
  `extension/serve` (the program side), `extension/conformance`; migration `0291`; hub endpoints and `personad extension`; voice (hub.json) and Telegram (built-in) moved over;
  `connector/serve` recognizes the extension handshake, `connector/proc` can talk through an extension process (`Link`); docs `docs/extensions/extensions.md`, `docs/protocol/connector-protocol.md`.
- [x] **1b: App Settings › Extensions** (iOS, page shared with Mac): list, details (permissions, contributions, about), config form drawn from `form.fields`, write-only secrets, switch;
  mock hub `MOCK_EXTENSIONS=1`, UI tests and unit tests, screenshots checked in default / BeautifulUI styles × light and dark (`docs/extensions/extensions.md` ("App: Settings › Extensions")).
- [x] The admin UI's **Extensions** page (#1208): list, switch, install (directory / package / https / upload / remote, preview first), upgrade (an explicit warning when permissions grow), rollback, uninstall (data can be kept).
- [x] The web extensions page (#1207).
- [x] The Android extensions page (#1206, `docs/android/android.md` ("Extensions (Settings › Extensions)")).
- [x] **Step 2 (server)**: extension point `quota` (extensions never see the Key: `quota.query` + `quota.http` sent on their behalf by the hub / node, placeholder `${launcher_key}`, same-origin restriction, permission `launcher_keys`),
  `providers` (declare providers, `GET /api/v1/launchers/presets`, new launchers carry `preset`); node's `launchers.quota` replaced by `launchers.http`; `server/internal/quota` keeps only the generic part,
  and the two gateways' readers moved to a private extension repository (ownership in `CLAUDE.local.md`). The spec is in `docs/extensions/extensions.md` ("Quota", "Model providers").
- [ ] Step 2 (App, #1259, in progress): the quota gateway name uses the `gateway_name` from the hub (no longer hard-coded), the mock hub's example gateway becomes a neutral one; **Settings › Model Providers** gets a new **From Extension** option.
- [x] Both quota extensions are in the private extension repository and were tested on the user's hub: through node relaying, they fetched the quota of `~/.zshrc` launchers; unknown gateways answer "can't check".
- [x] Step 3 (server): the whole Lark chain of the Lark gateway (including Moments) moved into a private extension, and the hub's own copy was deleted (3c, #1520); see "Step 3 plan" and "How step 3 went" below. Compatibility with custom Codex forks remains; internal product names are not published.
- [x] Step 3 (clients, 3d, #1530): the Lark UI shows according to `lark` / `lark_moments` in `features` (web #1542, iOS / Mac #1575, Android #1622), with the generic name **Moments**, and post titles using the archived feed name.
- [x] **Step 4 (server, #1284)**: extension point `ui` (`plugin_page` / `launcher_section`, `ui.render` / `ui.action`; a View is a few fixed kinds of items, checked by the hub with `extension.CheckView`;
  the launcher-page section reaches the gateway with the Key through `gateway.http`, and the extension never sees the Key) and `events` (`inbox.changed`, `agent.needs_you`, `agent.finished`, notify-only, subscriptions agreed to together with permissions).
  The spec is in `docs/extensions/extensions.md` ("Declarative UI", "Events").
- [ ] Step 4 (clients): iOS / Mac, Android and web draw Views (pages in extension details, a section on the launcher page).
- [x] **UI slots (server, #1451)**: the slot table (`nav_page` main navigation entry, `overview_card` overview card, `machine_section` a section of machine details), render context (time zone, language, platform, size, UI version),
  `chart` / `stat_grid` / `segmented`, `refresh_after`, `ui.badge` badges; main navigation / overview / machine details count toward "It can" and the permission summary by contribution; for old Apps the hub strips new items by UI version. The spec is in `docs/extensions/extensions.md` ("UI slots").
- [ ] UI slots (clients): web, Android and iOS / Mac draw the new slots and items.
- [x] **Extension market (server, #1303)**: a repository / website publishes `persona-extensions.json`; the hub registers markets (GitHub private repositories with a read-only token), with catalog and search; devices with full permissions install, upgrade and uninstall from a market;
  command line `personad extension market … / search / install market:…`. The spec is in `docs/extensions/extensions.md` ("Extension market").
- [x] A git repository as a market (#1373): `git:<address>`; the hub shallow-clones using the hub machine's SSH credentials, for private git hosts without a token-based file-reading API.
- [ ] Extension market (clients): the **Extension Market** page on iOS / Mac, Android, web and the admin UI.

## Step 3 plan: moving the Lark gateway into an extension

Audit (2026-11-04): the largest piece of the open-source repository that only makes sense for one particular service is the whole Lark chain connected through the **Lark gateway** (a third-party, unofficial Lark web client that signs in as the user):
the `server/internal/lark` package (about 3000 lines), the hub's `lark*.go`, Inbox / knowledge-base crawling and linking of Moments (Lark Moments), node's built-in Lark tools, the copy of the Lark gateway skill under `skills/`, node's write gate,
a batch of prompts and goldens, and the client Lark chat pages drawn after the Lark gateway's data shapes. Decided: **move the whole chain into a private extension repository**; the open-source edition keeps the public capabilities: the official `lark-cli`, the Lark bot and Lark mail.

Goal: with the extension installed, Lark in the App is exactly as it is now; without it, the open-source edition has nothing related to the Lark gateway (code, prompts, docs, test data), and Lark goes only through `lark-cli`.

### New extension points (open-source repository, generic)

| Extension point | Extension provides | What the hub does |
| --- | --- | --- |
| `api` (version 1) | `{id, prefix, features?, events?}`: a set of HTTP endpoints for the App, on the extension's HTTP side | Forwards `/api/v1/<prefix>/…` to the extension (authentication done in the hub, as with `/api/v1/extensions/<id>/…`); `prefix` must not clash with the hub's own endpoints; `features` are added to the `features` of hello / `/health` (clients use them to decide what to show); the extension sends `api.broadcast {type, …}` to push to online clients, where `type` must be one declared in `events` |
| `connectors` (existing, two additions) | Descriptions of Inbox kinds (`kinds` carries `{kind, description}`, used by the model for triage, replacing `prompts/inbox/kinds/<id>.md` in the open-source repository); knowledge-base items (`items`) already exist | Unchanged |
| `assistant` (version 1) | A skills directory (`skills/<name>/` in the package), fragments of the assistant instructions (markdown), tool descriptions for coding Agents | Skills are distributed to every computer along with the assistant's other skills; fragments are appended to the assistant's CLAUDE.md (prompt goldens still guard the open-source part) |

### Phases

1. **3a extension points** (open-source repository, #1262, done): `api`, `assistant`, and connector kind descriptions; Lark entry points in the hub now go through the registry (when no extension provides `lark`, Lark endpoints, tools and Inbox sources do not appear, and the App hides them based on `features`).
2. **3b extension** (the extension that connects to Lark, in the private extension repository): move `internal/lark` and the hub's Lark gateway logic over as a process extension: `api` (the existing `/api/v1/lark/…` endpoints as is, data shapes unchanged, no App change), `connectors` (tools, Inbox, knowledge-base archiving and items, live events), `assistant` (the Lark gateway's skills and instructions, write-gate rules), `/agent` (through `hub_api`). Verified against the real Lark gateway on the user's hub.
3. **3c cleanup** (open-source repository): delete the Lark gateway parts of `internal/lark`, the hub's `lark*.go` / Moments, prompts and goldens, the Lark gateway skill copy under `skills/`, node's write gate, sync scripts, and related content in docs and test data; keep `lark-cli`, the Lark bot and Lark mail. Deploy, install the extension on the user's hub, and confirm the Lark Inbox, chat, knowledge base and assistant all behave as before.
4. **3d clients**: the Lark chat page shows based on `features` (when the extension provides it); the Moments UI becomes the generic "Lark Moments", with names and groups coming from the extension, never hard-coding any tenant's name.

One PR per phase; 3b and 3c depend on each other, and 3c merges only after the extension is installed and verified.

### How step 3 went (2026-11-04)

While the extension was taking over, the hub gained these generic capabilities (one issue / PR each; specs in `docs/extensions/extensions.md`, `docs/protocol/connector-protocol.md`):

| Gap | Solution |
| --- | --- |
| Connector id `lark` was reserved by the hub, so the extension could not install | Migratable connector ids (`connector.MigratableConnectors`): the hub's yields when the extension is enabled and returns when it is disabled; after 3c the hub has none of its own and simply doesn't reserve it (#1320) |
| Forwarding extension HTTP: `Cache-Control` doubled, `%2F` split apart | The extension's cache headers win; escaped paths reach the extension as is (#1320) |
| Continuity for the existing blocks of Inbox items (chats, document rows, Moments posts) and old items | A connector's thing carries `extra`, which the hub reads only for migratable connectors; old items are matched to threads by block, new items use the `legacy_id` from the extension; replies and state changes go through `inbox.reply`'s `client_id` and `changes` (#1320) |
| Knowledge-base event / meeting / document titles, attachments in chats | The protocol adds `items.list`, `items.doc_title`, `archive.media` (#1372) |
| All of Moments into the knowledge base, posts that involve me | The program calls the hub's `posts.put` / `posts.comments` / `posts.pending` and `archive.chats`, with `archive_sources` in the manifest; post archiving, cleanup and project linking stay in the hub (#1444) |
| The assistant's write gate and credentials | `assistant.gate` (gate file in the package) + `assistant.credentials` / `assistant.summary`; node's gate, command wrapping and runtime tools follow the gate given by the hub (#1468) |
| /agent | Opening a chat carries `purpose` and `machine_id`; `GET /api/v1/kb/chats/{id}/context`; when the extension takes over, the hub's `lark_agent_reply` and /agent yield (#1484) |
| Personal Agents (a source on the Agents page) | The `agent_sources` extension point (#1400) + migratable source ids, session ids unchanged (#1497) |
| Send receipts, linked-hub roles, duplicate live events | `api`'s `receipts`; `hub.role` in `initialize`, restarting the extension when the role changes; the hub's event loop stops when the extension takes over (#1503) |
| Unread chats and today's meetings in the assistant snapshot and the Today card | Connector capability `snapshot` (`snapshot.get`) (#1520) |
| Clients show Lark per the extension | The extension's `api` reports features `lark` and `lark_moments` (#1530) |

Lessons:

- **First add the capabilities, let the extension take over and verify it on a real hub, then delete the hub's own copy**: during the takeover both copies coexist, and every one of the hub's own loops (live events, /agent, crawling, personal Agents) needs a "yield when the extension takes over" check;
  miss one and you get two copies (the /agent event handling missed it once: the check was placed after the handling).
- **Data continuity relies on unchanged ids**: connector ids, knowledge-base chat ids (`<source>-<id>`), Inbox item ids, personal Agent session ids and derived send-receipt ids all stay as they were;
  when the extension first takes over, it continues from the hub's existing state (`archive.chats`, old items matched by block), with no rereading, no duplicates and nothing missed.
- **Code in the extension repository can carry internal details; the open-source repository keeps only the extension points**: gate rules, prompts, skills and the parts of client data shapes that only mean something to one service all go into the extension package, and the hub only knows generic formats.
- Without the extension, the open-source edition behaves as if no Lark gateway were configured: Lark goes only through lark-cli and the bot; the old `lark.container / token / base_url`, `lark_agent` and `kb.moments` in hub.json are no longer read;
  if present, the hub logs one line at startup. The steps to move them into the extension config (container name, address, account, /agent switch and launcher, full Moments crawl) are below.

Moving the old hub.json config into the extension: after installing the extension, fill in the container name (or address + the token in secrets) and account under **Settings › Extensions** (or `personad extension config <id> key=value`);
`lark_agent.enabled` / `launcher` / `machine` map to the extension's `agent` / `agent_launcher` / `agent_machine`; `kb.moments.enabled` maps to `moments_crawl`. Delete these sections from hub.json only after confirming the extension has taken over.

## 5. Migrating the two existing systems

### Voice extensions (`extensions[]` in hub.json)

- Each hub.json entry is still read as before, and the hub synthesizes a manifest for it: `{id, name, version: <hub build number>, runtime: {command, args, env, rpc: "none", http: true}, contributes: {<kind>: [{id}]}, permissions: {hub_api: true}}`;
  an entry with only `socket` is `remote:` and the hub starts no process. The meanings of `dir`, `env`, `config`, `disabled`, and `command: "personad" / "personad"` are unchanged.
- Behavior is unchanged: socket paths, environment variables (including the hub token), `/health` liveness checks, stripping App credentials when forwarding, 403 for read-only devices such as watches, and `plugin.<id>` model config.
  The only change is that the maximum backoff rises from 60 seconds to 5 minutes.
- Old fields in `GET /api/v1/extensions` are unchanged (Android's voice entry looks for `kind == "voice"` and `status == "running"`); only fields are added.
- Voice config (`providers` is an array of objects) is outside the form subset, so in step 1 it stays in hub.json rather than becoming registry config; revisit when the subset supports object arrays.
- The existing assertions in `server/internal/extmgr/extensions_test.go` stay unchanged and must still pass.

### Telegram (connector v2)

- Becomes a built-in extension: its manifest is compiled into personad (`id: "telegram"`, `runtime.command` is `personad-telegram` next to personad, `contributes.connectors: [spec.Manifest()]`),
  registered only when the program is present, as today; `engines` requires the same build as the hub (the current `version -v` check stays).
- The connector id, session in `connector_secrets`, switches and options in connector settings, and default-off behavior are unchanged. Enabling the built-in extension and the connector's own switch are two layers: the extension is enabled by default, and normally users touch only the connector's switch.
- Connector conformance tests (GitHub, Messages, Telegram) keep running, once for each of `connector/serve`'s two handshakes; Telegram proc tests change to start the program through the extension host.

## 6. Out of scope / later

- ~~Sandboxing (real file and network isolation), CPU / memory limits, extension signing~~: signing #1848 and sandboxing, egress proxy and resource limits #1856 are done (`docs/extensions/extensions.md` ("Signing", "Sandbox", "Egress")).
  Still not done: sandboxing node-side extension programs (the `node` section), resource limits on Mac, and sandboxing extensions from hub.json. The registry is still out of scope.
- Dependencies between extensions, one extension calling another: not doing these.
- WordPress filters (extensions changing hub data): never; event hooks only notify.
- Linked hubs: an extension belongs to the hub where it is installed; step 1 does not sync it within the group; through the primary hub, the App sees the primary hub's extensions.
- Multilingual manifests (`name`, `description`, form titles): one language first, `l10n` later.

## 7. Running an extension on selected computers (node-side program, proposal, #1830)

**Problem to solve**: extension programs currently run only on the hub (`runtime`), but some capabilities can only run on a particular computer:

- iMessage: reading the iMessage database. Currently five methods (`connector.*`) hard-coded in node, with one machine selected by `imessage.machine` in hub.json.
- cua / ego: driving cua-driver and ego lite. Currently node code compiled into personad (`node/computer*.go`, `node/browser*.go`), plus declaration-only built-in extensions.
- Third-party extensions: cannot do these things.

### Manifest: the `node` section

A sibling of `runtime`. Both can exist (one program on the hub, another on the computer), or only one.

```json
"node": {
  "command": {"darwin-arm64": "node/bin/helper", "darwin-amd64": "node/bin/helper-x64", "linux-amd64": "node/bin/helper-linux"},
  "args": [], "env": {},
  "machines": "machines",
  "rpc": "stdio",
  "http": true,
  "keep_running": "lease",
  "secrets": ["api_key"],
  "permissions": ["network", "files", "macos_privacy"]
}
```

Fields:

- `command`: same syntax as `runtime.command`, selected by platform (`Command.For`). Its keys define the supported platforms; no separate platform list.
- `machines`: the config field used to select machines. This field must have `type: array` and `items.format: machine`; there is still one switch per machine (the existing `format: machine` rendering).
  `items` must specify `x-os`, restricted to systems with a program in `command` (validated by the hub); unsupported machines cannot be selected. For only one machine, set `maxItems: 1` on this field.
- `rpc` / `http`: extension protocol 1 over stdio; for HTTP, listen on `PERSONA_EXTENSION_SOCKET`, as on the hub side.
- `keep_running`: what the program does when the hub cannot connect, see "When node is offline" below.
- `secrets`: which ones to send to node (names declared in `secrets[]`); the rest stay only on the hub.
- `permissions`: node-side permissions, see "Permission display" below.

Validation:

- The field referenced by `machines` must exist with the right shape, and `x-os` must match the program's platforms;
- `secrets` can contain only names present in `secrets[]`;
- Installation no longer requires a program for the hub's platform, only that `runtime` (if present) can run on the hub and that `node` has at least one platform.

### Delivering installs and upgrades to node

**Package**:

- One package contains the node programs for every supported platform (for example under `node/`); installation checks that each exists and is executable.
- The market stays unchanged: it picks one package for the hub's platform, and that package includes programs for every computer platform (a larger package; if this becomes a real problem, add per-platform `{url, sha256}` to market entries later).
- Once installed, each platform's program is under `installed/<id>/<ver>/`, and the hub sends each machine the one for its platform.

**Delivery**:

- New RPC `plugin.node.put` (begin / chunk / commit, 1 MB chunks, with sha256), reusing chunking and verification code from `node.update.*`;
- Deduplicate by content digest, as with skills delivery (`assistant.prepare`); do not transfer the same version twice;
- On node, store it in `~/.persona/node-plugins/<id>/<ver>/`; the data directory `~/.persona/node-plugins/<id>/data/` survives upgrades (not shared with the hub's `~/.persona/extensions/`, #1933).

**Upgrade**:

- Upgrade the hub side first (the current flow), then each machine: deliver the new version → node stops the old one and starts the new one → successful `initialize` within 30 seconds counts as healthy; otherwise node rolls itself back to the previous version.
- Keep only one old version, as on the hub side.
- Record each machine's result and show it in settings.

**Version**:

- `node`-side and hub-side versions must match (the same package).
- Machines that missed an upgrade while offline receive it upon reconnecting. Until caught up, calls to that machine return 409 `plugin_updating`, rather than forcing the old program to run.

**Disable, remove a machine, uninstall**:

- Node stops the program and deletes the version directories;
- Uninstall also deletes the data directory; disabling or removing a machine keeps it.

### When node is offline

**Reconcile, don't queue**:

- The hub stores only the desired state: extension config, version and selected machines in Postgres (all already present).
- Whenever node connects (the reconnect hook in `Machine.serve`, alongside `refreshComputer`), the hub sends a full reconciliation `plugin.node.sync`: which extensions should run, which versions, which digests.
- Node receives missing programs, stops ones that should not run, and deletes ones no longer listed.
- Thus installs, upgrades, disables and uninstalls while offline need no queue; reconnection brings everything into line.

**Calls while offline**:

- As with cua / ego today, HTTP returns 409 "Machine offline", and the extension's hub-side program receives the error.
- In App settings, each machine shows "Running / Offline / Update pending / Error", with a reason on error.

**The computer's program when the hub cannot reach node**:

- `keep_running: "lease"` (default): the hub renews the lease every 10 minutes (as with `computer.nolock`); after 30 minutes without renewal, node stops it and restarts it on reconnection.
- `keep_running: "always"`: do not stop when the hub disconnects, for things like iMessage that need continuous collection and should not stop during hub restarts.

**Secrets never go to node's disk**:

- Sent only in `plugin.node.start` over the existing hub↔node connection and passed to the program in memory (`initialize.secrets`).
- Thus after node restarts, the program waits for the hub to connect before starting. `always` means "don't stop when the hub disconnects", not "start on its own after node restarts".

### How the hub starts, stops and forwards

**Host**: extract process management from `plugin/host` (start/stop, restart with backoff, close stdin → SIGTERM → SIGKILL) for both hub and node to share; node runs the same implementation.

**RPC**:

- `plugin.node.start{id, version, config, secrets}`, `plugin.node.stop{id}`, `plugin.node.status`;
- `plugin.node.call{id, method, params}`: forward to the program's extension-protocol method;
- Node converts program notifications (`changed`, etc.) into `plugin.node.event` and sends them to the hub, which hands them to the hub-side program or connector framework.

**Hub-side programs calling programs on computers**: add `nodes.call{machine, method, params}` to the extension protocol. Available when the manifest has `node`, with no extra permission. For node-only extensions, the hub forwards App requests directly.

**HTTP**:

- Route `/api/v1/extensions/{id}/machines/{mid}/…`: hub → stream (`internal/streams`, currently dialing only TCP ports, with a new target to dial an extension's unix socket) → the program's socket on node.
- Both ordinary requests and WebSockets use streams. As with hub-side HTTP, full devices only, with `Authorization` stripped.

### Permission display

**The "Run a program" row states where** (`perm.Explain` currently always says "Run a program on the hub machine"):

- With `runtime`: "Run a program on the hub machine";
- With `node`: "Run a program on these computers: MacBook, Mac Studio", listing the actual selected machines; if none are selected, "Run a program on the computers you select".

**Node-side permissions get a separate group**:

- `network`: network access;
- `files`: read and write files outside its own data directory;
- `macos_privacy`: request system permissions such as Full Disk Access and Accessibility.

**When consent must be renewed**:

- `perm.Digest` includes the node section (platforms, permissions, names of secrets sent to node); changes require renewed consent (the existing `Added` lists additions).
- Adding a machine to an extension also needs confirmation: turning its switch on shows "Run <extension>'s program on <machine>" (`x-confirm`).

### Migrating cua / ego

**The first step changes routing, not code**:

- Their node code stays in personad, registered as "built-in node-side programs": the same interface implemented in-process, no child process, `node.command` set to `builtin` in the manifest.
- The hub switches to `plugin.node.call{id: "cua", …}`. Old `computer.*` and `browser.*` method names remain for one version for nodes not yet upgraded.
- The `machines` field, switches on the machine page, and `PUT /machines/{mid}/computer|browser` are unchanged; users see no difference.

**What this buys**:

- All three extensions share "Run on these computers" in permissions, offline reconciliation, and per-machine status in settings;
- Separate reconnect code such as `refreshComputer`, `refreshBrowser` and `tellNoLock` is consolidated into `plugin.node.sync`.

**Later**: if they need standalone programs (for example to move into a private repository), distribute them as ordinary node programs then; there is no such need now, so do not split them.

### How iMessage uses it

- **Becomes extension `imessage`**: `node.command` has only `darwin-*`, the machine-selection field has `maxItems: 1`, and `keep_running: "always"`.
- **Node program**: the current Reader (`modules/imessage`), answering `connector.list/chats/older/changes/search` over stdio using the connector protocol.
  The hub-side connector framework forwards calls through `plugin.node.call` and returns `changed` notifications. Connector conformance tests keep running.
- **Config**: move from `imessage.machine` in hub.json to the extension's `machines` field. On the first startup during migration, read the old value and write it there; keep the old field read-only for one version. The "Send to Persona" switch is unchanged.
- **Full Disk Access**: macOS attributes child-process permissions to the "responsible process", namely the node that launches it, so step 1 remains as today: grant node permission.
  The "helper with its own permissions" in `plan-connectors.md` would need to be a LaunchAgent, left for later: `node.launch: "launchd"`, launched by node through launchd; not in step 1.
- **Future sending**: through the gate (`connector_gate`), as currently planned.

### Steps

1. Manifest `node` section, validation, permission text and digest, node programs in the package: hub-only changes, with tests. (Merged, #1878.)
2. Node-side host and `plugin.node.*`: delivery (chunks, digests, deduplication), start/stop, reconciliation, leases, plus the hub-side reconnect hook; test with fake nodes and real processes.
   (Done, see `docs/extensions/extensions.md` ("Programs running on computers"): one deterministic package per platform; process management in `host` shared by hub and node; programs on node belong to the process, not a particular connection.)
3. HTTP / WebSocket forwarding: streams can dial unix sockets. (Done: `/api/v1/extensions/{id}/-/machines/{mid}/…` through `plugin.node.dial`;
   the path lives under the hub's own `/-/`, leaving the extension's paths free.)
4. App (iOS, Mac, web): per-machine status, confirmation when adding a machine, new permission rows; show screenshots to the user.
   (Done: status appears in each row of the computer-selection field; the hub inserts confirmation into the field's `confirm`, which the App draws as before; the manifest JSON Schema and types generated for all four clients include `node`. Android plugin settings have not caught up.)
5. Switch cua / ego to built-in node programs, without UI changes.
   (Done: node reports `builtin_programs` in `node.info`; the hub calls reporting nodes through `plugin.node.call`, and uses `computer.*` / `browser.*` as before for old nodes, see `docs/extensions/extensions.md` ("Built-in node programs").
   Final cleanup (last PR for #1830): cua, ego and imessage manifests declare `node.command: "builtin"`; `Manifest.NodeProgram()` returns nil for them, with consent digests and UI unchanged;
   reconnect's `refreshComputer`, `refreshBrowser`, `tellNoLock` and `tellCursor` move into `reconcileNodePrograms`, sharing one entry point with installed-package sync.)
6. Migrate iMessage into an extension and migrate its hub.json config.
   (Done: built-in extension "Messages (iMessage)", one Mac-selection field in settings, with only one allowed on; the connector is unchanged, taking the selected machine from the extension and falling back to hub.json if none is selected;
   hub.json's value is migrated once at startup (name to id, enable the extension, record a marker), with hub.json still read if migration fails; node's `builtin_programs` adds `imessage`,
   method names stay `connector.*`. See `docs/extensions/extensions.md` ("Built-in node programs") and "Step 6 decisions" below.)

After steps 1–3, third-party node-side extensions can be installed; steps 5 and 6 each get their own PR.

### Step 6 decisions (2026-11-05, user: "follow all recommendations")

- Add "Messages (iMessage)" under **Settings › Extensions**; if **Settings › Connectors › Messages** is not configured, direct users to **Settings › Extensions › Messages** to select a Mac.
- Turning on a second Mac while one is already on: the hub refuses with "Only one Mac can be read; turn off <that machine> first".
- The user's hub has no iMessage config: use the generic open-source migration (there is no user value to migrate).
- The proposal's "mark the hub.json entry read-only in the admin UI" is unnecessary: the admin UI never had an iMessage entry.

### Decisions (2026-11-05, all four points adopted as proposed)

- **Programs on computers when the hub cannot connect**: stop by default, with a 30-minute lease (renewed by the hub every 10 minutes); extensions may declare `keep_running: "always"`.
- **Version**: node-side must match hub-side (the same package); no version range.
- **Extensions with only `node`, no hub-side program**: allowed; App requests are forwarded directly to node.
- **Can node-side programs call the hub API (`hub_api`)?** No; use hub capabilities through the extension's own hub-side program.
