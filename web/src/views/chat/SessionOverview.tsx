// The session overview's scrollable contents, loaded when the user turns it on.
import "./SessionOverview.css";
import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { formatTokens } from "../../lib/tokens";
import { contextLabel, contextTier, contextTooltip, contextUsage } from "../../lib/context-usage";
import { sessionTitle, shortPath, stateOf } from "../../lib/format";
import { credentialOwner } from "../../lib/api";
import { openPanel } from "../../lib/panel";
import { followChildSession, followSubagentLink } from "../../lib/subagent-link";
import { getSession, useStore } from "../../lib/store";
import { subagentTitle, taskLabel, taskPill, type TaskIndex } from "../../lib/tasks";
import { finishedBackgroundTasks, type FinishedTask } from "../../lib/finished-tasks";
import type { Item, Session, Subagent } from "../../lib/types";
import { cx } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { StatusMark } from "../../ui/StatusMark";
import { BackgroundList } from "./BackgroundPanel";
import { SessionInfoDialog } from "./SessionDialogs";

interface OverviewProps {
  session: Session;
  machineName: string;
  items: Item[];
  tasks: TaskIndex;
  id: string;
  children: readonly Session[];
  onJumpToCall?: (call: string) => void;
  hidden?: boolean;
}

export function SessionOverview({ id, session, machineName, items, tasks, children, onJumpToCall, hidden }: OverviewProps) {
  const [infoOpen, setInfoOpen] = useState(false);
  const finished = useMemo(() => finishedBackgroundTasks(items, tasks), [items, tasks]);
  // Codex child threads are standalone sessions. Use the known rows from this machine and manager.
  const listed = useStore((s) => (s.sessions[session.machine_id] ?? []).filter((row) =>
    row.session_id !== session.session_id && row.manager === session.manager && (
      row.parent_session_id === session.session_id || row.root_session_id === session.session_id ||
      (!row.parent_session_id && session.source_thread_id && row.parent_thread_id === session.source_thread_id)
    ),
  ));
  const agents = useMemo(() => {
    const embedded = session.subagents ?? [];
    const ids = new Set(embedded.map((a) => a.session_id));
    const rows = new Map(listed.map((child) => [child.session_id, child]));
    for (const child of children) {
      if (!child.folded_as && (child.is_subagent || child.parent_session_id || child.parent_thread_id)) rows.set(child.session_id, child);
    }
    const threads = [...rows.values()].filter((child) => !ids.has(child.session_id)).map((child): Subagent => ({
      id: child.session_id,
      session_id: child.session_id,
      parent_id: child.parent_session_id,
      description: sessionTitle(child),
      running: stateOf(child) === "working" || stateOf(child) === "needs_you" || Boolean(child.background_working),
      started_at: child.started_at ?? child.updated_at,
    }));
    return [...embedded, ...threads];
  }, [children, listed, session.subagents]);
  const running = agents.filter((a) => a.running).length;
  const mid = session.machine_id;
  const sid = session.session_id;
  const owner = credentialOwner();
  const current = () => {
    const live = getSession(mid, sid);
    return owner === credentialOwner() && live && live.manager === session.manager;
  };

  return (
    <aside id={id} data-session-overview aria-labelledby={`${id}-title`} hidden={hidden}
      className="session-overview flex min-h-0 shrink-0 flex-col bg-surface">
      <h2 id={`${id}-title`} className="shrink-0 border-b border-line px-3 py-2.5 text-sm font-semibold text-ink">{t("chat-chatview.session_overview")}</h2>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3">
        <Environment session={session} machineName={machineName} onInfo={() => setInfoOpen(true)} />
        <OverviewContext session={session} />
        {agents.length > 0 && <Subagents agents={agents} running={running} open={(agent) => {
          if (!current()) return;
          if (session.subagents?.some((entry) => entry.session_id === agent.session_id)) {
            followSubagentLink(mid, `${sid}@${agent.id}`);
          } else {
            followChildSession(mid, agent.session_id, sid);
          }
        }} />}
        <div className="border-t border-line">
          <BackgroundList session={session} mid={mid} sid={sid} inOverview
            onOpenRun={(task) => { if (current()) openPanel({ kind: "background", mid, sid, run: task, from: "overview" }); }}
            onOpenTask={(task) => { if (current()) openPanel({ kind: "background", mid, sid, task: task.id, from: "overview" }); }}
          />
          <FinishedTasks finished={finished} session={session}
            onOpenOutput={(path) => { if (current()) openPanel({ kind: "file", mid, path, cwd: session.cwd }); }}
            onJumpToCall={onJumpToCall}
          />
        </div>
      </div>
      <SessionInfoDialog session={session} machineName={machineName} open={infoOpen} onClose={() => setInfoOpen(false)} />
    </aside>
  );
}

