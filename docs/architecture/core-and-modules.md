# Persona Core and feature modules

Who this serves: people who change Persona's code, on any platform. Nothing here is visible to someone using Persona:
with every module on (the default) the product is what it was.

Persona is split into **Persona Core**, the parts everything else stands on, and **feature modules**, optional features
that reach the core only through one declared contract. The core never imports a module, so a module can be left out of
a build, switched off without one, or tested on its own. Decisions and stages: #13430 (tracking #13411); the server
half: #13408.

## What Persona Core is

"Persona Core" is a product-layer term. It holds (#13430 section 1):

- the Assistant main chat, with its memory (the knowledge-base files and search, the `kb_*` tools) and composer dictation;
- Agents: handing work to coding Agents, supervision with its follow-up scheduler, and `claudeplugin`;
- To-dos and the task board;
- Computers: machines, Link, the node with Claude Code, Codex and herdr;
- Inputs: the connector and channel framework, and the Inbox shell;
- hub plumbing (transport, auth, push, pairing), the extension host, and the shell mechanics (navigation, the settings
  shell, the design system);
- the north-star metrics, the yardstick of the core flow.

The membership test: if the owner can say "I never use this" and the assistant, Agents, to-dos, computers and connectors
still work, it is a module. The first set (voice, recording, lark, mail, health, briefing, ...) is in #13430 section 2.

No code is renamed for the term. The names that already say "core" mean something else:

| Name | What it is |
| --- | --- |
| `apple/Packages/PersonaCore` | The Apple binding of the shared Rust client library; it keeps its name. The Apple layer for Persona Core is `PersonaHostApp` |
| `native/app-core` | The shared Rust client logic every Persona Core client depends on; module-specific domains never go into it |
| `android/core/*` | Android's infrastructure Gradle modules |
| `server/internal/herdrcore` | An engine library |

## The module contract

One manifest concept, with the same field names on every platform: `schema/module.schema.json`. A module's manifest says
who it is (`id`, `api`, `origin`: `builtin` or `extension`, `title`), what it depends on (`needs`: must be loaded; `wants`:
used when loaded), and one list per kind of slot it adds to: `feature` (a wire feature flag), `hub_routes`, `mcp_tools`,
`prompt_sections`, `settings`, `shell_places`, `chat_card_kinds`, `composer_actions`, `chat_accessories`,
`background_jobs`, `schedules`, `db_migrations`, `i18n_namespaces`, `node_programs`, `channel_kinds`, `frame_handlers` and
`push_categories`. A module adds nothing it does not declare, and a host refuses a contribution the manifest does not
name. Where an extension point exists or is filed, the slot uses its name (#13400 to #13403), so built-in modules and
extensions share one registry shape and a module can later become an extension.

**A disabled or excluded module is absent.** That is the contract: its routes answer 404, its tools and feature flags
are not announced, it runs no loop, its settings keys stay stored but are hidden, and its tables stay. The core without
modules is a tested product.

What the server enforces today is only the slots it serves (see "Slots" below): a module's settings are hidden but
stay stored, its routine facts are gone (the routine opens with none), and its tables stay, since no module owns one
yet. A module that declares a slot the host does not serve is not loaded.

## Classification

`schema/module-classification.txt` (#13843) is the one list of what is Persona Core and what belongs to a module. It
classifies every unit as `core` or a module id: each Go package directory under `server/` and `sdk/go/`, each Apple
directory `apple/<name>`, `apple/Persona/<name>` and `apple/Packages/<name>`, each Gradle project
`android/settings.gradle.kts` includes, and each directory under `web/src` one and two levels deep. A unit that is a
module's future home gets the module id even while it still lives in a core path (`server/internal/lark` is `lark`), so
the list shows how far each module is from its own directory.

One line per unit, `<kind> <unit> <core|module id>`, with `module <id>` lines declaring the ids (#13430 section 2 plus
the modules already built). The most specific line wins. `<dir>/... <id>` gives a whole subtree to a module, and the
module roots (`server/internal/modules/*/...`, `web/src/modules/*`, `:module:*`) give each directory below them the
module its name says. Core is never inherited: each core unit has its own line, so nothing becomes core without
someone writing it down. A line may also name a directory deeper than the units (`apple/Persona/Views/Mail mail`).

`scripts/test/test_module_classification.py` (run by `scripts/ci/scripts.sh`) fails on a unit no line classifies and on
a line that matches nothing. To add a package, directory or Gradle project, add its line; to move one into a module
root, delete its old line (the root's line classifies the new place); `--print` lists every unit with its class. The
Apple module ratchet (`scripts/test/test_apple_module_refs.py`) takes the directories of the modules it guards from the
file; the server, web and Android guards still key on their module roots.

## Server

| Piece | Where | What it is |
| --- | --- | --- |
| Module API | `server/internal/modapi` | Pure types: `Info` (the manifest), `Module` (`Info()` + `Register(Host)`), `Host` (everything a module may use of the core) and the contribution types. A contract package (`server/internal/archguard/testdata/contracts.txt`) |
| Modules | `server/internal/modules/<id>` | One package per module, with its `module.json` (embedded: `Info()` reads it). No nested Go modules |
| Composition root | `server/internal/modules/all` | The one list of built-in modules, one file per module with its build constraint. `internal/manager`, `internal/managercli` and the binaries `cmd/personad` and `cmd/persona-manager` link it |
| Provider seams | `server/internal/builtinext` | What the core needs of a module that runs as a built-in extension program, declared by the core and provided by the composition root (see "Provider seams") |
| Host | `server/internal/assistant/kernel` | Loads the modules compiled in and switched on, serves `modapi.Host` to each, and answers the core's questions about what they added |

The core asks the kernel (`s.modules` in package assistant, `server/internal/assistant/modules.go`), never a module's
package. A module may later keep its own prompts in its own tree, under the same rules as `server/internal/prompts/`
(#13430, decision 5); none does yet.

### Loading

The manager loads modules once, in `assistant.New`, before `New` returns. Core routes are registered first so their real
patterns reserve the core namespace. Module registration finishes during construction; background jobs start only with
`Service.Run`. The kernel leaves out a module that is
switched off, is written for another `api`, is not `builtin`, declares a slot the host does not serve yet, has a feature
that is not exactly `module.<its own id>`, has a route or tool that conflicts with the core or an already accepted
module, needs a module that does not load (wherever it stands in the list; a cycle of needs loads none of them), or whose
`Register` fails. A module registers after the modules it needs and after those it wants that load; otherwise list order
holds. `wants` never keeps a module from loading. An id listed again is ignored after its first entry. A module left out
adds nothing, including what it added before failing; why is kept (`Kernel.Skipped`) and logged.

### Slots

`modapi.Info` declares every slot of the contract so manifests can be written against the whole of it. The server
serves only what the pilot needs so far; a module that fills any other slot is not loaded, so nothing it expects goes
missing quietly.

| Slot | Served | How |
| --- | --- | --- |
| `settings` | Yes | `Host.AddSetting(modapi.Setting)`: the module says what a value means (`Value`, `Check`, `Describe`); the core keeps it stored, lists it in `persona_settings` and asks the owner to approve a change |
| `feature` | Yes | The manifest value is `module.<id>`. While the module is loaded it joins the manager hello's `features`; the hub forwards it while that default manager is connected. Every built-in module declares it, and the manager announces the client-only modules' flags itself (below) |
| `hub_routes` | Yes | `Host.AddHubRoute(pattern, handler)`: the assistant serves the declared `METHOD /api/v1/…` route only while the module is loaded |
| `mcp_tools` | Yes | `Host.AddMCPTool(...)`: the assistant lists and dispatches the declared tool only while the module is loaded |
| `background_jobs` | Yes | `Host.AddBackgroundJob(name, run)`: `Service.Run` starts the declared loop under its context; an absent module starts none |
| `schedules`: `facts` | Yes | `Host.AddScheduleFacts(modapi.ScheduleFacts)`: the wait rules of the facts a scheduled routine with facts on opens with |
| `schedules` (others), `prompt_sections`, `db_migrations`, `i18n_namespaces`, `node_programs`, `channel_kinds`, `frame_handlers`, `push_categories`, `shell_places`, `chat_card_kinds`, `composer_actions`, `chat_accessories` | Not yet | Declared but not served: each gets a `Host` method when its first module moves (#13408 remaining work, #13400 to #13403) |

Route validation uses the real `http.ServeMux` conflict rules, including equivalent wildcard patterns, against the core
and previously accepted modules. The reserved tool names include conditional core tools and compatibility aliases. A
conflict rejects the module's complete contribution before the live mux changes; core tool dispatch remains authoritative.

A manager that publishes module flags also announces the reserved `modules` marker, including when every module is off
or built out. Clients treat a module with a `feature` as off only when the hello has `modules` and lacks that feature;
no hello, or no `modules` marker from an older hub, leaves it on. This keeps the default behavior on older hubs. The
marker and the module flags disappear from the hub hello when their default manager disconnects. Extension reports cannot
publish `modules` or anything in the `module.` namespace.

Because a missing flag means off, every flag a client gates on has to be announced by a publishing manager:

- **Every built-in module declares `feature`** (`module.<id>`), also one that registers nothing with the kernel
  (`voice`, `volc`, `mail`): `TestEveryBuiltinModuleAnnouncesItsFlag` in `server/internal/modules/all`. Built out or
  switched off, its flag is gone and the clients hide their half.
- **Client-only modules** (`modapi.ClientModules`: `glasses`, `health`, `knowledge-ui`, `screen-share`)
  have no server half, so the manager announces their flags itself. The `modules` settings row switches one off the
  same way (`{"disabled": ["health"]}`), which is how the owner turns a client module off from the hub without a new
  build. An id leaves the list in the PR that gives the module a server half (`TestNoBuiltinModuleIsListedAsClientOnly`).
  A client that starts gating a new module adds its id here in the same PR, or the module disappears on current hubs.
  `scripts/test/test_module_flags.py` (run by `scripts/ci/scripts.sh`) reads the ids the web, Android and Apple modules
  gate and fails on one no hub announces.

### The switch

- **At build time**: the build tag `persona_without_<id>` leaves one module out (a `-` in the id is `_` in the tag, which
  Go's build constraints need: `persona_without_screen_share`), `persona_core_only` leaves every module
  out. `go build -tags persona_core_only ./...` must pass. `scripts/ci/server.sh build` builds both and runs, with each
  tag, the module and kernel tests and a `-run` subset of `internal/assistant` (Brief, Module, Setting, Routine, Facts,
  ScheduledConfiguration, NorthStar, Accept, Later, Golden): the assistant's tests of the briefing itself skip
  (`needsBriefingModule`), and `TestBuildWithoutBriefingLoadsNone` checks the core without it. Every server package
  passes with `persona_core_only` too: the `core-node`, `core-managers` and `core-other` lanes run the whole suite
  with it ("How to build and run the core alone" below).
- **At run time**: the settings row `modules` (the `settings` table, as every other setting; no state file) holds
  `{"disabled": ["<id>", …]}`. `persona_settings` lists and reads this row as the `modules` key. A set rejects unknown
  or duplicate ids (compiled-in modules and the client-only ones of `modapi.ClientModules`) before showing the existing owner approval card; `null` or an empty disabled list
  restores the default. The manager reads the row once where it starts (`server/internal/assistant/main.go`,
  `ReadModuleSwitch`) and hands it to `assistant.New` as `Options.ModulesOff`, so the saved change takes effect at the
  next manager start, never as a hot switch in the running process. A built-in module switched off is absent exactly as one left
  out of the build; a client-only one loses its hub flag, so the clients hide it. There is no settings screen yet; the settings registry of #13395 remains its intended UI home.

### How to build and run the core alone

Persona Core is a product on its own: a hub built without any module starts, serves and passes the core's tests.

- **Build**: `cd server && go build -tags persona_core_only -o ../bin/ ./cmd/personad ./cmd/persona-hub ./cmd/persona-manager`.
  One module left out: `-tags persona_without_<id>`.
- **Run**: the core-only `persona-hub run` (or `personad hub run`) with a `hub.json` as for any hub (docs/operations/config.md);
  keep `persona-manager` beside `persona-hub` and the hub starts it. The manager's log says `modules: loaded none`
  (a default build: `modules: loaded briefing, voice, volc`, …).
- **Smoke test**: `python3 scripts/ci/core-only-smoke.py` builds the three programs with `persona_core_only`, starts a
  disposable `postgres:18-alpine` container on a private port, writes a `hub.json` in a temporary home and runs
  `persona-hub run` with a clean environment (no `PERSONA_HOME` of the machine's own hub). It checks health, an
  authenticated request, the manager's `modules: loaded none`, that every module's program (`builtinext.AddProgram` in
  `internal/modules/all`) is an unknown subcommand of `personad` and `persona-manager`, that its extension is not in
  `/api/v1/extensions` and `/api/v1/extensions/<id>/-/assistant-data` answers 404, and a clean SIGTERM shutdown, then
  prints one PASS / FAIL / TIMEOUT line. `--full` runs the same on a default build and expects the opposite. `DOCKER`
  names another Docker client.
- **End-to-end gate**: `python3 scripts/ci/module-e2e.py --modules <ids>` switches the modules off and on at run time
  (the `modules` row) on a test hub started the same way, with a default build and the real web app in headless
  Chrome: flags, the manager's list, the module's routes, extensions and connectors, its web entry and chunks, and the
  core's pages in every phase, plus the mock hub as an older hub. A module move merges only after it passes on its
  branch (docs/operations/testing.md "Module e2e gate", #14417).
- **Tests**: `GOFLAGS="-trimpath -tags=persona_core_only" python3 scripts/ci/server-shards.py <node|managers|other>
  --database-file <file with database_url>` runs a shard's every package without modules; a test of a module's own
  behaviour skips without it (`needsBriefingModule`, `needsVoiceModule`, `needsVolcModule`). Locally the `other` shard
  needs Bun 1.4.2 on `PATH`, as in CI.
- **CI** (`scripts/ci/server.sh`): the `build` lane runs 'Core-only hub smoke' and 'Builds without each module' (each
  `server/internal/modules/<id>/module.json`: `go build -tags persona_without_<id> ./...` and the module list and guard
  tests, so a module that moves is covered with no edit); the `core-node`, `core-managers` and `core-other` lanes (and
  `all`) run 'Server <shard> tests with Postgres, core only'. Measured on the hub machine (64 cores, shared): node
  about 5.5 minutes, managers about 6, other about 3.5.
  The per-module steps ('Builds without the … module(s)', 'Core-only and module-less builds') run in the `node`,
  `managers`, `core-node` and `core-other` lanes, one or two each, not in `build`: together they outgrow a hosted job's
  20 minutes. Each runs only its own `persona_without_<id>` tests (#14412): the module's `go build ./...` and the module
  list and guard tests are 'Builds without each module', and every package's tests with `persona_core_only` are the
  core lanes' (the node step keeps the one `persona_core_only` build, Windows' node included). Where a module section
  below names `persona_core_only` among a step's tags, the core lanes run those tests.
- **Size**: `python3 scripts/diag/core_size.py [--no-build] [--json] <ref> …` prints the core's non-test Go lines (every
  `*.go` under `server/` that is not `_test.go`, `server/internal/modules/` left out), each module's lines, and the
  stripped sizes of `personad`, `persona-hub` and `persona-manager` built by default and with `persona_core_only`
  (`go build -trimpath -ldflags '-s -w'`, the same for both). Read from `git archive`, so no checkout changes.

| Ref | Core non-test lines | Module lines | personad default / core-only | persona-hub | persona-manager default / core-only |
| --- | --- | --- | --- | --- | --- |
| `9fa299334` (before modules moved) | 842,284 | briefing 173 | 118,911,239 / 118,898,951 B | 40,755,463 B | 100,917,511 / 100,909,319 B |
| `2da914f99` (voice and volc moved) | 825,432 | briefing 173, voice 15,132, volc 3,259 | 119,030,023 / 117,809,415 B | 40,792,327 B | 101,007,623 / 99,791,111 B |

`persona-hub` has no module in it, so both of its builds are the same size.

### Provider seams

Some modules are not code the manager runs but a built-in extension's program: `volc` is `personad volc` and `voice`
is `personad voice`, which the extension host (`internal/extmgr`) starts and which reach the hub only through its API. The core needs two things of
such a module, and it declares them in `server/internal/builtinext` in its own terms, never as the module's types:

- the program a subcommand runs (`builtinext.AddProgram`, `LookupProgram`; `personad`'s usage lists the programs the
  build has);
- what the extension host needs of the extension: for `volc` its ids and its form's choices (`builtinext.Volc`); for
  `voice` its transcription contribution's id and how its settings merge, which the host reads to say what the
  extension lacks (`builtinext.Voice`).

The module's file in `server/internal/modules/all` provides them in its `init`, under the module's build constraint.
With the module left out nothing is provided: the subcommand is unknown and the extension host registers no such
extension, exactly as before the extension existed (`TestNoVolcWithoutItsModule`, `TestNoVoiceWithoutItsModule`). Such a module registers nothing
with the kernel yet; its `module.json` declares no slot. `scripts/ci/server.sh build` builds and tests the binaries,
`builtinext`, the modules, the guards, the extension host's tests of them and the manager's end-to-end meeting
transcription tests with `persona_core_only`, `persona_without_volc` and `persona_without_voice`.

Mail (`server/internal/modules/mail`, #13936) is the other kind: a service the extension manager runs in process
(the `/api/v1/mail/` routes, the `mail_*` tools and the mail connector, the Inbox's mail, the `mail.accounts` setting).
The manager reaches it through the contract `server/internal/mailwire` (the values both sides pass and the `Service`
interface; the module aliases them) and the port `server/internal/extmgr/mailport`: the module's file in
`server/internal/modules/all` provides `mail.Open` there in its `init`, and the manager opens mail with it when it runs.
The port is a package of its own so the composition root need not import `extmgr`. With the module left out
(`persona_without_mail`, `persona_core_only`) nothing is provided and the manager runs as with a mail service that
failed to open: the mail routes answer 404, no `mail_*` tool or mail connector is offered, `persona_settings` has no
`mail.accounts`, and everything else runs (`TestManagerWithoutMail`). The module registers nothing with the kernel and
its `module.json` declares no slot; the run-time switch (the `modules` row) does not cover it until the kernel serves
`hub_routes` and `mcp_tools` (#13844). Its prompts (`server/internal/prompts/mcp/mail`) and locales (`mail-*`) stay in
the core's trees until `prompt_sections` and `i18n_namespaces` are served. `scripts/ci/server.sh build` builds and
tests the core, the modules, the guards and the manager's mail, inbox, connector and settings tests with
`persona_core_only` and with `persona_without_mail`.

The GitHub connector (`server/internal/modules/github`, #13936) is a connector the extension manager registers. It is
written against the connector SPI, the contract `server/internal/connectorwire` (the `Connector` interface, its
`Manifest` and capability interfaces; `internal/connector`, the framework that runs connectors, aliases them), and the
manager reaches it through the contract `server/internal/githubwire` (manager.json's `github` section, the token
source and its errors, the CI-check snapshot, and the `Connector` interface of the methods the manager calls) and the
port `server/internal/extmgr/githubport`, which the module's file in `server/internal/modules/all` fills with
`github.Provider`. The assistant's CI checks read `githubwire.PRCheckSnapshot` from the manager, so they need no
module. With the module left out (`persona_without_github`, `persona_core_only`) the manager registers no built-in
GitHub connector: it is not listed, `/api/v1/connectors/github/auth` answers 404, no `github_*` tool is offered, and
an extension's GitHub connector can still take the id (`TestManagerWithoutGitHub`). Its prompts
(`server/internal/prompts/mcp/github`, `prompts/inbox/kinds/github.md`) stay in the core's trees as mail's do.
`scripts/ci/server.sh build` runs "Builds without the connector modules" with both tags, and
`scripts/ci/core-only-smoke.py` checks that a core-only hub lists no GitHub connector.
Its `module.json` declares `feature` = `module.github`, so a hub whose manager loaded the module announces it. The
run-time `modules` switch drops the flag, but the extension manager still registers the connector until it reads the
switch too (only a build without the module leaves the connector out today).

The iMessage connector (`server/internal/modules/imessage`, #13936) is the same kind of connector, with a second side:
the reader of a Mac's Messages runs in that Mac's node. The manager reaches the connector through the contract
`server/internal/imessagewire` (the id, manager.json's `imessage` section, the `Caller` and its errors, the `Remote`
interface it drives) and the port `server/internal/extmgr/imessageport`, which `server/internal/modules/all` fills.
The node reaches the module's handler of the hub's `connector.*` RPCs (`imessage.ServeLocal`) through the port
`server/internal/node/imessageport`, which the node's composition root `server/internal/modules/all/node` fills;
`server/internal/nodecmd` links that root (it is listed in `testdata/module_roots.txt`, and a root may import either
list), so `personad node`, persona-node and the manager's local node all have it, and persona-node still links no part
of the manager. With the module left out (`persona_without_imessage`, `persona_core_only`) the manager has no Messages
connector or built-in Messages extension, `/api/v1/imessage` answers that it is not configured
(`TestManagerWithoutIMessage`), and the node answers as a computer without Messages does. Its `module.json` declares
`feature` = `module.imessage`, with the same caveat on the run-time switch as GitHub's. "Builds without the connector
modules" in `scripts/ci/server.sh` runs with `persona_without_imessage` too, and `scripts/ci/core-only-smoke.py`
checks that a core-only hub has no `imessage` extension.

The `lark` module (#13404) is of the same kind as mail: code the manager runs in process, which the extension host
drives. The core declares what it needs of the Feishu / Lark bot's client in `server/internal/botport` (the client's REST calls,
the long connection, the device-code registration, as interfaces over the pure types of `server/internal/botcard`, a
leaf library the core's cards are built with), and `server/internal/modules/all/lark.go` provides it from
`server/internal/modules/lark/feishubot`. The lark-cli adapter and its connector (`server/internal/modules/lark/larkcli`,
`server/internal/modules/lark/connector/larkcli`) are reached the same way through `server/internal/cliport`, with their
values in the contract `server/internal/cliwire`; the connector builds on the connector SPI (`connectorwire`). Without the
module `botport.Available()` and `cliport.Available()` are false: the bot cannot be bound, registered or started (the
setting answers that it is not part of this build), `lark.cli` is not read (a warning at start), and the extension
host's tests of them skip (`needsLarkModule`, `needsLarkCLI`). `scripts/ci/server.sh build` builds and tests with `persona_core_only` and with
`persona_without_lark`. The assistant's own Feishu layer reaches the module through `server/internal/larkhost` ("Lark" below).

### The node's modules

Some modules run only on the computers: their code is part of the node (`persona-node`, and `personad` or
`persona-manager` running a node or its device commands). The manager's list (`internal/modules/all`) cannot hold
that code, since `persona-node` must not link the manager's modules (its `TestPersonaNodeDoesNotLinkTheHubOrTheManager`
refuses `internal/modules/all`), so it goes into the node's composition root, `server/internal/modules/all/node`, which
`server/internal/nodecmd` links (see the iMessage connector above). Each module adds a file there under its build
constraint (`!persona_without_<id> && !persona_core_only`) that fills the node's ports, each declaring what the node
needs in its own terms:

- a device command: `server/internal/nodecmd/commandport` (`AddCommand`, `LookupCommand`). `nodecmd` runs a command
  it does not know itself if a module provides it, and its usage lists the provided commands (`CommandUsage`).
- a live screen's sessions (#6398): `server/internal/node/screenport` (`Provide`, `New`, the `Links` and `Session`
  interfaces). Without them the node offers no session, and a live screen is the plain relayed stream the phone gets
  when it asks for none.
- the built-in program `iphone` (phone control): `server/internal/node/iphoneport` (`Provide`, `Get`, the `Bridge`
  interface of its methods over `proto` types). Without it `node.info` lists no `iphone` program, so the hub offers no
  phone control on that computer, and the program's methods are unknown.

Such a module keeps its manager side small: the module's root package (`internal/modules/<id>`) holds only its
manifest and a `modapi.Module` that registers nothing, and `internal/modules/all` lists it, so a hub announces its flag
(`module.<id>`) when the manager has it. The node half lives in a sub-package (`internal/modules/<id>/<pkg>`) that only
the node's root imports, so the manager does not link it. With the module left out nothing is provided: the command is
unknown and missing from the usage (`TestOfficeFollowsTheBuildTags` in the node's root), and
`scripts/ci/core-only-smoke.py` builds `persona-node` as well and checks that a core-only `personad` and `persona-node`
lack every command the node's root provides. `scripts/ci/server.sh build` runs "Builds without the node modules" with
each such module's tag and `persona_core_only`, Windows' node included.

The modules of this kind:

- `office` (`server/internal/modules/office`, #14258): `personad office …`, the Office files generator
  (`docs/assistant/office.md`), in `internal/modules/office/office`.
- `screen-share` (`server/internal/modules/screen-share`, #14258): the node's screen sessions
  (`internal/modules/screen-share/screenlink`, the relays and direct routes of `docs/extensions/computer-use.md`) and
  the measuring tool `vncbench`. Its manifest's flag `module.screen-share` replaces the id's place in
  `modapi.ClientModules`, so a hub whose manager lacks the module turns the clients' screen sharing off. The node's
  `internal/screen` is not part of it: it reads Agents' terminal panes (dialogs, the input box, rewind) and is core.
- `phone-extra` (`server/internal/modules/phone-extra`, #14258): phone control of this computer's paired iPhones
  (`docs/extensions/iphone.md`): the built-in program `iphone` (`phone-extra/nodeiphone`), the device bridge's client
  and pairing store (`phone-extra/iphone`), the look-and-do core (`phone-extra/iphonectl`), and `personad iphone …`.
  It uses two small helpers of the node that are leaf libraries (`internal/clitext`, `internal/node/nodekit`).

### Guards

`server/internal/archguard` keeps the boundary. Both module guards read the import lines of every `.go` file under
`server/` (testdata left out), whatever its build constraint or GOOS, so a file only a `persona_core_only` build or one
platform compiles cannot hide a violation (`TestModuleGuardsSeeEveryBuild` proves it on a fixture).

- **`TestCoreImportsNoModule`**: no package outside `internal/modules` imports `internal/modules/…`, except the composition
  roots listed in `testdata/module_roots.txt`, which import only a list: `internal/modules/all` (the manager's) or
  `internal/modules/all/node` (the node's, imported by `internal/nodecmd`). A core package's tests may
  import `internal/modules/all` to run as the manager does (`internal/assistant/modules_link_test.go`), and a module's
  stand-in, `internal/modules/<id>/<id>test`, a fake of the outside service the module's program talks to (the
  extension host's tests run `personad volc` against `volctest`); nothing else of `internal/modules`. Known violations would go into `testdata/module_imports_baseline.txt`, which only shrinks
  (a stale line fails); it is empty.
- **`TestModulesUseOnlyTheSPI`**: every file of a module, tests included, imports only the standard library, third-party
  packages, `modapi` and the other contract packages (pure, so nothing more comes with them), the leaf libraries, its
  own packages, and the modules its `module.json` needs. A module it only wants may be left out of the build (`persona_without_<id>`), so it is reached
  through the kernel, never imported. Each module directory has a `module.json` whose id is the directory's name.
  Known exceptions are debt in `testdata/module_services.txt`, which only shrinks (a stale line fails): `<module id>
  <core package>` for any of the module's files, `<module id> (test) <core package>` for its tests only, no wildcards.
  The lines are the GitHub and iMessage connectors' tests, which run them through the connector registry and gate, the
  conformance suite and the subscription service with the modules' own fixtures, and `recording internal/kb`: the recordings'
  storage (the `kb.Recording` model with its `Engine` and `Store`) stays in the knowledge base until it offers a
  recording port. A declared core-service dependency is a known debt, not a precedent: the assistant, the hub and the
  extension host are never listed.
- **`TestLibrariesAreLeaves`**: the leaf libraries (`testdata/libraries.txt`) are the core's packages a module may use
  that hold no state and own no feature (localization, the extension SDK, small helpers). Each depends only on the
  standard library, third-party packages, the contracts and the other libraries, so a module that uses one reaches no
  manager, hub, node, store or module. A library is added only when a module needs it and it passes this test.
- **`TestStandInsHoldNoModuleCode`**: a stand-in imports only what a library may, never its module, so the core tests
  that use it reach none of the module's code.
- **`TestContractsArePure`** covers `modapi` like every contract package, and the hub's
  `TestHubDoesNotImportTheManager` (`server/internal/hub/architecture_guard_test.go`) forbids `internal/modules`, and
  `persona-node`'s `TestPersonaNodeDoesNotLinkTheHubOrTheManager` forbids the manager's list, `internal/modules/all`.
- `scripts/test/test_module_schema.py` checks every `module.json` against `schema/module.schema.json`, refuses invalid
  manifests, and keeps `modapi.Info`'s fields and the schema's in step.

### The briefing pilot

The daily briefing (`internal/assistant/briefing*.go`) is a module (#13430, decision 3). This first step moves the
part that is self-contained behind the contract; the facts' gathering, the Lark card and delivery stay in package
assistant and move as the host learns to serve them.

- **The module owns** (`server/internal/modules/briefing`): the `assistant.briefing_waits` setting (its type, defaults,
  validation and card text) and the wait rules of the briefing's per-direction rows (when stopped work counts as waiting,
  how rows are ordered, how many are shown).
- **The core keeps** the stored value (the `briefing_waits` field of the assistant's settings, byte for byte as before),
  the setting's description in `persona_settings`, and the gathering of the rows from the ledgers it owns.
- **With the module absent** a routine with facts on opens with no facts and keeps no briefing run, so its answer goes
  out as a routine with facts off does (no brief item, no briefing rows on the Lark card); a daily report gets the north
  star's lines alone; the acceptance reminders wait for no briefing; `assistant.briefing_waits` is hidden from
  `persona_settings` but stays stored. `server/internal/assistant/modules_test.go` covers each of these.

### Volcengine speech

`volc` (`server/internal/modules/volc`, #13431) is the Volcengine speech provider module, apart from voice (#13430
section 2): the program `personad volc` and its stand-in service `volctest` (docs/extensions/volcengine-speech.md). The
extension host keeps the extension's manifest, its meetings switch and its settings text (`internal/extmgr/volc_extension.go`),
from what the module provides through `builtinext`; its catalog strings (`hub-volc_extension.*`, `volc-*`) stay in the
hub's locales.

### Voice

`voice` (`server/internal/modules/voice`, #13431) is full-duplex voice: the program `personad voice`, which also serves
the built-in meeting transcription contribution (`gpt-live`), and its stand-in upstream `voicetest`
(docs/assistant/voice.md). The core keeps the generic seam: the extension host's `transcription` point, which any
extension may contribute to, and the meeting transcription the assistant chooses; the built-in default is whatever the
voice module provides (`builtinext.Voice`), and without the module there is none, so a meeting transcribes with a
chosen extension or on the phone. The apps' composer dictation is core; the extension's GPT-Live dictation mode
(docs/assistant/voice.md 4.18) goes with the module, and apps use it only while the extension advertises its
`dictation` feature. Still in the core and named by string, to move as the host serves their slots: the channel kind
(`channel.KindVoice`), `assistantwire.PluginsVoice`, the `voice` and `speech` extension points, the extension host's
glue (`internal/extmgr/voice_extension.go`) and the `hub-voice_extension.*` strings; `internal/transcribelive`, a
measuring tool that also reads the knowledge base, stays where it is.

### Recording

`recording` (`server/internal/modules/recording`, #13407) is meeting recording on the manager: the `/api/v1/recordings`
routes and the phone's audio socket, meetings attended and transcribed on the hub (live jobs, final passes, the
fallback), the meeting asks, photos, live view, rooms, Just me and follow-ups, and the push of minutes and day recaps
with the to-dos made of their action items (docs/assistant/meeting-minutes.md, docs/assistant/day-recap.md). Its
manifest declares `feature: module.recording`, which the kernel announces while it is loaded.

- **The seam**: the core reaches it through `server/internal/recordinghost`, in the core's own terms (`Layer`: what
  the core asks of it; `Host`: what it asks of the core). `server/internal/modules/all/recording.go` provides it under
  `!persona_without_recording && !persona_core_only`; the assistant makes one layer per manager before it registers
  the knowledge base's routes (`internal/assistant/recording.go`, `loadRecording`), unless the `modules` row switches
  it off. The routes, room tools and loops stay on this seam rather than the kernel's `hub_routes`, `mcp_tools` and
  `background_jobs` slots: the routes are registered with the knowledge base's route guard, the room tools are guest
  tools (not assistant tools) and the loops run on the knowledge base's own schedule, so each moves when the kernel
  can carry those.
- **The host is the surface to shrink**: about 75 methods (`recordinghost.Host`: settings rows, conversations and
  their origins, guests, the computers, pushes and channels, to-dos, models, the transcription choice, tracks). Each
  group becomes a narrower port as the core serves one.
- **Shared with the core**: the knowledge base (`recording internal/kb`, above), the transcription job's socket
  (`server/internal/transcribejob`, a leaf library the core's transcription for extensions uses too) and the meeting
  transcription choice, which stays in the core (`meeting_transcription_settings.go`, the extension host's
  `transcription` point).
- **Times as configuration**: the module's times (its `timings.go`, pinned by `TestDefaultTimings`) and the
  attending feeder's clock and timer come through `Host.Timings` and `Host.FeedClock` from
  `assistant.Options.RecordingTimings`, `RecordingTimingLists`, `RecordingFeedNow` and `RecordingFeedAfter`: a test and
  tuning knob of the code that builds the manager, never a user setting, read from no settings, `hub.json` or route.
  `Layer.Wait` ends the live hub runs and waits for the module's background work; the manager calls it when it stops
  (`kbEnd`).
- **Tests**: the module's own tests run on a fake core (`fakecore_test.go`, with the assistant's fake hub ported as
  `fakehub_test.go` and a real knowledge base on the test's directory); the core's tests of how the module meets the
  rest of the manager (day-recap pushes over the channels, the evening facts, `kb_get`, the fleet's attendee facts,
  meeting turns against tool calls, hub transcription end to end) stay in `internal/assistant` and skip without the
  module (`needsRecordingModule`).
- **With the module absent** (`persona_without_recording`, `persona_core_only`, or switched off) the recording routes
  answer 404, the audio socket is unknown, the hello names none of the nine meeting and recording capabilities (so
  the hub announces none of their feature flags), no room tool is offered and no final pass or attending feeder runs
  (`TestRecordingFollowsItsModule`). The recordings already stored stay in the knowledge base.

### Lark

The assistant's Feishu / Lark layer (`server/internal/assistant/feishu*.go`: the chat's turns, cards, commands,
workbench, topics and pushes, about 10k lines) is moving into the `lark` module (#13404), in the shape of recording's.

- **The seam**: `server/internal/larkhost`. Its `Outbound` is the **outbound channel port**, the one way core code
  (acceptance, briefing, reports, conclusions, decisions, owner actions, to-dos and their sources, concerns) reaches
  the owner's chat: `Status` and `Features` (what the channel supports now), `Post` (a card, with a text fallback and
  an idempotency key), `Note` and `Reply` (a line under a message), `UpdateCard` and `ThreadPost` (a line for a to-do's
  chat thread; the core writes the text). Its values are the contract's (`assistantwire.ChannelTarget`,
  `assistantwire.ThreadEvent`) and `internal/botcard`'s. `Layer` (core -> module) embeds the port and grows with each
  move; `Host` (module -> core) is what the moved code reads of the assistant. The module declares its own `Host`
  with the same methods (`server/internal/modules/lark/layer.go`), so it imports no core service, and
  `server/internal/modules/all/lark.go` provides its layer under `!persona_without_lark && !persona_core_only`.
- **The core asks the port**, `s.chat()` (`server/internal/assistant/lark.go`), never a `feishu*.go` function: a
  core file that posts to the chat calls the port. The implementation still lives in `feishu*.go` and is served to the
  module through the host under the port's names; each move takes code into the module and drops the host methods
  it no longer needs, so the host only shrinks.
- **The Feishu turns are the module's** (`server/internal/modules/lark/turn.go`, `cot.go`, `cot_worker.go`,
  `stream.go`): a Feishu run's progress card and CardKit entity, its agent message and the worker that writes it, and
  its answer. The channels' pipeline (the runs, their lock, finishing, folding and dropping them) stays in the core,
  which reaches the turns through `larkhost.Turns` (the core's `feishuSink` forwards to it). The module keeps its own
  state of each live run in the core's `chanLive.sink` (`Host.RunState`), which the core asks to `Abort` when it drops
  the run and to forget a held run's turn (`NextTurn`). What the turns read of the core is the host's second group of
  methods: the channels' runs and their values under their lock, the hub's `feishu.*` transport, and what the core
  still draws (the progress card's content, the answer's cards and the process panel, `feishu_progress.go` and
  `feishu_answer.go`, which the core's own cards use too). The module's times (`timings.go`) come through
  `Host.Timings` from `assistant.Options.LarkTimings` and `LarkTimingLists`, a test and tuning knob as recording's;
  `Layer.Wait` waits for the agent messages' workers (the service waits for them when its connection ends).
- **Tests**: the turns' own tests run in the module on a fake core (`server/internal/modules/lark/service_test.go`:
  the channels' runs, their finishing, folding and dropping, and the hub's transport); the assistant's tests of the
  turns through the channels' pipeline stay in `internal/assistant` and skip without the module (`needsLarkLayer`).
- **With the module absent** (`persona_without_lark`, `persona_core_only`) the port is `larkhost.None`, a bot switched
  off: its status reads without an error as neither on nor ready (so an acceptance ask is card-less and a briefing
  goes out by push, as with the bot off), it supports nothing, a post or a reply answers `larkhost.ErrNoChannel`, and
  notes and thread lines are dropped.
  The run-time `modules` switch does not cover it yet, as for mail.

### How to add a module

1. Pick the id from #13430 section 2 and check its manifest needs only slots the kernel serves; if it needs another,
   add the `Host` method and the kernel's support first, in their own PR, and update "Slots" above.
2. Create `server/internal/modules/<id>/` with `module.json` (validated by `schema/module.schema.json`), a `Module` whose
   `Info()` reads it and whose `Register` adds what it declares. Import only `modapi`, contract packages and the modules
   it needs (never one it only wants).
3. Add `server/internal/modules/all/<id>.go` with `//go:build !persona_without_<id> && !persona_core_only`, and
   `want_<id>_test.go` there under the same constraint, adding the id to `wantModules`. A module that runs as a
   built-in extension program provides what the core needs of it there too ("Provider seams").
4. In the core, replace each direct call with a question to the kernel (`s.modules`), and define what happens when the
   module is absent; test both.
5. Run the archguard tests, the tagged builds and `scripts/test/test_module_schema.py`.

## Clients

Each client documents its side in its own platform doc; these are pointers only.

- **Apple** (#13432): `PersonaHostApp` with `PersonaModuleAPI` and `PersonaModule<Name>` packages. See `docs/apple/` (written with #13432).
  A module the hub may turn off declares `feature` = `module.<id>`; the app follows the rule agreed on #13844 (off only
  when the hello has the marker `modules` and lacks the flag; remembered per hub before the hello). See
  `docs/apple/ios.md`, "Modules" (#13849).
- **Android** (#13433): `:core:moduleapi` and `:module:<id>`. See `docs/android/` (written with #13433).
- **Web** (#13434, #13846): the module host `web/src/core/module-host.ts` and `web/src/modules/<id>/`, serving routes,
  sidebar entries, chat slots, Settings categories and card renderers. Shipped module switches are lasting browser
  preferences in Settings › General › Optional features. The form reads the registry and the browser stores disabled
  module ids in `persona.modules.off`. A core-only build has no module switches. See `docs/web/web.md` ("Modules").

Web Labs admits temporary trials or a choice between stable and new implementations of one capability. A trial keeps
the stable default and a lossless rollback, and links a tracking issue with a plan to graduate or remove it. Put
permanent browser and advanced preferences in their owning Settings category. Keep irreversible changes, internal
debug controls and placeholder demos out of Labs. See `docs/web/web.md` ("Labs").

**Hub flags.** Every client switches modules on the hub's hello the same way (agreed on #13844): a module's `feature`
is `module.<id>`; a hub that publishes module flags also announces the marker `modules`; a module with a `feature` is
off only when the hello has `modules` and lacks the module's flag. No hello yet, or an older hub without `modules`,
leaves it on.

Every client reads the same manifest fields from `schema/module.schema.json`.
