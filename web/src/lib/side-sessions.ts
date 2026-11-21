// Sessions drawn under the session that started them while staying sessions of their own (#5935): a Claude desktop
// app's side chat, /btw question or spawned session names its parent in `parent_agent_id` (docs/protocol/protocol.md
// "Session hierarchy"). Unlike a folded child (lib/child-sessions.ts) it keeps its row, its state and its place in every
// count; the lists only move it under its parent, a level in. This module is the arrangement, for any row type.
export interface Nested<T> {
  /** The rows in the order to draw them: each group where its most recent member was, its top first, then the rest. */
  rows: readonly T[];
  /** The rows drawn a level in, under their group's top. */
  nested: ReadonlySet<T>;
  /** Each row's group's most recent member (the first of `rows` given): the row that places the group. */
  lead: ReadonlyMap<T, T>;
}

/**
 * Arranges `rows` (newest first, as a list sorts them) so each row whose parent is among them comes under that parent.
 * A row nests under its topmost ancestor in `rows`, one level in whatever its depth; a row whose parent is not in
 * `rows` (another page, another filter, not listed) stays where it is; a loop links nothing. A group sits where its most
 * recent member was: the top first, then its other members in their own order. Nothing is added or left out, so every
 * count over the rows stays the same. `parentKey` gives the key of a row's parent as `key` gives a row's. When the
 * input is already structurally sorted, `activityAt` picks the activity lead by its value instead of the input order.
 */
export function nestUnderParents<T>(rows: readonly T[], key: (row: T) => string, parentKey: (row: T) => string | undefined,
  activityAt?: (row: T) => number): Nested<T> {
  const byKey = new Map<string, T>();
  for (const row of rows) byKey.set(key(row), row);
  const topOf = (row: T): T => {
    const seen = new Set([key(row)]);
    let at = row;
    for (;;) {
      const up = parentKey(at);
      const parent = up === undefined ? undefined : byKey.get(up);
      if (parent === undefined) return at;
      const k = key(parent);
      if (seen.has(k)) return row; // a loop: the row stays a top of its own
      seen.add(k);
      at = parent;
    }
  };
  const groups = new Map<T, T[]>(); // top → its members in list order, the top among them; insertion order places groups
  let moved = false;
  for (const row of rows) {
    const top = topOf(row);
    if (top !== row) moved = true;
    const members = groups.get(top);
    if (members) members.push(row);
    else groups.set(top, [row]);
  }
  const lead = new Map<T, T>();
  if (!moved) {
    for (const row of rows) lead.set(row, row);
    return { rows, nested: new Set(), lead };
  }
  const out: T[] = [];
  const nested = new Set<T>();
  for (const [top, members] of groups) {
    const first = activityAt ? members.reduce((a, b) => activityAt(a) >= activityAt(b) ? a : b) : members[0];
    out.push(top);
    lead.set(top, first);
    for (const row of members) {
      if (row === top) continue;
      out.push(row);
      nested.add(row);
      lead.set(row, first);
    }
  }
  return { rows: out, nested, lead };
}

/** The top-level parent and its contiguous side sessions, in their current group order. */
function nestedGroups<T>(nested: Nested<T>): T[][] {
  const groups: T[][] = [];
  for (const row of nested.rows) {
    if (nested.nested.has(row) && groups.length) groups[groups.length - 1]!.push(row);
    else groups.push([row]);
  }
  return groups;
}

/** A presentation sort that keeps every nested row with its top-level parent. Re-nesting the result for an activity
 *  window must pass `activityAt`, since structural order no longer puts the newest member first (#11801). */
export function orderNestedRows<T>(nested: Nested<T>, compare: (a: T, b: T) => number): readonly T[] {
  return nestedGroups(nested).sort((a, b) => compare(a[0]!, b[0]!))
    .flatMap(([top, ...side]) => [top!, ...side.sort(compare)]);
}

/** Structural placement, separate from activity and the transport's newest-first cursor order. */
export interface StableRowOrder<T> {
  compare: (a: T, b: T) => number;
}

/**
 * Reconciles presented rows with the newest-first transport window. A previous identity remains while its candidate
 * does. A full or paged read admits every new transport identity; a delta admits its marked creations and previously
 * presented identities re-entering a filter, so another older row that merely became active does not enter. An absent
 * admission set is the older-hub fallback and admits every new transport identity. The whole union is sorted by current
 * structural placement, with side sessions kept under their parents. Previous positions and activity never break ties
 * or rebase rows, so identical loaded identity sets have the same order after a reload (#11801).
 */
export function mergeStableRows<T>(previous: readonly T[], transport: readonly T[], candidates: ReadonlyMap<string, T>,
  key: (row: T) => string, order: StableRowOrder<T>, parentKey?: (row: T) => string | undefined, admittedIds?: ReadonlySet<string>): T[] {
  const retained: T[] = [];
  for (const row of previous) {
    const current = candidates.get(key(row));
    if (current !== undefined) retained.push(current);
  }
  const retainedIds = new Set(retained.map(key));
  const added = transport.filter((row) => !retainedIds.has(key(row)) && (admittedIds === undefined || admittedIds.has(key(row))));
  const combined = [...retained, ...added];
  if (!parentKey) return combined.sort(order.compare);
  return [...orderNestedRows(nestUnderParents(combined, key, parentKey), order.compare)];
}