function Environment({ session, machineName, onInfo }: Pick<OverviewProps, "session" | "machineName"> & { onInfo: () => void }) {
  const worktree = Boolean(session.worktree && session.repo_root && session.worktree !== session.repo_root);
  const worktreePath = worktree && (session.worktree !== session.cwd || !session.branch);
  const rows: { label: string; icon: IconName; value: string | undefined; badge?: string }[] = [
    { label: t("chat-sessioninfo.machine"), icon: "monitor", value: machineName },
    { label: t("chat-sessiondialogs.folder"), icon: "folder", value: shortPath(session.cwd) },
    { label: t("chat-sessioninfo.branch"), icon: "git", value: session.branch, badge: worktree && !worktreePath ? t("chat-sessionoverview.worktree") : undefined },
  ];
  if (worktreePath) {
    rows.push({ label: t("chat-sessionoverview.worktree"), icon: "folder", value: shortPath(session.worktree) });
  }
  return (
    <section className="py-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-xs font-semibold text-ink-2">{t("chat-sessionoverview.environment")}</h3>
        <button type="button" onClick={onInfo} className="text-xs text-accent-ink hover:underline">{t("chat-sessioninfo.info_title")}</button>
      </div>
      <dl className="mt-2 flex flex-col gap-1.5">
        {rows.filter((row) => row.value).map(({ label, icon, value, badge }) => (
          <div key={label} className="flex items-start gap-2 text-xs">
            <dt className="flex shrink-0 items-center gap-1.5 text-ink-3">
              <Icon name={icon} size={14} />
              {label}
            </dt>
            <dd className="min-w-0 flex-1 text-ink [overflow-wrap:anywhere]">{value}</dd>
            {badge && <dd className="shrink-0 text-ink-3">{badge}</dd>}
          </div>
        ))}
      </dl>
    </section>
  );
}

function OverviewContext({ session }: { session: Session }) {
  const usage = contextUsage(session);
  if (!usage) return null;
  const label = contextLabel(usage);
  const percent = usage.used;
  const nearFull = contextTier(usage) !== "ok";
  return (
    <section className="flex flex-col gap-2 border-t border-line py-3">
      <h3 className="text-xs font-semibold text-ink-2">{t("chat-sessioninfo.context")}</h3>
      <div className="flex flex-wrap justify-between gap-x-2 gap-y-1 text-xs tabular-nums">
        <span className={nearFull ? "text-orange" : "text-ink"}>{label}</span>
        <span className="text-ink-3">{t("context.tokens", { used: formatTokens(usage.tokens), window: formatTokens(usage.window) })}</span>
      </div>
      <div role="progressbar" aria-label={contextTooltip(usage)} aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100} className="h-1.5 overflow-hidden rounded-full bg-hover-2">
        <div className={cx("h-full rounded-full", nearFull ? "bg-orange" : "bg-accent")} style={{ width: `${percent}%` }} />
      </div>
    </section>
  );
}

