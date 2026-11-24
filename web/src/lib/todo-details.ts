// Presentation helpers used only by the lazily loaded to-do detail and link views. Keeping them out of the shared
// first-screen to-do module avoids sending their XML, source-label and link-resolution code before a view needs it.
import { t } from "../i18n";
import { contextBody } from "./context-body";
import { findTodoForSpace, isCancelled, type SessionLookup, type Todo, type TodoAgent, type TodoSpace } from "./todos";
import { reviewState } from "./supervision";
import type { Session } from "./types";

/** Whether Got it can file a finished turn. An open concern moves to Other open after Seen and bypasses only the
 *  ordinary live-delivery restriction; review and supervision still block filing. Seen is never acceptance (#5732). */
export function seenCanFile(todo: Todo): boolean {
  return (todo.kind === "concern" || todo.delivery?.state !== "live") && reviewState(todo) !== "pending_review" && !todo.supervision?.supervisor;
}

/** Why the to-do was cancelled; older hubs carry it in done_reason. */
export function cancellationReason(todo: Todo): string {
  return isCancelled(todo) ? (todo.cancelled_reason || todo.done_reason || "") : "";
}

/**
 * The format when handing it to an agent (the same as the inbox's)
 * `<persona-context kind="context" title="To-do · <title>" url="">notes</persona-context>`
 */
export function formatTodoContext(title: string, notes?: string): string {
  const escape = (s: string) => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  // a closing tag in the notes would end the block early (either name, #3182)
  const body = contextBody(notes ?? "").trim();
  return `<persona-context kind="context" title="${escape(t("lib-todos.context_title", { title }))}" url="">${body}</persona-context>`;
}

/**
 * Resolve the current todo ID for LinkTodoDialog target.
 * For a space, looks up from the todos list.
 * For a session, uses session.todo?.id directly so the unlink control
 * does not disappear if /todos hasn't loaded or failed (#874 item 4).
 */
export function resolveLinkCurrentTodoId(
  space?: { machineId: string; space: string },
  session?: Session,
  todos: Todo[] = [],
): string | undefined {
  if (space) {
    return findTodoForSpace(todos, space.machineId, space.space)?.id;
  }
  return session?.todo?.id;
}

/**
 * Format agent source label:
 * - Spawned from project: "With project <space>" (resolved from via, or agent's own session lookup as fallback);
 *   "With project" if neither is available;
 * - Spawned from parent agent: "Spawned by <parent title>"; "Spawned" if parent is not in session list;
 * - Same Feishu chat as most of the todo's botmux agents: "Same chat";
 * - Manual: undefined (not labeled).
 */
export function formatAgentSource(
  agent: TodoAgent,
  _spaces?: TodoSpace[],
  lookup?: SessionLookup,
): string | undefined {
  if (!agent.source || agent.source === "manual") return undefined;

  if (agent.source === "space") {
    let spaceName = "";
    if (agent.via) {
      const slash = agent.via.indexOf("/");
      if (slash >= 0) {
        spaceName = agent.via.slice(slash + 1);
      } else {
        spaceName = agent.via;
      }
    }
    if (!spaceName && lookup) {
      const s = lookup(agent.machine_id, agent.session_id);
      if (s?.space_name) spaceName = s.space_name;
    }
    return spaceName ? t("lib-todos.with_project_named", { name: spaceName }) : t("lib-todos.with_project");
  }

  if (agent.source === "spawned") {
    if (agent.via && lookup) {
      const slash = agent.via.indexOf("/");
      if (slash >= 0) {
        const mid = agent.via.slice(0, slash);
        const sid = agent.via.slice(slash + 1);
        const parent = lookup(mid, sid);
        if (parent?.title) {
          return t("lib-todos.spawned_by", { title: parent.title });
        }
      }
    }
    return t("lib-todos.spawned");
  }

  if (agent.source === "chat") return t("lib-todos.same_chat");

  return undefined;
}
