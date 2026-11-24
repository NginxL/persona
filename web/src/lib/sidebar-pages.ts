// The sidebar's pages as the user arranged them (Settings › General › Sidebar pages, docs/web/web.md "The sidebar's pages"): their order, which
// stay in the list and which wait under More, and the groups they are in (#9071). Kept on the hub for every device of
// the user where it has the route (lib/start-page.ts reads it at the hello and is told each change here), and in this
// browser as its copy (and alone on a hub without it).
//
// Groups: the built-in Pages group ("") and the ones extensions' nav_pages name (`group` of the place), and the user's
// own ("u:<id>"). A page goes in the group the user moved it to, else its extension's, else Pages; Settings stays in
// Pages. A group the user removes gives its pages back to where they would be without it; nothing is ever lost.
//
// What is not decided by the user: the built-in pages stay in the list; an extension's page does too while fewer
// than EXTENSION_CAP extension pages are in it, and goes under More after that, so installing a tenth extension
// does not make the list longer. Settings always stays in the list (it is where all this is changed).
// More itself can be hidden (#13798): its row, rail menu and phone-grid button disappear together. Its pages keep their
// switches and arrangement for when it comes back; Settings remains the fixed recovery path.
import { useSyncExternalStore } from "react";

const KEY = "sidebar.pages";

/** How many extension pages the list takes before the next ones wait under More (the user's own choices aside). */
export const EXTENSION_CAP = 3;

/** The page that is never put away. */
export const LOCKED = "settings";

/** A group in the user's order: its id ("" Pages, "u:<id>" the user's own, else an extension's) and the title the user
 *  gave it (none: its own). */
export type PageGroup = { id: string; title?: string };

/** What the user chose: an order (keys, the first ones first) and, per key, shown in the list or put under More; the
 *  groups in their order, the group a page was moved to, and the extensions' groups removed. */
export type SidebarPrefs = { order: string[]; shown: string[]; hidden: string[]; groups?: PageGroup[]; assign?: Record<string, string>; removed?: string[]; more?: boolean };

const NONE: SidebarPrefs = { order: [], shown: [], hidden: [] };

const isExtensionPage = (k: string) => k.startsWith("p:");

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x !== "") : []);

/** Choices as stored or as the hub sends them (anything else is none). */
export function readPrefs(v: unknown): SidebarPrefs {
  if (!v || typeof v !== "object") return NONE;
  const o = v as Record<string, unknown>;
  const p: SidebarPrefs = { order: strings(o.order), shown: strings(o.shown), hidden: strings(o.hidden), ...(typeof o.more === "boolean" ? { more: o.more } : {}) };
  const groups = (Array.isArray(o.groups) ? o.groups : []).filter((g): g is PageGroup => typeof g?.id === "string").map((g) => (g.title ? { id: g.id, title: String(g.title) } : { id: g.id }));
  const assign = Object.entries(o.assign && typeof o.assign === "object" ? o.assign : {}).filter((e): e is [string, string] => typeof e[1] === "string");
  if (groups.length) p.groups = groups;
  if (assign.length) p.assign = Object.fromEntries(assign);
  if (strings(o.removed).length) p.removed = strings(o.removed);
  return p;
}

/** The stored choices (an unreadable or broken value is none). */
export function parsePrefs(raw: string | null): SidebarPrefs {
  try {
    return raw ? readPrefs(JSON.parse(raw)) : NONE;
  } catch {
    return NONE;
  }
}

/** Whether `p` holds any choice at all. */
export const chosen = (p: SidebarPrefs) => Boolean(p.order.length || p.shown.length || p.hidden.length || p.groups?.length || p.removed?.length || Object.keys(p.assign ?? {}).length || p.more !== undefined);

/** `keys` in the user's order: the ordered ones as ordered; one the order does not know (a page added since, a new
 *  extension) right before the page that comes after it by default, so a new extension's page lands before Settings as it
 *  would have anyway, and Settings stays last. */
export function ordered(keys: readonly string[], order: readonly string[]): string[] {
  const present = new Set(keys);
  const out = order.filter((k, i) => present.has(k) && order.indexOf(k) === i);
  const known = new Set(out);
  keys.forEach((k, i) => {
    if (known.has(k)) return;
    const next = keys.slice(i + 1).find((p) => known.has(p));
    out.splice(next === undefined ? out.length : out.indexOf(next), 0, k);
  });
  return out;
}

/** Whether each page is in the list: the user's choice, else the default (built-in yes; an extension's while the
 *  list has fewer than EXTENSION_CAP of them, counted in the order shown). Settings is always in. */
