// Used only by the lazy To-dos detail, so its code and words need not load with the first screen.
import { t } from "../i18n";
import type { Todo, TodoDecision, makeSessionLookup } from "./todos";

/** Where a decision came from, as the detail page says it: the agent that asked, by its title when known. */
export function decisionSourceLabel(d: TodoDecision, todo: Todo, lookup: ReturnType<typeof makeSessionLookup>): string {
  if (d.source === "outcome") return t("lib-todos.decision_outcome");
  if (d.source === "agent") return t("lib-todos.decision_by_agent");
  if (d.source === "thread") return t("lib-todos.decision_by_thread");
  if (d.source !== "card") return t("lib-todos.decision_manual");
  const title = lookup(d.source_machine, d.source_session)?.title
    || todo.agents?.find((a) => a.machine_id === d.source_machine && a.session_id === d.source_session)?.title;
  if (!title) return t("lib-todos.decision_agent_question");
  // A session's title is often its first message: keep the label to a glance.
  const short = [...title.trim()].length > 24 ? `${[...title.trim()].slice(0, 24).join("")}…` : title.trim();
  return t("lib-todos.decision_in_question", { title: short });
}
