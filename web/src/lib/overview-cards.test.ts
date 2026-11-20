// Overview as the user arranged it (#9745, #10905, lib/overview-cards.ts): the hub's cards in the user's order, the ones
// the arrangement does not name after it, entries for cards that are not there passed over; Overview's own sections
// among them, where they are by default unless named; the edits (move, drop, hide, widen) and what an arrangement
// writes (lib/overview-edit.ts).
import { describe, expect, it } from "vitest";
import { arrangeOverview, entryKey, halvesPaired, keepSections, overviewRuns, readOverview, SECTIONS, type ArrangedCard, type OverviewEntry } from "./overview-cards";
import { changeEntry, dropEntry, entriesOf, moveEntry, OVERVIEW_MOST, written } from "./overview-edit";

const card = (extension: string, id: string) => ({ extension, id, title: id });
// the hub's order: by extension, then the manifest's
const HUB = [card("example-gateway", "today"), card("example-quota", "left"), card("example-today", "needs"), card("example-today", "agenda")];
const keys = (list: { key: string }[]) => list.map((c) => c.key);
// #9745's cards alone, in the arrangement's order
const arrangeCards = <T extends { extension: string; id: string }>(cards: readonly T[], prefs: readonly OverviewEntry[]) =>
  arrangeOverview(cards, prefs).filter((i): i is ArrangedCard<T> => "card" in i);
