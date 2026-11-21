import { describe, expect, it } from "vitest";
import { mergeStableRows, nestUnderParents, orderNestedRows } from "./side-sessions";

type Row = { id: string; parent?: string; place?: number; activity?: number };
const r = (id: string, parent?: string, place?: number): Row => ({ id, parent, place });
const key = (x: Row) => x.id;
const parentKey = (x: Row) => x.parent;
const ids = (rows: readonly Row[]) => rows.map(key);
const nestedIds = (rows: readonly Row[], nested: ReadonlySet<Row>) => rows.filter((x) => nested.has(x)).map(key);

describe("nestUnderParents", () => {
  it("gives the same rows back when none names a loaded parent", () => {
    const rows = [r("a"), r("b", "gone"), r("c")];
    const out = nestUnderParents(rows, key, parentKey);
    expect(out.rows).toBe(rows);
    expect(out.nested.size).toBe(0);
    expect(rows.every((x) => out.lead.get(x) === x)).toBe(true);
  });

  it("puts side sessions under their parent, the group where its most recent member was", () => {
    // newest first: a side chat of p is the newest row, p itself is older than q
    const rows = [r("s1", "p"), r("q"), r("s2", "p"), r("p"), r("z")];
    const out = nestUnderParents(rows, key, parentKey);
    expect(ids(out.rows)).toEqual(["p", "s1", "s2", "q", "z"]);
    expect(nestedIds(out.rows, out.nested)).toEqual(["s1", "s2"]);
    // the group is as recent as s1, its most recent member
    expect(key(out.lead.get(rows[3])!)).toBe("s1");
    expect(key(out.lead.get(rows[2])!)).toBe("s1");
    expect(key(out.lead.get(rows[1])!)).toBe("q");
  });

  it("nests a side session of a side session under the topmost loaded one, one level in", () => {
    const rows = [r("p"), r("s", "p"), r("g", "s")];
    const out = nestUnderParents(rows, key, parentKey);
    expect(ids(out.rows)).toEqual(["p", "s", "g"]);
    expect(nestedIds(out.rows, out.nested)).toEqual(["s", "g"]);
  });

  it("nests under the nearest loaded ancestor when the one between is not loaded", () => {
    const rows = [r("g", "s"), r("p")]; // s (g's parent) is on another page
    const out = nestUnderParents(rows, key, parentKey);
    expect(ids(out.rows)).toEqual(["g", "p"]);
    expect(out.nested.size).toBe(0);
  });

  it("links nothing in a loop, and keeps every row once", () => {
    const rows = [r("a", "b"), r("b", "a"), r("c", "a"), r("d", "d")];
    const out = nestUnderParents(rows, key, parentKey);
    expect(ids(out.rows)).toEqual(["a", "b", "c", "d"]);
    expect(out.nested.size).toBe(0);
  });

  it("neither adds nor drops a row", () => {
    const rows = [r("s1", "p"), r("x", "y"), r("p"), r("y"), r("s2", "p"), r("w", "x")];
    const out = nestUnderParents(rows, key, parentKey);
    expect([...ids(out.rows)].sort()).toEqual([...ids(rows)].sort());
    expect(ids(out.rows)).toEqual(["p", "s1", "s2", "y", "x", "w"]);
  });

  it("sorts top-level rows without separating side sessions from their parent (#11801)", () => {
    const out = nestUnderParents([r("s1", "p"), r("q"), r("s2", "p"), r("p"), r("z")], key, parentKey);
    const ordered = orderNestedRows(out, (a, b) => b.id.localeCompare(a.id));
    expect(ids(ordered)).toEqual(["z", "q", "p", "s2", "s1"]);
    expect(key(out.lead.get(out.rows[0])!)).toBe("s1"); // activity grouping still uses the newest member
  });

  it("keeps side sessions in presentation order when their transport order reverses (#11801)", () => {
    const compare = (a: Row, b: Row) => a.id.localeCompare(b.id);
    const first = nestUnderParents([r("s1", "p"), r("p"), r("s2", "p")], key, parentKey);
    const reversed = nestUnderParents([r("s2", "p"), r("p"), r("s1", "p")], key, parentKey);
    expect(ids(orderNestedRows(first, compare))).toEqual(["p", "s1", "s2"]);
    expect(ids(orderNestedRows(reversed, compare))).toEqual(["p", "s1", "s2"]);
  });
});

