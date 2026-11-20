# To-do counts fixtures

The numbers of the To-dos list and its places (#10226; `docs/design/proposals/todos-redesign.md` 3.3 and 3.7): one list
of to-dos → `counts` (`TodoCounts`, top level on `GET /api/v1/todos`) and the places (`TodoPlaces`, the answer of
`GET /api/v1/todos/places`), both in `schema/wire/todo-view.schema.json`. Each to-do here is placed as in
`spec/fixtures/todo-view`; most are the same rows. The manager's projection answers every case exactly
(`server/internal/assistant/todo/projection/fixtures_test.go`); `scripts/test/test_wire_schema.py` holds the expected
values to the schema and the generated types read them back unchanged (the tests named in
`spec/fixtures/todo-view/README.md`).

| File | What it pins | Cases |
| --- | --- | --- |
| `todo_counts.json` | A list → its counts, places and order | 8: an empty list, one to-do in each section, a failed start counted open, a superseded failed start, a step counted once, rows ranked in every section, rows ranked by what their steps carry, runs that wait counted In progress and ranked by their wake |

## Format

`{"version": 1, "cases": [...]}`; a case is `{name, description, source, input, expected}`.

- `input.now`, `input.sessions`, `input.loc` (optional): as in `spec/fixtures/todo-view`.
- `input.todos`: the list exactly as `GET /api/v1/todos` lists it (`listedTodo` rows).
- `input.someday_until` and `input.restored_at` (optional): by to-do id, what `spec/fixtures/todo-view` gives one to-do.
- `input.projects` (optional, added here): the knowledge base's project titles by slug. A row carries only its
  project's slug, and a place is titled by its project.
- `expected.counts`: the `TodoCounts`. `expected.places`: the `TodoPlaces` answer, whose `counts` equal
  `expected.counts`. Compare as JSON values.
- `expected.order` (added here): every to-do of the list by its view's section, in the order of its `rank`. Required
  for every case with to-dos (optional only for an empty list): the iOS app files rows from it to replay the counts
  (`apple/PersonaTests/TodoCountsFallbackTests.swift`), and the hub's projection test checks it against the ranks.

All data is made up.

## The rules the cases pin

- `open`: every to-do that is not closed, in any section but Someday. A failed start is open, though its
  `done_at` is stored; a ready or `finished_seen` to-do is open.
- `needs_you` (N): the to-dos in the Needs you section, each once; Can wait and quiet tasks (`quiet`) are left out, as
  the badge leaves them out today (`docs/assistant/todo-status-sop.md` "Header count").
- `in_progress`: the rows of Working and Waiting. Just finished is not counted.
- `sections`: the rows of each section, every `TodoListSection` key present, 0 included. A quiet task counts here as the
  row it is, so `sections.needs_you` can be larger than `needs_you`: where N is drawn, clients show `needs_you`.
- A step its parent carries is counted with its parent only (proposal 3.4 "Steps roll up", "counted once"): a to-do
  whose `part_of` names an open to-do of the list adds to no count and to no place, its parent standing for both. A
  step whose parent is closed or not listed counts as the row it is.
- A failed start whose work finished in a related to-do is closed (`superseded`), not open: related to-dos share a
  plan, a PR, an issue number in the title or the title itself, closed ones with closed ones
  (`docs/assistant/todo-status-sop.md` "Related to-dos").
- `places`: `all`, `today`, `upcoming`, then one place for each project with an open to-do (as `open` counts), ordered
  by slug (bytes, not a locale). `all` carries N; `today` and `upcoming` carry 0, since they draw no count (3.3); a
  project carries its part of N, counted the same way. Fixed places have the title `""`; a project's title is its
  `input.projects` title, else its slug. A manager's own answer has no `manager` field: the hub stamps it when it merges
  several managers' places, and adds their counts up field by field and key by key.

## Judgement calls

- Quiet tasks are left out of `needs_you` only, not of `sections` or `open`, which count the rows the list shows.
- `today` and `upcoming` say 0 rather than a number nobody draws, so their bytes need no Today rule yet.
- Projects are ordered by slug, which stays stable across languages and managers.
- **A project whose open to-dos are all in Someday gets no place** (`reading` in `mixed_list`): places follow `open`,
  which leaves Someday out, while proposal 3.3 says only "each knowledge-base project with open to-dos". Counting
  Someday in would keep such a project in the sidebar; change `mixed_list` with this line.
- **A project the knowledge base does not name still gets a place**, titled by its slug (`errands` in
  `failed_start_counts_open`), so its to-dos stay reachable from the sidebar; 3.3 lists knowledge-base projects only.
- **The order within a section** (`rank`, proposal 3.4 "Within a section"; `ranked_list`, `ranked_steps`): Needs you
  and Can wait by the soonest deadline (the earliest of the deadlines of the decisions on the row, a step's included,
  and the to-do's own due), then the oldest ask (`since`); Working by the latest activity, its steps' included;
  Waiting by the soonest wake (a park's end, a usage limit's reset plus its grace, the hub's next wake of a run that
  waits on another Agent or an outside event, the leading step's when a step leads the row), rows with none (an open
  PR, a run's wait with no `wake_at`) last, then the longest waiting; To do by due (overdue, today, later), then
  priority (high, normal, low), then the newest; Just finished and Done newest first (`since`); Someday by the soonest
  return, no date last, then the newest. Ties go by id, so the order never depends on the list's. The proposal's "the
  owner's order" for To do has no input yet: priority and age stand in.
- **A run that waits ranks by the hub's next wake of it** (`waiting_ranked_by_wake`): its `work.wake_at`, when the hub
  wakes it to check on an outside event or, for another Agent, looks again if that Agent's turn has not ended by then.
  It is the same kind of moment as a park's end, when the supervisor looks again, so the two rank together. A wait with
  none (a named wait the hub schedules no wake for, its wake switch being off, or a wake so overdue that the hub
  dropped it) has no known wake and goes after those with one. Such runs count in `in_progress` as the Waiting rows
  they are. A wait the run ends by itself (background commands, a `/loop`) is Working, not Waiting.
- Every to-do is ranked within its section, a step its parent carries too: clients sort the rows they draw by rank,
  and a gap left by a step they do not draw changes nothing.
- Not pinned yet: Today's and Upcoming's own rows. What the hub does across several managers has its own test
  (`server/internal/hub/todo_view_test.go`): it adds the counts up and joins the places, ranking nothing again.
  `rank` is worked out per manager's list, and the hub only joins the lists, so a merged section can hold equal ranks:
  clients order by `rank`, then the manager, then the id.

## Run

```sh
(cd server && go test ./internal/assistant/todo/projection)
python3 scripts/test/test_wire_schema.py
(cd server && go test ./internal/assistantwire -run TodoView)
```
