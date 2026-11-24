// The session overview's scrollable contents, loaded when the user turns it on.
import "./SessionOverview.css";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { formatTokens } from "../../lib/tokens";
import { contextLabel, contextTier, contextTooltip, contextUsage } from "../../lib/context-usage";
import { ago, shortPath } from "../../lib/format";
import { credentialOwner } from "../../lib/api";
import { openPanel } from "../../lib/panel";
import { getSession, useStore } from "../../lib/store";
import { taskLabel, taskPill, type TaskIndex } from "../../lib/tasks";
import { finishedBackgroundTasks, type FinishedTask } from "../../lib/finished-tasks";
import { tsMs, workDuration } from "../../lib/work-groups";
import type { Item, Session } from "../../lib/types";
import { AgentMark } from "../../ui/AgentMark";
import { cx, useNow } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { StatusMark } from "../../ui/StatusMark";
import { BackgroundList } from "./BackgroundPanel";
import { SessionInfoDialog } from "./SessionDialogs";
export { SubagentPanel } from "./SubagentPanel";
import {
  collectOverviewSubagents,
  groupOverviewSubagents,
  type OverviewSubagentItem,
  type OverviewSubagentRow,
} from "./overview-subagents";

interface OverviewProps {
  session: Session;
  machineName: string;
  items: Item[];
  tasks: TaskIndex;
  id: string;
  children: readonly Session[];
  onJumpToCall?: (call: string) => void;
  focusSubagents?: number;
  hidden?: boolean;
}

