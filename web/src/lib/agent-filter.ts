// The Agents list's quick filters (iOS AgentFilter / QuickFilterBar): "All" / "Needs you" / "Unread" / "Working", stacked on any
// grouping. Under a filter the agents it showed when it was chosen stay (`kept`): one opened from "Unread" is seen now but
// keeps its place, so the next one is still right below it.
import { t } from "../i18n";
import { sessionHost, sessionSource, type AgentMutes } from "./agent-groups";
import { backgroundWaiting, stateOf } from "./format";
import { agentKey, isSpacePinned, type AgentPins } from "./pins";
import { isMuted } from "./session-actions";
import { unseen } from "./seen-outbox";
import type { AgentState, Session } from "./types";
import { byTime, timeMs } from "./times";
import { stateName } from "./state-labels";

export type AgentFilter = "all" | "needs" | "unread" | "working" | "bg_waiting" | "feishu" | "terminal";

export const AGENT_FILTERS: { key: AgentFilter; label: string; state?: AgentState }[] = [
  { key: "all", label: t("lib-agent-filter.all") },
  { key: "needs", label: stateName("needs_you"), state: "needs_you" },
  { key: "unread", label: stateName("unread"), state: "unread" },
  { key: "working", label: stateName("working"), state: "working" },
  // working only on background commands that wait (#6045): its own chip, out of "Working"
  { key: "bg_waiting", label: stateName("bg_waiting"), state: "working" },
  // where the agent was opened (#2526): a botmux session (its Feishu group) or a terminal engine's pane
  { key: "feishu", label: t("lib-agent-filter.feishu") },
  { key: "terminal", label: t("lib-agent-filter.terminal") },
];

const SOURCE_FILTERS = { feishu: "feishu", terminal: "terminal" } as const;

export function agentFilter(value: string | null): AgentFilter {
  return AGENT_FILTERS.find((f) => f.key === value)?.key ?? "all";
}

export const isBotmux = (s: Session) => sessionHost(s) === "botmux";

/** The sessions the list shows: all of them, or all but botmux's while it is hidden. */
export function withoutBotmux(sessions: Session[], hidden: boolean): Session[] {
  return hidden ? sessions.filter((s) => !isBotmux(s)) : sessions;
}

export function filterState(f: AgentFilter): AgentState | undefined {
  return AGENT_FILTERS.find((x) => x.key === f)?.state;
}

/** A muted agent that is working or finished unread is not counted or listed under "Unread" / "Working" (it sits under "Muted");
 * one that needs you still is, the same as the list's order. */
export function matchesFilter(s: Session, f: AgentFilter, mutes: AgentMutes): boolean {
  if (f === "feishu" || f === "terminal") return sessionSource(s) === SOURCE_FILTERS[f];
  const state = filterState(f);
  if (!state) return true;
  if (state === "working" && backgroundWaiting(s) !== (f === "bg_waiting")) return false;
  return stateOf(s) === state && (state === "needs_you" || !isMuted(s, mutes));
}

export type FilterCounts = Record<Exclude<AgentFilter, "all">, number>;

export function filterCounts(sessions: Session[], mutes: AgentMutes): FilterCounts {
  const out: FilterCounts = { needs: 0, unread: 0, working: 0, bg_waiting: 0, feishu: 0, terminal: 0 };
  for (const s of sessions) for (const f of ["needs", "unread", "working", "bg_waiting", "feishu", "terminal"] as const) if (matchesFilter(s, f, mutes)) out[f]++;
  return out;
}

/** "All", and the states that have agents right now; the chosen one stays while it empties, so it can be left. */
export function shownFilters(selection: AgentFilter, counts: FilterCounts): AgentFilter[] {
  return AGENT_FILTERS.map((f) => f.key).filter((k) => k === "all" || (k === selection && k !== "feishu") || counts[k] > 0);
}

/** The agents a filter shows: what matches now, and what it showed when it was chosen (`kept`). */
export function applyFilter(sessions: Session[], f: AgentFilter, mutes: AgentMutes, kept: ReadonlySet<string>): Session[] {
  if (f === "all") return sessions;
  return sessions.filter((s) => matchesFilter(s, f, mutes) || kept.has(agentKey(s)));
}

/** What a filter matches now, by key (what `kept` starts from when it is chosen). */
export function matchingKeys(sessions: Session[], f: AgentFilter, mutes: AgentMutes): Set<string> {
  return new Set(f === "all" ? [] : sessions.filter((s) => matchesFilter(s, f, mutes)).map(agentKey));
}

/** The finished ones nobody has seen among these, Unread or Just finished (what "Mark all as seen" marks). */
export function unreadOf(sessions: Session[]): Session[] {
  return sessions.filter(unseen);
}

// ── the spaces' order (iOS ChatsView) ──

export interface SpaceLike {
  key: string;
  machineId: string;
  name: string;
  sessions: Session[];
}

const when = (s: Session) => (stateOf(s) === "working" ? s.state_since || s.updated_at : s.updated_at) ?? "";

/** The latest of its agents' times: a working agent's every line does not move the space. */
export function spaceDate(sp: SpaceLike): string {
  return sp.sessions.reduce((m, s) => (timeMs(when(s)) > timeMs(m) ? when(s) : m), "");
}

export function isSpaceMuted(sp: SpaceLike, mutes: AgentMutes): boolean {
  return mutes.spaces.includes(`${sp.machineId}/${sp.name}`);
}

/** Which spaces lead and which fold into "Idle spaces". Pinned spaces (not muted) lead; then the busy ones, the muted
 * busy ones after them, each in the order given (herdr's); quiet ones fold. Under a quick filter every space it leaves is listed, the most
 * recent first, and none folds. */
export function arrangeSpaces<T extends SpaceLike>(spaces: T[], o: { pins: AgentPins; mutes: AgentMutes; filtered: boolean; busy: (sp: T) => boolean }): { active: T[]; idle: T[] } {
  const lead = (sp: T) => isSpacePinned(sp.machineId, sp.name, o.pins) && !isSpaceMuted(sp, o.mutes);
  const pinned = spaces.filter(lead);
  const rest = spaces.filter((sp) => !lead(sp));
  if (o.filtered) {
    return { active: [...pinned, ...rest.slice().sort((a, b) => byTime(spaceDate(b), spaceDate(a)) || a.key.localeCompare(b.key))], idle: [] };
  }
  const busy = rest.filter(o.busy);
  return {
    active: [...pinned, ...busy.filter((sp) => !isSpaceMuted(sp, o.mutes)), ...busy.filter((sp) => isSpaceMuted(sp, o.mutes))],
    idle: rest.filter((sp) => !o.busy(sp)),
  };
}
