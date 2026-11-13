import { t } from "../../i18n";
import { useKnownDefault } from "../../lib/manager-choice";
import { href, onLinkClick, type Route } from "../../lib/router";
import { useStore } from "../../lib/store";
import type { InboxItem } from "../../lib/inbox";
import { Icon } from "../../ui/icons";

/** The business task and its current workers, separate from the Inbox's draft-processing chat. */
export function InboxTaskLinks({ item }: { item: InboxItem }) {
  const defaultManager = useKnownDefault();
  const id = item.agent?.todo_id;
  const todo = useStore((s) => id ? s.todos.find((x) => x.id === id && (x.manager ?? defaultManager) === (item.manager ?? defaultManager)) : undefined);
  if (!id) return null;
  const current = todo?.lineage?.filter((x) => x.current);
  // A current lineage supersedes registered workers from previous rounds, even while the next Agent is starting.
  const agents = current?.length ? current.flatMap((x) => x.machine && x.session_id ? [{
    machine: x.machine, session: x.session_id,
    title: todo?.agents?.find((a) => a.session_id === x.session_id && a.machine_id === x.machine)?.title || x.agent || x.session_id,
  }] : []) : (todo?.agents ?? []).filter((x) => !x.released_at && !x.start_error).map((x) => ({ machine: x.machine_id, session: x.session_id, title: x.title || x.session_id }));
  const taskRoute: Route = { name: "todos", id };
  const link = "flex min-w-0 max-w-full items-center gap-1.5 rounded-control text-xs text-accent hover:underline focus-visible:outline-2 focus-visible:outline-accent";
  return (
    <div className="mt-3 flex flex-col items-start gap-1.5" data-inbox-task-links>
      <a href={href(taskRoute)} onClick={(e) => onLinkClick(e, taskRoute)} className={link}>
        <Icon name="todo" size={13} /><span className="shrink-0">{t("inbox-inboxdetail.linked_task")}</span>
        <span className="truncate">{todo?.title || id}</span>
      </a>
      {agents.map((agent) => {
        const route: Route = { name: "session", mid: agent.machine, sid: agent.session };
        return <a key={`${agent.machine}/${agent.session}`} href={href(route)} onClick={(e) => onLinkClick(e, route)} className={link}>
          <Icon name="agent" size={13} /><span className="shrink-0">{t("inbox-inboxdetail.linked_agent")}</span>
          <span className="truncate">{agent.title}</span>
        </a>;
      })}
    </div>
  );
}