export function inList(keys: readonly string[], prefs: SidebarPrefs): Set<string> {
  const shown = new Set(prefs.shown);
  const hidden = new Set(prefs.hidden);
  let extensions = keys.filter((k) => isExtensionPage(k) && shown.has(k) && !hidden.has(k)).length;
  const out = new Set<string>();
  for (const k of keys) {
    if (k === LOCKED) out.add(k);
    else if (hidden.has(k)) continue;
    else if (shown.has(k)) out.add(k);
    else if (!isExtensionPage(k)) out.add(k);
    else if (extensions < EXTENSION_CAP) {
      out.add(k);
      extensions++;
    }
  }
  return out;
}

/** Whether More itself is available. It defaults on; hiding it leaves the pages' arrangement and Settings intact. */
export const moreShown = (p: SidebarPrefs) => p.more !== false;

/** The pages split as the sidebar draws them: the list, then what waits under More, both in the user's order. */
export function arrangePages<T extends { k: string }>(pages: readonly T[], prefs: SidebarPrefs): { shown: T[]; more: T[] } {
  const byKey = new Map(pages.map((p) => [p.k, p]));
  const keys = ordered(pages.map((p) => p.k), prefs.order);
  const listed = inList(keys, prefs);
  const all = keys.map((k) => byKey.get(k)!);
  return { shown: all.filter((p) => listed.has(p.k)), more: moreShown(prefs) ? all.filter((p) => !listed.has(p.k)) : [] };
}

/** One group of the sidebar's pages as drawn: its id, its title (none: Pages, which the app names) and its pages. */
export type PageSection<T> = { id: string; title?: string; pages: T[] };

/** The groups in the user's order, with their titles: Pages first unless moved, the user's own, then the extensions'
 *  (`pages`' `group`, first named first) not removed. */
export function pageGroups(pages: readonly { group?: { id: string; title: string } }[], prefs: SidebarPrefs): { id: string; title?: string }[] {
  const named = new Map<string, string>();
  for (const p of pages) if (p.group && !prefs.removed?.includes(p.group.id) && !named.has(p.group.id)) named.set(p.group.id, p.group.title);
  const out: { id: string; title?: string }[] = [];
  const add = (id: string, title?: string) => out.some((g) => g.id === id) || out.push({ id, ...(title ? { title } : {}) });
  if (!prefs.groups?.some((g) => g.id === "")) add("");
  for (const g of prefs.groups ?? []) if (g.id === "" || g.id.startsWith("u:") || named.has(g.id)) add(g.id, g.title?.trim() || named.get(g.id));
  for (const [id, title] of named) add(id, title);
  return out;
}

/** The pages split into their groups, in order, and what waits under More (drawn at the end of Pages). */
export function arrangeSections<T extends { k: string; group?: { id: string; title: string } }>(pages: readonly T[], prefs: SidebarPrefs): { sections: PageSection<T>[]; more: T[] } {
  const { shown, more } = arrangePages(pages, prefs);
  const groups = pageGroups(pages, prefs);
  const ids = new Set(groups.map((g) => g.id));
  const groupOf = (p: T) => {
    const moved = prefs.assign?.[p.k];
    return p.k === LOCKED ? "" : moved !== undefined && ids.has(moved) ? moved : p.group && ids.has(p.group.id) ? p.group.id : "";
  };
  return { sections: groups.map((g) => ({ ...g, pages: shown.filter((p) => groupOf(p) === g.id) })), more };
}

// ── the stored choices ───────────────────────────────────

const listeners = new Set<() => void>();
let cached: { raw: string | null; prefs: SidebarPrefs } = { raw: null, prefs: NONE };
let volatile: { raw: string | null; prefs: SidebarPrefs } | null = null;

export function getSidebarPrefs(): SidebarPrefs {
  if (volatile) return volatile.prefs;
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(KEY);
  } catch {
    // storage blocked: the defaults
  }
  if (raw !== cached.raw) cached = { raw, prefs: parsePrefs(raw) };
  return cached.prefs;
}

// The hub's copy is told each change made here (lib/start-page.ts), not the ones it sent (adoptSidebarPrefs).
let told: ((p: SidebarPrefs) => void) | null = null;
export const onSidebarPrefsSaved = (f: (p: SidebarPrefs) => void) => void (told = f);

/** Takes the hub's choices as this browser's. */
export function adoptSidebarPrefs(v: unknown) {
  keep(readPrefs(v));
}

function save(p: SidebarPrefs) {
  keep(p);
  told?.(p);
}

