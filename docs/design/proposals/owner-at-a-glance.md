# Proposal: the owner sees every direction at a glance

Status for #3465. H1 shipped in #3494, #3519 and #3530, H2 in #3561 (`docs/assistant/todos.md` "Concerns"), and P1 in #3609.
The H3 New agent and progress work shipped in #3608 and #3679. The New chat offer strip proposed below never shipped,
and #11700 removed its background classifier from #3607. The owner pins an ordinary chat to make it a direction.
See `docs/assistant/unified-entry.md` "Pin a chat as a direction", "New agent goes under a leader" and "Names and progress".
The proposal covers the hub, prompts, web, iOS, Mac and Android plus a contract with work-log extensions.

## 1. Goal

**One sentence**: from the sidebar, or from one report card, the owner sees which leader owns each direction, how far
it got, and what is waiting for their decision, without asking "how's it going?".

Terms. *Secretary*: the main chat. *Leader*: a side conversation in `assistant.leaders` (`docs/assistant/unified-entry.md`
"Leader conversations"). **One leader is one direction**; every new chat opens one, a peer of the existing leaders.
(This is not a *manager*, which here means a whole assistant process, `docs/assistant/managers.md`.) *Task*: a to-do
with supervision, worked by agents. *Concern*: something the owner raised in the main chat and wants an answer to.

**Reached when** (each is checked in the pilot week, section 6):

| # | The owner feels | Checked by |
| --- | --- | --- |
| G1 | Nobody has to ask for progress: a report card arrives when something was reached, needs a decision or got stuck, plus the morning and evening briefings | The main chat on a working day has no "progress?" message from the owner |
| G2 | Every open task sits under a leader or under the secretary; nothing is loose | To-dos page, Leader view: no "Unassigned" group with open tasks |
| G3 | "Needs you" is one list and one number | The main chat's Needs you, the Persona row's badge and the report card's waiting block show the same items for the same data (test); the To-dos page's Waiting on you is the part of it the SOP calls owner-in-person, and says so |
| G4 | The report card says what moved today | The work-log card lists every direction whose tasks moved that day; it is never empty on a day tasks moved |
| G5 | Every concern gets an answer | Each concern is a record with a status; it closes only after the secretary reported the outcome back |

## 2. Structure

### 2a. Secretary → leaders (directions) → tasks, in the sidebar and on the To-dos page

```
◉ Persona                          (secretary; badge = Needs you count)
─────────────────────────────────
Persona chats                  ✎ ⋯
 ▾ Persona product      ● Working        leader row: opens the leader's conversation
     Fix push after redeploy  ● Waiting on you    its tasks; a task opens its details and agents
     Android sidebar groups   ● Working
 ▸ Infrastructure       ○ Stopped · 3
 ▸ Skills               ● Working · 1
   Quick chats (2)                       one-offs: chats that are not directions
   Scheduled                             (the automatic group, unchanged)
   All chats
Agents                                   (only agents under no task; unchanged block)
```

