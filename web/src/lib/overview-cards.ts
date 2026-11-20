// Overview as the user arranged it (#9745, #10905; docs/web/web.md "Overview's extension cards"): the extensions' cards
// (their order, the ones hidden, and each one's width, half a row, the default, as they always were, or the whole row)
// and Overview's own sections among them (their order, and the ones hidden where that is safe). Kept on the hub for
// every device of the user (`overview` of GET / PUT /api/v1/navigation; lib/start-page.ts reads it at the hello and
// hears each push, and sends each change made here) and in this browser as its copy, alone where the hub (or its
// default manager) can't keep it.
//
// Which cards there are is the hub's list (lib/extension-slots.ts, in its order: by extension, then the manifest's);
// the sections are SECTIONS, above the cards by default (the cards last, #11147). A card or section the arrangement
// names comes in its place. A card it does not name (an extension installed since) comes after the last card it names
// (none: at the end, where the cards are by default), half a row and shown, in the hub's order; a section it does not
// name (one added since) where it is by default, right after the section before it by default (Needs you: first), so
// a section the user put first stays first. An entry naming a card that is not
// there now is passed over, and kept until the user saves another arrangement (it holds only the cards there are
// then); one naming a section this web does not have (a newer web's) is kept where it is. Changing it is
// lib/overview-edit.ts's, loaded with Overview's editor: the first screen only draws.
import { useSyncExternalStore } from "react";
import { feedCacheScope, scopedKey } from "./feed-cache";

const KEY = "overview.cards";

/** A card's width: half a row (the default) or the whole row. */
export type CardSpan = "half" | "full";

/** One card as the user placed it: the extension's id and the card's (its overview_card's id), hidden or not, its width. */
export type CardEntry = { extension: string; id: string; hidden?: boolean; span?: CardSpan };
/** One of Overview's own sections as the user placed it (#10905): its id, hidden or not. */
export type SectionEntry = { section: string; hidden?: boolean };
export type OverviewEntry = CardEntry | SectionEntry;

/** Overview's own sections under the greeting (views/Home.tsx), by the ids the arrangement names them by, in their
 *  default order: Needs you, Stuck (waiting on a computer), Tasks in progress, Working, Unread, Computers. */
export const SECTIONS = ["needs_you", "waiting_on_computer", "in_progress", "working", "unread", "computers"] as const;
export type SectionId = (typeof SECTIONS)[number];
/** The sections that are only moved, never hidden: their rows need the user (docs/design/DESIGN_SYSTEM.md 3.4). */
export const ALWAYS_SHOWN: readonly string[] = ["needs_you", "waiting_on_computer"];

/** A card as Overview draws it: the hub's card, whether it is hidden, its width. */
export type ArrangedCard<T> = { card: T; key: string; hidden: boolean; span: CardSpan };
/** A section as Overview draws it: which, whether it is hidden. */
export type ArrangedSection = { section: SectionId; key: string; hidden: boolean };
export type ArrangedItem<T> = ArrangedCard<T> | ArrangedSection;

/** A card's key: `<extension>/<id>`. */
export const cardKey = (c: { extension: string; id: string }) => `${c.extension}/${c.id}`;
/** A section's key: `section:<id>`, never a card's (a card's has a "/"). */
export const sectionKey = (id: string) => `section:${id}`;
export const isSection = (e: OverviewEntry): e is SectionEntry => typeof (e as SectionEntry).section === "string";
export const entryKey = (e: OverviewEntry) => (isSection(e) ? sectionKey(e.section) : cardKey(e));

/** An arrangement as stored or as the hub sends it (anything else is none; the hub checked its shape). */
export const readOverview = (v: unknown): OverviewEntry[] =>
  Array.isArray(v) ? v.filter((e) => typeof e?.section === "string" || (typeof e?.extension === "string" && typeof e.id === "string")) : [];

/** Overview's cards and sections in the user's arrangement: the named ones in their place, hidden and wide as chosen;
 *  the sections it does not name each after the one before it by default (Needs you: first); the cards it does not
 *  name after the last one it names (none: at the end), in the hub's order, half a row and shown. Without an
 *  arrangement, Overview's default (#11147): the sections in their order, then the cards. */
