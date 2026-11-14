# Side-session fold fixtures

A Claude desktop app side session (a side chat, a /btw question, a session spawned for a task; the node sets
`parent_agent_id`, #5935) lives in its parent session's chat (#7944): the manager folds it under the parent's row as
a side, and every client lists, counts and routes it the same way (`docs/design/PRINCIPLES.md`, "Consistency first"
and "Checkable rules"). These cases pin the fold the manager computes and what a client does with it.

| Rule | Cases |
| --- | --- |
| A row that is no child and no Assistant row, whose `parent_agent_id` names another listed, non-Assistant row on the same computer, folds under that row's topmost listed ancestor with `folded_as: "side"`. The parent need not run. | `side_folds_under_its_parent`, `an_offline_parent_still_holds_its_sides` |
| A side of a side folds under the same topmost row, also as a side. | `a_side_of_a_side_folds_under_the_topmost_row` |
| A row folded through a side, and not that side, names the nearest one in `folded_side`: its cards are answered in that side's chat. | `a_side_child_belongs_to_the_side` |
| Side wins over tab worker; a child keeps folding by its parent fields. | `a_side_wins_over_a_tab_worker`, `a_child_with_parent_agent_id_folds_as_a_child` |
| No fold when the parent is archived, not in the list, on another computer or an Assistant row, when the side is an Assistant row, or when the links loop. | `an_archived_parent_leaves_its_sides_as_rows`, `a_parent_gone_from_the_list_leaves_its_side_a_row`, `assistant_rows_are_on_neither_end`, `sides_that_loop_fold_nothing` |
| The parent's `children` counts its sides in `total`, `working` and `needs_you` like any child, and apart in `sides`, `sides_working`, `sides_needs_you` and `sides_unread` (each omitted when 0). | every case with a fold |
| Folded state: `needs_you` when the row or any folded row needs you; else `working` when a folded row works (not while the row is `unread` or awaits the user's reply); else `unread` when `sides_unread > 0` and the row does not work itself (sides only); else its own state. | `a_side_that_needs_you_makes_its_parent_need_you`, `an_unseen_side_shows_on_its_parent`, `a_working_parent_stays_working_beside_an_unseen_side`, `a_codex_child_result_does_not_make_its_parent_unread`, `a_paged_row_with_only_the_rollup` |
| A card's home is the row it shows under (`folded_under`, else its own session); its chat is the side it belongs to (`folded_side`, or the asking session itself when that is a side), else its home. | the cases with `interactions` |

- Structure: `{"version": 1, "cases": [...]}`. `input.sessions` are the rows as the node lists them (states already
  worked out), before the manager folds them; `input.interactions` the pending cards. With `input.paged: true` the rows
  are a paged list's as the hub sends them, already folded (a row's `children` stands for sides not on the page):
  nothing is folded again.
- `expected.fold` is what the manager adds, by `<machine id>/<session id>`: `folded_under`, `folded_as`, `folded_side`
  and `children`, each absent when empty; a row not named carries none. A client applies it to the input rows to get the
  rows the hub sends.
- `expected.rows` are the rows a `roots=1` list has (those not folded under a listed row, archived ones left out), in
  input order, each with its folded `state`. `expected.cards` gives each card's `home` and `chat` as
  `<machine id>/<session id>`.
- All data is made up.
- Run: `TestSideFoldFollowsTheSharedFixtures` in `server/internal/assistant/fleet_fold_test.go`. Each client adds a test
  reading these cases (#7944).
