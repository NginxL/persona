# Approval clarification

An assistant-owned permission card can carry **Have a question** in addition to Allow and Deny. A question asks about the exact proposal on that card; it is not an approval decision and never executes, declines, closes, or cancels the operation.

## Owner flow

- Web shows a question editor when the interaction has `can_clarify`. `POST /api/v1/interactions/{id}/clarify` takes only `{text, client_msg_id}`. The trimmed question is at most 1,000 Unicode code points. An unchanged retry reuses its `client_msg_id`; a deliberate new question uses a new one.
- Feishu shows the same action only on a public card. Every press must be a fresh, one-time owner action bound to that channel, card, and permission kind. The answer run returns to the card's explicit chat and message target as well as the originating assistant conversation.
- The owner's original text is delivered to the interaction's exact machine and session. An archived conversation is explicitly reopened. A deleted conversation or ended room returns a visible error; delivery never falls back to another conversation.
- The hub issues a short-lived, one-time proof for an authenticated Web question, bound to the card, exact text, destination and `client_msg_id`. Feishu gets the same bound proof only after its fresh owner action succeeds. The opaque token travels only inside `session.send_once`; the hub consumes it before sending and the devices manager writes `owner_input`. A manager-written `owner_input`, caller or submission is never accepted. Missing proof on an older hub/manager combination keeps the previous unmarked delivery; proof never grants Apply or another owner action.
- A successful question returns the original tool call with a non-final Clarifying outcome. Its result says that the question is not an approval decision, that it arrives as a separate owner message to be answered then, and that the same card remains valid and must not be recreated. The card stays pending for Allow or Deny, and further questions are allowed.
- Private interactions remain in their private Web conversation: they are not drawn or answered in Feishu. Field cards may be questioned, but field values are never put in the question and channel Allow remains unavailable. Only the owner can clarify; an assistant, supervisor, answer-only principal, remembered grant, or standing authorization cannot execute the prepared operation.

## Prepared operation and compatibility

`approval.Operation` is the process-local continuation behind one immutable proposal. It contains Apply, optional Discard, replacement identity, and the existing Denied/Unanswered tool results. Public interactions gain only the additive `can_clarify` boolean; older clients ignore it and keep their existing Allow/Deny behavior. Clarification is an action, not a new `proto.Resolution`.

For a hub-owned setting, `fleet.ask_prepared` retains the executable handle in the hub and returns `approval.Outcome` to the manager. Only the opaque interaction ID crosses the trusted bus. `fleet.approval_apply` consumes that manager-owned handle once after a fresh owner Allow; `fleet.approval_discard` releases it without applying. A structured stale result survives the bus boundary. Existing `fleet.ask` callers retain one-shot approval behavior.

Immediately before Apply, the operation re-reads current state and verifies that it still produces the visible before/after proposal. A revision-checked operation keeps its original revision baseline rather than rebasing it. A stale proposal applies nothing and ends as stale/cancelled with a request for a fresh card. An equivalent newer proposal replaces and closes an older Clarifying card so both cannot execute. Subscription deletion uses an atomic revision predicate.

A prepared operation without a live origin conversation uses the older one-shot card instead: `can_clarify` stays false, a fresh Allow applies once, Deny or expiry applies nothing, and the original caller is the only terminal recipient. No approval journal or other conversation receives a fallback result. Legacy wait failures and pre-Apply validation errors say definitely that nothing was applied; an error after Apply begins remains uncertain. `repository_add` finishes argument and current-state validation before presenting its card.

## Timing and terminal delivery

Before the first question, the card keeps its original decision deadline, but the original tool call holds the turn for at most 75 seconds (or until that deadline when it is sooner). While an accepted question is being answered, the remaining decision time is paused. The pause has a cumulative 24-hour budget per card; repeated questions do not reset it. The turn that produced the card does not count as its answer turn, and a question accepted during an answer turn waits for the next turn before timing resumes.

Every pending snapshot carries the current effective `expires_at`: during a pause it is the remaining pause-budget deadline, and after all answers finish it is the resumed decision deadline with exactly the preserved time left. Same-ID interaction updates publish those changes without another arrival or push. Android replaces its card and delivered notification with that current deadline, including notification action metadata and system timeout. A genuinely expired current card still cannot execute; stale notification timestamps alone cannot revive it.

### Unanswered cards keep waiting (#10096)

