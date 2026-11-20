# Phase 2 design: the `sync` domain — iOS survey and fixtures-first plan (#6793, design issue #9230)

This is the survey and fixtures-first design for the last item of phase 2 in `docs/plans/plan-rust-core.md`:
`sync`, the layer that turns lifecycle and transport results into the requests, reconnects, backoffs and catch-ups
that keep one chat current. `continuity` already owns what a batch of items does to one chat; `transport` already
decodes hub bytes into `continuity`'s inputs. Neither decides *when* to ask for a page, *when* to re-open a socket,
*when* to declare a subscription stalled, or *when* a chat is caught up. That decision layer is `sync`.

The behavioral reference is iOS (decision 7 of the parent plan). The initial pure Rust driver (#9783) implements
single-group reconnect backoff, the event cursor and lifecycle. Its [fixture manifest](../../spec/fixtures/sync-driver/README.md)
records the host evidence and the boundary with native code. The remaining sections describe the broader design.

**Who it serves**: everyone who uses the phone apps, indirectly, exactly as the parent plan. Nothing the user sees
changes. The goal is that reconnects, catch-ups and foreground resumes behave identically on every phone because
they are decided in one place.

## 1. Scope and the boundary with `continuity`

| Layer | Owns | Already in the core? |
| --- | --- | --- |
| `transport` | Bytes → typed events (`ItemsResponse`, `NewerResponse`, fold range, `items` frame). | Yes (`native/app-core`, #7385). |
| `continuity` | One chat's items, `covered`, `hasMore`, opened folds; what a batch leaves to do (`Gap.none / fill / reload`) and the immediate `fetch_after` / `fetch_latest` / `subscribe` actions. | Yes (`persona_core::continuity`, 38 cases / 94 steps). |
| `sync` (this design) | Socket lifecycle (open, drop, grace, reconnect with backoff), the event-resume cursor, per-chat subscription lifecycle, when to turn a `Gap` into a request and when to collapse duplicate requests, stalled-subscription detection and catch-up pacing, background prefetch scheduling, catch-up completion (the "Updating…" barrier), and foreground/background transitions. | Socket lifecycle, cursor and foreground/background transitions only (#9783). |
| Outbox and sends | Retry of a *write* through a gap (`OpRetry`, op ids), pending bubbles. | No — a separate later domain; only the seam is named here. |
| Storage | `ChatCache`, `AgentSync`'s rev ledger, the launch snapshot. | Phase 4 (`docs/plans/plan-rust-core-phase4-storage.md`, #9085). |

`sync` sits between the OS and `continuity`: it feeds `continuity` the batches it fetched, reads back the `Gap`,
and decides what to do next. The proposed slice keeps that split: `continuity`'s pure per-chat reducer is reused
unchanged; `sync` adds the driver around it.

## 2. iOS survey

All paths are relative to the repository root. Line numbers refer to the sources at the base of this design
(`origin/main` when issue #9230 was claimed). Where a rule spans several functions, each is listed.

### 2.1 Inputs

| Input | Where it enters | Notes |
| --- | --- | --- |
| HTTP page from the end (`ItemsResponse`) | `apple/Shared/APIClient.swift:964` `items(_:beforeSeq:turns:)`; applied at `apple/Persona/App/AppModel.swift:2057` `loadInitial`, `:2103` `reload`, `:2129` `loadOlder` | `turns` + `limit=80`, `fold_work=1`, `lazy_images=1`. |
| HTTP catch-up page (`NewerResponse`) | `apple/Shared/APIClient.swift:1057` `items(_:afterSeq:)`; applied at `AppModel.swift:2084` `fill` and `apple/Persona/App/AgentSync.swift:296` `pullInto` | Carries `history`, `from_seq`, `to_seq`, `more_after`. |
| HTTP fold range | `APIClient.swift:984` `foldedWork(_:_:)`; applied at `AppModel.swift:2112` `unfold` | Pages of ~1000 items until `more` is false. |
| WS frame (`items`, `hello`, `sessions`, `sessions_delta`, `revs`, `delta`, `error`, …) | `apple/Shared/LiveConnection.swift:727` `receive`, decoded at `:228` `ServerEvent.decode`; dispatched at `AppModel.swift:2485` `handle(_:group:)` | Decode happens off the main thread (`LiveConnection.swift:731`). |
| Frame metadata (broadcast cursor) | `apple/Shared/Reconnect.swift:91` `FrameMeta.read`; applied at `LiveConnection.swift:824` `note` | `event_stream`, `event_seq`, `resumed`, `hub_restarting`. |
| Lifecycle: foreground / background | `apple/Persona/App/PersonaApp.swift:82` `.onChange(of: phase)`; fans out to `resumeLinks` / `suspendLinks` (`apple/Persona/App/HubLinks.swift:152`, `:160`) | Background closes every socket on purpose. |
| Lifecycle: silent push wake | `PersonaApp.swift:410` `wakeLinks`; `HubLinks.swift:170`; `LiveConnection.swift:574` `wake()` | Connects while `suspended`. |
| Network path change | `apple/Persona/App/ConnectionStatus.swift:182` `pathChanged`; calls `LiveConnection.reconnectNow()` / `probe()` (`LiveConnection.swift:587`, `:603`) | `NWPathMonitor`. |
| Heartbeat / ping timer | `LiveConnection.swift:670` `heartbeat`, `:688` `ping` | `pingEvery` 20 s, `slowPingEvery` 5 s, `slowAfter` 5 s, `deadAfter` 45 s (`LiveConnection.swift:525-528`). |
| Grace / reconnect timers | `LiveConnection.swift:846` (grace), `:888` (reconnect), `AgentSync.swift:98` (300 ms plan coalescing), `:207` `lookAgain`, `:218` `replan` | All `Task.sleep`. |
| Stalled-subscription signals | `AgentSyncPacing.catchUpAt` inputs: session `updatedAt`, chat `newestLineAt`, `RevReads`, `arrivedAt`, `lastCatchUp`, `caughtUpFor` (`apple/Packages/PersonaKit/Sources/PersonaKit/AgentSyncPacing.swift:125`) | Read in `AgentSync.checkStall` (`AgentSync.swift:173`). |
| User: open / close a chat | `AppModel.swift:1814` `subscribe(_:by:)`, `:1825` `unsubscribe(_:by:)`; visibility via `:2032` `show` / `:2039` `hide` | Owner-keyed, so two views of one chat do not cancel each other. |
| User: scroll to older history | `AppModel.swift:2129` `loadOlder` | `hasMore` and `isLoadingOlder` gates. |
| User: open a fold | `AppModel.swift:2112` `unfold`; deduped by `unfolding` (`:2124`) | One task per `(chat, fold id)`. |
| Hub switch / regroup | `AppModel.swift:4037` `hubChanged`; `HubLinks.swift:246` `learned`, `:303` `watchCenter` | Group-level, not chat-level. |
| Cache restore (cold start) | `apple/Shared/ChatStore.swift:552` `ChatStore.init` reads `ChatCache.load` (`:1594`) | Paints the last two user-question turns plus any retained subagent-launch exchanges (`ChatCache.tail`, `:1626`) before any sync. |

### 2.2 State

| State | Where it lives | Meaning |
| --- | --- | --- |
| Socket `State.idle / connecting / connected / failed` | `LiveConnection.swift:392` | One per hub group. |
| `generation` | `LiveConnection.swift:512` | Invalidates late callbacks from a cancelled socket. |
| `backoff` (1 s doubling to 20 s) | `LiveConnection.swift:513`, reset at `:539`, `:582`, `:589`, applied at `:881` | Ordinary (non-drop) reconnects. |
| `LinkDrop { at, restart }`, `inGrace`, `graceOver` | `Reconnect.swift:10`; `LiveConnection.swift:468`, `:471`, `:473`, `:836` `dropped` | Grace 8 s, restart grace 30 s (`Reconnect.swift:17-18`). |
| `fastTries`, `tries` | `LiveConnection.swift:517-518` | Retry counters inside a drop, split by the restart fast phase (`Reconnect.swift:20`). |
| `rejected` (401), `accessBlocked` | `LiveConnection.swift:459`, `:463`, set at `:766-769` | `rejectedRetry` 60 s (`:460`). |
| `stalled`, `rtt`, `lastHeard`, `lastPong` | `LiveConnection.swift:453-455`, `:524`, `:529` | Weak-link detection. |
| `suspended` | `LiveConnection.swift:534`, set at `HubLinks.swift:154`, `:162`, `:737` | Background: nothing re-opens the socket except `wake()`. |
| `resumed`, `personaContracts` | `LiveConnection.swift:481`, `:483`, set at `:826`, `:748` | Whether the hello resumed the broadcast stream. |
| `EventCursor { stream, seq }` | `Reconnect.swift:40`; owned at `LiveConnection.swift:485` | Query items for the next handshake (`Reconnect.swift:45`). |
| `restartNotice`, `closedQuiet` | `LiveConnection.swift:515`, `:520` | Close classification for the next `dropped(restart:)`. |
| Per-chat `subscribers: [SessionKey: Set<UUID>]`, `shown`, `visibleSession` | `AppModel.swift:1640-1642` (`visibleSession` declared at `:357`) | Who keeps a chat subscribed and on screen. |
| Per-chat `ChatStore` (`items`, `ids`, `history`, `covered`, `hasMore`, `loadedOnce`, `opened`, `newestFrom`, `arrivedAt`, `resets`) | `ChatStore.swift:437-550` | The continuity state the core already owns. |
| `filling: Set<SessionKey>` | `AppModel.swift:1643`; used at `:2085` | Collapses concurrent `fill` for one chat. |
| `unfolding: [String: Task]` | `AppModel.swift:2124` | Collapses concurrent `unfold` per `(chat, fold)`. |
| `ConnectionStatus` phase + `awaiting` parts + `catchUpIDs` | `apple/Persona/App/ConnectionStatus.swift:8-62` | The "Updating…" barrier. |
| `DeltaPacer` (`pending`, `drawn`) | `apple/Persona/App/DeltaPacer.swift:8`; owned at `AppModel.swift:2323` | Coalesces streamed text at 5/s (`every = 0.2`). |
| `AgentSync`: `known`, `revReads`, `synced`, `queue`, `running`, `lastPull`, `failed`, `replanAt` | `AgentSync.swift:24-41` | Background prefetch of recent chats. |
| `AgentSync` stall tracking: `lastCatchUp`, `caughtUpFor`, `stallChecks`, `catchingUp` | `AgentSync.swift:163-166` | On-screen stalled-subscription catch-up. |
| `AgentSyncPacing.RevReads { moves, read, movedAt }` | `AgentSyncPacing.swift:84` | Whether the session rev moved past what the chat's reads cover. |

### 2.3 Request / retry rules

| Rule | Source |
| --- | --- |
| `subscribe` carries `since_seq = covered` only when `covered > 0`, always `fold_work: true`, `lazy_images: true`. | `AppModel.swift:2046` `subscribeMessage`. |
| A `Gap.fill` triggers `fill` immediately (no settling timer). `fill` loops up to 8 pages of `after_seq = covered`, re-reading `covered` each page; if a fill is still required after the eighth page it falls back to `reload` (latest page) without issuing a ninth `after_seq`. | `AppModel.swift:2068` `settle`, `:2084` `fill` (the 8-page cap at `:2089`, fallback at `:2099`). |
| A `Gap.reload` triggers `reload`, which fetches the latest page and calls `ChatStore.replace`. | `AppModel.swift:2103` `reload`; `ChatStore.swift:1073` `replace`. |
| `filling` collapses concurrent `fill`s for one chat; a live push can overtake the backlog, and `apply` may return `.fill` while the backlog is in flight — the overlap is safe because `merge` is idempotent by id/seq. | `AppModel.swift:2085`, comment at `:2073-2077`. |
| After a non-resumed reconnect, each subscribed chat with `covered > 0` also runs its own `fill`, guarded by `shouldApply` (still subscribed, same store). | `AppModel.swift:2194-2197`. |
| `loadOlder` requires `hasMore && !isLoadingOlder && items.first != nil`; fetches `before_seq = first.seq`, then `prepend`. | `AppModel.swift:2129-2142`; `ChatStore.swift:1115` `prepend`. |
| `unfold` dedups per `(chat.path, fold.id)`; pages `from_seq…through_seq` until `more` is false, then replaces the fold once. | `AppModel.swift:2112-2123`; `APIClient.swift:984-998`. |
| Background prefetch: up to 40 recent sessions (24 h window), 3 concurrent pulls, only when the chat is neither retained nor subscribed; a working agent is pulled at most once per 60 s (`workingGap`). | `AgentSync.swift:18-22`, `:105` `plan`, `:229` `pump`; `AgentSyncPacing.swift:8`, `:12` `prefetchAllowed`, `:18` `heldUntil`. |
| Background prefetch failure: `retryFirst` 30 s doubling to `retryMost` 300 s; a hub refusal (4xx other than 401/403/408/429) additionally waits for the transcript's rev to change. | `AgentSyncPacing.swift:26-27`, `:51` `retryAt`, `:60` `refused`. |
| On-screen stalled subscription: ask `after_seq = covered` once `catchUpAt` says so — rev unread or `updatedAt` newer than the newest line by more than `staleSlack` (1 s), after `staleQuiet` (4 s) of no arrivals and at most once every `stalePullEvery` (10 s), and not twice for the same `updatedAt`. | `AgentSync.swift:173` `checkStall`; `AgentSyncPacing.swift:77-80`, `:125` `catchUpAt`. |
| Reconnect after an ordinary failure: 1 s doubling to 20 s. After a drop while connected: one fast retry (300–600 ms), then the same backoff; after a hub restart: 150–400 ms, then 0.5–1 s for 30 s, never counted as failure. A 401 slows every retry to at least 60 s. | `LiveConnection.swift:872` `scheduleReconnect`; `Reconnect.swift:31` `LinkDrop.retryDelay`; `LiveConnection.swift:460`, `:885`. |
| Writes through a gap use op ids and `OpRetry`: 0.5 s, 1 s, 2 s, then 4 s, up to 60 s, only for gap-classified errors. | `Reconnect.swift:109-150` `OpRetry`. Out of this slice's scope; named for the seam. |

### 2.4 Dedup and cancellation

| Mechanism | Source |
| --- | --- |
| Item dedup by id, newest copy wins (while retained supervision-promotion evidence is preserved across replacement, `ChatStore.swift:651-685`); out-of-order batches sorted by `(seq, part)`. | `ChatStore.swift:645-649` `newestCopies`, `:686` indices, `:1111` `ordered`. |
| Concurrent `fill` per chat collapses via the `filling` set. | `AppModel.swift:1643`, `:2085`. |
| Concurrent `unfold` per `(chat, fold)` collapses via the `unfolding` task map; a second caller awaits the same task. | `AppModel.swift:2114-2121`. |
| `loadOlder` guarded by `isLoadingOlder`. | `AppModel.swift:2131-2133`. |
| Socket callbacks invalidated by `generation`; a cancelled socket's late receive/ping/reconnect does nothing. | `LiveConnection.swift:512`, `:611`, `:674`, `:693`, `:741`, `:890`. |
| `shouldApply` fences async chat work: the chat must still be subscribed and the same store instance. | `AgentSync.swift:298`; `AppModel.swift:2090`, `:2092`, `:2196`. |
| `ConnectionStatus.startCatchUp` bumps `catchUpIDs[group]` so a stale timeout cannot finish a newer catch-up. | `ConnectionStatus.swift:201-213`. |
| `AgentSync.lookAgain` / `replan` keep only the earliest pending date per chat / global. | `AgentSync.swift:207-227`. |
| `AgentSync` marks a session's rev as read when its subscription is (re)sent, so a backlog covers the moves heard so far. | `AgentSync.swift:78` `subscribing`; `AgentSyncPacing.swift:99`. |
| A chat whose subscription is (re)made drops its live text: deltas are not replayed. | `AppModel.swift:1819`, `:1829` `forgetAgentLive`, `:2187`; `ChatStore.swift:896` `backlogArrived`. |

### 2.5 Backoff and timing constants

| Constant | Value | Source |
| --- | --- | --- |
| Ordinary reconnect backoff | 1 s → 20 s, doubling | `LiveConnection.swift:513`, `:882`. |
| Drop fast retry | 300–600 ms once | `Reconnect.swift:33`. |
| Drop backoff afterwards | 1 s → 20 s, ±20 % jitter | `Reconnect.swift:34`. |
| Restart fast phase | 150–400 ms, then 0.5–1 s for 30 s | `Reconnect.swift:20`, `:32`. |
| Grace (ordinary / restart) | 8 s / 30 s | `Reconnect.swift:17-18`. |
| Rejected (401) retry floor | 60 s | `LiveConnection.swift:460`, `:885`. |
| Heartbeat ping | every 20 s; every 5 s while stalled | `LiveConnection.swift:525`, `:528`, `:673`. |
| Stalled threshold / dead threshold | 5 s / 45 s | `LiveConnection.swift:526-527`. |
| Connection title delay | 1500 ms (0 once grace is over) | `ConnectionStatus.swift:45`, `:125`. |
| Catch-up barrier limit | 8 s | `ConnectionStatus.swift:47`. |
| Background prefetch window / count / concurrency | 24 h / 40 / 3 | `AgentSync.swift:18-20`. |
| Background working pull gap | 60 s | `AgentSyncPacing.swift:8`. |
| Background failure retry | 30 s → 300 s | `AgentSyncPacing.swift:26-27`. |
| Stall quiet / pull-every / slack | 4 s / 10 s / 1 s | `AgentSyncPacing.swift:77-80`. |
| AgentSync plan coalescing | 300 ms | `AgentSync.swift:99`. |
| Delta pacer | 0.2 s (5/s) | `DeltaPacer.swift:9`. |
| Live-text clear fallback | 5 s | `ChatStore.swift:913`. |
| Chat-cache save debounce | 2 s | `ChatStore.swift:1044`. |
| Op retry through a gap | 0.5/1/2/4 s, 60 s cap | `Reconnect.swift:112-115`. |
| Silent-push wake window before re-suspend | 25 s | `PersonaApp.swift:415`. |

### 2.6 Error classification

| Class | Rule | Source |
| --- | --- | --- |
| Offline (no network / unreachable) | `NSURLErrorDomain` codes: notConnected, networkConnectionLost, cannotConnect, cannotFindHost, DNS, dataNotAllowed, roamingOff, callIsActive, secureConnectionFailed. | `APIClient.swift:2222-2232` `isOffline`. |
| Quiet (no user-facing line) | Cancellation, timeout, offline. | `APIClient.swift:2236` `isQuiet`, `:2239` `shown`. |
| Gap (write may not have reached a running hub) | Offline, timeout, 502/503/504 without a hub `code` or `delivered`, or `op_pending`. | `Reconnect.swift:156-164` `Error.isGap`. |
| Refused credential | Handshake HTTP 401 → `rejected`, slow retry, "Pairing Expired". | `LiveConnection.swift:765-766`, `:770`; `ConnectionStatus.swift:19`, `:120`. |
| Cloudflare Access block | Detected from the handshake response; kept through retries. | `LiveConnection.swift:463`, `:768`. |
| Hub-refused prefetch (do not retry until rev changes) | 4xx except 401, 403, 408, 429. | `AgentSyncPacing.swift:60` `refused`. |
| Error frame for a chat | Marks that chat's catch-up part done, sets `chat.error` unless it is a timeout text. | `AppModel.swift:2734-2737`. |
| Stalled socket | Ping unanswered for 5 s, or RTT > 5 s; not dead until 45 s quiet. | `LiveConnection.swift:453`, `:676`, `:698`, `:703`. |

### 2.7 Foreground / background

| Transition | Behavior | Source |
| --- | --- | --- |
| Active | `resumeLinks`: `suspended = false`, `ensureConnected()` per group (backs off to 1 s and reopens unless already connecting/connected). Plus a refresh pass. | `PersonaApp.swift:82-107`; `HubLinks.swift:152-157`; `LiveConnection.swift:580-583`. |
| Background | `suspendLinks`: `suspended = true`, `disconnect()` per group, plus tunnel teardown. The hub then treats the phone as away and uses silent push. | `PersonaApp.swift:115-122`; `HubLinks.swift:160-167`; comment at `LiveConnection.swift:531-533`. |
| Silent push while suspended | `wakeLinks(group)` → `wake()`: clears `suspended` and connects. Each push also schedules its own uncancelled 25 s re-suspend if the app is still backgrounded (`PersonaApp.swift:415-420`), so the socket stays up for that window — and because the delay is scheduled per push and never cancelled, an earlier push's deadline can re-suspend the app sooner than 25 s after a later wake (not a sliding timer). A wake received while foreground simply leaves `suspended` false. | `PersonaApp.swift:410`, `:415-420`; `HubLinks.swift:170-172`; `LiveConnection.swift:574-577`. |
| Network returns | `reconnectNow()` per group: reset backoff, replace a hanging attempt, and also reopen a **stalled** socket (`state != .connected || stalled`); only a connected, non-stalled socket is left alone. A path change with the network still up only `probe()`s (ping). | `ConnectionStatus.swift:188-194`; `LiveConnection.swift:587-591`, `:603-606`. |

### 2.8 Hub switching

| Aspect | Behavior | Source |
| --- | --- | --- |
| `connect` to a different config | Resets the `EventCursor` (another hub's stream is not ours), resets backoff, clears `rejected` / `accessBlocked`, opens. | `LiveConnection.swift:536-543`. |
| Credential traded mid-session | `renewCredential` swaps the token on the same config, keeps the cursor (same hub), reopens immediately unless idle. | `LiveConnection.swift:548-558`. |
| Group regroup (mesh learned) | Rebuilds links, forgets gone groups (and their `ChatCache` folders), connects new ones. | `HubLinks.swift:278-297`; `ChatStore.swift:1587` `ChatCache.removeGroup`. |
| `hubChanged` (the picked group changed) | Hub-level stores are forgotten and reloaded; per-chat `ChatStore`s are keyed by group and unaffected. | `AppModel.swift:4037-4060`; `ChatStore.swift:1576-1584`. |
| Center recheck | `watchCenter` periodically probes preferred candidates and `reroute()`s when one answers. | `HubLinks.swift:303-313`; `LiveConnection.swift:595-599`. |
| Transcript replaced under a continuing chat | `ChatStore.apply` sees a different `history`, resets, and returns `.reload` unless the batch is self-contained. | `ChatStore.swift:592-596`. This is `continuity`, already in the core; `Action::HubChanged` settles as `.fill`. |

### 2.9 Sync-completion ("Updating…") criteria

| Rule | Source |
| --- | --- |
| After a reconnect that did **not** resume, the parts are: `interactions` for the group plus one `chat:<path>` per subscribed chat. A resumed reconnect starts no parts (an empty catch-up only lets the outbox go). | `AppModel.swift:2158` `ReconnectPlan`, `:2182`; `Reconnect.swift:177-181`. |
| A chat's part completes when its backlog arrives (the `items` frame with `backlog: true`), or when an `error` frame names it. | `AppModel.swift:2669`, `:2735`. |
| The interactions part completes when the `GET /interactions` attempt finishes — the `defer` fires `caughtUp` even when the request throws or returns nil, before the `try?` guard, so the barrier tracks an *attempted* refresh, not a successful application. | `AppModel.swift:2201-2207`. |
| The whole catch-up completes when every part reported, or after the 8 s limit (a chat whose machine went away must not hang the title). | `ConnectionStatus.swift:200-227`; `catchUpLimit` at `:47`. |
| `onCaughtUp` fires once per finishing round or timed-out group, **not** once globally: `finishCatchUp` calls it unconditionally (`ConnectionStatus.swift:223-227`), so a timed-out group fires it even while other groups are still awaiting, and an empty parts list fires it immediately at `:206`. The outbox flush therefore may run several times per reconnect. | `ConnectionStatus.swift:50`, `:206`, `:223-227`; `AppModel.swift:571`. |
| Title phases: `online`, `waitingForNetwork`, `connecting`, `updating`, `rejected`; shown only after a 1500 ms delay unless grace is already over; multi-hub shows `connecting` only when *no* group is up. | `ConnectionStatus.swift:9-30`, `:127-146`. |

## 3. Fixtures-first design

### 3.1 What already exists and is reusable

| Corpus | What it gives `sync` | Reuse |
| --- | --- | --- |
| `spec/fixtures/continuity` (38 cases / 94 steps) | Per-chat batch application, gap detection, the `fetch_after` / `fetch_latest` / `subscribe` / `fetch_older` / `fetch_range` action vocabulary, and their parameters (`since_seq`, `after_seq`, `before_seq`, `fold_work`). | **Reuse as-is.** The `sync` driver emits exactly these actions; the fixtures pin their parameters. The `reconnect` event kind already models "reconnected, build the subscribe action from `covered`". |
| `spec/fixtures/wire` (17 cases the decoders own, plus connection/items/events samples) | Byte-level inputs for `transport.decode_*`; `hello` with `features`, `event_stream`, `event_seq`, `resumed`; `items` frames with `backlog`, `has_more`, `more_after`; `revs`. | **Reuse the byte payloads** as the inputs the `sync` slice receives after decoding. `sync` does not re-decode; it consumes `continuity`/`transport` outputs plus `FrameMeta`. |
| `spec/fixtures/sync` (8 files: sends, cards, reconnect, relaunch, restart, devices, wake, window) | Full multi-device stories with faults, `wait` steps, `hold`/`release` of stale reads, `socket.drop`/`open.resumed`, and a `requests` expectation vocabulary (`{"ws": "subscribe", "since_seq": 4}`, `{"get": "/items", "after_seq": 4}`). | **Reuse the scenario stories and the request vocabulary.** These are the acceptance scenarios; the new `sync` fixtures decompose the driver-level decisions the stories imply into deterministic, single-group steps (one hub group, possibly several subscribed chats, so the catch-up barrier and per-chat fill fan-out are covered). The stories themselves stay the end-to-end contract (already replayed by iOS `SyncScenarioTests`, Android, web, Go). |

### 3.2 What must be new: `spec/fixtures/sync-driver`

The existing `sync` stories assert *what the screen shows* and *which requests were sent*, but they run the whole
app. The `sync` domain in the core needs unit fixtures that pin the driver itself, with explicit clocks and no
app. New domain: `spec/fixtures/sync-driver/`, following the repository conventions (`spec/README.md`), English
`description`, `source` citing the Swift file and function from section 2.

Each topic file follows the `continuity` envelope: `{"version": 1, "cases": [...]}`, where each case is
`{"name", "description", "source", "input", "expected"}` with `input.initial` and `input.events` (and `from_test`
or `derived`, as `continuity` uses). `expected` is per step, aligned with `input.events` by index.

- `input.initial`: the driver state — per-chat `{covered, hasMore, loadedOnce, subscribed}`, socket `state`,
  `cursor`, `backoff`, an optional `drop`, and the wall clock `now` (all fixtures use a fixed base instant, as
  `spec/fixtures/sync` does with January 2026).
- `input.events`: ordered inputs, one of the kinds below. **No event reads a clock or a network**: every
  time-dependent input carries its own `now` (an offset in milliseconds from the base), and every network result
  is an explicit event.
- `expected`: per step, the full driver state plus the `actions` emitted that step (empty when none). Actions are
  the *requests to the host*, never performed by the core.

Event kinds (names follow the existing `continuity` vocabulary where they overlap):

| Kind | Carries | Models |
| --- | --- | --- |
| `foreground` / `background` | — | `resumeLinks` / `suspendLinks`. |
| `wake_push` | optional group | Silent push (`wake()`). |
| `network_path` | `up: Bool` | `pathChanged`. |
| `connect` | config identity (opaque), `now` | `LiveConnection.connect`. |
| `renew_credential` | `old`, `new` (opaque token identities), `now` | `LiveConnection.renewCredential`: swaps the token on the same config, keeps the cursor (same hub), reopens immediately unless idle (`LiveConnection.swift:548-558`). |
| `frame` | `generation`, `meta: FrameMeta` (mandatory), `event: Option<ServerEvent>` (optional decoded event, by reference to a `wire` case or inline) | One delivered socket frame. The **first** successful frame is processed atomically in one main-actor callback: `note(meta)` updates the cursor, the connection becomes connected and `onConnected` → `didConnect` builds the catch-up parts and re-subscribes, then that same frame's `onEvent` runs (`LiveConnection.swift:740-758`). There is no separate host `connected` input: connection is a *consequence* of the first frame, so no frame can be interleaved before it. Every frame carries `generation`; a stale one is dropped before any cursor update, `onConnected` or `onEvent`. |
| `pong` | `generation`, `sent`, `rtt`, `now` | A ping's pong, identified by the ping's `{generation, sent}` send instant: refreshes `lastHeard` / `lastPong` / `rtt` and sets `stalled = rtt > slowAfter` (`LiveConnection.swift:688-704`). Generation-fenced; a late pong clears the stall by its own RTT, and a later ping's timeout does not re-set it because the timeout fires only while `lastPong < sent`. |
| `socket_failed` | `generation`, `close_code`, `http_status`, `now` | `receive` returning a failure (401, Cloudflare, drop, goingAway). A stale `generation` is ignored, which is how a cancelled socket's late failure does nothing (`LiveConnection.swift:890`). |
| `session_list` | `generation`, per-chat `{rev, updated_at, now}` | A `sessions` / `sessions_delta` snapshot. Updates each session's rev; records a rev *move* only when the previously heard rev exists and differs (`AgentSync.swift:70-74`), and supplies `updatedAt` — the inputs `AgentSyncPacing.catchUpAt` needs (`rev.movedAt`, `updatedAt`). Arrives as a frame payload, so it is generation-fenced like `frame`. |
| `revs` | `generation`, per-chat `{rev, now}` | The light `revs` event (`AgentSync.revs`), which moves the rev under the same changed-rev rule, without an `updatedAt`. |
| `http_result` | `request_id`, `outcome`: page (reuse a `wire`/`continuity` payload) or `error {status, code, offline, timeout}` | An HTTP answer. |
| `tick` | `now`, `timer: TimerId`, optional `generation` for a generation-scoped timer | A timer firing. `TimerId` identifies the exact timer — its kind (heartbeat, grace, reconnect, `ping_timeout`, stall, catch_up_limit, plan) plus its scope (the catch-up round id for the barrier, the chat key for a per-chat `lookAgain`, the ping's `{generation, sent}` for `ping_timeout`) — so several timers of one kind stay distinct, an earliest-wins `lookAgain` replaces a later one (`AgentSync.swift:206-227`), a stale catch-up round cannot finish a newer one (`ConnectionStatus.swift:200-212`), and a `ping_timeout` / reconnect timer carrying a stale `generation` is rejected (`LiveConnection.swift:703`, `:888-891`). |
| `subscribe` / `unsubscribe` | chat key, owner id | User opened / closed a chat. |
| `load_older` | chat key | User scrolled up. |
| `open_fold` | chat key, fold id | User opened a fold. |
| `continue_fill` | chat key | A gap heard while a fill is in flight (the existing continuity idiom). |
| `hub_changed` | group | `Action::HubChanged` semantics: settle as `.fill`. |

Action kinds (what the host must do; the fixture asserts them exactly):

| Kind | Parameters | Maps to |
| --- | --- | --- |
| `open_socket` | config identity, `query` (`features`, `event_stream`, `event_seq`), `generation` | `LiveConnection.openSocket`. `generation` is the connection-attempt token (`LiveConnection.swift:512`): it increments per `open()`, and any later socket result carrying a stale `generation` is ignored. Distinct from `retryAttempt`, the retry ordinal within a drop phase (`Reconnect.swift:27-34`). |
| `close_socket` | `code` (goingAway), reason, `generation` | `disconnect` / heartbeat kill. Generation-fenced like the heartbeat that issues it (`LiveConnection.swift:670-681`). |
| `send_ws` | the subscribe / unsubscribe / client_hello message | `LiveConnection.send`. |
| `fetch_latest` / `fetch_after` / `fetch_older` / `fetch_range` | the existing `continuity` parameters | `APIClient.items` / `foldedWork`. |
| `schedule` | `timer: TimerId` (kind + scope: heartbeat, grace, reconnect, `ping_timeout`, stall, catch_up_limit, plan; scoped by catch-up round id, chat key or ping `{generation, sent}` as above), `at` (offset ms), `generation` where the timer is generation-scoped | `Task.sleep` + fire. |
| `cancel_timer` | `timer: TimerId` | Cancellation. |
| `caught_up` | group | `ConnectionStatus.onCaughtUp` (outbox flush). |
| `show_phase` | `online / waiting / connecting / updating / rejected` | `ConnectionStatus.shown`. |
| `ping` | `generation`, `sent` | `sendPing`, identified by its `{generation, sent}` send instant; its pong and `ping_timeout` callbacks are generation-fenced (`LiveConnection.swift:688-704`). |

### 3.3 New fixture files and what each pins

| File | Rules pinned (each with `source` from section 2) |
| --- | --- |
| `reconnect_backoff.json` | Ordinary backoff 1→20 s; drop fast retry then backoff; restart fast phase (150–400 ms, 0.5–1 s for 30 s) and its jitter bounds (assert delay ranges, using an injected `rand`); 401 floor 60 s; `reconnectNow` resets backoff and replaces a hanging attempt but leaves a working socket; grace 8 s / 30 s and `graceOver`. |
| `event_cursor.json` | `afterHello` on same/other/absent stream, resumed vs full; `afterEvent` monotonic; query items present only with a stream; cursor reset on `connect` to another config, kept on `renewCredential` (the `renew_credential` event). |
| `heartbeat_stall.json` | Heartbeat ping every 20 s, every 5 s while stalled; `pong` (keyed by the ping's `{generation, sent}`) refreshes `lastHeard` / `lastPong` / `rtt` and sets `stalled = rtt > 5 s`; a `ping_timeout` firing 5 s after a ping with no newer pong (`lastPong < sent`) sets `stalled`, and a late pong clears it by its own RTT without a later ping's timeout re-setting it; 45 s with nothing heard closes the socket (`closedQuiet`) and schedules the reconnect; ping/pong/`ping_timeout` are generation-fenced. |
| `lifecycle.json` | Background closes and nothing reopens except `wake`; foreground resets backoff and reopens unless connecting; `ensureConnected` vs `reconnectNow` vs `probe` on path changes; `reconnectNow` also reopens a stalled socket but leaves a connected non-stalled one; a silent-push wake connects, and each push schedules its own uncancelled 25 s re-suspend (so an earlier push's deadline can re-suspend sooner after a later wake — not a sliding timer); behavior of `ensureConnected` / `reconnectNow` while `suspended` (no-op until `wake`). |
| `catch_up_barrier.json` | Parts built from subscribed chats + interactions on a non-resumed reconnect, none on resumed; backlog completes a chat part; error frame completes it; the interactions part completes on the attempt (success or failure, via `defer`); 8 s limit; stale `catchUpIDs` cannot finish a newer round; `onCaughtUp` fires once per finishing round or timed-out group (possibly several flushes per reconnect), and immediately for an empty parts list. |
| `gap_to_request.json` | `Gap.fill` → immediate `fetch_after(covered)`; in-flight collapse (`filling`); 8-page loop re-reading `covered`; still-behind after the eighth page → `fetch_latest` (no ninth `after_seq`); `Gap.reload` → `fetch_latest` + `replace`; post-reconnect per-chat `fill` when `covered > 0` with the `shouldApply` fence. (These compose `continuity`'s per-chat reducer with the driver; `continuity` already pins the gap itself.) |
| `stall_catchup.json` | `catchUpAt` gating (rev unread, `updatedAt` vs newest line + slack, `quiet = max(lastArrival, rev.movedAt) + staleQuiet` 4 s, pull-every 10 s via `lastCatchUp`, not twice for one `updatedAt`); `RevReads` moved/subscribed/answered; skipped while socket down or chat not subscribed. Inputs arrive as `session_list` / `revs` events carrying rev, `updated_at` and movement time, and `frame`/`http_result` updating `lastArrival`. |
| `background_prefetch.json` | 24 h / 40 / 3 concurrency; `prefetchAllowed` (neither retained nor subscribed); `heldUntil` 60 s while working; failure retry 30→300 s; hub-refused waits for rev change; 300 ms plan coalescing. |
| `paging_folds.json` | `loadOlder` gates and `prepend` semantics at the driver level; `unfold` dedup per (chat, fold), paged `from_seq` continuation, single replacement. |
| `live_text.json` | Delta pacer 0.2 s draw/later/kept/due; drop on subscribe/unsubscribe/reconnect-refetch; backlog clears a non-agent live text; 5 s clear fallback. (Driver-level only; the text matching itself is `ChatStore`/`LiveText`, out of scope.) |
| `phases.json` | `ConnectionStatus.phase` single- and multi-link; `refined` rejected; 1500 ms show delay, zero after grace; `looksConnected` / `takesWrites` through grace. |

Every expectation is **written from the Swift source**, never generated from an implementation (migration rule 1).
Where iOS uses randomness (jitter), the fixture injects `rand` and asserts the exact delay the formula yields, plus
the min/max bounds over `rand ∈ [0,1)`.

### 3.4 Clock and network injection

- **No system clock in the core.** Every event that depends on time carries `now` (ms offset). `tick` advances the
  driver's own timers; the fixture never sleeps. This matches the existing `continuity` rule ("no test waits on a
  clock") and `spec/fixtures/sync`'s virtual-time runners.
- **No real network.** `open_socket`, `fetch_*`, `send_ws`, `ping` are *effects*; the fixture supplies their results
  as `frame` (socket frames, the first of which connects), `socket_failed`, `pong` and `http_result` events. A test
  asserts the effect sequence, then feeds the next result. This is the parent plan's "fixtures replay with no fakes".
- **Randomness is an input.** Jitter uses a `rand` supplied in the event or the case's `initial`.
- **Host capabilities stay native** (decision 2): the real socket, HTTP, and `NWPathMonitor` are the host's; the
  core only decides. `PlatformHost` gains the transport capability in phase 2 exactly as the parent plan's table
  says.

## 4. The first implementable slice: a pure `sync` driver state machine

A minimal, independently implementable slice, with no real socket and no persistence:

**Input**: lifecycle events, decoded frames, and HTTP results for **one hub group and its subscribed chats** (one
or several, so the catch-up barrier and per-chat fill fan-out are exercised).
**Output**: the effect sequence (open/close socket, send subscribe, fetch pages, schedule/cancel timers, phase
changes, caught-up) and the driver state.

It deliberately excludes: multi-group routing/mesh (`HubRoute`, `watchCenter`), the outbox/op-retry write path,
`AgentSync`'s background prefetch of *non-retained* chats (that is a cache warmer, phase 4 adjacent), and storage.

### 4.1 Rust API sketch

```rust
// persona-core/src/sync.rs — pure: no IO, no clock, no unsafe.

pub struct SyncDriver {
    // per-group socket state, cursor, backoff, drop/grace, `generation` (connection-attempt token)
    // per-chat: subscribed owners, covered mirror, filling, unfolding, stall tracking
    //   (RevReads, lastCatchUp, caughtUpFor, lastArrival)
    // catch-up barrier, delta pacer, pending timers
}

pub enum SyncInput {
    Foreground { now: Ms },
    Background { now: Ms },
    WakePush { now: Ms },
    NetworkPath { up: bool, now: Ms },
    Connect { config: ConfigId, now: Ms },
    RenewCredential { old: TokenId, new: TokenId, now: Ms },  // same hub: keep cursor, reopen unless idle
    /// One delivered socket frame. The first successful frame drives connection + onConnected
    /// (catch-up parts, re-subscribe) and then its own onEvent, atomically. `generation` fences
    /// stale frames before any cursor update, onConnected or onEvent.
    Frame { generation: u64, meta: FrameMeta, event: Option<ServerEvent>, now: Ms },
    Pong { generation: u64, sent: Ms, rtt: Ms, now: Ms },   // keyed by the ping's {generation, sent}; sets stalled
    SocketFailed { generation: u64, close: CloseInfo, now: Ms },
    SessionList { generation: u64, rows: Vec<SessionRev>, now: Ms },  // rev + updated_at; move only on changed rev
    Revs { generation: u64, rows: Vec<SessionRev>, now: Ms },          // light revs event
    HttpResult { id: RequestId, outcome: HttpOutcome, now: Ms },
    Tick { now: Ms, timer: TimerId, generation: Option<u64> },  // TimerId = kind + scope (round id / chat key / ping {generation, sent})
    Subscribe { chat: ChatKey, owner: OwnerId, now: Ms },
    Unsubscribe { chat: ChatKey, owner: OwnerId, now: Ms },
    LoadOlder { chat: ChatKey, now: Ms },
    OpenFold { chat: ChatKey, fold: FoldId, now: Ms },
    ContinueFill { chat: ChatKey, now: Ms },
    HubChanged { now: Ms },
}

pub enum SyncEffect {
    OpenSocket { config: ConfigId, query: WsQuery, generation: u64 },
    CloseSocket { code: CloseCode, generation: u64 },
    SendWs(WsMessage),                       // subscribe / unsubscribe / client_hello
    Fetch(FetchRequest),                     // reuses continuity's FetchAfter/Latest/Older/Range
    Schedule { timer: TimerId, at: Ms, generation: Option<u64> },
    CancelTimer { timer: TimerId },
    CaughtUp,
    ShowPhase(Phase),
    Ping { generation: u64, sent: Ms },
}

impl SyncDriver {
    pub fn new(config: SyncConfig) -> Self;
    /// Applies one input; returns the effects to perform, in order.
    /// `continuity` is called internally for chat batches; its `Gap` drives fill/reload here.
    /// Socket-originated inputs and generation-scoped timers carrying a stale `generation`
    /// (the connection-attempt token, distinct from the drop-phase `retryAttempt` ordinal)
    /// are ignored, so a cancelled socket's late frame/fail/timer does nothing.
    pub fn step(&mut self, input: SyncInput) -> Vec<SyncEffect>;
    pub fn state(&self) -> &SyncState;      // for fixtures' full-state comparison
}
```

`Ms` is a monotonic millisecond counter supplied by the caller, never read from a clock. `SyncConfig` carries the
constants from section 2.5 (so a fixture can shrink them, and so the values are reviewed in one place).

### 4.2 Effects and the host boundary

- The host performs `OpenSocket` / `Fetch` / `SendWs` / `Ping` and reports results back as `Frame` (socket frames,
  the first of which connects), `SocketFailed`, `Pong` and `HttpResult`. This is the parent plan's `PlatformHost`
  transport capability.
- `Schedule` / `CancelTimer` map onto the runtime's worker timers (the parent plan allows timers when a domain
  needs them; `sync` is that domain — grace, backoff, heartbeat, stall, catch-up limit). The fixture drives them
  with `Tick`.
- `ShowPhase` and `CaughtUp` go to the client's `ConnectionStatus` and outbox respectively; the words come from
  each client's localization (the core returns the `Phase` enum only).
- `continuity` stays the per-chat reducer. `SyncDriver` owns the *driver* concerns `continuity` does not have:
  the socket, the cursor, timers, the barrier, pacing, and the "which request now" decision. The `fill` loop,
  `filling` collapse, `unfold` dedup and `shouldApply` fence move here from `AppModel`.

### 4.3 Boundaries (what this slice does NOT do)

- No bytes: `transport` already decodes the `items`-continuity payloads; `SyncInput::Frame` carries decoded events.
  **Boundary caveat**: today's `persona_core::transport` exposes only `Frame::Items` / `Frame::Other`
  (`native/app-core/crates/persona-core/src/transport/mod.rs:242`), so it does not yet yield the decoded
  `hello` metadata, `sessions` / `revs` snapshots, or `error` frames this driver consumes. Those decoded values
  initially come from the native adapter (or a later transport expansion); they are *not* current `transport`
  outputs. The slice treats them as typed inputs and does not re-decode bytes. (A later composition wires
  `Action::WirePayload` → `transport` → `SyncInput::Frame` for the item path.)
- No multi-group: one group. Multi-group is a composition of independent drivers plus a phase aggregator
  (`ConnectionStatus.phase(network:links:catchingUp:)`, `ConnectionStatus.swift:137`).
- No persistence: `ChatCache`, the `AgentSync` rev ledger and the launch snapshot are phase 4.
- No write path: sends and op-retry are a separate domain; `sync` only emits `CaughtUp` for the outbox.
- No UI text, no pixels, no haptics (the parent plan's boundary rules).

### 4.4 Why this slice first

It is pure, deterministic, and covers the highest-drift behavior: every client reimplements reconnect/backoff,
catch-up and lifecycle resume today. It reuses `continuity` unchanged for the per-chat item reduction and leaves
`transport` as-is (the non-item decoded inputs come from native adapters — see the boundary caveat in 4.3). It
needs no new host capability beyond the phase-2 transport the plan already schedules, and its fixtures
(section 3.3) are written directly from the iOS sources cited in section 2.

## 5. Android and HarmonyOS differences (recorded, not resolved)

| Area | iOS (reference) | Android | HarmonyOS |
| --- | --- | --- | --- |
| Gap fill timing | Immediate `fill` on `Gap.fill` (`AppModel.swift:2072-2078`). | Immediate: `Action.FetchAfter` dispatches "no settling delay on a real gap" (`HubChatRepository.kt:767-774`). The `spec/fixtures/sync/README.md` "Allowed differences" line still says Android waits ~1.5 s — that text is stale relative to current production. | Immediate, `fillNow` (`harmony/entry/src/main/ets/models/TranscriptModel.ets:137`). |
| Fill loop cap | 8 pages then latest (`AppModel.swift:2089-2099`). | `catchUp` / `catchUpAsync` driven by continuity actions (`android/core/data/.../HubChatRepository.kt:894-955`). | Loops `after_seq` while `behind`, with an eight-page fallback to `fetch_latest` in `Domain.ts` (`harmony/entry/src/main/ets/core/Domain.ts:555-568`; executed at `TranscriptModel.ets:95-114`). |
| Reconnect/backoff | `LinkDrop.retryDelay` + grace (`Reconnect.swift:31`). | Own reconnect logic in the connection layer (not surveyed line-by-line here). | `sync.reconnect()` (`TranscriptModel.ets:50`). |
| Stalled on-screen catch-up | `AgentSyncPacing.catchUpAt` with rev reads (`AgentSyncPacing.swift:125`). | Web shares the rule (`web/src/lib/stale-page.ts`); Android's equivalent is in its chat repository/session layer. | Not present in `TranscriptModel`; relies on reconnect + fill. |
| Background prefetch of recent chats | `AgentSync` (24 h / 40 / 3, rev ledger): warms the disk cache of recent chats not retained in memory. | **No equivalent message-body prefetch.** `HubChatRepository` syncs bodies only for observed, lingering or awaited chats; "closed chats need no message-body prefetch" (`HubChatRepository.kt:18-22`, `:600`). Paging its session list is metadata sync, not iOS's cache warming. | None equivalent in `TranscriptModel`. |
| Subscription retention after close | `subscribe`/`unsubscribe` by owner; chat kept in memory (LRU 8). | `releaseAfterMillis` lingering subscription (`HubChatRepository.kt:20`, `:36`, `:356`). | Unsubscribes on close (`TranscriptModel.ets:159`). |
| Rust continuity adoption | Native `ChatStore` is the reference; adapter + shadow in #8970. | `RustContinuityAdapter` exists behind `-PpersonaContinuity=rust` (#7448). | Host replays only; no production adapter. |

These differences are inputs to the fixture design, not decisions to change any client: decision 7 says behavior
follows iOS, and a genuine cross-client behavioral difference is filed separately, not silently harmonized here.

## 6. Open questions for the maintainer

1. **Constants as configuration vs fixed.** Section 2.5's timings are currently Swift literals. The slice puts them
   in `SyncConfig` so fixtures can shrink them and review is centralized. Confirm they stay *behavioral constants*
   (same values in production on every client), not user-tunable settings (which would be a product change).
2. **Jitter determinism.** `LinkDrop.retryDelay` uses `Double.random`. The design injects `rand` per event. Confirm
   the production core should take a seeded RNG in `SyncConfig` (deterministic per launch) rather than reading
   system randomness, so replays are reproducible.
3. **Background prefetch placement.** `AgentSync`'s warmer is closely tied to `ChatCache` (phase 4). Keep it out of
   the first `sync` slice (proposed), or include its scheduling now and its cache writes in phase 4?
4. **`OpRetry` write path.** Confirm sends/op-retry stay a separate later domain, with `sync` owning only the
   `CaughtUp` signal the outbox listens to.

## 7. What the next agent can implement directly

1. Extend the first three topics in `spec/fixtures/sync-driver/` with the remaining section 3.3 cases. Write the
   expectations from the cited Swift sources and add each case to the replay and negative checks.
2. Extend `persona_core::sync` at the per-chat request boundary, reusing `persona_core::continuity` for chat
   batches. The existing driver hands the host the full-refresh or resumed-replay decision.
3. Wire `Action::WirePayload` → `transport` → `SyncInput::Frame` in the runtime, and surface `SyncEffect`s through
   the existing effect channel; regenerate codecs (a wire-type change ships its regenerated codecs).
4. Only then: the iOS adapter behind a protocol with the native path as shadow reference (migration rules 3–4),
   reusing the #8970 continuity-adapter seam, and the existing `spec/fixtures/sync` stories as the end-to-end
   acceptance (already replayed by `SyncScenarioTests`).

The first three topics completed under #9783 use behavior constants and injected jitter. Production random-source
selection, per-chat scheduling and runtime integration remain open. Physical-device cells are **blocked: no physical device**.
