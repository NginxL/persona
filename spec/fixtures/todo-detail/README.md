# To-do detail fixtures

The elements of a to-do's page, the paper (#10465; `docs/design/proposals/todos-redesign.md` ("3.5 The paper")): given
what the item route answers for a to-do and where its page is shown, which elements the page draws, in which order,
which action is its one primary, and whom its composer addresses first. Presence and order only: words, sizes and looks
are each client's (proposal 7.2). `scripts/test/test_todo_fixtures.py` checks the format, holds every case to the rules
below and the proposal's three columns to its table, and keeps the everyday cases free of machinery (proposal 7.7). No
client reads them yet: iOS will replay them from its paper (#10465), the web and Android from their stages.

| File | What it pins | Cases |
| --- | --- | --- |
| `todo_detail.json` | One to-do as the item route answers it, and where its page is shown → its page, elements, primary action and addressee | 23: the proposal's three columns (everyday, developer, several Agents), a fresh to-do, a closed one, failed starts with and without an origin, Just finished, the Inactive mark, Can wait, the conversation pane on the to-do's conversation and on a run, two asks on one page, and rows this version does not know all of |

## Format

`{"version": 1, "cases": [...]}`. A case is `{name, audience, description, source, input, expected}`; the descriptions
are English.

- `audience`: `everyday`, for the Assistant's audience, whose page needs no programming or Agent tools
  (`docs/SUPER_RULES.md` rule 4), or `agents`, for work Agents do on a computer.
- `input.item`: `{"todo": row}`, the row the page has: what `GET /api/v1/todos/{id}` answers from a manager that names
  `todo_view` (`docs/protocol/protocol.md`), else the list's row where that route is not there (404 or 405). The row
  is the listed one, with its `brief`, `agents`, the legacy `ask` while a decision waits, and `view` (`TodoSummary` in
  `schema/wire/todo-view.schema.json`), which a manager without `todo_view` does not send. The view is given here, not
  worked out: `spec/fixtures/todo-view` pins how the manager works it out.
- `input.timeline` (optional): what the item answer adds while the hub lists `todo_timeline` (#10465), by count only,
  since only presence is pinned: `rounds` (rounds after the to-do was written down), `results`, and `recap` (whether
  there is a Since you looked). Absent: a hub without the feature, whose page draws none of the three.
- `input.context`: where the page is shown.
  - `wide`: a regular-width window (an iPad, the open iPhone Duo, the web), where the conversation can open beside the
    paper; false on an iPhone or the folded Duo, where it is pushed over the paper.
  - `pane_open`: the conversation shows beside the paper (only when `wide`; `spec/fixtures/todo-layout` says when it
    fits).
  - `pane_channel`: what the pane shows: `steward`, the to-do's conversation (its steward is Persona until a to-do can
    have another), or `run:<machine_id>/<session_id>`, one of its runs (`agents`) or a run one of its steps asks from;
    `null` while the pane is closed.
  - `opened_from`: what opened the page: `list` (the To-dos list or one of its places), `assistant` (the main chat: a
    task card, the tray), or `run:<machine_id>/<session_id>` (that run, from Agents). Any other door (a notification,
    search, an Inbox line, a link) reads as `list`; a session's info page reads as that session's run.
- `expected.page`: `paper`, or `as_before` (see "Rows this version does not know"); an `as_before` case has nothing
  else in `expected`.
- `expected.elements`: the elements drawn, top to bottom (the ids below). Compare as lists.
- `expected.primary`: the one action filled with the accent: `ask_card` (the card's primary answer), `ask_door` (the
  door's Review or answer), `mark_done`, `tick` or `none`.
- `expected.addressee`: whom the composer's chip names before the owner picks: `steward` ("To Persona") or
  `run:<machine_id>/<session_id>` ("To ‹run›").

All data is made up.

## Elements

| Id | Proposal 3.5 row | Drawn when |
| --- | --- | --- |
| `bar` | 0 | Always. Its leading item is the host's: ‹ with the place or what opened the page while it is pushed, the list or sidebar button while it is a root region (`spec/fixtures/todo-layout` says which region steps aside); then Conversation and ⋯ |
| `title` | 1 | Always: the tick and the title |
| `state_line` | 2 | Always, except a plain To do with nothing to say: phase `todo`, no `line`, no Inactive mark (`inactive`) and no `start_error` |
| `recap` | 3 | `timeline.recap` |
| `ask_card` | 4 | An ask of `view.asks` the page can draw is the to-do's own (home `todo`): its card, under the state line. A `retry` counts only on a to-do with an `origin` |
| `ask_door` | 4 | An ask the page can draw is a run's (home `run`), and the pane does not show that run: the door "From ‹run› · ‹ask›" |
| `ask_pointer` | 4 | An ask the page can draw is a run's, and the pane shows that run: the door's one line, "‹ask› · answer in the conversation →" |
| `chips` | 5 | The to-do is open (any phase but `done`: the + chip), or it has a due date, a reminder, a priority, a project or a parent (`part_of`) |
| `notes` | 6 | It has notes, or it has no brief (the Add Notes line) |
| `brief` | 6 | It has a `brief`: What you asked, with Done when |
| `help` | 7 | Never yet: Persona can help waits for the steps routes (#10465), so no case draws it |
| `steps` | 8 | `view.steps.total` is above 0 |
| `results` | 9 | `timeline.results` is above 0 |
| `conversation_block` | 10 | `timeline.rounds` is above 0, and the pane does not show the to-do's conversation |
| `conversation_pointer` | 10 | `timeline.rounds` is above 0, and the pane shows the to-do's conversation (`pane_channel` `steward`): "Conversation · open beside →" |
| `work_details` | 11 | Code work (`pr_url`, `pr_status`, `other_prs`, or `stage_kind` `code`), linked workspaces (`spaces`), or a run on a computer (`agents`, past ones included). Whether one of Persona's own conversations linked as a run unfolds it is not pinned yet |
| `composer` | 12 | The pane is closed; while it shows, the composer is in the pane with its draft |

The order is the table's, whatever order `view.asks` lists the asks in. A page can draw `ask_card`, `ask_door` and
`ask_pointer` together, each once: the to-do's own asks share the card, and every run's ask is a door or, for the run
the pane shows, the pointer. `conversation_block` and `conversation_pointer` take one place.

### Primary action and addressee

- `primary` (proposal 3.5, "One primary action, chosen by state"): the first ask of `view.asks` the page draws, the
  most urgent, as the row shows it: `ask_card` or `ask_door` by the element it is drawn as, or `none` when it is drawn
  as the pointer, since its home in the pane holds the screen's one primary. With no ask drawn: `none` for phase
  `needs_you` (the to-do waits on the owner, but not for an answer on this page), `mark_done` for phase `ready`
  (finished, open), else `tick`.
- `addressee` (`docs/design/proposals/todos-redesign.md` ("The addressee chip")): while the pane shows, its channel (a
  run, or the steward for the to-do's conversation); else the run the page was opened from; else `steward`.

### Rows this version does not know

The view's enums are open: a newer hub may send other values (`schema/wire/todo-view.schema.json`).

- **A row without `view`** (from a manager that does not name `todo_view`: one of several, or one reconnecting), or
  whose `phase`, `section` or `reason` this version does not know, gets no paper: the page is drawn as it was before
  the view (`as_before`), as `TodoSummary` says such a row is drawn and as the list files it
  (`row_from_an_older_manager`, `section_from_a_newer_hub`).
- **An ask whose `home` or `kind` this version does not know is left out**, as a Retry the hub would refuse is, and
  the next one is drawn (`unknown_ask_is_skipped`).

## Cases

| Case | To-do | Audience | Shown | Elements after `bar` and `title` | Primary |
| --- | --- | --- | --- | --- | --- |
| `kyoto_needs_your_choice` | Plan the Kyoto trip | everyday | compact | state_line, recap, ask_card, chips, notes, brief, steps, results, conversation_block, composer | ask_card |
| `kyoto_selected_beside_the_list` | Plan the Kyoto trip | everyday | wide | state_line, recap, ask_card, chips, notes, brief, steps, results, conversation_block, composer | ask_card |
| `kyoto_from_the_main_chat` | Plan the Kyoto trip | everyday | compact, from the main chat | state_line, recap, ask_card, chips, notes, brief, steps, results, conversation_block, composer | ask_card |
| `kyoto_pane_on_the_steward` | Plan the Kyoto trip | everyday | wide, pane on the conversation | state_line, recap, ask_card, chips, notes, brief, steps, results, conversation_pointer | ask_card |
| `login_fix_waits_for_merge` | Fix login crash for SSO users | agents | compact | state_line, chips, brief, steps, results, conversation_block, work_details, composer | tick |
| `release_door_from_widget` | Release Weather App 1.3 | agents | compact | state_line, recap, ask_door, chips, brief, steps, results, conversation_block, work_details, composer | ask_door |
| `release_from_agents` | Release Weather App 1.3 | agents | compact, from `run:m1/s-notes` | state_line, recap, ask_door, chips, brief, steps, results, conversation_block, work_details, composer | ask_door |
| `release_pane_on_the_steward` | Release Weather App 1.3 | agents | wide, pane on the conversation | state_line, recap, ask_door, chips, brief, steps, results, conversation_pointer, work_details | ask_door |
| `release_pane_on_the_widget_run` | Release Weather App 1.3 | agents | wide, pane on `run:m1/s-widget` | state_line, recap, ask_pointer, chips, brief, steps, results, conversation_block, work_details | none |
| `fresh_todo` | Renew passport | everyday | compact | chips, notes, composer | tick |
| `closed_done_by_persona` | Fix widget refresh crash | agents | compact | state_line, chips, brief, results, conversation_block, work_details, composer | tick |
| `failed_start_with_origin` | Sort last year’s photos | everyday | compact | state_line, ask_card, chips, brief, conversation_block, composer | ask_card |
| `failed_start_without_origin` | Rename the holiday videos by date | everyday | compact | state_line, chips, notes, composer | none |
| `failed_start_without_origin_older_hub` | Rename the holiday videos by date | everyday | compact, no `todo_timeline` | state_line, chips, notes, composer | none |
| `failed_start_three_days_on` | Print the holiday photo book | everyday | compact | state_line, chips, notes, conversation_block, composer | tick |
| `just_finished_mark_done` | Book the dentist | everyday | compact | state_line, chips, notes, results, conversation_block, composer | mark_done |
| `inactive_todo` | Call the plumber about the boiler | everyday | compact | state_line, chips, notes, conversation_block, composer | tick |
| `nara_step_and_plan_both_ask` | Plan the Nara day | agents | compact | state_line, ask_card, ask_door, chips, notes, steps, conversation_block, composer | ask_door |
| `nara_pane_on_the_guide_run` | Plan the Nara day | agents | wide, pane on the step's `run:m1/s-guide` | state_line, ask_card, ask_pointer, chips, notes, steps, conversation_block | none |
| `can_wait_decision` | Pick the cherry blossom spot | everyday | compact | state_line, ask_card, chips, notes, conversation_block, composer | ask_card |
| `row_from_an_older_manager` | Buy groceries | everyday | compact, no `view` | as before | — |
| `section_from_a_newer_hub` | Book the car service | everyday | compact, an unknown section | as before | — |
| `unknown_ask_is_skipped` | Renew the music school term | everyday | compact, an unknown ask kind first | state_line, ask_card, chips, notes, conversation_block, composer | ask_card |

The addressee is `steward` in every page but `release_from_agents` (`run:m1/s-notes`),
`release_pane_on_the_widget_run` (`run:m1/s-widget`) and `nara_pane_on_the_guide_run` (`run:m1/s-guide`). The to-dos
are the V1 prototype's (`docs/design/prototypes/todos/data/todos.js`), with a few added for the rules the prototype
does not draw; the Nara row and its view are `step_and_parent_both_ask` of `spec/fixtures/todo-view`.

## Judgement calls

Where the proposal and the prototype leave a choice open, these cases make it; change a case and this list together.

- **Notes and the brief both show** when the owner wrote notes on a delegated to-do, as the prototype's `iphone/kyoto`
  draws them. The Add Notes line shows only on a to-do with no brief, so a delegated one with no notes shows What you
  asked alone (`iphone/release`). Proposal 3.5 row 6 says "notes always" and its everyday column lists notes only; the
  prototype is followed.
- **A page offers no Retry the hub refuses.** `POST /api/v1/todos/{id}/retry` refuses a to-do that was not handed out
  by the assistant. From #10465 the hub lists a retry ask only on a to-do with an origin; until then, and on older
  hubs, every failed start has one, and the page leaves out the one it cannot send
  (`failed_start_without_origin_older_hub`). That page says why it did not start.
- **A Needs you page with nothing to answer on it has no primary**: a failed start without Retry, an interrupted run or
  a reply awaited in text (`spec/fixtures/todo-view` lists no ask for them) waits on the owner, and the tick in the
  accent would offer to mark done work that never started or waits for them.
- **Several asks: the card first, the primary to the most urgent.** The page keeps proposal 3.5's one order (the
  to-do's own card under the state line, then the doors), and the primary goes to the first ask the hub lists, which
  is the one the row shows ("Two asks show '2 asks' and the more urgent one", 3.4). So in `nara_step_and_plan_both_ask`
  a step's run question that needs the owner holds the primary over the to-do's decision that can wait.
- **Can wait still needs the owner**: its ask has its card and the primary (`can_wait_decision`). It is only left out
  of the counts (proposal 3.4).
- **A step's run that asks is named on its parent's page**: its door and its pane work as for the to-do's own runs
  (`nara_pane_on_the_guide_run`), though Work details lists only the to-do's own runs.
- **A failed start's page keeps saying why it did not start** after its row stops saying it
  (`spec/fixtures/todo-view/README.md` ("Judgement calls"): two days on it is a plain To do row with no line).
- **The Inactive mark is something to say**: an Inactive To do has a state line.
- **The pane on a run leaves the conversation block on the paper** (`ipad11-landscape/open~widget`): the pane shows the
  run, not the to-do's conversation, so nothing is drawn twice. The block turns into the pointer only while the pane
  shows the conversation.
- **While the pane shows a request's home, the pane holds the primary** (`none` on the paper), so the screen still has
  one.
- **A run's ask is a door on the paper whatever its kind and risk**: a door that answers a safe ask in place
  (proposal 3.7) differs in what it holds, not in which element it is.
- **A closed to-do's primary is its tick** (done, a tap reopens it); Mark Done is for open, finished work only.
- **The composer's chip follows the pane first, then what opened the page**: a pane on a run addresses that run
  (proposal 3.6, "The pane shows a run"), and a page opened from a run under Agents addresses it.
- **Where the page is shown changes nothing by itself**: the same to-do selected beside the list, pushed from the list
  or pushed from the main chat draws the same elements (`kyoto_selected_beside_the_list`, `kyoto_from_the_main_chat`).

## Not pinned yet

- How several asks of one home merge or stack (proposal 3.5: "same-kind asks merge"): the to-do's own asks share one
  card and every run's ask is a door, but their order and grouping inside the card and among the doors come with the
  first case that needs them.
- One of Persona's own conversations linked as the to-do's run (`persona_working` in `spec/fixtures/todo-view`) is
  Persona's work, not an Agent's on a computer; whether it unfolds Work details waits for a case that carries the
  session list.
- Persona can help (`help`), suggested steps and the chat topic channel: with the steps routes and the chat topic's
  reader. `help` then joins `fresh_todo`, which shows five things instead of four.
- The narrow paper in the Duo's halves (the prototype folds Since you looked and Steps at 335 pt,
  `duo-inner-portrait/list-paper`): decided with the Duo's measurement (proposal 3.9).
- The keyboard's folded paper (proposal 3.8, "The keyboard"): a layout of these elements, not another set.

## Run

```sh
python3 -B scripts/test/test_todo_fixtures.py   # the format, the rules above, the three columns, the everyday guard
```
