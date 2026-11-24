// Where the navigation shortcuts go (docs/web/web.md "Keyboard shortcuts"): which panel ⌘B opens or closes, what ⌘J switches to, the
// tab next to this one. Pure, so it is tested without a DOM.
import { buildMachineTree, paneRoute, type HerdrMachine, type HerdrSpace } from "./herdr-tree";
import type { Panel } from "./panel";
import type { Route, SessionView } from "./router";
import { otherView } from "./session-view";
import { splitSubagentLink } from "./subagent-link";
import { isPaneAddress } from "./terminals";
import type { Machine, Session } from "./types";

/** The conversation on the page: a session's, or the pane a tab opens on. */
export interface PaneRef {
  mid: string;
  sid: string;
}

/** A panel that belongs to a conversation (files / Git / subagent) is only meant for that one; the others (a link, a task…) go anywhere. */
function fits(panel: Panel, pane: PaneRef | undefined): boolean {
  if (panel.kind === "files" || panel.kind === "git") return Boolean(pane && panel.mid === pane.mid && panel.sid === pane.sid);
  if (panel.kind === "subagent") {
    const pMid = panel.parentMid ?? panel.mid;
    const pSid = panel.parentSid ?? splitSubagentLink(panel.sid)?.parent;
    return Boolean(pane && pane.mid === pMid && pane.sid === pSid);
  }
  return true;
}

/**
 * ⌘B: an open panel closes (and is remembered); with none open, the one just closed comes back if it still fits the
 * page, else the conversation's files (`cwd`: its directory; the terminal is the page's own view now, ⌘J). null: nothing to open.
 */
export function panelToggle(current: Panel | null, last: Panel | null, pane: PaneRef | undefined, cwd?: string): { close: true } | { open: Panel } | null {
  if (current) return { close: true };
  if (last && fits(last, pane)) return { open: last };
  return pane && cwd ? { open: { kind: "files", mid: pane.mid, sid: pane.sid, cwd } } : null;
}

/**
 * ⌘J, terminal ⇄ chat. On an agent's page the middle switches in place to the other view (`view`: the one showing; null
 * when the agent has no terminal to show). On the terminal page: the agent's conversation, when the terminal is an
 * agent's (a plain shell has no chat). null: not here.
 */
export function terminalChat(route: Route, view: SessionView, canTerminal: boolean): { go: Route } | null {
  if (route.name === "terminal") {
    return route.mid && route.id && !isPaneAddress(route.id) ? { go: { name: "session", mid: route.mid, sid: route.id, view: "chat" } } : null;
  }
  if ((route.name === "session" || route.name === "tab") && canTerminal) return { go: { ...route, view: otherView(view) } };
  return null;
}

/** The pages of a machine's tabs in the sidebar's order: each tab, then what has no tab (loose agents, "Other"). */
export function machineStops(tree: HerdrMachine): { sids: string[]; to: Route }[] {
  const walk = (sp: HerdrSpace): { sids: string[]; to: Route }[] => [
    ...sp.tabs.map((t) => ({ sids: t.sessions.map((s) => s.session_id), to: { name: "tab", mid: t.lead.machine_id, space: t.spaceId, slot: t.slotId } as Route })),
    ...sp.loose.map((s) => ({ sids: [s.session_id], to: paneRoute(s) })),
    ...sp.children.flatMap(walk),
  ];
  return [...tree.spaces.flatMap(walk), ...tree.other.map((s) => ({ sids: [s.session_id], to: paneRoute(s) }))];
}

/** ⌘⌥← / →: the tab before / after the one on the page, on the same machine, wrapping round; undefined when there is none to go to. */
export function adjacentTab(machines: Machine[], sessions: Session[], at: PaneRef | undefined, dir: 1 | -1): Route | undefined {
  if (!at) return undefined;
  const tree = buildMachineTree(machines, sessions).find((m) => m.machine.id === at.mid);
  if (!tree) return undefined;
  const stops = machineStops(tree);
  const i = stops.findIndex((s) => s.sids.includes(at.sid));
  if (i < 0 || stops.length < 2) return undefined;
  return stops[(i + dir + stops.length) % stops.length]!.to;
}

/** ⌘1–9: the n-th machine, as the sidebar counts them. */
export const machineAt = (machines: readonly Machine[], n: number): Machine | undefined => (n >= 1 ? machines[n - 1] : undefined);
