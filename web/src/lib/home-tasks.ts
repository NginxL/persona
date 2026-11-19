// The overview's "Tasks in progress" (#6450, the web side of the Lark bot's /tasks, #5432): every open task, manual ones
// too, in the Tasks page's order (sortTodos: what the user must act on first, then newest). Pure, so the section and its
// count agree.
import { stateOf } from "./format";
import { sortTodos, todoStatus, type SessionLookup, type Todo, type TodoStatus } from "./todos";
import type { Session } from "./types";

/** How many tasks the overview lists; the rest are a "See all" away. */
export const HOME_TASKS_MAX = 6;

export interface HomeTask {
  todo: Todo;
  status: TodoStatus;
  /** Agents linked and not released, as the Tasks page counts them. */
  agents: number;
  /** The agent its card names (#10904): of those whose rows the page has, the one working, else the one that moved last.
   *  Absent while none of their rows is read. */
  lead?: Session;
}

const ms = (at: string | undefined) => {
  const v = at ? Date.parse(at) : 0;
  return Number.isNaN(v) ? 0 : v;
};
/** When an agent last moved, as the Tasks page's triage reads it (lastActivity). */
const movedAt = (s: Session) => Math.max(ms(s.updated_at), ms(s.state_since));

/** The working agent (the one that moved last when several are), else the one that moved last; undefined with no row. */
export function leadAgent(rows: Session[]): Session | undefined {
  let lead: Session | undefined;
  for (const s of rows) {
    if (!lead) { lead = s; continue; }
    const working = stateOf(s) === "working", leadWorking = stateOf(lead) === "working";
    if (working !== leadWorking ? working : movedAt(s) > movedAt(lead)) lead = s;
  }
  return lead;
}

/** lookup is the page's sessions with the cards answered here applied (answeredLookup), as the Tasks page reads them. */
export function homeTasks(todos: Todo[], lookup: SessionLookup, max = HOME_TASKS_MAX): { rows: HomeTask[]; total: number } {
  const open = sortTodos(todos.filter((todo) => !todo.done_at), lookup);
  const rows = open.slice(0, max).map((todo): HomeTask => {
    const linked = (todo.agents ?? []).filter((a) => !a.released_at);
    const read = linked.map((a) => lookup(a.machine_id, a.session_id)).filter((s): s is Session => Boolean(s && !s.deleted));
    return { todo, status: todoStatus(todo, lookup), agents: linked.length, lead: leadAgent(read) };
  });
  return { rows, total: open.length };
}
