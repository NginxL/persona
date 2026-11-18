# Plan: search Agent sessions by content across computers (#757, step 2)

Status: proposal for review, no code yet. Step 1 of #757 (a `mnemo` extension that gives the assistant mnemo's MCP
tools) is separate work in `persona-extensions`. Once the steps below are built, their parts move into
`docs/protocol/protocol.md`, `docs/web/web.md` and the client docs, and this plan is deleted.

## Background

"How did we fix this last time?" is a question about an Agent's own history: what was said, which command failed,
what the fix was. One user runs more than 160 Agent sessions a week across three computers. The answer is usually in
one of those sessions, but nothing in Persona finds it by content, so today the user opens computers one by one and
scrolls from memory.

The issue asks for the App side as step 2: each computer searches its own sessions, the results come back merged,
the session text stays on its computer, snippets are redacted and short, and computers that did not answer are named.
It also rules out writing a second indexer inside Persona. This plan answers how, against the code as it is.

## What Persona has today

### The search entries and where they stop

| Entry | What it matches | Where it runs | Why it cannot answer the question |
| --- | --- | --- | --- |
| `GET /api/v1/sessions?search=` (#7624) | Title, project, computer, launcher, model, state, latest text (`sessionsearch.Document` in `server/internal/sessionsearch/document.go`; operators `machine:`, `project:`, `harness:`, `since:` in `parseSearch`, `server/internal/sessionlist/search.go`) | The hub's in-memory list index (`runtimeSearchContext`, `server/internal/hub/sessions_search.go`) | Never opens a transcript (`SearchDocument` is "not a transcript"), so an earlier message is never matched |
| Search in chat (#2977) | Every loaded item of one open chat (`web/src/lib/chat-search.ts`, Android `ChatSearchScreen.kt`) | The client | One session the user already found |
| `GET /api/v1/assistant/search` (#6282) | The user's words and the replies in assistant chats (`server/internal/assistant/chatsearch/text.go`, `server/internal/db/assistant_search.go`) | The manager's Postgres, filled through `session.items` | Assistant chats only: its design (`docs/design/proposals/assistant-search.md`) leaves Agents' own history to #757, and it is an app endpoint, not a model tool |
| `kb_search` | Knowledge-base files | The manager (`server/internal/kb/`) | Agent transcripts are not knowledge-base files |
| `agent_read` | The last turns of one named session (`server/internal/prompts/mcp/agents/agent_read.md`) | The manager, through the node | Needs the session id first |

### How a node reads sessions

- **Claude Code**: `<config dir>/projects/<cwd>/<session>.jsonl`, in every config directory the node knows
  (`claudeConfigDirs`, `projectDirs` in `server/internal/node/claudedirs.go`: the default, launcher profiles, running
  claudes' `CLAUDE_CONFIG_DIR`, the hub's list), minus blocked directories and folders (`claudeDirBlocked`,
  `cwdBlocked`).
- **Codex and compatible CLIs**: `<home>/sessions/YYYY/MM/DD/rollout-*.jsonl` and `<home>/archived_sessions/`
  (`codexPath` in `server/internal/node/codex.go`).
- **Codex App**: the App deletes rollouts once a chat's history moves into its SQLite database (#3305). The node reads
  that database with the system `sqlite3`, read-only (`codexAppThreads`, `server/internal/node/codex_appdb.go`), and
  writes the history back as rollout lines in its own state directory (`codexAppHistoryPath`,
  `server/internal/node/codex_apphistory.go`), so one reader serves both.
- **Other programs** answer `session.items` through their own APIs (`server/internal/node/opencode.go`,
  `server/internal/node/ompcalls.go`, `server/internal/node/daemons.go`). This plan does not cover them in its first
  version (see "Coverage" below).
- **One parser**: `transcript.File` (`server/internal/transcript/file.go`) reads a transcript incrementally, line by
  line. An item's `seq` is its line number (`ParseLine(seq, line, meta)` in `server/internal/transcript/transcript.go`),
  and `History` changes when a file is replaced or rewritten. `n.file(id)` (`server/internal/node/transcript_files.go`)
  finds a transcript by id in the directories the node reads, not only among listed sessions, so an old session opens
  by `(machine, session_id)`.
- **The list is a window**: offline Claude transcripts are listed for 7 days (`offlineWindow`,
  `server/internal/node/sessions_offline.go`), Codex rollouts for 14 (`codexWindow`). Content search has to reach
  further back than the list does.
- **Incremental signals that exist**: `transcript.File.Changed` and `RefreshSpan`, `sessions.revs` (`revsChanged` in
  `server/internal/node/sessions.go`), `session.watch`. A scan with no index needs none of them; mnemo keeps its own
  index current by syncing before each search (below).

### How a call reaches every computer

- Node connections belong to the manager that connects the computers (`server/internal/devices/`,
  `docs/server/machines.md`). The hub's `Machine.call` (`server/internal/hub/machine.go`) goes through `busNode.Call`
  (`server/internal/hub/devices_calls.go`) to the owning manager's `Devices.Call` (`server/internal/devices/devices.go`),
  which passes node methods through (a few owner controls aside).
- "Ask every online computer" exists once: subscription usage windows. `usageWindowsHub.poll`
  (`server/internal/devices/usagewindows.go`) asks each online source in parallel with its own deadline
  (`usageHubAsk`, 8 s), ignores a failed or older node and merges the rest. The hub only relays the route
  (`usageRoutesRelayed`, `server/internal/hub/devices_routes.go`).
- The hub answers only its own routes (`docs/server/architecture.md` ("The hub is only a communication center"),
  `server/internal/hub/testdata/hub_routes_allowed.txt`, guarded by `server/internal/hub/architecture_guard_test.go`).
  A search route is business logic, so it belongs to the manager and the hub relays it.
- With several managers, `mergeList` (`server/internal/hub/ownership_lists.go`) asks each one and merges lists, one
  after the other; `pagedLists` leaves the merging to the apps.
- `Machine.reachable` tells whether a call would go out at all; `Machine.hasCapability` tells an older node; failed
  and slow calls are logged with method, result and duration (`server/internal/devices/calllog.go`).

### Redaction that exists

- `proto.RedactArgs` and `proto.SecretKey` (`server/internal/proto/secrets.go`, #4734): the value of every key named
  like a credential, at any depth of a tool's JSON input, becomes `***`.
- `transcript.SanitizeShareText` (`server/internal/transcript/share_sanitize.go`, session sharing): private-key blocks,
  `Bearer` values, known token shapes (`sk-…`, `ghp_…`, `xox?-…`, `AIza…` and more), `NAME=value` assignments whose
  name says secret, credentials inside URLs, `curl -u`. It runs over the whole text before any clipping.
- `chatsearch` (`server/internal/assistant/chatsearch/text.go`): what of an item is searchable (`IndexText`), folding
  (`Fold`: NFKC and lower case), query words (`Words`), matching (`Holds`) and snippets (`Snippet`: about 40 characters
  before the match and 80 after, UTF-16 ranges). The node cannot link it where it is: `cmd/persona-node`'s guard
  refuses anything under `internal/assistant` (`server/cmd/persona-node/main_test.go`).

## Design

```text
App ──GET /api/v1/sessions/content-search──► hub (relays) ──► manager (devices)
                                                                 │ parallel, one deadline each
                                     ┌───────────────────────────┼───────────────────────────┐
                                     ▼                           ▼                           ▼
                              node A: session.search      node B: session.search      node C: offline
                              mnemo, else bounded scan    bounded scan                (no call)
                              redacted snippets only      redacted snippets only
                                     └──────────────► manager merges, ranks, names C ◄─────────┘
```

### 1. On each computer: the node method `session.search`

A new node method, announced as `session_search_v1` in `server/internal/nodeproto/nodeproto.go`.

**Request**: `{query, filters: {project?, harness?, since?}, include_tools?, limit, budget_ms}`. The node folds the
query with `chatsearch.Words`; every word must be in the same item, as substrings after NFKC and lower case, so case,
width and Chinese word boundaries do not matter (the rule `assistant_search` already uses).

**What is searched**: by default the user's words and the Agent's replies as the chat shows them
(`chatsearch.IndexText`). With `include_tools`, also tool names, tool input (after `proto.RedactArgs`) and tool output.
Never thinking, injected prompts or images.

**Which sessions**: Agent sessions only, the ones the node would list as the user's, by one predicate shared with the
listing so the two cannot drift (PR 2 lifts it out of `server/internal/node/sessions_offline.go` and the Codex
registry). Left out, by the rules the listing already uses:

- assistant conversations, the health space and meeting rooms: `isAssistantProject` (the assistant's projects
  directory, its `-health` sibling, meeting-room directories, the former state directory's names), and Codex threads
  whose start directory is the assistant's (`identity.IsAssistantDir`, `server/internal/node/codex_registry.go`).
  `assistant_search` keeps those, so nothing is found twice;
- other platforms' runs (`agentFiltered`), one-shot `claude -p` runs (`oneShotRun`) and launcher health checks
  (`isLauncherCheckRun`);
- directories outside the node's session scope (`sessionScopeAllows`) and blocked ones (`claudeDirBlocked`,
  `cwdBlocked`).

The list's time window (`offlineWindow`, `codexWindow`) and its small-file rule do not apply: content search reaches
further back than the list. mnemo's hits go through the same predicate (section 2).

**What a hit is**: an item `session.items` returns. The raw bytes of a line are only a prefilter; a file with a line
that may match is read through `transcript.File`, whose branch tree leaves out lines on a branch abandoned by a rewind
or an edit and whose `History` notices a rewritten file, and the words are matched on the visible items it yields. So
an abandoned message never gives a hit, and every `item_id` and `seq` returned opens in `session.items`.

**Whole items.** `transcript.File` keeps a page-sized copy of a long item (`pageTextLen`, 24 KiB; `File.Full` reads
the line again for the whole). A candidate item that came cut is read whole with `File.Full` before it is matched,
redacted or clipped, so a word past the cut is found and a credential that crosses it is redacted whole; if `Full`
fails (the line changed since the page was read), the item gives no hit.

**A continued Codex thread is one session.** A thread continued into a new rollout keeps its older segments as
separate files (`rolloutSegments`, `server/internal/node/codex.go`; the list follows the newest). The scan takes a
thread's segments together, newest first, and the thread counts toward `limit` only once every segment has been
searched; one the budget cut short is returned with what was found and `partial: true` on its row. `session.items`
opens a thread at its newest file, so a hit in an older segment cannot land on its item today: it is returned with
its snippet and `"segment": "older"`, without `item_id` or `seq`, and opens the session at its start (a decision
below).

**Answer** (at most `limit` sessions, 3 hits each, 64 KiB in all):

```json
{"backend": "mnemo", "partial": false, "searched_back_to": null, "more": true, "uncovered": {"codex_app": 0},
 "sessions": [{"session_id": "…", "harness": "claude", "cwd": "…", "started_at": "…", "updated_at": "…",
               "hits": [{"item_id": "…#2", "seq": 412, "role": "assistant", "ts": "…",
                         "snippet": "…the retry budget comes from RELAY_RETRY_BUDGET, so the fix was…",
                         "ranges": [[24, 36]]}],
               "more_hits": 4}]}
```

A hit's `seq` and `item_id` are the ones `session.items` uses, so a client lands on the item. Transcript paths are
never in the answer.

**Coverage**: the Agent sessions above in Claude transcripts in every non-blocked config directory, Codex rollouts
(live and archived) in every Codex home the node reads, and Codex App chats.

Codex App chats need a step first. The node's history file for a chat (`codexAppHistoryPath`) is written or brought
up to date only when the chat is opened or watched (`refreshCodexAppHistory`,
`server/internal/node/codex_apphistory.go`), so a chat never opened in Persona has none, and an old one may lack the
latest turns. Before matching, the node lists
the eligible App threads from the App's database (`codexAppThreads`), newest first, and brings each history up to date
with a form of `refreshCodexAppHistory` that takes the search's context and deadline (PR 2 adds it; today's callers
keep their behavior). A thread the budget did not reach is not called searched: the answer counts it in
`uncovered.codex_app` and sets `partial`. The first search on a computer with many never-opened App chats writes their
histories, within the budget; later searches only append what changed.

Sessions served by other programs' APIs are counted in `uncovered` too (by program) so the App can say so; covering
them is later work per program.

### 2. Where the matching runs: three options

| | (a) `mnemo search --json` | (b) A built-in index | (c) A bounded scan |
| --- | --- | --- | --- |
| New code in Persona | An adapter | An indexer, a schema, migrations | A scan over the existing parser |
| Dependency | Optional, user-installed Python CLI | An SQLite library in persona-node | None |
| Speed | 60–100 ms on 236k messages (mnemo's own benchmark) | Similar | Grows with history (below) |
| Disk | mnemo's index and archive, 31% of the logs plus a copy of each log (mnemo's README) | A second plaintext copy | None |
| Ids, landing, blocked folders | Mapped back by the node | Native | Native |
| Codex App chats | Missed when the App deleted the rollout | Native | Native |

**(b) does not fit as things stand.** `server/go.mod` has no SQLite or full-text library, the release builds in the
`Dockerfile` use `CGO_ENABLED=0`, and `cmd/persona-node`'s guard fails if persona-node links `database/sql/driver`,
which the usual pure-Go SQLite packages pull in; a package that avoids it still compiles all of SQLite into
persona-node for five platforms. It is also the second indexer #757 says not to write.

**(c) alone is honest but slow on long histories.** At mnemo's published average (872 sessions, 4.14 GB of logs),
160 sessions a week are about 0.75 GB of logs a week: across three computers, three months are about 3 GB each.
Read from the page cache that fits a budget of a few seconds; read cold it does not. These are estimates from
mnemo's numbers; PR 2 measures them on a synthetic corpus.

**Recommended: (c) everywhere, (a) where mnemo is installed.** Both sit behind `session.search` and give the same
answer, so the App and the manager never know which one ran (`backend` is for the coverage line only).

- **Scan (always there)**: sessions newest first by their newest file's modification time (a continued Codex
  thread with all its segments, section 1). Each line goes through a prefilter that may only rule a line out, and a
  file with a line it did not rule out is read through `transcript.File` and matched on its visible items, whole
  (section 1).
- **The prefilter** rules a line out only when its visible text cannot hold the words. `chatsearch.IndexText` shows a
  reply's Markdown without its markup, so `foo**bar**` matches `foobar` although the raw bytes never hold `foobar`:
  comparing raw bytes would drop real matches. Instead the line and each word are reduced the same way: JSON escapes
  decoded (`\n`, `\"`, `\\`, `\uXXXX`), folded as matching folds (`chatsearch.Fold`, NFKC and lower case), cut down to
  letters, digits and combining marks, and recomposed (NFC). A line is ruled out when some word's reduced form is
  missing from the line's. Removing markup drops only characters that this reduction drops too, except where
  markup takes letters with it; a line holding such markup (a link or image target, `](`; a reference label, `][`;
  an HTML tag or entity, `<` or `&` followed by a letter or `/` or `#`) is never ruled out, and a word that reduces to
  nothing rules nothing out. PR 2 checks the contract with a property test (below), and if the prefilter skips too
  little to keep inside the budget, the answer is a scan that reaches less far back, never a looser prefilter.
- **When the scan stops**: once `limit` sessions have hits (`more: true`), or at `budget_ms` (2.5 s by default), which
  answers `partial: true` with `searched_back_to`, the modification time of the last session it finished. One scan at a time per node; a new search
  from the same caller cancels the old one. No state is kept between searches beyond a small in-memory cache of each
  file's session id and head metadata, keyed by path, size and modification time.
- **mnemo (when present)**: the node looks for `mnemo` the way it looks for other CLIs, checks `mnemo --version`
  against a minimum chosen in PR 3 (the flags below are checked against that version), and runs
  `mnemo search --json --host local --limit <n> [--source] [--cwd] [--since] -- <words>` with the same budget.
  `--host local` keeps mnemo from searching its own SSH remotes, so it never reaches a computer Persona does not know
  and never duplicates another node's hits. For each hit the node:
  - keeps it only if `path` is a transcript it reads itself (not blocked, in a known config directory or Codex home)
    and section 1's predicate keeps its session (not an assistant, health or meeting chat, not another platform's run);
  - maps it to its own session id (a Codex thread outside the legacy home has an opaque `c0de-` id,
    `docs/protocol/protocol.md`) and uses `lineno` as `seq`;
  - finds the visible item at that line through `transcript.File` and applies section 1's rule, redaction and
    snippet to it; the hit is dropped if the line is not a visible item (a branch abandoned by a rewind or an edit)
    or no longer matches (the file changed since mnemo's sync). mnemo's own `snippet` and `body` are never forwarded;
  - drops `archived: true` hits (mnemo's copy of a log the Agent deleted: Persona cannot open them).
  Then the node brings the Codex App histories up to date and scans them (section 1), within what is left of the
  budget, since mnemo cannot see those chats. If mnemo is
  missing, too old, fails or runs out of time, the node scans instead and says so in its answer.
- Persona never runs `mnemo index` or `reindex`, never reads mnemo's configuration and never installs it. mnemo
  syncs its own index before each search (about 0.1 s when nothing changed, per its docs); that is mnemo's behavior on
  a computer where the user installed it.

If scans turn out too slow, the knowledge base's prefilter (`server/internal/kb/search_index_grams.go`: per-file keys
that only ever say "this file cannot match") could be lifted into a shared package. That is an index of a kind, so it
is listed as a decision below, not part of this plan.

### 3. Across computers: the manager fans out, the hub relays

- **The route** `GET /api/v1/sessions/content-search?q=<text>&tools=1&order=relevance|newest&limit=20` is the
  manager's, in a new `devicewire` route table relayed by the hub like `devicewire.UsageRoutes` (to the manager the
  request names, else the default). The guard's list of forwarding tables learns the new table; no line is added to
  `hub_routes_allowed.txt`.
- **The query** takes the operators the Agents search already knows (`machine:`, `project:`, `harness:`, `since:`,
  parsed by `parseSearch`), so one query means the same in both searches. The manager turns them into node filters;
  `machine:` limits which computers are asked.
- **The fan-out** follows `usageWindowsHub.poll`: every computer of the manager that is online and announces
  `session_search_v1` is asked in parallel, each with a 5 s deadline (the node's 2.5 s budget plus the connection),
  the whole request at most 6 s. Unlike usage windows, the calls are cancelled when the client goes away (no
  `context.WithoutCancel`), and nothing is cached: queries are not kept.
- **Every computer gets a row** in the answer, whether it answered or not.


```json
{"results": [{"machine_id": "…", "session_id": "…", "title": "…", "project": "…", "harness": "codex",
              "updated_at": "…", "archived": false, "score": 0.016, "hits": […], "more_hits": 1}],
 "computers": [{"machine_id": "…", "name": "computer-a", "status": "searched", "backend": "mnemo", "took_ms": 180},
               {"machine_id": "…", "name": "computer-b", "status": "partial", "backend": "scan",
                "searched_back_to": "2026-09-01T10:00:00Z"},
               {"machine_id": "…", "name": "computer-c", "status": "offline"}],
 "partial": true, "more": false}
```

A computer's `status` is `searched`, `partial` (the budget ran out), `offline` (not connected: no call is made),
`timeout`, `outdated` (the node lacks `session_search_v1`), `busy` or `error` (with the node's code). `not_covered`
counts ride along on the computer's row.

- **Decoration**: the manager already holds each computer's session list (`docs/server/machines.md`); a hit in a listed
  session takes its title, project and launcher label from there. A session outside the list window gets its title
  from its first user words, redacted and clipped by the node. Agent session archives are the manager's, so archived
  sessions are marked.
- **Several managers**: the hub's relay asks each manager in parallel, each row stamped with its manager as merged
  lists are, and merges the answers rather than concatenating them: all managers' `results` sorted together by
  `score` (`order=relevance`, section 5) or by the newest hit (`order=newest`), ties broken by the newer hit, then
  machine and session id, and `limit` applied once to the merged list. `computers` is every manager's rows (a computer
  belongs to one manager). `partial` is true if any answer's is; `more` is true if any answer's is or the merge cut
  results. `mergeList` asks one after the other, which is fine for to-dos but would add seconds here, so the relay
  needs a parallel variant (a decision below). Most installs have one manager.
- **Capability**: the manager names `session_content_search` in its hello and the hub lists the feature
  `sessions_content_search` while the default manager names it, as `assistant_search` does. Clients without it keep
  today's search.

### 4. Relation to the existing entries

A new route rather than a new mode of `search=`:

- `search=` answers from the hub's memory in milliseconds, freezes a result window and pages it with a cursor;
  content search wakes every computer, takes seconds, and its completeness is per computer. One parameter for both
  would give one response two meanings of "complete".
- `search=`'s strict combination rules and evidence fields stay as they are; content search reuses its operators only.
- Search in chat stays the way to search inside an opened session, and the landing (`?at=`) is shared.
- `assistant_search` keeps assistant chats. Content search covers Agent sessions only, so a chat is never found twice.
- `kb_search` keeps the knowledge base. The clients may show these sources side by side, never merged into one list.

### 5. Ranking, merging and limits

- **On a node**: the scan returns sessions by their newest hit; mnemo returns its own relevance order (BM25). Within
  a session, hits are newest first.
- **In the manager**: the computers' orders are fused with Reciprocal Rank Fusion (k = 60, the usual constant; mnemo
  merges its own devices with RRF too), since a scan's order and BM25 scores cannot be compared directly. A session
  lives on one computer, so its fused score is `1 / (60 + rank)`, with `rank` its 1-based place in its own computer's
  answer: it depends on nothing but that answer, so a manager and the hub's relay compute the same `score` and any
  number of managers' results sort together (section 3). Ties go to the newer hit. `order=newest` sorts by the newest
  hit instead.
- **Limits**: query at most 200 characters; `limit` 20 sessions by default, 50 at most; 3 hits per session with
  `more_hits`; each node asked for `limit` sessions; snippets about 40 characters before the match and 80 after.
- **Paging**: no cursor. A cursor would make every computer keep state for a query. "More" asks again with a larger
  `limit` (up to 50); beyond that the App suggests narrowing with an operator. `more: true` says some computer had
  more.

### 6. Privacy and redaction

- **What never leaves the computer**: transcript files, whole items, tool output beyond a snippet, transcript paths,
  mnemo's index, archive and snippets. The answer carries ids, times, the session's working directory (already in the
  session list) and redacted snippets.
- **How a snippet is made**, on the node: the whole item text first (read with `File.Full` when the page copy was cut,
  section 1; an item that cannot be read whole gives no hit), tool input through `proto.RedactArgs`, then
  `transcript.SanitizeShareText` over the whole text, and only then `chatsearch.Snippet` cuts the window. Redacting
  before cutting means a credential cut in half at the edge of a snippet is never shown. A hit whose matched words fall
  inside a redacted span is dropped: the search should not answer "this token is in session X".
- **What is searched by default**: messages only (section 1). Tool output is opt-in per search, because it is where
  environment dumps and file contents end up; even then only the redacted snippet leaves.
- **Blocked folders**: directories and folders the user blocked for a computer are never read, and mnemo hits inside
  them are dropped.
- **Logs**: never the query or a snippet. The call log already records method, result and duration only.
- **Nothing new on disk**, apart from what mnemo itself keeps where the user installed it.

### 7. Who may search

- Full-credential devices only: the route is `-` in `server/internal/hub/testdata/route_scopes.txt`, as
  `GET /api/v1/assistant/search` is. Not extension tokens (`agents.read`) and not answer-only devices.
- The manager asks only its own computers, so a manager never searches another manager's.
- Work computers (`machines[].trust: "work"`, `docs/operations/config.md`) keep personal data off them. The owner's
  apps search every computer, as they already show every session. A search made by the assistant from a work computer
  (`isWorkMachine` in `server/internal/assistant/machine_privacy.go`) covers work computers only.
- Agents do not get the tool in this plan; guest conversations get only their own tools, as today.

### 8. The apps

- **Web**: ⌘K "Search chats" already shows runtime hits (`web/src/lib/agent-search.ts`, `docs/web/web.md`). It gains a
  last row, "Search Agent conversations for “…”". Content search starts only from that row (or ⌥Enter), not on every
  key, because it wakes every computer. The results view lists sessions with title, computer, launcher, project, time
  and snippets with matches marked, and a coverage line above them: "Searched 2 of 3 computers · computer-c is
  offline", with "searched back to <date>" for a partial computer. A result opens the Agent's chat at the hit
  (`/s/<machine>/<session>?at=<item>`: the `?at=` landing `assistant_search` built for assistant chats, extended to
  Agent chats); a session outside the list opens by id.
  A stopped session offers Continue: `POST /api/v1/sessions` with `resume_session_id` in its folder
  (`docs/protocol/protocol.md`: `claude --resume`, `codex resume`).
- **iOS**: the Agents page's search field (`docs/apple/ios.md`) gets the same row at the end of its matches, a results
  page with the coverage line, and the `?at=` landing it has for assistant chats, extended to Agent chats.
- **Android**: the Agents page's search box (`docs/android/android.md`), the same row, page and landing.
- New strings in `en`, `zh-Hans` and `ja`; screenshots in dark and light before merging.

### 9. The assistant's tool

- **`agent_history_search {query, machine?, project?, since?, include_tools?, limit?}`** (at most 10 sessions), a
  definition beside the other Agent tools in `server/internal/prompts/mcp/agents/`. It is deferred (not in `coreTools`,
  `server/internal/assistant/tool_loading.go`) and given to assistant conversations only.
- The manager runs the same fan-out in process, over its own computers. The model gets per session: computer, short
  id, title, folder, last active time, launcher, one or two snippets, and the coverage line ("not searched:
  computer-c, offline"), so it does not claim "never fixed" while a computer was offline.
- To read around a hit, `agent_read` gains `around_seq`: the exchange that holds the hit, for sessions outside
  `agents_list` too. The answer then cites the session, which the user can open or continue.
- With step 1's mnemo extension installed, the model also has mnemo's tools for the computer the assistant runs on.
  The tool descriptions say which to use: `agent_history_search` for "which computer, which session", mnemo's
  `get_context` for raw context on the assistant's own computer.

### 10. Pull requests, each verifiable on its own

1. **Move `chatsearch` to a package the node may link** (out of `internal/assistant`), no behavior change. Check: its
   tests and the `assistant_search` tests pass unchanged; `cmd/persona-node`'s guard passes once PR 2 imports it.
2. **Node scan and `session.search`** with `session_search_v1` and its row in `docs/protocol/protocol.md`. Tests on a
   temporary home with Claude, Codex and Codex App history fixtures: Chinese, width and case; words in different
   items do not match; blocked directories; assistant, health-space and meeting-room chats and an assistant-started
   Codex thread give no hits or snippets while an ordinary session beside them does; rewind and edit fixtures where an
   abandoned message gives no hit and every returned `item_id` opens through `session.items`; the prefilter's property
   test: over generated Markdown (emphasis, code spans, links, images, reference links, tables, fences, HTML tags and
   entities, JSON escapes, fullwidth and decomposed letters) and every fixture, no line whose visible item matches is
   ruled out, including `foo**bar**` for `foobar` and `[foo](u)bar` for `foobar`; a long item whose match lies past the
   24 KiB page cut is found, a secret that crosses the cut is redacted whole, and an item whose line changed before
   `Full` gives no hit; a Codex thread continued across segments, its hits in the older segment found (marked
   `older`, no `item_id`), the thread counted once and only after all its segments, and a budget that stops inside it
   giving `partial` on its row; a never-opened Codex App
   chat and an App history behind its database both found, and a budget too small for them counted in
   `uncovered.codex_app`; each secret shape from `share_sanitize.go` redacted in snippets; a match inside a secret
   dropped; the budget giving `partial` and `searched_back_to`; cancellation; the size caps; a
   benchmark on a synthetic corpus, numbers in the PR. Never the developer's real home.
3. **The mnemo backend.** A fake `mnemo` script on `PATH` in tests: too old or missing leads to a scan; remote-host
   hits, unknown paths, blocked folders, assistant and health chats, lines on an abandoned branch and `archived` hits
   are dropped; a changed line is dropped; a hit in a cut item is matched and redacted whole; mnemo's snippet is
   never forwarded; a timeout falls back to a scan.
4. **Manager fan-out, route and relay**, the feature, the route scope, the architecture guard's table list, and
   `scripts/mock/mock_hub.py` answering the route. Tests with fake computers: slow (timeout), offline (no call),
   outdated, failing; one deadline per computer; cancellation; fusion order; caps; no query in the logs; two
   managers through the relay: relevance and newest orders interleaved across them, one `limit` over the merge,
   `partial` and `more` from either.
5. **Web**: the ⌘K row, results view, coverage line, landing and Continue; an end-to-end check against the mock hub.
6. **Assistant tool** and `agent_read`'s `around_seq`: the tool definition, deferred loading, the work-computer rule,
   the text it returns, coverage lines.
7. **iOS** and 8. **Android**: the row, page and landing against the mock hub; screenshots.

PR 1 goes first, PR 3 after PR 2, PR 4 after PR 2, PRs 5 and 6 after PR 4, PRs 7 and 8 after PR 5.

## Points for the maintainer to decide

Each comes with a recommendation; small reversible choices are settled during implementation.

1. **Backends**: a bounded scan everywhere plus mnemo where installed (recommended); or mnemo only, with computers
   without it reported as "not searched: install mnemo"; a built-in index is not recommended (the node guard, #757).
2. **Several managers**: a parallel relay in the hub that merges the managers' answers by score and applies the limit
   once (recommended); or the apps ask each manager and merge the same way, as with `pagedLists`.
3. **Tool output**: searched only when asked per search (recommended), or by default as mnemo does.
4. **Scan defaults**: 2.5 s per computer, 6 s per request, no default time window (the budget is the window and the
   answer says how far back it got). Or a default `since:90d`.
5. **Starting a search in the apps**: an explicit row (recommended), or automatically after typing pauses.
6. **Extensions**: full-credential devices only for now (recommended); an `agents.read` extension later?
7. **mnemo's archived copies**: dropped (recommended), or shown as found but not openable.
8. **The assistant tool**: `agent_history_search` next to step 1's mnemo extension (recommended), or the mnemo
   extension alone for the assistant.
9. **A switch for mnemo**: used automatically when found (recommended), with a per-computer way to turn it off; or
   off until the user turns it on.
10. **Later**: lifting the knowledge base's file prefilter into a shared package if scans prove slow. It is a cache
    that can only rule files out, but it is an index of a kind.
11. **Hits in a continued Codex thread's older segments**: shown with their snippet, opening the session at its start
    (recommended for now); or `session.items` taught to page back into a thread's older segments, so they land on the
    item, as a separate change.
