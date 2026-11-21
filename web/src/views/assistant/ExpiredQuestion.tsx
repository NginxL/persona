import { useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { decisionLaterKey } from "../../lib/assistant-entries";
import { acceptBrief, later } from "../../lib/feed";
import { openTask } from "../../lib/handoff";
import { refreshTodos } from "../../lib/store";
import type { Todo, TodoAsk } from "../../lib/todos";
import type { Interaction } from "../../lib/types";
import { Button, toast } from "../../ui/atoms";
import { QuestionCard } from "../chat/Interactions";

/** Reuse the original question form, but return answers through the to-do's late-decision path. */
export function ExpiredQuestion({ todo, ask, rowKey }: { todo: Todo; ask: TodoAsk; rowKey?: string }) {
  const [busy, setBusy] = useState(false);
  const [recorded, setRecorded] = useState(false);
  const ix: Interaction = {
    id: `${todo.id}:${ask.asked_at}`, manager: todo.manager, machine_id: "", session_id: "",
    kind: "question", tool_name: "ask_user", questions: ask.questions,
    created_at: ask.asked_at,
  };
  const answer = async (answers: Record<string, string>) => {
    setBusy(true);
    try {
      const result = await acceptBrief(todo.id, ask.asked_at, { answers });
      void refreshTodos();
      if (result === "handled") {
        toast(t("assistant-brief.handled"));
        return false;
      }
      setRecorded(true);
      return true;
    } catch (error) {
      toast(t("assistant-decisionrow.failed", { error: errorText(error) }), "error");
      return false;
    } finally {
      setBusy(false);
    }
  };
  return <div data-needs-row={rowKey} data-expired-question className="flex min-w-0 flex-col gap-2">
    {recorded
      ? <p className="text-sm text-ink-2">{t("assistant-decisionrow.answer_recorded")}</p>
      : <QuestionCard ix={ix} canApprove onAnswer={answer} />}
    <div className="flex items-center gap-2">
      <Button disabled={busy} onClick={() => later(decisionLaterKey(todo))}>{t("assistant-task-actions.later")}</Button>
      <Button disabled={busy} onClick={() => openTask(todo.id)}>{t("assistant-decisionrow.another")}</Button>
    </div>
  </div>;
}