function Subagents({ agents, running, open }: { agents: Subagent[]; running: number; open: (agent: Subagent) => void }) {
  const [expanded, setExpanded] = useState(false);
  const ended = agents.length - running;
  return (
    <section className="border-t border-line py-2">
      <h3>
        <button type="button" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}
          className="flex min-h-9 w-full items-center gap-2 rounded-control px-1 text-left text-xs hover:bg-hover">
          <Icon name="agent" size={15} className="shrink-0 text-ink-3" />
          <span className="flex-1 font-semibold text-ink-2">{t("chat-sessionoverview.subagents")}</span>
          {running > 0 && <span className="whitespace-nowrap text-accent-ink tabular-nums">{t("chat-sessionoverview.running_count", { count: running })}</span>}
          {ended > 0 && <span className="whitespace-nowrap text-ink-3 tabular-nums">{t("chat-sessionoverview.ended_count", { count: ended })}</span>}
          <Icon name="chevron" size={13} className={cx("shrink-0 text-ink-3", expanded && "rotate-180")} />
        </button>
      </h3>
      {expanded && (
        <ul className="mt-1 flex flex-col gap-0.5">
          {subagentTree(agents).map(({ agent, depth }) => {
            const doing = agent.running ? agent.last_tool?.summary || agent.last_tool?.name : undefined;
            return (
              <li key={agent.id} style={{ paddingInlineStart: `calc(var(--spacing) * ${Math.min(depth, 4) * 3})` }}>
                <button type="button" onClick={() => open(agent)}
                  className="flex min-h-9 w-full items-center gap-2 rounded-control px-2 py-1.5 text-left text-xs hover:bg-hover">
                  {agent.running ? <StatusMark state="working" size="sm" /> : <Icon name="check" size={12} className="shrink-0 text-ink-3" />}
                  <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="truncate text-ink" title={subagentTitle(agent)}>{subagentTitle(agent)}</span>
                    {doing && <span className="truncate text-ink-3">{doing}</span>}
                  </span>
                  <span className="shrink-0 whitespace-nowrap text-ink-3">{agent.running ? t("chat-subagentcard.status_running") : t("chat-sessionoverview.ended")}</span>
                  <Icon name="chevronRight" size={12} className="shrink-0 text-ink-3" />
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function FinishedTasks({ finished, session, onOpenOutput, onJumpToCall }: Pick<OverviewProps, "session" | "onJumpToCall"> & {
  finished: readonly FinishedTask[];
  onOpenOutput: (path: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const list = useRef<HTMLUListElement>(null);
  const active = new Set(session.background?.map((task) => task.id));
  const ended = finished.filter((task) => !active.has(task.id));
  useLayoutEffect(() => {
    if (open) list.current?.firstElementChild?.scrollIntoView({ block: "nearest" });
  }, [open]);
  if (!ended.length) return null;
  return (
    <section className="pb-3">
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}
        className="flex min-h-9 w-full items-center gap-2 rounded-control px-2 text-left text-xs text-ink-2 hover:bg-hover">
        <Icon name="check" size={14} className="text-green" />
        <span className="flex-1">{t("chat-sessionoverview.finished_count", { count: ended.length })}</span>
        <Icon name="chevron" size={13} className={cx("text-ink-3", open && "rotate-180")} />
      </button>
      {open && (
        <ul ref={list} className="divide-y divide-line rounded-control border border-line">
          {ended.map(({ id, task, title, call, at, startedAt }) => {
            const pill = taskPill(task, startedAt, at);
            return (
              <li key={id} className="flex min-w-0 flex-col gap-1 px-2.5 py-2 text-xs">
                <div className="flex items-start justify-between gap-2">
                  <span className="min-w-0 break-words text-ink">{title || taskLabel(task)}</span>
                  <span className={cx("shrink-0 whitespace-nowrap", pill?.tone === "red" ? "text-red" : "text-ink-3")}>{pill?.text}</span>
                </div>
                {(task.output_file || (onJumpToCall && call)) && (
                  <div className="flex flex-wrap gap-2 text-accent-ink">
                    {task.output_file && <button type="button" onClick={() => onOpenOutput(task.output_file!)} className="hover:underline">{t("chat-activity.open_output_file")}</button>}
                    {onJumpToCall && call && <button type="button" onClick={() => onJumpToCall(call)} className="hover:underline">{t("chat-sessionoverview.show_in_chat")}</button>}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

interface SubagentRow {
  agent: Subagent;
  /** 0: the session launched it (or its launcher is not listed); 1: a subagent did; … */
  depth: number;
}

/** Every subagent by who launched whom (`parent_id`), flattened for a list: each right under its launcher, one level
 *  deeper; one whose launcher is not listed (it ended before the node's one-day cutoff) at the top level. Within a
 *  level running ones first, then the newest first (as the node lists them). */
function subagentTree(all: Subagent[] | undefined): SubagentRow[] {
  const list = all ?? [];
  const listed = new Set(list.map((a) => a.id));
  const newest = (a: Subagent) => Date.parse(a.started_at ?? "") || 0;
  const order = (a: Subagent, b: Subagent) => Number(Boolean(b.running)) - Number(Boolean(a.running)) || newest(b) - newest(a);
  const kids = new Map<string, Subagent[]>();
  for (const a of [...list].sort(order)) {
    const p = a.parent_id && a.parent_id !== a.id && listed.has(a.parent_id) ? a.parent_id : "";
    kids.set(p, [...(kids.get(p) ?? []), a]);
  }
  const out: SubagentRow[] = [];
  const placed = new Set<string>();
  const place = (a: Subagent, depth: number) => {
    if (placed.has(a.id)) return;
    placed.add(a.id);
    out.push({ agent: a, depth });
    for (const k of kids.get(a.id) ?? []) place(k, depth + 1);
  };
  for (const a of kids.get("") ?? []) place(a, 0);
  // launchers naming each other (only a runtime could write that) are reached from nowhere: list them at the top
  for (const a of [...list].sort(order)) place(a, 0);
  return out;
}
