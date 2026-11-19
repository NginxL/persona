# To-do rounds fixtures

A to-do's conversation (#10465, `docs/design/proposals/todos-redesign.md` 3.6): its history grouped into rounds, what
its work produced, and what changed since the owner last looked. The shapes are `TodoTimeline` (its `TodoRound`s and
their `TodoTimelineEntry`s), `TodoResult` and `TodoRecap` in `schema/wire/todo-view.schema.json`. The manager's timeline
(`server/internal/assistant/todo/timeline`) answers every case exactly, whatever order the rows come in
(`server/internal/assistant/todo/timeline/fixtures_test.go`); `scripts/test/test_wire_schema.py` holds every expected
value to the schema, and the generated types read each one back unchanged (Apple
`apple/PersonaTests/TodoViewWireTests.swift`, Go `server/internal/assistantwire/todo_view_gen_test.go`, Kotlin
`android/core/contracts/src/test/kotlin/persona/core/contracts/TodoViewWireTest.kt`).

Stage 2 reads the timeline from the stores the hub already keeps, and keeps nothing new; stage 3 stores the entries
themselves (`todo_activity`, proposal 7.6), and these cases still hold for what both can say, ids aside.

| File | What it pins | Cases |
| --- | --- | --- |
| `todo_rounds.json` | One to-do and its stores' rows → its rounds, the ones the paper shows, its results and its recap | 30: every entry type and round kind, each of R1 to R7 |

## Format

`{"version": 1, "cases": [...]}`. A case is `{name, description, source, input, expected}`, as in
`spec/fixtures/todo-view`; the descriptions are English.

