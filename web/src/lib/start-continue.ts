// A start's startup question answered from here (#10365, docs/protocol/protocol.md "Answering a start's question"):
// a New Agent start whose new terminal tab stopped on a question of the shell's startup files (shell_waiting) lists
// the question and its answers in start_error_args (or a refused message's 409 body); the owner picks one, the hub's
// start.continue types it into that tab and the session starts again there, and the messages that start refused go
// out again: they wait for the new start on the computer.
// A planned task whose Agent start stopped on such a question lists it in its plan task's error_args; the answer goes
// to POST /api/v1/todos/{id}/start/answer, and the hub starts the task again in that tab (answerTaskStart).
import { api, enc, sessionPath } from "./api";
import { deliverLaunch } from "./new-agent";
import { outboxKey, retryOutboxEntry } from "./outbox";
import { getState, putTodo, refreshTodos, upsertSession } from "./store";
import type { Todo } from "./todos";
import type { Session } from "./types";

export const START_CONTINUE_FEATURE = "start_continue";

/** The hub answers a planned task's startup question and starts the task again in that tab (hello `features`). */
export const TODO_START_ANSWER_FEATURE = "todo_start_answer";

/** The refusal code of a start stopped on its shell's question. */
export const SHELL_WAITING = "shell_waiting";

/** The question a start stopped on, as the computer listed it. */
export interface ShellQuestion {
  /** The question: the tab's last line. */
  line: string;
  /** The tab's last lines, for a look. */
  screen?: string;
  /** update, yes_no, press_key, compinit, ssh_host_key, zsh_newuser, p10k_wizard, rvm_trust, secret; another: a
   *  newer computer's, its answers worded generically. */
  kind: string;
  /** The answers' ids, in order: a key ("y", "n", "q", "0", "c"), "enter", "yes" / "no", "cancel" (Ctrl-C). */
  choices: string[];
  /** It reads a whole line. */
  lineMode: boolean;
  /** A password or passphrase: only "cancel" is offered, nothing is ever typed into it from here. */
  secret: boolean;
  /** The answer Enter alone gives, when the question says. */
  default?: string;
  /** Until when the computer keeps the tab (an ISO time). */
  keptUntil?: string;
}

/** The question in `args` (a session's start_error_args, or the body of a message refused with shell_waiting); none
 *  without a question or answers. */
export function shellQuestion(args: Record<string, unknown> | undefined): ShellQuestion | undefined {
  const text = (k: string) => (typeof args?.[k] === "string" ? (args[k] as string) : "");
  const line = text("line");
  const choices = text("choices").split(",").map((c) => c.trim()).filter(Boolean);
  if (!line || !choices.length) return undefined;
  return {
    line, choices, kind: text("kind"), screen: text("screen") || undefined, lineMode: text("line_mode") === "true",
    secret: text("secret") === "true", default: text("default") || undefined, keptUntil: text("kept_until") || undefined,
  };
}

/** The question while it can still be answered: its tab is kept until a time still ahead. */
export function openQuestion(q: ShellQuestion | undefined, now = Date.now()): ShellQuestion | undefined {
  if (!q) return undefined;
  const until = q.keptUntil ? Date.parse(q.keptUntil) : NaN;
  return Number.isNaN(until) || until > now ? q : undefined;
}

/** Sends the answer: the session is starting again on the computer, under the same id, in the same tab. The row is
 *  shown starting at once (local first); the hub's list follows. */
export async function continueStart(mid: string, sid: string, choice: string): Promise<Session | undefined> {
  const r = await api<{ session?: Session }>(`${sessionPath(mid, sid)}/start/continue`, { method: "POST", json: { choice } });
  const known = (getState().sessions[mid] ?? []).find((s) => s.session_id === sid);
  if (known) upsertSession({ ...known, ...r.session, machine_id: mid, session_id: sid, starting: true, status: "idle",
    start_error: undefined, start_error_code: undefined, start_error_args: undefined });
  return r.session;
}

/** After an answer: the messages this start refused go again (a New Agent launch's first message; an outbox entry's
 *  Retry), and wait on the computer for the agent to be up. Only refusals: they were never delivered. In the order
 *  they were written, which is the order the computer hands them to the agent in: the launch's first message first,
 *  and the chat's only once it went (one refused again keeps them back, failed as they were); those one after
 *  another (the outbox sends a conversation's messages in turn). */
export async function resendRefused(mid: string, sid: string) {
  for (const launch of Object.values(getState().launches)) {
    if (launch.phase === "failed" && launch.machineId === mid && launch.session?.session_id === sid && launch.shellWaiting) {
      await deliverLaunch(launch.id);
      if (getState().launches[launch.id]?.phase !== "ready") return;
    }
  }
  for (const e of getState().outbox[outboxKey(mid, sid)] ?? []) {
    if (e.state === "failed" && e.code === SHELL_WAITING && !e.inDoubt) retryOutboxEntry(mid, sid, e.id);
  }
}

/** Sends the answer to the question task `todo`'s start stopped on: the hub types it into the kept tab and starts the
 *  task again there, the to-do reopened (shown at once, as a Retry's is). */
export async function answerTaskStart(todo: Pick<Todo, "id">, choice: string): Promise<Todo | undefined> {
  const r = await api<{ todo?: Todo }>(`/todos/${enc(todo.id)}/start/answer`, { method: "POST", json: { choice } });
  if (r.todo) putTodo(r.todo);
  void refreshTodos();
  return r.todo;
}
