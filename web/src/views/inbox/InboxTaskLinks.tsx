import { t } from "../../i18n";
import { useKnownDefault } from "../../lib/manager-choice";
import { href, onLinkClick, type Route } from "../../lib/router";
import { useStore } from "../../lib/store";
import type { InboxItem, InboxTodoForward } from "../../lib/inbox";
import type { Todo } from "../../lib/todos";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export function todoForwardLabel(status: InboxTodoForward["status"]): string {
  switch (status) {
    case "queued":
      return t("inbox-inboxdetail.forward_queued");
    case "resumed":
      return t("inbox-inboxdetail.forward_resumed");
    case "fallback":
      return t("inbox-inboxdetail.forward_fallback");
    case "failed":
      return t("inbox-inboxdetail.forward_failed");
    default:
      return t("inbox-inboxdetail.forward_forwarded");
  }
}

/** The business task and its current workers, separate from the Inbox's draft-processing chat. */
export function InboxTaskLinks({ item }: { item: InboxItem }) {
  const defaultManager = useKnownDefault();
  const id = item.agent?.todo_id;
  const fwd = item.todo_forward;
  const todos = useStore((s) => s.todos);
  const linked = todos.filter((x) => (x.manager ?? defaultManager) === (item.manager ?? defaultManager) && (x.inbox_item === item.id || x.id === id || (fwd && x.id === fwd.todo_id)));
  const tasks: { id: string; todo?: Todo }[] = linked.map((todo) => ({ id: todo.id, todo }));
  if (id && !tasks.some((x) => x.id === id)) tasks.push({ id });
  if (fwd?.todo_id && !tasks.some((x) => x.id === fwd.todo_id)) tasks.push({ id: fwd.todo_id });
  if (!tasks.length && !fwd) return null;
  const link = "flex min-w-0 max-w-full items-center gap-1.5 rounded-control text-xs text-accent hover:underline focus-visible:outline-2 focus-visible:outline-accent";
  const warn = fwd?.status === "fallback" || fwd?.status === "failed";
  return (
    <div className="mt-3 flex flex-col items-start gap-1.5" data-inbox-task-links>
      {fwd && (
        <div className="flex min-w-0 max-w-full flex-wrap items-center gap-1.5 text-xs" data-inbox-todo-forward={fwd.status}>
          <span className={cx("shrink-0 rounded-chip px-1.5 py-0.5 text-submeta font-medium", warn ? "bg-field text-red" : "bg-field text-ink-2")}>
            {todoForwardLabel(fwd.status)}
          </span>
          {fwd.reason && <span className="min-w-0 truncate text-ink-3" title={fwd.reason}>{fwd.reason}</span>}
        </div>
      )}
      {tasks.map(({ id: taskID, todo }) => {
        const current = todo?.lineage?.filter((x) => x.current);
        // A current lineage supersedes previous workers, even while the next Agent is starting.
        const agents = current?.length ? current.flatMap((x) => x.machine && x.session_id ? [{
          machine: x.machine, session: x.session_id,
          title: todo?.agents?.find((a) => a.session_id === x.session_id && a.machine_id === x.machine)?.title || x.agent || x.session_id,
        }] : []) : (todo?.agents ?? []).filter((x) => !x.released_at && !x.start_error).map((x) => ({ machine: x.machine_id, session: x.session_id, title: x.title || x.session_id }));
        const taskRoute: Route = { name: "todos", id: taskID };
        const title = todo?.title || (fwd?.todo_id === taskID ? fwd.todo_title : undefined) || taskID;
        return <div key={taskID} className="contents">
          <a href={href(taskRoute)} onClick={(e) => onLinkClick(e, taskRoute)} className={link}>
            <Icon name="todo" size={13} /><span className="shrink-0">{t("inbox-inboxdetail.linked_task")}</span>
            <span className="truncate">{title}</span>
          </a>
          {agents.map((agent) => {
            const route: Route = { name: "session", mid: agent.machine, sid: agent.session };
            return <a key={`${agent.machine}/${agent.session}`} href={href(route)} onClick={(e) => onLinkClick(e, route)} className={link}>
              <Icon name="agent" size={13} /><span className="shrink-0">{t("inbox-inboxdetail.linked_agent")}</span>
              <span className="truncate">{agent.title}</span>
            </a>;
          })}
        </div>;
      })}
    </div>
  );
}
