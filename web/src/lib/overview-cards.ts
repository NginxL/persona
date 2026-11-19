// Overview's extension cards as the user arranged them (#9745, docs/web/web.md "Overview's extension cards"): their
// order, the ones hidden, and each one's width, half a row (the default, as they always were) or the whole row. Kept
// on the hub for every device of the user (`overview` of GET / PUT /api/v1/navigation; lib/start-page.ts reads it at
// the hello and hears each push, and sends each change made here) and in this browser as its copy, alone where the hub
// (or its default manager) can't keep it.
//
// Which cards there are is the hub's list (lib/extension-slots.ts, in its order: by extension, then the manifest's).
// A card the arrangement names comes in its place; one it does not name (an extension installed since) after the named
// ones, half a row and shown, in the hub's order; an entry naming a card that is not there now is passed over, and kept
// until the user saves another arrangement (it holds only the cards there are then). Changing it is
// lib/overview-edit.ts's, loaded with Overview's card editor: the first screen only draws the cards.
import { useSyncExternalStore } from "react";
import { feedCacheScope, scopedKey } from "./feed-cache";

const KEY = "overview.cards";

/** A card's width: half a row (the default) or the whole row. */
export type CardSpan = "half" | "full";

/** One card as the user placed it: the extension's id and the card's (its overview_card's id), hidden or not, its width. */
export type OverviewEntry = { extension: string; id: string; hidden?: boolean; span?: CardSpan };

/** A card as Overview draws it: the hub's card, whether it is hidden, its width. */
export type ArrangedCard<T> = { card: T; key: string; hidden: boolean; span: CardSpan };

/** A card's key: `<extension>/<id>`. */
export const cardKey = (c: { extension: string; id: string }) => `${c.extension}/${c.id}`;

/** An arrangement as stored or as the hub sends it (anything else is none; the hub checked its shape). */
export const readOverview = (v: unknown): OverviewEntry[] =>
  Array.isArray(v) ? v.filter((e) => typeof e?.extension === "string" && typeof e.id === "string") : [];

/** The cards in the user's arrangement: the named ones in their place, hidden and wide as chosen; then the others in the
 *  hub's order, half a row and shown. Without an arrangement, the hub's cards as they always were. */
export function arrangeCards<T extends { extension: string; id: string }>(cards: readonly T[], prefs: readonly OverviewEntry[]): ArrangedCard<T>[] {
  const byKey = new Map<string, T>();
  for (const c of cards) if (!byKey.has(cardKey(c))) byKey.set(cardKey(c), c);
  const out: ArrangedCard<T>[] = [];
  for (const e of prefs) {
    const key = cardKey(e);
    const card = byKey.get(key);
    if (!card) continue; // not there now, or named twice
    byKey.delete(key);
    out.push({ card, key, hidden: e.hidden === true, span: e.span === "full" ? "full" : "half" });
  }
  for (const [key, card] of byKey) out.push({ card, key, hidden: false, span: "half" });
  return out;
}

/** Whether the half-row cards go two to a row from a tablet's width: when two or more of them are drawn (`all`: the
 *  hidden ones too, drawn while arranging). A lone one fills the row until the page has room for two columns of rows
 *  (web audit item 76; ExtensionSlots' grid). */
export const halvesPaired = (cards: readonly { hidden: boolean; span: CardSpan }[], all = false) => cards.filter((c) => (all || !c.hidden) && c.span === "half").length > 1;

// ── the stored arrangement ───────────────────────────────

const listeners = new Set<() => void>();
const changed = () => listeners.forEach((l) => l());
// This browser's copy, kept per hub and sign-in (lib/feed-cache.ts scopedKey, as the extensions' places list is; signing
// out removes it): another hub or account in this browser neither draws it nor hands it to its hub. `prefs` is the copy
// as read under `scope`, read again when the scope changes (null: not read yet).
let prefs: OverviewEntry[] | null = null;
let scope: string | null = null;
function read(key: string | null) {
  try {
    return key ? readOverview(JSON.parse(localStorage.getItem(key) ?? "[]")) : [];
  } catch {
    return []; // none, broken, or storage blocked: as they always were
  }
}

export function getOverviewPrefs(): OverviewEntry[] {
  const now = feedCacheScope();
  if (prefs === null || now !== scope) {
    scope = now;
    prefs = read(scopedKey(now, KEY));
  }
  return prefs;
}

/** Whether this browser holds an arrangement at all (none: the cards as they always were). */
export const overviewChosen = () => getOverviewPrefs().length > 0;

// Bumped by each arrangement kept here (made here or taken from the hub) and each change another tab made to this
// sign-in's copy: one equal to an earlier (another Reset) is newer all the same (OverviewArrange's Undo).
let revision = 0;
export const overviewRevision = () => revision;

// Bumped by each change another tab made to this sign-in's copy: a hello's read begun before it is older (lib/start-page.ts).
let elsewhere = 0;
export const overviewElsewhere = () => elsewhere;

// The hub's copy is told each change made here (lib/start-page.ts), not the ones it sent (adoptOverview).
let told: ((p: OverviewEntry[]) => void) | null = null;
export const onOverviewSaved = (f: (p: OverviewEntry[]) => void) => void (told = f);

/** Takes the hub's arrangement as this browser's. */
export function adoptOverview(v: unknown) {
  keep(readOverview(v));
}

/** Keeps `list` as the arrangement and sends it to the hub (none: the cards as they always were). */
export function saveOverview(list: OverviewEntry[]) {
  keep(list);
  told?.(list);
}

function keep(p: OverviewEntry[]) {
  scope = feedCacheScope();
  prefs = p;
  revision++;
  const key = scopedKey(scope, KEY);
  try {
    if (key && p.length) localStorage.setItem(key, JSON.stringify(p));
    else if (key) localStorage.removeItem(key);
  } catch {
    // storage blocked: the change lasts until the page reloads
  }
  changed();
}

// Whether the hub keeps the arrangement: null until it is known (again for another sign-in), false where its default
// manager answered a change of it without taking it (a manager from before the field; a hub without the route at all is
// lib/start-page.ts's).
let kept: boolean | null = null;
export const overviewKept = () => kept;
export function noteOverviewKept(on: boolean | null) {
  if (kept !== on) {
    kept = on;
    changed();
  }
}

if (typeof window !== "undefined") {
  // another tab changed it: newer for Undo only where this sign-in's copy is not what it was (not another sign-in's
  // copy, nor storage cleared while this one held none)
  window.addEventListener("storage", (e) => {
    if (e.key === null || e.key.endsWith(`:${KEY}`)) {
      const ours = e.key === scopedKey(feedCacheScope(), KEY);
      if (e.key === null ? !!prefs?.length : ours && e.oldValue !== e.newValue) {
        revision++;
        elsewhere++;
      }
      prefs = null;
      getOverviewPrefs(); // read again now: a clear after this knows whether this sign-in held a copy
      changed();
    }
  });
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

export const useOverviewPrefs = () => useSyncExternalStore(subscribe, getOverviewPrefs, getOverviewPrefs);
export const useOverviewKept = () => useSyncExternalStore(subscribe, overviewKept, overviewKept);
