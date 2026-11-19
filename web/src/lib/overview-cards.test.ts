// Overview's extension cards as the user arranged them (#9745, lib/overview-cards.ts): the hub's cards in the user's
// order, the ones the arrangement does not name after it, entries for cards that are not there passed over; the edits
// (move, drop, hide, widen) and what an arrangement writes (lib/overview-edit.ts).
import { describe, expect, it } from "vitest";
import { arrangeCards, halvesPaired, readOverview } from "./overview-cards";
import { changeEntry, dropEntry, entriesOf, moveEntry, OVERVIEW_MOST } from "./overview-edit";

const card = (extension: string, id: string) => ({ extension, id, title: id });
// the hub's order: by extension, then the manifest's
const HUB = [card("example-gateway", "today"), card("example-quota", "left"), card("example-today", "needs"), card("example-today", "agenda")];
const keys = (list: { key: string }[]) => list.map((c) => c.key);

describe("arrangeCards", () => {
  it("without an arrangement: the hub's cards in its order, shown, half a row", () => {
    const out = arrangeCards(HUB, []);
    expect(keys(out)).toEqual(["example-gateway/today", "example-quota/left", "example-today/needs", "example-today/agenda"]);
    expect(out.every((c) => !c.hidden && c.span === "half")).toBe(true);
    expect(out[0].card).toBe(HUB[0]);
  });

  it("the named cards in their place, hidden and wide as chosen; the others after them in the hub's order", () => {
    const out = arrangeCards(HUB, [
      { extension: "example-today", id: "agenda", span: "full" },
      { extension: "example-quota", id: "left", hidden: true },
    ]);
    expect(keys(out)).toEqual(["example-today/agenda", "example-quota/left", "example-gateway/today", "example-today/needs"]);
    expect(out.map((c) => [c.hidden, c.span])).toEqual([[false, "full"], [true, "half"], [false, "half"], [false, "half"]]);
  });

  it("passes over a card that is not there now, and a card named twice", () => {
    const out = arrangeCards(HUB, [
      { extension: "example-removed", id: "card", span: "full" },
      { extension: "example-today", id: "needs", hidden: true },
      { extension: "example-today", id: "needs", span: "full" },
    ]);
    expect(keys(out)).toEqual(["example-today/needs", "example-gateway/today", "example-quota/left", "example-today/agenda"]);
    expect(out[0]).toMatchObject({ hidden: true, span: "half" });
  });
});

describe("readOverview", () => {
  it("keeps the entries that name a card; anything else is none", () => {
    expect(readOverview(null)).toEqual([]);
    expect(readOverview({ extension: "a", id: "b" })).toEqual([]);
    expect(readOverview([{ extension: "example-quota", id: "left", hidden: true, span: "full" }, { extension: "example-gateway" }, "example-today/needs", null, 1])).toEqual([
      { extension: "example-quota", id: "left", hidden: true, span: "full" },
    ]);
  });

  it("an entry of another shape draws as the default", () => {
    const out = arrangeCards(HUB, readOverview([{ extension: "example-gateway", id: "today", hidden: "yes", span: "wide" }]));
    expect(out[0]).toMatchObject({ key: "example-gateway/today", hidden: false, span: "half" });
  });
});

describe("the edits", () => {
  const list = entriesOf(arrangeCards(HUB, [{ extension: "example-quota", id: "left", hidden: true }, { extension: "example-removed", id: "card" }]));

  it("an arrangement writes every card there is now, in its place, and only what differs from the default, at most OVERVIEW_MOST", () => {
    const many = Array.from({ length: OVERVIEW_MOST + 5 }, (_, i) => card(`example-${i}`, "card"));
    expect(entriesOf(arrangeCards(many, []))).toHaveLength(OVERVIEW_MOST);
    expect(list).toEqual([
      { extension: "example-quota", id: "left", hidden: true },
      { extension: "example-gateway", id: "today" },
      { extension: "example-today", id: "needs" },
      { extension: "example-today", id: "agenda" },
    ]);
  });

  it("moves a card one place earlier or later, and not past either end", () => {
    expect(moveEntry(list, "example-today/needs", -1).map((e) => e.id)).toEqual(["left", "needs", "today", "agenda"]);
    expect(moveEntry(list, "example-today/needs", 1).map((e) => e.id)).toEqual(["left", "today", "agenda", "needs"]);
    expect(moveEntry(list, "example-quota/left", -1)).toEqual(list);
    expect(moveEntry(list, "example-today/agenda", 1)).toEqual(list);
  });

  it("a card dropped on another takes its place", () => {
    expect(dropEntry(list, "example-today/agenda", "example-quota/left").map((e) => e.id)).toEqual(["agenda", "left", "today", "needs"]);
    expect(dropEntry(list, "example-quota/left", "example-today/needs").map((e) => e.id)).toEqual(["today", "needs", "left", "agenda"]);
    expect(dropEntry(list, "example-quota/left", "example-quota/left")).toEqual(list);
  });

  it("hides, shows and widens a card, writing only what differs from the default", () => {
    const wide = changeEntry(list, "example-gateway/today", { span: "full" });
    expect(wide[1]).toEqual({ extension: "example-gateway", id: "today", span: "full" });
    const shown = changeEntry(wide, "example-quota/left", { hidden: false });
    expect(shown[0]).toEqual({ extension: "example-quota", id: "left" });
    expect(changeEntry(shown, "example-gateway/today", { hidden: true, span: "half" })[1]).toEqual({ extension: "example-gateway", id: "today", hidden: true });
  });
});

describe("halvesPaired", () => {
  it("pairs half-row cards only when two or more of them are drawn (a lone one: web audit item 76)", () => {
    const half = { hidden: false, span: "half" as const };
    expect(halvesPaired([half, half])).toBe(true);
    expect(halvesPaired([half, { hidden: true, span: "half" }])).toBe(false);
    expect(halvesPaired([half, { hidden: true, span: "half" }], true)).toBe(true); // arranging: the hidden one is drawn
    expect(halvesPaired([{ hidden: false, span: "full" }, half])).toBe(false);
    expect(halvesPaired([{ hidden: false, span: "full" }, half, half])).toBe(true);
  });
});
