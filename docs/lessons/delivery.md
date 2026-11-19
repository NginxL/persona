# Dispatching work and delivery

A retrospective from 2026-11-05: 8 machines and 20-plus Agents worked for a day, and only 6 items actually shipped. Most stopped at "opened a PR", "merged but off by default" or "not deployed".
What made it into the product is in `docs/assistant/todos.md` ("Delivery") (#3306, the assistant setting `done_needs_delivery`, off by default; that section says how to turn it on).

## Why work stopped halfway

1. **The definition of done was wrong**: the dispatch prompt said "open a PR / merge", the Agent did exactly that and reported done; the hub's completion check (`assistant/task_done.md`) also counted "merged the PR" as done and closed the task automatically, so what the user saw as "Done" had not actually shipped.
2. **Things needing the user's OK popped up piecemeal**: merging, deploying, turning on an off-by-default feature, sending messages outside. Each task asked separately, scattered across different chats and cards, with no single place to approve them all at once, so they just sat there.
3. **Nobody chased work blocked elsewhere**: CI runner backlog (one wait was over 40 minutes), waiting for outside reviewers. The Agent stopped with "waiting for CI" and that was the end; nobody came back to check.
4. **Capacity went into fixing Agent infrastructure**: many Agents were fixing launchers, tunnels and auth, the things Agents themselves need to run, while the real requirements waited.
5. **No real environment**: with no environment to deploy to and test in, verification could only say "untested", and finished work could not ship.

## Countermeasures

- **Done = shipped and tested, with readings attached**: say so when dispatching (the hub automatically appends the delivery standard to non-read-only tasks). Work that stops at PR / merge must say which gate it is stuck at and who clears it; off-by-default features must state how to enable them.
  When the hub judges completion it also judges delivery: it closes only tasks that shipped or never involved shipping, and keeps the rest open with the blocker written on the to-do.
- **Fixed format for the last line**: `交付：…；卡点：…；谁来解：…` (Delivery: …; Blocker: …; Who clears it: …). A model reading natural language is easily fooled by "merged"; a fixed line gives both the judgment and the statistics something solid. When the model's claim contradicts this line, the evidence in the body wins.
- **Batch the items waiting on the user**: the hub groups tasks where "who clears it = the user", the assistant lists them to the user at once, the user approves in one sentence, and the assistant then tells each Agent. Do not let every Agent raise its own question.
- **Chase work blocked elsewhere**: for CI queues, waiting on people and the like, first try to clear it yourself (rerun once, switch machines); if you cannot, say who can. Follow-up belongs to the main chat's supervisor (#3124); the `gate` recorded when judging delivery is its input.
- **Count infrastructure work separately**: tasks that fix the Agents' own runtime environment get a cap. After half a day, stop and change course (another machine, another model, handle it manually) so it does not crowd out real requirements.
- **Prepare a real environment first**: before dispatching work that must ship, confirm there is an environment to deploy to and test in (test hub, simulator, real device); if not, make "set up the environment" the first task instead of letting every task write "untested".

## Keep a mirror claim separate from its facts and retry status (#10290)

A claimed scheduled-briefing completion is already handled by its dedicated mirror, including while the post is
pending or being retried. Returning only "no facts" for that state lets a repeated callback send a generic card
under a different UUID. Check the retained turn claim before the facts TTL, and distinguish handled from ordinary
follow-up turns. During an existing mirror retry, unreadable channel status is not proof that pushes are disabled:
consume that bounded retry budget and recheck before posting. Keep the same card and key; do not claim restart or eviction
deduplication from this memory-only state.

## Stage evidence is not runtime state (#3380)

The final handoff alone misses progress while an agent still works or waits on background jobs. Observe complete assistant replies from session lists and followed items too, but never use user text, tool output or partial deltas as delivery evidence. Skip list snippets ending in an ellipsis and items marked with `FullSize`; never fetch full transcripts in the session-list event queue. Final handoffs and complete followed items supply the missing evidence. Keep list-row deduplication to avoid repeated deploy checks.

A pending plan-approval card is reliable; `working` and a reply mentioning a plan are not. Show plan as a temporary view of a pending `IxPlan`, and recompute it when agent ownership changes, including the old task. Stored progress is forward-only, including delivery writes; only an explicit review can withdraw verification. Run the deploy gate before applying stored delivery to an unconfirmed keyword guess, and never cap a confirmed deploy with old delivery. Record explicit verification only after acceptance is saved, not when a supervisor submits `accepted` while the owner still needs to verify. Default to code unless read-only or the acceptance criteria explicitly describe non-code work without code wording.

Every path that closes a task has to apply the delivery rule, not only the judge's finish: a supervisor's `task_review accepted` used to close a task with an open draft PR while `done_needs_delivery` was on and the stage ledger already said "not in the running hub" (#6428). Acceptance criteria that only ask for a PR do not override the switch.

## Not done yet

- The iOS / Mac to-do cards do not yet show where delivery is stuck (web and Android already do).
- "Waiting on the user" is still just a group in the assistant snapshot; there is no card where you can tick items one by one and approve them all with one tap.
- Following up on blockers (checking CI again at a set time, nudging reviewers) is handed to the supervisor (#3124): when a supervised task has not been delivered, the supervisor message includes the blocker, but there is no automatic check-back at a set time yet.
- There are no statistics or alerts yet for the share of infrastructure tasks or their cap.
