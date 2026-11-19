// UI version 10 as the web reads it (docs/extensions/extensions.md "UI version 10", #9727): a view's `graph`
// component — nodes, edges, folds and the node drawn as selected — from the schema's examples; what it cannot use is
// left out, and the component is then drawn as a graph without it. The layout itself is lib/graph-layout.test.ts.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { readView, UI_VERSION, type UIGraphNode } from "./extension-ui";

const example = (name: string) => JSON.parse(readFileSync(new URL(`../../../schema/extension/examples/View.v10-${name}.json`, import.meta.url), "utf8"));
const graphOf = (raw: unknown) => readView({ sections: [{ items: [raw] }] }).sections[0]?.items[0] as
  { type: string; nodes?: UIGraphNode[]; edges?: { from: string; to: string }[]; folded?: string[]; layout?: string; direction?: string; selected?: string; key?: string };

describe("UI 10: reading a view's graph", () => {
  it("is the version the web sends", () => {
    expect(UI_VERSION).toBe(10);
  });

  it("keeps every node, edge, fold and selection of the examples", () => {
    const work = graphOf(example("work-tree").sections[0].items[1]);
    expect(work.type).toBe("graph");
    expect(work.key).toBe("work");
    expect(work.nodes).toHaveLength(12);
    expect(work.nodes![2]).toMatchObject({ id: "build", title: "Build", tone: "positive", badge: { text: "Done", tone: "positive" }, action: { id: "open", values: { id: "build" } } });
    expect(work.nodes![3]).toMatchObject({ id: "build.small", title: "Smallest change", group: "Two ways", color: "green" });
    expect(work.nodes![5]).toMatchObject({ id: "tests", subtitle: "2 of 3 suites", progress: 0.66, icon: "check" });
    expect(work.edges).toHaveLength(11);
    expect(work.folded).toEqual(["docs"]);
    expect(work.selected).toBe("tests");
    expect(work.layout).toBeUndefined(); // tree is the default: not carried
    const plan = graphOf(example("plan-layered").sections[0].items[0]);
    expect(plan.layout).toBe("layered");
    expect(plan.direction).toBe("down");
    expect(plan.nodes).toHaveLength(6);
  });

  it("leaves out what does not read: nodes without a title, edges naming an unknown node or itself, repeats", () => {
    const g = graphOf({ type: "graph", nodes: [
      { id: "a", title: "A" }, { id: "b", title: "B" }, { id: "c" }, { title: "no id" }, "junk",
      { id: "a", title: "A again" },
    ], edges: [
      { from: "a", to: "b" }, { from: "a", to: "b" }, { from: "a", to: "a" }, { from: "a", to: "nope" }, { from: "nope", to: "b" }, 7,
    ], selected: "nope", folded: ["nope", "a", "b"], direction: "sideways", layout: "circle" });
    expect(g.nodes?.map((n) => n.id)).toEqual(["a", "b"]);
    expect(g.edges).toEqual([{ from: "a", to: "b" }]);
    expect(g.selected).toBeUndefined();
    expect(g.folded).toEqual(["a", "b"]); // an id that is gone is dropped, the person's folds are kept
    expect(g.direction).toBeUndefined();
    expect(g.layout).toBeUndefined();
  });

  it("keeps a node's fields only as they read, and an unknown tone is no tone", () => {
    const g = graphOf({ type: "graph", nodes: [
      { id: "a", title: "A", tone: "positive" },
      { id: "b", title: "B", color: "teal", tone: "danger", progress: 1.5, icon: 3, badge: { text: "" }, subtitle: 9 },
      { id: "c", title: "C", tone: "sparkly", color: "not-a-color", progress: 0.4, group: "" },
    ] });
    expect(g.nodes).toEqual([
      { id: "a", title: "A", tone: "positive" },
      { id: "b", title: "B", color: "teal", tone: "danger", progress: 1.5 },
      { id: "c", title: "C", progress: 0.4 },
    ]);
  });

  it("a graph with no node that reads is left out of the view", () => {
    expect(readView({ sections: [{ items: [{ type: "graph", nodes: [] }, { type: "text", text: "hi" }] }] }).sections[0].items.map((i) => i.type)).toEqual(["text"]);
    expect(readView({ sections: [{ items: [{ type: "graph", nodes: [{ id: "a", title: "A" }] }] }] }).sections[0].items[0].nodes).toHaveLength(1);
  });
});
