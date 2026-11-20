import { t } from "../../i18n";
import { field, useFeed } from "../../lib/feed";
import { openTask } from "../../lib/handoff";
import { plainMarkdown } from "../../lib/plain-markdown";
import { heldDelivery, interruption, type Todo, type TodoStatus } from "../../lib/todos";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { progressOf, retryTaskNow, ROW_ACTIONS, useRetrying, useTaskNow, waitsLabel } from "./Tasks";

/** A task in Needs you that only waits (#8237): cut off by the model service, with why and Continue (its receipt card's
 *  until then); waiting for review, with what it handed in; else what it waits for. The title opens the task. */
export default function WaitingTaskRow({ rowKey, todo, status, countsOpenAttr }: { rowKey?: string; todo: Todo; status: TodoStatus; countsOpenAttr: { "data-counts-open"?: string } }) {
  const task = useTaskNow(todo);
  const retrying = useRetrying(todo.id);
  const summary = useFeed((s) => {
    const done = todo.origin ? s.feeds[todo.origin]?.findLast((it) => it.kind === "receipt" && it.ref === todo.id && field(it, "state") !== "failed") : undefined;
    return done ? field(done, "summary") : undefined;
  });
  const cutOff = status === "interrupted";
  const cut = cutOff ? interruption(task?.lead) : undefined;
  const canContinue = cutOff && (cut ? cut.canContinue : true);
  const hold = heldDelivery(todo);
  const approval = !cutOff && hold?.unblock === "user";
  const line = cutOff ? cut?.reason
    : approval ? hold.gate
      : todo.supervision?.state === "pending_review" ? (summary && plainMarkdown(summary)) || progressOf(task?.lead)?.text : undefined;
  const label = cut?.invalid ? t("lib-api-errors.invalid_label")
    : approval ? t("todos.kicker_permission")
      : waitsLabel(todo, status, task?.status === status ? task.wait : undefined);
  return (
    <div data-needs-row={rowKey} {...countsOpenAttr} data-waiting-task={todo.id} className={cx("flex flex-col gap-1.5 rounded-control px-3 py-2 text-sm", cutOff ? "bg-danger-wash" : "bg-field")}>
      <div className="flex min-w-0 items-center gap-2">
        <Icon name={cutOff ? "info" : "hand"} className={cx("shrink-0", cutOff ? "text-red" : "text-attention")} />
        <button type="button" onClick={() => openTask(todo.id)} className="min-w-0 flex-1 truncate text-left hover:underline">{todo.title}</button>
        <span className={cx("shrink-0 text-xs", cutOff ? "text-ink" : "text-ink-2")}>{label}</span>
      </div>
      {line && <div className={cx("pl-6 text-xs", cut && !cut.canContinue ? "whitespace-normal" : "truncate", cutOff ? "text-ink" : "text-ink-2")}>{line}</div>}
      {canContinue && (
        <div className={ROW_ACTIONS}>
          <button type="button" data-continue-task={todo.id} disabled={retrying} onClick={() => retryTaskNow(todo, "continue")} className="touch-hit inline-flex items-center gap-1 whitespace-nowrap font-semibold text-ink hover:underline disabled:opacity-50 disabled:hover:no-underline">
            <Icon name="retry" size={12} />
            {retrying ? t("assistant-tasks.retrying") : t("assistant-tasks.continue")}
          </button>
        </div>
      )}
    </div>
  );
}