describe("mergeStableRows", () => {
  const byId = { compare: (a: Row, b: Row) => a.id.localeCompare(b.id) };
  const byPlace = { compare: (a: Row, b: Row) => a.place! - b.place! || a.id.localeCompare(b.id) };
  const candidates = (rows: readonly Row[]) => new Map(rows.map((row) => [key(row), row]));

  it("retains displaced rows, removes departed candidates and admits full-read identities", () => {
    const initial = [r("b"), r("a")];
    const presented = mergeStableRows([], initial, candidates(initial), key, byId);
    expect(ids(presented)).toEqual(["a", "b"]);

    const withNew = [r("c"), ...initial];
    const retained = mergeStableRows(presented, [r("c"), r("b")], candidates(withNew), key, byId);
    expect(ids(retained)).toEqual(["a", "b", "c"]);

    const withoutA = withNew.filter((row) => row.id !== "a");
    const removed = mergeStableRows(retained, [r("c"), r("b")], candidates(withoutA), key, byId);
    expect(ids(removed)).toEqual(["b", "c"]);
  });

  it("keeps a displaced b between a and c by its own placement when a's activity changes", () => {
    const initial = [r("a", undefined, 0), r("b", undefined, 1), r("c", undefined, 2)];
    const presented = mergeStableRows([], initial, candidates(initial), key, byPlace);
    const activeA = { ...initial[0]!, activity: 100 };
    const current = [activeA, initial[1]!, initial[2]!];
    // b is absent from the newest-first transport prefix, not removed from the candidates.
    const after = mergeStableRows(presented, [activeA, initial[2]!], candidates(current), key, byPlace, undefined, new Set());
    expect(ids(after)).toEqual(["a", "b", "c"]);
    expect(after[0]).toBe(activeA);
  });

  it("places a created first tab without moving a displaced retained row away from its placement", () => {
    const initial = [r("a", undefined, 0), r("b", undefined, 1), r("c", undefined, 2)];
    const presented = mergeStableRows([], initial, candidates(initial), key, byPlace);
    const first = r("first", undefined, -1);
    const activeA = { ...initial[0]!, activity: 100 };
    const current = [first, activeA, initial[1]!, initial[2]!];
    const after = mergeStableRows(presented, [first, activeA, initial[2]!], candidates(current), key, byPlace, undefined, new Set(["first"]));
    expect(ids(after)).toEqual(["first", "a", "b", "c"]);
    expect(ids(mergeStableRows([], current, candidates(current), key, byPlace))).toEqual(ids(after));
  });

  it("admits an older newly active row on a full read but not on a delta", () => {
    const initial = [r("a", undefined, 1), r("b", undefined, 2)];
    const presented = mergeStableRows([], initial, candidates(initial), key, byPlace);
    const older = { ...r("older", undefined, 3), activity: 100 };
    const current = [...initial, older];
    const transport = [older, initial[0]!];
    expect(ids(mergeStableRows(presented, transport, candidates(current), key, byPlace, undefined, new Set()))).toEqual(["a", "b"]);
    expect(ids(mergeStableRows(presented, transport, candidates(current), key, byPlace))).toEqual(["a", "b", "older"]);
  });

  it("applies swapped structural placements and matches a fresh reload", () => {
    const initial = [r("a", undefined, 0), r("b", undefined, 1)];
    const presented = mergeStableRows([], initial, candidates(initial), key, byPlace);
    const swapped = [r("a", undefined, 1), r("b", undefined, 0)];
    const after = mergeStableRows(presented, swapped, candidates(swapped), key, byPlace, undefined, new Set());
    expect(ids(after)).toEqual(["b", "a"]);
    expect(ids(mergeStableRows([], swapped, candidates(swapped), key, byPlace))).toEqual(ids(after));
  });

  it("sorts every newly read page into the union without using previous positions", () => {
    const initial = [r("a", undefined, 10), r("b", undefined, 20)];
    const presented = mergeStableRows([], initial, candidates(initial), key, byPlace);
    const secondPage = [...initial, r("older", undefined, 0)];
    expect(ids(mergeStableRows(presented, secondPage, candidates(secondPage), key, byPlace))).toEqual(["older", "a", "b"]);
  });

  it("sorts the union without separating side sessions from their parent", () => {
    const rows = [r("s2", "p"), r("q"), r("s1", "p"), r("p"), r("z")];
    const ordered = mergeStableRows([], rows, candidates(rows), key, byId, parentKey);
    expect(ids(ordered)).toEqual(["p", "s1", "s2", "q", "z"]);
  });

  it("keeps the newest side member as activity lead after structural ordering", () => {
    const parent = { ...r("p", undefined, 1), activity: 100 };
    const side = { ...r("s", "p", 2), activity: 200 };
    const transport = [side, parent];
    const presented = mergeStableRows([], transport, candidates(transport), key, byPlace, parentKey);
    expect(presented).toEqual([parent, side]);
    const after = nestUnderParents(presented, key, parentKey, (row) => row.activity ?? 0);
    expect(after.lead.get(parent)).toBe(side);
  });
});