- A task belongs to the leader that supervises it (`supervision.supervisor`, already stored); one supervised by the
  main chat sits under **Persona** (the secretary's own). No new link table is needed for tasks.
- **Status** has one source, `docs/assistant/todo-status-sop.md`. A task shows its SOP section. A leader shows its most
  urgent task, ranked as the SOP ranks related to-dos, plus its own conversation asking. Three tones:

  | Tone | SOP sections |
  | --- | --- |
  | Waiting on you | Waiting on you, or the leader's conversation asks a question / approval |
  | Working | Working, or the leader's own turn is running |
  | Stopped | Just finished, PR to merge, Other open, Shelved, Done; the hint names which |

  Each client feeds its existing triage copy (`web/src/lib/todo-triage.ts`, `apple/Shared/TodoTriage.swift`,
  `android/core/domain/src/main/kotlin/persona/core/domain/tasks/TodoTriage.kt`) and one small aggregation with the
  same test cases on all three. A leader stopped for 48 hours folds as the SOP shelves.
- **To-dos page**: List › Status | **Leader** | Folder (iOS has no such switch yet; I1 adds it). Leader view = the same tree (secretary, then leaders by status,
  then tasks); inside a leader, tasks keep the SOP's section order. The header count is unchanged.
- **Progress count** (the main chat's Needs you / In progress, `docs/assistant/unified-entry.md` "Needs you and In progress: each thing once (#3387)"):
  same rule, scope by tree. In the main chat: everything, rows grouped by leader. In a leader: its tasks only.
  In progress keeps #3387's rule (the To-dos page's open tasks, Needs you items left out); Needs you keeps its wider
  set (plans waiting for Start, message cards, agents opened by hand), which is why G3 compares it with the badge and
  the card, not with the SOP's Waiting on you section.

### 2b. New chat opens a direction; New agent goes under one

**New chat** (the compose icon on the Persona chats title, section 3). Its first message is the one sentence. The hub
reads it right after it is sent, off the conversation's path, with a small model under its own purpose
`assistant.route` (like `assistant.title` in `server/internal/assistant/models.go`), and shows a strip:

```
┌───────────────────────────────────────────────────────────┐
│ New direction 「Android parity」 · project android-parity    │
│ Becomes a leader; a work-log record is created              │
│                    [Start direction]  [Just a quick chat]   │
└───────────────────────────────────────────────────────────┘
```

- **Start direction** adds it to `assistant.leaders` through a new endpoint: the owner's tap replaces the phone
  approval `persona_settings` asks for today (`server/internal/assistant/supervise_leader.go`), within the same hard
  limit of 20 leaders; files it with
  a kb project (an existing long project the sentence names, else a new one) and announces it (`leader.changed`,
  section 2c), so a work-log extension creates its record.
- A sentence that is plainly one-off (a quick question, a lookup) gets no strip: it stays a **Quick chat**, and its ⋯
  menu has **Make it a direction**. Past 6 leaders the strip suggests the closest existing one instead (rename, or open
  the chat there).
- **Undo / change**: the leader row's ⋯ has Rename, Change project and **End direction** (removes it from
  `assistant.leaders`; its tasks' signals go back to the main chat as today, its record is marked ended, nothing is
  deleted). A choice the owner made is never redone by the hub, as with titles the owner gave (`assistant.titles`).

**New agent** (and a task the secretary hands out): the secretary decides which leader supervises it. Today
`task_plan` makes the caller the supervisor, so H3 adds a `supervisor` argument (main chat only, a current leader). The New agent
sheet gets one field, **Under: Persona product ▾**, pre-filled by the same purpose: the kb project that owns the agent's
directory (already read by triage, `docs/assistant/knowledge.md`) → that project's leader; else the prompt matched
against leaders' titles and projects; else **Persona** (the secretary). Start is the one tap that confirms it; the field
changes it. The hub then creates the task with that leader as supervisor, as `agent_supervise` does for the main chat.
Moving a task later: the task's ⋯ **Move to…**; signals follow from the next one.

**Stored**: one record per leader in the assistant's store, `assistant.directions` (kept like `assistant.titles`; H1 lists
it at `GET /api/v1/assistant/leaders` and on the snapshot's session views; the apps' session rows get `direction` in W1,
where its cost on the list's allocation budgets is weighed): `{project: "<kb slug>", by: "assistant" | "user", at, ended_at?}`. Who is a leader is
still only `assistant.leaders`; which task is whose is still `supervision.supervisor`. The kb project lists the leader
among its related chats (`meta.json`), as for any chat.

### 2c. Leaders report up; the work-log record is the backbone

A leader reports only three things, matching today's escalation rules (`server/internal/assistant/supervise_leader.go`):

| Kind | When | Today | Added |
| --- | --- | --- | --- |
| Reached | `task_review` accepted a task | Owner gets a conclusion card | A result line on the direction's record |
| Needs a decision | `task_review` needs_user; a plan waiting for Start | `escalated` entry in the main chat + decision card | Same, plus a line and "waiting on you" on the record |
| Stuck | A task of the leader stays in Waiting on you / stopped unfinished over 24 h, or the leader's tasks did not move for 24 h | Nothing | A line on the record; one main-chat entry, no push |

All three are written by the hub from the ledger, not by a leader's turn, so they cost no model call. Stuck needs the
SOP's sections on the hub, which only the clients compute today (`docs/assistant/todo-status-sop.md` "Clients"): H1
ports the rules to Go and replays the clients' test cases, so there is one more copy, not a second definition. Progress
(`info`) is never reported up. Once a day the morning and evening briefings (`docs/assistant/assistant.md`,
"Scheduled tasks", their computed facts) gain one line per leader; no new push.

**Contract with a work-log extension** (one that keeps a requirement table with progress lines, a "waiting on" field
and a progress card). Today such an extension maps a session to a requirement only by the session's directory or
title, so a secretary → leader → task tree never reaches it and its progress card can come out empty.

| | Reused | New |
| --- | --- | --- |
| Events to extensions | `agent.finished`, `agent.needs_you`, `todo.changed`, `todo.decision` (`docs/extensions/extensions.md`) | `leader.changed` (`id`, `title`, `project?`, `ended?`) and `leader.report` (`id`, `leader`, `kind` reached / decision / stuck, `todo`, `text` ≤ 300 characters, `link?`, `at`; H1 added `id` and `at`, see `docs/extensions/extensions.md` "Leader events"); `todo.changed` gains `supervisor` and also fires when the supervisor changes (Move to…), which it does not today |
| Scopes | `todos.read`, `todos.write` | `leaders.read` for the two events and `GET /api/v1/assistant/leaders` (the leaders made before the extension was installed; added in H1) |
| Extension side (in its own repository) | Its progress-line route, its board and progress card | Create a record on `leader.changed`, keep leader id → record in its state; on `leader.report` write the line; map a session through its task's supervisor before directory / title |
| Report to the owner | The extension's progress card (sections by project, waiting on you, please verify) | Sections are leaders; the secretary sends that card instead of writing a report by hand (prompt change) |

**Concerns** (G5): when the owner raises something in the main chat, the secretary records it as a to-do of kind
`concern` (origin = main chat, no agent needed, never auto-closed). It closes only when the secretary has told the owner
the outcome in the main chat (a `reported` decision on the to-do with the message). It shows under **Persona** in the
tree and reaches the extension through `todo.changed`. H2 adds one SOP rule on all clients: an open concern sits in
Working while a task of it runs, else in Other open, labelled Concern and not folded.

### 2d. One place for "needs your decision"

The main chat's **Needs you** becomes the one place: it covers every leader's decisions (needs_user, plans waiting
for Start, agent questions, please-verify), the secretary's own, and concerns waiting for the owner, grouped by leader,
with one-tap answers where they exist. Everything else points there: the Persona row's badge, a leader row's Waiting
on you tone (opens Needs you filtered to it), the To-dos page's Waiting on you section (same function, G3) and the
report card's "waiting on you" block (fed by `leader.report` decision lines). Lark decision cards stay as they are.

## 3. UI fixes on the way (web follows the phone)

Only done inside the PRs of section 4, never on their own.

| # | Where | Fix |
| --- | --- | --- |
| U1 | New chat, all clients | The compose icon at the right of the Persona chats title, always (on web today it shows only while folded); web drops the `j:new` row (`web/src/views/Sidebar.tsx`), iOS drops the action row in `phoneActions` (`apple/Persona/Views/Assistant/AssistantSidebar.swift`), Android moves `JSidebarNewChat` into a `JSectionHeader` action |
| U2 | Web chat rows | Show waiting on you and unread like iOS, not only working (`showState={false}` today) |
| U3 | Web recent chats | A Recent heading with ⋯ (All chats, Archived) and 30 rows + Show all, as iOS; today 5 |
| U4 | Web auto chats | Right under the heading as on iOS, so a waiting one stays in view |
| U5 | Web phone drawer | Places as rows, as iOS; no second Agents block in the drawer |
| U6 | Web new chat page | The phone's short Needs you list above the composer (3 rows, then N more) |
| U7 | To-dos badge | One rule on every client, the SOP's open count, in a neutral color (the needs-you color is for Needs you only); iOS counts shelved ones today |
| U8 | iOS task dock | Move to the web's two entries, Needs you / In progress (#3387), from Pending / Progress / Agents |
| U9 | iOS to-do row | Show what it waits on with the one-tap answer, as the web does (iOS behind here) |
| U10 | Needs-you color | One color on all clients (DS-01 in `docs/design/DESIGN_SYSTEM.md`); the owner picks it, as that section says, then D1 records it |

## 4. Order and PRs

Each PR has its own issue and acceptance; a PR never only moves UI.

| # | PR | Acceptance |
| --- | --- | --- |
| **Phase 1: the backbone** | | |
| H1 | hub: `assistant.directions`, `leader.changed` / `leader.report` events, `leaders.read` scope, `supervisor` on `todo.changed`; the SOP rules in Go; reports written from the ledger | Go tests: each event fires once per change (replay writes nothing); stuck fires once per 24 h; ended leader → `ended`; the Go rules pass the clients' triage cases |
| X1 | the work-log extension, in its repository: record per leader, lines from `leader.report`, map by supervisor | Its fake-hub tests; on a real hub one accepted task gives one line and the card lists that direction (G4) |
| H2 | hub: concerns (`concern` kind, `reported` decision, never auto-closed) | Tests: closes only after a report; shows in `todo.changed` |
| **Phase 2: the structure** | | |
| H3 | hub: `assistant.route` for New chat and New agent; Start direction / Make it a direction / End direction endpoints (replacing the phone approval for this one setting); `task_plan` `supervisor`; New agent with a leader as supervisor | Rule table tests; timeout → quick chat, silent; a hand-started agent under a leader gets that leader's signals |
| P1 | prompts: the secretary routes and records concerns, sends the work-log card as its report; the leader prompt | Golden tests, diff reviewed (`docs/assistant/prompts.md`) |
| W1 | web: sidebar tree + tones, New chat strip, New agent field, Needs you as the one place, To-dos Leader view; U1–U6 | Tests for the aggregation cases and the Needs you count equal to To-dos; mock-hub screenshots light / dark; with no leaders the list looks as today plus U1 |
| I1 | iOS / Mac: the same; U1, U7–U9 | Same aggregation cases in `PersonaTests`; screenshots iPhone and Mac, light / dark; en / ja strings |
| A1 | Android: the same; U1, U7 | Same aggregation cases; shots in three styles × light / dark |
| **Phase 3: polish** | | |
| D1 | docs: `docs/design/DESIGN_SYSTEM.md` 3.4 (a list grouped by owner) and U10; `docs/assistant/unified-entry.md` "Leader conversations" | `scripts/test/test_doc_paths.py` passes |

Order: H1 → X1 → H2 → H3 + P1 → W1 → I1 / A1 → D1. The data backbone comes first, as the owner asked: until the
work-log record follows the leaders, no screen can show the right numbers. X1 lives in the extension's repository but
blocks only the pilot, not the PRs here. The prompts (P1) ship with H3 because the secretary's new tools are useless
without them; the leader-facing wording can follow the clients. A pilot week starts after X1 with the existing leaders.

## 5. Principles self-check (`docs/design/PRINCIPLES.md`)

| Principle | How this proposal meets it |
| --- | --- |
| 1 Defaults first | No setup: the first message is the sentence; New agent comes pre-filled. With no leaders the sidebar is today's list. Default in one sentence: "a new chat can become a direction with one tap, and a new agent goes under the direction it belongs to". |
| 2 Progressive disclosure | The tree appears only once a leader exists; the strip only for a direction-like sentence; the To-dos Leader view is one more option; the work-log contract only matters with such an extension installed. |
| 3 User control | Starting, renaming, changing a project, ending a direction and moving a task are the owner's taps, each undoable; the hub never redoes an owner's choice; every tone and count says which SOP section it counts. |
| 4 Consistency | Reuses `assistant.leaders`, `supervision.supervisor`, kb projects, the SOP and triage, the #3387 entries, extension events and scopes, and `SectionAdd` / `heading(_:menu:)` / `JSectionHeader`. New: the grouping by owner (D1 says why: folders say where work lives, not who owns it). |
| 5 Model and product separate | Routing has its own purpose `assistant.route`; reports are computed, not a model's; no model named. |
| 6 Rules are checkable | G1–G5 are measurable; each PR has acceptance; routing rules and the aggregation are tests shared by three clients. |

## 6. Open questions for the owner

1. **Who routes** New chat and New agent: the hub's small model (recommended: about 2 s, no main-chat turn, the cost
   leaders were added to cut) or a turn of the main chat (sees the whole day, slower and costlier)?
2. **Work-log record vs Persona to-do, which is the truth**: recommended, Persona's to-dos and supervision are the
   truth for tasks and concerns, the work-log record is one per direction and holds only result lines written from
   Persona's events; the extension's old to-do sync is not ported, so nothing is written twice. The alternative, syncing
   each task into its own record, gives two places to close the same thing.
3. **"Waiting on you" on the card vs in the app**: recommended, the card's waiting block comes only from Persona's
   Needs you (via `leader.report`), and the secretary's manual "settled" list is dropped once G3 holds; until then both
   exist and may differ for a day.
