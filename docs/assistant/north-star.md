# North star: interruptions and availability

Persona's Agent environment is judged by two numbers (#4601):

- **Interruptions**: how many things reached the owner in a day. Fewer is better.
- **Availability**: how often the main conversation and the leader conversations could be started or resumed when
  something tried. Higher is better.

Both are read from what the assistant manager records anyway; nothing is sampled, mocked or read from a health check.
The code is `server/internal/assistant/north_star.go` (readout, recording, route, briefing lines) and
`server/internal/db/north_star.go` (queries).

## Interruptions

A day is the owner's calendar day in the assistant's time zone (`timezone` in the assistant's settings, else the
machine's), from midnight to the end of the day, or to now for today.

Five categories count, each only at the moment it was **first dispatched to the owner** (`tellUser`, the
`interactions.user_notified_at` marker added in #4443), not when it was raised or shown in an app:

| Category | What it is |
| --- | --- |
| `ask_user` | A conversation's own `ask_user` question card (`kind` `question`, `tool_name` `ask_user`). |
| `action_card` | Any other card the owner has to act on: an Agent's question, a terminal dialog (`screen`), a form (`elicitation`). |
| `authorization` | A tool permission card (`permission`) or a plan approval (`plan`). |
| `notify_user` | A `notify_user` call that a device took (work event `notify_user`): the hub handed it to a phone, computer or browser (`notify.request`'s `devices`, `docs/protocol/protocol.md` "Bus"). One a hub that does not report devices took counts too, as before. |
| `urgent_call` | A matter that rang the owner's phone (work event `urgent_call`, `urgent-call.md`), once however often it rang; `by_kind` says why it rang (`owner_asked`, `rule`). The pushes after a missed or declined call are not counted again. |

Each counted item has a source:

- `dot`: it came from the main conversation or a registered leader conversation (`assistant.leaders`).
- `task`: it came from a session working on a task (the card's `todo_id`, or the notification session's task).
- `session`: anything else.

The readout also lists each source row with its machine, session, task and card kind.

These are listed apart and never count as interruptions:

| Field | What it is |
| --- | --- |
| `supervisor_routed` | Cards routed to a task's supervisor first (work event `ask_routed`). |
| `supervisor_held` | Of those, the ones that never reached the owner. Only events that carry the card id (this build on) are matched. |
| `supervisor_escalated` | Cards a supervisor passed up (`ask_escalated`). If one then reached the owner, it counts in its category. |
| `auto_authorized` | Cards answered by a standing authorization (`interactions.auto_authorized_at`). |
| `probe` | Cards and notifications from a session whose working directory is under a temporary directory (`/tmp`, `/private/tmp`, `/var/folders`, the manager's `os.TempDir()`), where local tests and live probes run. |
| `notify_missed` | `notify_user` calls no device took when they were made, by why (the work event's `missed`, #8138): `no_device` (nothing could take it), `filtered` (every device that could turned it off), `kept` (only offline devices keep it for their next connection), `held` (push grading held it for the briefing and no connected device took it), `hub_away` (the hub was away; the manager's outbox sends it when it is back). Copies to chat channels with pushes on are not known when the call returns and do not change this. |
| `call_skipped` | Urgent calls that did not ring, by why (work event `urgent_call_skipped`): a gate stopped them (`off`, `no_quote`, `no_rule`, `no_happening`, `no_action`, `already_called`, `quiet`, `cap`, `spacing`) or no phone took the ring (`no_phone`). Each went out as a P0 notification instead, counted under `notify_user` when a device took it. A test call from Settings counts under `probe`. |

### Unknown, never zero

The manager writes `assistant.north_star_since` (`{at, build}`) once, the first time a build with this readout starts.
If the database cannot be read or written then, the manager tries again before each record it keeps, so the setting is
never missing while records exist. It is when recording began:

- A card raised before then without a dispatch marker counts as `unknown` in its category, because nobody marked
  whether it reached the owner. The readout does not guess.
- `notify_user` calls before then were never kept.
- When the day starts before recording began, `partial` is `true`. The briefing then says "partly unknown", or
  "unknown (not recorded yet)" when nothing at all was recorded.

## Availability

The conversations read are the main conversation (`assistant.main`) and every registered leader conversation
(`assistant.leaders`), as registered when the attempt was recorded (`role`) or, for older records, when it is read.

Each attempt is one real start or resume and its outcome. They are recorded as work events `conversation_send`
(`session_work_events`, kept 30 days like every work event):

- **The manager's own sends** (`origin: manager`): every `assistant.send` the manager makes. These are relays,
  supervision messages, scheduled runs and follow-ups, and new main conversations. They are recorded in `Service.call`.
  An attempt without a session id is a `start`, and anything else is a `resume`.
- **An app's message** (`origin: app`): the hub runs the send itself, then tells the manager how it went with
  `session.assistant_sent` (`docs/protocol/protocol.md`). Only a send that ran counts. A retry answered from the
  hub's record of the same operation does not. A message the manager refuses before the hub sends it (the hub's
  `assistant.send_params` call fails, for example on a model id that is not one) is a failed resume, recorded by the
  manager when it refuses.
- A manager send refused before it reaches the node, such as one whose launcher is out of the computer's scope, is a
  failed start or resume with that code as its reason.

The outcome is `ok`, `failed` (with a `reason`) or `unknown` (the message may have been typed; the hub could not tell).
`rate` is `ok / attempts`, so an unknown outcome is not a success. A failed start keeps the id it asked for
(`new_session_id`), so a leader conversation's failed start counts too. A failure's reason is grouped:
`conversation_not_found` (the runtime no longer has the conversation), `timeout`, `machine_offline`, `cancelled`, the
node's own error code, or `internal`.

Every attempt carries the manager's `build`. The readout groups by build (`by_build`), operation, role and origin. With
`split` it also gives the attempts before and from a time, such as a deployment.

### Coverage

Before recording began, only these say how a send went:

- The scheduled runs in `assistant.runs` (`origin: scheduled_run`). The setting keeps only the newest 200 runs, so a
  busy day loses its first runs. The gap `scheduled_runs_truncated` says so.
- In the command only, the hub's `client_ops` records of app messages sent with an operation id
  (`origin: app_ops`). The hub's database belongs to the hub, so the route cannot read it. A record left `unknown` whose
  answer holds an error code counts as failed with that reason.

`gaps` names what the window cannot see:

| Gap | Meaning |
| --- | --- |
| `manager_sends_before_recording` | The manager's relays, supervision messages and new-chat starts before recording began were never kept. |
| `app_sends_before_recording` | App messages before recording began (the route; the command reads them from `client_ops`). |
| `scheduled_runs_truncated` | `assistant.runs` no longer reaches back to the window's start. |

Not covered:

- An app's message while the manager is not connected to the hub: the hub answers `assistant_offline` and the
  manager never hears of it, so the attempt is missing rather than failed.
- The processes the node restarts on its own without a send (compaction, a background task waking the conversation).
- Pushes other than `notify_user` (turn-finished pushes, task notices).

## HTTP contract

`GET /api/v1/assistant/north-star` belongs to the assistant manager and needs the full-access role (403 otherwise).

| Query | Meaning |
| --- | --- |
| `date` | `YYYY-MM-DD` in the time zone, today or earlier (default today, up to now). |
| `split` | An RFC 3339 time; availability is also given before and from it. |
| `tz` | A time zone name overriding the assistant's. |

A bad query answers 400 and a role without full access 403 (`http-status.forbidden`). A database that cannot be read
answers 503. The answer (abridged):

```json
{
  "date": "2026-11-11", "time_zone": "Asia/Shanghai",
  "since": "2026-11-11T00:00:00+08:00", "until": "2026-11-11T15:00:00+08:00",
  "recording_since": "2026-11-11T10:00:00Z", "recording_build": "<build>",
  "interruptions": {
    "total": 5, "partial": true,
    "categories": {
      "ask_user": {"count": 1, "by_source": {"dot": 1}, "by_kind": {"ask_user": 1}, "unknown": 3},
      "action_card": {"count": 2, "by_source": {"task": 2}, "by_kind": {"question": 2}, "unknown": 0},
      "authorization": {"count": 1, "by_source": {"session": 1}, "by_kind": {"permission": 1}, "unknown": 0},
      "notify_user": {"count": 1, "by_source": {"dot": 1}, "by_kind": {"notify_user": 1}, "unknown": 0},
      "urgent_call": {"count": 0, "by_source": {}, "by_kind": {}, "unknown": 0}
    },
    "excluded": {"supervisor_routed": 2, "supervisor_held": 1, "supervisor_escalated": 1, "auto_authorized": 1, "probe": 2, "notify_missed": {"no_device": 1}, "call_skipped": {"cap": 1}},
    "sources": [{"category": "action_card", "source": "task", "kind": "question", "machine_id": "<machine>",
                 "session_id": "<session>", "todo_id": "<task>", "title": "Fix the build", "count": 2}]
  },
  "availability": {
    "attempts": 6, "ok": 2, "failed": 3, "unknown": 1, "rate": 0.333,
    "conversations": [{"session_id": "<main>", "role": "main"}, {"session_id": "<leader>", "role": "dot"}],
    "by_op": {"resume": {"attempts": 5, "ok": 2, "failed": 2, "unknown": 1, "rate": 0.4}},
    "by_role": {}, "by_origin": {}, "by_build": {},
    "failures": [{"reason": "conversation_not_found", "count": 2}],
    "split": {"at": "2026-11-11T12:00:00+08:00", "before": {"attempts": 3, "ok": 1, "failed": 2, "unknown": 0, "rate": 0.333},
              "after": {"attempts": 3, "ok": 1, "failed": 1, "unknown": 1, "rate": 0.333}},
    "gaps": []
  }
}
```

## Command

`personad assistant north-star [--date YYYY-MM-DD] [--split RFC3339] [--tz Area/City] [--brief en]` runs the same
readout against the built-in manager's database without a running manager or a migration. The command:

- opens the databases with read-only transactions (`default_transaction_read_only`), so Postgres refuses any write;
- also counts the hub's `client_ops` app messages from before recording began;
- with `--brief`, prints the briefing's opening lines in that language instead of JSON.

Use it for a baseline from a build that never recorded anything.

## Weekly view

`personad assistant north-star --weekly [--date YYYY-MM-DD] [--tz Area/City]` reads four weeks of seven calendar days,
ending at the start of the reading day. `--date` defaults to today and `--tz` follows the same setting as the day
readout. For example, a reading on 2026-11-04 covers 2026-10-07 through 2026-11-03. Calendar-date arithmetic keeps a
week correct when the clocks change. `--weekly` cannot be combined with `--split` or `--brief`; without `--weekly`,
the command's output is unchanged.

The command opens the same databases with read-only transactions and runs no migration. It is intended for a person
to run on a fixed day each week. The JSON has `reading_day`, `time_zone`, four `weeks` (oldest first, with inclusive
`start` and `end` dates), and an ordered `rows` list. Seven rows have four `cells`, one per week;
`sessions_that_may_be_gone` has one `snapshot` at reading time instead. Cells contain only aggregates: no ids, titles,
paths, names, session ids or to-do ids.

A cell is a number or aggregate object, or a fixed result word. `unknown` means the records cannot answer it;
`not enabled` means the current switch is off; `not probed` means no probe was made; `not recorded` means the input
was not recorded; `too few` means the denominator is below the stated floor; `to-do deleted` means a required to-do
no longer exists. None of these words is a zero. A missing table or column needed by a row is `unknown`, as is a week
before `assistant.north_star_since` or outside the 30-day work-event retention for rows that read those records.
An unreadable `manager.json` makes only the review and dispatch rows `unknown`.

The weekly view does not detect stretches while the manager was stopped, whether a workspace folder still exists, or
an Agent replaced on the same to-do. It adds no event, setting, table, migration, HTTP route or client change.

### Registry

This is the single definition of each weekly count. Other documents cite these ids instead of restating their rules.
Thresholds are proposals for maintainers, not decisions made by the command; changing a threshold changes its registry
row, not the reader. A later count gets a registry row before it gets a reader.

| id | Definition, source and fields | Window and inconclusive cases | Proposed threshold |
| --- | --- | --- | --- |
| `interruptions_per_finished_todo` | `interruptions` uses the day readout's rules; `n` is current to-dos closed and not cancelled, excluding failed starts and counting concerns. `ratio` is interruptions / n; `from_tasks` and `share_from_tasks` use the day readout's task source; `reopened_within_24h` deduplicates `todo_reopened` by to-do and includes exactly 24 hours. It excludes reopened cancellations and retried failed starts. Older events record the close reason but cannot distinguish a failed start from a manual close. Sources: `interactions`, work events and `todos`. | Four weeks. Recording/retention/schema gaps: `unknown`. n < 10: `ratio: too few`; no interruptions: `share_from_tasks: too few`. This reopen count includes manual and automatic closes, unlike `wrong_done.reopened` (judge closes only), and counts the reopen event, unlike supervision-by-model's reopen assigned to a settled acceptance. | A4's number, read against a baseline week; no threshold of its own. |
| `workspaces_of_closed_todos` | For the same closed to-dos, use the started task that holds the execution binding. A reserved or reused to-do id is a fallback when no execution binding survives. `created`, `reused` and `shared` come from `workspace_resolved`; `existing` and `none` from the workspace mode; missing plan, task or resolution is `not_recorded`. `n` is their sum. `still_there_and_clean` is always `not probed`. | Four weeks. Missing schema: `unknown`. Buckets always add up to n; no filesystem probe is made. | Evidence only; no threshold. |
| `independent_review` | `by_result` counts the latest stored `todo_supervision.auto_review` per to-do by ended result. `runs_started` unions reviewer links and stored starts by run id, including a run that failed before its reviewer linked. Each run is assigned to one week; a non-zero stored start takes precedence over the reviewer link time. `lower_bound` is always true because older results are replaced. The launcher comes from `manager.json` `agents.review.launcher`. | Four weeks. No launcher: `not enabled`; unreadable config or missing schema: `unknown`. Results are a lower bound; runs that left no link or latest record cannot be recovered. | Evidence only, never a reason to change the reviewer. |
| `dispatched_tasks` | Count successful records in `agents.dispatch.tasks`; the switch is `agents.dispatch.enabled` in `manager.json`. | Four weeks. Dispatch off: `not enabled`; unreadable config: `unknown`. A week pruned by the newest retained dispatch's seven-day horizon, including a week starting exactly at that cutoff, or a 200-entry list whose oldest record is after the week's start, is `unknown`. An empty history is a real 0. Four weekly readings are needed for four weeks. | With dispatch on, at least one permits proposing per-task state and the three dispatch events below; four readings of 0 do not. |
| `conversation_not_found` | Sum the day readout's `conversation_not_found` failures for the main conversation (`main`) and leader conversations (`leaders`), including its read-only baseline sources. `lower_bound` is always true. | Four weeks. Recording or work-event retention gap: `unknown`. Each week equals the sum of its seven day readouts. | Evidence only; no threshold. |
| `continuations` | Count historical `task_brief_todos.continues` links by `linked_at`. Current builds run a continuation on its existing to-do and write no new link of this kind. | Four weeks. Missing table/column: `unknown`. Zero is `not recorded`, because same-to-do replacement leaves no continuation link and is therefore inconclusive. | Historical evidence only. The counting-rules decision in #11399 is open. |
| `sessions_that_may_be_gone` | One upper-bound snapshot: `claude` and `agent_empty` count non-review `todo_agents` links older than 30 days; `reviewers` counts reviewer links separately; `archived` counts `sessions.archived` records still archived whose session activity is older than 30 days. | Reading time only. Missing schema: `unknown`. These may be gone, not proven gone; `upper_bound` is always true. | 0 in three consecutive windows means a gone-marker is not built; any other number decides nothing. Transcript retention remains a maintainer decision. |
| `workers_agent_difference` | For each `source=worker` link, compare its Agent with the latest `source=leader` link named by `via` (the leader's to-do, not its session). Output `same`, `different`, `not_recorded` and `n`. | Four weeks by worker link time. Missing schema: `unknown`; no worker rows: `not recorded`. A missing leader or an empty Agent on either side is `not_recorded`, never `same`. | A task-spawn model preset may be shelved only with recorded pairs, no differences and no reported task-leader limitation. Empty or missing Agent kinds decide nothing. |

### Proposed, not recorded yet

The following registry rows reserve definitions for later changes. **This change writes none of them.** Each future
writer is the assistant manager, carries no message content and has a review date; an uncited kind is removed then.
Their rows must not be read as evidence that the event or marker exists.

| id | Future source and fields | Proposed reading and threshold |
| --- | --- | --- |
| `manager_start` | Once per manager start; no fields. | No threshold. It lets a stretch with the manager stopped read `unknown`. |
| `queue_refused` | A queued message leaves delivery after a coded refusal: `code`, relay `kind`, and `to` (`background`, `main`, `sender` or `none`). Not written for an immediate `conversation_send` refusal. | Of refused supervision messages, count those not reaching the main conversation within 30 minutes. With events, 0 supports the route; a full window with none means older-to-do re-delivery is not built. |
| `agent_dispatched` | A later dispatch stage starts a dispatch; no fields. | With dispatch on, no such event supports shelving automatic result return. |
| `dispatch_settled` | A dispatched task's round ends; whether the dispatching Agent is still stopped on that round. | Read with `result_read`; no automatic return when settled dispatches are read as working less than once each on average in model rounds. |
| `result_read` | `dispatch_read` or `agent_read` answers: `tool`, returned state, bytes, seconds waited, and whether a model round or `personad tool` made the read. Command-line reads are excluded from the model-round average. | Otherwise maintainers decide; dispatch off is inconclusive. |
| `record_unavailable` | `{surface, state}`; state is `unreachable`, `gone`, `not_recorded` or `unknown`, at most once per to-do/session, surface and state per day. Surfaces are `work_details`, `result_file` and a client-observed `session` rechecked by the manager. | For work details/result files, 3+ `gone` or `not_recorded` in a window goes further; two windows of 0 stop at the first step, but 0 is inconclusive until a released client reads them. For sessions, any `gone` builds the marker; 0 is inconclusive. |
| `review_run` | An independent review ends: blocking count (empty, not 0, on failure/cancellation), and launcher, requested model and reply-reported model for reviewer and working Agent. | Evidence only. Compare same/different launcher or reported model groups only with at least 30 runs in each; never group by provider. |
| `todo_priority_sample` | Once a day with no session: `undated` To-do rows and `high` among them; Monday's sample is read. `high` is an upper bound because its author is unknown. | A priority menu needs rank-drawn rows and `high >= 1` in three of four weeks; four later weeks of 0 rule it out. A sort key needs two weeks with `high >= 3`. Missing samples, `undated < 3`, or a different sort is inconclusive. |
| `session_file_gone` | A long-lived marker on a to-do link or archive record, set once and cleared by the next successful read; not a 30-day event. | Remove it if unused in its first year. A retention raise works when no newer session carries it; earlier readings are inconclusive. |
| `device_connection_tally` | Device-only, never in this JSON: app opens with network but no connected address by four results, and successful connections by trust method and route. It records only while Developing Persona is on and leaves the device only by export. | Two baseline weeks first. Pinned success is fewer plaintext/system-trust addresses. No pinned address across four weeks means pinning is unnecessary; indistinguishable failure kinds need no split. Local-proxy/private-tunnel TLS needs fingerprinted pairings actually using those routes. Switch-off time is inconclusive. |
| `weekly_view_reading` | Whether the fixed-day reading was posted to the issue it informs after this view ships on the maintainer's hub. | Four posted readings permit later event kinds under their registry rows; none means none are added; one to three means read another window. `not enabled`, `not probed` and `too few` are inconclusive, not rejections. |

## Daily briefing

Two kinds of task open with the north star:

- A routine with `facts` on (the morning and evening briefings), as the first part of its facts.
- A daily report (`report` on the task), such as a leader conversation's: its runs get the north star's lines alone.
  Set it with `schedule_routine`'s `report`, or with `report` in the body of `PUT /api/v1/assistant/tasks/{id}`; a
  change that leaves it out keeps it. Any other task, such as a daily reminder in the same conversation, gets nothing.

The run reads today until it starts, and yesterday from its start until the same wall-clock time as the comparable
period (on a day the clocks moved, that is not the same length). Its lines (`assistant-north_star.*` strings):

```text
Interruptions today: 1 (0 questions · 0 cards to act on · 1 notifications · 0 authorization cards); yesterday by this time 3, -2
Availability today (main and leader conversations): 75% (3 of 4 starts and resumes worked); yesterday by this time 50%, +25 pp
Failures by reason: conversation not found 1
Source: Persona's own records: cards and notifications that reached you (cards a supervisor took, standing authorizations and test probes excluded), and every start and resume of the main and leader conversations.
```

The change against yesterday is a number only when both days were recorded the same way: each day was recorded from its
start (`partial` is false) and neither leans on what was kept before recording began (a gap ending in
`_before_recording`). Otherwise the line says "not comparable with yesterday". Failure reasons are said in the report's
language (`assistant-north_star.reason_*`); a node's own code without a text reads "other (code)".

The same lines appear in three places:

- The Lark card's first section, before "Today's confirmation cards".
- The top of the facts the model is given (`prompts/assistant/routine_facts.md`), which tell it to open the report
  with them.
- The `brief` feed item's `north_star` (`lines` and the numbers; a number is `null` when unknown).

A failed read says the numbers are unknown and never shows zeros.
