// Changing Overview's card arrangement (#9745, lib/overview-cards.ts): what an arrangement writes and the edits Overview's
// card editor makes (views/extensions/OverviewArrange.tsx), in its chunk rather than the first screen's.
import { cardKey, type ArrangedCard, type CardSpan, type OverviewEntry } from "./overview-cards";

/** How many cards an arrangement holds at most (the hub's limit, server/internal/assistant/navigation.go). */
export const OVERVIEW_MOST = 256;

/** The arrangement a change writes: every card there is now, in its place, with only what differs from the default
 *  (the cards not there now are left out). */
export function entriesOf(cards: readonly ArrangedCard<{ extension: string; id: string }>[]): OverviewEntry[] {
  return cards.slice(0, OVERVIEW_MOST).map(({ card, hidden, span }) => ({ extension: card.extension, id: card.id, ...(hidden ? { hidden: true } : {}), ...(span === "full" ? { span: "full" as const } : {}) }));
}

/** `list` with the card `key` moved one place earlier (-1) or later (+1). */
export function moveEntry(list: readonly OverviewEntry[], key: string, by: -1 | 1): OverviewEntry[] {
  const i = list.findIndex((e) => cardKey(e) === key);
  const j = i + by;
  if (i < 0 || j < 0 || j >= list.length) return [...list];
  const next = [...list];
  [next[i], next[j]] = [next[j], next[i]];
  return next;
}

/** `list` with the card `key` dropped on the card `target`: it takes the target's place, the cards between move up one. */
export function dropEntry(list: readonly OverviewEntry[], key: string, target: string): OverviewEntry[] {
  const from = list.findIndex((e) => cardKey(e) === key);
  const to = list.findIndex((e) => cardKey(e) === target);
  if (from < 0 || to < 0 || from === to) return [...list];
  const next = list.filter((_, i) => i !== from);
  next.splice(to, 0, list[from]);
  return next;
}

/** `list` with the card `key` hidden or shown, or half a row or the whole row. */
export function changeEntry(list: readonly OverviewEntry[], key: string, change: { hidden?: boolean; span?: CardSpan }): OverviewEntry[] {
  return list.map((e) => {
    if (cardKey(e) !== key) return e;
    const hidden = change.hidden ?? e.hidden === true;
    const span = change.span ?? e.span ?? "half";
    return { extension: e.extension, id: e.id, ...(hidden ? { hidden: true } : {}), ...(span === "full" ? { span: "full" as const } : {}) };
  });
}
