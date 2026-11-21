# Plan: the counts people see, computed once in the managers and relayed by the hub (#11679)

**Status: a plan for maintainers to decide on.** It is the Stage C item "Derived counts computed on the hub and shipped
as generated view types" of #10613. #5974 owns what each count means; this plan covers where each count is computed and
how it reaches the clients. Nothing is built; each step becomes its own issue and PR. A step that changes a number
people see is marked **visible**, waits on a named decision (section 6) and carries the principles self-check. **Who it
serves:** everyone: Assistant users see one number for each name on every screen and device, Agents users can trust that
badge, list and Lock Screen agree, and nothing new is drawn.

**How the facts were taken:** read on `origin/main` at e4308ecc23 (2026-11-20); nothing was built or run. Open-PR
overlaps are from the 171 PRs open that day; GitHub lists at most 100 files per PR and 23 reach that cap, so overlaps
may be undercounted. Steps find code by name, since line numbers drift. Paths that do not exist yet are plain text.

## 1. Why

- **The managers send few counts, and the hub computes almost none.** What travels: the Live Activity's FleetState, from
  the default manager only (`hub/fleet_link.go` fleetLiveOf); Persona Link's summary; the roster's `inbox_needs_you`;
  TodoCounts on `GET /api/v1/todos`, which `hub/todo_view.go` adds up across managers; the direction tree's counts; and
  per-row numbers (`pending_interactions`, the children rollup). The hub's own counts are only the sessionlist state
  maps and the catalog (`counts=1`, `with_counts=1`, `catalog=1`). The hello, health and every push carry no count:
  nothing in `server/internal/hub` sets `aps.badge`, and no Android code calls `setNumber`.
- **So every badge is counted by the client that draws it, from raw lists.** "Agents that need you" alone is counted by
  **19 functions in five codebases**. Server: `needsYou` with `liveCounts` (`assistant/fleet_live.go`), `linkAttention`
  (`assistant/fleet_attention.go`), `addCount` (`sessionlist/query.go`). iOS: `AgentsNeedsYou.badge`,
  `AgentsNeedsYou.rows` for the section, `AssistantBadges.waiting`, `WatchPet.status`, `IntentPicker.waiting`. Android:
  `ShellCounts.agentRows`, `agentNeedsYouRows`, `ManagerRoster.waiting`, `NeedsYou.kt` `needsYou`. Web: `stateCounts`,
  manager-attention's `waitingAgents`, `islandState`, `filterCounts`, the Machines page's `agentCounts`, and inbox.ts's
  `waitingAgents` (cards, labelled agents). Rust core: `sort::counts`.
- **The same label shows different numbers for the same data:**

  | One thing waiting | iPhone / Android Agents badge | Web title, favicon, Dock | Persona Link | Live Activity |
  | --- | --- | --- | --- | --- |
  | A muted agent | 0 (on a hub with bounded lists, 1 for a card in a muted space whose row is not loaded) | 1 | 1 | 0 |
  | A quiet task's agent, hub with bounded lists | 0 (iPhone: on the Persona tab if it has a card; Android: 1 if it has a card and its row is not loaded) | 1 | 0 | 0 |
  | A directory-hidden session (an Agent-folder hide rule) | iPhone 0; Android 1 with a card on any hub, and with no card on a hub without bounded lists | 0 | 0 | 0 |
  | A card on no session (`personad ask` outside herdr) | 1 | 0 | 0 | 0 |
  | A Persona conversation with a card | 0 (iPhone Persona tab 1) | 0 | 0 | 1 |

  The switcher (several assistants) counts by the rule `docs/assistant/managers.md` ("Counts") writes, not the Agents
  badge's: a Persona conversation with a card counts 1 there on every client; the iPhone counts a waiting child apart
  from its parent, and Android each card of a session it does not hold (section 2).

- **The Inbox has three units.** Every client's tab or sidebar badge counts unread open action items (iOS `unreadCount`,
  web `badgeCount`, Android `inboxUnreadCount`). The roster's `inbox_needs_you`, the Apple Inbox widget, the face dot
  and the watch count open action items, read or not (`tellInboxNeedsYou`, `actionCount`). The Inbox bar's Needs you
  adds pending cards, one per card.
- **The To-dos badge has four rules:** the web's `ownerBadge`, what waits on the owner (#11085); every open, non-quiet
  to-do, shelved ones too, on iOS release builds and Android (`TodoStore.badgeCount`, `ShellCounts.todos`); the hub's N
  on iOS behind the V1 gate (`TodoFeatures.released = false`); to-dos due today or overdue, across every assistant, on
  the Apple widget.
- **Text in the code and docs contradicts the code.** `InboxStore.actionCount`'s comment, `docs/apple/home-widgets.md`
  ("What each number counts") and the `assistantwire.InboxNeedsYou` comment each say they are "the Inbox badge"; the
  badge reads `unreadCount`. `TodoCounts.needs_you`'s schema text says the To-dos row shows it; no release client reads
  it. `DESIGN_SYSTEM.md` 3.8 says the tab badge is "the same number the row and the list's Needs You show", while
  `spec/fixtures/needs-you` pins a split (`muted_and_quiet_agents_are_rows_but_not_on_the_badge`).
- **Why it happened.** Each surface got its rule when it shipped. The fixtures pin single surfaces (needs-you: the
  Agents tab; attention: Link; todo-counts: the list), some pin splits on purpose (`TestLiveActivityKeepsItsOwnCount`),
  and no test compares surfaces, which `docs/design/proposals/owner-at-a-glance.md` G3 asks for. The one hub-sent count
  clients could use, TodoCounts, is summed across managers while the pages show one assistant: iOS recounts with several
  managers, and the web and Android never adopted it. Lesson: ship per-manager values; leave the scope to the client.

