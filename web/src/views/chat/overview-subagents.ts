// Pure helpers for Session Overview's Subagents section and SubagentPanel (lazy-loaded: never imported on first screen).
import { agentName, isMarked, markId, sessionAgent, type AgentSurface } from "../../lib/agent-kind";
import { rowKey } from "../../lib/child-sessions";
import { sessionTitle, stateOf } from "../../lib/format";
import { machineWithHub } from "../../lib/mesh";
import { splitSubagentLink } from "../../lib/subagent-link";
import { supervisorHeld } from "../../lib/supervisor-held";
import { subagentTitle, taskFailed, type TaskIndex } from "../../lib/tasks";
import type { Interaction, Item, Machine, Session, Subagent } from "../../lib/types";

export type OverviewSubagentStatus = "needs_you" | "running" | "failed" | "ended";

export interface OverviewSubagentItem {
  key: string;
  id: string;
  mid: string;
  sid: string;
  parentKey?: string;
  title: string;
  agent: { agent: string; label?: string; surface?: AgentSurface };
  /** Progressive provenance chip: non-empty only when machine or harness differs from the parent session. */
  provenance: string;
  status: OverviewSubagentStatus;
  startedAt?: string;
  updatedAt?: string;
  /** Latest tool or activity summary; populated only for active (running / needs_you) items. */
  doing?: string;
  /** Launching call ID (for embedded subagents) or placed child card ID (for child sessions), used by "Show in chat". */
  call?: string;
  embedded: boolean;
  session?: Session;
  subagent?: Subagent;
}

export interface OverviewSubagentRow {
  item: OverviewSubagentItem;
  /** 0..2: nested indentation depth, capped at 2 levels per DESIGN_SYSTEM. */
  depth: number;
}

const MAX_NEST_DEPTH = 2;

const oneLine = (text: string | undefined): string => (text ?? "").replace(/\s+/g, " ").trim();

/** Progressive provenance chip label: shown only when machine or harness differs from the parent, naming only what differs
 *  (e.g. "workstation · Codex", "workstation", or "Codex"; empty string when both match). */
export function subagentProvenance(
  parent: Pick<Session, "machine_id" | "agent" | "agent_label" | "source_kind" | "agent_surface" | "launcher">,
  childMid: string,
  childAgent: { agent: string; label?: string },
  machines: Machine[],
): string {
  const diffMachine = Boolean(childMid && parent.machine_id && childMid !== parent.machine_id);
  const pAgent = sessionAgent(parent);
  const pMark = markId(pAgent.agent, pAgent.label);
  const cMark = markId(childAgent.agent, childAgent.label);
  const pName = agentName(pAgent.agent, pAgent.label);
  const cName = agentName(childAgent.agent, childAgent.label);
  const diffHarness = isMarked(pMark) && isMarked(cMark)
    ? pMark !== cMark
    : pName.trim().toLowerCase() !== cName.trim().toLowerCase();
  return [diffMachine ? machineWithHub(childMid, machines) : "", diffHarness ? cName : ""].filter(Boolean).join(" · ");
}

/** Whether a standalone row in `allSessions` belongs to `parent` (same-machine folded, cross-machine folded,
 *  cross-machine A2A/parent_machine_id, or Codex parent_thread_id). Ordinary side chats (`folded_as === "side"`,
 *  e.g. `/btw`) belong to the Side sessions panel rather than Subagents. */
function rowBelongsToParent(parent: Session, row: Session): boolean {
  if (row.folded_as === "side") return false;
  if (row.machine_id === parent.machine_id && row.session_id === parent.session_id) return false;
  if (row.folded_under === parent.session_id && (row.folded_under_machine_id || row.machine_id) === parent.machine_id) {
    return true;
  }
  const sameManager = (row.manager ?? "") === (parent.manager ?? "");
  if (!sameManager) return false;
  const parentMid = row.parent_machine_id || row.machine_id;
  if (parentMid === parent.machine_id && (row.parent_session_id === parent.session_id || row.root_session_id === parent.session_id)) {
    return true;
  }
  return Boolean(
    row.machine_id === parent.machine_id &&
    !row.parent_session_id &&
    parent.source_thread_id &&
    row.parent_thread_id === parent.source_thread_id,
  );
}

/** Status of an embedded `Session.subagents` entry, incorporating user-facing pending interactions (excluding
 *  supervisor-held cards) and failed launching tool outcomes. */