- `input.todo`: the to-do as `GET /api/v1/todos/{id}` lists it: its runs' links (`agents`; the list leaves out a run
  the owner took off the to-do), its ledger (`supervision`: its `state` and `reviews`), its brief and lineage, its open
  `ask` and `decision_review`, its PRs (`pr_url`, `pr_status`, `other_prs`) and `delivery`, `seen_at`, `done_at`,
  `done_by` (a cancellation's is `review`, as the list sends it), `start_error`.
- `input.feed`: the conversation feed's items whose `ref` is the to-do (`FeedItem`, whatever conversation they are in),
  in no particular order.
- `input.decisions`: its decisions, superseded ones included, as `todo_decisions` rows (with `superseded_by`).
- `input.sessions`: its runs' sessions as the hub sends them to the apps (`state` already worked out).
- `input.machines`: the computers' names by machine id, which receipts written before #10465 name their run by.
- `input.loc` (optional): the owner's time zone (an IANA name), which a Background round's day is in; UTC when absent.
- `expected.timeline`: the `TodoTimeline`, every round oldest first (`more` is the route's, for paging; absent here).
- `expected.results`: the `TodoResult`s, newest first, at most 3; absent when the work produced nothing.
- `expected.recap`: the `TodoRecap`; absent when the owner never looked, or nothing but their own acts came since.
- `expected.latest`: the ids of the rounds the paper shows (`timeline.Latest` with `PaperRounds`, R7); absent when
  there are no rounds.

Compare as JSON values: a field that is absent must stay absent. Every time is UTC, RFC 3339 as Go writes it.

All data is made up.

## Entries

Each row a store keeps about the to-do is one entry, or none, or adds to another; a "header" is an owner act that
opens a round (R1).

| Read from | Entry | By | What it carries |
| --- | --- | --- | --- |
| `routed` feed item with neither `from` nor `relay` | `instruction`, a header | owner | `text`: the owner's words; `state` `delivered` (the item is written only once its run took them); `run`: the run it reached, `machine_id` and `session_id` (#10465), else `new_agent_machine` and `new_agent` (older items) |
| `routed` with `from` `assistant` | `relay` | persona | `text`; `run` when the item names one |
| `routed` with `relay` | `relay` | hub | `text`: the hub's wording of the owner's answer for the run; `run` |
| `receipt` `done` or `interrupted` | a turn of its run (R4) | run | `text`: the summary of a done turn |
| `receipt` `failed` | `run_failed` | hub | `text`: its summary, the hub's words as the conversation showed them (no run started), left out once a start error says why (below) |
| `answered` | `answer`, a header, `via` `card` | owner | `question`; `text`: the answer of each of the card's questions, one a line; the summary line when it has none (a permission, a plan) |
| `answered` with `by` `assistant` | `decision` | persona | `question`; `reason`; line `answered_by_persona` |
| `report` | `report`, Background (R3) | run | `text`; line `report` |
| `review` (feed and ledger, R6), `needs_user` | `ask` | persona | `text`: the question (the ledger's `reason`, else the note) |
| a `needs_user` review's `owner_response` (ledger) | `answer`, a header, at `owner_response_at` | owner | `question`: the ask's; line `owner_response`. The owner's fixed answer to the decision (`ownerCannotAnswer`), which no decision and no feed item records |
| `review` `accepted` or `cancelled` | `review` | persona; the done judge's (`by` `judge`): hub | `text`: the note; line `review` |
| `review` `parked` | `parked`, Background | persona | line `parked` |
| `review`, any other verdict (`rework`, `pending_review`) | `review`, Background | persona; the independent reviewer's (`by` `reviewer`): run | `text`: the note; line `review` |
| decision `manual` | `answer`, a header | owner | `text`: the choice; `question`; `reason`; `via` `chat` when it names the conversation that recorded the owner's words (`source_session`, `task_decided`), and then `text` is those words (its `reason`) and `choice` the option they chose |
| decision `card` | `answer`, a header, `via` `card` | owner | as `manual` |
| decision `thread` | `answer`, a header, `via` `thread` | owner | as `manual` |
| decision `agent` | `decision` | persona; the run it names, when it names a run and no message (a run's own note, `todo_decision_add`) | as `manual`; `via` `default` when it names nothing (the hub took the recommendation at its deadline), with no `reason`: the hub's sentence saying so, which `via` says |
| decision `outcome`, the hub's outcome line of a completion (`source_message` `outcome:<to-do>:<done_at>#o`) | `result`, a Results chip | the run it names, else hub | as `manual` |
| decision `outcome`, the hub's decisions and pitfalls of a completion (`#d<n>`, `#p<n>`) | `decision` | the run it names, else hub | as `manual` |
| decision `outcome` of an extension (`source_machine` `extension:<name>`) | `result`, a chip | extension | as `manual` |
| decision `outcome`, any other (the owner wrote it by hand) | `result`, a chip | owner | as `manual` |
| decision `reported` (a concern's outcome told to the owner) | `result`, a chip | persona | as `manual` |
| decision of any other source | `decision` | the run it names, else hub | as `manual` |
| a run's link (`agents[]`) | `run_started` at `linked_at` | run | `run`; line `run_started`. A `task` feed item that says how a run the task did not start came to it (`wake`) adds `wake` to the start nearest it within 2 minutes |
| a link's `released_at` | `run_ended` | hub | `run`; line `run_ended` |
| a link's `start_error` | `run_failed` at `linked_at` | hub | `run`; line `run_failed`. The failed `receipt` nearest it within 2 minutes that names no run yet is the same failure: that entry takes the run and the line instead, and leaves out the receipt's `text`. A run whose start failed never starts or ends |
| the to-do's `start_error` (its latest failed start) | the line `run_failed` of the failure nearest its `done_at` within 2 minutes (a failed receipt or a run's), without the receipt's `text` | hub | with neither, a `run_failed` of its own at `done_at` |
| the to-do's close (`done_at` without a `start_error`) | `closed` at `done_at` | owner (`done_by` empty); hub (`auto`, or a review whose accepting review nearest the close within 2 minutes was the done judge's); persona otherwise | line `closed`; `outcome` `canceled` when `done_by` is `review` and the ledger's `state` is `cancelled`, as the view reads it (the owner's close of a once cancelled to-do is theirs, and done) |
| the brief's request, when the owner started the work (`started_by` `owner`) | `instruction` with `state` `delivered`: the start round's header | owner | `text` |
| the brief's log, `scope` | `scope` | persona | line `scope` |
| the brief's log, `overdue` | `overdue`, Background | hub | line `overdue` |
| the lineage step of this to-do, when it `continues` another | `run_started` at the step's `started` (else `created_at`) | hub | line `continues` |
| the to-do's open decision (`ask` with `outcome` `decide` or `cannot`, unless `decision_review` has a `dismissal_id`) | `ask` on the `needs_user` entry that raised it, the latest at most 10 minutes before `asked_at`; with none, an `ask` of its own at `asked_at` | persona | `ask`: the id the view gives it (`todo:decision@<asked_at as listed>`); line `cannot` for a conclusion that says it cannot be done |

A brief is shared by every to-do of one piece of work: its log counts when the entry names this to-do, or names none
and comes after this one was created. Feed items of other kinds (`task`, `start_remedy`, `plan`, `message`, …) are no
entries (see "Judgement calls").

### Ids

Unique within a timeline, and the same on every read while the stores keep the rows (R7). Times are UTC, RFC 3339 to
the nanosecond.

| Entry | `id` |
| --- | --- |
| a feed item, or a review read from both the feed and the ledger | `feed:<feed id>` |
| a decision | `dec:<decision id>` |
| a ledger review with no feed row | `review:<at>` |
| the brief's request; a log entry | `brief:<brief id>:request`; `brief:<brief id>:<index in its log>` |
| a run's link, its end, its failed start | `run:<machine id>/<session id>:linked`, `:ended`, `:failed` |
| a run's line in a round (R4) | `run:<machine id>/<session id>@<round id>`; `run:?@<round id>` for "A run" |
| the to-do's failed start, close, open ask; what it continues | `todo:failed@<done_at>`, `todo:closed@<done_at>`, `todo:ask@<asked_at>`; `lineage:<to-do id>` |
| the owner's fixed answer to a review's decision | `review:<the review's at in the ledger>:response` |

A round's id: `start`, `r:<its header's id>`, `bg:<YYYY-MM-DD>`.

## Rounds

The rules, applied to every entry sorted by time, then id.

- **R1. Owner acts open rounds.** Every header (the owner's instruction, their answer of a card, a decision they made
  by hand, in a conversation, on a card or in the chat topic, their fixed answer to a decision) opens an owner round,
  `r:<header id>`. One act that left two rows makes one header:
  - a decision recorded with the instruction that carried it (its `source_message` is the instruction's feed id;
    `routeDecisionAnswer`) folds under the instruction;
  - the card decisions of one card (one per question, the card's id as their `source_message`; `cardDecisions`) are
    one answer: the earliest stands for the card, and the others go where it goes;
  - a card's decisions fold under the owner's answer of a card nearest the earliest within 10 seconds (both are written
    when the card is answered, in no fixed order, and the answer does not say its run);
  - the hub's relay of the owner's choice (`relay`) folds under the owner's decision it relays: the one nearest it
    within 10 seconds when the choice was relayed, then recorded (`chooseOption`, `takeRecommendation`); else the
    latest before it within 5 minutes, when the decision was recorded first and relayed once its run took it, which the
    hub waits for up to 90 seconds, or 5 minutes for a run it resumes (a reply to the decision's card in Lark,
    `acceptReply`). A relay with another decision within 10 seconds (the hub's default) relays that one;
  - the run an instruction started when every run had stopped (`new_agent`) folds under that instruction, though its
    link is a moment older.
- **R2. What follows a header is its round's.** An entry belongs to the round of the latest header at or before it,
  except what R1 pairs and R3 takes. Entries before every header go to the `start` round, headed by the brief's
  request when the owner started the work, else by nothing; it comes first, and is left out when it has neither header
  nor entries.
- **R3. Supervision nobody asked for** (a review but an ask, an acceptance or a cancellation; a park; a run's report to
  its supervisor; the overdue mark) goes to one `background` round per day in the owner's zone, `bg:<YYYY-MM-DD>`.
- **R4. One line per run per round.** The turns (receipts) of one run in one round are one entry, `run`, at the latest
  turn: its summary as `text`, how many turns as `turns`; `interrupted`, without `text`, when the latest turn was cut
  off. A receipt that names no run (written before #10465) goes to the one run whose computer has the receipt's
  machine name, whose start did not fail and whose link covers its time (`linked_at` ≤ at < `released_at`); with none
  or several, to "A run" (no `run`), and the unnamed turns of a round make one line. In the latest round that is not a
  Background one, the line of every run that works now (its session's `state` `working`) is `live`, a `run` line
  saying what the run does now (its session's `activity`, else the first line of its `last_text`) when the session
  says; a run with no turn in that round gets a live line of its own, from when it has worked (not before the round
  began).
- **R5. Superseded decisions stay**, with `superseded_by`: the timeline is a record (the paper's Decisions list leaves
  them out).
- **R6. A review is read once.** A review the conversation shows is also in the ledger, written a moment before its
  feed row: a feed row and the ledger entry of the same verdict at most a minute before it are one review, under the
  feed row's id and time, with what only the ledger keeps (who gave it, a decision's question, a park's reason). The
  ledger adds the reviews no row has (a to-do no conversation follows, a review from before it had one); it keeps the
  latest 20.
- **R7. Ids** are the ones above. Rounds are ordered by when they began (an owner round: its header; a Background
  round: its first entry), then id; a round's entries by time, then id, its header not repeated. The paper shows the
  latest two rounds that are not Background ones, where the work and the owner's asks are, the latest Background ones
  making up the rest, in that order (`Latest`, `expected.latest`): a Background round can begin after the round that
  holds the open ask and its live run lines.

### Line keys

`line {key, args}` for what the hub has to say about an entry: clients say each key in their own words; no hub catalog
carries text. Args are strings, a time is absolute RFC 3339, and an empty argument is left out. The keys are stable:
never renamed or reused. `interrupted`, `parked` and `cannot` are also keys of the to-do's view
(`spec/fixtures/todo-view/README.md` "Line keys"), with the same meaning and arguments.

| Key | Entry | Args |
| --- | --- | --- |
| `run_started` | `run_started`, a run's link | `run`: its title; `wake`: how a run the task did not start came to it (`active`, `resumed`), absent when the task started it |
| `continues` | `run_started`, where a to-do that took the work over began | `todo`: the to-do it took the work over from; `title`: that to-do's title |
| `run_ended` | `run_ended` | `run` |
| `run_failed` | `run_failed` | `run`: the run whose start failed, absent when none was linked; `error`: the start error, absent when the hub kept none (the entry's `text` says why) |
| `interrupted` | `interrupted`, a run line | `run`, absent for "A run" |
| `review` | `review` | `verdict`: `accepted`, `cancelled`, `rework`, `pending_review` or a newer one; `round`: a rework's round, absent on other verdicts |
| `parked` | `parked` | `reason`: what it waits for; `until`: when the park ends |
| `overdue` | `overdue` | none |
| `scope` | `scope` | `change`: what changed, as the supervisor said it; `why`, absent when it did not say |
| `closed` | `closed` | `by`: `owner`, `persona` or `judge`; `outcome`: `done`, `canceled` or `reported` |
| `cannot` | `ask`, a conclusion that says it cannot be done | none |
| `report` | `report` | `kind`: `question`, `blocked` or `done` |
| `answered_by_persona` | `decision`, Persona's answer of a card in the owner's place | `answer`: the card's answers, one a line; absent when the card had no questions (a plan, a permission) |
| `owner_response` | `answer`, the owner's fixed answer to a decision | `response`: `decide_yourself`, `not_what_i_asked`, `explain_first` or a newer one |

## Results

What the work produced, newest first, at most 3 (the paper's Results, proposal 3.5 row 9):

- each code change: a PR a receipt named, the one its runs last named (`pr_url`), or one the hub looked at for the
  to-do (`pr_status`, `other_prs`), `kind` `change`; one per PR however its address is written (compared without
  fragment, query, trailing slash or case, as the view compares them), `id` `change:<that address>`, `url` as the hub
  looked at it, else as `pr_url` or the receipt wrote it; at the first receipt that named it, else when the hub last
  looked, else at `done_at`, else at the latest start or end of one of its runs (a to-do a task leader runs has no
  receipts); with `state` as the hub last saw it (`TodoChangeState`: `open`, `draft`, `merged`, `closed`; absent while
  it has not looked) and the receipt's run;
- the work going live (`delivery` `live`), `kind` `deploy`, `id` `deploy@<at>`;
- each `result` entry no later decision superseded, `kind` `report`, under the entry's id: `text` its first line,
  `url` the first link among the lines after it, `run` the run that produced it.

## Since you looked

`recap` (the paper's Since you looked, proposal 3.5 row 3): `since` is `seen_at`; `counts` how many entries of each
type came after it, each of a run line's turns counted as a `run` or an `interrupted`; `round` the round of the first of
them, which a tap scrolls to. The owner's own acts are no news to them, nor is what R1 folds under one (the hub's relay
of their answer, the run their message started): both are left out. Absent without `seen_at`, or when nothing else
came.

## Judgement calls

- **A review's two rows are not at the same moment** (R6): the ledger's time is when the review was decided, the feed
  row's when it was written, after the review's other writes. A minute covers that; two reviews of one verdict within
  a minute pair in order.
- **An ask and an acceptance stay in their round**, the background supervisor's too (R3 takes only what nobody needs to
  see where the work is). An acceptance is a `review` line, not the close: one that wants the owner to check, or one of
  a concern, closes nothing.
- **Only the latest close.** The to-do keeps one `done_at`; a to-do reopened since has no close until it closes again.
- **A failed start is one line**: its receipt, the link of the run whose start failed, and the to-do's `start_error`
  are one failure when they are within 2 minutes of each other. The line says the error when the hub kept it, and the
  receipt's sentence is then left out.
- **A header is the owner's own words**: an `answered` item's answers in full (its summary line is clipped to 120
  characters), and what they said in a conversation for a decision recorded from it, the option in `choice`.
- **Some words are the hub's**, kept as the stores have them, in the language they were written in, since nothing
  tells them apart from someone's: a failed start's receipt that names no error; a turn that ended without a summary
  (the hub's word for done); the owner's answer of a card without questions (a permission or a plan: the summary line);
  a decision's question when the supervisor gave none. The hub's sentences the timeline can tell apart are left out:
  the summary line of a card Persona answered (the line key says it) and a default's reason (`via` says it). Stage 3's
  `todo_activity` stores keys for them.
- **A cancellation is read off the ledger**, as the view reads it: the list sends a cancelled to-do's `done_by` as
  `review`, and its ledger's `state` says `cancelled`.
- **A run the owner took off the to-do is not listed**, so it has no start or end here. Its turns since #10465 still
  name it; an older turn of it goes to a listed run on its computer whose link covers that time, or "A run".
- **Hand-off cards and start remedies are no entries.** A `task` item says nothing the run's link does not (except how
  a run came to it, `wake`); `start_remedy` steps (the keys typed, the retries, the hub giving up) are the hub working
  on a failed start, which its `run_failed` line and the to-do's retry ask already say.
- **Persona's relays stay in the round they fall in**, a rework's note to its run included; the rework itself is in
  the day's Background round.
- **A run works now** when its session's `state` is `working`; a run waiting on the owner or idle has no live line.
- **"A run"**: every turn no run can be named for in a round is one line, as R4 has it.
- **`via`** says only what the stores show: `card`, `chat`, `thread` and `default`. A decision made from the app, a
  push or a briefing card is a `manual` row that does not say which (`app`, `push` and `briefing` wait for stage 3).

## What a timeline read from today's stores cannot show

Stage 3's `todo_activity` will: the owner's messages sent straight to a run's session (they are in its transcript
only), the steward's replies (there is no steward yet), message states other than `delivered`, the rounds of the
to-do a take-over continues (only the line that says it continues them), earlier closes, when the owner chose Don't
handle or Later, the owner's fixed answers to decisions older than the ledger's latest 20 reviews, the runs the owner
took off the to-do, and Undo of a scope change.