## 2. Inventory

Paths in the server rows are under `server/internal`. Every row was checked at e4308ecc23.

| Count | Computed by | From | Shown on | Semantics |
| --- | --- | --- | --- | --- |
| Live Activity `needsYou` / `working` / `unread` | manager: `assistant/fleet_live.go` buildFleetIn → needsYou(liveCounts, cardStands), foldSidesIn | decorated rows, visible pending cards, mutes | Live Activity (APNs); the open Apple and Android apps (`{type:fleet}`); Android's ongoing notification, widget and tile; the hub's widget wakes (`hub/widget_wake.go` widgetCountsOf) | One per agent. Quiet and directory-hidden rows are skipped (fleetInputsInto); muted agents and subtasks are out; sides count under their parent. A Persona conversation counts only by a standing card. An elicitation does not stand. No card on no session. `working` includes rows that only wait in the background. Default manager only |
| Persona Link `needs_you` | manager: `assistant/fleet_attention.go` linkAttention | same inputs | Persona Link | Rows in needs_you, not the assistant's and not quiet. Muted agents and delegating cards count. At most 20 items plus `more`. Equal to the web badge's rule (`spec/fixtures/attention`) |
| Roster `inbox_needs_you` | manager: `assistant/inbox_store.go` tellInboxNeedsYou; hub: `hub/managers.go` | inbox items' status() | every client's switcher | Open action items, read or not. Told on every change; the hub drops repeats and coalesces the push over 1 s |
| Paged session counts and catalog | hub: `sessionlist/query.go` addCount; `hub/sessions_catalog.go` | the sessionlist index | web badge on bounded hubs; Android paged chips; the Rust core's aggregates | A state map over the whole filtered list. No mutes or quiet applied. `background_waiting` is a subset of working, `just_finished` of idle |
| TodoCounts and places | manager: `assistant/todo/projection/list.go` Counts; hub sum: `hub/todo_view.go` | to-dos, linked sessions, cards | iOS only, behind the V1 gate (TodoCountsSource) | N is the Needs you section, each to-do once; quiet and Can wait are out; a step counts with its parent |
| Direction tree counts | manager: `assistant/direction_tree.go` | todo_triage.go (triageCountsAsOpen) | web and Android leader rows | Tasks by tree state; quiet tasks included, unlike TodoCounts |
| Row `pending_interactions`, state, children | manager: `assistant/fleet_sessions.go` decorateInventory, `assistant/fleet_fold.go` | pending cards, host status | every row | Per row; the basis of every state count. Possible gap, read from code only: the row count takes `s.fleet.interactions`, while pendingCards (`assistant/fleet_ix.go`) also merges session-review cards from the review store |
| iOS Persona tab | `AppModel.countBadges` | cards in scope | tab badges (iPad, visionOS, iPhone Duo) | Cards on Persona conversations and on quiet tasks' agents (#1731), one each; muted and directory-hidden cards out |
| iOS Agents tab | `AgentsNeedsYou.badge` | cards plus the sessions the app holds | Agents tab, top switch, sidebar, Home Agents widget | One per waiting session; a card on no session counts 1; Persona, quiet, muted and directory-hidden are out (hidden cards through directoryVisibleInteractions, hidden waits in `AgentsNeedsYou.waiting`). On a hub with bounded lists, sessions that wait with no card count only if loaded, and a card in a muted space counts if its row is not loaded (`TabBadges.place` reads the space from the rows the app holds; quiet falls back to the to-do list); the full read (readNeedsYou) feeds only the switcher, and only with several assistants |
| iOS Agents section and filter | `AgentsNeedsYou.rows` (ChatsView) | the same | list header, filter chip | The badge's rows with muted and quiet kept; follows the machine and hub filters |
| iOS Inbox | `InboxStore.unreadCount` / `actionCount` | inbox items in scope | tab, drawer row, icon / Inbox widget, face dot | Unread open action items / open action items, read or not |
| iOS To-dos row | `TodoStore.sidebarCount` | to-dos in scope | drawer and sidebar | `badgeCount` (open and not quiet, any section) unless the V1 gate is on |
| iOS switcher | `AssistantBadges.waiting` + `counts` | sessions, cards, roster | switcher, avatar dot | With several assistants: sessions once, each card on the session it names (`sessionKey`, not `requestHome`), so a waiting child counts apart from its parent; Persona conversations by their cards (`.assistant` counts); quiet, muted and directory-hidden out; a card on no session 1; plus `inbox_needs_you`. Where a bounded list is a window, it reads the hub's sessions in Needs you (readNeedsYou) |
| iOS app icon | `AppIconBadge.count` | TabBadges, unreadCount | home screen | Persona + Agents + Inbox unread; set by the app only (`setBadgeCount`), so it goes stale while the app is not running |
| Watch, Siri, visionOS | `WatchPet.status`; `IntentPicker.waiting`; `VisionShell` ornament | phone summary or own reads | frog and complication; Siri; ornament | Watch: card sessions plus needs_you sessions, muted and Persona included, unscoped. Siri: cards, one each. Ornament: Persona plus Agents |
| Android Agents tab | `ShellCounts.agentRows` | cards, sessions | Agents badge | The iOS rule (`spec/fixtures/needs-you`) over the rows the app holds, except that Android reads no `hidden_by_directory`: a directory-hidden session's cards count on every hub (`fleet.interactions` carries every pending card, and HubInteractionCenter keeps them all), and its wait with no card counts on a hub without bounded lists, whose inventory keeps the marked rows (bounded lists leave them out). On a hub with bounded lists, until the Agents page is shown in a run, those are the 50 most recently updated (`HubSessionsRepository` firstPage, Mode.Paged; the cold-start snapshot is cut to 50 too): only that page asks for the complete list (`requiresCompleteMembership`, set while it is observed). Outside them, a session that waits with no card is not counted, and a card counts even if its task is quiet or its space muted, both read from the row |
| Android Persona tab | `Root.kt` assistantRequests.size | cards | not drawn on Glass | Cards on Persona conversations only; a quiet task's card is on no tab |
| Android page and others | `agentNeedsYouRows`; `inboxUnreadCount`; `ShellCounts.todos`; `ManagerRoster.waiting` | | Agents header; sidebar Inbox and To-dos; switcher | Muted and quiet kept; unread action items, minus read Lark rows; open and not quiet; with several assistants, sessions once, Persona conversations counted (no `assistant` check, for cards or for sessions in Needs you), quiet and muted out as the tab reads them, a card of a session the app does not hold 1 each (outside the 50-row window, one per card), plus `inbox_needs_you`. The header and the switcher keep directory-hidden sessions, as the tab does |
| Web Agents badge | `web/src/lib/sessions.ts` stateCounts / useCounts; `web/src/lib/current-assistant.ts` useScopedCounts | rows, or the hub's `state.needs_you` | tab title, favicon, Electron Dock (unscoped); sidebar Home, ShellSwitch (scoped) | Without bounded lists: quiet out, muted in, no sessionless cards. With them (the default): the hub's number, quiet and muted in (`quietCounted: true`) |
| Web Inbox, To-dos, switcher | `web/src/lib/inbox.ts` badgeCount; `web/src/lib/todo-triage.ts` ownerBadge; `web/src/lib/manager-attention.ts` | items, to-dos, rows | sidebar rows, switcher | Unread action items; what waits on the owner; with several assistants, sessions once from the complete list (useFullAgentSessions), children folded, Persona conversations by their cards, a card on no session 1, quiet, muted and directory-hidden out, plus `inbox_needs_you` |
| Desktop | `desktop/src/badge.ts`; `web/src/lib/desktop-island.ts` | the web's numbers | Dock and taskbar; notch island | The Dock shows the web's unscoped Agents number, and nothing resets it on sign-out. The island counts quiet and muted agents and background waits as working |
| HarmonyOS and the Rust core | `native/app-core/crates/persona-core/src/sort/mod.rs` | projection input | HarmonyOS group headers only | Muted counted; background waits as idle; no quiet field. No badges on HarmonyOS |