export function embeddedSubagentStatus(
  mid: string,
  sid: string,
  entry: Subagent,
  interactions: readonly Interaction[],
  items: readonly Item[],
  tasks: TaskIndex,
): OverviewSubagentStatus {
  const entrySid = entry.session_id || `${sid}@${entry.id}`;
  if (interactions.some((ix) => !supervisorHeld(ix) && ix.machine_id === mid && (ix.agent_id === entry.id || ix.session_id === entrySid))) {
    return "needs_you";
  }
  if (entry.running) return "running";
  if (entry.tool_use_id) {
    const notes = tasks.notes.get(entry.tool_use_id);
    if (notes?.some((n) => n.kind === "agent" && taskFailed(n))) return "failed";
    if (items.some((it) => it.kind === "tool_result" && it.tool_use_id === entry.tool_use_id && it.is_error)) {
      return "failed";
    }
  }
  return "ended";
}

/** Status of a standalone child session row (same-machine folded or cross-machine A2A). Cards held by a task supervisor
 *  are never user-facing "Needs you": a child waiting solely on supervisor-held cards reads as Running. */
export function standaloneSubagentStatus(child: Session, interactions: readonly Interaction[]): OverviewSubagentStatus {
  const cards = interactions.filter((ix) => ix.machine_id === child.machine_id && ix.session_id === child.session_id);
  const hasUserCard = cards.some((ix) => !supervisorHeld(ix));
  const heldOnly = cards.length > 0 && !hasUserCard;
  const st = stateOf(child);
  if (hasUserCard || (st === "needs_you" && !heldOnly)) {
    return "needs_you";
  }
  if (st === "working" || (st === "needs_you" && heldOnly) || Boolean(child.background_working)) return "running";
  if (child.turn_error) return "failed";
  return "ended";
}

/** Unifies all three subagent classes for Session Overview:
 *  1. Embedded `session.subagents` (`<parent>@<agent>`)
 *  2. Same-machine folded child sessions (Codex, Paseo, workers, etc., regardless of `folded_as`)
 *  3. Cross-machine / cross-harness A2A subagents */
export function collectOverviewSubagents(params: {
  session: Session;
  children: readonly Session[];
  allSessions: readonly Session[];
  interactions: readonly Interaction[];
  items: readonly Item[];
  tasks: TaskIndex;
  machines: Machine[];
}): OverviewSubagentItem[] {
  const { session, children, allSessions, interactions, items, tasks, machines } = params;
  const mid = session.machine_id;
  const sid = session.session_id;
  const parentAgent = sessionAgent(session);
  const embedded = session.subagents ?? [];
  const embeddedById = new Map<string, string>();
  const embeddedSessionIds = new Set<string>();
  for (const entry of embedded) {
    const entrySid = entry.session_id || `${sid}@${entry.id}`;
    const k = rowKey(mid, entrySid);
    embeddedById.set(entry.id, k);
    embeddedById.set(entrySid, k);
    embeddedSessionIds.add(entrySid);
  }

  const out: OverviewSubagentItem[] = [];
  for (const entry of embedded) {
    const entrySid = entry.session_id || `${sid}@${entry.id}`;
    const status = embeddedSubagentStatus(mid, sid, entry, interactions, items, tasks);
    const active = status === "running" || status === "needs_you";
    const doing = active ? oneLine(entry.last_tool?.summary || entry.last_tool?.name) || undefined : undefined;
    out.push({
      key: rowKey(mid, entrySid),
      id: entry.id,
      mid,
      sid: entrySid,
      parentKey: entry.parent_id ? embeddedById.get(entry.parent_id) : undefined,
      title: subagentTitle(entry),
      agent: parentAgent,
      provenance: "",
      status,
      startedAt: entry.started_at,
      updatedAt: entry.updated_at ?? entry.started_at,
      doing,
      call: entry.tool_use_id,
      embedded: true,
      subagent: entry,
    });
  }

  const standalone = new Map<string, Session>();
  for (const row of allSessions) {
    if (rowBelongsToParent(session, row)) standalone.set(rowKey(row.machine_id, row.session_id), row);
  }
  for (const child of children) {
    if (child.folded_as === "side") continue;
    if (child.machine_id === mid && child.session_id === sid) continue;
    standalone.set(rowKey(child.machine_id, child.session_id), child);
  }

  for (const child of standalone.values()) {
    if (child.machine_id === mid && embeddedSessionIds.has(child.session_id)) continue;
    const status = standaloneSubagentStatus(child, interactions);
    const active = status === "running" || status === "needs_you";
    const childAgent = sessionAgent(child);
    const parentMid = child.parent_machine_id || child.machine_id;
    const parentKey =
      child.parent_session_id && !(parentMid === mid && child.parent_session_id === sid)
        ? rowKey(parentMid, child.parent_session_id)
        : undefined;
    const doing = active
      ? oneLine(child.activity || child.last_text) || undefined
      : undefined;
    out.push({
      key: rowKey(child.machine_id, child.session_id),
      id: child.session_id,
      mid: child.machine_id,
      sid: child.session_id,
      parentKey,
      title: sessionTitle(child),
      agent: childAgent,
      provenance: subagentProvenance(session, child.machine_id, childAgent, machines),
      status,
      startedAt: child.started_at ?? child.updated_at,
      updatedAt: child.updated_at ?? child.started_at,
      doing,
      embedded: false,
      session: child,
    });
  }

  return out;
}