export function SessionOverview({ id, session, machineName, items, tasks, children, onJumpToCall, focusSubagents, hidden }: OverviewProps) {
  const [infoOpen, setInfoOpen] = useState(false);
  const subagentsSection = useRef<HTMLElement>(null);
  const finished = useMemo(() => finishedBackgroundTasks(items, tasks), [items, tasks]);
  const allSessions = useStore((s) => Object.values(s.sessions).flat());
  const interactions = useStore((s) => s.interactions);
  const machines = useStore((s) => s.machines);

  const subagents = useMemo(
    () => collectOverviewSubagents({ session, children, allSessions, interactions, items, tasks, machines }),
    [session, children, allSessions, interactions, items, tasks, machines],
  );
  const grouped = useMemo(() => groupOverviewSubagents(subagents), [subagents]);

  const handledFocus = useRef(0);
  useEffect(() => {
    if (!focusSubagents || handledFocus.current === focusSubagents || hidden) return;
    const el = subagentsSection.current;
    if (!el) return;
    handledFocus.current = focusSubagents;
    el.scrollIntoView?.({ block: "nearest" });
    el.querySelector<HTMLElement>("button")?.focus({ preventScroll: true });
  }, [focusSubagents, hidden, subagents.length]);

  const mid = session.machine_id;
  const sid = session.session_id;
  const owner = credentialOwner();
  const current = () => {
    const live = getSession(mid, sid);
    return owner === credentialOwner() && (!live || live.manager === session.manager);
  };

  return (
    <aside id={id} data-session-overview aria-labelledby={`${id}-title`} hidden={hidden}
      className="session-overview flex min-h-0 shrink-0 flex-col bg-surface">
      <h2 id={`${id}-title`} className="shrink-0 border-b border-line px-3 py-2.5 text-sm font-semibold text-ink">{t("chat-chatview.session_overview")}</h2>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3">
        <Environment session={session} machineName={machineName} onInfo={() => setInfoOpen(true)} />
        <OverviewContext session={session} />
        {subagents.length > 0 && (
          <SubagentsSection
            sectionRef={subagentsSection}
            running={grouped.running}
            ended={grouped.ended}
            onOpen={(item) => {
              if (!current()) return;
              openPanel({
                kind: "subagent",
                mid: item.mid,
                sid: item.sid,
                parentMid: mid,
                parentSid: sid,
                call: item.call,
                from: "overview",
              });
            }}
          />
        )}
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
        <button type="button" onClick={onInfo} className="whitespace-nowrap text-xs text-accent-ink hover:underline">{t("chat-sessioninfo.info_title")}</button>
      </div>
      <dl className="mt-2 flex flex-col gap-1.5">
        {rows.filter((row) => row.value).map(({ label, icon, value, badge }) => (
          <div key={label} className="flex items-start gap-2 text-xs">
            <dt className="flex shrink-0 items-center gap-1.5 whitespace-nowrap text-ink-3">
              <Icon name={icon} size={14} />
              {label}
            </dt>
            <dd className="min-w-0 flex-1 text-ink [overflow-wrap:anywhere]">{value}</dd>
            {badge && <dd className="shrink-0 whitespace-nowrap text-ink-3">{badge}</dd>}
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

/** Elapsed time while a subagent runs, ticking each second in its own leaf component. */
function LiveElapsed({ since }: { since: number }) {
  const now = useNow(1000);
  return <span className="tabular-nums text-ink-3">{workDuration(Math.max(0, now - since))}</span>;
}

function SubagentAvatar({ agent, label, className }: { agent: string; label?: string; className?: string }) {
  return (
    <span className={cx("inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-field text-ink-2", className)}>
      <AgentMark agent={agent} label={label} size={13} decorative />
    </span>
  );
}

function SubagentListRow({
  row,
  now,
  onOpen,
}: {
  row: OverviewSubagentRow;
  now: number;
  onOpen: (item: OverviewSubagentItem) => void;
}) {
  const { item, depth } = row;
  const startMs = tsMs(item.startedAt) ?? undefined;
  const endedLabel = item.status === "ended" ? ago(item.updatedAt ?? item.startedAt, now, "narrow") : "";
  return (
    <li style={depth > 0 ? { paddingInlineStart: `calc(var(--spacing) * ${Math.min(depth, 2) * 3})` } : undefined} data-subagent-depth={depth}>
      <button
        type="button"
        data-subagent-row={item.sid}
        data-subagent-status={item.status}
        onClick={() => onOpen(item)}
        className="flex min-h-9 w-full items-center gap-2 rounded-control px-2 py-1.5 text-left text-xs hover:bg-hover"
      >
        <SubagentAvatar agent={item.agent.agent} label={item.agent.label} />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="min-w-0 flex-1 truncate text-ink" title={item.title}>{item.title}</span>
            {item.provenance && (
              <span
                data-subagent-provenance
                title={item.provenance}
                className="inline-flex max-w-32 shrink-0 items-center truncate rounded-full bg-field px-1.5 py-0.5 text-submeta whitespace-nowrap text-ink-3"
              >
                {item.provenance}
              </span>
            )}
          </span>
          {item.doing && (
            <span data-subagent-doing className="truncate text-submeta text-ink-3" title={item.doing}>
              {item.doing}
            </span>
          )}
        </span>
        <span className="flex shrink-0 items-center gap-1 whitespace-nowrap tabular-nums">
          {item.status === "needs_you" ? (
            <>
              <StatusMark state="needs_you" size="sm" label={false} />
              <span className="font-medium text-attention">{t("chat-sessionoverview.status_needs_you")}</span>
            </>
          ) : item.status === "running" ? (
            <>
              <StatusMark state="working" size="sm" label={false} />
              {startMs !== undefined && <LiveElapsed since={startMs} />}
            </>
          ) : item.status === "failed" ? (
            <span className="font-medium text-red">{t("chat-sessionoverview.status_failed")}</span>
          ) : (
            endedLabel && <span className="text-ink-3">{endedLabel}</span>
          )}
        </span>
        <Icon name="chevronRight" size={12} className="shrink-0 text-ink-3" />
      </button>
    </li>
  );
}

function SubagentsSection({
  sectionRef,
  running,
  ended,
  onOpen,
}: {
  sectionRef: React.RefObject<HTMLElement | null>;
  running: readonly OverviewSubagentRow[];
  ended: readonly OverviewSubagentRow[];
  onOpen: (item: OverviewSubagentItem) => void;
}) {
  const [endedOpen, setEndedOpen] = useState(false);
  const now = useNow(60_000, ended.length > 0 && endedOpen);
  const hasNeedsYou = running.some((r) => r.item.status === "needs_you");
  const avatars = ended.slice(0, 4);

  return (
    <section ref={sectionRef} data-overview-subagents className="border-t border-line py-3">
      <div className="flex items-center gap-2 px-1 text-xs">
        <Icon name="agent" size={15} className="shrink-0 text-ink-3" />
        <h3 className="min-w-0 flex-1 truncate font-semibold text-ink-2">{t("chat-sessionoverview.subagents")}</h3>
        {running.length > 0 && (
          <span className={cx("shrink-0 whitespace-nowrap tabular-nums", hasNeedsYou ? "font-medium text-attention" : "text-accent-ink")}>
            {t("chat-sessionoverview.running_count", { count: running.length })}
          </span>
        )}
        {ended.length > 0 && (
          <span className="shrink-0 whitespace-nowrap tabular-nums text-ink-3">
            {t("chat-sessionoverview.ended_count", { count: ended.length })}
          </span>
        )}
      </div>

      {running.length > 0 && (
        <div data-subagents-group="running" className="mt-2">
          <div className="px-2 pb-1 text-submeta font-medium whitespace-nowrap tabular-nums text-ink-3">
            {t("chat-sessionoverview.group_running", { count: running.length })}
          </div>
          <ul className="flex flex-col gap-0.5">
            {running.map((row) => (
              <SubagentListRow key={row.item.key} row={row} now={now} onOpen={onOpen} />
            ))}
          </ul>
        </div>
      )}

      {ended.length > 0 && (
        <div data-subagents-group="ended" className={running.length > 0 ? "mt-2" : "mt-1.5"}>
          <button
            type="button"
            data-subagents-ended-toggle
            aria-expanded={endedOpen}
            onClick={() => setEndedOpen((v) => !v)}
            className="flex min-h-9 w-full items-center gap-2 rounded-control px-2 text-left text-xs hover:bg-hover"
          >
            {!endedOpen && (
              <span data-subagents-ended-avatars className="subagent-avatar-stack flex shrink-0 items-center" aria-hidden>
                {avatars.map(({ item }) => (
                  <SubagentAvatar key={item.key} agent={item.agent.agent} label={item.agent.label} className="ring-1 ring-surface" />
                ))}
              </span>
            )}
            <span className="min-w-0 flex-1 truncate whitespace-nowrap tabular-nums text-ink-2">
              {endedOpen
                ? t("chat-sessionoverview.group_ended", { count: ended.length })
                : t("chat-sessionoverview.ended_count", { count: ended.length })}
            </span>
            <Icon name="chevron" size={13} className={cx("shrink-0 text-ink-3 transition-transform", endedOpen && "rotate-180")} />
          </button>
          {endedOpen && (
            <ul className="mt-0.5 flex flex-col gap-0.5">
              {ended.map((row) => (
                <SubagentListRow key={row.item.key} row={row} now={now} onOpen={onOpen} />
              ))}
            </ul>
          )}
        </div>
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
        <span className="flex-1 truncate whitespace-nowrap">{t("chat-sessionoverview.finished_count", { count: ended.length })}</span>
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
                    {task.output_file && <button type="button" onClick={() => onOpenOutput(task.output_file!)} className="whitespace-nowrap hover:underline">{t("chat-activity.open_output_file")}</button>}
                    {onJumpToCall && call && <button type="button" onClick={() => onJumpToCall(call)} className="whitespace-nowrap hover:underline">{t("chat-sessionoverview.show_in_chat")}</button>}
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