export function arrangeOverview<T extends { extension: string; id: string }>(cards: readonly T[], prefs: readonly OverviewEntry[]): ArrangedItem<T>[] {
  const byKey = new Map<string, T>();
  for (const c of cards) if (!byKey.has(cardKey(c))) byKey.set(cardKey(c), c);
  const out: ArrangedItem<T>[] = [];
  const placed = new Set<string>();
  for (const e of prefs) {
    const key = entryKey(e);
    if (placed.has(key)) continue; // named twice
    if (isSection(e)) {
      if (!(SECTIONS as readonly string[]).includes(e.section)) continue; // a newer web's
      out.push({ section: e.section as SectionId, key, hidden: e.hidden === true && !ALWAYS_SHOWN.includes(e.section) });
    } else {
      const card = byKey.get(key);
      if (!card) continue; // not there now
      out.push({ card, key, hidden: e.hidden === true, span: e.span === "full" ? "full" : "half" });
    }
    placed.add(key);
  }
  // in their order: the one before each is in place by then
  SECTIONS.forEach((id, i) => {
    const key = sectionKey(id);
    if (!placed.has(key)) out.splice(i ? out.findIndex((x) => x.key === sectionKey(SECTIONS[i - 1])) + 1 : 0, 0, { section: id, key, hidden: false });
  });
  const last = out.findLastIndex((i) => "card" in i);
  let at = last < 0 ? out.length : last + 1;
  for (const [key, card] of byKey) if (!placed.has(key)) out.splice(at++, 0, { card, key, hidden: false, span: "half" });
  return out;
}

/** What a list of only cards from the hub leaves of `stored` while the sections are this browser's alone (a default
 *  manager from before them, #9745's, keeps cards only): the manager's own rule (server/internal/assistant/navigation.go
 *  keepSections). The sections stay where they are and the cards take the stored cards' places in `list`'s order, one
 *  `stored` did not have going after the last of them (none: at the end, where the cards are by default); an empty list (Reset) is taken as it is, as is one
 *  naming a section or one that would hold more than the hub's most. */
export function keepSections(stored: readonly OverviewEntry[], list: OverviewEntry[]): OverviewEntry[] {
  if (!list.length || list.some(isSection) || !stored.some(isSection)) return list;
  const out: OverviewEntry[] = [];
  let next = 0;
  let after = -1;
  for (const e of stored) {
    if (isSection(e)) out.push(e);
    else if (next < list.length) {
      out.push(list[next++]);
      after = out.length;
    }
  }
  out.splice(after < 0 ? out.length : after, 0, ...list.slice(next));
  return out.length > 256 ? list : out;
}

/** A row of cards that come one after another, or a section: how Overview lays the arrangement out. */
export type OverviewRun<T> = { key: string; cards: ArrangedCard<T>[] } | ArrangedSection;

/** The arrangement as Overview lays it out: each run of cards (hidden ones too) one grid, each section on its own. A
 *  run is named by its place among the runs, so a section moved past it does not draw its cards again. */
export function overviewRuns<T>(items: readonly ArrangedItem<T>[]): OverviewRun<T>[] {
  const out: OverviewRun<T>[] = [];
  let run: ArrangedCard<T>[] | null = null;
  let n = 0;
  for (const it of items) {
    if (!("card" in it)) {
      out.push(it);
      run = null;
    } else if (run) run.push(it);
    else out.push({ key: `cards:${n++}`, cards: (run = [it]) });
  }
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

// Whether the hub's default manager refused the sections (#9745's, lib/start-page.ts): they are then this browser's
// alone, noted per hub and sign-in beside the copy (signing out removes both) and laid over the cards the hub sends
// (keepSections), until a hello finds the manager takes them. `local` is the note as read under its sign-in, kept in
// this page where storage is blocked.
const LOCAL = "overview.sectionsLocal";
let local: { scope: string | null; on: boolean } | undefined;
export function sectionsLocal() {
  const now = feedCacheScope();
  if (local?.scope !== now) {
    let on = false;
    try {
      on = localStorage.getItem(scopedKey(now, LOCAL) ?? "") === "1";
    } catch {
      // storage blocked: not noted
    }
    local = { scope: now, on };
  }
  return local.on;
}
export function noteSectionsLocal(on: boolean) {
  if (sectionsLocal() === on) return;
  local!.on = on;
  const key = scopedKey(local!.scope, LOCAL);
  try {
    if (key && on) localStorage.setItem(key, "1");
    else if (key) localStorage.removeItem(key);
  } catch {
    // storage blocked: this page only
  }
  changed();
}

if (typeof window !== "undefined") {
  // another tab changed it: newer for Undo only where this sign-in's copy is not what it was (not another sign-in's
  // copy, nor storage cleared while this one held none)
  window.addEventListener("storage", (e) => {
    if (e.key === null || e.key.endsWith(`:${LOCAL}`)) {
      local = undefined;
      changed();
    }
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
export const useSectionsLocal = () => useSyncExternalStore(subscribe, sectionsLocal, sectionsLocal);