A notification is only a way to tell the owner that a card is waiting; the card itself must still be there when they open the Persona app or the web. When no notification reaches their devices (no working push route, the app in the background), they may only look much later. So when a prepared card's 75-second initial wait ends unanswered while its caller still waits (or its original decision deadline arrives first), the coordinator keeps it (`keepWaiting` in `server/internal/assistant/approval_clarify.go`) instead of expiring it:

- it is recorded in the approval journal, as on a first question, so its final outcome reaches the original conversation;
- its deadline moves to the end of a 24-hour waiting window, and the apps are told the card again with that deadline, without a second push; the app and the web already load pending cards when they reconnect (`GET /api/v1/interactions`);
- the original tool call returns the non-final `approval.WaitingText`: nothing changed, end this turn, the card is still pending, do not retry or create another card, a later message reports the outcome;
- a later owner Allow applies once (the operation still re-checks current state), Deny changes nothing, and the end of the waiting window expires it without changing anything; each is reported in the conversation. A question on a waiting card pauses the remaining waiting time as on any clarified card.

A card whose caller already went away (a caller that bounds its own wait, such as the scheduled-check probe) is not kept: it expires at its decision window as before.

The coordinator makes one final claim under the fleet mutex. An in-flight question and a final Allow, Deny, expiry, or replacement are explicitly arbitrated. Allow executes at most once, Deny executes nothing, and a proxy-attributed Allow executes nothing.

Once a card has clarified, every terminal path sends exactly one result containing the literal interaction ID to the original conversation, including Allow, Deny, expiry, replacement, stale cancellation, and restart cancellation. Delivery uses the stable ID `approval-result-<interaction-id>` and the durable relay queue. The journal stores only the ID, original target, delivery state, status, and result text—never field values, callbacks, or executable closures.

Before the first question, a card whose caller still waits is normally kept waiting when its window ends ("Unanswered cards keep waiting" above). When it cannot be (its journal entry could not be written) or its caller already went away, an unanswered expiry returns through the original tool result instead of starting a second delivery. It contains the literal interaction ID, says the approval expired, and explicitly says nothing was executed. Expiry remains distinct from the owner's Deny: it never becomes a user refusal.

An unfinished Clarifying card fails closed on restart without executing. If the process stops after recording `applying`, recovery reports the execution outcome as uncertain rather than claiming success or reapplying it.

## Detached command approvals

A node command gate in an assistant conversation marks its permission card `detachable`. The manager validates that capability against the conversation and requires the complete tool input plus `execution_cwd`, the directory where the command actually runs (which can differ from the card's display directory). An older or incomplete request keeps the ordinary blocking approval instead of receiving a detached grant. After the same short initial wait, `interaction.create` returns `{dismiss:true,pending:true,approval_id,message}`: this is not a denial, no command ran, and the model is told to end the turn. The card stays pending under its authoritative interaction ID.

A later ordinary Allow stores a continuation grant in `assistant.detached_approval_grants` before its result is journalled. The result message asks the original conversation to retry the same tool once. The grant covers the exact machine, session, execution directory, kind, tool and complete input bytes. Consuming it deletes and persists the grant before returning Allow; a failed write allows nothing, a changed input does not match, and a second retry cannot reuse it. Replaying the original resumable interaction never consumes its own grant, including when an Always rule now covers the call. The returned `approval_id` keeps the sent-command ledger attached to the card the owner actually answered. An explicit Always allow remains a separate durable grant under the existing Always rules.

Continuation delivery is serialized with grant consumption. If a matching call consumes the grant before its result reaches the conversation, the delivered result says not to retry automatically instead of requesting a duplicate execution. A newer Always decision closes covered detached sibling cards: an original hook still waiting receives its ordinary answer, while an already released sibling creates no second grant and requests no automatic retry.

The manager never starts a second shell for this continuation. An editable command's grant carries `edited_text` into the ordinary node-side edited-answer path, so only the original body word is changed and the edited command is classified again. Deny and expiry store no grant. Grants expire after 24 hours and are bounded to the 128 most recent unused approvals. A restart loads the durable grant book but never replays an approval by itself; recovery revokes any grant whose approved result was not completely recorded, including an interrupted `applying` entry, before reporting that outcome as failed or uncertain. A disconnected resumable hook keeps its ordinary reconnect grace, and the reconnecting caller replaces the cancelled caller for the initial-wait timer.
