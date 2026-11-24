# Agents' Needs You fixtures

The Agents tab names one number "Needs you" in three places: its section's header, its Needs you filter and the tab's
badge. These cases pin what that number counts, so the places and the clients agree (`docs/design/PRINCIPLES.md`,
"Consistency first"): one row per Agent session that waits for the user, the definition `docs/assistant/managers.md`
("Counts") gives an assistant's waiting agents. The section lists every waiting row; the three places count the rows
the badge counts (D6 of `docs/plans/plan-derived-counts.md`, "6. Decisions"). The rows the count leaves out stay listed
in the section, in uncounted groups named for why: Muted, and Assistant is looking for quiet tasks' agents and held
questions (`docs/protocol/agent-states.md`, "Needs you that is not counted", says how such a row reads). A card its
task's supervisor holds is the supervisor's, not the user's: it is neither listed nor counted (D11, below).

| Rule | Cases |
| --- | --- |
| A session waits while it has at least one pending card, whatever its state says, or while the hub says it needs the user and no card was read (a prompt only its terminal shows). It is one row; its cards sit under it, oldest first. | `cards_of_one_session_are_one_row`, `a_session_waiting_in_its_terminal_is_a_row`, `a_card_makes_its_session_wait_whatever_its_state` |
| A card that belongs to no session (`personad ask` outside herdr) is a row of its own, even beside another on the same computer. | `a_card_with_no_session_is_its_own_row` |
| Rows go oldest first: by their oldest card or, with none, by when the session started waiting (`state_since`). | `rows_go_oldest_first` |
| The assistant's own conversations are the Persona tab's: never a row. | `persona_conversations_are_not_agents` |
| A muted agent (itself, or its space unless it was turned back on) and a quiet task's agent stay rows, with their cards: they still wait. The count leaves them out (the header, the filter and the badge), as the assistant's count does. | `muted_and_quiet_agents_are_rows_but_not_on_the_badge`, `a_quiet_tasks_card_is_a_row_but_not_on_the_badge` |
| Working, finished (Awaiting reply included), idle and offline sessions without a card are not rows. | `other_states_do_not_need_the_user` |
| A child the hub folds under its parent (`folded_under`) and a side session (`folded_as: "side"`) are no rows of their own: their cards sit under the parent's row, which waits through the children rollup or the sides it holds. One row, counted once. | `a_childs_card_counts_under_its_parent`, `a_side_counts_under_its_parent` |
| A session an Agent-folder hide rule matches (`hidden_by_directory`, `docs/protocol/protocol.md` ("Agent directory visibility")) is no row, and its cards count nowhere. | `a_directory_hidden_card_counts_nowhere` |
| A card of any kind makes its session wait: an MCP server's form (`elicitation`) does too. | `an_elicitation_makes_its_session_wait` |
| A held question (D11) is listed and answerable but counts nowhere until it is first handed to the user: a `delegating` question, which the assistant is still deciding whether it may answer for the user. A session whose only pending cards are held is a row with them, but no count includes it, though its state says `needs_you`: the manager's `agentState` marks a session with any pending card so, and that stays as it is. A session with a held card and one already handed to the user counts. A card escalated to the user is handed, whatever its push level. | `a_held_card_is_listed_but_counts_nowhere`, `a_handed_card_beside_a_held_one_counts` |
| A card its task's supervisor holds (a `delegating` card of any kind but `question`: a permission, a plan, or a parsed terminal question, kind `screen`) is the supervisor's until it passes it up with `ask_escalate`: it is in no row and counts nowhere, and the clients offer no way to answer it. A session whose pending cards are all so held is no row, though its state says `needs_you` (for a `screen` card, from the terminal dialog behind it); with a card not held it is a row with that card alone. The owner confirmed this for routed terminal questions on 2026-11-23 (D11, the plan's 6.1). | `a_routed_terminal_question_is_held`, `a_supervisor_held_card_beside_an_open_one_leaves_the_row` |

A copy of a card mirrored to the owner (`mirror_of`) counts nowhere (D1). That rule comes with #12645, which adds the
field. While #12645 is open no case pins it: its case lands with #12645, or with the first change after it merges.

- Structure: `{"version": 1, "cases": [...]}`. `input` has the hub's wire objects, `sessions` and `interactions` (the
  pending cards), with `mutes` as the hub's pins give them (`sessions`, `spaces`, `allowed`, each `<machine>/<name>`)
  and `quiet`, the `<machine>/<session>` of quiet tasks' agents.
- `expected.rows` lists the section's rows in order, each with its `id` (`agent:<machine id>/<session id>`, or
  `card:<interaction id>` for a card with no session) and its `cards` (interaction ids, oldest first);
  `expected.badge_rows` lists the rows the tab's badge counts, in the same shape and order (the rows less muted agents,
  quiet tasks' agents and sessions whose only cards are held), and `expected.badge` is their number. A card its task's
  supervisor holds is in no row at all.
  `expected.header` is the number the section's header and its Needs you filter show: the badge's rows (D6).
