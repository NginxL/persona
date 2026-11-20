# Agents' Needs You fixtures

The Agents tab names one number "Needs you" in three places: its section's header, its Needs you filter and the tab's
badge. These cases pin what that number counts, so the places and the clients agree (`docs/design/PRINCIPLES.md`,
"Consistency first"): one row per Agent session that waits for the user, the definition `docs/assistant/managers.md`
("Counts") gives an assistant's waiting agents.

| Rule | Cases |
| --- | --- |
| A session waits while it has at least one pending card, whatever its state says, or while the hub says it needs the user and no card was read (a prompt only its terminal shows). It is one row; its cards sit under it, oldest first. | `cards_of_one_session_are_one_row`, `a_session_waiting_in_its_terminal_is_a_row`, `a_card_makes_its_session_wait_whatever_its_state` |
| A card that belongs to no session (`personad ask` outside herdr) is a row of its own, even beside another on the same computer. | `a_card_with_no_session_is_its_own_row` |
| Rows go oldest first: by their oldest card or, with none, by when the session started waiting (`state_since`). | `rows_go_oldest_first` |
| The assistant's own conversations are the Persona tab's: never a row. | `persona_conversations_are_not_agents` |
| A muted agent (itself, or its space unless it was turned back on) and a quiet task's agent stay rows, with their cards: they still wait. The tab's badge leaves them out, as the assistant's count does. | `muted_and_quiet_agents_are_rows_but_not_on_the_badge`, `a_quiet_tasks_card_is_a_row_but_not_on_the_badge` |
| Working, finished (Awaiting reply included), idle and offline sessions without a card are not rows. | `other_states_do_not_need_the_user` |
| A child the hub folds under its parent (`folded_under`) and a side session (`folded_as: "side"`) are no rows of their own: their cards sit under the parent's row, which waits through the children rollup or the sides it holds. One row, counted once. | `a_childs_card_counts_under_its_parent`, `a_side_counts_under_its_parent` |
| A session an Agent-folder hide rule matches (`hidden_by_directory`, `docs/protocol/protocol.md` ("Agent directory visibility")) is no row, and its cards count nowhere. | `a_directory_hidden_card_counts_nowhere` |
| A card of any kind makes its session wait: an MCP server's form (`elicitation`) does, and so does a question the assistant is still deciding whether it may answer for the user (`delegating`). Whether such a question should wait until it is handed to the user is D11 of `docs/plans/plan-derived-counts.md` ("6. Open decisions for maintainers"). | `an_elicitation_makes_its_session_wait`, `a_delegating_card_counts` |

- Structure: `{"version": 1, "cases": [...]}`. `input` has the hub's wire objects, `sessions` and `interactions` (the
  pending cards), with `mutes` as the hub's pins give them (`sessions`, `spaces`, `allowed`, each `<machine>/<name>`)
  and `quiet`, the `<machine>/<session>` of quiet tasks' agents.
- `expected.rows` lists the section's rows in order, each with its `id` (`agent:<machine id>/<session id>`, or
  `card:<interaction id>` for a card with no session) and its `cards` (interaction ids, oldest first);
  `expected.badge_rows` lists the rows the tab's badge counts, in the same shape and order (the rows less muted agents
  and quiet tasks' agents), and `expected.badge` is their number. The header and the filter show the number of rows.
- Sessions carry the hub's fields for folding (`folded_under`, `folded_as`, `children`) and hiding
  (`hidden_by_directory`), as a hub that folds sides (`sessions_side_fold`) sends them. Every case holds the whole
  list: what a bounded list leaves unloaded is not pinned here (`docs/plans/plan-derived-counts.md`, section 2).
- `known_differences` marks a case one client's counting fails today, by client (`ios`, `android`; "Known
  differences" below).
- All data is made up.
- Run: `AgentsNeedsYouFixtureTests` in `apple/PersonaTests` (iOS: the section, its filter and the tab's badge) and
  `cd android && ./gradlew :core:domain:test --tests '*AgentsNeedsYouFixtureTest*'` (Android: the rows and the tab's
  badge; the Agents page's Needs you section, its header and its filter count rows by the same rule, one per waiting
  session, through `agentNeedsYouRows`, which also applies the computer filter). The web app's Agents badge counts
  sessions in Needs you without quiet tasks' agents by a rule of its own, which Persona Link's summary shares
  (spec/fixtures/attention): it counts muted agents and no card on no session, so it does not replay these cases.
- Each replay reads the cards and sessions as its client's badge does. iOS (`AgentsNeedsYouFixtureTests`, and
  `HomeWidgetTests` for the Home Agents widget, which draws the badge's rows) folds sides under their parents as the
  lists land (`SideFold.fold`, `AppModel.allSessions`) and counts the badge from every pending card but a
  directory-hidden session's (`AppModel.directoryVisibleInteractions`), as `AppModel.countBadges` does. Android reads
  them through the functions `ShellCounts.agentRows` calls. Both assert `rows`, `badge_rows` and `badge`.
- With the counts report switch on (spec/fixtures/counts/README.md, "Lane reports"), both replays also write what they
  counted for each case, marks included, and `scripts/test/counts_agreement.py` compares the two lanes.

## Known differences

The fixture is the Agents count's definition (`docs/plans/plan-derived-counts.md`, section 3.5). A client whose own
counting fails a case is not changed to follow it here, since that would change the number it draws; the case carries
`known_differences.<client>` instead:

- `decision`: the decision that settles it, from the plan's
  `docs/plans/plan-derived-counts.md` ("6. Open decisions for maintainers"); `why`, in a sentence; `source`, the code
  that counts so.
- `replace`: today's values for the keys of `expected` it replaces (`rows`, `badge_rows`, `badge`).

That client's replay asserts the case at `expected` with `replace` applied; every other client asserts `expected`.
Both replays fail on a mark that names no decision, names a client that does not replay the fixture, or changes
nothing. Since a marked case is asserted at today's value, a client that comes to follow the case fails until its mark
is removed, so marks are only ever taken away. They stay with each client's frozen fallback until the plan's step 13
deletes both.

| Case | Client | Decision | Today |
| --- | --- | --- | --- |
| `a_directory_hidden_card_counts_nowhere` | Android | D1 | Android reads no `hidden_by_directory`: the hidden session and its card are a row, and the badge counts it (2, not 1) |
