// Task-review decisions use the existing row and draft actions, including legacy reviews without an acceptance ask.
// Dismissal is round-bound and persisted by the manager; Later only defers this browser session, as on reply drafts.
import { openTask } from "../../lib/handoff";
import { useRef, useState } from "react";
import { t } from "../../i18n";
import { api, credentialOwner, enc, errorText } from "../../lib/api";
import { decisionLaterKey, decisionRound } from "../../lib/assistant-entries";
import { askRow, later } from "../../lib/feed";
import { defaultManager, MANAGER_HEADER, shownManager } from "../../lib/manager-choice";
import { getState, putTodo, refreshTodos } from "../../lib/store";
import { latestReview } from "../../lib/supervision";
import { askOptions, openAsk, type Todo } from "../../lib/todos";
import { Button, toast } from "../../ui/atoms";
import { AcceptRow } from "./Brief";
import { BriefingMark } from "./BriefingMark";
import { DecisionRow } from "./DecisionRow";
import RuleSuggestionLine from "./RuleSuggestionLine";
import type { RuleCard } from "../../lib/rule-suggestion";

type Review = NonNullable<Todo["decision_review"]>;

export function TaskDecision({ todo, rowKey, hideTitle = false }: { todo: Todo; rowKey?: string; hideTitle?: boolean }) {
  const ask = openAsk(todo);
  const review = latestReview(todo.supervision);
  const round = decisionRound(todo);
  const admission = useRef(false);
  const [busy, setBusy] = useState(false);
  const [dismissing, setDismissing] = useState(false);
  const owner = credentialOwner();
  const manager = todo.manager || defaultManager();
  const selection = shownManager();
  const current = () => owner === credentialOwner() && selection === shownManager();
  const open = () => openTask(todo.id);
  const apply = (state: Review) => {
    if (!current()) return;
    const now = getState().todos.find((t) => t.id === todo.id && (t.manager || defaultManager()) === manager);
    // An old write cannot hide a newer review that arrived while it was pending.
    if (now && decisionRound(now) === round) putTodo({ ...now, decision_review: state });
    void refreshTodos();
  };
  const write = (undo?: string) => api<Review>(`/todos/${enc(todo.id)}/decision-${undo ? "undo-dismiss" : "dismiss"}`, {
    method: "POST", json: { review_at: round, ...(undo ? { dismissal_id: undo } : {}) },
    headers: { [MANAGER_HEADER]: manager }, isAuthContextCurrent: current,
  });
  const dismiss = async () => {
    if (admission.current || !round || !current()) return;
    admission.current = true;
    setBusy(true);
    setDismissing(true);
    const deferredKey = decisionLaterKey(todo);
    later(deferredKey);
    try {
      const state = await write();
      if (!current()) return;
      apply(state);
      later(deferredKey, false);
      toast(t("assistant-decisionrow.dismissed"), "ok", {
        label: t("assistant-task-actions.undo_dismiss"),
        run: async () => {
          if (!current() || !state.dismissal_id) return;
          try {
            const restored = await write(state.dismissal_id);
            apply(restored);
          } catch (error) {
            if (current()) toast(t("assistant-decisionrow.undo_failed", { error: errorText(error) }), "error");
          }
        },
      });
    } catch (error) {
      if (current()) {
        later(deferredKey, false);
        toast(t("assistant-task-actions.dismiss_failed", { error: errorText(error) }), "error");
      }
    } finally {
      admission.current = false;
      setBusy(false);
      setDismissing(false);
    }
  };
  const defer = () => {
    if (admission.current || !current()) return;
    later(decisionLaterKey(todo));
  };
  const buttons = <>
    <Button className="min-h-11" disabled={busy} onClick={defer}>{t("assistant-task-actions.later")}</Button>
    <Button className="min-h-11" disabled={busy || !round} busy={dismissing} onClick={dismiss}>{t("assistant-task-actions.dismiss_draft")}</Button>
  </>;
  const controls = <div data-decision-actions className={`${ask && askOptions(ask).length ? "" : "sticky -bottom-2.5 "}-mx-3 -mb-2.5 mt-1 flex items-center gap-2 border-t border-line bg-field px-3 py-2.5`}>{buttons}</div>;
  // a decision that offers a go-ahead the owner gave three times carries a suggested rule: its line goes under the
  // row (#5802; always mounted here), answered through the to-do's manager as the row's own answers are
  // (kept by the decision's round: what Always made is not said under the to-do's next decision)
  const rule = { id: todo.id + (ask?.asked_at ?? ""), suggestion: ask?.suggestion, headers: { [MANAGER_HEADER]: manager } };
  if (ask && askOptions(ask).length) return <><DecisionRow todo={todo} ask={ask} rowKey={rowKey} hideTitle={hideTitle} admission={admission} onBusyChange={setBusy} blocked={busy} footer={controls} /><RuleSuggestionLine of={rule} /></>;
  return <><div data-needs-row={rowKey} className="flex min-w-0 flex-col gap-1.5 rounded-control bg-field px-3 py-2.5">
    {ask ? <><AcceptRow stacked hideTitle={hideTitle} admission={admission} onBusyChange={setBusy} blocked={busy} secondary={<span data-decision-actions className="contents">{buttons}</span>} row={askRow(todo, ask)} />{ask.for_briefing && <BriefingMark />}</> : <>
      <div className="flex min-w-0 items-center gap-2 text-sm"><button type="button" onClick={open} className="min-w-0 flex-1 truncate text-left font-medium hover:underline">{todo.title}</button><span className="shrink-0 text-xs text-ink-2">{t("assistant-tasks.review_needs_user")}</span></div>
      {(review?.reason || review?.note) && <p data-decision-legacy className="whitespace-pre-wrap text-sm select-text [overflow-wrap:anywhere]">{review.reason || review.note}</p>}
      {review?.recommend && <p className="text-xs text-ink-2">{t("assistant-brief.recommend", { option: review.recommend })}</p>}
      {review?.options?.length ? <ul className="text-xs text-ink-2">{review.options.map((option) => <li key={option.label}>{option.label}{option.leads_to ? t("assistant-decisionrow.leads_to", { leads: option.leads_to }) : ""}</li>)}</ul> : null}
      <p className="text-xs text-ink-2">{t("assistant-decisionrow.missing_options")}</p>
      <div><Button disabled={busy} onClick={open}>{t("assistant-decisionrow.another")}</Button></div>
    </>}
    {!ask && controls}
  </div><RuleSuggestionLine of={rule} /></>;
}

export type NeedsYouPartProps =
  /** A task's decision in the tray. */
  | { decision: Todo; rowKey?: string; hideTitle?: boolean }
  /** The suggested rule of a plan card or a passed-up question, under it. */
  | { rule: RuleCard };

/** What views/assistant/Tasks loads, with the first card that draws one and not with the first screen
 *  (startup-preload's budget): a decision's row, or a suggested rule's line (#5802). One chunk behind one component,
 *  so the first screen names one file for both. */
export default function NeedsYouParts(props: NeedsYouPartProps) {
  if ("decision" in props) return <TaskDecision todo={props.decision} rowKey={props.rowKey} hideTitle={props.hideTitle} />;
  return <RuleSuggestionLine of={props.rule} />;
}
