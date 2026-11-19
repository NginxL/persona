// The Inbox's section bar (docs/assistant/inbox.md "Section bar", #9053): which choices it offers with what counts,
// which sections the list draws under a choice, what an empty choice says, where an item opened from outside the list
// lands, and how a search moves between choices. Pure, so spec/fixtures/inbox/bar*.json replay what the list draws.
import { isDone, type InboxItem, type InboxSections, type SectionKey } from "./inbox";
import { statusOf } from "./inbox-events";

/** A choice in the bar: one section, or All (every section stacked, as the page was). Collapsed has none of its own. */
export type BarChoice = "action" | "working" | "fyi" | "done" | "all";
export const BAR_CHOICES: readonly BarChoice[] = ["action", "working", "fyi", "done", "all"];

/** A section the list can draw: the open ones (Collapsed included) and Done, in the list's order. */
export type BarSection = SectionKey | "done";
const ORDER: readonly BarSection[] = ["action", "working", "fyi", "noise", "done"];

/** `needs`: Needs you's count (what waits for the user); `neutral`: a passive total. Done and All carry none. */
export type BarTone = "needs" | "neutral";
export interface BarEntry {
  id: BarChoice;
  count: number | null;
  tone: BarTone | null;
}

/** Why the list draws nothing under the choice: nothing open anywhere (caught_up, or filtered / no_match under the chips
 *  or the search), or only this choice is empty (section). */
export type BarEmpty = "caught_up" | "section" | "filtered" | "no_match" | null;

/** Each section's rows under the filters and the search: Needs you's items and the Agents waiting, In progress, FYI,
 *  Collapsed, and the done rows loaded (the list's and its history's). */
export interface BarRows {
  action: number;
  working: number;
  fyi: number;
  noise: number;
  done: number;
}

/** The rows of `sections()`'s result, with the Agents waiting (`waitingAgents`) heading Needs you and Done's loaded
 *  rows (by default the list's own). */
export function barRows(sec: InboxSections, agents: number, done = sec.done.length): BarRows {
  return { action: sec.tiers.action.length + agents, working: sec.tiers.working.length, fyi: sec.tiers.fyi.length, noise: sec.tiers.noise.length, done };
}

export interface BarInput {
  rows: BarRows;
  chosen: BarChoice;
  /** A source, kind or folder narrows the list. */
  chips: boolean;
  /** The search field has text. */
  search: boolean;
  /** The hub's day counts added up; undefined on a hub without them. They count only with no filter and no search. */
  doneDays?: number;
}

export interface InboxBar {
  bar: BarEntry[];
  /** The sections drawn, in the list's order. */
  shown: BarSection[];
  empty: BarEmpty;
}

/** The bar, what the list draws under `chosen`, and why it is empty when it draws nothing. */
export function inboxBar({ rows, chosen, chips, search, doneDays }: BarInput): InboxBar {
  const has: Record<BarSection, boolean> = {
    action: rows.action > 0,
    working: rows.working > 0,
    fyi: rows.fyi > 0,
    noise: rows.noise > 0,
    done: rows.done > 0 || (!chips && !search && (doneDays ?? 0) > 0),
  };
  // Needs you and All always; the others while they have a row, or while chosen (so a choice emptied can be left)
  const bar = BAR_CHOICES.filter((id) => id === "action" || id === "all" || has[id] || id === chosen).map((id): BarEntry => ({
    id,
    count: id === "action" || id === "working" || id === "fyi" ? rows[id] : null,
    tone: id === "action" ? "needs" : id === "working" || id === "fyi" ? "neutral" : null,
  }));
  const shown = chosen === "all" ? ORDER.filter((k) => has[k]) : has[chosen] ? [chosen] : [];
  const open = has.action || has.working || has.fyi || has.noise;
  // nothing open anywhere: the whole Inbox's empty state, as before the bar
  const nothing: BarEmpty = chips ? "filtered" : search ? "no_match" : "caught_up";
  let empty: BarEmpty = null;
  if (chosen === "done") empty = has.done ? null : chips ? "filtered" : search ? "no_match" : "section";
  else if (chosen === "all") empty = open ? null : nothing;
  else if (!has[chosen]) empty = open ? "section" : nothing;
  return { bar, shown, empty };
}

/** Where an item opened from outside the list lands: its section, Done for a done one, and All with Collapsed open
 *  for a collapsed one (Collapsed has no choice of its own). */
export function barLanding(it: InboxItem): { choice: BarChoice; openCollapsed: boolean } {
  if (isDone(it)) return { choice: "done", openCollapsed: false };
  const section = statusOf(it);
  return section === "noise" ? { choice: "all", openCollapsed: true } : { choice: section, openCollapsed: false };
}

/** The section an item is drawn in. */
export function barSectionOf(it: InboxItem): BarSection {
  return isDone(it) ? "done" : statusOf(it);
}

/** The choice and the one a search put aside (to come back to when its text is cleared), with the search's text. */
export interface BarSearch {
  chosen: BarChoice;
  saved: BarChoice | null;
  query: string;
}

/** The search text becomes `query`; a choice tapped; the Inbox entered with this search text. */
export type BarStep = { query: string } | { pick: BarChoice } | { enter: string };

/** Whether the search field's text searches: blank text matches everything (`matches` trims it), so it is no search. */
export const searching = (query: string) => query.trim() !== "";

/**
 * A search looks through the whole Inbox: text appearing puts the choice aside and shows All, a choice tapped meanwhile
 * narrows the matches, and clearing the text brings back the choice put aside (Needs you when none). Entering the Inbox
 * shows Needs you, or All with Needs you put aside when a kept search text comes with it.
 */
export function barStep(s: BarSearch, step: BarStep): BarSearch {
  if ("pick" in step) return { ...s, chosen: step.pick };
  if ("enter" in step) return searching(step.enter) ? { chosen: "all", saved: "action", query: step.enter } : { chosen: "action", saved: null, query: step.enter };
  const was = searching(s.query);
  const now = searching(step.query);
  if (!was && now) return { chosen: "all", saved: s.chosen, query: step.query };
  if (was && !now) return { chosen: s.saved ?? "action", saved: null, query: step.query };
  return { ...s, query: step.query };
}