## 3. Target

### 3.1 One definition per count, computed once in the manager

- **A new pure package**, server/internal/assistant/attention (name and home: D15), shaped like
  `assistant/todo/projection`: it imports only proto and assistantwire, so its tests skip the 58 s compile of the
  assistant package's test binary. Each agents count is one function over the inputs fleetInputsInto already builds,
  and the Live Activity and Persona Link call it; the roster's Inbox count stays a function over the Inbox items'
  status(), as tellInboxNeedsYou computes it today.
- **Each count has an id and a one-line definition** in a new doc, docs/protocol/counts.md (principle 3). An id starts
  with the rule of the client it replaces, so no number changes before its decision.
- **The pilot is the Agents count.** `agents_needs_you` is `spec/fixtures/needs-you`'s badge rule as iOS computes it
  (Android keeps directory-hidden sessions, section 2): one row per waiting session, folded to its parent; Persona
  conversations, quiet tasks' agents, muted agents and directory-hidden sessions (`docs/protocol/protocol.md`, "Agent
  directory visibility") left out; a card on no session counts 1. Its unit, one per session, is the one
  `docs/design/DESIGN_SYSTEM.md` ("One count, per session") takes from `docs/assistant/managers.md` ("Counts"). That
  bullet writes the switcher's rule, which also counts Persona conversations by their cards and names no hide rule, so
  the switcher draws this count plus a part of its own (section 3.3 item 7). Every count follows
  `docs/protocol/agent-states.md`: a background-only wait is not counted as working, and Just finished changes no count.
- **Later ids come one at a time, each after its decision:** `inbox_unread`, `inbox_action`, `todos_needs_you` (from the
  same Counts() call `GET /api/v1/todos` uses, pinned by `spec/fixtures/todo-counts` and `spec/fixtures/todo-view`),
  `persona_cards`, and the Live Activity's and Link's numbers.
- **The composer's Needs you is not a CountsView id.** It stays the `assistant_needs` payload that
  `docs/design/proposals/todos-redesign.md` (7.3, Attention) and `docs/design/DECISIONS.md` (11-19) define:
  `{todos: N, elsewhere: M, groups}`. That payload carries the per-to-do count unit, gates the rolling upgrade (a client
  counts each to-do once only where it is present), and is what the watch, widgets and Live Activity read for it there.
  CountsView's `{n, keys, held, more}` cannot carry its parts, and `schema/wire/todo-view.schema.json` reserves its
  type, so one number keeps one carrier. If both are built, the manager package computes them from the same snapshot.

### 3.2 One small generated view per manager, relayed as is

```
CountsView {                       // per manager; schema/wire, generated for Go, TS, Swift and Kotlin
  epoch: string, rev: int,         // a new epoch (manager restart) resets; a lower rev loses to a higher one
  as_of: int,                      // ms; only to say how old a kept view is
  counts: { <id>: { n: int,
                    keys: [{id, cards[]}],   // counted rows, agent:<machine>/<session> or card:<id>, at most 200
                    held: [{id, why}],       // rows today's list keeps but the count leaves out: muted
                    more: int } }
}
```

The first schema is only what today's rules already say. `held` lists the muted rows that `AgentsNeedsYou.rows` keeps
and the badge leaves out; whether a header draws them is D6, and a delegating card becomes a `why` only if D11 holds it.
Quiet rows are not in it: fleetInputsInto drops them before any input is built (`assistant/fleet_live.go:173`), so
listing them would mean widening that function inside the repair PRs' hunks (and still keeping them out of FleetState
and Link) or reading the lists a second time. If D6 wants the header to show quiet rows, the client keeps drawing them
from its own list, or a later optional `why: quiet` comes with that cost stated. A
`surfaces: {<surface>: <id>}` map (which count each surface draws, so a remap is a server change) is an optional field
added after D5; until then each client names the id it draws.

