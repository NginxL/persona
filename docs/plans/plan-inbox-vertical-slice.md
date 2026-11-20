# Plan: the Inbox as the first vertical domain slice (#11309)

**Status: a plan for maintainers to decide on.** It is the Stage B item "Inbox as a vertical pilot" of #10613 and
follows #7891's package-split rules. Nothing here is built; each step becomes its own issue and PR. No step changes what
a user sees (no UI, setting, default or wire change), so the design-principles self-check does not apply.

**How the numbers were taken**, as of `origin/main` at 287db61638 (2026-11-20):
- "14 days" is `git log --since=2026-11-06T00:00:00Z` on that commit, counted by exact path. History starts on
  2026-10-24, so the window covers most of it, repository-wide sweeps included.
- Open-PR facts come from the 155 PRs open on 2026-11-20. GitHub lists at most 100 files per PR and 23 of them reach
  that cap, so overlaps may be undercounted. Hunk positions are on each PR's own base. Line numbers drift, so each
  step finds its lines again by name and re-checks open PRs before it is pushed.
- Nothing was built or run for this plan; every build or test speedup below is expected, not measured. Paths that do
  not exist yet are plain text, not code, so the docs path checks stay meaningful.

## 1. Why

The Inbox is the busiest single domain on every side, and most of its changes also edit lists every other feature edits.

- **Server.** The Inbox code in `server/internal/assistant` is 59 non-test files (`inbox*.go`, `supervise_inbox.go`,
  `tasks_inbox.go`) with 17,098 lines, plus 143 test files with 36,624 lines. 149 commits touched the non-test files in
  14 days; of those, 26 also touched `assistantwire/inbox.go`, 16 `assistant/service.go`, 13
  `server/internal/hub/testdata/route_scopes.txt`, 12 the `mcp_tools.assistant` golden, 11 `extwire/scopes.go`, and
  8 each `assistant/tools.go` and `assistant/bus.go`.
- **Registration is spread over central files.** `assistant/service.go` has the fields `inboxExt` (70) and `inbox`
  (264), `s.inbox.lang` (326), `s.inboxRoutes()` (386), `inboxReport` in the snapshot (874-875) and the turn-settled
  hook (966); `assistant/bus.go` the needs-you tell after each handshake (272-278) and the dispatch to `inboxRequest`
  (660); `assistant/tools.go` five names in `toolNames` (34-35) and five `runTool` cases (205-214); `extwire/scopes.go`
  the 31 Inbox scope entries (22-52); `assistant/handler.go` four Inbox methods on `Handler` (99-105), called only from
  `assistant/inbox_bus.go`.
