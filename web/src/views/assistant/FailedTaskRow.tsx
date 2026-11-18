// A task whose Agent did not start, in Needs you (#5096, #8237, #10365): why in plain words (by its code first), the
// question its tab's shell stopped on, "Use … instead" on its computer's other launcher, Retry and Remove. It comes with
// the tray's first such row (TaskDecision's NeedsYouParts), not with the first screen (startup-preload's budget); the
// task panel's failed start takes its next steps from here too.
import { lazy, Suspense } from "react";
import { t } from "../../i18n";
import { useFeed, type PlanTask } from "../../lib/feed";
import { openTask } from "../../lib/handoff";
import { startFailureRowLine } from "../../lib/start-failure";
import { planTaskOf, retryLauncher, retryOnLauncher, startFailureCodeLine } from "../../lib/start-retry";
import { useStore } from "../../lib/store";
import type { Todo } from "../../lib/todos";
import { toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { whyNotStarted } from "./Handoff";
import { removeTaskNow, retryTaskNow, ROW_ACTIONS, useRetrying, waitsLabel } from "./Tasks";

// the question a failed start's tab stopped on, answered on its row (#10365): only for such a failure
const ShellQuestion = lazy(() => import("../ShellQuestionCard"));

/** A task whose Agent did not start: its plan task (why, by its code) and the launcher "Use … instead" starts it with,
 *  when the hub suggests one and takes it (lib/start-retry retryLauncher, #10365). */
export function useFailedStart(todo: Todo | undefined): { task?: PlanTask; instead?: string } {
  const task = useFeed((s) => (todo ? planTaskOf(todo, s.plans) : undefined));
  const features = useStore((s) => s.features);
  return { task, instead: retryLauncher(task, features) };
}

/** "Use … instead" (lib/start-retry retryOnLauncher), saying why when it is refused, as Retry does (retryTaskNow). */
export function retryOnLauncherNow(todo: Todo, launcher: string) {
  retryOnLauncher(todo, launcher).catch((e) => toast(t("assistant-tasks.retry_failed", { error: String(e instanceof Error ? e.message : e) }), "error"));
}

/** A task whose Agent did not start, in Needs you: why, and Retry / Remove where it is (#5096: no panel to open for them;
 *  the title opens the to-do's page). */
export function FailedTaskRow({ rowKey, todo, countsOpenAttr }: { rowKey: string; todo: Todo; countsOpenAttr: { "data-counts-open"?: string } }) {
  const retrying = useRetrying(todo.id);
  const why = useFeed((s) => whyNotStarted(todo, s.plans));
  const { task, instead } = useFailedStart(todo);
  return (
    <div data-needs-row={rowKey} {...countsOpenAttr} data-failed-task={todo.id} className="flex flex-col gap-1.5 rounded-control bg-danger-wash px-3 py-2 text-sm">
      <div className="flex min-w-0 items-center gap-2">
        <Icon name="info" className="shrink-0 text-red" />
        <button type="button" onClick={() => openTask(todo.id)} className="min-w-0 flex-1 truncate text-left hover:underline">{todo.title}</button>
        <span className="shrink-0 text-xs text-ink">{waitsLabel(todo, "failed")}</span>
      </div>
      {/* in plain words, by its code first (#10365); the hub's raw error is the task's details' (#8237) */}
      {why && <div className="truncate pl-6 text-xs text-ink">{startFailureCodeLine(task) ?? startFailureRowLine(why)}</div>}
      {task?.error_code === "shell_waiting" && <Suspense fallback={null}><ShellQuestion todo={todo} task={task} /></Suspense>}
      <div className={ROW_ACTIONS}>
        {/* its launcher can't start there: the one-tap way out first, on the computer's other launcher (#10365) */}
        {instead && (
          <button type="button" data-retry-launcher={instead} disabled={retrying} onClick={() => retryOnLauncherNow(todo, instead)} className="touch-hit inline-flex min-w-0 items-center gap-1 whitespace-nowrap font-semibold text-ink hover:underline disabled:opacity-50 disabled:hover:no-underline">
            <Icon name="retry" size={12} />
            <span className="truncate">{t("assistant-task-actions.use_launcher_instead", { launcher: instead })}</span>
          </button>
        )}
        <button type="button" disabled={retrying} onClick={() => retryTaskNow(todo)} className="touch-hit inline-flex items-center gap-1 whitespace-nowrap font-semibold text-ink hover:underline disabled:opacity-50 disabled:hover:no-underline">
          <Icon name="retry" size={12} />
          {retrying ? t("assistant-tasks.retrying") : t("assistant-tasks.retry")}
        </button>
        <button type="button" onClick={() => removeTaskNow(todo)} className="touch-hit inline-flex items-center gap-1 whitespace-nowrap font-semibold text-ink hover:underline">
          <Icon name="trash" size={12} />
          {t("assistant-tasks.remove")}
        </button>
      </div>
    </div>
  );
}