- **Manager to hub.** The manager names the capability `counts_view` and tells a one-way `counts.view` event, built in
  liveLoop's round after tellLive from the same fleetInputs, so `agents_needs_you`, FleetState and Link's summary come
  from one snapshot. It is deduplicated by bytes as tellLive does, re-told after each handshake (liveChanged(force)),
  and sent after the round's list changes, so a badge is never ahead of its list.
- **The hub stays a relay.** It keeps each manager's latest view as raw JSON on the bus connection, beside
  `inboxNeedsYou` in `hub/bus.go`; it checks only that the view is an object with `epoch` and `rev` of at most 16 KB,
  and never decodes the counts. It puts the view on the roster entry (`GET /api/v1/managers`), with `counts_view: true`
  on the entry of a connected manager that named the capability, and pushes `{type:"counts", manager, view}` to apps
  that name the client feature `counts_view`, with `view: null` when the manager's connection ends (section 3.3 item 5
  says what a client does with it). `counts` is listed in hubMessageTypes, so no manager can broadcast it. The hub
  feature `counts_view` comes from a function appended in features(), as todoViewFeatures does. There is no new hub
  route and no hub arithmetic: clients add up the managers in a surface's scope. Managers' sessions and cards are
  disjoint; a test checks that a key two managers list (mesh, a migration) counts once.
- **Only full-role apps get views.** A view lists every waiting session and card of a manager, so it reaches only what
  may read them all. toWanting checks no role, and `wants()` honours `?features=` for every role, so hub/counts_view.go,
  at connect and on each change, sends only to clients with `role == roleFull` that want the feature, not through
  toWanting alone, as broadcasts skip scoped clients for everything but `sessions` (`hub/http.go`). Scoped device tokens
  get no push; the scoped writer's allowlist (`hub/scoped_tokens.go` scopedSocketEvent) would also drop a `counts`
  frame, a second line, not the rule. Extension tokens open only the routes `extwire.RouteScopes` lists, which has
  neither the roster nor `/api/v1/ws`, and scoped device tokens are refused the roster for the same reason. Answer-only
  devices (the watch) open neither (`answerReads`); whether they may is D12. Persona Link and peer credentials open only
  their link (`allowed` in `hub/devicetokens.go`).
