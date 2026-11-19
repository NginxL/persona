// The shared graph layout cases (spec/fixtures/graph-layout/README.md, written by scripts/gen/graph_layout.py):
// every field of every case, at a 1e-9 tolerance, so the web's copy of the rules (lib/graph-layout.ts) agrees with
// the reference to the bit. What draws a laid-out graph is views/extensions/ViewBody10.tsx.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { layOutGraph, type LayoutGraph } from "./graph-layout";

interface Case {
  name: string;
  graph: LayoutGraph;
  expected: {
    at: Record<string, [number, number]>;
    size: [number, number];
    edges: [string, string][];
    order: string[];
    depth: Record<string, number>;
    folds: Record<string, number>;
    frames: { group: string; members: string[] }[];
  };
}

const doc = JSON.parse(readFileSync(new URL("../../../spec/fixtures/graph-layout/cases.json", import.meta.url), "utf8")) as { version: number; cases: Case[] };

const near = (a: number, b: number): boolean => Math.abs(a - b) <= 1e-9;

/** Field-by-field comparison of a whole layout (the same checks the shared cases get). */
const expectLayout = (got: ReturnType<typeof layOutGraph>, want: Case["expected"]) => {
  expect(Object.keys(got.at).sort()).toEqual(Object.keys(want.at).sort());
  for (const id of Object.keys(want.at)) {
    expect(near(got.at[id][0], want.at[id][0]), `${id} x`).toBe(true);
    expect(near(got.at[id][1], want.at[id][1]), `${id} y`).toBe(true);
  }
  expect(got.size.map((v, i) => near(v, want.size[i]))).toEqual([true, true]);
  expect(got.edges).toEqual(want.edges);
  expect(got.order).toEqual(want.order);
  expect(got.depth).toEqual(want.depth);
  expect(got.folds).toEqual(want.folds);
  expect(got.frames).toEqual(want.frames);
};

describe("the shared graph layout cases", () => {
  it("is the cases file this test is written against", () => {
    expect(doc.version).toBe(1);
    expect(doc.cases.length).toBeGreaterThanOrEqual(19);
  });

  for (const c of doc.cases) {
    it(`lays out "${c.name}"`, () => {
      expectLayout(layOutGraph(c.graph), c.expected);
    });
  }

  // The shared cases alone let these mutations of the layered rules survive; each case below is checked against the
  // reference (scripts/gen/graph_layout.py) and kills its mutation (the expected values are the reference's output,
  // verified with it, not derived from this copy).
  it("layered: the up sweep settles a middle layer by the mean place of its children", () => {
    // p1's child n follows m (its other child's place) one layer down, and the last layer follows: without the up
    // sweep n and m keep their node order ([n 0, m 1] instead of [m 0, n 1]) and both edges into them cross.
    expectLayout(layOutGraph({
      nodes: [{ id: "p1" }, { id: "p2" }, { id: "n" }, { id: "m" }, { id: "u" }, { id: "v" }],
      edges: [
        { from: "p1", to: "n" }, { from: "p2", to: "n" }, { from: "p2", to: "m" },
        { from: "m", to: "u" }, { from: "p1", to: "u" }, { from: "n", to: "v" }, { from: "p2", to: "v" },
      ],
      layout: "layered",
    }), {
      at: { p1: [0, 0], p2: [0, 1], n: [1, 1], m: [1, 0], u: [2, 0], v: [2, 1] },
      size: [3, 2],
      edges: [["p1", "n"], ["p2", "n"], ["p2", "m"], ["m", "u"], ["p1", "u"], ["n", "v"], ["p2", "v"]],
      order: ["p1", "n", "v", "u", "p2", "m"],
      depth: { p1: 0, n: 1, v: 2, u: 1, p2: 0, m: 1 },
      folds: { p1: 0, p2: 0, n: 0, m: 0 },
      frames: [],
    });
  });

  it("layered: a group is its title among nodes with the same parents — same title, different parents, two frames", () => {
    // a and b share a group title but have different parents: they are not one frame, and the quarter-slot group gap
    // sets them and their parents apart. A group key that ignored the parents would pull them together into one frame.
    expectLayout(layOutGraph({
      nodes: [{ id: "s1" }, { id: "s2" }, { id: "a", group: "ways" }, { id: "b", group: "ways" }],
      edges: [{ from: "s1", to: "a" }, { from: "s2", to: "b" }],
      layout: "layered",
    }), {
      at: { s1: [0, 0.125], s2: [0, 1.125], a: [1, 0], b: [1, 1.25] },
      size: [2, 2.25],
      edges: [["s1", "a"], ["s2", "b"]],
      order: ["s1", "a", "s2", "b"],
      depth: { s1: 0, a: 1, s2: 0, b: 1 },
      folds: { s1: 0, s2: 0 },
      frames: [{ group: "ways", members: ["a"] }, { group: "ways", members: ["b"] }],
    });
  });

  it("layered: the same group under the same parents in any edge order is one frame", () => {
    // a and b have the same two parents, each named in a different edge order: they compare equal whatever their
    // parents' order (the reference sorts the parents before comparing). A key built from the parents' edge order
    // alone would keep them apart; one that dropped the parents would still join them, so the case above carries that.
    expectLayout(layOutGraph({
      nodes: [{ id: "s1" }, { id: "s2" }, { id: "a", group: "ways" }, { id: "b", group: "ways" }],
      edges: [{ from: "s1", to: "a" }, { from: "s2", to: "a" }, { from: "s2", to: "b" }, { from: "s1", to: "b" }],
      layout: "layered",
    }), {
      at: { s1: [0, 0], s2: [0, 1], a: [1, 0], b: [1, 1] },
      size: [2, 2],
      edges: [["s1", "a"], ["s2", "a"], ["s2", "b"], ["s1", "b"]],
      order: ["s1", "a", "b", "s2"],
      depth: { s1: 0, a: 1, b: 1, s2: 0 },
      folds: { s1: 0, s2: 0 },
      frames: [{ group: "ways", members: ["a", "b"] }],
    });
  });
});