const id = (e: OverviewEntry) => ("id" in e ? e.id : entryKey(e));
// Overview's own sections as they are by default, above the cards (the cards last, #11147)
const DEFAULT_SECTIONS = ["section:needs_you", "section:waiting_on_computer", "section:in_progress", "section:working", "section:unread", "section:computers"];
const section = (s: string, hidden?: true): OverviewEntry => ({ section: s, ...(hidden ? { hidden } : {}) });

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
    expect(moveEntry(list, "example-today/needs", -1).map(id)).toEqual(["left", "needs", "today", "agenda"]);
    expect(moveEntry(list, "example-today/needs", 1).map(id)).toEqual(["left", "today", "agenda", "needs"]);
    expect(moveEntry(list, "example-quota/left", -1)).toEqual(list);
    expect(moveEntry(list, "example-today/agenda", 1)).toEqual(list);
  });

  it("a card dropped on another takes its place", () => {
    expect(dropEntry(list, "example-today/agenda", "example-quota/left").map(id)).toEqual(["agenda", "left", "today", "needs"]);
    expect(dropEntry(list, "example-quota/left", "example-today/needs").map(id)).toEqual(["today", "needs", "left", "agenda"]);
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

describe("Overview's own sections (#10905)", () => {
  const CARD_KEYS = ["example-gateway/today", "example-quota/left", "example-today/needs", "example-today/agenda"];
  // In progress first, then the other sections in their order, then the cards: the user's target arrangement
  const IN_PROGRESS_FIRST: OverviewEntry[] = [
    section("in_progress"),
    ...SECTIONS.filter((s) => s !== "in_progress").map((s) => section(s)),
    ...HUB.map((c) => ({ extension: c.extension, id: c.id })),
  ];

  it("without an arrangement: the sections in their order, then the cards (#11147), all shown", () => {
    const out = arrangeOverview(HUB, []);
    expect(keys(out)).toEqual([...DEFAULT_SECTIONS, ...CARD_KEYS]);
    expect(out.every((i) => !i.hidden)).toBe(true);
    expect(SECTIONS).toEqual(["needs_you", "waiting_on_computer", "in_progress", "working", "unread", "computers"]);
  });

  it("an arrangement of only cards (#9745's) leaves the sections where they are by default, above the cards", () => {
    const out = arrangeOverview(HUB, [{ extension: "example-today", id: "agenda" }, { extension: "example-quota", id: "left", hidden: true }]);
    expect(keys(out)).toEqual([...DEFAULT_SECTIONS, "example-today/agenda", "example-quota/left", "example-gateway/today", "example-today/needs"]);
  });

  it("a section the arrangement moves comes in its place: In progress first, above Needs you", () => {
    const out = arrangeOverview(HUB, IN_PROGRESS_FIRST);
    expect(keys(out)).toEqual(["section:in_progress", ...DEFAULT_SECTIONS.filter((k) => k !== "section:in_progress"), ...CARD_KEYS]);
  });

  it("a section it does not name (one added since) comes where it is by default: right after the one before it", () => {
    const without = (x: string) => IN_PROGRESS_FIRST.filter((e) => !("section" in e && e.section === x));
    // Working (after In progress by default) follows In progress, which stays first
    expect(keys(arrangeOverview(HUB, without("working")))).toEqual([
      "section:in_progress", "section:working", "section:needs_you", "section:waiting_on_computer", "section:unread", "section:computers", ...CARD_KEYS,
    ]);
    // Computers after Unread, still above the cards
    const noComputers = keys(arrangeOverview(HUB, without("computers")));
    expect(noComputers.slice(noComputers.indexOf("section:unread"), noComputers.indexOf("section:unread") + 3)).toEqual(["section:unread", "section:computers", CARD_KEYS[0]]);
    // the first of them by default comes first
    expect(keys(arrangeOverview(HUB, without("needs_you"))).slice(0, 2)).toEqual(["section:needs_you", "section:in_progress"]);
  });

  it("cards it does not name come after the last card it names; with none named, at the end, where they are by default", () => {
    const some = [section("in_progress"), { extension: "example-quota", id: "left" }, ...SECTIONS.filter((s) => s !== "in_progress").map((s) => section(s))];
    expect(keys(arrangeOverview(HUB, some)).slice(0, 6)).toEqual(["section:in_progress", "example-quota/left", "example-gateway/today", "example-today/needs", "example-today/agenda", "section:needs_you"]);
    // arranged while there was no card: a card installed since comes at the end, not in In progress's place at the top
    const noCards = IN_PROGRESS_FIRST.filter((e) => "section" in e);
    expect(keys(arrangeOverview(HUB, noCards))).toEqual(keys(arrangeOverview(HUB, IN_PROGRESS_FIRST)));
  });

  it("Needs you and Stuck are never hidden, whatever the list says; the others can be", () => {
    const out = arrangeOverview(HUB, [section("needs_you", true), section("waiting_on_computer", true), section("working", true), section("computers", true)]);
    const hidden = Object.fromEntries(out.filter((i) => "section" in i).map((i) => [i.key, i.hidden]));
    expect(hidden).toMatchObject({ "section:needs_you": false, "section:waiting_on_computer": false, "section:working": true, "section:computers": true, "section:in_progress": false });
  });

  it("a section this web does not have (a newer web's) is passed over, and one named twice once", () => {
    const out = arrangeOverview(HUB, [section("later_section"), section("unread"), section("unread", true)]);
    expect(keys(out)).not.toContain("section:later_section");
    expect(keys(out).filter((k) => k === "section:unread")).toHaveLength(1);
    expect(out.find((i) => i.key === "section:unread")?.hidden).toBe(false);
  });

  it("readOverview keeps the entries that name a section, with or without hidden", () => {
    expect(readOverview([section("in_progress"), { section: 3 }, section("working", true), { extension: "example-quota", id: "left" }])).toEqual([
      section("in_progress"), section("working", true), { extension: "example-quota", id: "left" },
    ]);
  });

  it("runs: the cards that come one after another are one row, named by their place among the rows", () => {
    const rest = ["waiting_on_computer", "working", "unread", "computers"].map((s) => section(s));
    const runs = overviewRuns(arrangeOverview(HUB, [section("in_progress"), { extension: "example-quota", id: "left" }, section("needs_you"), { extension: "example-today", id: "agenda" }, ...rest]));
    expect(runs.map((r) => ("cards" in r ? [r.key, keys(r.cards)] : r.key)).slice(0, 4)).toEqual([
      "section:in_progress",
      ["cards:0", ["example-quota/left"]],
      "section:needs_you",
      ["cards:1", ["example-today/agenda", "example-gateway/today", "example-today/needs"]],
    ]);
    // the default's one row keeps its name when a section moves: its cards are not drawn again
    const row = (prefs: OverviewEntry[]) => overviewRuns(arrangeOverview(HUB, prefs)).find((r) => "cards" in r)?.key;
    expect(row(IN_PROGRESS_FIRST)).toBe("cards:0");
    expect(row([])).toBe("cards:0");
  });

  describe("what a change writes", () => {
    const items = arrangeOverview(HUB, []);

    it("only the cards while no section was ever placed and the sections are where they are by default", () => {
      const list = moveEntry(entriesOf(items), "example-quota/left", -1);
      expect(written(items, list, [])).toEqual([{ extension: "example-quota", id: "left" }, { extension: "example-gateway", id: "today" }, { extension: "example-today", id: "needs" }, { extension: "example-today", id: "agenda" }]);
    });

    it("every section once one is moved or hidden, and from then on, back in its place too (until Reset)", () => {
      const moved = written(items, dropEntry(entriesOf(items), "section:in_progress", "section:needs_you"), []);
      expect(moved).toEqual(IN_PROGRESS_FIRST);
      const hidden = written(items, changeEntry(entriesOf(items), "section:computers", { hidden: true }), []);
      expect(hidden.find((e) => entryKey(e) === "section:computers")).toEqual(section("computers", true));
      expect(hidden.filter((e) => "section" in e)).toHaveLength(SECTIONS.length);
      // put back where it was by default: the list still names them, or a hub keeping the sections of a list naming
      // none (for #9745's web) would keep the old place
      const now = arrangeOverview(HUB, moved);
      const back = written(now, dropEntry(entriesOf(now), "section:in_progress", "section:waiting_on_computer"), moved);
      expect(back.filter((e) => "section" in e).map(entryKey)).toEqual(DEFAULT_SECTIONS);
    });

    it("a section this web does not have stays after the entry it followed, or first", () => {
      const cards = HUB.map((c) => ({ extension: c.extension, id: c.id }));
      const rest = SECTIONS.filter((s) => s !== "in_progress").map((s) => section(s));
      const prefs = [section("later_first"), section("in_progress"), ...cards.slice(0, 2), section("later_section"), ...cards.slice(2), ...rest];
      const now = arrangeOverview(HUB, prefs);
      const out = written(now, moveEntry(entriesOf(now), "example-today/needs", -1), prefs);
      // Needs me moved before Quota left: the newer web's section stays after Quota left, the entry it followed
      expect(out.map(entryKey).slice(0, 7)).toEqual(["section:later_first", "section:in_progress", "example-gateway/today", "example-today/needs", "example-quota/left", "section:later_section", "example-today/agenda"]);
    });

    it("hides and shows a section, moves it, and drops it on a card, writing only what differs from the default", () => {
      const list = entriesOf(items);
      expect(list.slice(0, 6)).toEqual(SECTIONS.map((s) => section(s)));
      expect(changeEntry(list, "section:working", { hidden: true, span: "full" }).find((e) => entryKey(e) === "section:working")).toEqual(section("working", true));
      expect(changeEntry(changeEntry(list, "section:working", { hidden: true }), "section:working", { hidden: false }).find((e) => entryKey(e) === "section:working")).toEqual(section("working"));
      expect(moveEntry(list, "section:computers", 1).map(entryKey).slice(5, 7)).toEqual(["example-gateway/today", "section:computers"]);
      expect(dropEntry(list, "section:in_progress", "example-gateway/today").map(entryKey).slice(5, 7)).toEqual(["example-gateway/today", "section:in_progress"]);
    });
  });
});

// The web's copy of the default manager's rule, for the cards a manager of #9745 sends while the sections are this
// browser's alone: server/internal/assistant/navigation_test.go TestKeepSections's cases, each the same.
describe("keepSections", () => {
  const c = (i: string): OverviewEntry => ({ extension: "example", id: i });
  const show = (l: readonly OverviewEntry[]) => l.map((e) => ("section" in e ? e.section : e.id)).join(" ");
  it("lays a list of only cards over the stored sections as the manager does", () => {
    const cases: [OverviewEntry[], OverviewEntry[], string][] = [
      [[], [c("a")], "a"],
      [[c("a"), c("b")], [c("b"), c("a")], "b a"],
      [[section("s"), c("a"), section("t"), c("b")], [c("b"), c("a"), c("c")], "s b t a c"],
      [[section("s"), section("t")], [c("a")], "s t a"],
      [[c("a"), section("s"), c("b")], [c("c")], "c s"],
      [[section("s")], [], ""],
      [[section("s"), c("a")], [section("t"), c("a")], "t a"],
    ];
    for (const [stored, list, want] of cases) expect(show(keepSections(stored, list))).toBe(want);
  });
});
