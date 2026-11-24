# Proposal: To-dos as a paper, with its conversation beside it

- **Status:** decided 2026-11-18 (#10051): V1 Paper and Pane, decisions 2–7 as recommended, and the section bar puts
  All first. Being built in the stages of section 7.6 (#10174).
- **Built on** (decided elsewhere; a change to one is named where it happens and again in section 5): the super rules,
  `docs/design/PRINCIPLES.md`, the navigation options of `docs/design/sidebar-navigation.md` (option 2 on phones,
  option 3 at regular widths), task cards P1–P7 (`docs/design/task-cards.md`), the Inbox's section bar (#9053) and the
  message states of DESIGN_SYSTEM 4.3. "DESIGN_SYSTEM 4.3" is a section of `docs/design/DESIGN_SYSTEM.md`; a bare
  "section 4.3" is this document's.
- **Decided:** the seven owner decisions in section 5, after the owner clicked through the three prototypes in
  `docs/design/prototypes/todos/` (V1 is the one being built; V2 and V3 stay as the record of the alternatives).
- **Covers:** the hub and manager, the wire, iOS, iPadOS and the iPhone Duo first; then the web, Android, HarmonyOS, the
  watch, widgets and the Live Activity.

**Summary.** A to-do becomes one page, the **paper**. Before any conversation, the paper says what the to-do is, where
it stands, what needs the owner and what happens next. The owner can tell Persona something from a composer docked at
the paper's foot without opening anything.

When the owner wants to talk, the **Conversation** arrives on the right: as a pane at regular widths, or pushed from the
right on phones with the to-do pinned as its header. The paper's short Conversation block and the pane are **one
timeline at two zooms**, stored once on the to-do. List, paper and conversation are the content regions: at most three
show, with the sidebar beside them where it fits. When the conversation opens, the leftmost column steps aside in the
same transition, and on the iPhone Duo every step cross-fades.

The hub computes one phase and one sentence for each to-do, and every client draws them. A request has one home, and
every place that collects requests shows the same number.

**The default a new user meets** (principle 1): *"To-dos is your list. Write anything down and tick it off, or tell
Persona about it at the foot of its page. Persona works on it and asks you only when it needs you."*

**How this was made.** Twelve research reports fed five concepts, which four reviewers scored and two fact-checks
against `origin/main` corrected (working notes, not published; their public sources are in section 9). Where the
fact-checks showed that a rule is already decided (the To-dos section order, what In progress counts, the Inbox bar's
order, what Fleet counts), the proposal keeps the decided rule and offers the panel's original design as the
alternative in owner decisions 3 and 4.

**Reading guide.** The owner: the Summary, the decisions below, section 4.2, then click the prototypes; section 5 has
each decision in full. Implementers: sections 3, 6 and 7.

**Decisions at a glance** (in full in section 5):

| # | Decision | Decided (2026-11-18) | What it changes |
| --- | --- | --- | --- |
| 1 | The shape of To-dos | V1 Paper and Pane | one page per to-do instead of two; no detour to Agents |
| 2 | Navigation and layout | yes to all six parts | places under the sidebar's To-dos row; the sidebar or list steps aside; the per-place overlay; a 320-pt paper on the Duo; a pushed to-do from the main chat; one web column width |
| 3 | One partition: order, counts, words | the hub's phase and section, in DESIGN_SYSTEM 3.4's order | one partition for list, tray and tree; a failed start stays open; rule 9a; Got it; Someday |
| 4 | Ask homes and counts | the DESIGN_SYSTEM 4.8 amendment | a to-do's own asks live on its page; the Assistant's Needs you counts each to-do once |
| 5 | The to-do's own Conversation | stored on the to-do, answered by a steward | talking to a to-do no longer routes through the chat that created it |
| 6 | One owner-facing noun | "to-do" | "task" leaves screens |
| 7 | Where status is computed | the hub's manager | the three client copies of the status rules |

**Terms** (used before they are defined; internal words, never on screens, section 3.11):

- **Paper**: a to-do's own page (section 3.5). **Pane**: the conversation's region beside it at regular widths.
- **Place**: a To-dos destination with its own title, URL and empty state: All To-dos, Today, Upcoming, a project.
- **Phase / section**: the hub's one value for where a to-do stands, and the list section it is drawn in (section 3.4).
- **Steward**: whoever answers messages sent to the to-do: its Pinned lead, otherwise Persona (section 3.6).
- **Round**: one owner instruction or answer, with everything that followed from it (section 3.6).
- **Run**: one Agent session working for a to-do. **Ask**: a request for the owner (a decision, question, plan, tool).
- **Home / door**: where an ask's full card lives, and the places that point to it and answer safe asks in place
  (section 3.7).

---

## 1. Why: what is wrong today

Line numbers in this document are as of `origin/main` d2187cc84b (2026-11-18).

| # | Problem | Evidence (repository) |
| --- | --- | --- |
| 1 | **Two to-do pages on every client**, showing different things: a record-first form and a conversation-first sheet or panel. Some features exist on only one (on iOS and Android the one-tap answer to a to-do's ask lives only on the chat side) | iOS `TodoDetailView` (`apple/Persona/Views/Todos/TodoDetailView.swift`) and `TaskDetailSheet` (`apple/Persona/Views/Assistant/Tasks/TaskSheets.swift`); web `TodoDetail` (`web/src/views/Todos.tsx:2019`) and `TaskPanel` (`web/src/views/assistant/TaskPanel.tsx`); Android `TodoDetailScreen` and `TaskDetailSheet` / `TaskDetailPage` (`android/feature/agents/…/TodoDetailScreen.kt`, `android/feature/chat/…/AssistantTaskSheets.kt`) |
| 2 | **The conversation is a detour.** Opening an Agent from a to-do leaves To-dos; on iPhone the app switches to Agents, and Back lands on the Agents list, not on the to-do. From the main chat the to-do shrinks to a banner over the Agent's chat | `TodoDetailView.swift:407-413` → `apple/Persona/App/AppModel.swift:1574-1604`; web `Todos.tsx:2316` navigates to the session, and "Answer in chat" opens the origin conversation; `docs/assistant/unified-entry.md` "Task details" |
| 3 | **No to-do has a conversation of its own.** "Talking to a to-do" routes text through the conversation that created it to its most urgent Agent, refused for any other caller. Only a bound Lark topic behaves like a thread of its own. A to-do attaches to conversations in 16 ways, with no timeline and no "send to this to-do" | `server/internal/assistant/tasks_follow.go:749` (`routeToTaskAs`) and the same guard in `decision.go:364` (`routeDecisionAnswer`) |
| 4 | **Feature soup on the page.** The web detail stacks about 20 blocks in one 760-px column; iOS stacks about 10 sections. A new to-do already shows about nine blocks. "Project" means three things on one screen (the knowledge-base project, linked workspaces, and a Folder grouping that falls back to the repository). The web list offers 4 groupings × 5 due filters × 3 sorts | `TodoDetailView.swift:188-340`; `apple/Persona/Views/Todos/TodoPlanViews.swift:127-167`; `web/src/views/Todos.tsx:2019-2470` |
| 5 | **One object, five domains**: the owner's intent, delegation, oversight and acceptance, requests to the owner, and software delivery, plus bindings to conversations and channels. One row with 26 columns, 12 more tables, about 16 settings blobs and about 25 in-memory books; the `Todo` struct has about 40 JSON fields. About 77 concepts hang off it, about 15 of them essential; about 10 implicit signals decide what kind of thing a row is, and 13 server paths create to-dos | `server/internal/assistant/todos.go:49-132`; `server/internal/db/migrations/manager/` |
| 6 | **Status chaos.** About 26 independent status axes; the visible status is computed in at least nine places with six vocabularies. The status rules are hand-copied in Go (612 lines), TypeScript (353), Swift (432) and Kotlin (389), and HarmonyOS has a six-state view of its own; only the web and Go replay the 82 shared fixture cases. Four partitions of open work compete: the status rules' eight sections, the tray's entries, the direction tree's five states and PR #9823's five groups. A failed start is stored as done | `server/internal/assistant/todo_triage.go`, `web/src/lib/todo-triage.ts`, `apple/Shared/TodoTriage.swift`, `android/core/domain/…/tasks/TodoTriage.kt`, `harmony/entry/src/main/ets/models/TodoWorkspace.ets:192`; `spec/fixtures/triage`; `docs/assistant/todo-status-sop.md` ("Sections"); `server/internal/assistant/direction_tree.go:52` |
| 7 | **Counts disagree on one screen.** The iOS sidebar's To-dos number counts every open, non-quiet to-do and draws it in the needs-you tone, while the page's own "open" rule leaves out shelved, accepted and deployed ones. On the web, the sidebar's Agents block counts its Needs you filter in sessions while the To-dos page beside it counts its Needs you section in to-dos, so one screen shows Needs you with two numbers | `apple/Persona/App/TodoStore.swift:129`; `apple/Persona/Views/Assistant/AssistantSidebar.swift:1304`, `:1515`; `apple/Shared/TodoTriage.swift:227-231`; `docs/design/DESIGN_SYSTEM.md` ("One count, per session") |
| 8 | **A developer flow on an everyday page.** Hand to Agent asks for a computer, folder, worktree, launcher and model. The list shows "PR to Merge" next to Needs you. HarmonyOS already hands to the Assistant by default, which is the closest to super rule 4 | `apple/Persona/Views/Todos/TodoHandoffSheet.swift:103`; `apple/Shared/TodoTriage.swift:13-40` |
| 9 | **No list beside the detail on iPad, the open Duo or Android regular windows.** The detail pushes over a list stretched to the full page; there is no inspector anywhere under `apple/`. The web already has sidebar, list and detail | `apple/Persona/Views/Todos/TodosView.swift:116`; Android `paneMetadata` gives list/detail roles only to Agents, Inbox and Settings (`android/app/src/main/java/persona/android/ui/Root.kt:1247`) |
| 10 | **The whole list is fetched again on every change.** Every to-do write sends a bare `{"type":"todos"}` and every client re-reads the merged, unpaginated list (about 200 KB on a busy hub). On the web a tick is not optimistic: PATCH, a whole-list read, `refresh()`, then the push's read: about 3 full list reads and 9 requests | `server/internal/assistant/todos.go:422-465`; `web/src/views/Todos.tsx:553-561`; `web/src/lib/store.ts:1359-1384` |

For scale: iOS To-dos views are 14 files and 4,480 lines; `web/src/views/Todos.tsx` is 3,387 lines; Android has about
5,000; the server's to-do code is about 109 files and 39,000 lines. The whole area is about 16 days old.

---

## 2. What the research found

### 2.1 Findings

| # | Finding | Public evidence | For Persona |
| --- | --- | --- | --- |
| 1 | **The work item is primary; agent runs hang off it** as cards in its activity, opening into a panel or sheet | [Linear coding sessions](https://linear.app/changelog/2026-06-11-coding-sessions) · [GitHub agent sessions from issues](https://github.blog/changelog/2026-04-23-view-and-manage-agent-sessions-from-issues-and-projects/) · [Jira agents on work items](https://support.atlassian.com/jira-software-cloud/docs/collaborate-on-work-items-with-ai-agents/) · [Sunsama agentic delegation](https://www.sunsama.com/blog/work-has-changed) | The to-do is the root; a run is a child |
| 2 | **One context holds many tasks; a redo is a new task in the same context** | [A2A specification](https://a2a-protocol.org/latest/specification/) (`contextId`, artifacts) | A retry is a new run on the same to-do |
| 3 | **Five columns never fit.** Split views have two or three panes; Material layouts have one to three; nobody keeps app nav, area nav, list, detail and a chat open together (the products that come closest are called too dense) | [HIG split views](https://developer.apple.com/design/human-interface-guidelines/split-views) · [Android canonical layouts](https://developer.android.com/develop/ui/compose/layouts/adaptive/canonical-layouts) | At most three content regions; the leftmost gives way |
| 4 | **App and area navigation share one sidebar** of at most two levels | [HIG sidebars](https://developer.apple.com/design/human-interface-guidelines/sidebars) · [WWDC25 208](https://developer.apple.com/videos/play/wwdc2025/208/) (Mail flattens its sidebar) · [Linear's conceptual model](https://linear.app/docs/conceptual-model) | To-dos places live under the To-dos row of the existing sidebar |
| 5 | **The conversation is a supporting pane**, "meaningful only in relation to the primary content"; an inspector is a trailing column at regular width and a sheet or page in compact width | [Android canonical layouts](https://developer.android.com/develop/ui/compose/layouts/adaptive/canonical-layouts) · [SwiftUI inspector](https://developer.apple.com/documentation/swiftui) | A toggled trailing pane, not a fifth navigation level |
| 6 | **On a phone no sheet keeps the item visible while typing.** A medium sheet plus the keyboard leaves about 76 pt of the item; sheets stack badly with the composer's own sheets | [HIG sheets](https://developer.apple.com/design/human-interface-guidelines/sheets) ("one sheet at a time") · [WWDC21 10063](https://developer.apple.com/videos/play/wwdc2021/10063/) · [NN/g on bottom sheets](https://www.nngroup.com/articles/bottom-sheet/) | A docked composer for quick notes, a pushed conversation with a pinned header for real talk |
| 7 | **A docked comment or follow-up box at the item's foot** is the common quick path | [Todoist task view](https://www.todoist.com/inspiration/todoist-new-task-view) (sticky comment bar) · [Jules](https://jules.google/docs/running-tasks/) | The paper's composer |
| 8 | **Addressing splits by audience**: everyday tools send to one assistant that knows the context; developer tools send to the working agent | [Notion agent](https://www.notion.com/help/notion-agent) · [Planner agent](https://techcommunity.microsoft.com/blog/plannerblog/manage-tasks-in-planner-with-the-planner-agent/4528939) · [Linear agents](https://linear.app/docs/agents-in-linear) | A steward is the default addressee; a visible chip can name one Agent |
| 9 | **Steer and queue are different, and receipt, delivery and execution are shown** on the sent instruction | [Vibe Kanban chat](https://www.vibekanban.com/docs/workspaces/chat-interface) · [Codex cloud](https://learn.chatgpt.com/docs/cloud) | Queued for X · after this step, with Send Now where the run supports it |
| 10 | **"Needs input" is a first-class, non-terminal state**, and answering where the request appears matters: removing list-level Approve was filed as a regression ("every approval costs three interactions instead of one"); the server may gate lock-screen answers per push | [Linear agent interaction](https://linear.app/developers/agent-interaction) · [MCP tasks](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks) · [claude-code#93985](https://github.com/anthropics/claude-code/issues/93985) · [claude-code#106486](https://github.com/anthropics/claude-code/issues/106486) · [codex#60079](https://github.com/openai/codex/issues/60079) | One home per ask, answerable in one tap on the row, the paper and outside the app |
| 11 | **History reads as rounds**: agent rows fold under the message they answer, similar events group | [Devin release notes](https://docs.devin.ai/release-notes/2026) · [Linear collapsed history](https://linear.app/changelog/2025-04-03-collapsed-issue-history) | Rounds, with one summary line per run (super rule 2) |
| 12 | **Calm details**: properties show when set, an "add" control holds the rest; quick add "opens clean" | [Things](https://culturedcode.com/things/features/) · [Todoist 2026 changelog](https://www.todoist.com/help/todoist/product-updates/2026-changelog-HD3jJAtLd) · [Jira issue layout](https://support.atlassian.com/jira-cloud-administration/docs/configure-issue-layout/) · [Reminders in iOS 27](https://9to5mac.com/2026/09/29/heres-everything-new-for-reminders-in-ios-27/) | Chips only when set, plus "+" |
| 13 | **Results live apart from messages**, as attached things with an outcome | [GitHub issue artifacts](https://github.blog/changelog/2026-10-28-issue-artifacts-are-now-in-public-preview/) · [A2A artifacts](https://a2a-protocol.org/latest/specification/) | A Results strip on the paper |
| 14 | **For everyday people the flagship AI breaks an intention into steps**, and suggestions look different until kept | [Tiimo, App Store Award 2025](https://www.tiimoapp.com/resource-hub/tiimo-winner-2025-app-store-awards) · [Apple Design Awards 2026](https://developer.apple.com/design/awards/) (Structured) | **Break Into Steps** on a fresh to-do |
| 15 | **On a foldable, add one level of hierarchy on the inner display** and avoid extreme layout changes; task apps that shipped Duo layouts used two panes | [HIG designing for iPhone Duo](https://developer.apple.com/design/human-interface-guidelines/designing-for-iphone-duo) · [Things for iPhone Duo](https://www.macrumors.com/2026/10/22/things-updated-for-iphone-duo/) | Two panes at the Duo's crease |

### 2.2 Verdict on the owner's five-level idea

The owner proposed main sidebar → To-dos sidebar → list → detail → conversation, with motion running left → right and
the conversation unfolding only when wanted. The research keeps the idea and its direction, and changes five things:

1. **A sliding window, not five columns.** At about 320 pt per column, five need about 1,600 pt, so only wide desktop
   windows show all five. Elsewhere at most three content regions show, and the leftmost column steps aside as the
   owner moves right: the sidebar first, then the list.
2. **Levels 1 and 2 merge** into the existing sidebar: To-dos places are the To-dos row's children, never a second
   column (HIG: no more than two levels in a sidebar). On phones the list's title menu is the second way in.
3. **The conversation is a supporting pane.** At regular widths a toggle shows and hides it (⌃⌘I); it is not part of
   Back history. On phones it becomes the destination.
4. **Phones keep the to-do in view** with a docked composer for quick notes and a pushed conversation whose header is
   the to-do collapsed. No sheet.
5. **Under the Duo's side bars, nothing slides sideways.** Moving the shell's pages sideways there cost about 28 ms a
   frame (p95) (`docs/lessons/ios.md`, "Pages that move sideways under iPhone Duo's side bars"), so the left → right
   flow is drawn as cross-fades.

---

## 3. The recommended design: paper and pane

### 3.1 The idea

The to-do reads like a sheet of paper: a title, one plain sentence about where it stands, what needs the owner
(answerable right there), the owner's notes, steps, results, and the last word or two of its conversation. Telling it
something does not require a conversation: the composer at its foot names its addressee, and the reply lands on the
paper. Talking opens the conversation on the right. The machinery stays out of sight: Agents, computers, code changes
and stages exist only for code work, behind one folded line, **Work details**, and in a run's own session.

### 3.2 Information architecture per device

Forms only; the widths are in section 3.8.

| Level | iPhone, Duo outer | Duo inner | iPad | Web ≥ 1024 | Android regular |
| --- | --- | --- | --- | --- | --- |
| L1 app places | the drawer (unchanged) | the sidebar, over the page | the sidebar: over in portrait, beside in landscape | the sidebar, or the rail | sidebar column or drawer |
| L2 To-dos places | the sidebar's To-dos children and the list's title menu | same | same | same | same |
| L3 list | the place's root page | a pane (left of the crease in portrait) | a pane | a column | the list pane |
| L4 paper | pushed | a pane | a pane | the flexible column | the detail pane |
| L5 conversation | pushed, the paper pinned as its header | a pane; the list steps aside | a pane; the list steps aside in portrait, the **sidebar** in landscape (iPad mini: pushed) | a column; the sidebar becomes the rail | the supporting pane, or pushed |

The window shows the deepest region the owner asked for, plus as many to its left as fit; columns give way leftmost
first. The sidebar (the web's rail) is navigation, not one of the three content regions. The conversation never lies
over the paper, except on web windows of 640–1023 px (as the side panel does today).

### 3.3 Sub-navigation: places and the Filter

**Places** have identity: a URL, a title, an empty state of their own, a capture default, a remembered scroll and
selection. The zh-Hans words of every label in this section are in section 3.11.

| Place | Where | Contents | Count on its row | Capture default | Empty state |
| --- | --- | --- | --- | --- | --- |
| **All To-dos** | the sidebar's **To-dos** row itself | every open to-do, in sections, with the Filter | blue N (to-dos that need you), hidden at 0 | none | "Nothing to do yet. Write down anything you want done. Persona can help with the rest." + **New To-Do** |
| **Today** | child row | due, reminding or overdue today, plus what the owner put on Today; no Filter (amended 2026-11-21) | none | due today | "Nothing for today." + "3 coming up this week ›", a link beside the foot's **+** |
| **Upcoming** | child row | dated to-dos under day headings, no Filter and no sort (amended 2026-11-21); ⋯ › Show as Calendar (Day, Week, Month; drag to reschedule), alone in its ⋯ | none | asks for a date, tomorrow at first; a day heading's **+**, or an hour tapped on its calendar, fills it | "Nothing scheduled. To-dos with a date show up here." |
| **Projects** (each knowledge-base project with open to-dos) | child rows after a 12-pt gap, up to 5, then **All Projects…** (every project, each with its needs-you part and its open count, neutral, amended 2026-11-21) | that project's open to-dos, with the Filter | the project's needs-you part, blue, hidden at 0 | in that project | "Nothing open in Kyoto trip."; the foot's **+** (New To-Do in Kyoto trip) |

- The sidebar stays at two levels, with no "Projects" heading row. The fold state belongs to the owner
  (`sidebar.fold.todos`, a `@StoredValue` like `sidebar.fold.places`); it starts unfolded and never folds or unfolds by
  itself, so rows below never jump.
- **Pinned directions are not To-dos places.** They keep their home under Persona Chats › Pinned, where the hub's tree
  unfolds into their to-dos. A to-do picked there opens in To-dos as a root swap with its row selected. There is no
  Group by Pinned (DESIGN_SYSTEM 3.4, #6662: the Pinned rows already draw that tree).
- **Phones:** the drawer shows the same tree, and To-dos reopens on the last place (option 2). **Regular widths:** a
  pick replaces the root, never animated (option 3). **Everywhere:** the list's title ("All To-dos ▾") is a menu of the
  same places, the second way in that the HIG asks for.

**States are a Filter at the list's foot** (amended 2026-11-21, `docs/design/DECISIONS.md` "11-21 The To-dos list";
first drawn as the Inbox's section bar), in All To-dos and each project's place: the same choices as the Inbox's bar, as
a pop-up at the leading end of the foot toolbar, Mail's, beside the system's search field and **+** (New To-Do). Its
menu always lists all five with how many each would show; a choice on names itself, with its own ×; the choice is not
remembered. Needs you's blue N stays on its section header and the drawer's row; while a choice hides what needs the
owner, the list's first line says "● 3 more need you · Show All", and on iOS 26 the title's subtitle keeps it once that
line scrolls away. Nothing sits under the title. Today and Upcoming draw no Filter (amended 2026-11-21; it was "inside
every place"): they list open to-dos already narrowed by date, so Done would always be empty there and the other choices
would narrow little. Outside All To-dos the same one guard line also counts what needs the owner outside the place ("●
2 more need you, not in Today · Show All"), what a choice hides in a project's place too, never two lines, and its Show
All opens All To-dos. Each client draws the Filter in its own idiom (stage 4); the contract below is the same everywhere.

| Choice (the landing choice first) | Shows | Count |
| --- | --- | --- |
| **All** (landing) | every section stacked | none |
| **Needs you** | Needs you, with Can wait folded at its foot | blue N |
| **In progress** | Working and Waiting, with Just finished as an uncounted group at its foot, as the tray shows it (#8237) | neutral: Working + Waiting (5 in the mock) |
| **To do** | To do, with Someday folded at its foot | neutral |
| **Done** | closed to-dos by day (Today, Yesterday, This week, Earlier · Show older) | none |

**Where this Filter differs from the Inbox's bar** (Needs you · In progress · FYI · Done · All, landing on Needs you;
`docs/assistant/inbox.md`): it is drawn as a Filter, not a bar, because the list already lands on every section with
its heading and count, so the control only narrows it, where the Inbox's bar is the way to the sections it does not
land on; the list lands on **All**, because To-dos is also the owner's plain list and All already
puts Needs you first; **All is listed first** in the Filter's menu, its landing choice; To do takes FYI's place, since
To-dos has no FYI; Done keeps the to-do's shipped zh-Hans word (section 3.11); and the calm rule below hides the Filter.
Owner decision 3 names all five. The Inbox keeps its order: its All is the reset, not the landing.

**Answer to #9053.** No filter is drawn as navigation: places have identity, and states are the Filter. There is no
third column: places live in the existing sidebar column. There is no second pattern by width: the same tree is the
drawer on phones and the sidebar on wide screens, and the Filter is the same everywhere, every name whole at every text
size, with nothing past an edge.

**Calm rule.** While only the owner's plain To do rows exist and nothing needs the owner, the Filter and the section
headers are not drawn, so the list reads as a plain list with search and + at its foot; they appear once two sections
have rows. **Search** (amended 2026-11-21) is the system's search field on the list, scoped to To-dos: it looks through
the titles and notes of the to-dos on the device as the words are typed, keeps the sections with their folds open, and
ends with one row, "Search Chats for …", that takes the words to the app's search; closed to-dos and the hub's own
matches merge in later, with #10465's search step. It replaces "the app's one search, scoped to To-dos, shown as a
removable chip": the iPhone's drawer has no scoped search, and every native sibling list keeps the system's field. The
web keeps #6589's one search.

**Grouping and display** (amended 2026-11-21, after weighing grouping, filtering and sorting together for the fewest
paths that still cover what people do). Places pick **where**, the Filter picks **which**, search finds **any** to-do
and + **adds** one; the list's sections are its grouping. The list's one ⋯ (on All To-dos, Today and a project;
Upcoming's holds Show as Calendar alone) holds **Sort By**: **Suggested** (the hub's order inside each section, the
default) · **Recently Changed** (the old page's default order; not "Recent Activity", since Activity names a to-do's
timeline, section 3.11) · **Due Date** (the one way to order Working and Waiting by date) · **Priority** (high, then
normal, then low, the hub's order inside each; #3740's "show high priority first"), the current one checked, kept on the
device; choosing Suggested is the reset. A high-priority to-do carries "!" before its title in the caution tone, as
Reminders marks it, so the order has a reason on every row. Then one checkmarked item, **Group by Project**, as Mail's
Group by Sender: below Needs you, a group per project in place of the other sections; never offered in the calm list,
greyed with why under Needs You and Done; kept on the device. A sort never moves a section, and Needs you and Can wait
keep the hub's order, deadlines first, under every sort and grouping. Gone: Group By's Due Date (Today and Upcoming
cover it, and Filter › To Do lists the dated to-dos soonest first with the undated after), Reset View, and the Show
Someday and Show Done Today toggles (the folds at the list's end are their one home). What the list shows starts fresh
each visit.

### 3.4 The list

**One partition.** The hub computes `phase` (the domain value) and `section` (where the row is drawn) for every to-do
(section 6.3). Sections, top to bottom:

| # | Section | Holds (phase · reasons) | Line 2 says | Count | Folded |
| --- | --- | --- | --- | --- | --- |
| 1 | **Needs you** | `needs_you`: `failed_start`, `agent_asks` (question, plan, tool), `interrupted`, `stopped_unfinished`, `owner_decides` (a decision, a possible duplicate), `verify` ("Check the result"), `steps_proposed`, `signin` | the ask's own name in the needs-you text tone ("Which ryokan?"); a failed start in danger text, with the shipped state word ("Didn't start · Desk Mac is offline") | **blue N** | never |
| 1b | └ **Can wait** | asks marked `can_wait` (held for the briefing); runs that ended on a plain-text question (`awaits_reply`, state word **Awaiting Reply**) | "Persona picks Hanami on Fri unless you answer" / "Persona is waiting for your reply" | listed, **not counted** | yes, "Can wait · 1 ›" |
| 2 | **Working** | `working`: `persona_working`, `agent_running`, `starting`, `queued`, `background` | the lead run's latest line, amber mark ◐ | neutral | no |
| 3 | **Waiting** | `waiting`: `parked`, `pr_open`, `usage_limit`, `computer_offline` (after Start When It's Back), `scheduled_report`, `someone_else` | who or what, since when, what wakes it: "Waiting for a reviewer to merge · since Tue" | neutral | no |
| 4 | **To do** | `todo`: `rest`, `finished_seen` ("Finished Tue · Mark Done"), nobody working; an **Inactive** mark ("No activity for 2 days") is a flag inside its section | the first line of notes, or nothing | neutral | no |
| 5 | **Just finished** | `ready` (open, finished, not ticked: while unseen and 24 h after seen) and closed-unseen within 24 h (`phase = done`, `section = just_finished`) | the conclusion: "✓ Booked Tue 10:00 · check and tick" | neutral | no |
| 6 | **Someday** | `someday`: the owner's own "not now" | "Someday · from Apr 1" | none | yes |
| 7 | **Done** | `done`, seen or older than 24 h | how it ended, and by whom | none | yes, "Done Today · N ›" in All |

- **The order** is DESIGN_SYSTEM 3.4's rule for To-dos status sections (#3156, #5707): what the owner has to act on,
  then the unfinished work, then what just finished, so what still has to be done never takes a scroll. The Agents list
  orders its rows differently since #9751 (what still runs, what just finished, then what waits); owner decision 3
  offers that order as the alternative, for one order everywhere.
- **Awaiting Reply keeps its own state** (`docs/design/DECISIONS.md` ("11-10 Awaiting Reply stays its own state")): its
  row says Awaiting Reply, it is not counted in N, and Can wait only files it under Needs you's header, folded.
- **Within a section** the hub's `rank` orders rows: Needs you by deadline, then oldest ask; Working by latest
  activity; Just finished newest first; Waiting by soonest wake; To do by overdue, today, due date, the owner's order.
- **Steps roll up**: a step (a child to-do) is never a row in its parent's place; the parent carries the most urgent
  phase of itself and its steps, counted once, and line 2 names the step ("Ryokan · Which of 3?").
- **Rows never move under a finger**: a phase change re-files a row only while the list is idle (a 0.2-s fade). Moves
  that depend only on time travel as data: `next_change_at` (24 h in Just finished, 48 h to Inactive, a wake) lets a
  client re-file a row offline without a rule engine.

**Row anatomy** (the work-item row of DESIGN_SYSTEM 3.4, with the tick in the source mark's slot; compact as
Reminders' rows, amended 2026-11-21: one 12-pt padding, no door, no chevron):

```
 ○  Plan the Kyoto trip            Due Apr 3   line 1: title, bodyStrong 17, ≤ 2 lines; the time in words
    ● Which ryokan? · Hanami recommended       line 2: mark + the hub's line, secondary 15, phase tone
    Kyoto trip · Steps 2/5                     line 3: caption 13, facts the grouping doesn't already show
    ( OK: Ryokan Hanami )     Options ⌄        answers: safe asks only, on one line at the row's foot
```

- **Container** (amended 2026-11-21): the Agents list's section cards, one system inset grouped card per section, the
  shared header above it, the rows 16 pt inside it, separators from the titles' column; Can wait folds inside Needs
  you's card, Someday and Done today fold after the last card (DESIGN_SYSTEM 3.4 "To-dos list").
- **Time:** in words that say what it is, "Due Fri" or "16m ago" (how long ago it came to where it is, or closed),
  never a bare day or clock, and none when line 2 already says one ("since Tue").
- **Priority** (amended 2026-11-21): a high-priority open to-do carries "!" before its title in the caution tone, as
  Reminders marks it, spoken "High priority"; normal and low carry no mark.
- **Tick:** a 22-pt ring in the secondary color (3:1 on every card and canvas) with a 44-pt hit area, labelled
  "Complete, Plan the Kyoto trip" with its value (closes HIG-006 and HIG-007 in To-dos). A tick is optimistic:
  strike-through, a haptic and "Done · Undo" for 4 s, then the row leaves once the list is idle. On a to-do someone is
  working on, the tick first asks "Persona is still working on this. Mark it done and stop?" with **Done and Stop** /
  **Cancel**.
- **Marks on line 2:** ● needs you (blue), ◐ working (the amber spinner arc, paused off screen and under Reduce
  Motion), ✓ just finished (green), ◷ waiting (an idle clock). To do has no mark. The needs-you mark follows
  DESIGN_SYSTEM 2.7 exactly as on Agents rows, so the two lists never breathe differently.
- **Answers on a row** (DESIGN_SYSTEM 3.4: choices and low-risk tool approvals answer in place; plans open their home).
  A safe choice with a hub-marked recommendation shows **[ OK: ‹option› ]** (as wide as its words, at most 260 pt) and
  **Options ⌄**, which unfolds the full ask card in place (task cards P2); a low-risk tool approval, **[ Allow Once ]**
  with the full command on line 2; a failed start, **[ Start When It's Back ]**. They sit on one line at the row's foot,
  after line 3, stacking where they do not fit. Plans, payments, merges, sends to other people and anything
  irreversible have no button (amended 2026-11-21: the earlier **[ Review Plan › ]** / **[ Review › ]** door looked like
  a button and did nothing of its own): line 2 names the ask ("Plan awaiting approval") and the row opens it. Two asks
  end line 2 with "· 2 asks", the more urgent one named.
- **Never on a row:** runtime, engine, launcher, model, path, repository, session id, PR number, Agent brand marks, or
  more than one count. A computer appears only by the owner's name for it (DESIGN_SYSTEM 5.6), and only in a
  failed-start or waiting reason.
- **Swipe:** one trailing swipe, as `docs/design/DECISIONS.md` ("11-13 The Inbox swipes as on iOS") gives the Inbox,
  holding **Done** (full swipe) and **Someday**; Someday is an addition, named in owner decision 3. Long press or right
  click: Today, Date…, Project…, Someday, Message Persona… (opens the paper with the composer focused), Copy Link,
  Delete… (a confirmation naming the to-do, then Undo).
- **Web keys:** J/K move, X ticks, Enter opens, ⇧Enter opens with the conversation.
- **Offline:** the cached list stays, with one caution line, "Can't reach your hub · showing what was saved 10:14".
  Writes queue. An empty section is not drawn.

### 3.5 The paper

**Anatomy, top to bottom, each element with the rule that shows it.** A new fixture set, `todo-detail`, holds this
table as data (presence and order only), so every client draws the same page. The columns follow three of the mock's
to-dos (section 4.2): an everyday one (Plan the Kyoto trip), a developer one (Fix login crash for SSO users) and one
with several Agents (Release Weather App 1.3).

| # | Element | Shown when | Everyday | Developer | Several Agents |
| --- | --- | --- | --- | --- | --- |
| 0 | Bar: leading ‹ *place* when pushed, or the list / sidebar button at the root (#8603); trailing **Conversation** toggle (`Theme.Icon.conversation`, a dot for unread replies; under the Duo's side bars a symbol-only glass item in the side column) and **⋯** | always | ✓ | ✓ | ✓ |
| 1 | Tick + **title** (title 22 semibold, edited in place); the bar shows the title once it scrolls away | always | ✓ | ✓ | ✓ |
| 2 | **State line**: the state word in its tone · the hub's sentence (`line {key, args}`) | always, except a plain To do with nothing to say | "Needs you · Persona found 3 ryokans near Gion" | "Waiting · for merge · checks passed" | "Needs you · Widget's plan needs your OK · 1 working · 1 done" |
| 3 | **Since you looked** (caption; the hub's `recap`; a tap scrolls to the round) | when there are unseen changes | "Since you looked: flights held · 1 question" | — | "Since you looked: 6 screenshots ready" |
| 4 | **Ask card** (`AskCard`, shared with `PendingCards`): the to-do's own asks live here; a run's ask is a door headed "From Widget ·"; same-kind asks merge. Buttons follow task cards P1: the secondary buttons share one row, the primary takes its own full-width line, then Later · Don't handle · Discuss… | when the to-do needs the owner | Which ryokan? (home) | — | From Widget · Plan awaiting approval (door) |
| 5 | **Chips** (due, remind, High, project, repeat, part of) + **+** | each chip when set; + on open to-dos | [Apr 3–9] [Kyoto trip] [+] | [High] [Weather App] [+] | [Fri] [Weather App] [+] |
| 6 | **Notes** (live text, no Edit mode; the mic dictates) / **What you asked** (the brief, 3 lines + More) + **Done when · 1 of 3 ›** | notes always (a one-line "Add Notes" placeholder); the brief when delegated | notes | brief + Done when | brief + Done when |
| 7 | **Persona can help** · [Break Into Steps] [Do It for Me] | an open to-do with no runs and no steps; ✕ hides it for that to-do | on a fresh to-do | — | — |
| 8 | **Steps** n of m: circle, text, who ("· you", "· Persona", "· Widget"); suggested steps dashed with [Keep Steps] [Edit]; the first 5, then **Show All N Steps** | when there are steps or suggestions | Choose dates ✓ · you … | Reproduce ✓ · Fix ◐ · Open change ○ | Widget ◉ needs your OK · Release notes ◐ · Screenshots ✓ |
| 9 | **Results**: chips with an outcome; a tap opens the in-app link card, a file preview or a review | when something was produced | [Flights · held until Fri] [Ryokan shortlist · 3] | [Code change · ready to review] | [Screenshots · 6] [What's New · draft] |
| 10 | **Conversation**: the latest 2 rounds, one line each, then **All N ›**; while the pane is open, one line: "Conversation · open beside →" | when any round exists after capture | ✓ | ✓ | ✓ |
| 11 | **Work details ▸**, one folded line for the Agents audience: Agents by title, role, computer and state with **Open Session**; code changes (number, repository, checks, merge state, **Review Changes**); stages; Done-when checks; workspaces; Earlier runs | only for code work or runs on a computer | never | "Work details · 1 Agent · 1 code change ▸" | "Work details · 3 Agents · 1 code change ▸" |
| 12 | **Docked composer** (`floatingFooter`, glass capsule, radius 32): **[To Persona ▾]** "Message about this to-do…" · mic · ↑ | open to-dos, always; it moves into the pane with its draft while the pane shows. Closed to-dos say "Ask about this to-do…", which never reopens them | To Persona | To Persona | To Release lead |
| ⋯ | Copy Link · Move to Project… · Someday · Decisions (the first five, then Show All, as `docs/design/DECISIONS.md` ("11-13 A to-do's Decisions show the first five")) · Chat Topic… · **Start an Agent on a Computer…** (Agents audience; New Agent prefilled) · Delete… | behind ⋯ | | | |

- **One primary action, chosen by state:** the ask's primary answer when the to-do needs the owner; **Mark Done** on
  Just finished; the tick otherwise. Nothing else is filled with the accent.
- **A new to-do shows five things** (title, + Add Date / Reminder…, Add Notes, Persona can help, the composer) where
  today it shows about nine. **Leaving the default page:** the four always-on plan rows, "Decisions · 0", "No notes
  yet", "No agents are running", the floating Hand to Agent and Done pills, "Projects · N" and the raw `cwd` fallback.

**Everyday, developer and multi-agent pages use one component and one order; only the facts differ.** The prototypes
draw the everyday page (`iphone/kyoto`) and the multi-agent page (`iphone/release`) at iPhone width. The developer page
adds Done when and Work details, and its default lines name no runtime, repository, path or PR number: "Code change"
stands for the pull request on the paper, and its number is inside Work details.

`spec/fixtures/todo-detail` pins this section's table; its README's "Judgement calls" settle where the section is silent
or differs from the prototype, such as notes beside the brief and the primary with several asks.

### 3.6 The conversation

**One timeline, two zooms.** Each to-do has one **Conversation**, stored once as typed entries on the to-do. On the
paper it shows the latest two rounds, one line each; in the pane, or the pushed page on phones, every round is open,
the latest at the bottom, with tool work still one summary line per run (super rule 2).

A **round** is one owner instruction or answer. Its header is the owner's exact words and its message state. Folded
under it: the steward's reply (≤ 3 lines, More); relay lines ("↗ Queued for Release notes · after this step"); one
summary line per run, live while the run works ("◐ Release notes · drafting What's New · 3 min"); the decisions and
results the round produced; scope changes with **Undo** ("Scope: + home-screen widget in the release notes · Undo").
Work nobody asked for (supervision, checks) forms one folded **Background** round per day. The hub groups entries into
rounds (`round_id`); clients only draw them.

**Channels in the pane.** The pane's header is a switcher; switching never animates and never nests a push. The
**steward** channel (the default, shown by name: "Persona ▾" or "Release lead ▾") is the to-do's conversation. **A run**
("Widget · Needs you"; Earlier runs folded) is that Agent's own session, drawn as Agents draws it; its pending cards sit
at its foot, which is their **home**, and its composer addresses the run directly. **Chat topic** (when a Lark topic is
bound) is a mirror: replies there arrive as rounds marked "from the chat topic", and the steward's replies post back.

**The steward** is the Pinned lead when the to-do's `todo_supervision.supervisor` is a lead conversation (one in
`Settings.Leaders`, a Pinned direction). Otherwise, including to-dos written by hand (which have no supervision row), a
side chat that planned the task, or a task leader's Agent (`machine/session`, which appears as a run with the lead
role), it is **Persona**.

- **When the steward is a lead,** a message goes into the lead's existing conversation as a turn carrying the to-do's
  id, and the lead's reply is recorded on the to-do as a reply entry.
- **When the steward is Persona,** the manager runs one model turn built from the to-do's snapshot and its last rounds,
  and writes the reply on the to-do. **This is a new mechanism.** The nearest precedent, the background supervisor's
  clean rounds (#7315, `supervise_round.go`), resumes one persistent conversation listed as "Background supervisor",
  reads the snapshot but not earlier rounds, and needs Claude Code 2.1.322 or later on the command line. The steward
  needs a fresh, unlisted call per message, so nothing leaks into chat lists (risk 2 in section 7.8).
- **Status questions** ("how's it going?") are answered from the snapshot without waking an Agent (#2124).
- **A message whose chip names a run** is forwarded by the hub verbatim, with no model turn.
- **Cost:** one model turn per steward-addressed message; none for relays.

**The addressee chip** is always visible inside the composer, on every send.

| Situation | Default chip | Menu |
| --- | --- | --- |
| No runs, no direction | **To Persona** (a context-keyed string: the same words ship as the forwarded-message header) | none (no ▾) |
| Under a Pinned direction | **To Release lead** (the lead's title, never a project's name) | Persona |
| Runs exist | the steward, which relays verbatim to the run that needs it (an open ask first, then the lead run) and says so | each active run by title, role and state ("Release notes · helper · Working"); never a runtime, computer or model |
| The pane shows a run | **To Widget** (direct; today's purple routing look) | the steward |
| After **Discuss…** on an ask | **Answer: Which ryokan? ×** | × returns to the steward. Ordinary messages never answer an ask by accident (`unified-entry.md` "Routing") |
| Paper opened from Agents | **To ‹that Agent›** | the steward |

The draft is `{text, attachments, to, mode}`, kept per to-do in a workspace model above the view tree. It survives
closing the pane, folding, resizing and relaunch, and is cleared only when the hub's receipt (Processing) arrives.

**Steer or queue.**

- **To a run that is mid-turn:** **After this step** (the default) queues the message. The queued line offers **Send
  Now** (shipped in the queue strip) only where the run supports it, and Send Now sends the whole queue
  (`docs/protocol/protocol.md` `queue/send-now`). In a terminal it does not stop the step: it is Claude Code's own
  send-now (a running command moves to the background and goes on; a reply being written is asked again with the
  message). In a Persona chat it is `interrupt` with `send_now`: the step it was on stops or, when it can, goes on in
  the background. Elsewhere the hub refuses with 409 `send_now_unsupported`.
- **Which runs support it is not on the wire today**: `SessionCapabilities` has no such field
  (`server/internal/proto/codex.go`), and clients guess (web `QueuePanel.tsx`, Apple `steersQueue`, Android
  `SessionSource.kt`). A new session capability, `send_now`, set by the node, replaces the guesses.
- **To the steward:** it decides, and its relay line says what it did. "Stop" and "wait" go now; scope and detail queue.

**Message states** (super rule 5; DESIGN_SYSTEM 4.3, said once: the round's status line is replaced as it advances):

| State | en | Status |
| --- | --- | --- |
| local echo | Sending | DESIGN_SYSTEM 4.3, decided (< 100 ms; the draft is kept) |
| the hub stored it | Processing | DESIGN_SYSTEM 4.3, decided; ≤ 1 s per principle 7 |
| taken by the addressee | **Delivered to Persona** / **Delivered to Release lead** | **new, a named addition to DESIGN_SYSTEM 4.3** |
| waiting for the step to end | **Queued for Release notes · after this step** [Send Now] | **new, a named addition to DESIGN_SYSTEM 4.3** (it extends the web's queued bubble) |
| execution | the run line appears under the round: "◐ Release notes · adding the widget" | no new word: the work itself shows it |
| failed | Not delivered · Retry (the text kept; a retry reuses `client_op_id`) | the web's shipped words (`not_delivered`, `delivery_failed`) |
| unknown | Not sure it arrived · Check (never resent automatically) | the web's shipped words (`delivery_unknown`); DESIGN_SYSTEM 4.3 already lists it |

**Where an instruction lands: one record, three effects.** (1) On the to-do, as the round's instruction entry: the
record, which outlives any run. (2) In the addressee's transcript, as a user turn carrying the to-do's id; Persona's
relays are folded "↗" lines, never a copy of the owner's bubble. (3) As to-do state, when it changes the to-do: scope,
dates, budget and Done when, written by the steward with provenance and **Undo**.

**Several Agents.** On the paper, parallel work is drawn as Steps with who has each, and an ask's door names its run
("From Widget"). Each run is one line in its round and one entry in the switcher. Child runs fold under their parent
and report results only. There are never chat tabs.

**Escalation, at most two taps each way, never leaving To-dos:**

| Step | Gesture | Opens | Back |
| --- | --- | --- | --- |
| 0 | An answer on the row or the ask card | nothing; the answer becomes a round | — |
| 1 | Type in the docked composer | nothing; the round appears in the paper's block | — |
| 2 | **Conversation** / a round / All N › / **Discuss…** | the pane (wide), or the pushed Conversation (phones) | ✕ / ‹ |
| 3 | A run in the switcher, a run's step, or **Review Plan ›** | that run's session in the pane (wide), or pushed (phones) | the switcher / ‹ |
| 4 | ⋯ › **Open Full Session** in a run channel | the Agent's full page (terminal, files, changes), pushed on the To-dos place's path | ‹ returns to the to-do with the pane on that run |

### 3.7 Attention: homes, counts and what reaches outside the app

**Homes.**

| Ask | Kinds | Home (the full card) | Doors (answer in place when safe) |
| --- | --- | --- | --- |
| **A to-do's own ask** | decision ("Which ryokan?"), Check the result, Didn't start, sign-in, steps proposed, possible duplicate | the paper's ask card, under the state line | the list row, the Assistant's Needs you row, the push (`TODO_DECIDE`), the pinned header on phones |
| **A run's ask** | question, plan, tool approval, secret, form | the foot of the run's session (DESIGN_SYSTEM 4.8, unchanged), shown in the pane's run channel or the pushed session | the paper's door "From Widget · …", the list row, the Needs you row, the push (the Agent categories), the Fleet Live Activity (as today). Choices and low-risk approvals answer in place; plans, secrets and high-risk actions show **Review ›**, which opens the home |
| A session's ask with no to-do | as today | its session | as today (the Assistant's Needs you "Elsewhere", Agents) |

- **Drawn once per screen.** While the pane, a pushed session or the pinned header shows a request's home, every door to
  it on that screen shrinks to one line with no buttons: "Plan awaiting approval · answer in the conversation →". This
  **adapts** DESIGN_SYSTEM 4.8's Team panel rule, under which the lead's dock leaves the request out entirely; To-dos
  keeps one line so the paper still says that something waits.
- **Three outcomes on every ask** (task cards P4): the answer; **Later** (not now, for this app session); **Don't
  handle** (declined, recorded, undoable).
- **An answer** shows in place as "✓ You chose Ryokan Hanami · Undo" for 4 s, then becomes a decision line in its round.
  It leaves every door at once; the first answer anywhere wins.

**Answers and taps per surface:**

| Surface | Safe ask | Plan / payment / high-risk |
| --- | --- | --- |
| List row | 1 (**OK: Ryokan Hanami**), or 2 via Options ⌄ | 1 to the home, then the answer |
| Paper ask card | 1 | **Review Plan ›** opens the run (pane or push), then **Approve**: 2 |
| Push (`TODO_DECIDE` for to-do asks; the Agent categories for run asks) | 1: up to 3 option actions, recommended first (**new**: today `TODO_DECIDE` has one action, Take Recommendation) | **Open** only, where the hub marks the ask `lockscreen: open_only` (a **new** field) |
| Fleet Live Activity (unchanged: it lists sessions) | a run's ask as today: buttons on the first actionable item | plans keep today's View / Approve (Face ID, `acceptEdits`) |
| The Assistant's Needs you row | 1 (task cards P2: the folded row opens in place) | opens the paper |
| Watch | notification actions; the `answer` role may answer **safe** asks (**new**: today it gets 403 on every to-do write, `docs/assistant/todos.md`) | Open on the phone |

**Counts: which number shows where** (one hub computation: `counts` on the list, `assistant_needs` for the Assistant).
The mock values are the prototypes' data (section 4.2).

| Number | Definition | Shown on | Mock |
| --- | --- | --- | --- |
| **N** | open to-dos whose phase is `needs_you` (Can wait excluded), each counted once however many of its runs ask | the sidebar or drawer **To-dos** row (blue, hidden at 0); the Needs you header in All; the Filter menu's Needs You line; under a choice that hides it, the guard line and, on iOS 26, the title's subtitle; the tray's "To-dos" group; the To-dos widget | 3 |
| A place's part of N | the same rule within a project | the project's sidebar row; a group header in By Project | Kyoto trip 1 · Weather App 1 · Home 1 |
| **The Assistant's Needs you** | N + M, where M = sessions needing the owner that belong to no to-do + the conversation's own cards + plans waiting for Start + drafts (DESIGN_SYSTEM 4.8) | the composer's Needs you entry, grouped "To-dos · 3" / "Elsewhere · 1" so the parts add up | 4 |
| Agents badge | sessions needing the owner (unchanged, `docs/design/DECISIONS.md` ("11-10 Needs You counts sessions")); a to-do's run counts here as the session it is | the Agents tab, the Agents list's Needs you section and filter | 2 |
| Fleet Live Activity header | sessions needing the owner (unchanged, `docs/apple/live-activity.md`) | the Lock Screen and the Dynamic Island | 2 |
| In progress | the rows of Working and Waiting; Just finished is listed at its foot but not counted (unchanged, #8237) | the Filter menu's In Progress line; the tray's In progress entry | 5 |
| App icon | unchanged (the tab badges added up, #5824); its double counting belongs to #5974 | — | — |
| Section counts | rows in the section in the current place; tinted only for Needs you | section headers, the Filter menu's lines | Working 2 · Waiting 3 · To do 2 · Just finished 2 |

One number changes, named in owner decision 4: the Assistant's Needs you counts each to-do once, however many of its
runs ask, and says so by grouping its rows. The web's separate Parked entry (#9325) folds into In progress's Waiting
rows, named in owner decision 3.

**The DESIGN_SYSTEM 4.8 amendment** (owner decision 4). Three edits, worded to sit beside the existing child-session,
Team panel and side-session paragraphs:

> **Insert after the side-session paragraph (4.8, before "One count, per session"):**
> **To-dos (#10051).** A to-do is the home of the requests the to-do itself raises: a decision, Check the result,
> Didn't start, a sign-in, proposed steps and a possible duplicate. Their card sits on the to-do's page under its state
> line. Its row in To-dos, its Needs you row and its notification are doors that answer choices
> and low-risk approvals in place. A request an Agent raises while working for a to-do keeps its home at the foot of
> that Agent's session. The to-do's page shows it as a door, "From ‹run title› · ‹request›", answerable in place under
> the rows rule. It is counted once under the to-do, as a child session's request is counted under its parent. While
> the to-do's conversation pane or a pushed session shows the run that owns the request, the page's door shrinks to one
> line, "‹request› · answer in the conversation →", so a screen never shows a request twice.
>
> **Append to "One count, per session":** The Assistant's Needs you counts each to-do once, however many of its Agents
> ask, and groups its rows "To-dos · N" and "Elsewhere · M". The To-dos row's badge, the To-dos list's Needs you
> section and the tray's To-dos group show N.
>
> **In the paragraph on the conversation's tasks' questions, replace** "one for each task or Agent that asks" **with**
> "one for each to-do, or Agent outside a to-do, that asks".

Nothing else in DESIGN_SYSTEM 4.8 changes besides that parenthesis: Agents' requests keep their home, tray rows open
homes and never copy a card, and the Agents count stays per session.

**Outside the app.**

- **Push.** Title: the to-do's title. Body: the named ask and the recommendation ("Which ryokan? Hanami is
  recommended"). Up to 3 actions, recommended first; the body opens the paper with the card in view. The hub sets
  `lockscreen` per ask: `answer` for safe choices, `open_only` for payments, merges, sends to other people and anything
  irreversible. One push per ask round, collapsed per to-do (as `TODO_DECIDE` is today). No push for a to-do that only
  finished, in line with #8237, which added no push for receipts or finished work.
- **Live Activity.** The phone keeps one hub-pushed Live Activity, Fleet (`docs/apple/live-activity.md`), unchanged: it
  lists the sessions that need the owner, at most two items with buttons on the first actionable one, then "+N". A run
  working for a to-do may show the to-do's title as its subtitle; a to-do's own asks reach the Lock Screen through the
  `TODO_DECIDE` notification above, not through Fleet.
- **Widget.** The To-dos widget lists Needs you first, then Today, and ticks in place as today.

### 3.8 Layout per device: the window rule

`todoPanes(window, insets, duoSideColumn, textScale, fold, wants) → [region: form, width]` is one pure function per
client, replaying the new `todo-layout` fixtures. Each region's form is *beside*, *over*, *pushed* or *hidden*.
Thresholds have 24 pt of hysteresis. Minimums scale with `scaledFrame` up to the xxxLarge cap, so larger text drops a
region by arithmetic; there is no accessibility-size branch (DESIGN_SYSTEM 6.2,
`scripts/test/test_apple_ax_branches.py`). Nothing is written while the scene is inactive.

**Widths:**

| Token | Value | Status |
| --- | --- | --- |
| `Theme.Size.sidebar` / `sidebarPhone` | 400 (iPad) / 320 (Duo) | existing |
| `todoList` | 320; 340 where the window's shorter side is 1032 or more; 300 in iPad windows of 600–759 | new |
| paper minimum | `Theme.Size.splitPageMin` (400); **320 on the Duo's inner display, for To-dos only** (owner decision 2) | existing + one new value |
| paper content | `Theme.Size.readable` (560) unless screenshots show the ask card and steps need more; a wider token needs that reason | existing |
| `todoConversation` | 400; 440 on the web | new |
| Duo inner portrait | each region takes its half of the crease (334 / 335), whatever the tokens above say | a rule, no token |

| Window | Conversation closed | Conversation open | Steps aside | Conversation form |
| --- | --- | --- | --- | --- |
| iPhone 402 × 874 | 1 page | 1 page, pinned header 92 | — | push |
| Duo outer 466 × 678 (382 + side column 84) | 1 page | 1 page, pinned header 92 | — | push, cross-fade |
| Duo inner portrait 669 × 951 | List 334 ┊ Paper 335 | Paper 334 ┊ Conversation 335 | the list | pane at the crease; each half cross-fades |
| Duo inner landscape 951 × 669 (867 usable) | List 320 │ Paper 547 | Paper 467 │ Conversation 400 | the list | pane, cross-fade |
| iPad mini portrait 744 | List 300 │ Paper 444 | pushed with the pinned header (400 + 400 > 744) | — | push |
| iPad 11" portrait 834 × 1194 | List 320 │ Paper 514 | Paper 434 │ Conversation 400 | the list | pane |
| iPad 13" portrait 1032 × 1376 | List 340 │ Paper 692 | Paper 632 │ Conversation 400 | the list | pane |
| iPad 11" landscape 1194 × 834 | Sidebar 400 │ List 320 │ Paper 474 | List 320 │ Paper 474 │ Conversation 400 | the sidebar (transient) | pane; a pure −400-pt shift |
| iPad 13" landscape 1376 × 1032 | Sidebar 400 │ List 340 │ Paper 636 | List 340 │ Paper 636 │ Conversation 400 | the sidebar (transient) | pane; a pure −400-pt shift |
| iPad window 600–759 | List 300 │ Paper ≥ 400, or one page | pushed with the pinned header | — | push |
| Web 1440 | Sidebar 284 │ List 360 │ Paper 796 | Rail 56 │ List 360 │ Paper 584 │ Conversation 440 | sidebar → rail | column |
| Web ≥ 1600 | Sidebar 284 │ List 360 │ Paper (the rest) | Sidebar 284 │ List 360 │ Paper ≥ 516 │ Conversation 440 | — | column |
| Android open foldable, book posture | List ┊ Paper at the hinge | Paper ┊ Conversation | the list | supporting pane |

`spec/fixtures/todo-layout` pins this table and its rule; its README's "Judgement calls" settle where the section is
silent, such as how the 24-pt band holds a region.

- **Per-place overlay.** While To-dos is in front, the sidebar lies over the page on windows where list and paper
  don't fit beside it: iPad 11" portrait, iPad 13" portrait and the Duo inner landscape. Today the rule is "beside or
  over by width alone" (`docs/apple/ios.md`), so this is owner decision 2 (iii). ☰ at the list's leading edge brings
  the sidebar over; a pick slides it away.
- **The keyboard.** With the composer focused on an iPhone, the paper folds so that the title, state and open ask stay
  above the keyboard: the bar shows the title with the state as a subtitle, the ask folds to one line (44 pt), and
  chips, notes and steps collapse into "Steps 2 of 5 · Results 2". Focusing the composer never opens the pane and never
  changes the layout (moving the first responder during a layout change makes the keyboard bounce).
- **Nothing selected on wide screens:** the paper region shows the **place summary**: "All To-dos · 12 open", then one
  tappable line per part in section order, "3 need you · 2 working · 3 waiting · 2 to do · 2 just finished", and last
  the uncounted "1 can wait". Just finished counts its closed, unseen row as its header does. Each line scrolls the list
  to its section: what needs the owner, runs, waits and finished, in one glance.

### 3.9 Motion

| Move | iPhone | Duo, both displays, every posture | iPad, web, Android regular | Reduce Motion |
| --- | --- | --- | --- | --- |
| List → paper | push from the trailing edge (system); Back pops on the same path | cross-fade in place, 0.2 s | not animated: a selection change; the row highlight moves | cross-fade |
| Open the conversation | push from the trailing edge | cross-fade in place; panes resize without sliding; on the inner portrait each half cross-fades, so nothing crosses the crease | one transition, `Theme.Motion.layout` 0.3 s: the pane enters from the trailing edge while the leftmost column leaves to the leading edge (iPad landscape: a pure −400-pt shift; elsewhere drawn from snapshots, as `SplitColumnControl` does) | cross-fade; panes resize at once |
| Close the conversation | pop (‹, edge swipe, a tap on the pinned header) | the reverse cross-fade | the exact reverse: same edges, same duration | cross-fade |
| Sidebar or list called back while the pane is open | — | slides over a still page (the drawer rule) | slides over, one width, `Theme.Split.overDim` 0.2 | fade |
| Switch place, select a to-do, switch channel, counts change | never animated | never | never | — |
| Ask answered | the card collapses (`Theme.Motion.state` 0.25 s) to "✓ You chose … · Undo" | same | same | fade |
| Tick | strike-through and a haptic; "Done · Undo" for 4 s, then the row leaves once the list is idle (`conceal` 0.2) | same | same | the row disappears |
| Hub re-files a row | waits until the list is idle, then fades (0.2) | same | same | instant |

- **One thing moves at a time**: the sidebar stepping aside and the pane arriving are one transition. **Every enter has
  its reverse** on the same edge, and Back returns to where it started.
- **The Duo's cross-faded pushes need a mechanism and a measurement.** Today only the shell's page switch cross-fades
  under side bars (`ShellPageSlide`, `docs/apple/ios.md`); pushes inside a navigation stack still slide. The build names
  a UIKit navigation animator for the To-dos stack and measures it on the Duo simulator before stage 2.
- **Loops:** the working spinner on a ◐ mark (paused off screen) and the needs-you breath of DESIGN_SYSTEM 2.7, as on
  Agents rows. Nothing else loops.

### 3.10 Accessibility

- **Panes are labelled containers**, read leading to trailing: "To-dos list", "To-do", "Conversation". When the pane
  opens, focus moves to its composer and the pane is announced; on close, focus returns to the Conversation toggle. The
  pinned header is one element ("Plan the Kyoto trip, needs you, Which ryokan?") with the actions Answer Ryokan Hanami,
  Options and Back to To-do. Inline answers and swipes are accessibility actions on the row.
- **Text size.** The pinned header caps at the title (1 line) plus the ask's name; its answers fold into Options ⌄ when
  they don't fit. Fitting uses `ViewThatFits` with each candidate wrapped in `MeasuredOnce` (`docs/apple/performance.md`
  P7), never a bare `ViewThatFits` in a row.
- **Keyboard on iPad and the web:** ⌘N New To-Do, ⌘F search To-dos, ⌃⌘I Show/Hide Conversation, ⌥⌘↑/↓ previous/next
  to-do (the pane stays), ⌘L show the list. The View menu lists Show Sidebar, Show List and Show Conversation (closes
  HIG-064 in To-dos). Web: Mod+B toggles the conversation, Mod+I focuses the composer, Esc closes only overlays.
- **Contrast:** needs-you text and captions on cards use the existing ≥ 4.5:1 colors (`StatusPalette.needsYou.text`,
  `Reply.secondaryInk` on Apple, and their web counterparts once the web's Needs you turns blue, DS-01).

### 3.11 Glossary (owner-facing words, en · zh-Hans)

Every string also ships in ja. "Shipped" means the pair already exists in the app; a shipped pair used in a new place
gets a context-keyed string. The other tables in this document give the English labels only; their zh-Hans words are
here.

| en | zh-Hans | Meaning | Notes |
| --- | --- | --- | --- |
| To-do / To-dos | 待办 | something the owner wants done; always the owner's | shipped; replaces "task" on screens (owner decision 6) |
| All To-dos · Today · Upcoming | 全部待办 · 今天 · 即将到来 | places | Today shipped; the others new |
| Project | 项目 | a knowledge-base area; a place | shipped |
| Pinned | 常驻 | a Pinned direction: a lead conversation and its to-dos | shipped; not a To-dos place (section 3.3) |
| All | 全部 | the Filter's landing choice | shipped (the Inbox's bar) |
| Filter · Show · Show All | 筛选 · 显示 · 看全部 | the list's state control, its menu's heading, and the way back to All | Filter and Show All shipped; context-keyed for To-dos |
| Recently Changed · Due Date · Priority (⋯ › Sort By) | 最近更新 · 截止日期 · 优先级 | the list's other orders inside each section, the old page's; not "Recent Activity", which would give Activity two meanings | new for V1, context-keyed (To-dos Sort) |
| Group by Project (⋯) | 按项目分组 | the list's one other grouping, below Needs you | new |
| Search To-dos · Search Chats for … | 搜索待办 · 在聊天里搜索… | the list's search and its last row | new |
| Chat | 聊天 | talking with Persona, on everyday screens | the one talk word (2026-11-21); Session / 会话 only in Agents and Work details |
| Needs you | 需要你 | only the owner can move it | shipped StateName; the direction tree's 等你 retires |
| Can wait | 可以晚点答 | for the owner but not counted (held asks, waiting for a reply) | shipped as the briefing card's header |
| Working | 在做 | Persona or an Agent is on it | shipped (the tree's word) for a to-do's phase; a session's state on Agents stays 工作中: different objects, different definitions |
| Just finished | 刚做完待看 | finished; not seen, or seen within 24 h | shipped on the web (`just_finished`) and the iOS To-dos section; Android's section reads 刚做完 and aligns; Apple and Android show Agents rows as Seen until #9751 lands there |
| Waiting | 等待中 | it waits on someone or something else | shipped |
| To do | 待做 | the owner's own; nobody works on it | the pair ships as a setup-step value; new as a section word |
| Someday | 以后再做 | the owner's own "not now" | new; never "Later" |
| Done | 已完成 | closed; says by whom | shipped; the Inbox's Done reads 已处理, a to-do's stays 已完成 |
| In progress | 进行中 | Working + Waiting, with Just finished listed (not counted) at its foot | shipped |
| Inactive (a mark) | 暂无进展 | no activity for 48 h; a mark, not a section | shipped on the web for the same rule; Apple's Shelved / 搁置 retires |
| Awaiting Reply · Reply | 待你回复 · 回复 | an Agent waits for the owner's plain-text reply | shipped StateName (web: "Awaiting your reply") |
| Didn't start | 没开起来 | a run failed to start; the to-do stays open | shipped StateName for a failed start |
| Start When It's Back · Use ‹computer› Instead | 上线后开始 · 改用 ‹computer› | the next actions after Didn't start | new; the button names the owner's other computer ("Use MacBook Instead") |
| Notes | 说明 | the owner's words on the to-do | shipped |
| What you asked · Done when | 你的要求 · 完成标准 | the brief and its criteria | new |
| Steps · Keep Steps · Suggested | 步骤 · 保留步骤 · 建议 | pieces of a to-do | new, context-keyed: "Steps" reads 步数 (health) today |
| Suggested (⋯ › Sort By) | 推荐顺序 | the list's default order inside each section: the hub's | new, context-keyed apart from suggested steps (建议) |
| Results | 成果 | what the work produced, with its outcome | new |
| Activity | 进展 | the to-do's timeline: its rounds, decisions and results, with the composer at its foot | new (2026-11-21, replacing "Conversation / 对话"); ships with the timeline (PRs 18 and 20) |
| Work details | 工作细节 | how code work is done (Agents audience) | new |
| Decisions | 决定 | recorded answers | shipped |
| Since you looked | 你上次看过之后 | the hub's recap | new |
| Persona can help · Break Into Steps · Do It for Me | Persona 可以帮忙 · 拆成步骤 · 帮我办 | starters on a fresh to-do | new |
| Add & Ask Persona | 添加并交给 Persona | capture with a hand-off | new |
| To Persona · To Release lead | 发给 Persona · 发给 Release lead | the addressee chip | "To Persona" ships in another place; context-keyed |
| Answer: … | 回答：… | the chip after Discuss… | new |
| Later · Don't handle · Discuss… | 稍后 · 不处理 · 讨论… | an ask's exits (task cards P4) | Later and Don't handle shipped |
| Sending · Processing | 发送中 · 处理中 | message states | decided (DESIGN_SYSTEM 4.3) |
| Delivered to X · Queued for X · after this step | 已送达 X · 排队等 X · 这一步做完后 | delivery states | new, named additions to DESIGN_SYSTEM 4.3 |
| Send Now | 立即发送 | send a queued message at once | shipped (queue strip) |
| Not delivered · Retry · Not sure it arrived · Check | 已确认未送达 · 重试 · 不确定送到没有 · 查看 | failed and unknown delivery | shipped on the web |
| Mark Done · Reopen | 标为完成 · 重新打开 | lifecycle | Reopen shipped |
| Review Plan · Open Session · Open Full Session | 查看计划 · 打开会话 · 打开完整会话 | doors to a run's home | Open Session shipped |
| New To-Do | 新建待办 | capture | shipped |

- **One label, one definition:** Someday ≠ Later; Waiting ≠ Needs you; Just finished ≠ Check the result (an ask);
  Activity ≠ Session.
- **Names avoided because they collide:** "Thread" (the Lark chat-thread binding and the hub hello feature
  `todo_threads`); "task" on screens (owner decision 6); "Ready" and "To Check" (the shared word is Just finished);
  "Open" as a section name (a to-do is open in every section but Done); "Quiet" for the 48-h mark (a quiet task is
  something else); "Couldn't start" (the shipped word is Didn't start).

Internal only (code and docs, never on screens): Paper, Pane, Spine, Place, Run, Ask, Home, Door, Steward, Round,
Phase, Section, Reason, Entry, Deliverable, Check, Channel.

---

## 4. Three prototypes

### 4.1 What each tests

The three variants share one kit, one data set and one harness, so the only differences the owner sees are each
variant's ideas.

| Variant | Spatial model | What it tests |
| --- | --- | --- |
| **V1 Paper and Pane** (recommended) | A sliding window: list, paper, then the conversation pane on the right; phones push the conversation with the to-do pinned | The owner's left → right idea, kept within the platforms' pane limits |
| **V2 Rounds** | One column everywhere. The paper carries the latest three rounds expanded; the whole Conversation and an Agent's session replace the to-do inside its own column; there is no conversation region | Whether a conversation region is needed at all (this concept scored highest overall, and declines the owner's central idea) |
| **V3 Spine** | Regions never move on their own. The to-do keeps a still spine (title, state, open ask) and its body switches between Paper and Conversation, or splits them side by side under **Focus** when the to-do region is at least 800 wide. Rows carry a "living mark" in the tick's slot | Whether a still to-do header beats a push or a sliding window, on phones and at every other width |

**V2 differs from V1** in these rules: the latest three rounds expanded on the paper (about 30% of it); no
Conversation toggle and no pane; sessions replace the to-do in its column (a hand-built column stack, because a
`NavigationStack` can't nest in the split's detail stack); only starred places in the sidebar; the composer's idle
placeholder names the addressee ("Tell Persona about this…"), and a label "To Persona ▾" stands above the field while
there is text; an "Agents" line for multi-agent to-dos; the sidebar never steps aside.

**V3 differs from V1** in these rules: the sidebar and list never step aside on their own; on phones the body slides
between Paper and Conversation under a still spine, and Back always pops to the list; Focus (⤢) splits paper and
conversation under one spine; the living mark replaces the tick (only ○ ticks on a tap; any other mark opens the to-do);
a crew line of runs on to-dos with two or more runs; the paper's conversation block is one "Latest" line.

### 4.2 How to open them

- **Gallery:** `docs/design/prototypes/todos/index.html` (open it from disk in Chrome; no build, no network). Pick a
  variant, switch light and dark, filter to key frames, or **Compare** one frame id across the variants side by side.
- **One frame:** `frame.html#v=V1&f=iphone/list`, at its device size. A state is a frame too
  (`frame.html#v=V1&f=iphone/list~calm`). The floating strip offers Light / Dark, Reduce Motion, Hotspots (dashed
  outlines on everything tappable), Reset, Fold / Unfold on Duo frames, and previous / next. Keys: `t` theme, `m` Reduce
  Motion, `h` hotspots, `←` / `→` frames, `g` the gallery. A tap on a hotspot moves to the next frame with the
  transition section 3.9 prescribes; a destination the prototype doesn't build shows a 2-s toast naming it.
  `tools/shoot-all.sh V1 <out dir>` shoots every frame in both appearances (the kit's `README.md` has the details).
- **Data:** every frame reads `data/todos.js`. Everything in it is made up. "Now" is Wednesday 18 November 2026, 10:14;
  the computers are MacBook and Desk Mac; the projects are Kyoto trip, Weather App and Home; one Pinned direction,
  Release lead, supervises the Weather App release. `Data.check()` compares every count with section 3.7's Mock column
and the list below.

| Section | To-dos (row line 2) |
| --- | --- |
| Needs you | T01 Plan the Kyoto trip (● Which ryokan? · Hanami recommended) · T02 Release Weather App 1.3 (● Widget: Plan awaiting approval) · T03 Sort last year's photos (Didn't start · Desk Mac is offline) |
| Can wait | T13 Renew the car insurance (Awaiting Reply · Persona asked about roadside cover) |
| Working | T05 Draft the school newsletter (◐ Persona is writing part 2 of 3) · T06 Get quotes for the garden gate |
| Waiting | T04 Fix login crash for SSO users (◷ For merge · checks passed) · T09 Update the privacy policy page · T10 Keep an eye on the app size after 1.3 |
| To do | T11 Renew passport · T12 Buy groceries |
| Just finished | T07 Book the dentist (open, unseen: ✓ Booked Tue 10:00 · check and tick) · T08 Fix widget refresh crash (closed, unseen: Done by Persona · merged and live) |
| Someday | T14 Learn to bake sourdough (folded) |

**Counts beyond section 3.7's Mock column:**

- Open = 12: T01–T07 and T09–T13 (T08 is closed, T14 is Someday).
- Elsewhere · 1 is an Agent opened by hand, "Tidy the settings code", asking "Allow running the test suite?"; with
  Widget it makes the Agents badge's 2.
- The Fleet header reads "2 need you": it counts sessions, as today (section 3.7).
- `Data.check()` in the browser console returns an empty list while every count matches.

### 4.3 Walkthrough: the same nine tasks in each variant

The tasks cover nine scenarios:

| # | Scenario |
| --- | --- |
| S1 | Everyday capture: "Renew passport, due Friday", typed or dictated, ticked later, with no Agent |
| S2 | Everyday delegated: "Plan the Kyoto trip"; Persona breaks it into steps and asks "Which ryokan?", answered in one tap from the row and from a push |
| S3 | Developer work: "Fix login crash for SSO users"; an Agent works, opens a code change and waits for merge |
| S4 | Several Agents: "Release Weather App 1.3", one asking for a plan approval, one working, one finished |
| S5 | Talk about it: a scope change while an Agent is mid-turn; who receives it, steer or queue, where it is recorded |
| S6 | Escalate: from a quick instruction to an Agent's full session and back, without losing the place |
| S7 | Triage at a glance: 12 open to-dos in three projects; what needs the owner, runs, waits and finished |
| S8 | Failure and waiting: a run that didn't start (computer offline), a to-do waiting for a reviewer's merge |
| S9 | Fold and unfold: the Duo closed → open mid-conversation, an iPad window resized; nothing lost |

Run the tasks on the iPhone first, then the Duo (outer, unfold, inner), then the iPad 11" landscape. Also check light
and dark on every key frame, Reduce Motion on every transition, and the largest interface text size (xxxLarge) on
iPhone and iPad.

| # | Task | What to watch | Measure |
| --- | --- | --- | --- |
| 1 | Open To-dos and say what needs you, what runs, what waits and what finished (S7) | the counts: sidebar (3) = bar (3) = header (3) = tray "To-dos · 3"; the Assistant shows 4 and says why | seconds to answer; any mismatch fails |
| 2 | Answer "Which ryokan?" from the row, then from the lock screen (S2) | one tap each; "✓ You chose … · Undo"; the count drops everywhere at once | taps (target 1) |
| 3 | Tell Persona "also check that breakfast is included" from the Kyoto paper (S5, short) | nothing opens; Sending → Processing → Delivered; the reply lands on the paper (V1, V3) or in the rounds (V2) | taps (target ≤ 3 from the list); is the to-do in view while typing? |
| 4 | Have a longer back-and-forth about Kyoto | V1: the pane on iPad, a push on iPhone. V2: the whole Conversation replaces the to-do. V3: the switch, or Focus | the owner's preference; does the paper stay visible? |
| 5 | Approve Widget's plan on Release 1.3 (S4, S6) and come back | V1: the pane's run channel on iPad (the door becomes a pointer); V2: the session in the column; V3: the run card, then the session | taps in and out (target ≤ 2 each way); Back lands where it started |
| 6 | Change the Release scope mid-turn: "also add the home-screen widget" (S5) | To Release lead; Queued for Release notes · after this step; Send Now; Scope · Undo | can the owner say who received it and whether it was acted on? |
| 7 | Find "Sort last year's photos" and do the one next action (S8) | Didn't start · Desk Mac is offline → Start When It's Back → Waiting | 1 tap; no "failed" word |
| 8 | Fold the Duo mid-draft, unfold, rotate (S9) | the draft, the selection and the open conversation survive | anything lost fails |
| 9 | Capture "Renew passport fri" and tick it later (S1) | the calm list; no Agent words | taps (target ≤ 4 to capture, 1 to finish) |

### 4.4 Key frames

Each variant marks eight key frames (★) in the gallery; V1 also marks two of their states, `iphone/kyoto~answered` and
`iphone/kyoto-quicknote-keyboard~reply`, and V2 one, `iphone/kyoto~rounds` (its rounds, one scroll below). Screenshots of every ★ frame, light and dark, are attached to the PR, not
committed (`docs/operations/collaboration.md` ("Screenshots in PRs")); `tools/shoot-all.sh` writes them outside the
checkout.

- **V1 Paper and Pane ★:** `iphone/list` (the list; a row answer; Didn't start's next action) · `iphone/kyoto` (the
  paper; the item comes first) · `iphone/kyoto-quicknote-keyboard` (a quick note from the paper's foot; nothing opens) ·
  `iphone/conversation-kyoto-keyboard` (the conversation pushed, the to-do pinned) · `duo-inner-portrait/list-paper`
  (list and paper at the crease) · `duo-inner-portrait/paper-conversation` (unfolded mid-conversation; the list stepped
  aside) · `ipad11-landscape/closed` (sidebar, list and paper) · `ipad11-landscape/open` (the sliding window; steer and
  queue).
- **V2 Rounds ★:** `iphone/kyoto` (rounds on the page) · `iphone/kyoto-keyboard` (the keyboard budget) ·
  `iphone/conversation-kyoto` (the whole Conversation, pushed) · `iphone/release` (the Agents line; a scope change on
  the page) · `duo-inner-portrait/list-todo` (the only two-pane state) · `ipad11-landscape/todo` (starred places in the
  sidebar) · `ipad11-landscape/session-in-column` (Review Plan opens the session in the to-do's column) ·
  `ipad13-landscape/long-conversation` (the cost of no pane: the paper is out of view).
- **V3 Spine ★:** `iphone/list` (living marks) · `iphone/kyoto-paper` (spine and paper body) ·
  `iphone/kyoto-conversation` (spine and conversation body) · `iphone/kyoto-conversation-keyboard` (the spine keeps the
  ask while typing) · `duo-outer/kyoto-conversation-keyboard` (the body cross-fades under the side bars) ·
  `duo-inner-portrait/list-conversation` (the list stays) · `ipad11-landscape/conversation` (nothing on the left moved)
  · `ipad11-landscape/focus-split` (the owner chose Focus).

V1 also draws `iphone/release`, `iphone/release-widget-session`, `iphone/capture-passport`, `iphone/lock-push`,
`iphone/assistant-tray`, `duo-outer/conversation-kyoto-keyboard`, `duo-inner-landscape/release-pane-on-widget`,
`ipad11-portrait/open`, `ipad13-landscape/summary`, `V1:web/open` and `android-fold/paper-conversation`; V2 and V3
draw their own. (Web frame ids carry their variant, the form the kit's links use, so they don't read as repository
paths.)

The gallery's Compare lines frames up by id, and the variants name some shared scenes differently, so this table pairs
them:

| Scene | V1 | V2 | V3 |
| --- | --- | --- | --- |
| The list | `iphone/list` ★ | `iphone/list` | `iphone/list` ★ |
| The Kyoto to-do | `iphone/kyoto` ★ | `iphone/kyoto` ★ | `iphone/kyoto-paper` ★ |
| A quick note, keyboard up | `iphone/kyoto-quicknote-keyboard` ★ | `iphone/kyoto-keyboard` ★ | `iphone/kyoto-paper~typing` |
| The conversation on iPhone | `iphone/conversation-kyoto-keyboard` ★ | `iphone/conversation-kyoto` ★ | `iphone/kyoto-conversation-keyboard` ★ |
| The conversation on the Duo's outer display | `duo-outer/conversation-kyoto-keyboard` | `duo-outer/conversation-kyoto` | `duo-outer/kyoto-conversation-keyboard` ★ |
| Two panes on the Duo's inner display | `duo-inner-portrait/paper-conversation` ★ | `duo-inner-portrait/list-todo` ★ | `duo-inner-portrait/list-conversation` ★ |
| Several Agents | `iphone/release` | `iphone/release` ★ | `iphone/release-paper` |
| A plan's session | `iphone/release-widget-session` | `iphone/release-widget-session` | `iphone/release-widget-session` |
| iPad 11" landscape, talking | `ipad11-landscape/open` ★ | `ipad11-landscape/session-in-column` ★ | `ipad11-landscape/focus-split` ★ |
| Web | `V1:web/open` | `V2:web/todo` | `V3:web/rail-split` |
| Android foldable | `android-fold/paper-conversation` | `android-fold/list-todo` | `android-fold/list-conversation` |

---

## 5. Owner decisions

Seven decisions, each saying what it replaces. **Decided on 2026-11-18:** 1 (a) V1; 2, all six parts; 3 (a), with All
first in the bar (section 3.3); 4 (a); 5 (a); 6 (a); 7 (a). Recorded in `docs/design/DECISIONS.md` ("11-18 To-dos is a
page per to-do with its conversation beside it").

**1. The shape of To-dos.**

- **Options:** (a) **V1 Paper and Pane**; (b) **V2 Rounds**; (c) **V3 Spine** (section 4.1).
- **Recommendation: (a)**, to be confirmed by walkthrough tasks 3–5 and 8. Keep (c)'s spine as the phone fallback if
  testers lose track of the to-do while it is pushed. Choose (b) only if the pane reads as "a second place".
- **Replaces:** the two to-do pages on every client (iOS `TodoDetailView` + `TaskDetailSheet`; web `TodoDetail` +
  `TaskPanel`; Android `TodoDetailScreen` + `TaskDetailSheet` / `TaskDetailPage`); the detour to Agents.

**2. Navigation and layout rules for To-dos.** One package; each part can be refused on its own.

- (i) **Places** under the sidebar's To-dos row, the title menu as the second way in, states as the bar (section 3.3).
  Alternatives: projects only, or only starred places (tested in V2).
- (ii) **Transient sidebar step-aside** on iPad landscape while the conversation is open; never saved, so it departs
  from "don't hide the sidebar by default" only while the pane is open.
- (iii) **Per-place overlay**: while To-dos is in front, the sidebar lies over the page where list and paper don't fit
  beside it (section 3.8). Today: beside or over by width alone.
- (iv) **The Duo's inner display**: two panes 50/50 at the crease, with a **320-pt paper minimum** for To-dos where
  `splitPageMin` is 400 elsewhere. Decide after screenshots on the Duo simulator in both appearances.
- (v) **From the main chat**, a to-do is **pushed** on the Assistant stack, and Back returns to the chat. It no longer
  opens in the task-details sheet (full screen, resting at 0.6; `unified-entry.md` "Task details"), because the paper
  has its own composer, attach sheet and links (one sheet at a time). Watching while chatting moves to the Live Activity
  and tray.
- (vi) **The web's conversation column has one width, 440.** Today's side panel is resizable (default 600) with a stored
  width. One width, as on every other client, keeps the window rule one pure function (section 3.8).
- **Recommendation:** yes to all six.
- **Replaces:** the page's four list controls (List or Calendar, due filter, grouping, the standing search capsule;
  2026-11-21: grouping is now the sections, with one Group by Project item in ⋯, and the capsule's job is the system's
  search field on the list);
  `splitPageMin` 400 for To-dos on the Duo's inner display; the task-details sheet for to-dos; the side panel's stored
  width, for To-dos.
- **Coordinate with** #9071 (a start page and user-arranged sidebar groups) and #9971 (Back rules on iOS and the Duo's
  side column).

**3. One partition, its order, its counts and its words.**

- **Options:**
  - (a) The hub computes `phase` (`needs_you`, `working`, `waiting`, `ready`, `todo`, `someday`, `done`; `done` exactly
    when closed) and a display `section`, used by the list, the tray, the direction tree and the widget. The order is
    DESIGN_SYSTEM 3.4's To-dos rule: **Needs you · Working · Waiting · To do · Just finished** (· Someday · Done
    folded). In progress counts Working + Waiting (5 in the mock), with Just finished as an uncounted group at its foot,
    as the tray shows it since #8237. Can wait under Needs you is listed, not counted, and holds held asks and Awaiting
    Reply. The bar has the Inbox's chips with the landing choice first (All · Needs you · In progress · To do · Done),
    landing on All, with the calm rule (amended 2026-11-21: drawn as a Filter at the list's foot, section 3.3).
  - (b) As (a), in the order the Agents list uses since #9751: **Needs you · Working · Just finished · Waiting · To
    do**, with In progress counting every row it lists (7 in the mock). One order on To-dos,
    the tray, the tree and Agents, at the cost of DESIGN_SYSTEM 3.4's rule and #8237's uncounted group.
  - (c) Four sections exactly as the tray has them, with Working, Waiting and Just finished as groups inside In
    progress.
  - (d) PR #9823's five peer groups (Needs you · In progress · Awaiting acceptance · Stopped unfinished · Parked).
- **Named changes in (a):**
  - The bar's differences from the Inbox's (section 3.3): it lands on All, which comes first (the landing choice
    first, as in the Agents list), To do takes FYI's place, Done keeps the to-do's word, and the calm rule hides it
    (amended 2026-11-21: drawn as a Filter at the list's foot, section 3.3).
  - The web's Parked entry (#9325) folds into In progress's Waiting rows; Parked is a reason, not a group.
  - A finished open to-do stays in Just finished while unseen and 24 h after it is seen, then files in To do ("Finished
    Tue · Mark Done"), never in Done. Today rule 9a files it in Done once seen
    (`docs/assistant/todo-status-sop.md` ("Decision table")).
  - A failed start is a run outcome: the to-do stays open as "Needs you · Didn't start", and after 48 h it files in To
    do with the Inactive mark. Today it ends done (`server/internal/assistant/todos.go:60`).
  - Automatic shelving becomes the Inactive mark, with restores on the hub instead of per device. The judge's
    auto-close policy is unchanged.
  - Someday joins Done on the row's one trailing swipe, an addition to
    `docs/design/DECISIONS.md` ("11-13 The Inbox swipes as on iOS"), whose swipe holds Done or Move Back.
  - Just finished's **Got it** (#6578, `PUT /api/v1/todos/{id}/seen`) leaves the row: opening the to-do sees it, and
    Mark Done closes it.
- **Words with (a):** Just finished stays the shared word; **Someday** is the owner's deferral and **Later / Don't
  handle** stay the ask's exits; Needs you has one zh-Hans word everywhere, retiring the tree's (section 3.11); Working
  is amber on every client.
- **Recommendation: (a).** It keeps the decided rules (DESIGN_SYSTEM 3.4's order, #8237's count, the Inbox's bar) and
  changes only what the redesign needs: one partition everywhere, a failed start that stays open, and Someday. Choose
  (b) if one order across To-dos and Agents matters more than finished work waiting below unfinished work. Decide it
  **before PR #9823 merges**, and take PR #6206's lifecycle (judge done → delivered, awaiting review; cancelled apart
  from done) as input.
- **Replaces:** the status rules' eight sections, #9823's five groups, the web's Parked peer, iOS's Shelved section, the
  tree's five task states; several names for "finished, not accepted"; failed-start-as-done; rule 9a; per-device
  restores; the Just finished row's Got it.
- **Coordinate with** #10047 (keep tasks open while an accepted review waits for the owner) and #9289 (parked tasks
  backed by evidence).

**4. Ask homes and counts (the DESIGN_SYSTEM 4.8 amendment in section 3.7).**

- **Options:**
  - (a) **The amendment** (section 3.7): homes on the paper for a to-do's own asks and in the session for a run's,
    with doors that answer safe asks; drawn once per screen; the Assistant's Needs you counts each to-do once ("To-dos
    · N / Elsewhere · M"); the To-dos badge is N; Agents keeps counting sessions; the app icon is unchanged. Outside
    the app: `TODO_DECIDE` with up to 3 option actions and a per-ask `lockscreen` gate; Fleet unchanged; the watch may
    answer safe asks.
  - (b) #5974's single number on every surface, the app icon included.
  - (c) Move run asks' home to the to-do too.
- **Fleet stays as it is** under (a): it keeps listing sessions (2 in the mock) with today's plan buttons (View /
  Approve with Face ID). A to-do's own asks reach the Lock Screen through `TODO_DECIDE`. The alternative, Fleet counting
  to-dos as the Assistant does (4 in the mock), would make the Lock Screen and the Assistant say one number but changes
  a shipped surface; it belongs with #5974.
- **Recommendation: (a).** (b) is a separate decision under #5974; (c) breaks the rule that Agents' requests have their
  home in the session, which Agents users rely on.
- **Replaces:** the To-dos badge's open count drawn in the needs-you look; "Answer in chat" routing into the origin
  conversation as a separate answer path; the watch's 403 on to-do answers.
- **Rolling upgrade:** the count change rides on the `assistant_needs` payload (7.3, Attention): a client counts each
  to-do once only where that payload is present, so old clients keep counting sessions until every supported manager
  sends it. `todo_asks` gates the answer route alone, which ships first (stage 2), so a manager that answers asks never
  claims a count unit it does not have (amended 2026-11-19, #10465).

**5. The to-do's own Conversation.**

- **Options:**
  - (a) **Store it on the to-do** (section 3.6): typed entries at two zooms; the steward is the Pinned lead or Persona,
    whose turn is a new, unlisted model call; verbatim relays to runs; both origin-only guards lifted
    (`tasks_follow.go:749`, `decision.go:364`; `tasks_start.go:239` already accepts the supervisor); queue by default,
    Send Now where the new `send_now` capability allows; Delivered to X and Queued for X as named additions to
    DESIGN_SYSTEM 4.3; Hand to Agent moves to ⋯ › Start an Agent on a Computer….
  - (b) A hidden Persona conversation per to-do, mirrored into an activity list.
  - (c) Persona as steward even under a Pinned lead.
- **Recommendation: (a).** (b) keeps two records, leaks into older clients' chat lists and holds a runtime process per
  to-do. (c) splits a direction's conversation in two.
- **Replaces:** "talking to a to-do" as routing through its origin conversation to its most urgent Agent; Hand to Agent
  as the default action; implicit, invisible queueing.
- **Depends on** PR #6924 (closing a to-do keeps a usable conversation).

**6. "To-do" as the one owner-facing noun.**

- **Options:** (a) screens say "to-do" where they say "task" today (hand-off cards, tray rows, task details); "session"
  stays the Agents word for an Agent's run; "task" stays a code and wire word. (b) Keep "task" for handed-out work.
- **Recommendation: (a).** **Replaces** the clause of
  `docs/design/DECISIONS.md` ("11-10 An Agent's run is called a session") that "task" names the work Persona hands out,
  for screen words only.

**7. Where to-do status is computed.**

- **Options:** (a) **the hub's manager** computes phase, section, reason, line, rank, counts, places and rounds; clients
  draw them with a small optimistic overlay; the Rust core carries cache, deltas, overlay and outbox (section 7.5).
  (b) The client-side Rust core computes to-do status: a new scope, since no core plan covers to-dos (its phase 3
  "attention" is the Agent sessions' Needs you) and the plan keeps triage on the Go hub; the web, watch, widgets and
  the hub's own tree would still need copies.
- **Recommendation: (a)**, to be agreed with the core plan's owner (#6793). The hub's copy already passes the shared
  triage fixtures.
- **Replaces:** the three client copies of the status rules (TypeScript 353, Swift 432 and Kotlin 389 lines: 1,174) and
  HarmonyOS's own six-state view. The Go copy (612 lines) is not replaced; it becomes the one projection.

---

## 6. Domain model (for implementers)

### 6.1 The aggregate

```
Todo (root: the owner's intent)
  id · rev · title · notes · kind (omitted | concern) · project · schedule {due, remind, someday_until}
  priority · part_of / after[] · lifecycle {open | closed {outcome, by, reason, at}} · seen_at
 ├─ Brief        immutable "What you asked" + Done when; scope changes are entries with provenance
 ├─ Steps        child to-dos (part_of, one level); roll up to the parent, counted once
 ├─ Run          an Agent session working for it: title, role {lead, worker, helper, reviewer},
 │               start {started | failed(code)}, end reason; the session's capabilities (incl. the new send_now);
 │               its transcript and cards live in the session
 ├─ Ask          kind {decision, verify, retry, signin, steps_proposed, duplicate |
 │                     question, plan, tool, secret, form}
 │               home {todo | run}, round, risk {safe | open}, lockscreen {answer | open_only},
 │               can_wait, default_at, state {open, answered, expired, withdrawn} → one Decision
 ├─ Decision     append-only; provenance; supersedes
 ├─ Deliverable  kind {booking, doc, file, link, change, deploy, report}, ref, outcome,
 │               facts {state, checks, observed_at}   (the code facet lives here)
 ├─ Check        verdicts against Done when (internal; surfaces as Just finished or an Ask)
 └─ Activity     todo_activity: rev, round_id, type, actor, at, data   = the Conversation, both zooms
Steward (derived): the supervisor when it is a Pinned lead conversation, else Persona
Row (the hub's read model): phase · section · reason · line {key, args} · since · next_change_at · rank
                      · seen · inactive · can_wait · asks[] · lead_run? · steps {done, total}
                      · results[≤ 3] · has_work_details · steward {kind, name} · recap?
```

Notes against today's code: `kind` is `concern` or omitted, and stays so. Run roles map from `TodoAgent.source`:
`leader` → lead, `worker` → worker, `review` → reviewer, `spawned` → helper (a child run). The session already carries
its to-do (`Session.todo`). The stale mark is `inactive` (the time activity stopped; absent while active), because
`quiet` already means a quiet task, which clients read (no pushes, Live Activity or badges, `todos.go:72-75`). The
entries table is `todo_activity`, because `todo_events` names the extension-event code. `rev` and `If-Match` are new.

### 6.2 Invariants (each checked by fixtures)

1. `phase = done` exactly when the lifecycle is closed; `section` may show closed, unseen work in Just finished for
   24 h.
2. Every change appends exactly one activity entry in the same transaction and bumps `rev` (deltas, the timeline and
   extension events depend on it).
3. An owner message is stored once, as an instruction entry; the run's transcript gets a turn carrying the to-do's id.
4. Each ask has one home, at most one open ask per (run, kind) and per round, and exactly one Decision when answered.
5. Only the owner, the judge and a Check close a to-do; an Agent's words never do; a failed start never does.
6. A step's ask makes its parent need the owner, counted once. Tapping a step opens its own paper.
7. A relay that names a run is a verbatim forward with no model turn. The steward's own reply is a reply entry.
8. An unknown delivery is never resent automatically (`docs/protocol/protocol.md`); a retry reuses `client_op_id`.

### 6.3 How the phase derives

The first row that holds wins, except row 13, which only sets a mark and lets the rows below place the to-do. The last
column names today's rule in `docs/assistant/todo-status-sop.md` ("Decision table"); every row marked new or changed is
new on the hub.

| # | When | Phase | Section | Reason | Today |
| --- | --- | --- | --- | --- | --- |
| 1 | A run failed to start, within 48 h | needs_you | Needs you | `failed_start` | rule 1, changed: the to-do stays open; after 48 h rows 13 and 14 file it in To do with the Inactive mark, never in Done |
| 2 | An Agent asks (question, plan, tool), was cut off by a model error, or stopped unfinished | needs_you | Needs you | `agent_asks` / `interrupted` / `stopped_unfinished` | rule 2; `stopped_unfinished` new (#9823's group) |
| 3 | The owner decides (a decision, a possible duplicate); Check the result blocks; steps proposed; a sign-in | needs_you | Needs you | `owner_decides` / `verify` / `steps_proposed` / `signin` | rule 3; `verify`, `steps_proposed` and `signin` new |
| 4 | An ask marked can_wait; a run ended on a plain-text question | needs_you + `can_wait` | Can wait (not counted) | `can_wait` / `awaits_reply` | new |
| 5 | Persona or an Agent is running, starting, queued or working in the background | working | Working | `persona_working` / `agent_running` / `starting` / `queued` / `background` | rule 4; Persona's own turn, `queued` and `background` new |
| 6 | Closed, unseen, within 24 h | done | Just finished | `closed_unseen` | rule 5 |
| 7 | Closed, and seen or older than 24 h | done | Done | `completed` / `canceled` / `reported` / `superseded` | rule 5's "then Done"; the outcome reasons new |
| 8 | Open and parked | waiting | Waiting | `parked` | rule 5a, web only today; the hub adds it, reading the supervision ledger's `Parked` (`server/internal/assistant/supervise.go`) |
| 9 | Open, and accepted (even when the hub reopened it), deployed, or finished with nobody having seen it; or one of these seen within 24 h | ready | Just finished | `finished_unseen` / `finished_recent` | rules 6–8; "24 h after seen" new |
| 10 | Waiting on a PR to merge, a usage limit, a computer that is offline after Start When It's Back, a scheduled report, or someone else | waiting | Waiting | `pr_open` / `usage_limit` / `computer_offline` / `scheduled_report` / `someone_else` | rule 9; the other four new |
| 11 | Finished, seen more than 24 h ago, still open | todo | To do | `finished_seen` ("Mark Done") | rule 9a, changed: today Done once seen |
| 12 | The owner chose Someday | someday | Someday | `deferred` | new |
| 13 | No activity for 48 h | unchanged | unchanged, with the Inactive mark | `inactive` set | rule 10, changed: a mark, not the Shelved section |
| 14 | Anything else | todo | To do | `rest` | rule 11 |

- **Closed work comes first** (rows 6 and 7, today's rule 5), so an accepted or deployed to-do that is open again
  reaches row 9 as `ready`, never `done` (invariant 1).
- **Parked stays above finished work, as today** (rule 5a precedes rules 6–8): a parked to-do waits on someone else
  whatever its Agent last said. An open PR still comes after finished work, as rule 9 does today.
- **Rule 1a (superseded)** retires with related-row merging (section 6.4); a to-do closed as a duplicate ends with the
  reason `superseded` in row 7.

### 6.4 Concept mapping

About 77 concepts hang off the to-do today. Their fate:

- **Kept** as the root and its parts: the manual to-do; completion and reopen; the brief; the scope log; decision
  records; concern (a kind); the chat-thread binding (a mirror channel of the Conversation); due, remind, priority; the
  knowledge-base project (a place); seen; hand-off cards (one card in the chat, #8237). They become the To-do, Brief +
  Done when, Decision, `kind`, Chat topic, chips, the Project place and `seen_at`.
- **Merged** into one concept: task → a to-do with a brief and a steward; the Agent link → **Run**; lineage,
  continuation and successor → a new run on the same to-do; please verify, owner decision, Don't handle / Later,
  answering in chat, question to-dos → **Ask** with one answer route; parked → Waiting with evidence; pieces and
  direction pieces → **Steps**; capture provenance → `captured_from`; filter and sort → places + bar + one grouping
  menu; delivery and PR facts → **Deliverable** facts.
- **Derived** on the hub, into the row read model: runtime state (4 copies) → `phase` + `reason`; start remedy and
  queue → reasons; "stopped, not done" → a reason; the pending-review clock → `since`; outcome extraction → Results and
  decisions at close; orphaned → nobody working; the calendar → Upcoming › Show as Calendar; the direction tree → reads
  `phase`; Agent reports → the run's latest line; session decoration, By To-do, briefing and the snapshot index → read
  the row.
- **Moved** behind Work details (code work only), to Agents or to Machines: whole-project (space) links → "workspace
  link"; spawned following → child runs; task leader / worker → run roles; the stage strip; repository associations;
  Hand to Agent → ⋯ › Start an Agent on a Computer…; Agents › By To-do stays; the launcher-repair to-do → Machines.
- **Policy**, not data on the to-do (settings and ask attributes): automatic completion (the judge); the supervision
  ledger, review verdicts and the owner-wait guard → Check (internal); independent review and spot checks; decision
  deadline and can-wait → ask attributes; quiet → a notification policy; the background supervisor → Background rounds;
  the "Agent tasks" switch.
- **Retired:** a failed start stored as done; Shelved / Inactive as a section and per-device restores (→ the Inactive
  mark and hub restores); related-row merging (→ a "possible duplicate" ask at capture); reopening accepted tasks by
  sending; the `todo_ask_` id prefix; the List or Calendar toggle; "Got it" (opening a Just finished to-do sees it); the
  four always-on plan rows.

---

## 7. Architecture convergence (for implementers)

The convergence targets of this proposal; C1–C12 come from reviewing today's architecture, and C13–C15 are what the
redesign adds:

| Id | Target | Id | Target |
| --- | --- | --- | --- |
| C1 | one server projection (phase, section, reason, line, rank, counts) | C9 | a detail contract (`todo-detail`) |
| C2 | one item shape for list and item | C10 | one vocabulary (section 3.11) |
| C3 | deltas instead of whole-list reads | C11 | to-do rows in the launch snapshot |
| C4 | one schema for the wire types | C12 | guards (section 7.7) |
| C5 | a module boundary: a `todo` package in slices | C13 | the to-do's timeline |
| C6 | device-independent marks (seen, restores) on the hub | C14 | messages to the to-do and its steward |
| C7 | settings blobs moved to tables | C15 | the asks table with one answer route |
| C8 | idempotent writes (op ids, revisions) | | |

### 7.1 Module boundaries

- **Manager:** a new `todo` package in the assistant manager, in slices: `domain` (the aggregate, invariants and
  validation; no IO, no clock); `projection` (phase, section, reason, line, rank, counts, places; moved from
  `todo_triage.go` with its fixture tests first); `timeline` (`todo_activity` appends with an outbox, round grouping,
  and in the interim a timeline derived over today's stores); `asks` (one lifecycle and answer route over today's
  `acceptance`, reviews, dismissals and `ask_todos`); `steward` (resolution, verbatim relay, the model turn, scope and
  decision writes with provenance); `store` (a narrow interface, operations named by intent, one transaction each).
- **Archguard:** `movedClusters` rows (`server/internal/archguard/split_clusters_test.go`) stop moved files from coming
  back. A new rule in the settings-key scanner (`server/internal/hub/store_tables_guard_test.go`) bans new `assistant.*`
  settings keys for to-do data.
- **Hub:** a communication center. It merges per-manager lists and cursors as it does for the Inbox
  (`server/internal/hub/ownership_lists.go`). Summing per-manager counts and places is **new merge logic**: today merged
  lists take `triage` and similar fields from the default manager, and the direction tree is the default manager's
  alone. The hub only sums; it never computes a phase. `server/internal/hub/architecture_guard_test.go` pins the new
  routes as relays (`assistantwire.Routes`, `testdata/route_scopes.txt`).
- **Prompts:** the steward's turn lives in `server/internal/prompts/`, one prompt per file, with golden tests; its
  status-answer and relay templates are data, never `fmt`.

### 7.2 Wire contract

- **Schema:** a new `todo-view.schema.json` in `schema/wire/`, named apart from `schema/extension/todos.schema.json`.
  It defines `TodoSummary` (the row and its `view`), `OwnerAsk`, `TodoPlace`, `TodoCounts`, `AssistantNeeds`,
  `TodoTimelineEntry`, `TodoRound` and `TodoMessageState`. The obvious names are taken (`TodoRow`, `Ask`, `TodoAsk`,
  `Place`, `Counts` and `MessageState` are declared in Swift or TypeScript today), and `test_wire_schema.py` fails on a
  wire name any tracked Swift or TypeScript file declares.
- **Generation:** `scripts/gen/extension_schema.py` emits only TypeScript and Swift for `schema/wire` today. Go,
  Kotlin and ArkTS outputs are new work: Kotlin exists only for `schema/extension`, and the repository's only ArkTS
  generator is the Rust core's typegen, which works from Rust types. `test_wire_schema.py` scans only Swift and
  TypeScript and grows to Kotlin, ArkTS and Go; each schema needs fixture samples.
- **First step:** `view` and `OwnerAsk` only, generated before the first client ships, so the first UI wave does not
  re-create the five-way hand copy.
- **Fixtures** (new, under `spec/fixtures/`), replayed by every client: `todo-view` (hub facts → row; extends `triage`);
  `todo-detail` (row → the visible elements of section 3.5, presence and order only); `todo-rounds` (entries →
  rounds); `todo-overlay` (the optimistic table: done, reopen, seen, someday, answer, send states); `todo-counts` (one
  list → every number in section 3.7); `todo-layout` (window → regions, per client, never in the core).

### 7.3 Routes, pushes and capabilities

Capabilities are per manager (`manager.hello`, `server/internal/assistantwire/bus.go`); the hub lists one only while
every manager names it, as it already does. `todo_threads`, by contrast, is a hub hello feature. Every new push type
joins `managerMessageTypes` (`server/internal/hub/bus_broadcast.go`), or `TestBroadcastTypes` fails.

| Need | Shape | Gate | Target |
| --- | --- | --- | --- |
| Rows | `GET /api/v1/todos[?place=]` → rows with `view` + list `counts`; heavy fields left out of done rows (`chain=last` style) | `todo_view` (per item: a row with `view` is drawn from it; one without falls back) | C1, C2 |
| Item | `GET /api/v1/todos/{id}` → row + brief, steps, results, runs (title, role, state, capabilities), the latest 2 rounds, recap | `todo_view`; the rounds, recap and results with `todo_timeline` (#10465: they come from the timeline's stores, so the timeline's gate) | C2 |
| Places | `GET /api/v1/todos/places` → `[{id, kind, title, needs_you}]` + counts; push `{"type":"todo_places","rev"}` | `todo_places` | C1 |
| Timeline | `GET /api/v1/todos/{id}/timeline?before=&limit=` → rounds grouped by the hub; push `todo_timeline {todo, entries}` only to clients that have the to-do open, in stage 3 (#10465: stage 2 has no push of its own; clients read again on `{"type":"todos"}` and on an `assistant_feed` whose item is about the to-do). Manager pushes reach every app connection today and the only subscription is per session, so a per-to-do subscription is new hub work | `todo_timeline` (stage 2: derived over today's stores; stage 3: `todo_activity`) | C13 |
| Messages | `POST /api/v1/todos/{id}/messages {client_op_id, text, to: steward or run:<id>, mode: queue or now, answers?: ask_id}` → `202 {message_id, state: processing}`; push `todo_message {message_id, state: delivered, queued, failed or unknown, to}` | `todo_messages` (without it: today's origin route where allowed, else the main chat with the route chip) | C8, C14 |
| Send Now support | a session capability `send_now`, set by the node; the client heuristics retire | per session | — |
| Asks | `POST /api/v1/todos/{id}/asks/{ask}/answer {option, response, text or outcome: later, decline or undo}` with the op id in `X-Persona-Op-Id`, as every to-do write (Writes); a stale round → 409; `/accept` and the other answer routes kept for clients without it | `todo_asks` (the route alone) | C7, C15 |
| Steps | `POST /todos/{id}/steps:suggest` (nothing kept until Keep); `POST /todos/{id}/steps` (add, reorder, tick) | `todo_view` | — |
| Writes | `PATCH /todos/{id}` and `PUT` / `DELETE …/seen` (existing); `…/someday` and `…/restored` (new routes); all with `X-Persona-Op-Id` and `If-Match: rev`. `client_ops` covers no to-do route today, so the to-do kinds are added | `todo_view` | C6, C8 |
| Delta | the Inbox's precedent (#4740): `GET /todos?since=<cursor>` → `{as_of, cursor, delta: true, items, removed: [{id, manager}], …}`; the bare `{"type":"todos"}` push stays; a cursor the hub can't follow gets the whole list | none: an older hub answers without a cursor and the client falls back | C3 |
| Attention | `assistant_needs {todos: N, elsewhere: M, groups}` (not `attention`, which is Persona Link's Needs-you summary); `TODO_DECIDE` with up to 3 option actions and `lockscreen` | the payload itself: the per-to-do count where `assistant_needs` is present, the options and `lockscreen` where the push carries them | — |

**Rolling upgrade.** Every field is additive. Old clients keep every old field (`done_at` and `done_by` filled from the
lifecycle, `start_error` from the run outcome) and the bare `{"type":"todos"}` push. On an old hub the new routes are
not there: `GET /todos/{id}` and `GET /todos/places` answer **405** (the method-routed `PATCH`/`DELETE /todos/{id}`
matches them), the others 404, and clients treat both as "not there" and hide the entry. The client triage copies stay
as the frozen fallback until every supported manager advertises `todo_view`; then they are deleted behind a guard.

### 7.4 Client feature folders

- **iOS, iPadOS, Duo (the reference):** a new `Features/Todos/` folder in the app target.
  - `TodoWorkspace` (`@Observable`, above `RootView`): place, selection, conversation open, channel, drafts by to-do
    with addressee and mode; written only while the scene is active.
  - `TodosListModel`, `TodoPaperModel`, `TodoConversationModel` (`Deps` + `FakeHub`); `todoPanes()` (pure, replaying
    `todo-layout`).
  - A custom pane container in the split's detail column: no nested `NavigationSplitView`, no `.inspector` width range
    (one width), and not the system's arrangement view, which must not hold navigation.
  - Value-based pushes only (the classic drawer's view-based fallback for To-dos converts first); `.id(todo.id)` on
    paper and pane.
  - Components `TodoRow`, `AskCard` (shared with `PendingCards`), `AskPointer`, `RoundRow`, `AddresseeChip`,
    `PinnedHeader`; the composer through `floatingFooter`; a UIKit cross-fade animator for the To-dos stack under the
    Duo's side bars, measured first.
  - `TodoDetailView`, the to-do role of `TaskDetailSheet` and `TodoHandoffSheet` as the default path retire.
- **Web (desktop too):** a new `features/todos/` folder in the web app: `api` (generated types), `model` (row cache,
  optimistic overlay with op ids), `ui` (list, paper, the pane on `SidePanel` mechanics at one width: today's
  `SidePanel` is resizable with a stored width, so To-dos fixes it), `layout` (container queries + `todoPanes` in
  TypeScript). URL state `/todos/<place>?id=&talk=1`. `Todos.tsx` retires in slices and joins the lint file list.
- **Android:** `:feature:todos` out of `:feature:agents`; Navigation 3 `ListDetailSceneStrategy` plus
  `SupportingPaneSceneStrategy` (in the pinned adaptive library, not used yet); the Glass style first, with screenshot
  review (#4194).
- **HarmonyOS:** draws `view`; its own six-state set retires.
- **Watch, widgets, Live Activity:** read rows, places and `assistant_needs`; Fleet is unchanged (a run may name its to-do); the
  watch's `answer` role answers safe asks.

### 7.5 Rust core alignment

The core (#6793) covers iOS, Android and HarmonyOS; the web and the Go hub are outside it, and the watch, widgets, Live
Activity and Persona Link don't link it (`docs/plans/plan-rust-core.md`). No core plan mentions to-dos today, and phase
4's launch snapshot holds machines, sessions, interactions, the assistant, managers and picks, but no to-dos
(`docs/plans/plan-rust-core-phase4-storage.md`). The proposal adds to-dos to the core's scope where they fit it: the
mobile row cache and delta apply (phase 2, sync); the outbox for messages and answers and the optimistic overlay
(`todo-overlay`); to-do rows in the launch snapshot (phase 4, C11). The core never triages (the plan keeps triage on
the hub), never lays out (the core is client logic without UI), and never groups rounds.

### 7.6 Staged order

| Stage | Hub and manager | Clients | Targets | Unlocks (V1 frames) |
| --- | --- | --- | --- | --- |
| 0 | This proposal, the owner decisions and the glossary; the fixtures first (`todo-view`, `todo-detail`, `todo-counts`, `todo-layout`). Decide owner decision 3 before PR #9823 merges; take PR #6206's lifecycle as input. Docs only | the prototypes V1–V3 | C9, C10 (spec) | all, as mocks |
| 1 | Additive: one item shape and `GET /todos/{id}`; `view` from `todo_triage.go` moved into the projection slice, with the parked rule added, shadow-compared on the web against the fixtures; `places` and `counts`; restores and seen on the hub; the schema for `view` + `OwnerAsk` with Go, Kotlin and ArkTS outputs | the web draws `view` behind a debug shadow | C1, C2, C4, C5, C6 | the data behind the list, rows, counts, places and summary, drawn on iOS from stage 2 (`iphone/list`, `ipad11-landscape/closed`, `ipad13-landscape/summary`) |
| 2 | A timeline derived over today's stores (decisions, feed items with `ref`, reviews, runs) behind `todo_timeline`; the asks route over today's stores (both origin guards lifted for answers) behind `todo_asks`; `TODO_DECIDE` option actions and `lockscreen`; the Duo animator measured | **iOS reference:** places, list, paper, ask cards and doors, the docked composer routing through today's route where allowed, and the pane showing today's run view plus the derived timeline. **The two to-do pages merge here** | C9 | paper, run session, pane on a run (`iphone/kyoto`, `iphone/release`, `iphone/release-widget-session`, `duo-inner-landscape/release-pane-on-widget`, `iphone/lock-push`) |
| 3 | `todo_activity` with an outbox (C13); the asks table (C7, C15); messages and the steward (its model turn, verbatim relay) behind `todo_messages`; message states; the per-to-do timeline subscription; the `send_now` session capability; the to-do count unit, carried by `assistant_needs`; client op kinds for to-do routes (C8). Requires PR #6924 | iOS: the steward Conversation in the pane and pushed; receipts; steer and queue; the tray's to-do grouping | C7, C8, C13–C15 | quick note, Conversation, the scope change mid-turn (S5), the to-do count unit (`iphone/kyoto-quicknote-keyboard`, `iphone/conversation-kyoto-keyboard`, `duo-outer/conversation-kyoto-keyboard`, `duo-inner-portrait/paper-conversation`, `ipad11-landscape/open`, `iphone/assistant-tray`) |
| 4 | Deltas in the Inbox's shape (C3); the places push | the web, then Android (Glass) and HarmonyOS, from the same fixtures; the web writes optimistically | C3, C8 | `V1:web/open`, `android-fold/paper-conversation` |
| 5 | The rest of the module slices (C5); the `todo_stages` settings blob → a `todo_progress` table (C7); the guards below (C12) | the core adds to-do rows to the launch snapshot (C11); the client triage copies, the second detail pages, the List or Calendar toggle and the Hand to Agent default are deleted | C5, C7, C11, C12 | — |

Stages 1 and 2 are additive and ship visible value, the merged page, before any new storage.

### 7.7 Guards

- **Payload budget** for the list response, so done rows stay slim.
- **Settings-key ratchet**: no new `assistant.*` keys for to-do data (a rule in the existing scanner).
- **`movedClusters` rows** for every slice moved out of the assistant root package.
- **No client triage** outside the frozen fallback file, and none at all once every manager advertises `todo_view`.
- **No runtime words in everyday fixtures**: the `todo-detail` fixtures for everyday to-dos contain no engine, runtime,
  model, path, repository or session-id word.
- **Hand-written copies banned** for every wire type, in every client language the generator emits.
- **No accessibility-size branch** (`scripts/test/test_apple_ax_branches.py`) and the token ratchets, as for every
  Apple screen.

### 7.8 Risks and how the plan limits them

1. **The steward hop slows developers or blurs who read a message.** Relays naming a run take no model turn; Delivered
   and Queued are measured apart (design targets: Processing ≤ 1 s, Delivered ≤ 3 s at p95); the chip goes direct in
   one tap, and a to-do opened from Agents defaults to that Agent.
2. **The steward's model turn is a new mechanism** (section 3.6): a prompt with golden tests, measured in stage 3, and
   gated by `todo_messages`, so today's route keeps working until it ships.
3. **The list steps aside while the owner talks at mid widths** (V1's main risk). ⌥⌘↑/↓ moves between to-dos with the
   pane open, and "‹ List" lays the list over. V3 tests the alternative where nothing on the left moves.
4. **Collisions with in-flight work** (#6206, #9823, #9289, #6924, #10047): the partition is decided before #9823
   merges, the lifecycle comes from #6206, and the conversation waits for #6924.
5. **The Duo's 320-pt paper and cross-faded pushes are unmeasured.** Both are tried on the Duo simulator in both
   appearances before stage 2; if either fails, the inner portrait falls back to the sidebar-over-page rule with one
   pane plus a push.
6. **Changes to shipped counts** (owner decisions 3 and 4) are gated per manager and announced in the release notes.

---

## 8. Principles self-check (for the recommended direction)

"Met by the design" says what the design does; each row also names where the build or the walkthrough verifies it.

| Rule | Status | Evidence or reason |
| --- | --- | --- |
| P1 Defaults first | met by the design; walkthrough task 9 verifies | The default fits one sentence (top of this document). A new to-do shows five things; the calm rule keeps a first-use list plain; the list lands on All. The prototypes draw it (V1's `iphone/list~calm`) |
| P2 Progressive disclosure | met by the design | Developer detail sits behind one folded Work details line and in run sessions; Hand to Agent moves to ⋯ (Agents audience); tool work is one line per run; the full transcript is one level deeper (escalation table) |
| P3 User control | met by the design, one deviation | Undo on tick, answer, scope change and Don't handle; Delete confirms; every number has a one-line definition (counts table); the sidebar's fold state is the owner's. **Deviation:** the sidebar steps aside by itself while the pane is open on iPad landscape; it is transient, never saved, reverses on close, and is owner decision 2 (ii) |
| P4 Consistency first | met by the design, with named deviations | Reuses the Inbox's five choices and words for the Filter, drawn as a pop-up at the iOS list's foot (amended 2026-11-21, section 3.3), `PendingCards`' card, `floatingFooter`, `CountBadge`, shipped state words and DESIGN_SYSTEM 4.3's states. **Kept as decided:** DESIGN_SYSTEM 3.4's section order, #8237's uncounted Just finished, the Inbox's bar order, Fleet's session count. **Deviations, each with its reason in section 5:** the Filter's differences from the Inbox's bar (section 3.3); the Someday swipe, the Parked group folding into Waiting and Got it's retirement (decision 3); counting each to-do once in the Assistant's Needs you and the one-line door adapting DESIGN_SYSTEM 4.8's Team panel rule (decision 4); the per-place overlay, the Duo's 320-pt paper minimum, the main-chat push and the web's fixed 440 column (decision 2). New patterns (the pane, the pinned header, the addressee chip, rounds) go into `DESIGN_SYSTEM.md` with the build |
| P5 Model and product are separate | met by the design | Nothing picks or names a model. The steward's turn uses the assistant's configured model; the addressee chip never names a runtime or model |
| P6 Rules are checkable | met by the design; the checks are built in stages 0–5 | Six fixture sets (section 7.2), the guards (section 7.7), the walkthrough's measures (section 4.3) and screenshots of every key frame in both appearances, attached to the PR (section 4.4) |
| P7 Fast, local-first and stateful | met by the design; to measure in the build | Optimistic tick and answer; Sending → Processing, with Delivered and Queued kept apart from execution; drafts kept per to-do above the view tree; unknown delivery never resent; the cached list offline; the web's tick becomes optimistic (stage 4). Targets named; unmeasured: the Duo animator and the Duo's keyboard height |
| Super rule 2 (tool work collapsed) | met by the design | One summary line per run in rounds and in the pane; ▸ unfolds the latest steps on request; the full session stays inspectable |
| Super rule 3 (Assistant and Agents the only primary destinations) | met by the design | To-dos stays a place reached from the sidebar or drawer, never a tab or segment; a to-do opened from the main chat is pushed on the Assistant stack |
| Super rule 4 (Persona for everyone) | met by the design; the fixture guard verifies | No runtime, engine, model, path, repository or session-id words on everyday screens (rows, paper, chip); organizing a trip never asks for a computer or folder; developer facts only in Work details and run sessions; a computer appears only by the owner's name for it, in a failure or waiting reason |
| Super rule 5 (fast, local-first, stateful) | met by the design; to measure in the build | As P7: receipt, delivery and execution are three visible states; failure keeps the text; retries reuse the op id |

The build PRs run the "UI change self-check" of `DESIGN_SYSTEM.md` with simulator screenshots, light and dark, and the
performance rules of `docs/apple/performance.md` for every Apple change.

---

## 9. References

The public sources of section 2.1 are linked there; these are the others the research read.

### Further reading

| Area | Sources |
| --- | --- |
| Apple | HIG: [layout](https://developer.apple.com/design/human-interface-guidelines/layout) · [motion](https://developer.apple.com/design/human-interface-guidelines/motion) · [Live Activities](https://developer.apple.com/design/human-interface-guidelines/live-activities) · [notifications](https://developer.apple.com/design/human-interface-guidelines/notifications) · [accessibility](https://developer.apple.com/design/human-interface-guidelines/accessibility) · [WWDC25 282, Make your UIKit app more flexible](https://developer.apple.com/videos/play/wwdc2025/282/) · [Tech Talk 111461, Prepare your app for iPhone Duo](https://developer.apple.com/videos/play/tech-talks/111461/) |
| Android and Material | [list-detail](https://developer.android.com/develop/ui/compose/layouts/adaptive/list-detail) · [window size classes](https://developer.android.com/develop/ui/compose/layouts/adaptive/use-window-size-classes) · [fold-aware layouts](https://developer.android.com/develop/ui/compose/layouts/adaptive/foldables/make-your-app-fold-aware) · [Material 3 adaptive releases](https://developer.android.com/jetpack/androidx/releases/compose-material3-adaptive) |
| Task and team tools | [OmniFocus inspector](https://support.omnigroup.com/documentation/omnifocus/universal/4.7.1/en/inspector/) · [Linear design refresh](https://linear.app/now/behind-the-latest-design-refresh) · [Notion views](https://www.notion.com/help/views-filters-and-sorts) |
| Agent products | [ChatGPT notifications](https://learn.chatgpt.com/docs/notifications) · [Claude Code on mobile](https://code.claude.com/docs/en/mobile) · [Vibe Kanban task execution](https://vibekanban.com/docs/core-features/monitoring-task-execution) |
| Protocols and research | [MCP elicitation](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation) · [NN/g, progressive disclosure](https://www.nngroup.com/articles/progressive-disclosure/) · [WCAG 2.2](https://www.w3.org/TR/WCAG22/) |

### Practices borrowed from other projects

Open-source sources are linked; the rest are general engineering practice.

- Keep a To-dos glossary where the UI label wins, in the style of
  [Paseo's glossary](https://github.com/getpaseo/paseo/blob/main/docs/glossary.md).
- Separate the to-do from each run of work on it, as [Multica](https://github.com/multica-ai/multica) models issues and
  agent tasks.
- Lay out compact screens as one left-to-right axis of mutually exclusive destinations, as in
  [Paseo's mobile panels](https://github.com/getpaseo/paseo/blob/main/docs/mobile-panels.md).
- Guard multi-device edits with a revision, and write down which operations apply locally first, as
  [Paradev documents its optimistic task edits](https://github.com/ParadevAI/Paradev/blob/main/apps/desktop/docs/OPTIMISTIC_ELECTRIC_UPDATES.md).
- Store each fact once and derive display states once; end every guideline in the test that enforces it.

### Repository

| Area | Documents |
| --- | --- |
| Rules | `docs/SUPER_RULES.md` · `docs/design/PRINCIPLES.md` · `docs/design/DESIGN_SYSTEM.md` (2.7, 3.4, 4.3, 4.8, 5.6, 6.2) · `docs/design/DECISIONS.md` · `docs/design/HIG_AUDIT.md` · `docs/design/sidebar-navigation.md` · `docs/design/task-cards.md` |
| To-dos and the assistant | `docs/assistant/todos.md` · `docs/assistant/todo-status-sop.md` · `docs/assistant/unified-entry.md` · `docs/assistant/inbox.md` · `docs/assistant/agent-work.md` · `docs/design/proposals/owner-at-a-glance.md` |
| Platforms | `docs/apple/ios.md` · `docs/apple/live-activity.md` · `docs/apple/performance.md` · `docs/lessons/ios.md` · `docs/android/android-design.md` · `docs/web/web.md` |
| Wire and architecture | `docs/protocol/protocol.md` · `docs/server/architecture.md` · `docs/plans/plan-rust-core.md` · `docs/plans/plan-rust-core-phase4-storage.md` · `schema/wire/README.md` · `spec/fixtures/triage/README.md` |
| Prototypes | `docs/design/prototypes/todos/README.md` · `docs/operations/collaboration.md` ("Screenshots in PRs") |
