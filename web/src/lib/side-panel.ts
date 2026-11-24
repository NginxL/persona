// A session's Side sessions panel (#7944; docs/web/web.md "Side sessions"): the side sessions the hub folds into it
// (lib/side-fold.ts) are in no list; its chat header has a "Side sessions" button with their count, and the panel is the
// Team panel's layout (views/chat/TeamPanel.tsx): a strip of the sides above the chosen one's own conversation. Unlike
// the Team panel it never opens by itself: only by hand, or for a link to a side (a deep link, a notification, a card in
// Needs you, a search hit), which opens the parent's page with that side shown. This module says which side the panel
// shows and carries those links to the parent's page.
import { useSyncExternalStore } from "react";
import { getPanel, openPanel, type Panel } from "./panel";
import { shownMember } from "./team";
import type { Session } from "./types";

/** The Side sessions panel, when it is `mid/sid`'s (the parent's). */
export type SidesPanel = Extract<Panel, { kind: "sides" }>;
export function sidesPanelOf(p: Panel | null, mid: string, sid: string): SidesPanel | undefined {
  return p?.kind === "sides" && p.mid === mid && p.sid === sid ? p : undefined;
}

// the side picked last on each parent's strip (this browser tab), so the panel opens on it again
const picked = new Map<string, string>();
const parentKey = (mid: string, sid: string) => `${mid}/${sid}`;

/** The side the panel shows: the one asked for (a link, a pick), else the one picked last for this parent, else the first
 *  that needs you, else the first (sides in the panel's order); undefined without sides. */
export function shownSide(sides: readonly Session[], mid: string, parent: string, asked?: string): Session | undefined {
  return shownMember(sides, picked.get(parentKey(mid, parent)), asked);
}

/** Shows `side` in its parent's panel (a pill picked on the strip), opening the panel if it is not. */
export function showSide(mid: string, parent: string, side: string) {
  picked.set(parentKey(mid, parent), side);
  openPanel({ kind: "sides", mid, sid: parent, side }, sidesPanelOf(getPanel(), mid, parent) ? { replace: true } : { push: false });
}

/** Opens or closes the parent's panel from its header button or ⋯. */
export function toggleSides(mid: string, parent: string, open: boolean) {
  openPanel(open ? null : { kind: "sides", mid, sid: parent }, { push: false });
}

// ── a link to a side (App: `/s/<mid>/<side>`, its card, a notification, a search hit): its parent's page, asked to show it ──
export interface SideAsk {
  side: string;
  /** A message to land on in the side's conversation (a search hit's). */
  at?: string;
}
const asks = new Map<string, SideAsk>();
const askListeners = new Set<() => void>();
const tellAsks = () => askListeners.forEach((l) => l());

/** The parent's page is asked to open its panel on `side` (a link to the side); returns the parent, to open. */
export function askSide(mid: string, parent: string, side: string, at?: string): string {
  asks.set(parentKey(mid, parent), { side, at });
  tellAsks();
  return parent;
}

/** The side the parent's page was asked to show, until it did (`doneSideAsk`). */
export function useSideAsk(mid: string, sid: string): SideAsk | undefined {
  const read = () => asks.get(parentKey(mid, sid));
  return useSyncExternalStore(
    (l) => {
      askListeners.add(l);
      return () => void askListeners.delete(l);
    },
    read,
    read,
  );
}

/** The parent's page did what it was asked: the panel open on the side (`showSide`, landing at the hit). */
export function doneSideAsk(mid: string, sid: string, ask: SideAsk) {
  if (asks.get(parentKey(mid, sid)) !== ask) return;
  asks.delete(parentKey(mid, sid));
  picked.set(parentKey(mid, sid), ask.side);
  openPanel({ kind: "sides", mid, sid, side: ask.side, at: ask.at }, { push: false });
  tellAsks();
}