function keep(p: SidebarPrefs) {
  const raw = chosen(p) ? JSON.stringify(p) : null;
  try {
    if (raw !== null) localStorage.setItem(KEY, raw);
    else localStorage.removeItem(KEY);
    volatile = null;
  } catch {
    volatile = { raw, prefs: p }; // storage blocked: the change lasts until the page reloads at most
  }
  cached = { raw, prefs: p };
  listeners.forEach((l) => l());
}

/** Puts page `k` in the list (`on`) or under More; the choice sticks over the default. Settings cannot be put away. */
export function setPageShown(k: string, on: boolean) {
  if (k === LOCKED) return;
  const p = getSidebarPrefs();
  save({ ...p, shown: on ? [...p.shown.filter((x) => x !== k), k] : p.shown.filter((x) => x !== k), hidden: on ? p.hidden.filter((x) => x !== k) : [...p.hidden.filter((x) => x !== k), k] });
}

/** Shows or hides More itself. Off does not promote its pages or discard their arrangement. */
export function setMoreShown(on: boolean) {
  save({ ...getSidebarPrefs(), more: on });
}

/** The whole order, as the settings list now has it. */
export function setPageOrder(keys: readonly string[]) {
  save({ ...getSidebarPrefs(), order: [...keys] });
}

/** Moves `k` one place up (-1) or down (+1) among `keys` (the order shown). */
export function movePage(keys: readonly string[], k: string, by: -1 | 1) {
  const i = keys.indexOf(k);
  const j = i + by;
  if (i < 0 || j < 0 || j >= keys.length) return;
  const next = [...keys];
  [next[i], next[j]] = [next[j], next[i]];
  setPageOrder(next);
}

/** Puts back choices as they were (a removed group's Undo). */
export const restoreSidebarPrefs = (p: SidebarPrefs) => save(p);

/** Back to the defaults: order, list, More and groups as if nothing had been chosen. */
export function resetSidebarPrefs() {
  save(NONE);
}

// ── groups (#9071) ───────────────────────────────────────
// `ids` is the groups' order as drawn (pageGroups): the first change to it writes the whole order down.

const listed = (ids: readonly string[], p: SidebarPrefs): PageGroup[] => ids.map((id) => p.groups?.find((g) => g.id === id) ?? { id });

/** Makes a group of the user's own at the end, named `title`; its id. */
export function addPageGroup(ids: readonly string[], title: string): string {
  const p = getSidebarPrefs();
  const id = `u:${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`;
  save({ ...p, groups: [...listed(ids, p), { id, title: title.trim() }] });
  return id;
}

/** Names group `id` `title` (a blank one gives an extension's group its own name back). */
export function renamePageGroup(ids: readonly string[], id: string, title: string) {
  const p = getSidebarPrefs();
  save({ ...p, groups: listed(ids, p).map((g) => (g.id === id ? (title.trim() ? { id, title: title.trim() } : { id }) : g)) });
}

/** Moves group `id` one place up (-1) or down (+1). */
export function movePageGroup(ids: readonly string[], id: string, by: -1 | 1) {
  const p = getSidebarPrefs();
  const groups = listed(ids, p);
  const i = groups.findIndex((g) => g.id === id);
  const j = i + by;
  if (i < 0 || j < 0 || j >= groups.length) return;
  [groups[i], groups[j]] = [groups[j], groups[i]];
  save({ ...p, groups });
}

/** Removes group `id`: its pages go back to where they would be without it (their extension's group, else Pages). An
 *  extension's group stays away until Reset. Pages cannot be removed. */
export function removePageGroup(id: string) {
  if (!id) return;
  const p = getSidebarPrefs();
  const assign = Object.fromEntries(Object.entries(p.assign ?? {}).filter(([, g]) => g !== id));
  save({ ...p, groups: (p.groups ?? []).filter((g) => g.id !== id), assign, removed: id.startsWith("u:") ? p.removed : [...(p.removed ?? []).filter((g) => g !== id), id] });
}

/** Moves page `k` into group `id` ("" Pages). */
export function setPageGroup(k: string, id: string) {
  const p = getSidebarPrefs();
  save({ ...p, assign: { ...p.assign, [k]: id } });
}

if (typeof window !== "undefined") {
  // another tab changed it
  window.addEventListener("storage", (e) => {
    if (e.key === KEY || e.key === null) {
      volatile = null;
      listeners.forEach((l) => l());
    }
  });
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

export function useSidebarPrefs(): SidebarPrefs {
  return useSyncExternalStore(subscribe, getSidebarPrefs, () => cached.prefs);
}
