// A planned task whose Agent did not start (#10365): the plan task it runs as, which says why by its code (`error_code`,
// `error_args`; a short line read before the hub's words of lib/start-failure.ts) and names the other launchers of its
// computer a Retry may start it on (`retry_with`), and that Retry, "Use … instead". Only a failed start's own views read
// these (its Needs you row, the task panel, the to-do's page), all loaded after the first screen, so they are apart
// from lib/feed.ts and lib/start-failure.ts (startup-preload's budget).
import { t } from "../i18n";
import { getFeedState, retryKey, retryTask, type PlanTask, type TaskPlan } from "./feed";
import { getState, hubHas } from "./store";
import type { Todo } from "./todos";

/** The hub starts a failed task again on another launcher of its computer when a Retry names one (hello `features`,
 *  #10365). An older hub ignores the body and starts the launcher that just failed again, so "Use … instead" is offered
 *  only where it is listed. */
export const TASK_RETRY_LAUNCHER_FEATURE = "task_retry_launcher";

/** The plan task a to-do runs as: its plan's (`plan_id`), else the plan that took it over through todo_id (a to-do
 *  such a plan runs keeps no `plan_id`, #8770); undefined while no plan the page has holds it. A failed start's carries
 *  why (`error_code`, `error_args`) and the launchers it may start with instead (`retry_with`). */
export function planTaskOf(todo: Todo, plans: Record<string, TaskPlan>): PlanTask | undefined {
  const own = todo.plan_id ? plans[todo.plan_id]?.tasks.find((t) => t.todo_id === todo.id) : undefined;
  if (own) return own;
  for (const p of Object.values(plans)) {
    if (p.state === "dismissed") continue;
    const task = p.tasks.find((t) => t.todo_id === todo.id);
    if (task) return task;
  }
  return undefined;
}

/** A failed start's coded refusal, as its plan task carries it (`error_code`, `error_args`). */
export interface StartFailureCode {
  error_code?: string;
  error_args?: Record<string, string>;
}

/** The coded refusal in a short line, read before the text (lib/start-failure.ts startFailureRowLine): a launcher the
 *  new terminal tab could not run (its long sentence, the shells and the file, stays for the details), or a checkout
 *  folder an earlier start left where the new one goes; undefined for any other code. */
export function startFailureCodeLine(coded: StartFailureCode | undefined): string | undefined {
  const args = coded?.error_args ?? {};
  switch (coded?.error_code) {
    case "launcher_missing":
      return args.where === "terminal" && args.launcher ? t("lib-start-retry.start_code_launcher_terminal", { launcher: args.launcher }) : undefined;
    case "worktree_path_taken":
      return t("lib-start-retry.start_code_worktree_path_taken");
  }
  return undefined;
}

/** Another launcher a task whose Agent did not start may start with (`task`'s `retry_with`, the first suggested), when the
 *  hub takes it; undefined otherwise. */
export function retryLauncher(task: PlanTask | undefined, features: string[]): string | undefined {
  const launcher = task?.retry_with?.[0];
  return launcher && hubHas(features, TASK_RETRY_LAUNCHER_FEATURE) ? launcher : undefined;
}

/** "Use … instead": the Retry (lib/feed retryTask) on `launcher` (and `model`) of the same computer, sent only to a hub
 *  that takes it; to any other the call is refused here rather than starting the launcher that failed again. A tap
 *  while a Retry is on its way does nothing, as there. */
export async function retryOnLauncher(todo: Todo, launcher: string, model?: string): Promise<void> {
  if (getFeedState().busy[retryKey(todo.id)]) return;
  if (!hubHas(getState().features, TASK_RETRY_LAUNCHER_FEATURE)) throw new Error(t("lib-start-retry.retry_launcher_unsupported"));
  await retryTask(todo, { launcher, ...(model ? { model } : {}) });
}