const timeMs = (iso?: string): number => {
  const v = Date.parse(iso ?? "");
  return Number.isFinite(v) ? v : 0;
};

/** Groups unified subagents into Running (`needs_you` pinned first, then newest start first) and Ended (newest first),
 *  flattening nested subagent hierarchies up to at most 2 levels of indentation. */
export function groupOverviewSubagents(items: readonly OverviewSubagentItem[]): {
  running: OverviewSubagentRow[];
  ended: OverviewSubagentRow[];
} {
  const active = items.filter((it) => it.status === "needs_you" || it.status === "running");
  const done = items.filter((it) => it.status === "failed" || it.status === "ended");
  return {
    running: flattenGroup(active, true),
    ended: flattenGroup(done, false),
  };
}

function flattenGroup(list: readonly OverviewSubagentItem[], activeGroup: boolean): OverviewSubagentRow[] {
  if (!list.length) return [];
  const byKey = new Map(list.map((it) => [it.key, it]));
  const kids = new Map<string, OverviewSubagentItem[]>();
  const parentOf = (it: OverviewSubagentItem) =>
    it.parentKey && it.parentKey !== it.key && byKey.has(it.parentKey) ? it.parentKey : "";

  for (const it of list) {
    const p = parentOf(it);
    const arr = kids.get(p);
    if (arr) arr.push(it);
    else kids.set(p, [it]);
  }

  // Track whether a subtree contains any `needs_you` item so waiting work always rises to the top.
  const hasNeeds = new Map<string, boolean>();
  const checkNeeds = (k: string, visiting: Set<string>): boolean => {
    const cached = hasNeeds.get(k);
    if (cached !== undefined) return cached;
    if (visiting.has(k)) return false;
    visiting.add(k);
    const self = byKey.get(k)?.status === "needs_you";
    const childNeeds = (kids.get(k) ?? []).some((c) => checkNeeds(c.key, visiting));
    const res = self || childNeeds;
    hasNeeds.set(k, res);
    return res;
  };
  if (activeGroup) {
    for (const it of list) checkNeeds(it.key, new Set());
  }

  const cmp = (a: OverviewSubagentItem, b: OverviewSubagentItem): number => {
    if (activeGroup) {
      const na = a.status === "needs_you" ? 0 : hasNeeds.get(a.key) ? 1 : 2;
      const nb = b.status === "needs_you" ? 0 : hasNeeds.get(b.key) ? 1 : 2;
      if (na !== nb) return na - nb;
      const dt = timeMs(b.startedAt ?? b.updatedAt) - timeMs(a.startedAt ?? a.updatedAt);
      if (dt !== 0) return dt;
    } else {
      const dt = timeMs(b.updatedAt ?? b.startedAt) - timeMs(a.updatedAt ?? a.startedAt);
      if (dt !== 0) return dt;
    }
    return a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
  };

  for (const arr of kids.values()) arr.sort(cmp);

  const out: OverviewSubagentRow[] = [];
  const placed = new Set<string>();
  const place = (it: OverviewSubagentItem, depth: number) => {
    if (placed.has(it.key)) return;
    placed.add(it.key);
    out.push({ item: it, depth: Math.min(depth, MAX_NEST_DEPTH) });
    for (const child of kids.get(it.key) ?? []) place(child, depth + 1);
  };

  for (const root of kids.get("") ?? []) place(root, 0);
  // Cycle guard: place any remaining items at root level in sorted order.
  for (const it of [...list].sort(cmp)) place(it, 0);
  return out;
}

/** Synthesize a read-only Session descriptor for an embedded `<parent>@<agent>` subagent so embedded/standalone
 *  ChatView can display its transcript cleanly without a standalone session row on the hub. */
export function syntheticEmbeddedSubagentSession(mid: string, sid: string, parent?: Session): Session | undefined {
  const parsed = splitSubagentLink(sid);
  if (!parsed) return undefined;
  const entry = parent?.subagents?.find((a) => a.id === parsed.agent || a.session_id === sid);
  return {
    machine_id: mid,
    session_id: sid,
    title: entry ? subagentTitle(entry) : parsed.agent,
    cwd: parent?.cwd ?? "",
    agent: parent?.agent,
    agent_label: parent?.agent_label,
    agent_surface: parent?.agent_surface,
    launcher: parent?.launcher,
    model: entry?.model,
    status: entry?.running ? "working" : "idle",
    state: entry?.running ? "working" : "idle",
    started_at: entry?.started_at,
    updated_at: entry?.updated_at ?? entry?.started_at,
    is_subagent: true,
    parent_session_id: parsed.parent,
    capabilities: { read: true, send: false, interrupt: false },
  };
}