- **Generation stays a pure addition (#9117).** Only CountsView is generated, because no client wrote it by hand.
  FleetState (an ActivityKit ContentState), link.Attention, sessionlist.Counts and InboxItem stay hand-written.
  CountsView does not `$ref` todo-view, since `scripts/gen/extension_schema.py` loads each schema alone.

### 3.3 What clients still compute, and why

1. **The owner's own acts (principle 7).** An overlay, generalising iOS TodoCountsSource's answered asks, holds answered
   or dismissed card ids, Inbox items marked done or read, and local mute changes (which move a key into `held`). The
   number drawn is `n` minus its matches, in the same frame as the tap. Each entry goes pending → received (the hub's
   receipt) → confirmed (its key leaves the view); a failed act, or a key still listed 30 s after receipt, drops the
   entry, so the number honestly comes back and the card's own error says why.
2. **Scope:** which managers and paired hubs a surface adds up (D2).
3. **List-local counts:** filters, search and folds intersect `keys`. Their unfiltered value equals their label's count,
   or is a `differs` row (section 3.5).
4. **Drawing:** hidden at 0, `99+`, the favicon's `9+`, tone, dot or number.
5. **Cold start and gaps:** the last view per hub is persisted (Apple StateCache, Android DataStore, web storage) and
   drawn on the first frame, marked stale. `view: null` and a disconnected manager mean unknown, not old: the last view
   stays, drawn stale (`docs/assistant/north-star.md` "Unknown, never zero"; D13), so a manager restart or redeploy
   never switches the rule. With a view, no client recounts from a partial paged list.
6. **The frozen fallback:** today's rule, moved unchanged into one file per client. It is used only on an explicit
   absence: the hub's hello lists no `counts_view` feature (an old or rolled-back hub), or a connected manager's roster
   entry has no `counts_view: true` (an old or rolled-back manager), or a manager has never sent a view this client
   kept. A surface uses it while any manager in its scope is absent (D10), so one number never mixes two rules.
7. **The switcher's Persona conversations, until D9.** `agents_needs_you` leaves Persona conversations out, while every
   client's switcher counts one with a pending card (section 2). So with a view the switcher draws `agents_needs_you`
   plus that part, counted locally by today's rule, and its Persona conversations keep today's value. The two parts
   share no key, so this is not D10's mixing. D9 decides whether the part becomes a view id or leaves the switcher.

### 3.4 Old clients and old hubs

| Case | What happens |
| --- | --- |
| Old app, new hub | It never names the feature and ignores the unknown roster field. FleetState, link.Attention and `inbox_needs_you` keep their values |
| New app, old hub, or a manager without the capability | No view for that manager: the fallback, as today |
| New manager, old hub | The hub drops the unknown event, as it would `inbox.needs_you` |
| Manager restarts or is redeployed | `view: null`, then a view with a new epoch; meanwhile its last view is drawn stale and no rule changes (D13) |
| Manager or hub rolled back | The hub's hello lists no `counts_view`, or the reconnected manager's roster entry has no `counts_view: true`: the fallback from that message on |
| Several paired hubs | Each hub on its own; the app adds them up |

New WebSocket types and features also need their rows in `docs/protocol/protocol.md` (`scripts/test/test_doc_wire.py`),
and only those, since 51 open PRs touch it; the contract lives in docs/protocol/counts.md, and
`docs/protocol/rolling-upgrade-compatibility.md` gets this table.

### 3.5 The agreement check

- **The definition** is `spec/fixtures/needs-you`, replayed by the manager package (authoritative) and by the iOS and
  Android fallbacks. A fallback is today's rule, so its replay keeps its step-3 marks until step 13 deletes both; a
  client that draws the view also replays a view-path case (the manager package's view for the fixture case, decoded and
  drawn) that asserts the fixture value. **The surface table**, spec/fixtures/counts/surfaces.json, has one row per
  surface in section 2 with its count id, scope, each client's code symbol and test, and per client `matches` (drawing
  the view would show today's number) or `differs` (with the decision that settles it). A guard,
  scripts/test/test_counts_surfaces.py, checks that every symbol exists, `matches` rows with the same label share one
  id, the `differs` list only shrinks, and a hub-fed surface calls its fallback nowhere outside the fallback file.
- **Replays:** each client's replay drives the functions its views call and, with a report switch, writes
  `{lane, case, surface, value}`; scripts/test/counts_agreement.py compares the lanes, reading a fallback lane's marked
  case as its known difference, not a failure. The scheduled main verification runs it
  (`docs/operations/main-verification.md`). A live mock-hub check stays optional: it needs new tooling, since
  `scripts/app/android.sh shots` writes images, not drawn numbers.

## 4. Steps

Steps 2 to 7b change no number. Steps 2 to 4 wait on no decision; step 5 waits on D15 (the package's name and home), and
6 to 7b build on it. They leave out what a decision would settle: step 2 leaves `DESIGN_SYSTEM.md` 3.8 to D6, and step
6 ships no `surfaces` map (D5), no delegating `held` reason (D11) and no quiet one (D6). Steps 8 on wait on named decisions, whether or not
they change a number: a client that draws views builds in D10's mixing rule and D13's handling of a missing view. Each
step re-checks open PRs on its files before it is pushed; the four automated "continue #8584 verification repair" PRs
(#10564, #10593, #10599, #10920) touch many count files.

**1. This plan.** *Change:* this document, one CLAUDE.md index entry, comments on #10613 and #5974 (proposing that the
fixtures of steps 3 and 4 carry #5974's planned count fixture; the definitions stay #5974's). *Conflict:* CLAUDE.md is
in 10 open PRs; one entry in one cell. *Gate:* docs tests, privacy scan. *Rollback:* revert. *Done:* merged; steps 2 to
4 can start, and 5 to 7b once D15 is taken.

**2. Comments and docs that claim the wrong number.** *Change:* correct section 1's claims about what the code counts,
except `DESIGN_SYSTEM.md` 3.8 against the needs-you fixture's split, since choosing between them is D6. Also correct the
needs-you README's sentence that Android's section counts cards (it counts rows, `agentNeedsYouRows`) and the side-fold
README, which names `assistant/fleet_fold_test.go` while the test is in `assistant/fleet_side_fold_test.go`. TodoCounts'
description is schema text, so its four outputs are regenerated. *Conflict:* `InboxStore.swift` has 2 open PRs (#7495,
#4512), `assistantwire/inbox.go` 3 (#9782d, #6539d, #4512), the rest none. *Gate:* none. *Rollback:* revert. *Done:*
`scripts/gen/extension_schema.py --check` and the docs tests pass.

*Landed (#11832):* every correction above, none skipped (no open PR rewrites those lines), plus the same claims in the
`HomeInbox` comment, `docs/apple/ios.md` ("Counts"), `docs/assistant/managers.md` ("Badges") and the web side-fold
replay's comment.

**3. The Agents definition, pinned.** *Change:* every `spec/fixtures/needs-you` case gets `expected.badge_rows`, plus
six cases: a child's card under its parent, a side folded under its parent, a quiet task's card, a directory-hidden card
(counted nowhere), a delegating card (counts today; D11), an elicitation-only card (its session waits). The iOS and
Android replays assert `badge_rows`. A replay that skips a filter its client's badge applies is changed to apply it,
which changes no number: iOS's replay counts the badge from every pending card, while countBadges first drops
directory-hidden ones. A client whose own counting fails a case is not fixed here, since that would change what it
draws: the case is marked as that client's known difference, asserted at today's value and naming D1. The replay
becomes the frozen fallback's (section 3.5), so the mark stays until step 13 deletes the fallback; the client's step (8
or 9) adds a view-path case that asserts the fixture value instead. Android fails the directory-hidden card this way
(it reads no `hidden_by_directory`).
*Conflict:* the fixture and both replays have 0 open PRs. *Gate:* none. *Rollback:* revert. *Done:* Android
`:core:domain:test --tests '*AgentsNeedsYouFixtureTest*'` (`--max-workers=4`) and iOS AgentsNeedsYouFixtureTests pass,
each marked case at today's value, iOS on the implementer's own simulator.

*Landed (#11823):* `badge_rows` on every case and the six cases; iOS's replays (with `HomeWidgetTests`) now drop
directory-hidden cards and fold sides as the app does, and the one mark is Android's on
`a_directory_hidden_card_counts_nowhere` (D1); iOS needs none.

**4. The surface table and the agreement report** (section 3.5). *Change:* spec/fixtures/counts (README, surfaces.json),
the guard, the report switch in the existing replays, the lane differ, and entries in `scripts/ci/paths.py` and
`scripts/ci/check_fast_map.py`. *Conflict:* new files; `spec/README.md` has 1 open PR (#5969d), `paths.py` and
`check_fast_map.py` 0. *Gate:* none. *Rollback:* delete. *Done:* the guard passes and fails on each seeded violation
(unit tests); the baseline count of `differs` rows is in the README.

*Landed (#11900):* 32 rows, 26 of them `differs` (the baseline, in the README); the guard
(`scripts/test/test_counts_surfaces.py`), `PERSONA_COUNTS_REPORT` in both needs-you replays, the differ
(`scripts/test/counts_agreement.py`: iOS agrees on every case, Android on all but its D1 mark) and check-fast's rule.
`scripts/ci/paths.py` needed no entry: CI's lanes already select every file (`spec/**`, and `**` for scripts).

**5. The pure manager package.** *Change:* move needsYou, liveCounts and cardStands out of `assistant/fleet_live.go` and
linkAttention's predicate out of `assistant/fleet_attention.go`; add `agents_needs_you`, replaying the needs-you
fixture. fleetInputsInto and foldSidesIn stay. *Conflict:* the four repair PRs' `fleet_live.go` hunks are in
fleetInputsInto (lines 170 and 180); the moved functions start at line 254. `fleet_attention.go` has 0. *Gate:* D15 (the
package's name and home). *Rollback:* revert. *Done:* TestLinkAttentionFollowsTheSharedFixtures,
TestLiveActivityKeepsItsOwnCount, TestFleetCountsCodexRootsOnly, TestFleetCountsASideUnderItsParent and
TestSnapshotNeedsYouIsTheState pass unchanged; FleetState and Link bytes are identical on the fixtures; the package's
tests run on their own (`go test -p 4`, `GOFLAGS=-trimpath`); no added allocations in a benchmark; the known
internal/assistant failures on main are unchanged.

**6. The schema and generated types.** *Change:* schema/wire/counts.schema.json (`x-out` ts, swift, go, kotlin;
`x-guard` arkts, rust), its four outputs, `.gitattributes` rows and a `schema/wire/README.md` row; samples from step 5's
Go output. The schema has no `surfaces` map (D5), no delegating `held` reason (D11) and no quiet `held` reason (D6,
section 3.2); each is an optional field added after its decision. *Conflict:* schema/wire, the generator and `scripts/test/test_wire_schema.py` have 0 open PRs.
*Gate:* step 5. *Rollback:* revert; nothing reads the types. *Done:* `--check` and test_wire_schema.py pass; round trips
pass in Go, Kotlin, Swift (`swift test` in PersonaWire) and the web.

**7a. The manager tells the view.** *Change:* the capability constant in a new assistantwire/counts.go (avoiding
`assistantwire/bus.go`, 9 open PRs), named in the hello's capability list in `assistant/bus.go` (10 open PRs; one
identifier on one line); the tell from liveLoop. *Gate:* steps 5 and 6. *Rollback:* revert. *Done:* on one round,
`agents_needs_you.n` equals the fixture rule over the same inputs; a quiet fleet tells nothing; a hub built from
`origin/main` drops the event; the round benchmark stays within 5% of step 5's.

**7b. The hub relays it** (section 3.2). *Change:* a new hub/counts_view.go and one field in `hub/bus.go` (1 open PR,
#6924d); one line each in bus_broadcast.go (3) and features() in extensions_link.go (5); the kept views go out in the
connect sequence in `hub/http.go` (7) and after a client hello in `hub/clientmeta.go` (0). Rows in protocol.md,
docs/protocol/counts.md with its CLAUDE.md entry, and a mock domain module under scripts/mock registered with one line
in `scripts/mock/mock_domains.py`'s DOMAINS (0), so `mock_hub.py` (24) is untouched. *Gate:* step 7a (the capability
name). *Rollback:* revert; clients see no feature. *Done:* hub tests: kept raw; sent only to full-role apps that want
it, with a scoped client that asks `?features=counts_view` queued no `counts` frame (checked before the scoped writer's
allowlist, so the role check is tested on its own) and the roster refusing scoped, answer and extension tokens;
`counts_view: true` only on a connected manager that named it; `null` on disconnect; oversized views refused; connect
order hello, machines, counts, then session lists; TestBroadcastTypes, `test_doc_wire.py` and the hub's architecture
guard pass, apart from `TestHubAnswersOnlyItsOwnRoutes`, which already fails on main (#9485) and is compared with
`origin/main`.

**8. Android reads `agents_needs_you`.** *Change:* ShellCounts and the switcher's agents share read the view, with the
overlay and the frozen fallback, and the switcher adds its Persona-conversation part by today's rule (section 3.3 item
7), so a Persona conversation's card stays on it; HubManagers.kt decodes the generated type. Step 3's Android mark
stays on the fallback's replay, which is unchanged, and a view-path case asserts the fixture value. **Visible** in two
cases (section 2, Android rows). Wherever an Agent-folder hide rule matches a session that waits, on any hub: its
cards, and on hubs without bounded lists its wait with no card, leave the badge and the switcher. On hubs with bounded
lists while the app holds its 50-row window: sessions outside it that wait with no card now count, cards on unloaded
quiet tasks' agents or in muted spaces leave, and the switcher counts such a session once, not once per card. That is
the written rule (the needs-you fixture, and `docs/protocol/protocol.md` ("Agent directory visibility") for hidden
sessions), applied once D1 confirms it, with a self-check. *Conflict:* `ShellCounts.kt` and `HubManagers.kt` 0;
`Root.kt` (#11660d, #5969d) is not touched. *Gate:* steps 3 to 7b, D1, D10 (mixed support) and D13 (a missing view).
*Rollback:* revert, or turn off the hub feature. *Done:* ShellCountsTest, with a paged case (a wait with no card
outside the window: counted from the view, not by the fallback), a hidden case (a directory-hidden session's card: left
out by the view, counted by the fallback) and a switcher case (a Persona conversation's card counts with a view and
without), AgentsNeedsYouFixtureTest (the marked case at today's value, the view-path case at the fixture's) and the
surfaces replay pass; `scripts/app/android.sh shots` in three styles, light and dark, shows today's numbers where the
list is complete and no hide rule matches a waiting session.

**9. iOS reads `agents_needs_you`.** *Change:* the tab badge and the switcher's agents share read the view, and the
Home Agents widget's number once D2 has set its scope (until then it keeps today's count); the switcher adds its
Persona-conversation part by today's rule (section 3.3 item 7). Any iOS mark from step 3 stays on the fallback's
replay, with a view-path case beside it. **Visible** in two cases. On hubs with bounded lists, the tab badge: sessions
that wait with no card now count when not loaded, and a card in a muted space whose row is not loaded leaves. With
several assistants, the switcher: where a session and a child folded under it both wait, it counts 1, not 2 (section
2, iOS switcher). Both are the written rule, applied once D1 confirms it, with a self-check. *Conflict:*
`AppModel.swift` 4 (#11456d, #11282, #7517, #2051), `AssistantScope.swift` the four repair PRs, `HomeWidgetWriter.swift`
and `AppIconBadge.swift` 0; `AgentsNeedsYouRows.swift`, which the four repair PRs touch, stays as it is, since the
fallback and the list's section keep reading it. *Gate:* steps 3 to 7b, D1, D10 and D13; D2 for the widget.
*Rollback:* revert. *Done:* AgentsNeedsYouFixtureTests (any marked case at today's value, the view-path case at the
fixture's), AppIconBadgeTests, AssistantBadgesTests (a Persona conversation's card counts with a view and without) and
HomeWidgetTests pass; the PR names the `docs/apple/performance.md` rules followed (P1, P4, P6) with countBadges timings
before and after from an optimized build, light and dark screenshots, and the Xcode used.

**10. Web and desktop read `agents_needs_you`.** **Visible:** muted agents leave the tab title, favicon and Dock; cards
on no session join; quiet agents leave on bounded hubs. *Change:* useCounts and useScopedCounts keep their signatures,
so views do not change; the switcher's agents share (`waitingAgents`) reads the view and its Persona-conversation part
stays counted by today's rule (section 3.3 item 7). Its rule already matches the view's otherwise (section 2, web
switcher row), so its number does not change. Separately, in a bug issue of its own, the Dock resets to 0 on sign-out
and hub change. *Conflict:* `sessions.ts` and `current-assistant.ts` meet the repair PRs; `manager-attention.ts` has
#10920, #10396d; `attention-signals.ts` #8290, #1995d; `SidebarParts.tsx` (12) is untouched. *Gate:* steps 3 to 7b,
D1, D2, D10 and D13. *Rollback:* revert, or turn off the hub feature. *Done:* vitest through check-fast, with a
switcher case (a Persona conversation's card counts with a view and without); `npm run build` after merging
`origin/main` (FIRST_SCREEN_BUDGET); light and dark screenshots; the desktop e2e reads the Dock equal to the web's
number, and 0 after sign-out.

**11. Persona Link and the Live Activity on the same definition** (server only, **visible**). *Change:* Link's and
FleetState's `needsYou` become `agents_needs_you`, with Persona conversations with a standing card as a named part (D4);
wire shapes stay; `spec/fixtures/attention` and TestLiveActivityKeepsItsOwnCount are rewritten; the widget waker decides
on the number the widgets draw. *Conflict:* `fleet_attention.go` 0, `widget_wake.go` 0, `docs/operations/link.md` 1
(#10759). *Gate:* D1, D3, D4. *Rollback:* revert; only numbers change. *Done:* the Go tests and the web
attention-fixture test pass; the lane differ is green for Link.

**12. The next counts, one at a time.** *Change:* each is one server PR (the id, its fixture, its counts.md line), then
one PR per client: the Inbox after D7; To-dos after D8 (`todos_needs_you` per manager, tested equal to that manager's
`GET /api/v1/todos` counts on `spec/fixtures/todo-counts`, which retires TodoCountsFallback's multi-manager case); the
Persona tab, the app icon and the switchers' Persona-conversation part after D9; numbers outside the running app
(alert pushes, the iOS Notification Service Extension, widget wakes) after D12. *Conflict:*
`assistant/todo_projection.go` and `todo/projection` have 4 open PRs (#11179, #11168, #10809d, #9823), `todo-triage.ts`
3; draft PRs #10809 and #9777 change what Needs you holds and must be reconciled. *Gate:* the named decision.
*Rollback:* revert. *Done:* the count's rows in surfaces.json turn `matches`; the lane differ is green.

**13. Retire the fallbacks.** *Change:* when D14's window ends, delete each client's fallback file with its fixture
replay and step-3 marks (the view-path cases stay); the hub keeps FleetState, `inbox_needs_you` and Link for old apps;
the guard fails if a fallback file returns, as a branch that edited it can keep it through the delete (#11476).
*Gate:* D14, `count_skew` at 0. *Rollback:* restore from history. *Done:* every `differs` row is gone or links a
decision.

## 5. How we will know it worked

| Measure | Now | Target |
| --- | --- | --- |
| Functions counting "agents that need you" | 19 (section 1) | 1 in the manager, plus one frozen fallback per client until step 13 (the switchers' Persona-conversation part stays local until D9) |
| `differs` rows in surfaces.json | baseline recorded in step 4 | only shrinks; 0, or each row links a decision |
| Cross-client agreement on the fixtures | not measured: the web replays no needs-you case, no lane compares surfaces | every lane's report equal, in the scheduled main verification |
| `count_skew` per device-day: a drawn number unlike view plus overlay for over 5 s, per surface, no content, in Developing Persona packages (`docs/operations/developing-persona.md`) | no diagnostic | 0 before step 13 |
| Tap to the tapping device's own badge (principle 7) | not measured | the same frame (Apple: under 16 ms on the main thread, optimized build) |
| Card raised to the number drawn on another device | not measured | p95 at most 1 s on the mock hub, reported apart from execution latency |
| Manager cost per liveLoop round | not measured | no added allocations in step 5; within 5% after step 7a |
| Cost of a decided rule change | the server, three clients and the fixtures | one server PR, once D5 holds |
| Open mismatch issues | #5974, #11085 open | closed |

## 6. Open decisions for maintainers

D1, D6 to D9, D11 and D16 decide what a count includes, questions #5974 and the To-dos proposals already hold; they are
listed here to name each step's gate. D2 to D5, D10 and D12 to D15 are this plan's own: which count each surface draws,
its scope, and how views travel and are drawn.

- **D1** The one Agents rule (#5974). Recommended: `spec/fixtures/needs-you`'s badge on every surface named for it and
  as the switchers' agents share, which changes the web's number; Android's wherever a hide rule matches a waiting
  session (it reads no `hidden_by_directory`) and, like the iPhone's, where a bounded list is only partly loaded (steps
  8 and 9); the iPhone switcher's where a session and its folded child both wait (step 9); then Link's and the Live
  Activity's. The switchers' Persona-conversation part is D9's.
- **D2** Scope of the numbers seen outside one assistant's pages (tab title, favicon, Dock, app icon, Live Activity,
  Link, watch, widgets, Siri): the whole hub (recommended: outside the app there is no current assistant, and the
  switcher's dot says where), the current assistant, or the default manager (the Live Activity today).
- **D3** Persona Link's muted agents: counted (today), listed but not counted (recommended), or hidden.
- **D4** The Live Activity's headline: `agents_needs_you` plus Persona conversations with a standing card, as named
  parts, and whether cards on no session appear (recommended: yes to both).
- **D5** The `surfaces` map on the wire (an optional field after step 6), so a decided remap needs no client release
  (recommended), or ids pinned per client.
- **D6** The Agents section header and filter against the badge: `DESIGN_SYSTEM.md` 3.8 says they are equal, the fixture
  pins a split. Either the header counts the badge's rows and shows muted and quiet agents on one uncounted line, the
  muted ones from `held` and the quiet ones from the client's own list (partial on hubs with bounded lists, so a
  complete line needs the optional `why: quiet` of section 3.2), or the doc is amended to the split.
- **D7** The Inbox unit for the tab, the switcher, the widget and the face dot: unread action items, or open action
  items read or not; changed together.
- **D8** The To-dos badge: N in blue (`docs/design/proposals/todos-redesign.md` 3.7), the SOP's open count in neutral
  (owner-at-a-glance U7), or `ownerBadge` (#11085).
- **D9** The Persona tab and the app icon: cards on Persona conversations and quiet tasks' agents (iOS), Android's rule,
  or the composer's Needs you (owner-at-a-glance G3), read from todos-redesign's `assistant_needs` payload, not a
  CountsView id (section 3.1); and with it whether the icon stays the sum of the tabs (`DESIGN_SYSTEM.md` 3.8). Also
  the switchers' Persona-conversation part (section 3.3 item 7; every client counts a Persona conversation with a card
  today): kept as a view id of its own (recommended, so the switcher loses nothing), or left out.
- **D10** Mixed support on one hub: draw the view for a surface only while no manager in its scope is absent (section
  3.3 item 6; recommended), or per manager, which mixes two rules in one number.
- **D11** Delegating and supervisor-held cards: counted (iOS and Android badges, Link), or held (a `held` reason) until
  first dispatched to the owner, as north-star counts interruptions.
- **D12** Numbers outside the running app: alert pushes carry each manager's icon value and the iOS Notification Service
  Extension adds them up per hub (recommended: a plain `aps.badge` from one hub overwrites the others' share on a
  multi-hub device), plain `aps.badge`, or set by the app only (today). Also: whether Android sets a notification
  number, and whether answer-only devices (the watch) may read the views; `answerReads` in `hub/devicetokens.go` lists
  neither the roster nor `/api/v1/ws`.
- **D13** A disconnected manager, and `view: null`: its last view kept and drawn stale until it reconnects, so a
  restart or redeploy never switches rules (recommended), or dropped from totals. Either way only an explicit absence
  (section 3.3 item 6) brings the fallback.
- **D14** The fallback window before step 13: the oldest hub version that must send views, and how long `count_skew`
  stays at 0.
- **D15** The package's name and home (assistant/attention recommended), and whether archguard pins its imports.
- **D16** The direction tree's `needs_you`: it joins the view after the to-dos convergence moves it onto the projection
  (recommended), or earlier under an id of its own.

## 7. Out of scope

- **Deciding what a count includes** (#5974, U7, todos-redesign 3.7, #11085, #10787, #9731): each lands once, in the
  manager package.
- **The composer's Needs you and its carrier:** `assistant_needs` stays todos-redesign's payload, gate and readers
  (section 3.1).
- **How badges look:** tones and the marks vocabulary (#4538, #4547, #4653).
- **List-local counts and totals:** chips, section and group headers, the Machines page, "N pending", "N more", the
  sessionlist counts and catalog that filter lists (#994), feed and metric totals (Today, brief,
  `docs/assistant/north-star.md` interruptions, `docs/assistant/agent-work.md` "Daily confirmation counts"), Lark and
  personal-agent unread counts, extension badges.
- **Regenerating any hand-written type** (FleetState, link.Attention, InboxItem; #9117 and the Inbox plan's D12).
- **Other Stage C parts and new badges:** the Rust core (#6793); HarmonyOS, Wear OS (#8554), Android's launcher.
- **Splitting `docs/protocol/protocol.md` or `docs/design/DESIGN_SYSTEM.md`:** #10613's docs decision.