- **Ten files outside the Inbox core** (`inbox*.go` less the task bridge's files) **reach its in-memory state**
  (`inboxBook`, `s.inbox`). Five write no item:
  `assistant/context.go` (`followingInboxItems`, 525-528), `assistant/tasks_draft_decisions.go` (88-89, 111-118),
  `assistant/service.go` (326), and through `b := &s.inbox` `assistant/origins.go` (`gatherOriginEvidence`, 219) and
  `assistant/feishu_workbench.go` (`workbenchItem`, 530). Five are the task bridge's (section 2), and they also write
  items under the book's lock: `assistant/supervise_inbox.go` (51, 154; writes at 179-186),
  `assistant/tasks_inbox.go` (22, 60, 80; writes at 81-91; `checkTaskInbox`, a method on `*inboxBook`, at 38),
  `assistant/inbox_task_links.go` (211, 281; writes at 232-235, 247-252 and 305-313), `assistant/inbox_task_recheck.go`
  (37; writes at 56-58 and 110-115) and `assistant/inbox_todo.go` (91, 281; `fileTodo`, a writing method on
  `*inboxBook`, at 29, which the core's `assistant/inbox_bulk.go:212` calls). Only the first two of those are outside
  `inbox*.go`. `assistant/briefing.go` only calls `s.followingInboxItems`, and the lock in `assistant/inbox_no_output.go`
  is the tasks' event ledger (`s.tasks.turns`), not the book. 123 test files in the package touch `.inbox.mu`
  (1,241 times) or `.inbox.items` (588 times).
- **Build cost** (#10613): a one-line edit in `internal/assistant` costs 58 s to compile its test binary, and check-fast
  tests the whole `./internal/assistant/...` subtree for any change under it (#10615).
- **Web.** `web/src/lib/inbox.ts` (1,721 lines) is on every first screen (`web/src/lib/inbox-page.boundary.test.ts`).
  Of the 134 commits that touched `web/src/lib/inbox*` or `web/src/views/inbox` in 14 days, 47 edited it and 6 raised
  `FIRST_SCREEN_BUDGET` in `web/src/startup-preload.ts`. 27 source files outside those paths import it, many for
  helpers that are not the Inbox's (`withToken`, `shortTime`, `conversationRoute`, the Lark message types).
- **Android.** `android/core/data/build.gradle.kts` depends on `:feature:inbox` (a core module on a feature module), and
  7 core:data main files use `persona.feature.inbox`; feature:chat and feature:knowledge depend on it too.
  `android/feature/inbox` had 102 commits, `RealInboxRepository.kt` 32 and the feature's `InboxRepository.kt` 27.
- **Apple.** `apple/Persona/Views/Inbox` is 73 Swift files (21,239 lines, 118 commits). No Inbox logic runs under
  `swift test`.
- **Wire.** The item type is written by hand in Go, Swift, TypeScript and Kotlin; HarmonyOS reads keys one by one, and
  `scripts/mock/mock_hub.py` writes the JSON by hand. A maintainer ended generating it in #9117 (section 3.2).

Per-domain registration was proposed as a stage of #7891 (comment of 2026-11-18); one domain, measured, shows whether
it pays off before others repeat it.

## 2. What the Inbox domain owns

Ownership follows `docs/assistant/inbox.md` ("Ownership: the manager owns the Inbox") and the rule that the hub is only
a communication center. "Inbox" is the user-facing Inbox, not #7563's durable inbox for assistant rounds.

**Owned (the manager's assistant process):**
- Items and their state (admission, triage, rules, policies, mutes, folders, grouping, events and the handling chain,
  following up, done and history, merge, parents, bulk actions, handled elsewhere); the AI twin (judging, processing
  conversations, drafts, suggestions, message cards, hand-off with write-back); the assistant's own rows; translation,
  with `/api/v1/translate`, `/translate/message` and `/translate/mail` (D11); the tools inbox_suggest, inbox_draft,
  inbox_folders, inbox_move and inbox_post.
- The 34 routes in `assistantwire.InboxRoutes`, the 8 `inbox.*` bus methods, the events inbox.changed and
  inbox.needs_you, the table `inbox_items`, and the settings keys inbox_mutes, inbox_policies, inbox_folders and
  inbox_mail_baselines.
- The model purposes inbox.triage, inbox.judge, inbox.draft, inbox.agent and translate; the prompts in
  `server/internal/prompts/inbox` and `server/internal/prompts/mcp/inbox`; the locales assistant-inbox* and inbox-errors.
- On clients: the list store with its delta and cursor, the section, filter, bar and row rules pinned by
  `spec/fixtures/inbox`, and the Inbox pages.

**Not owned:**
- **Sources.** Mail, connectors and subscriptions belong to `server/internal/extmgr` and `server/internal/subscriptions`
  (send-once in `sent_messages`, the answer to inbox.config) and reach the Inbox only through inbox.collected and the
  AppInbox calls. No step moves their code, so `docs/design/proposals/inbox-sources-migration.md` ("Order") goes on as
  designed: the issues of P1-P5, M1 and M2 are closed; M3 (Lark) and M4 (mail) remain.
- **The hub**, which no step changes. It relays the routes, merges, pages and localizes lists, merges deltas, caches
  answers, and keeps reply and hand-off receipts and the roster's needs-you count. `hub/bus_forward.go` picks Inbox
  answers by path (`strings.HasPrefix(r.URL.Path, "/api/v1/inbox")`); comments in `hub/inbox_answers.go`,
  `hub/inbox_delta.go` and `hub/inbox_forms.go` name it. The answer-only regexes in `hub/devicetokens.go` are a
  security boundary and stay hand-edited.
- **The task bridge** (`supervise_inbox.go`, `tasks_inbox.go`, `inbox_no_output.go`, `inbox_task_*.go`,
  `inbox_todo.go`): To-dos code that consumes Inbox events and writes task links onto items.
- **In-process readers:** the Lark workbench, the briefing, the main prompt's following section, the knowledge base.

## 3. Target on each side

### 3.1 Server

New packages: server/internal/assistant/kernel (Module and Kernel, the registration seam),
server/internal/assistant/inbox/item (the pure item, its nested types, tiers, status and the listed() projection) and,
only after D3, server/internal/assistant/inbox (the stateful module behind inbox.Host). Package assistant keeps the glue
(the Inbox module, the Host adapter, aliases, the task bridge); extwire gets the Inbox scopes in a file of their own.

The kernel starts as exactly the five members `server/internal/assistant/saved/saved.go` takes today (`saved.Host`), so
saved is its first user and the interface is not Inbox-shaped. It grows only with the Inbox as its second user:

```go
type Module interface {
	Name() string
	Register(k Kernel) // once, from assistant.New, where the module's lines are today
}

type Kernel interface {
	Handle(pattern string, fn http.HandlerFunc) // a relayed route; must be in assistantwire.Routes or InboxRoutes
	Store() any
	Event(ctx context.Context, ev map[string]any)
	Full(r *http.Request) bool
	BodyErr(w http.ResponseWriter, err, bad error)
	// Added in steps S4 and S5:
	Bus(method string, fn BusFunc)                                       // one bus request method; a duplicate panics
	Tool(t Tool)                                                         // Tool{Name, After, Run}
	TurnSettled(fn func(ctx context.Context, machine, session string))   // inside turnEnded's !meeting branch
	Connected(fn func(ctx context.Context))                              // per handshake, own goroutine, after client.Wait
	Snapshot(fn func(ctx context.Context, snap *assistantwire.Snapshot)) // where snap.Inbox is filled today
}
```

- **Hook order is part of the contract.** Each hook runs where its line runs today: the snapshot hook before
  `snap.Work` (`fleetTakeSnapshot` in `assistant/fleet_snapshot.go` reads `snap.Inbox` back), the turn hook inside
  `!meeting`; `Tool.After` keeps the MCP tool list and its goldens byte-identical. The Inbox has no loop in
  `Service.Run`, so the pilot does not test #7891's proposed Loops or Recover hooks.
- **The item slice** follows the To-dos precedent (`server/internal/assistant/todo/projection`). Because
  `TestMovedClustersStayOut` (`server/internal/archguard/split_clusters_test.go`) fails when the old package declares a
  name the subpackage exports or gets back a file with a moved prefix, item's exported names differ from assistant's
  top-level names, and the aliases (`type InboxItem = item.Item`) live in a file not starting with "inbox" (S9's row
  bans that prefix), as in `docs/server/architecture.md` ("Contract packages").
- **The stateful module** sits behind inbox.Host, small role interfaces (store, sources, notify, models, owner,
  conversations, feed, machines, privacy, tasks, knowledge) over the 47 `*Service` methods defined outside `inbox*.go`
  that the core (bridge excluded) calls. Package inbox cannot import assistant, so Host speaks contract types and an
  adapter converts.
- **Other code uses the Inbox through three surfaces:** a Reader (following items, an item copy, the card-send lock,
  origin evidence), Actions for the Lark workbench, and Policy (the model-call gate, the manager's origin decision,
  agent started), which stays explicit calls, not kernel hooks. The Reader is read-only: it serves section 1's
  readers, not the bridge.
- **The task bridge** is renamed `task_inbox_*` / `todo_inbox`, sits behind an inboxTasks interface, and registers as
  the module "inbox-tasks", which owns `/inbox/todo`, `/inbox/todo/undo` and the task-link recheck route. It writes
  items (a task link and its removal, a closed task's continuation, filing for a finished task, the supervisor's
  in-memory signal watermark). While it shares package assistant with the core it keeps writing as it does today:
  under the book's lock, rechecking the item and its link if it let go of the lock since reading them; a saved write
  goes `beginWrite`, change, `settle`, unlock, then `saveWrites`; and it never takes the to-dos' lock while holding the
  book's (`assistant/inbox_task_links.go:301`). S9 takes the lock out of package assistant, so it adds a fourth
  surface, Links: one method for each read, check or write the bridge makes under the lock today, keeping those steps
  inside the module (D5).

### 3.2 Contracts and wire

- `assistantwire.InboxRoutes`, `server/internal/assistantwire/inbox.go` and `server/internal/connectorwire/inbox.go` stay.
- **No wire generation.** #9117 (stage 5 of #7891) ended it for the Inbox by maintainer decision: a generated type
  would not be a pure replacement. In #9117's words, Go's `InboxItem` is the persisted struct (about 60 fields, half
  never listed, with `omitempty`, `*time.Time` and an embedded wire type), and Swift's has a hand-written decoder with
  `Date` fields, computed properties and nested types. No step generates; reopening it is decision D12.
- Two tests come first, so "byte-identical" is checked rather than assumed (step S2): a reflection test that fills
  every exported `InboxItem` field and checks the keys the unexported `InboxItem.listed()` emits, and a Go-produced
  "every block" case in `spec/fixtures/wire/inbox.json`.

### 3.3 Clients

- **Web.** A domain folder, web/src/domains/inbox (name: D8), takes the pure model first (the section bar), then the
  store and API client from `web/src/lib/inbox.ts`, then the page-only modules; `web/src/views/inbox` stays the
  route-level UI, and helpers that are not the Inbox's leave `lib/inbox.ts`. A boundary test generalizes
  `web/src/lib/inbox-page.boundary.test.ts`: the domain imports nothing from views; page-only modules are imported only
  from the Inbox; new domain namespaces stay out of `web/src/i18n/startup-namespaces.json` unless a first-screen
  exception shows its bytes; re-export shims are on a shrink-only list. New Inbox mock endpoints go to an Inbox module
  in `scripts/mock/mock_domains.py` (#11016), not `scripts/mock/mock_hub.py`.
- **Android.** The contracts slice is a package, persona.core.contracts.inbox, in core:contracts: pure Kotlin, no
  core:domain, no Android (`android/core/contracts/build.gradle.kts` has no project dependency, so no cycle). The
  page-facing repository needs core:domain types, so it moves to a JVM module, :core:inbox, on contracts and domain;
  @StringRes members become keys, and Compose-backed members stay in feature:inbox. Then core:data stops depending on
  feature:inbox, and a source scan keeps core modules off feature modules.
- **Apple.** A Swift package, apple/Packages/PersonaInbox, on PersonaWire, holds UI-free Inbox logic under `swift test`.
  The tier enum moves into `apple/Packages/PersonaWire`, which 11 targets in `apple/project.yml` already link, the
  watch included. `apple/Shared/InboxModels.swift`, `InboxEvents.swift` and `InboxNow.swift` stay in Shared: the watch
  compiles them, and the item's Codable form is the StateCache record and the WatchSync payload. The item moves only
  with two-way compatibility tests (an old blob decodes in the new build, and the reverse). Views wait for
  PersonaDesign (#10613).
- **HarmonyOS.** Unchanged: `harmony/entry/src/main/ets/models/InboxWorkspace.ets` reads raw keys and has no typed item
  to replace.

## 4. Steps

**Rules for every step** (#7891):
- **Pure moves, one cluster per PR**, each with its own issue linking #10613 (and #7891 for server moves). Wire JSON,
  prompt and MCP goldens, and `route_scopes.txt` stay byte-identical.
- **Check open PRs before pushing:** list those that touch the files and their hunks; if one was updated in the last
  24 hours, wait or coordinate in that PR.
- **Moved clusters stay out:** a `movedClusters` row for every moved Go cluster. Path-keyed baselines (fonts, tokens,
  isolation lint, doc paths) move with their files at identical counts and are never raised.
- **Evidence:** Go with GOFLAGS=-trimpath and `-p 4`, Gradle with `--max-workers=4`, no booted simulator for package
  tests. The PR says what was run and what was not. Rollback is a revert of the PR unless the table or the step says
  more. The tracks (server, web, Android, Apple) are independent; conflict costs are as of 2026-11-20.
- **A known failure on main:** `TestHubAnswersOnlyItsOwnRoutes` has failed since #9485 merged, because
  `hub/scoped_tokens.go` ranges over `extwire.RouteScopes`, which the guard does not take as a relayed wire table
  (reported on #9485; it still fails at e21095770d, and no issue tracks it yet). Until it is fixed, a step whose
  evidence names that test meets it when the test's only failure is that message, as on the step's base, and the PR
  reports it; the step does not fix it (section 7).

| Step | Change | Conflict cost | Gate |
| --- | --- | --- | --- |
| S1 | Kernel package; saved registers through it | Low: 25 open PRs touch `assistant/service.go`, none the saved or Inbox lines; none touches `assistant/saved` | now |
| S2 | Guards and contract tests | None: new files; `server/internal/hub/wire_samples_test.go` is only in #2071, a draft idle since 2026-11-09 | now |
| S3 | Seams in quiet files | None: the four files had no open PR | now |
| S4 | Routes, bus methods, hooks, snapshot through the kernel | Medium: 25 open PRs on `assistant/service.go` and 6 on `assistant/bus.go`, none on the moved lines; none on `assistant/handler.go` | a window with a rebase note in each open PR on these files (#7891, 2026-11-18) |
| S5 | The five tools through the kernel | Medium: 14 open PRs on `assistant/tools.go`; #6539 and #9782 (updated 2026-11-19) edit its Inbox line | after #6539 and #9782, or a rebase note |
| S6 | Inbox scopes in their own extwire file | Low once #4512 lands (it adds three Inbox scopes, updated 2026-11-20); #7396 edits only the To-dos block | after #4512 |
| S7 | The task bridge renamed; the core calls it through one interface | Medium: #6206, #9744, #10806, #10808 and #10894 (updated 2026-11-19 or 20) edit bridge files | after them, or once none was updated in 24 hours |
| S8 | The item slice | High: `assistant/inbox.go` has #4512 and the #9790 family (#9790, #10580, #10587); 12 open PRs touch `inbox*.go`, the #10079 family (#10079, #10891, #10915) too. Rollback: later steps first | a window after both families; announced in the open PRs and merged the same day |
| S9 | The stateful module | Very high: every open server Inbox PR, and 123 test files rewritten. Rollback: before later PRs add files to the package | D3, a window, a short Inbox PR freeze |
| W1 | Web domain folder, starting with the section bar | None: the moved and edited files had no open PR; `web/src/startup-preload.ts` and the startup namespaces (20 open PRs each) are untouched | now |
| W2 | Non-Inbox helpers leave `lib/inbox.ts` | Low: its two open PRs, #4512 (near 140, 497-515, 1166, 1308, 1329) and #1995 (near 522), miss the moved ranges | now |
| A1 | Android contracts slice and a module-edge ratchet | Low: new files; `docs/android/android.md` has 2 open PRs | now |
| A2 | Pure reply and hand-off types into the slice | Low: no open PR on the files (#10396 touches only `InboxTopBar.kt` and strings in the feature), but they are hot | now, landed fast |
| A3 | `:core:inbox`; core:data off feature:inbox | Medium: `RealInboxRepository.kt`, the feature's `InboxRepository.kt` and the app's `InboxModule.kt` are hot (32, 27, 21 commits) with no open PR; about 68 test files implement the repository, one of them (`InboxShowcaseShotsTest.kt`) in the open #9823 repair PRs | after A2 |
| I1 | Tier into PersonaWire; the PersonaInbox package | Low: #4512's and #7495's hunks in `apple/Shared/InboxModels.swift` miss the enum; `InboxBar.swift` has no open PR; #4512, #7495 and #11282 edit `InboxView.swift` but not its empty-state block, which loses only `EmptyKind` and `emptyKind`; `apple/project.yml` (2 open PRs) only gains entries | now |

### S1. Kernel package, with saved as its first user
- **Change:** server/internal/assistant/kernel with Module and a Kernel of saved.Host's five members.
  `saved.Register(saved.Host{…})` (`assistant/service.go:370`) becomes one register line at the same spot, and
  saved.Host goes. The adapter is a new file in package assistant whose name does not start with `saved_` (the
  movedClusters row for `assistant/saved` bans that prefix). A registry test checks that a duplicate name panics.
- **Done when:** `go test -p 4 ./internal/assistant/kernel/... ./internal/assistant/saved/... ./internal/archguard`, the
  saved tests, `TestRoutesAreTheWireOnes` and `go test ./internal/hub -run 'TestRouteScopes|TestHubAnswersOnlyItsOwnRoutes'`
  pass with `route_scopes.txt` unchanged, `TestHubAnswersOnlyItsOwnRoutes` apart from #9485's known failure (reported,
  not fixed); `go vet ./internal/assistant/...` is clean.

### S2. Guards and contract tests
- **Change:** an archguard parse-only test: the book (`s.inbox.*` selectors, `b := &s.inbox`, methods on `*inboxBook`)
  is reached only from the core (`inbox*.go` less the bridge's files), from the bridge's five files in section 1, named
  in the guard so that S7's renames move their entries and add none, and from a shrink-only allowlist of section 1's
  five readers; and `toolNames` and `extwire/scopes.go` gain no Inbox entry beyond main's and those open PRs add
  (#4512's three scopes, #6539's inbox_replay); the hint names the seam. Also the reflection test of `listed()`
  (section 3.2), the every-block case for `TestWireSamples` (the existing case's bytes unchanged), and an agreement
  test between a per-route answer-only table and `hub/devicetokens.go`.
- **Done when:** the new tests pass; a planted `s.inbox.items` read in a non-Inbox file fails the guard (shown in the
  PR); the guard passes on a local `git merge-tree` of each open Inbox PR's head, commands in the PR body; Android's
  `WireFixturesTest` still decodes `spec/fixtures/wire/inbox.json`.
- **Landed (#11650):** `server/internal/archguard/inbox_slice_test.go`. `TestInboxBookStaysInTheInbox` parses the
  package's production files and counts each place that reaches the book: a selector through the Service's `inbox`
  field, its address, the name `inboxBook`, or a call of a function that returns one. Re-derived on 2cba124967 (and
  unchanged at 44e9dadce0), it is as section 1 says (S3 moved none of it): the 52 core files, the bridge's five files by
  name, and the five readers with pinned counts (`context.go` 3, `feishu_workbench.go` 1, `origins.go` 1, `service.go`
  2, `tasks_draft_decisions.go` 5). The bridge's other three files reach nothing. `TestCentralListsGainNoInboxEntry`
  treats as the Inbox's a tool named `inbox_*` or described in `server/internal/prompts/mcp/inbox`, and a route under
  `/api/v1/inbox` or `/api/v1/translate` or with an Inbox scope. It allows main's five tools and 31 scopes, plus what
  open PRs add: `inbox_replay` (#6539), `message_send` (#9782, described there, staying in `toolNames` at S5) and
  #4512's three scopes. Two open PRs add files that reach the book, allowed apart with their counts until they land or
  close: `outbound_messages.go` (#9782, a reader) and `tasks_inbox_existing.go` (#9744, the bridge's). In
  `server/internal/assistant/inbox_listed_keys_test.go`, `TestListedKeys` fills every exported field by reflection and
  pins the 43 key paths `listed()` leaves out, with `tier_was` (#4512) and `reply_approval` (#9790, #10580, #10587)
  added ahead of their PRs. `inbox_every_block` follows `inbox_list` in `spec/fixtures/wire/inbox.json`, whose bytes
  stay unchanged, and Android's `WireFixturesTest` decodes it. Deferred: the agreement test with `hub/devicetokens.go`,
  because no per-route answer-only table exists and the plan defines one only as D7; a check that `inbox_every_block`
  sets every block, which would make #4512 (a new `progress` block) edit the sample; and the two Reader methods S3
  deferred, which are production code for a step of their own.

### S3. Seams in quiet files
- **Change:** pure moves in package assistant, in files no open PR touches. `clipRunesTo` (`assistant/inbox_merge.go:50`,
  also used by the urgent-call files) and `cardLocks` (`assistant/inbox_bus.go:144`, also used by `assistant/tasks.go`)
  move to neutral files. The Inbox half of `assistant/extension_context.go` (`inboxExtensionCache`,
  `inboxExtensionContext`) moves to an inbox-prefixed file; `projectExtensionContext` stays for
  `assistant/todo_outcome.go:406`. `assistant/tasks_draft_decisions.go` reads the book through the first two Reader
  methods (the card-send lock, an item copy) and leaves the allowlist. Deferred until their PRs land: `replyInDoubt`
  (`assistant/inbox_triage.go:470`, 8 open PRs), `modelPaused` and the purpose constants (`assistant/inbox_hub.go`, 4),
  `assistant/context.go` (8), `assistant/feishu_workbench.go` (4), `assistant/origins.go` (#2049, idle since 2026-11-09).
- **Done when:** `go build ./...` succeeds; the full `./internal/assistant` package passes once, with its duration, and
  `./internal/archguard` passes; the allowlist is shorter; `scripts/test/test_doc_go_paths.py` passes.
- **Landed (#11536):** byte-for-byte moves of `clipRunesTo` to `assistant/text.go`, `cardLocks` to
  `assistant/card_locks.go`, and the Inbox half of `assistant/extension_context.go` (its cache, `inboxExtensionContext`
  and the never-quote check, which only the Inbox's drafts use) to `assistant/inbox_extension_context.go`; its tests stay
  in `assistant/extension_context_test.go`, which #10079, #10891 and #10915 edit. Deferred: the two Reader methods,
  because S2's allowlist is not on main yet and `listed()` blanks the `Last` that `decideDraft` reads, so the item copy's
  shape is chosen with S2; and the comments in `assistant/service.go` (24 open PRs) and `assistant/inbox_triage.go` (8)
  that still name `extension_context.go`, whose header now points to the Inbox file.

### S4. Routes, bus methods, hooks and snapshot through the kernel
- **Change:** the kernel gains Bus, TurnSettled, Connected and Snapshot. An Inbox module in package assistant registers
  the unchanged `inboxRoutes` body, the 8 bus methods (`inboxRequest` split per method), `inboxAgentTurnSettled`, the
  needs-you tell and `inboxReport`, each at its current place (section 3.1). The four Inbox methods leave `Handler`.
  `s.inbox.lang` stays until the #8584 repair PRs (#10564, #10593, #10599, #10920), which edit the line above it, land.
  A guard then allows no Inbox line in `assistant/bus.go` or `assistant/handler.go`, and in `assistant/service.go` only
  the two fields, the register line and `s.inbox.lang`.
- **Done when:** `go test ./internal/assistant -run 'TestRoutesAreTheWireOnes|Kernel|Bus|Inbox|Snapshot'`, then the
  full package, pass; `go test ./internal/hub -run 'TestRouteScopes|TestGoldenMCPTools|TestHubAnswersOnlyItsOwnRoutes'`
  passes with every golden unchanged, `TestHubAnswersOnlyItsOwnRoutes` apart from #9485's known failure (reported, not
  fixed); `./internal/archguard` passes.

### S5. The Inbox's tools through the kernel
- **Change:** the five tools register as one block after ask_escalate and leave `toolNames` (`assistant/tools.go:34-35`)
  and `runTool` (205-214); `toolInboxDraft` moves out of `assistant/tools_apps.go`. The model-call gate
  (`assistant/tools.go:69`), `inboxAgentStarted` after agent_start and the read-only lists in `assistant/inbox_policy.go`
  stay. A rebase note for #6539 and #9782: inbox_replay registers with the Inbox, message_send stays in `toolNames`.
  The order the models see does not change, because the block is inserted right after its anchor.
- **Done when:** `go test ./internal/hub -run TestGoldenMCPTools` passes with every `mcp_tools.*.golden` unchanged, and
  `go test ./internal/assistant -run 'Tool|InboxSuggest|InboxDraft|InboxFolders|InboxPost|InboxPolicy'` and
  `go test ./internal/node -run TestInbox` (the node's Inbox model policy) pass.

### S6. Inbox scopes in their own extwire file
- **Change:** the 31 entries at `extwire/scopes.go:22-52` move to a new extwire file, merged into `RouteScopes` in
  `init()` (the pattern `assistantwire/board.go`, `assistantwire/models.go` and `assistantwire/agents.go` use for
  `Routes`); a duplicate key panics. The translate routes stay absent, so they keep needing full credentials.
- **Done when:** `go test ./internal/extwire/... ./internal/extmgr -run Scope` and `go test ./internal/hub -run TestRouteScopes`
  pass with `route_scopes.txt` byte-identical, and no `/api/v1/inbox` line is left in `extwire/scopes.go`.

### S7. The task bridge, named as such, called through one interface
- **Change:** `git mv` the bridge files (`inbox_no_output.go`, `inbox_task_*.go`, `inbox_todo.go`, their tests) to
  task_inbox_* and todo_inbox names, so the inbox prefix means the core only. The core calls the bridge's entry points
  only through inboxTasks (confirmTodo, linkInboxTaskURLs, scopeInboxTasks, superviseInbox), and the bridge registers
  as "inbox-tasks", so `assistant/inbox_api.go` stops calling inboxTodoRoutes and inboxTaskLinkRoutes. The other
  bridge code the core calls today moves to core files by a pure move: the item helpers `inboxTaskLinkEligible`,
  `inboxScopeOf` (with its type), `judgeEvidence`, `receivedSince` and `checkSuggestedDue`, and `fileTodo`, the book
  method `assistant/inbox_bulk.go:212` calls; `taskOpen` moves to a tasks file. The bridge's own reads and writes of
  the book stay as they are (section 3.1): the Reader is read-only, so no surface is added here, and Links waits for
  S9 (D5).
- **Done when:** `ls server/internal/assistant/inbox*.go` lists only core files; an AST guard finds no call from the
  core to a bridge function outside inboxTasks; S2's guard passes with the bridge's entries renamed, none added, and the
  reader allowlist unchanged; the Inbox and task tests, then the full package, pass; the docs naming the files are
  updated and `scripts/test/test_doc_go_paths.py` passes.

### S8. The item slice
- **Change:** `InboxItem`, its nested types and pure methods (tiers, `status()`, `listed()` and its helpers) move to
  server/internal/assistant/inbox/item, with section 3.1's aliases and naming rule and a names-only movedClusters row,
  plus Go replays of the server-side `spec/fixtures/inbox` operations (hub order, item state, follow time). The PR body
  lists gopls rename commands for open PRs to replay.
- **Done when:** the item tests pass on their own; `TestWireSamples` and S2's reflection test pass with
  `spec/fixtures/wire/inbox.json` byte-identical; the assistant package and `./internal/archguard` pass; `go list -deps`
  shows only the standard library and contract packages; the item tests' compile time is measured next to the 58 s.

### S9. The stateful module (only after D3)
- **Change:** the remaining core files move to server/internal/assistant/inbox; `*Service` methods become `*Module`
  methods behind inbox.Host, and Service keeps one field and calls the Reader, Actions and Policy surfaces. The
  bridge's reads, checks and writes under the book's lock become calls to Links (section 3.1), with `checkTaskInbox`
  moving into the module behind one, so the bridge's five files leave S2's list. The book-reaching unit tests move
  with a fake Host; a movedClusters row with the "inbox" prefix. One codemod PR, because slicing it would split the
  book's locks across two packages.
- **Done when:** the package's tests pass alone, then the assistant suite, hub goldens, `TestWireSamples`, archguard and
  the four docs tests; timings of a one-line Inbox edit before and after (package compile, test-binary compile,
  check-fast) are in the PR and in #10613's table.

### W1. Web domain folder, starting with the section bar
- **Change:** `web/src/lib/inbox-bar.ts` and its test (pure; they replay `spec/fixtures/inbox` bar*.json) move to the
  domain folder; `web/src/views/inbox/InboxBar.tsx` and `web/src/lib/inbox-view-memory.ts` import the new path.
  `lib/inbox-bar.ts` stays a one-line re-export for `web/src/views/inbox/InboxList.tsx`, which #1995, #4512, #4653 and
  #10920 edit. Add section 3.3's boundary test.
- **Done when:** `cd web && npm test && npm run build` passes, again after merging the latest `origin/main`; the
  first-screen room under `FIRST_SCREEN_BUDGET` is reported before and after (not raised); tsc and eslint are clean.

### W2. Non-Inbox helpers leave lib/inbox.ts
- **Change:** `withToken` (390), `shortTime` (397), `conversationRoute` (555), and the Lark message types with
  `mergeMessages` and `comparePosition` (592-667) move to their own lib modules. `lib/inbox.ts` re-exports them, so none
  of the 27 importers changes here; later PRs flip quiet importers and drop the re-exports. The interaction helpers
  (495-530) wait for #4512, whose hunk touches them.
- **Done when:** npm test and the build pass, with the first-screen room unchanged or its difference reported; tsc and
  eslint are clean; `git diff --stat` shows no importer changed.
- **Landed (#11538):** `withToken` moved to `web/src/lib/media-url.ts`, a new module (in `web/src/lib/api.ts` its
  default token would come from that module's own `getToken`, which the tests that mock `lib/api` cannot replace);
  `shortTime` to `web/src/lib/format.ts`, after `clock`; `conversationRoute` to `web/src/lib/conversation-page.ts`; the
  Lark message types with `mergeMessages`, `comparePosition` and `LarkMention` (a sent message's @s, from the file's
  end) to `web/src/lib/lark-messages.ts`, a new module; and `MailAccount` to `web/src/lib/mail.ts`, its one importer
  outside the Inbox, which no longer imports `lib/inbox.ts`. Each block moved verbatim, and `lib/inbox.ts` re-exports
  every name, so no other importer changed. Each language's first screen is 129 B smaller (40 B left under
  `FIRST_SCREEN_BUDGET` before, 169 B after, ja): the build follows the re-export, so `mergeMessages` and
  `comparePosition` now ship only in the Lark chat's lazy chunk.
- **Deferred:** `interactionMuted`, `interactionTitle` and `interactionAsk`. Re-checked on 2026-11-20, it is #1995 (a
  draft) whose hunk edits `interactionTitle` and reaches `interactionAsk`'s comment; #4512's hunks do not touch them.
  They move once #1995 lands or closes. The rest stays: the Inbox's own (`waitingAgents` among them), or imported by no
  source outside the Inbox (`tightCJK`, the Inbox's `contextXML`).

### A1. Android contracts slice and a module-edge ratchet
- **Change:** the package persona.core.contracts.inbox with one KDoc file saying what belongs there, and a plain JUnit
  source scan next to `InboxFacadeTest` that counts imports and fully qualified references: core:data main files using
  `persona.feature.inbox` stay a subset of today's 7, and other features may use it only from `ChatLinkHost.kt`,
  `ChatLinks.kt` (fully qualified `LarkNativeLink`, 230-232) and `MeetingHandoff.kt`. A note in `docs/android/android.md`.
- **Done when:** the scan and `:core:contracts` unit tests pass (`--max-workers=4`); a planted import fails with the hint.

### A2. Pure reply and hand-off types into the slice
- **Change:** ReplyTarget (sealed: Inbox, Lark), ReplyRequest, ReplyDraft, ReplyDelivery, InboxHandOffRequest,
  SuggestionRequest, SuggestionAction, SuggestionOutcome, InboxDoneCursor and InboxDoneDay leave the feature's
  `InboxRepository.kt`. Each moved @Serializable class pins @SerialName to its old fully qualified name, because the
  reply outbox stores ReplyTarget as polymorphic JSON; so files written by either build decode in the other, which is
  also the rollback. `InboxReplyJournal.kt`, `InboxReplyOutbox.kt` and `InboxReplyReceipt.kt` use only these types, so
  the ratchet's baseline goes from 7 to 4.
- **Done when:** the `:core:contracts`, `:core:data` and `:feature:inbox` unit tests pass; a test decodes an outbox entry
  written under the old names; the PR says whether `android/app/src/main/baseline-prof.txt` was regenerated.

### A3. :core:inbox, and core:data off feature:inbox
- **Change:** a JVM module :core:inbox takes the page-facing `InboxRepository` and its pure types. LarkFailure,
  InboxMuteOption.title and InboxPolicyTier move from @StringRes to keys mapped in feature:inbox; ReplyComposers stays
  out. It also takes the helpers core:data borrows: inboxDocRef, larkThreadSource (without core:hub and R),
  larkInteractivePart, larkAvatar, inboxMuteOptions, applyPolicyTier and inboxPersonaText. The `:feature:inbox` line
  leaves `android/core/data/build.gradle.kts`, the scan forbids core-to-feature edges, and the test fakes (58 in
  feature:inbox, 7 app, 2 chat, 1 settings) change imports.
- **Done when:** `scripts/app/android.sh test` and `lint` pass (#9338's known failure reported, not fixed);
  `scripts/app/android.sh shots` shows no changed Inbox screenshot; core:data depends on no feature module.

### I1. Tier into PersonaWire; the PersonaInbox package with the bar's decisions
- **Change:** the item's `Tier` (`apple/Shared/InboxModels.swift:352-365`) becomes a typealias of a public InboxTier in
  PersonaWire. It is not Codable (items carry the tier as a string), so cache and watch bytes do not change; its
  localized title stays an extension in Shared. The new package takes the pure section-bar decisions from
  `apple/Persona/Views/Inbox/InboxBar.swift` (`Rows`, entries, shown, empty, draws, `InboxBarState`, and `InboxChoice`
  without its title), with what `empty` returns and calls: the nonisolated `EmptyKind` enum and
  `emptyKind(source:kind:query:)`, now nested in the SwiftUI `InboxPage` (`apple/Persona/Views/Inbox/InboxView.swift`),
  which keeps a typealias and a forwarding `emptyKind` so its callers and tests compile unchanged. The empty-state
  titles, messages and views, the landing, `waiting` (it reads Shared's `Interaction` and `SessionKey`), `Rows.init`
  from `InboxProjection` (it now starts with the package's public `init()`, as an extension of another module's struct
  must) and `InboxBarLanding` stay in the app. The Rows unit cases move to `swift test`; the `bar*.json` replay goes
  through `InboxProjection`, so it stays in PersonaTests.
- **Done when:** PersonaInbox's `Package.swift` depends on PersonaWire alone; `swift test` passes in both packages
  without a simulator; iOS and watch builds pass with `-jobs 4`, a generic destination and
  `scripts/persona_config.py xcconfig`, naming the Xcode used or "26.0 not verified"; the PR carries the
  `docs/apple/performance.md` checklist; `scripts/i18n/apple_l10n.py` sync shows no string change.

**Later, not planned in detail:** the web store, API and page modules as they go quiet; the Apple views (after #4512 and
#7495) and the item (with the compatibility tests); Android's other feature edges; the deferred S3 moves; Lark after D9.

**Risks:** a "pure" registration step can change behaviour silently (hook order, `!meeting`, the handshake goroutine,
tool order), which byte-identical goldens, `TestWireSamples`, `TestRoutesAreTheWireOnes` and a full package run catch;
the S2 guards catch new entries in the old lists while both ways exist; the kernel grows only with a second user and a
review; S9 stays behind D3; check-fast gains nothing until #10615 picks option (a) or the package leaves the subtree.

## 5. How we will know it worked

| Measure | Now (287db61638) | Target |
| --- | --- | --- |
| Inbox server commits (of 149 in 14 days) also touching `assistant/service.go`, `assistant/tools.go`, `assistant/bus.go`, `extwire/scopes.go` | 16, 8, 8, 11 | 0 for new routes, tools and bus methods in the 30 days after S6 |
| Files outside the Inbox core reaching the book: readers; the bridge's, all of which write items | 5; 5 | `assistant/service.go` only; 0 after S9, the bridge writing through Links (without S9, they stay on S2's named list) |
| Compile cost of a one-line Inbox edit | 58 s test-binary compile for `internal/assistant` (#10613) | measured for the item package (S8) and module (S9), same machine |
| Web: commits editing `lib/inbox.ts` / raising `FIRST_SCREEN_BUDGET` (of 134) | 47 / 6 | falling / 0 |
| Web: importers of `lib/inbox.ts` outside the Inbox | 27 | falling as re-exports are dropped |
| Android: core:data files using `persona.feature.inbox`; core-to-feature edges | 7; 1 | 0; 0 |
| Apple: Inbox logic under `swift test` | none | the bar's decisions (I1), then more |

## 6. Open decisions for maintainers

| # | Decision | Options |
| --- | --- | --- |
| D1 | Server package location | server/internal/assistant/inbox (recommended; it follows the movedClusters rows and the To-dos precedent), or server/internal/inbox outside the assistant tree. The check-fast gain depends on #10615, so decide that first |
| D2 | Kernel scope | One shared kernel that grows past saved's five members only with a second user (recommended), or a per-module Host struct of functions like saved.Host |
| D3 | Whether S9 happens, and when | Decide after S8 measures the item package against the 58 s and the test rewrite cost is known |
| D4 | Transitional aliases in package assistant | Allowed during the move (recommended); and whether a ratchet removes them later |
| D5 | The task bridge | Stays with tasks as "inbox-tasks" (recommended): it writes items under the book's lock as today until S9, which gives it the Links surface for those writes; or moves with the Inbox, so its writes stay inside the module and it reaches the to-dos through inbox.Host's tasks role. Either way S7 only renames it, and the Reader stays read-only |
| D6 | Model purposes | The purpose table stays central in `assistant/models.go` (recommended), or modules declare their own. inbox.triage is also the model of the knowledge base and agent naming, and the parent of agent.triage, agent.delegate and translate, so renaming it changes configuration and needs its own issue |
| D7 | A route manifest after the pilot | A per-route table (scope, relay kind, list key, receipt, answer-only) that `hub/inbox_routes.go`, `hub/ownership_lists.go` and the path prefix in `hub/bus_forward.go` read as data, or none. The answer-only regexes stay hand-edited either way |
| D8 | Web folder name | web/src/domains/inbox (#10613's wording) or web/src/features/inbox (like the existing `web/src/features/todos`); and one `FIRST_SCREEN_BUDGET`, or a slice per domain |
| D9 | The Lark chat pages, under the Inbox on every client (`LarkThread.tsx` and the rest on the web, 47 files in `apple/Persona/Views/Inbox/Lark`, `LarkScreen.kt` on Android) | Stay in the Inbox, or become their own domain |
| D10 | Android and Apple homes | Android: a package in core:contracts (recommended), or its own module. Apple: a PersonaInbox package now (recommended), or a Features/Inbox folder first; the item's final home is PersonaWire |
| D11 | `/api/v1/translate*` | Stays in the Inbox's routes, or becomes its own module |
| D12 | Whether to reopen Inbox wire generation | Not reopened, so #9117's decision stands (recommended), or reopened on the conditions below |
| - | Windows | When the maintainers schedule S4 and S8, and whether S4 lands with #7891's registry stage |

**D12 in detail.** #9117 ended Inbox wire generation; this plan does not reopen it, and nothing in it depends on it. A
generated type would be a pure replacement only if all of these hold:
- Go has a listed wire type separate from the persisted `InboxItem`, proven equal to `listed()` by S2's reflection test
  (S8 first makes the item a package of its own);
- the generator (`scripts/gen/extension_schema.py`, its `schema/wire/` channel) gains `omitempty` and a per-field Swift
  type for `Date`;
- Swift's hand-written decoder is replaced without changing the StateCache record or the WatchSync payload (two-way
  compatibility tests), and IMAP UIDs (`uint32` in Go, `UInt32` in Swift) never become `Int`, which is 32-bit on
  arm64_32 watches;
- field order is unchanged, because delta cursors and ETags hash the listed bytes, and the behaviour fixtures (partial
  items) get a partial-sample mode.

#9117 estimated leaf types only (web and Swift, Go untouched) at a day or less, the full item at several days.

## 7. Out of scope

- **Any behaviour, wire, UI, setting or default change.** For example, the roster counts Needs you with `status()`,
  while the Lark workbench uses `wbNeedsYou` (tier or urgent); unifying them is for #5974 or a bug issue of its own.
- **Sources** (extmgr, subscriptions), the hub's relay code, HarmonyOS, and splitting `docs/protocol/protocol.md` or
  `docs/assistant/inbox.md` (#10613's docs decision).
- **Known test bugs, for issues of their own:** #9338's failing hand-off tests, `TestHubAnswersOnlyItsOwnRoutes`
  failing since #9485 (section 4), and
  `android/app/src/test/java/persona/android/di/InboxFacadeTest.kt`, which looks for a declaration text
  `android/app/src/main/java/persona/android/di/InboxModule.kt` no longer has (the facade is at line 200), so it scans
  the file up to the assistant facade and checks less than it says.
