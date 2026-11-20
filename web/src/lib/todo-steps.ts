// A to-do's steps on the To-dos row and page (#11858): the to-dos whose `part_of` names it, as the hub counts them on
// the row's view (`view.steps`, TodoStepCount; server/internal/assistant/todo/projection/view.go withSteps: the closed
// ones of all, a failed start open unless its work finished in a related to-do). iOS draws the same count ("Steps 2/5").
// The web keeps no count of its own: a row without the view (an older manager) says what it said before.

import { t } from "../i18n";
import type { TodoStepCount } from "./todo-view-wire";
import { reviewState } from "./supervision";
import { acceptedAtRest, openAsk, type Todo, type TodoStatus } from "./todos";

/** What the status line says about an open to-do's steps, each part in place of or beside today's words. */
export interface StepsWords {
  /** In place of the status word, for a to-do no Agent of its own is on: "Steps 3/9", or "9/9 steps done". */
  label?: string;
  /** In place of "Not handed to an agent yet" beside `label`: "Next step not arranged yet" once every step is closed,
   *  "" (nothing) while some are open, since its steps were handed out. */
  tail?: string;
  /** After the Agent count, for a to-do with an Agent of its own: "Steps 3/9". */
  after?: string;
}

/** The hub's count of an open to-do's steps; undefined when it has none, is closed, or its row has no view. */
export function todoSteps(todo: Todo): TodoStepCount | undefined {
  const steps = todo.view?.steps;
  if (todo.done_at || !steps || !(steps.total > 0)) return undefined;
  return steps;
}

/**
 * What an open to-do's status line says about its steps, from `todoSteps` and its status (todoStatus):
 *  - no Agent of its own (`not_started`): "Steps 3/9" in place of "Not started", nothing in place of "Not handed to an
 *    agent yet"; once every step is closed, "9/9 steps done" and "Next step not arranged yet";
 *  - otherwise its status stays, "Steps 3/9" after its Agent count; so does an accepted one at rest (acceptedAtRest)
 *    and one whose own result is finished or awaits review (ownWorkFinished): it keeps its status word, nothing claims
 *    its next step is unplanned, and nothing says it was not handed over.
 * Undefined when it has no steps the hub counted: the line is as before.
 */
export function stepsWords(todo: Todo, status: TodoStatus): StepsWords | undefined {
  const steps = todoSteps(todo);
  if (!steps) return undefined;
  const count = t("todos.steps_count", { done: steps.done, total: steps.total });
  // accepted at rest: Accepted stays, its steps after it, and nothing about handing it over (its steps were handed out)
  if (acceptedAtRest(todo, status)) return { after: count, tail: "" };
  if (status !== "not_started") return { after: count };
  // its own result is finished or awaits review: its status word stays (Awaiting review), its steps after it
  if (ownWorkFinished(todo)) return { after: count, tail: "" };
  if (steps.done >= steps.total) return { label: t("todos.steps_all_done", { done: steps.done, total: steps.total }), tail: t("todos.steps_next_unplanned") };
  return { label: count, tail: "" };
}

/** Its own work finished as the hub reads it (server/internal/assistant/todo/projection/view.go finish): deployed, a
 *  review not done yet, or its ask saying done or please verify; accepted is acceptedAtRest's. An open concern is never
 *  deployed. Such a to-do began and waits on its result, so its steps do not take its status word. */
function ownWorkFinished(todo: Todo): boolean {
  if (todo.kind !== "concern" && todo.delivery?.state === "live") return true;
  if (reviewState(todo) === "pending_review") return true;
  const ask = openAsk(todo);
  return !!ask && (!ask.outcome || ask.outcome === "done");
}
