import { describe, expect, it } from "vitest";
import { countsLabel, directionLabel, directionNames, leaderCounts, totalCounts } from "./direction-names";

describe("direction names", () => {
  it("keep the current leaders' names by id, leaving out ended ones and blank names", () => {
    const names = directionNames([
      { id: "a", name: "Product" },
      { id: "b", name: "  " },
      { id: "c", name: "Old", ended: true },
      { id: "", name: "Nobody" },
    ]);
    expect([...names]).toEqual([["a", "Product"]]);
    expect(directionNames(undefined).size).toBe(0);
  });

  it("label a row with its name, else its title", () => {
    const names = new Map([["a", "Product"]]);
    expect(directionLabel(names, "a", "1110 | explore | x")).toBe("Product");
    expect(directionLabel(names, "b", "Trip plan")).toBe("Trip plan");
  });
});

describe("what a lead's row shows of its direction (#4331, #6626)", () => {
  const tt = (key: string, o: { count: number }) => `${key === "sidebar.leader_needs_you" ? "等你" : key} ${o.count}`;
  it("is read for current leaders only", () => {
    const c = leaderCounts([{ id: "a", counts: { running: true, needs_you: 1 } }, { id: "b", ended: true, counts: { running: true, needs_you: 0 } }, { id: "c" }]);
    expect([...c.entries()]).toEqual([["a", { running: true, needs_you: 1 }]]);
  });
  it("says only what waits on you: what runs is the row's arc, never words as well", () => {
    expect(countsLabel({ needs_you: 3 }, tt)).toBe("等你 3");
    expect(countsLabel({ needs_you: 0 }, tt)).toBe("");
    expect(countsLabel(undefined, tt)).toBe("");
  });
  it("adds up for the folded section: running when any lead runs", () => {
    const c = new Map([["a", { running: false, needs_you: 1 }], ["b", { running: true, needs_you: 2 }]]);
    expect(totalCounts(c, ["a", "b", "gone"])).toEqual({ running: true, needs_you: 3 });
    expect(totalCounts(c, ["a"])).toEqual({ running: false, needs_you: 1 });
  });
});
