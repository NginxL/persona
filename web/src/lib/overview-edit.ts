// Changing Overview's arrangement (#9745, #10905, lib/overview-cards.ts): what an arrangement writes and the edits
// Overview's editor makes (views/extensions/OverviewArrange.tsx), in its chunk rather than the first screen's.
import { arrangeOverview, entryKey, isSection, SECTIONS, type ArrangedItem, type CardSpan, type OverviewEntry } from "./overview-cards";

/** How many cards and sections an arrangement holds at most (the hub's limit, server/internal/assistant/navigation.go). */
export const OVERVIEW_MOST = 256;

type Items = readonly ArrangedItem<{ extension: string; id: string }>[];

/** Every card there is now and every section, in its place, with only what differs from the default (the cards not
 *  there now are left out). */
export function entriesOf(items: Items): OverviewEntry[] {
  return items.slice(0, OVERVIEW_MOST).map((i) =>
    "card" in i
      ? { extension: i.card.extension, id: i.card.id, ...(i.hidden ? { hidden: true } : {}), ...(i.span === "full" ? { span: "full" as const } : {}) }
      : { section: i.section, ...(i.hidden ? { hidden: true } : {}) },
  );
}

/** What a change writes: `list` (entriesOf `items`, changed) with the sections this web does not have (a newer web's)
 *  kept after the entry they followed in `prefs`, the arrangement before. While `prefs` names no section and the
 *  sections are all shown where they are by default, none is written: the list is then one of only cards, which a
 *  default manager from before the sections (#9745's) keeps too. Once one is written they all are, until Reset. */
export function written(items: Items, list: readonly OverviewEntry[], prefs: readonly OverviewEntry[]): OverviewEntry[] {
  const known = new Set(list.map(entryKey));
  const after = new Map<string, OverviewEntry[]>(); // by the key of the entry they followed ("": none)
  let anchor = "";
  for (const e of prefs) {
    const key = entryKey(e);
    if (known.has(key)) anchor = key;
    else if (isSection(e) && !(SECTIONS as readonly string[]).includes(e.section)) after.set(anchor, [...(after.get(anchor) ?? []), e]);
  }
  const out = [...(after.get("") ?? []), ...list.flatMap((e) => [e, ...(after.get(entryKey(e)) ?? [])])];
  if (!prefs.some(isSection)) {
    const cards = items.flatMap((i) => ("card" in i ? [i.card] : []));
    const drawn = (l: OverviewEntry[]) => arrangeOverview(cards, l).map((i) => `${i.key} ${i.hidden}`).join();
    const bare = out.filter((e) => !isSection(e));
    if (drawn(bare) === drawn(out)) return bare.slice(0, OVERVIEW_MOST);
  }
  return out.slice(0, OVERVIEW_MOST);
}

/** `list` with the card or section `key` moved one place earlier (-1) or later (+1). */
export function moveEntry(list: readonly OverviewEntry[], key: string, by: -1 | 1): OverviewEntry[] {
  const i = list.findIndex((e) => entryKey(e) === key);
  const j = i + by;
  if (i < 0 || j < 0 || j >= list.length) return [...list];
  const next = [...list];
  [next[i], next[j]] = [next[j], next[i]];
  return next;
}

/** `list` with the card or section `key` dropped on `target`: it takes the target's place, the ones between move up one. */
export function dropEntry(list: readonly OverviewEntry[], key: string, target: string): OverviewEntry[] {
  const from = list.findIndex((e) => entryKey(e) === key);
  const to = list.findIndex((e) => entryKey(e) === target);
  if (from < 0 || to < 0 || from === to) return [...list];
  const next = list.filter((_, i) => i !== from);
  next.splice(to, 0, list[from]);
  return next;
}

/** `list` with the card or section `key` hidden or shown, or (a card) half a row or the whole row. */
export function changeEntry(list: readonly OverviewEntry[], key: string, change: { hidden?: boolean; span?: CardSpan }): OverviewEntry[] {
  return list.map((e) => {
    if (entryKey(e) !== key) return e;
    const hidden = change.hidden ?? e.hidden === true;
    if (isSection(e)) return { section: e.section, ...(hidden ? { hidden: true } : {}) };
    const span = change.span ?? e.span ?? "half";
    return { extension: e.extension, id: e.id, ...(hidden ? { hidden: true } : {}), ...(span === "full" ? { span: "full" as const } : {}) };
  });
}
