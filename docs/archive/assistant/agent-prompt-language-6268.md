# Agent prompt language migration (#6268)

This is the caller/parser inventory made before implementation on 2026-11-18, based on main
`2b158adf0ad93a532d0ce64f18cc9e7fcc8e2109`. The prerequisite migration contract and wake exemplar (#5976 / #6300)
were merged. The issue progress comment records these phases before broad edits: manager wrappers and model inputs;
parser-coupled limit/peer delivery; remaining Chinese judge/closeout instructions and boundary tests; ratchet,
package/build/docs/privacy verification and PR. No task authority or policy placement was changed.

## Template and caller inventory

All paths below are relative to `server/internal/`; each filename in the first column is under `prompts/agents/`.
There were 46 templates, with ten Chinese-only ratchet omissions. Seventeen Chinese-bearing templates were
translated (the ten omissions plus seven prompts that previously supplied or inherited only a language hint).
The completed wake exemplar is unchanged. The remaining English templates contain established classifier,
protocol/replay, tool-result and task-policy guidance; they are not represented as translated by this change.

| Templates | Production callers | Boundary and treatment |
| --- | --- | --- |
| `answer.md` | `assistant/agents_work_act.go` via the answer helpers in `assistant/agents_triage.go`; `node/claudeoutside_ask.go` | Translate wrapper; preserve exact answers and question order, assistant reason and distinction from the user's answer. Node language arrives per RPC. |
| `auth_relayed.md` | `assistant/agents_auth_relay.go` via `assistant/agents_triage.go` | Translate notice at send time. Preserve app name; login completion does not grant task authority. |
| `delegate.md`, `delegate_input.md` | `assistant/delegate.go` | Capture one owner locale for both model inputs. Preserve owner rules, task decisions, requests, replies, questions and labels. Existing red-line and offered-option gates remain. Localized rule heading is local to this template; general shared-fragment migration remains #6275. |
| `triage.md`, `triage_input.md` | `assistant/agents_triage.go` | Translate instructions/wrappers. Preserve answered-decision records, verbatim question extraction, user approval versus task authorization, exact wait target/step and output schema. Input language is set from the same captured system language. |
| `report_line.md`, `report_line_input.md` | `assistant/report_line.go` | Translate instructions/wrappers using its existing single locale capture; retain report data, per-language length limits, clipping and latest-report protection. |
| `review.md` | `assistant/supervise_autoreview.go` | Translate reviewer instructions; preserve task, criteria, diff, paths, scope and exact `<review-findings>` schema. Read-only review and no-new-work boundaries remain. |
| `closeout_ask.md`, `closeout_brief.md`, `closeout_required.md`, `closeout_rules.md` | `assistant/agents_closeout_ask.go`, `assistant/agents_closeout_brief.go`, `assistant/agents_closeout.go`; rules included by all three | Translate instructions and rules using existing caller locale. Preserve closeout form, status/merged/deployed enums and evidence requirements. Keep the existing delivery-line literal used by the separate delivery contract. |
| `limit_continue.md` | `assistant/agents_work_tick.go`, `assistant/agent_switch.go`, both immediate/queued paths of `assistant/agents_switch.go` | Translate at send time. Add exact localized-message identity and retain old Chinese readback in `prompts/injected.go`; retain structured follow-up source. |
| `peer_message.md` | `node/claudepeer.go`, reached by sends and answers in `node/claudebg.go` and `node/claudeoutside_ask.go` | Translate wrapper, not user data. Devices overwrites optional RPC language; node captures it per call. `prompts/peer_message.go` strips all exact current-language heads and historical Chinese heads; `transcript/peer.go` applies this only for Persona. Preserve peer envelope, permission assertions and owner provenance. |
| `readonly_task.md` | `assistant/tasks_placement.go` | Translate the first-prompt prohibition at task launch. No file changes, commits or external writes are authorized. |
| `reanswer.md` | `assistant/fleet_delegate.go` | Translate delivery, preserve question/before/after data. Hash legacy text for dedup identity so locale changes/upgrades cannot resend the same answer. |
| `wake.md` | `assistant/agents_work_tick.go` via `assistant/agents_triage.go` | Already migrated by #5976. Reuse its marker, old transcript compatibility and user/task authorization variants unchanged. |
| `session_name.md`, `session_name_input.md` | `assistant/agent_naming.go` | Already English, with dialogue-language generation policy and bounded original dialogue. Unchanged. |
| `tool_loop.md` | `assistant/agents_tool_loop.go` | Already English; fixed STOP/CONTINUE JSON classifier, untrusted metadata policy. Unchanged. |
| `standing_actions.md`, `standing_actions_input.md` | `assistant/standing_authorization_judge.go` | Already English classifier; exact actions, verified directory and task/card data. Unchanged authorization registry and independent server gates. |
| `engine_handoff.md`, `engine_handoff_command.md`, `engine_handoff_message.md`, `engine_handoff_speaker.md` | `node/engine_handoff_build.go`, `node/engine_handoff.go` via `prompts/engine_handoff.go` | Already English replay wrappers; preserve user/assistant/tool role labels, clipped transcript, commands and permission ledger. Unchanged. |
| `codex_continuation.md` | `node/codex_takeover.go` | Already English bounded replay/context wrapper. Source history and identifiers unchanged. |
| `peer_stop.md` | `node/claudebg.go`; exact readback in `transcript/peer.go` | Already English, explicitly asks for a reply in the conversation's language. Keep exact stop recognition and stop-only behavior. |
| `question_cancelled.md`, `question_user_spoke.md`, `question_released.md`, `question_asked_later.md`, `question_defaulted.md`, `question_moved_to_todo.md` | `assistant/tools.go`, `assistant/ask_later.go`, `assistant/decision_deadline.go`, `node/hooks.go` | Existing English tool-result notifications; preserve cancellation, withdrawn question, no-invented-answer, default and task-link behavior. Unchanged. |
| `setting_unanswered.md` | `assistant/settings_tool.go`, `assistant/skill_import_tool.go`, `assistant/repository_add_tool.go`, `extmgr/self_settings.go`, `extmgr/subscription_tools.go`, `hub/self_settings.go` | Existing English refusal of a missing user approval. Unchanged. |
| `task_continuation.md`, `plan_first_task.md`, `task_leader.md`, `task_worker.md` | `assistant/tasks_placement.go` | Existing English task/policy instructions. Preserve same-to-do continuation, planning and leader/worker scope. Unchanged. |
| `task_handoff.md` | `assistant/task_handoff.go` | Existing English instructions with captured output language; retain verified scoped MCP/executable entry, no standalone-thread substitution, credential protection and task links. Unchanged. |
| `readonly_plan_exit.md` | `assistant/fleet_ix.go` | Existing English plan-mode/read-only refusal. Unchanged. |
| `task_successor.md` | `assistant/tasks_successor.go` | Existing English wrapper around previous Agent identity/reply and current original task text. Unchanged. |
| `dispatch_task.md` | `assistant/dispatch.go` | Existing English sender/assignment boundary around the original assignment. Unchanged. |
| `worker_supervise.md` | `assistant/task_leader.go` | Existing English supervision guidance plus source task/criteria/reports/evidence. Unchanged. |
| `launcher_repair.md` | `assistant/launcher_repair.go` | Existing English repair task with source launchers/errors and fixed scope. Unchanged. |

## Compatibility and verification boundaries

The original empty-language branch is preserved byte for byte; no existing Chinese golden is regenerated.
Explicit Chinese owner locale flows through `answerLanguage` as empty. Other supported locales without complete
translations use the English instruction branch while retaining the requested output language where applicable.
No locale change rewrites source history or owner rules. It does not change read-only tasks, approvals, criteria,
peer mode assertions, standing authorization or model choice. The node's language field is additive; old nodes ignore
it, while old managers omit it and new nodes retain Chinese. Old hubs do not infer the new continuation marker by
text alone; upgrade the hub before relying on localized source classification.

Synthetic caller goldens exercise actual triage/delegation/report requests, quota-reset and auth-relay sends,
read-only task starts, question answers, overrides, review/closeout variants and the real node peer inbox. Parser
checks cover legacy/current bodies, malformed markers, pasted user-authored text, app message ids, stop readback,
other peer senders and concurrent header use. Red-line and verbatim-question gates are still enforced after model
responses. These checks prove rendering, delivery and server rules, not a live model's semantic choices or real
user/service acceptance. The PR records commands, results and any unrun live-model or deployment gates.