- Sessions carry the hub's fields for folding (`folded_under`, `folded_as`, `children`) and hiding
  (`hidden_by_directory`), as a hub that folds sides (`sessions_side_fold`) sends them. Every case holds the whole
  list: what a bounded list leaves unloaded is not pinned here (`docs/plans/plan-derived-counts.md`, section 2).
- `known_differences` marks a case one client's counting fails today, by client (`ios`, `android`; "Known
  differences" below).
- All data is made up.
- Run: `AgentsNeedsYouFixtureTests` in `apple/PersonaTests` (iOS: the section, its header, its filter and the tab's
  badge) and `cd android && ./gradlew :core:domain:test --tests '*AgentsNeedsYouFixtureTest*'` (Android: the rows, the
  header and the tab's badge; the Agents page's Needs you section, its header and its filter count rows by the same
  rule, one per waiting session, through `agentNeedsYouRows`, which also applies the computer filter). The web app's
  Agents badge counts sessions in Needs you without quiet tasks' agents by a rule of its own, which Persona Link's
  summary shares (spec/fixtures/attention): it counts muted agents and no card on no session, so it does not replay
  these cases.
- Each replay reads the cards and sessions as its client's badge does. iOS (`AgentsNeedsYouFixtureTests`, and
  `HomeWidgetTests` for the Home Agents widget, which draws the badge's rows) folds sides under their parents as the
  lists land (`SideFold.fold`, `AppModel.allSessions`) and counts the badge from every pending card but a
  directory-hidden session's (`AppModel.directoryVisibleInteractions`), as `AppModel.countBadges` does. Android reads
  them through the functions `ShellCounts.agentRows` calls. Both assert `rows`, `badge_rows`, `badge` and `header`.
- With the counts report switch on (spec/fixtures/counts/README.md, "Lane reports"), both replays also write what they
  counted for each case, marks included, and `scripts/test/counts_agreement.py` compares the two lanes.

## Known differences

The fixture is the Agents count's definition (`docs/plans/plan-derived-counts.md`, section 3.5). A client whose own
counting fails a case is not changed to follow it here, since that would change the number it draws; the case carries
`known_differences.<client>` instead:

- `decision`: the decision that settles it, from the plan's
  `docs/plans/plan-derived-counts.md` ("6. Decisions"); `why`, in a sentence; `source`, the code
  that counts so.
- `replace`: today's values for the keys of `expected` it replaces (`rows`, `badge_rows`, `badge`, `header`).

That client's replay asserts the case at `expected` with `replace` applied; every other client asserts `expected`.
Both replays fail on a mark that names no decision, names a client that does not replay the fixture, or changes
nothing. Since a marked case is asserted at today's value, a client that comes to follow the case fails until its mark
is removed. Marks are added only with a decided definition change (below) and are otherwise only taken away. They
stay with each client's frozen fallback until the plan's step 13 deletes both.

### A decided definition change

When a decision in the plan's section 6 changes what this count includes, or which number a place draws, the cases
change to the decided definition in one change, before any client follows it. Each client that still counts by the old
rule gets a mark naming that decision, at today's value, so its replay keeps the number people see until its own change
removes the mark. A decision that changes the count's own rule also goes into `agents_needs_you`'s `refined_by` in
`spec/fixtures/counts/surfaces.json`, named on every row of that table that counts by the old rule (its README,
"Changing the table"). D6 (the header draws the count) and D11 (held cards, a refinement of the rule) came in this way
(#12888); D1's `mirror_of` rule is next, once #12645 merges.

| Case | Client | Decision | Today |
| --- | --- | --- | --- |
| `muted_and_quiet_agents_are_rows_but_not_on_the_badge` | iOS, Android | D6 | The header and the filter count every row of the section, muted agents and a quiet task's agent included (5, not 2) |
| `a_quiet_tasks_card_is_a_row_but_not_on_the_badge` | iOS, Android | D6 | The header and the filter count the quiet task's agent's row (2, not 1) |
| `a_directory_hidden_card_counts_nowhere` | Android | D1 | Android reads no `hidden_by_directory`: the hidden session and its card are a row, and the badge and the header count it (2, not 1) |
| `a_held_card_is_listed_but_counts_nowhere` | iOS, Android | D11 | Every pending card's session counts, held or not: the badge, the header and the filter count it (1, not 0) |
| `a_routed_terminal_question_is_held` | Android | D11 | Android reads no `delegating`: the supervisor's terminal question is listed under its session, and the badge, the header and the filter count it (1, not 0) |
| `a_supervisor_held_card_beside_an_open_one_leaves_the_row` | Android | D11 | Android reads no `delegating`: the supervisor's permission is listed in the session's row beside the question (the count is 1 either way) |
