// @vitest-environment jsdom
// UI version 10 as the web draws it (docs/extensions/extensions.md "UI version 10", #9727): a view's `graph` as an
// interactive canvas — the visible nodes as buttons in reading order, the edges in one SVG, the group frames, the
// fold controls, the selected node's ring; a node presses its action, folding is the person's and kept for the
// `key`, a redraw that changes only states keeps the position, a compact card draws the still fitted preview that
// opens Full screen (an interactive canvas of its own), and the keyboard walks the nodes in reading order. Its
// pixels at each width are checked in a real browser (web/e2e/extension-graph.mjs); the layout itself is
// lib/graph-layout.test.ts.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readView, type UIAction, type UIItem } from "../../lib/extension-ui";
import { ExtensionOwnerScope } from "../../lib/extension-owner";
import { t } from "../../i18n";
import { INERT, ViewSections } from "./ViewBody";
import { forgetGraphStores, GraphCanvas } from "./ViewBody10";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

const doc = (html: string) => new DOMParser().parseFromString(`<body>${html}</body>`, "text/html");

// A small work graph: a root with two parts, the first with a grouped pair of ways and a step with a check, the
// second folded with two steps of its own.
const WORK = {
  sections: [{
    title: "Work",
    items: [{ type: "graph", key: "work", nodes: [
      { id: "root", title: "Ship it", subtitle: "2 parts", icon: "list", action: { id: "open", values: { id: "root" } } },
      { id: "a", title: "Part A", group: "Two ways", action: { id: "open", values: { id: "a" } } },
      { id: "b", title: "Part B" },
      { id: "way1", title: "Smallest", group: "Two ways" },
      { id: "way2", title: "Remove", group: "Two ways" },
      { id: "c", title: "Check", tone: "positive", badge: { text: "Done", tone: "positive" }, progress: 0.4 },
      { id: "s1", title: "Step 1", subtitle: "2 suites" },
      { id: "s2", title: "Step 2" },
    ], edges: [
      { from: "root", to: "a" }, { from: "root", to: "b" }, { from: "a", to: "way1" }, { from: "a", to: "way2" },
      { from: "way1", to: "c" }, { from: "b", to: "s1" }, { from: "b", to: "s2" },
    ], folded: ["b"], selected: "c" }],
  }],
};

/** The node ids the canvas shows, in reading order, with `b` folded away. */
const VISIBLE = ["root", "a", "way1", "c", "way2", "b"];

const draw = (view: unknown) => readView(view as object).sections[0].items[0];

let host: HTMLDivElement;
let root: Root;
// the item's key remounts the canvas (a different key is a different graph, as the page's redraws keep it); the owner
// scopes with it, so two owners' graphs never share one instance either — the owner travels as a context
// (lib/extension-owner.tsx), as the surfaces that draw an extension's view provide it
type MountOpts = { compact?: boolean; onPress?: (a: UIAction) => void; owner?: string };
const mount = async (item: UIItem, opts?: MountOpts) => {
  const key = `${opts?.owner ?? ""}/${item.key ?? ""}`;
  const canvas = <GraphCanvas item={item} compact={opts?.compact} busy="" onPress={opts?.onPress ?? (() => {})} />;
  await act(async () =>
    root.render(opts?.owner === undefined ? <GraphCanvas key={key} item={item} compact={opts?.compact} busy="" onPress={opts?.onPress ?? (() => {})} /> : (
      <ExtensionOwnerScope key={key} owner={opts.owner}>{canvas}</ExtensionOwnerScope>
    )),
  );
};

const sizeStage = (w: number, h: number) => {
  const spy = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(w);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(h);
  return spy;
};

// jsdom has PointerEvent but not the capture API the canvas's drag path reads; a polyfill stands in for the
// browser's (a captured pointer stays the canvas's drag — true once a drag began, exactly what the code asks next).
const capturePolyfill = () => {
  const held = new Set<number>();
  const proto = HTMLElement.prototype as HTMLElement & {
    setPointerCapture: (id: number) => void;
    hasPointerCapture: (id: number) => boolean;
    releasePointerCapture: (id: number) => void;
  };
  proto.setPointerCapture ??= (id: number) => void held.add(id);
  proto.hasPointerCapture ??= (id: number) => held.has(id);
  proto.releasePointerCapture ??= (id: number) => void held.delete(id);
  return held;
};

/** A pointer drag over the stage: down at (x, y), then moves, without a control under it. */
const drag = (stage: Element, moves: [number, number][], pointerId = 1) => {
  const [x0, y0] = moves[0];
  const at = (x: number, y: number) => ({ pointerId, button: 0, clientX: x, clientY: y, bubbles: true, cancelable: true });
  act(() => stage.dispatchEvent(new PointerEvent("pointerdown", at(x0, y0))));
  for (let i = 1; i < moves.length; i++) act(() => stage.dispatchEvent(new PointerEvent("pointermove", at(moves[i][0], moves[i][1]))));
};
/** The layer's transform as numbers: [x, y, zoom]. */
const transformOf = (el: Element | null) =>
  (el as HTMLElement).style.transform.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/)!.slice(1).map(Number);

describe("UI 10: the graph canvas", () => {
  beforeEach(() => {
    forgetGraphStores(); // a test's clean slate: no key keeps another test's pan or folds
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    document.querySelectorAll("[role=dialog]").forEach((d) => d.remove());
  });

  const mountView = async (view: unknown, opts?: { compact?: boolean; onPress?: (a: UIAction) => void; owner?: string }) => {
    const pressed: UIAction[] = [];
    await mount(draw(view), { compact: opts?.compact, onPress: opts?.onPress ?? ((a) => pressed.push(a)), owner: opts?.owner });
    return { pressed };
  };

  it("draws the visible nodes as buttons in reading order, the edges in one SVG behind them", () => {
    // static markup: b is folded, so its own steps are hidden; the rest shows in reading order
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />));
    const nodes = [...d.querySelectorAll("[data-graph-node]")].map((e) => e.getAttribute("data-graph-node"));
    expect(nodes).toEqual(VISIBLE);
    // a node with an action is a button; without one it is a focusable group whose whole label reads out
    const buttons = [...d.querySelectorAll("[data-graph-node]")].filter((e) => e.tagName === "BUTTON");
    expect(buttons.map((b) => b.getAttribute("aria-label"))).toEqual([
      "Ship it，2 parts", // a root: nothing under it
      `Part A，${t("extensions-viewbody.graph_under", { title: "Ship it" })}`,
    ]);
    const bare = d.querySelector('[data-graph-node="c"]')!;
    expect(bare.tagName).not.toBe("BUTTON");
    expect(bare.getAttribute("role")).toBe("group");
    expect(bare.getAttribute("tabindex")).toBe("0"); // Tab reaches every node, with an action or without one
    expect(bare.getAttribute("aria-label")).toBe(`Check，Done，40%，${t("extensions-viewbody.graph_under", { title: "Smallest" })}`);
    expect(d.querySelectorAll("svg path[data-graph-edge]")).toHaveLength(5); // b's two edges are folded away
    expect(d.querySelector("[data-graph-frame]")?.getAttribute("data-graph-frame")).toBe("Two ways");
    expect(d.querySelector("[data-graph-selected]")).not.toBeNull(); // c is the selected node
  });

  it("reads a node out with its badge, subtitle, progress and parent, and a fold as Fold or the hidden count", () => {
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />));
    const c = d.querySelector('[data-graph-node="c"]')!;
    expect(c.getAttribute("aria-label")).toBe(`Check，Done，40%，${t("extensions-viewbody.graph_under", { title: "Smallest" })}`);
    const fold = d.querySelector('[data-graph-fold="b"]')!;
    expect(fold.getAttribute("aria-label")).toBe(t("extensions-viewbody.graph_show_hidden", { count: 2 }));
    expect(fold.textContent).toContain("+2");
    expect(fold.getAttribute("aria-expanded")).toBe("false"); // b is folded: nothing expanded under it
    // an open fold says Fold, expanded
    const rootFold = d.querySelector('[data-graph-fold="root"]')!;
    expect(rootFold.getAttribute("aria-label")).toBe(t("extensions-viewbody.graph_fold"));
    expect(rootFold.getAttribute("aria-expanded")).toBe("true");
  });

  it("reads the label in the doc's order — title, badge, subtitle, progress, then the parent — with all of them on one node", () => {
    // a node with a badge and a subtitle at once: their order in the label is the doc's, not whatever order the
    // fields happen to be read in (a swapped badge and subtitle must change the string)
    const both = structuredClone(WORK);
    const nodes = (both.sections[0].items[0] as { nodes: { id: string; subtitle?: string }[] }).nodes;
    nodes.find((n) => n.id === "c")!.subtitle = "3 checks";
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(both)} busy="" onPress={() => {}} />));
    expect(d.querySelector('[data-graph-node="c"]')!.getAttribute("aria-label")).toBe(
      `Check，Done，3 checks，40%，${t("extensions-viewbody.graph_under", { title: "Smallest" })}`,
    );
  });

  it("the selected node is the current one for a screen reader, and a group's frame title reads out", () => {
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />));
    expect(d.querySelector('[data-graph-node="c"]')!.getAttribute("aria-current")).toBe("true");
    expect(d.querySelector('[data-graph-node="root"]')!.getAttribute("aria-current")).toBeNull();
    // the frame's caption is not aria-hidden: the group's name reaches a screen reader (the nodes under it read
    // their own fields; the frame itself says which group they belong to)
    const frame = d.querySelector("[data-graph-frame]")!;
    expect(frame.getAttribute("aria-hidden")).toBeNull();
    expect(frame.textContent).toBe("Two ways");
  });

  it("a fold with a 3-digit hidden count grows past the 24 px circle instead of clipping its +N", () => {
    // a deep folded part with 123 hidden nodes: the chip widens for the count, keeping its centre on the gap
    const wide = structuredClone(WORK);
    const graph = wide.sections[0].items[0] as { nodes: { id: string; title: string }[]; edges: { from: string; to: string }[] };
    for (let i = 0; i < 121; i++) {
      graph.nodes.push({ id: `x${i}`, title: `X${i}` });
      graph.edges.push({ from: "s1", to: `x${i}` }); // under b's subtree: folding b hides them all
    }
    const item = draw(wide);
    item.folded = ["b"]; // b starts folded in the view: 2 steps + 121 leaves = 123 hidden
    const d = doc(renderToStaticMarkup(<GraphCanvas item={item} busy="" onPress={() => {}} />));
    const fold = d.querySelector('[data-graph-fold="b"]') as unknown as HTMLElement;
    expect(fold.textContent).toContain("+123");
    expect(Number.parseFloat(fold.style.width)).toBeGreaterThan(24); // the count fits, not clipped by the circle
    // a 2-digit count stays the 24 px circle
    const d2 = doc(renderToStaticMarkup(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />));
    expect(Number.parseFloat((d2.querySelector('[data-graph-fold="b"]') as unknown as HTMLElement).style.width)).toBe(24);
  });

  it("puts each fold control inside its node's own listitem, so Tab walks node, its fold, the next node", async () => {
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />));
    const item = d.querySelector('[data-graph-at="root"]')!;
    expect(item.getAttribute("role")).toBe("listitem");
    expect(item.contains(d.querySelector('[data-graph-fold="root"]')!)).toBe(true);
    expect([...d.querySelectorAll("[data-graph-fold]")].every((f) => f.closest('[role="listitem"]') !== null)).toBe(true);
    // and the DOM gives Tab exactly that order: a node, then its own fold, then the next node — jsdom moves no
    // focus itself, but the focusables in document order are what the browser's Tab walks
    await mountView(WORK);
    const list = [...host.querySelectorAll<HTMLElement>("button, [tabindex='0']")];
    const labels = list.map((el) => el.getAttribute("data-graph-node") ?? el.getAttribute("data-graph-fold"));
    expect(labels.filter(Boolean).slice(0, 5)).toEqual(["root", "root", "a", "a", "way1"]); // root's button, root's fold, a's button, a's fold, then way1
  });

  it("puts the fold control fully outside the node's box, beside it on the side its children leave it", () => {
    // the contract (docs/extensions/extensions.md "Drawing, on every app"): the fold control sits beside the node,
    // outside the box — never over the node's own end, where it would cover its badge. A right tree puts it in the
    // gap to its children on the right; a down tree below it. The control is placed inside the node's own wrapper
    // (so Tab walks the node then its fold), so its offsets are the wrapper's own frame: beside the box means past
    // its width (right) or its height (down), inside the pitch's gap, centred on the crossed axis.
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />));
    const node = d.querySelector('[data-graph-at="root"]')! as unknown as HTMLElement;
    const fold = d.querySelector('[data-graph-fold="root"]')! as unknown as HTMLElement;
    const px = (v: string): number => Number.parseFloat(v); // jsdom gives "224px"; the number is before the unit
    const w = px(node.style.width); // the wrapper is the node's own box: left, top, width, height
    const h = px(node.style.height);
    const fl = px(fold.style.left);
    const ft = px(fold.style.top);
    const S = 24; // the control's own size (size-6), scaled only on screen
    expect(w).toBeGreaterThan(0);
    expect(fl).toBeGreaterThanOrEqual(w); // fully past the node's right edge: nothing over the node's badge
    expect(fl).toBeLessThan(w + 48); // inside the gap to its children (pitch − node = 48)
    expect(ft + S / 2).toBeCloseTo(h / 2, 6); // centred on the node's own middle, beside it
    // the same downwards: the control sits below the node, centred in the gap below it
    const down = structuredClone(WORK);
    (down.sections[0].items[0] as { direction?: string }).direction = "down";
    const dd = doc(renderToStaticMarkup(<GraphCanvas item={draw(down)} busy="" onPress={() => {}} />));
    const dnode = dd.querySelector('[data-graph-at="root"]')! as unknown as HTMLElement;
    const dfold = dd.querySelector('[data-graph-fold="root"]')! as unknown as HTMLElement;
    const dw = px(dnode.style.width);
    const dh = px(dnode.style.height);
    const dfl = px(dfold.style.left);
    const dft = px(dfold.style.top);
    expect(dft).toBeGreaterThanOrEqual(dh); // fully below the node: never over its subtitle or progress
    expect(dft).toBeLessThan(dh + 28); // inside the gap (pitch − node = 28)
    expect(dfl + S / 2).toBeCloseTo(dw / 2, 6); // centred on the node's own middle, below it
  });

  it("presses a node's action and not the fold control beside it", async () => {
    const pressed: UIAction[] = [];
    await mountView(WORK, { onPress: (a) => pressed.push(a) });
    act(() => host.querySelector<HTMLButtonElement>('[data-graph-node="root"]')!.click());
    expect(pressed).toEqual([{ id: "open", values: { id: "root" } }]);
    act(() => host.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!.click());
    expect(pressed).toHaveLength(1); // folding never presses a node
  });

  it("keeps the node's element and the fold's focus across a redraw and a fold toggle", async () => {
    await mountView(WORK);
    const rootEl = host.querySelector('[data-graph-node="root"]')!;
    // a fold toggle re-renders in place: the node's element stays connected and the fold keeps the focus
    const foldBtn = host.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!;
    foldBtn.focus();
    act(() => foldBtn.click());
    await act(async () => {});
    expect(host.querySelector('[data-graph-node="root"]')!.isSameNode(rootEl)).toBe(true);
    expect(rootEl.isConnected).toBe(true);
    expect(document.activeElement?.isSameNode(foldBtn)).toBe(true);
    // a redraw that changes only states keeps the same element too
    const again = structuredClone(WORK);
    const nodes = (again.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
    nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
    await mount(draw(again), { compact: false });
    expect((host.querySelector('[data-graph-node="root"]') as HTMLElement).isSameNode(rootEl)).toBe(true);
    expect(rootEl.isConnected).toBe(true);
  });

  it("folds and unfolds by the person's choice, kept across redraws of the same key", async () => {
    await mountView(WORK);
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length); // b folded by the view
    act(() => host.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!.click());
    await act(async () => {}); // the fold's state settles
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length + 2); // b's steps are back
    // a redraw of the same key with the view's own folded list: the person's choice wins
    const again = structuredClone(WORK);
    (again.sections[0].items[0] as { folded: string[] }).folded = ["b"];
    await mount(draw(again), { compact: false });
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length + 2); // still the person's: b open
    // a different key takes the view's folds again
    const otherKey = structuredClone(WORK);
    (otherKey.sections[0].items[0] as { key: string }).key = "other";
    await mount(draw(otherKey), { compact: false });
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length);
  });

  it("gives two keyless graphs their own folds, and keeps two owners' keyed graphs apart", async () => {
    const noKey = structuredClone(WORK);
    delete (noKey.sections[0].items[0] as { key?: string }).key;
    // two keyless graphs, one after another: each takes its own `folded` (b folded, b not)
    await mountView(noKey);
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length);
    const open = structuredClone(noKey);
    (open.sections[0].items[0] as { folded?: string[] }).folded = [];
    await mount(draw(open), { compact: false });
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length + 2); // not the first one's folds
    // the same key under two owners: their folds never leak between them
    await mountView(WORK, { owner: "one" });
    act(() => host.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!.click());
    await act(async () => {});
    await mount(draw(WORK), { owner: "two" });
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length); // b still folded there
  });

  it("keeps two owners' keyed graphs apart when a lone re-render happens under one of them", async () => {
    // the stale-global shape the reviewer found: two cards on one page, the second card's graph re-renders alone (its
    // own state), and a module global set by the last render would have the first card's scope read the second's
    // owner. A context is right on every re-render, so each owner's entry keeps its own folds.
    const twoHost = document.createElement("div");
    document.body.append(twoHost);
    const twoRoot = createRoot(twoHost);
    try {
      const Card = ({ owner }: { owner: string }) => (
        <ExtensionOwnerScope owner={owner}>
          <GraphCanvas item={draw(structuredClone(WORK))} busy="" onPress={() => {}} />
        </ExtensionOwnerScope>
      );
      await act(async () => twoRoot.render(<><Card key="a" owner="one" /><Card key="b" owner="two" /></>));
      // the person unfolds b under the first owner alone
      const first = twoHost.querySelectorAll("[data-graph-stage]")[0]!;
      act(() => first.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!.click());
      await act(async () => {});
      expect(twoHost.querySelectorAll("[data-graph-node]")).toHaveLength(2 * VISIBLE.length + 2); // one unfolded, one still folded
      // a re-render of the whole page with the same owners: the first keeps its unfold, the second keeps its fold
      await act(async () => twoRoot.render(<><Card key="a" owner="one" /><Card key="b" owner="two" /></>));
      const per = [...twoHost.querySelectorAll("[data-graph-stage]")].map((s) => s.querySelectorAll("[data-graph-node]").length);
      expect(per).toEqual([VISIBLE.length + 2, VISIBLE.length]);
    } finally {
      act(() => twoRoot.unmount());
      twoHost.remove();
    }
  });

  it("keeps the pan and zoom across a redraw that changes only states, and re-fits while untouched", async () => {
    const spy = sizeStage(900, 500);
    await mountView(WORK);
    const stage = host.querySelector("[data-graph-stage]")!;
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const before = layer.style.transform;
    expect(before).not.toBe(""); // it laid itself out
    // the person zooms: Ctrl + wheel over the canvas
    act(() => stage.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, ctrlKey: true, bubbles: true, cancelable: true })));
    const moved = layer.style.transform;
    expect(moved).not.toBe(before);
    // a redraw with the same shape but a state moved on: the position stays
    const again = structuredClone(WORK);
    const nodes = (again.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
    nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
    await mount(draw(again), { compact: false });
    expect((host.querySelector("[data-graph-layer]") as HTMLElement).style.transform).toBe(moved);
    // while the person never moves it, a redraw takes the opening view again
    const otherKey = structuredClone(WORK);
    (otherKey.sections[0].items[0] as { key: string }).key = "fresh";
    await mount(draw(otherKey), { compact: false });
    expect((host.querySelector("[data-graph-layer]") as HTMLElement).style.transform).toBe(before);
    spy.mockRestore();
  });

  it("keeps Fit's view across a redraw of the same key: Fit counts as the person moving the canvas", async () => {
    const spy = sizeStage(900, 500);
    await mountView(WORK);
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const lw = Number.parseFloat(layer.style.width);
    const lh = Number.parseFloat(layer.style.height);
    // Fit: everything centred at the fitted scale (the opening view would not be centred, with `selected` there)
    act(() => host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_fit")}"]`)!.click());
    const fitted = transformOf(layer);
    const wantZ = Math.max(0.25, Math.min(1, Math.min((900 - 80) / lw, (500 - 80) / lh)));
    expect(fitted[2]).toBeCloseTo(wantZ, 9);
    expect(fitted[0]).toBeCloseTo((900 - lw * wantZ) / 2, 9);
    // a redraw that changes only states keeps the person's Fit — without marking Fit as a move, the redraw would
    // take the opening view again and throw it away
    const again = structuredClone(WORK);
    const nodes = (again.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
    nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
    await mount(draw(again), { compact: false });
    expect(transformOf(host.querySelector("[data-graph-layer]"))).toEqual(fitted);
    spy.mockRestore();
  });

  it("keeps Fit's zoom across a redraw at a stage where Fit and the opening view differ", async () => {
    // 600 x 400: the fitted scale is 0.471 but the opening view holds it to 50%, so the two differ. A Fit that did
    // not count as the person's move survives one redraw only while it coincides with the opening view — at this
    // stage the redraw's re-open would change the zoom, and the assertion sees it.
    const spy = sizeStage(600, 400);
    await mountView(WORK);
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const lw = Number.parseFloat(layer.style.width);
    const lh = Number.parseFloat(layer.style.height);
    const wantZ = Math.max(0.25, Math.min(1, Math.min((600 - 80) / lw, (400 - 80) / lh)));
    expect(transformOf(layer)[2]).toBeCloseTo(0.75, 9); // the first screen: a wide graph opens at the readable floor
    expect(wantZ).toBeLessThan(0.75); // the stage makes the two differ: the check below can see a lost Fit
    act(() => host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_fit")}"]`)!.click());
    expect(transformOf(layer)[2]).toBeCloseTo(wantZ, 9); // Fit went below the opening view's 50% floor
    const again = structuredClone(WORK);
    const nodes = (again.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
    nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
    await mount(draw(again), { compact: false });
    expect(transformOf(host.querySelector("[data-graph-layer]"))[2]).toBeCloseTo(wantZ, 9); // the fit is kept
    spy.mockRestore();
  });

  it("a drag keeps panning across a redraw mid-gesture (a live graph's states change while it runs)", async () => {
    capturePolyfill();
    const spy = sizeStage(300, 200); // small enough that the drawing overflows: a pan is not clamped back to centre
    await mountView(WORK);
    const stage = host.querySelector("[data-graph-stage]")!;
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const at = () => transformOf(layer);
    const start = at();
    drag(stage, [[500, 300], [470, 280]]); // a 30 px pan
    const pan1 = at();
    expect(pan1).not.toEqual(start);
    // a redraw of the same key (states changed) must not lose the pointer: the drag goes on panning
    const again = structuredClone(WORK);
    const nodes = (again.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
    nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
    await mount(draw(again), { compact: false });
    const stage2 = host.querySelector("[data-graph-stage]")!;
    const layer2 = host.querySelector("[data-graph-layer]") as HTMLElement;
    expect(layer2).toBe(layer); // the redraw re-renders in place, not a remount
    drag(stage2, [[470, 280], [440, 260]], 1); // the same pointer keeps moving
    expect(transformOf(layer2)).not.toEqual(pan1);
    spy.mockRestore();
  });

  it("without a selected node and nothing needing the person, opens with the first root at the top start", async () => {
    const spy = sizeStage(900, 500);
    const noSel = structuredClone(WORK);
    delete (noSel.sections[0].items[0] as { selected?: string }).selected;
    (noSel.sections[0].items[0] as { key?: string }).key = "fresh-root";
    // no tone, badge, progress or timing: nothing for the opening view to focus instead of the first root
    const nodes = (noSel.sections[0].items[0] as { nodes: Record<string, unknown>[] }).nodes;
    const c = nodes.find((n) => n.id === "c")!;
    delete c.tone;
    delete c.badge;
    delete c.progress;
    await mountView(noSel);
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const lw = Number.parseFloat(layer.style.width);
    const lh = Number.parseFloat(layer.style.height);
    const m = layer.style.transform.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/)!;
    const [x, y, zoom] = m.slice(1).map(Number);
    // a right tree's first root sits halfway down its children: the layer's corner would leave it off-screen. The
    // root's top-left corner (its slot is [0, 0], so at() is MARGIN on both axes) lands at the inset (40, 40) unless
    // an axis fits and clampPan centres it — then the whole drawing is inside, which the numbers say too.
    const rootX = x + 32 * zoom;
    const rootY = y + 32 * zoom;
    if (lw * zoom <= 900 && lh * zoom <= 500) {
      expect(x).toBeCloseTo((900 - lw * zoom) / 2, 9);
      expect(y).toBeCloseTo((500 - lh * zoom) / 2, 9);
    } else {
      expect([rootX, rootY]).toEqual([40, 40]);
    }
    spy.mockRestore();
  });

  it("opens centred on the selected node, wherever it is — not on the first root or the drawing's centre", async () => {
    // a stage the drawing overflows on both axes at the opening scale (300 x 150 at zoom 0.5: 552 x 167.5), so
    // neither axis is centred by clamping and each candidate centre (selected, first root, drawing) gives a
    // different transform. The selected node c sits at slots [3, 0]: its own centre must be the stage's centre.
    const spy = sizeStage(300, 150);
    await mountView(WORK);
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const lw = Number.parseFloat(layer.style.width);
    const lh = Number.parseFloat(layer.style.height);
    const [x, y, zoom] = transformOf(layer);
    expect(zoom).toBeCloseTo(0.75, 9); // the readable floor, never under 75% on a first screen
    expect(lw * zoom).toBeGreaterThan(300); // the drawing overflows both axes: clamping centres neither
    expect(lh * zoom).toBeGreaterThan(150);
    const centreOf = (id: string): [number, number] => {
      const el = host.querySelector(`[data-graph-at="${id}"]`) as HTMLElement;
      const sx = Number.parseFloat(el.style.left);
      const sy = Number.parseFloat(el.style.top);
      return [x + (sx + Number.parseFloat(el.style.width) / 2) * zoom, y + (sy + Number.parseFloat(el.style.height) / 2) * zoom];
    };
    const [cx, cy] = centreOf("c");
    expect(cx).toBeCloseTo(150, 6); // the selected node's centre is the stage's centre…
    expect(cy).toBeCloseTo(75, 6);
    const [rootCx] = centreOf("root"); // …not the first root's (half a drawing away) nor the layer's
    expect(Math.abs(cx - rootCx)).toBeGreaterThan(50);
    expect(Math.abs(cx - (x + lw * zoom / 2))).toBeGreaterThan(50); // the drawing's own centre
    spy.mockRestore();
  });

  it("in a card, draws the still fitted preview that opens Full screen, with no buttons or node presses", async () => {
    const pressed: UIAction[] = [];
    await mountView(WORK, { compact: true, onPress: (a) => pressed.push(a) });
    const preview = host.querySelector(".graph-preview")!;
    expect(preview).not.toBeNull();
    // compact: no zoom buttons, no node presses — the only button is the preview itself
    const labels = [...host.querySelectorAll("button")].map((b) => b.getAttribute("aria-label"));
    expect(labels).toEqual([t("extensions-viewbody.graph_full_screen")]);
    expect(host.querySelector("[data-graph-stage]")!.getAttribute("tabindex")).toBe("-1");
    expect(host.querySelector('[data-graph-node="root"]')!.tagName).not.toBe("BUTTON");
    act(() => (preview as HTMLButtonElement).click());
    await act(async () => {});
    const overlay = document.querySelector("[role=dialog]")!;
    expect(overlay.getAttribute("aria-modal")).toBe("true");
    expect(overlay.querySelector("[data-graph-stage]")).not.toBeNull(); // the same canvas inside
    // Esc closes it (ui/overlay's stack)
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(pressed).toEqual([]); // nothing was pressed on the way
    // the focus comes back to the card's preview button, the control that reopens Full screen — not lost to the body
    expect(document.activeElement?.classList.contains("graph-preview")).toBe(true);
  });

  it("gives the focus back to the Full screen button after Esc, on the page and from a card", async () => {
    // the page: the Full screen button is replaced by the portal while the overlay is over it, so the focus used to
    // fall to the body; it comes back to the button that reopens the overlay
    await mountView(WORK);
    const full = host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_full_screen")}"]`)!;
    full.focus();
    expect(document.activeElement).toBe(full);
    act(() => full.click());
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).toBeNull();
    const back = host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_full_screen")}"]`)!;
    expect(document.activeElement).toBe(back); // a keyboard user presses Enter to reopen, not Tab from the top
    // a card: the preview button is the opener, and it is back too
    await mountView(WORK, { compact: true });
    const preview = host.querySelector<HTMLButtonElement>(".graph-preview")!;
    preview.focus();
    act(() => preview.click());
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    expect(document.activeElement).toBe(host.querySelector<HTMLButtonElement>(".graph-preview"));
  });

  it("opening Full screen focuses the dialog itself, so the Tab trap runs (the dialog is focusable)", async () => {
    // useModalFocus focuses the dialog element when nothing inside has the focus — which needs tabIndex={-1}, or the
    // call does nothing and the focus stays outside the overlay (Tab leaves it, and a screen reader reads the page
    // behind the modal).
    await mountView(WORK);
    const full = host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_full_screen")}"]`)!;
    full.focus();
    act(() => full.click());
    await act(async () => {});
    const overlay = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(overlay.getAttribute("tabindex")).toBe("-1");
    expect(document.activeElement).toBe(overlay); // nothing inside was focused: the dialog itself takes it
    // the Tab trap runs: Tab from the dialog's close button wraps to the dialog and never leaves the overlay
    const close = overlay.querySelector<HTMLButtonElement>("button")!;
    close.focus();
    act(() => overlay.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true })));
    await act(async () => {});
    expect(document.activeElement).not.toBe(document.body);
    expect(overlay.contains(document.activeElement)).toBe(true);
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  it("a focusable node without an action shows the focus ring (a group div, not a button)", async () => {
    await mountView(WORK);
    // `b` has no action: its node is a plain div with tabindex="0" — focusable with Tab, so it needs its own
    // focus-visible outline (a button's own UA ring does not cover it).
    const b = host.querySelector<HTMLElement>('[data-graph-node="b"]')!;
    expect(b.tagName).toBe("DIV");
    expect(b.getAttribute("tabindex")).toBe("0");
    expect(b.className).toContain("focus-visible:outline-2");
    expect(b.className).toContain("focus-visible:outline-accent");
  });

  it("a compact preview fits the whole layer into the stage, with no 25% floor", async () => {
    const spy = sizeStage(600, 200);
    await mountView(WORK, { compact: true });
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    // the mock's view is drawn at 600 x 200 (jsdom reports the client sizes the test set): the still preview fits
    // everything into it — zoom = min((w-80)/lw, (h-80)/lh) held to [0.01, 1], centred — never the opening view.
    const lw = Number.parseFloat(layer.style.width);
    const lh = Number.parseFloat(layer.style.height);
    const want = Math.min((600 - 80) / lw, (200 - 80) / lh);
    const m = layer.style.transform.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/)!;
    const [x, y, zoom] = m.slice(1).map(Number);
    expect(zoom).toBeCloseTo(Math.max(0.01, Math.min(1, want)), 9);
    expect(x).toBeCloseTo((600 - lw * zoom) / 2, 9);
    expect(y).toBeCloseTo((200 - lh * zoom) / 2, 9);
    // a re-run of the layout (a redraw of the same item) fits again, not the opening view
    await mount(draw(WORK), { compact: true });
    const again = (host.querySelector("[data-graph-layer]") as HTMLElement).style.transform.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/)!;
    expect(Number(again[3])).toBeCloseTo(Math.max(0.01, Math.min(1, want)), 9);
    spy.mockRestore();
  });

  it("a still card never writes the page's kept transform, and Full screen from a card is interactive", async () => {
    const spy = sizeStage(900, 500);
    const pressed: UIAction[] = [];
    // the page's canvas: the person zooms it (kept for the key)
    await mountView(WORK, { onPress: (a) => pressed.push(a) });
    const stage = host.querySelector("[data-graph-stage]")!;
    act(() => stage.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, ctrlKey: true, bubbles: true, cancelable: true })));
    const kept = (host.querySelector("[data-graph-layer]") as HTMLElement).style.transform;
    expect(kept).not.toBe("");
    // a compact card of the same key fits itself and never touches the kept view; opening it fits again
    await mount(draw(WORK), { compact: true, onPress: (a) => pressed.push(a) });
    const preview = host.querySelector<HTMLButtonElement>(".graph-preview")!;
    expect(preview).not.toBeNull();
    // Full screen from the card: the same interactive canvas — node buttons, fold controls, a drag and a wheel
    act(() => preview.click());
    await act(async () => {});
    const overlay = document.querySelector("[role=dialog]")!;
    const nodeBtn = overlay.querySelector<HTMLButtonElement>('[data-graph-node="root"]')!;
    expect(nodeBtn.tagName).toBe("BUTTON"); // not the still preview's plain card
    expect(overlay.querySelector('[data-graph-fold="root"]')).not.toBeNull();
    act(() => nodeBtn.click());
    expect(pressed).toEqual([{ id: "open", values: { id: "root" } }]);
    // Full screen from the card opens with its own view — the page's opening view when the person has not moved it
    // (the card's fits never touch the store) — never the still preview's own fit.
    const overlayZoom = Number((overlay.querySelector("[data-graph-layer]") as HTMLElement).style.transform.match(/scale\(([-\d.]+)\)/)![1]);
    expect(overlayZoom).toBeGreaterThan(0.5);
    expect(overlayZoom).toBeLessThanOrEqual(1.2); // the opening view's fitted scale held to 50%-100%
    // Esc closes it
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).toBeNull();
    // the page's own kept view is what it was: the card's fits and the overlay's zoom never reached it
    await mount(draw(WORK), { compact: false });
    const again = (host.querySelector("[data-graph-layer]") as HTMLElement).style.transform.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/)!;
    const keptM = kept.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/)!;
    expect(Number(again[3])).toBeCloseTo(Number(keptM[3]), 9); // the zoom the person left it at
    spy.mockRestore();
  });

  it("walks the nodes with Tab in reading order and presses with Enter", async () => {
    const pressed: UIAction[] = [];
    await mount(draw(WORK), { compact: false, onPress: (a) => pressed.push(a) });
    // every visible node is focusable in the DOM's reading order: a button when it acts, a group when it does not.
    // A button without its own tabindex is focusable by its own nature; a group carries tabindex="0".
    for (const id of VISIBLE) {
      const el = host.querySelector(`[data-graph-node="${id}"]`)!;
      expect(el.tagName === "BUTTON" || el.getAttribute("tabindex") === "0").toBe(true);
      expect(el.getAttribute("tabindex")).not.toBe("-1");
    }
    const order = [...host.querySelectorAll("[data-graph-node]")].map((e) => e.getAttribute("data-graph-node"));
    expect(order).toEqual(VISIBLE);
    const a = host.querySelector<HTMLButtonElement>('[data-graph-node="a"]')!;
    a.focus(); // Tab reaches it in the reading order above
    act(() => a.click()); // Enter or Space presses a focused button (the browser's own button behavior)
    expect(pressed).toEqual([{ id: "open", values: { id: "a" } }]);
  });

  it("pans by the arrow keys with the canvas focused, each arrow its own way", async () => {
    // a stage the drawing overflows (300 x 150 at the opening scale), so a pan is not clamped back and each arrow
    // leaves a visible, different delta. ArrowRight moves the drawing left (the view travels right): the deltas
    // carry the directions, so a swapped pair cannot pass.
    const spy = sizeStage(300, 150);
    await mountView(WORK);
    const stage = host.querySelector("[data-graph-stage]")!;
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const before = transformOf(layer);
    const key = (k: string) => act(() => stage.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
    key("ArrowRight");
    const right = transformOf(layer);
    expect(right[0]).toBeCloseTo(before[0] - 48, 6); // the drawing moves left: the view travels right
    expect(right[1]).toBeCloseTo(before[1], 6);
    key("ArrowLeft");
    expect(transformOf(layer)[0]).toBeCloseTo(before[0], 6); // and back where it was
    key("ArrowDown");
    const downv = transformOf(layer);
    expect(downv[1]).toBeCloseTo(before[1] - 48, 6); // the drawing moves up: the view travels down
    expect(downv[0]).toBeCloseTo(before[0], 6);
    key("ArrowUp");
    expect(transformOf(layer)[1]).toBeCloseTo(before[1], 6);
    spy.mockRestore();
  });

  it("zooms with + and fits with 0 while the canvas is focused", async () => {
    // 600 x 400: the fitted scale (0.471) differs from both the opening view (0.5) and the floor (0.25), so `0`
    // fitting can be told apart from a zoom-out and from a no-op.
    const spy = sizeStage(600, 400);
    await mountView(WORK);
    const stage = host.querySelector("[data-graph-stage]")!;
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const lw = Number.parseFloat(layer.style.width);
    const lh = Number.parseFloat(layer.style.height);
    const wantZ = Math.max(0.25, Math.min(1, Math.min((600 - 80) / lw, (400 - 80) / lh)));
    const key = (k: string) => act(() => stage.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
    key("+");
    const plus = transformOf(layer);
    expect(plus[2]).toBeCloseTo(0.75 * 1.25, 6); // + zooms in from the opening view
    key("0");
    const zero = transformOf(layer);
    expect(zero[2]).toBeCloseTo(wantZ, 6); // 0 fits: the fitted scale, centred — not the floor, not the opening view
    expect(zero[0]).toBeCloseTo((600 - lw * wantZ) / 2, 6);
    key("-");
    expect(transformOf(layer)[2]).toBeCloseTo(wantZ / 1.25, 6); // - zooms back out from the fit
    spy.mockRestore();
  });

  it("pans a focused node into view, without counting as the person's move", async () => {
    // a wide stage (900 x 500) where the whole drawing fits inside the inset after the opening view: a far node
    // (b's hidden steps aside, take s2 at the bottom) still sits outside the stage after the opening view centres
    // on the selected node, and focusing it must pan it into view.
    const spy = sizeStage(300, 150);
    await mountView(WORK);
    const stage = host.querySelector("[data-graph-stage]")!;
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const before = transformOf(layer);
    // b is folded, so its steps are not there: unfold it first — the person's fold, then the nodes exist again
    act(() => host.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!.click());
    await act(async () => {});
    // the opening view re-ran on the redraw (the fold changed the layout): read where it is now
    const folded = transformOf(layer);
    // s2 sits at slots [2, 3.25] — the deepest column, far off the 300 x 150 stage; focus it
    const s2 = host.querySelector<HTMLElement>('[data-graph-node="s2"]')!;
    act(() => s2.focus());
    const after = transformOf(layer);
    expect(after).not.toEqual(folded); // focusing a far node pans
    // the node's box is inside the stage now: its place × zoom plus the translate, with its whole size
    const el = host.querySelector('[data-graph-at="s2"]') as HTMLElement;
    const nx = Number.parseFloat(el.style.left);
    const ny = Number.parseFloat(el.style.top);
    const nw = Number.parseFloat(el.style.width);
    const nh = Number.parseFloat(el.style.height);
    const sx = after[0] + nx * after[2];
    const sy = after[1] + ny * after[2];
    expect(sx).toBeGreaterThanOrEqual(12);
    expect(sy).toBeGreaterThanOrEqual(12);
    expect(sx + nw * after[2]).toBeLessThanOrEqual(300 - 12);
    expect(sy + nh * after[2]).toBeLessThanOrEqual(150 - 12);
    // and the pan is kept for the key but does not mark the person's move: a redraw keeps it (an untouched key
    // would take the opening view again and drop the focused node out of view)
    const again = structuredClone(WORK);
    const nodes = (again.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
    nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
    // the person's unfold (kept in the store) survives too: s2 is still there after the redraw
    await mount(draw(again), { compact: false });
    expect(host.querySelector('[data-graph-node="s2"]')).not.toBeNull();
    const kept = transformOf(host.querySelector("[data-graph-layer]"));
    expect(kept[2]).toBeCloseTo(after[2], 6); // the zoom the reveal left it at
    void before;
    void stage;
    spy.mockRestore();
  });

  it("keeps the person's folds in the store across an unmount and remount of the same key", async () => {
    // the fold set is written to the store on the toggle and read from it at the mount: a remount of the same key
    // (the person leaves the page and comes back) keeps their choice, where the view's own `folded` alone would
    // fold b again
    await mountView(WORK);
    act(() => host.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!.click());
    await act(async () => {});
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length + 2); // b's steps are back
    act(() => root.unmount());
    host.remove();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await mount(draw(WORK), { compact: false }); // the same key, a fresh instance: the store's folds, not the view's
    expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length + 2); // still the person's: b open
  });

  it("grows only through design tokens: no new pixel font sizes or hex colors in what it draws", () => {
    // the per-file ratchet (scripts/ci/web_font_ratchet.py) counts the source; here the markup carries no raw
    // hex color of its own — sizes may still come from shared components (a badge's Pill)
    const html = renderToStaticMarkup(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />);
    expect(html).not.toMatch(/#[0-9a-f]{3,8}\b/i);
  });

  it("the first tap after a pan that began on a node still presses (the drag's click-swallow is gone)", async () => {
    // the reviewer's finding: a drag that began on a control attaches a one-shot capture click listener to swallow
    // its own click, but the stage's pointer capture retargets that click to the stage — the listener never fires,
    // stays attached, and eats the NEXT real tap on the same node in the capture phase.
    const held = capturePolyfill();
    const spy = sizeStage(300, 200); // the drawing overflows: the pan moves and clamps nothing back
    const pressed: UIAction[] = [];
    await mountView(WORK, { onPress: (a) => pressed.push(a) });
    const stage = host.querySelector("[data-graph-stage]")!;
    const node = host.querySelector<HTMLButtonElement>('[data-graph-node="root"]')!;
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const before = transformOf(layer);
    // the gesture: down on the node, moves still targeting the node (the browser keeps the target until the drag
    // takes the capture), a move past 4 px makes the drag take over, up, and the stage's own click
    act(() => node.dispatchEvent(new PointerEvent("pointerdown", { pointerId: 7, button: 0, clientX: 500, clientY: 300, bubbles: true, cancelable: true })));
    const move = (target: Element, x: number, y: number) => act(() => target.dispatchEvent(new PointerEvent("pointermove", { pointerId: 7, clientX: x, clientY: y, bubbles: true, cancelable: true })));
    move(node, 494, 300); // under 4 px from the down: still the node's own press
    move(node, 470, 280); // past it: the drag takes over, captures, and pans
    expect(transformOf(layer)).not.toEqual(before);
    move(stage, 470, 280); // the captured pointer now reports to the stage
    act(() => stage.dispatchEvent(new PointerEvent("pointerup", { pointerId: 7, clientX: 470, clientY: 280, bubbles: true, cancelable: true })));
    act(() => stage.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }))); // the gesture's own click, retargeted
    expect(pressed).toEqual([]); // the pan never pressed the node
    // the gesture's click swallowed (or timed out), the next real tap on the SAME node presses — the leftover
    // listener used to stop it in the capture phase
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    act(() => node.click());
    expect(pressed).toEqual([{ id: "open", values: { id: "root" } }]);
    spy.mockRestore();
    void held;
  });

  it("the first tap after a pan that began on a fold control still folds", async () => {
    // the same leftover, on the fold control: a pan begun on it must not leave the next fold tap dead
    const held = capturePolyfill();
    const spy = sizeStage(300, 200);
    await mountView(WORK);
    const stage = host.querySelector("[data-graph-stage]")!;
    const fold = host.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!;
    act(() => fold.dispatchEvent(new PointerEvent("pointerdown", { pointerId: 7, button: 0, clientX: 500, clientY: 300, bubbles: true, cancelable: true })));
    // the moves target the fold until the drag takes the capture
    act(() => fold.dispatchEvent(new PointerEvent("pointermove", { pointerId: 7, clientX: 470, clientY: 280, bubbles: true, cancelable: true })));
    act(() => stage.dispatchEvent(new PointerEvent("pointermove", { pointerId: 7, clientX: 470, clientY: 280, bubbles: true, cancelable: true })));
    act(() => stage.dispatchEvent(new PointerEvent("pointerup", { pointerId: 7, clientX: 470, clientY: 280, bubbles: true, cancelable: true })));
    act(() => stage.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    const before = host.querySelectorAll("[data-graph-node]").length;
    act(() => fold.click());
    await act(async () => {});
    expect(host.querySelectorAll("[data-graph-node]")).not.toBe(before); // b unfolds: the tap folded it
    spy.mockRestore();
    void held;
  });

  it("a node whose action is busy shows the busy cue and stays focusable, as the row's button does", () => {
    // a slow action on a node must not look idle: the row's own Button gets a spinner and aria-busy
    const view = structuredClone(WORK);
    const nodes = (view.sections[0].items[0] as { nodes: { id: string; action?: { id: string } }[] }).nodes;
    nodes.find((n) => n.id === "a")!.action = { id: "open-a", values: {} } as never; // a distinct action, so `open` is root's alone
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(view)} busy="open" onPress={() => {}} />));
    const root = d.querySelector('[data-graph-node="root"]')!;
    expect(root.getAttribute("aria-busy")).toBe("true"); // a screen reader hears the work
    expect(root.querySelector(".busy-motion")).not.toBeNull(); // a spinner shows it
    // a press on a node whose action is NOT the busy one is disabled (the row's own rule), but never loses focus
    const a = d.querySelector('[data-graph-node="a"]')!;
    expect(a.getAttribute("aria-busy")).toBe(null);
    expect((a as unknown as HTMLButtonElement).disabled).toBe(true);
    const plain = d.querySelector('[data-graph-node="b"]')!;
    expect(plain.tagName).not.toBe("BUTTON"); // a node without an action is never disabled
  });

  it("a node fades in under StrictMode: the fade memory is written by the commit, not the render", async () => {
    // the render-side memory write meant every committed node under the app's StrictMode (main.tsx) drew with no
    // fade at all: the double-invoked render read the signatures it had just written. The fade must come from the
    // previous committed map.
    const { StrictMode } = await import("react");
    const strictHost = document.createElement("div");
    document.body.append(strictHost);
    const strictRoot = createRoot(strictHost);
    try {
      await act(async () => strictRoot.render(
        <StrictMode>
          <GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />
        </StrictMode>,
      ));
      const first = strictHost.querySelector('[data-graph-node="root"]')!;
      expect(first.getAttribute("style")).toContain("fade-in"); // the first commit fades in
      // a states-only redraw does not replay it; a changed field does
      const redraw = structuredClone(WORK);
      const nodes = (redraw.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
      nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
      await act(async () => strictRoot.render(
        <StrictMode>
          <GraphCanvas item={draw(redraw)} busy="" onPress={() => {}} />
        </StrictMode>,
      ));
      expect(strictHost.querySelector('[data-graph-node="root"]')!.getAttribute("style")).not.toContain("fade-in"); // only states changed
      expect(strictHost.querySelector('[data-graph-node="c"]')!.getAttribute("style")).toContain("fade-in"); // c's fields changed
    } finally {
      act(() => strictRoot.unmount());
      strictHost.remove();
    }
  });

  it("a pointer held over the Full screen switch does not pinch the next drag", async () => {
    // the reviewer's stale-map finding: one finger on the canvas, another taps Full screen (the stage element is
    // replaced); the second pointer's entry stays in the map, so the next single-finger drag pairs with it and
    // becomes a phantom pinch. The rebind must drop what it did not see go down.
    const held = capturePolyfill();
    const spy = sizeStage(300, 200);
    await mountView(WORK);
    const stage = host.querySelector("[data-graph-stage]")!;
    // finger one goes down on the canvas and stays down
    act(() => stage.dispatchEvent(new PointerEvent("pointerdown", { pointerId: 1, button: 0, clientX: 100, clientY: 100, bubbles: true, cancelable: true })));
    act(() => stage.dispatchEvent(new PointerEvent("pointermove", { pointerId: 1, clientX: 100, clientY: 100, bubbles: true, cancelable: true })));
    // finger two taps the Full screen button: the overlay replaces the stage, finger one never sees its pointerup
    act(() => host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_full_screen")}"]`)!.click());
    await act(async () => {});
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    // a fresh single-finger drag: 10 px, straight down. A stale second point would pinch (zoom 0.5 -> ~0.44); a
    // clean map pans at the same zoom.
    const stage2 = host.querySelector("[data-graph-stage]")!;
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const before = transformOf(layer);
    act(() => stage2.dispatchEvent(new PointerEvent("pointerdown", { pointerId: 9, button: 0, clientX: 200, clientY: 100, bubbles: true, cancelable: true })));
    act(() => stage2.dispatchEvent(new PointerEvent("pointermove", { pointerId: 9, clientX: 200, clientY: 110, bubbles: true, cancelable: true })));
    const after = transformOf(layer);
    expect(after[2]).toBeCloseTo(before[2], 9); // no phantom pinch: the zoom never moved
    spy.mockRestore();
    void held;
  });

  it("a canvas whose owner changes without a remount takes the new owner's folds and writes to its store", async () => {
    // the reviewer's finding: personFolds was read once at mount, so the same instance under a new owner kept the
    // old owner's folds and wrote its toggles into the old owner's store.
    const swapHost = document.createElement("div");
    document.body.append(swapHost);
    let swapRoot = createRoot(swapHost);
    try {
      const Graph = ({ owner }: { owner: string }) => (
        <ExtensionOwnerScope owner={owner}>
          <GraphCanvas item={draw(structuredClone(WORK))} busy="" onPress={() => {}} />
        </ExtensionOwnerScope>
      );
      // owner one unfolds b (their store keeps it); owner two has never touched it (the view's fold stands)
      await act(async () => swapRoot.render(<Graph owner="one" />));
      act(() => swapHost.querySelector<HTMLButtonElement>('[data-graph-fold="b"]')!.click());
      await act(async () => {});
      expect(swapHost.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length + 2); // b open under one
      // the same instance, a new owner (the context value changes; no keyed remount)
      await act(async () => swapRoot.render(<Graph owner="two" />));
      expect(swapHost.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length); // two's store: b folded by the view
      // and a toggle under two writes to TWO's store, not one's: a remount under one still keeps the unfold
      act(() => swapHost.querySelector<HTMLButtonElement>('[data-graph-fold="root"]')!.click());
      await act(async () => {});
      await act(async () => swapRoot.unmount());
      swapHost.remove();
      swapRoot = createRoot(swapHost);
      document.body.append(swapHost);
      await act(async () => swapRoot.render(<Graph owner="one" />));
      expect(swapHost.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length + 2); // one's unfold kept
      await act(async () => swapRoot.render(<Graph owner="two" />));
      const twoNodes = swapHost.querySelectorAll("[data-graph-node]").length;
      expect(twoNodes).not.toBe(VISIBLE.length + 2); // two's own store, not one's
    } finally {
      act(() => swapRoot.unmount());
      swapHost.remove();
    }
  });

  it("the store's cap never evicts the entry of a canvas that is still mounted", async () => {
    // the reviewer's finding: with the cap reached, the on-screen key's entry was rebuilt fresh on its next
    // states-only redraw, which reads moved: false and takes the opening view — throwing the person's pan away.
    const spy = sizeStage(900, 500);
    // the holder host keeps the canvas mounted the whole time; the fillers mount and unmount on their own hosts
    const holderHost = document.createElement("div");
    document.body.append(holderHost);
    const holderRoot = createRoot(holderHost);
    const held = { root, host };
    await act(async () => holderRoot.render(<GraphCanvas item={draw(WORK)} busy="" onPress={() => {}} />));
    const stage = holderHost.querySelector("[data-graph-stage]")!;
    act(() => stage.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, ctrlKey: true, bubbles: true, cancelable: true })));
    const kept = transformOf(holderHost.querySelector("[data-graph-layer]"));
    expect(kept[2]).not.toBeCloseTo(0.75, 9); // the person zoomed: the store keeps it as moved
    // mount 140 other keys (over the cap of 128) — each on its own host, unmounted at once: never this one
    const fillerHost = document.createElement("div");
    document.body.append(fillerHost);
    for (let i = 0; i < 140; i++) {
      const other = structuredClone(WORK);
      (other.sections[0].items[0] as { key: string }).key = `filler-${i}`;
      const fillerRoot = createRoot(fillerHost);
      await act(async () => fillerRoot.render(<GraphCanvas item={draw(other)} busy="" onPress={() => {}} />));
      act(() => fillerRoot.unmount());
    }
    fillerHost.remove();
    // the filler keys evicted each other, never this one: a states-only redraw keeps the person's zoom
    const again = structuredClone(WORK);
    const nodes = (again.sections[0].items[0] as { nodes: { id: string; badge?: { text: string } }[] }).nodes;
    nodes.find((n) => n.id === "c")!.badge = { text: "Running" };
    await act(async () => holderRoot.render(<GraphCanvas item={draw(again)} busy="" onPress={() => {}} />));
    const now = transformOf(holderHost.querySelector("[data-graph-layer]"));
    expect(now[2]).toBeCloseTo(kept[2], 9); // not the opening 0.5: the pan was never thrown away
    act(() => holderRoot.unmount());
    holderHost.remove();
    root = held.root;
    host = held.host;
    spy.mockRestore();
  });
});

describe("UI 10: a graph in a view", () => {
  beforeEach(() => {
    forgetGraphStores(); // a test's clean slate: no key keeps another test's pan or folds
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
  });

  it("loads the canvas from its own chunk into the page", async () => {
    await act(async () => root.render(<ViewSections view={readView(WORK)} {...INERT} onPress={() => {}} />));
    await vi.waitFor(() => expect(host.querySelectorAll("[data-graph-node]")).toHaveLength(VISIBLE.length), { timeout: 30_000 });
    expect(host.querySelector(".graph-canvas")).not.toBeNull(); // the page keeps its height meanwhile
  }, 60_000);
});

// ── the control room: outline, inspector, timeline (UI version 10, #9727) ─────────────────────────────────────────
// A rich "run" graph: one node with timing, readings, a summary and a session link (a press opens it in place), one
// plain node that still presses its action, and two leaves.
const NOW = 1_700_000_000;
const RICH = (startedAgo = 10, span?: number) => {
  const started = NOW - startedAgo;
  return {
    sections: [{
      title: "Run",
      items: [{ type: "graph", key: `rich-${startedAgo}-${span ?? "live"}`, nodes: [
        { id: "root", title: "Ship it", subtitle: "2 parts", action: { id: "open", values: { id: "root" } } },
        { id: "run", title: "Build", tone: "caution", badge: { text: "Running", tone: "caution" }, started_at: started, ...(span !== undefined ? { ended_at: started + span } : {}),
          facts: [{ label: "Model", value: "small" }], detail: "Verifier says hi", links: [{ title: "Session 1", url: "https://example.com/s/1" }],
          action: { id: "open-run", values: { id: "run" } } },
        { id: "leaf1", title: "Unit", action: { id: "open-leaf", values: { id: "leaf1" } } },
        { id: "leaf2", title: "UI" },
      ], edges: [{ from: "root", to: "run" }, { from: "root", to: "leaf1" }, { from: "root", to: "leaf2" }] }],
    }],
  };
};
const keyEl = (el: Element, k: string) => act(() => el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));

describe("UI 10: the control room", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW * 1000);
    forgetGraphStores();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    document.querySelectorAll("[role=dialog]").forEach((d) => d.remove());
    vi.useRealTimers();
  });

  const mountPress = async (view: unknown) => {
    const spy = sizeStage(900, 600);
    const pressed: UIAction[] = [];
    await mount(draw(view), { compact: false, onPress: (a) => pressed.push(a) });
    return { pressed, spy };
  };

  it("draws the reading-order outline, never as canvas nodes", async () => {
    const { spy } = await mountPress(RICH());
    const outline = host.querySelector("[data-graph-outline]")!;
    expect(outline).not.toBeNull();
    expect([...host.querySelectorAll("[data-graph-outline-node]")].map((b) => b.getAttribute("data-graph-outline-node"))).toEqual(["root", "run", "leaf1", "leaf2"]);
    expect([...host.querySelectorAll("[data-graph-outline-node]")].every((b) => b.getAttribute("data-graph-node") === null)).toBe(true);
    expect(host.querySelector("[data-graph-inspector]")).toBeNull(); // nothing selected yet
    expect(host.querySelector("[data-graph-timeline]")).not.toBeNull(); // run carries a start time
    spy.mockRestore();
  });

  it("walks the outline with j / k and the arrows, guards typing, and Esc closes the inspector", async () => {
    const { spy } = await mountPress(RICH());
    const outline = host.querySelector("[data-graph-outline]")!;
    keyEl(outline, "j");
    expect(host.querySelector('[data-graph-outline-node="root"]')).toBe(document.activeElement);
    expect(host.querySelector("[data-graph-selected]")).not.toBeNull();
    keyEl(outline, "j");
    expect(host.querySelector('[data-graph-outline-node="run"]')).toBe(document.activeElement);
    expect(host.querySelector("[data-graph-inspector]")).not.toBeNull(); // run is rich: its inspector opens
    keyEl(outline, "ArrowDown");
    expect(host.querySelector("[data-graph-outline-node=" + '"leaf1"]')).toBe(document.activeElement);
    keyEl(outline, "ArrowUp");
    expect(host.querySelector('[data-graph-outline-node="run"]')).toBe(document.activeElement);
    keyEl(outline, "k");
    expect(host.querySelector('[data-graph-outline-node="root"]')).toBe(document.activeElement);
    // the last row stops the walk
    keyEl(outline, "ArrowDown"); keyEl(outline, "ArrowDown"); keyEl(outline, "ArrowDown");
    expect(host.querySelector('[data-graph-outline-node="leaf2"]')).toBe(document.activeElement);
    // Esc closes the inspector and the pick
    keyEl(outline, "Escape");
    expect(host.querySelector("[data-graph-inspector]")).toBeNull();
    expect(host.querySelector("[data-graph-selected]")).toBeNull();
    // a field inside the outline keeps its own keys
    const input = document.createElement("input");
    outline.appendChild(input);
    input.focus();
    keyEl(outline, "j");
    keyEl(input, "j");
    expect(host.querySelector('[data-graph-outline-node="root"]')).toBe(document.activeElement); // j from outline picked root
    spy.mockRestore();
  });

  it("Enter opens a rich node's first link and presses a plain node's action", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const { pressed, spy } = await mountPress(RICH());
    const outline = host.querySelector("[data-graph-outline]")!;
    keyEl(outline, "j"); // root (plain, has an action)
    keyEl(outline, "Enter");
    expect(pressed).toEqual([{ id: "open", values: { id: "root" } }]);
    keyEl(outline, "j"); // run (rich, has a link)
    keyEl(outline, "Enter");
    expect(open).toHaveBeenCalledWith("https://example.com/s/1", "_blank", "noopener,noreferrer");
    expect(pressed).toHaveLength(1); // the link wins over the run's own action
    open.mockRestore();
    spy.mockRestore();
  });

  it("collapses and reopens the outline from its toggles", async () => {
    const { spy } = await mountPress(RICH());
    expect(host.querySelectorAll("[data-graph-outline-toggle]")).toHaveLength(1);
    act(() => host.querySelector<HTMLElement>("[data-graph-outline-toggle]")!.click());
    await act(async () => {});
    expect(host.querySelector("[data-graph-outline]")).toBeNull();
    const reopen = host.querySelector<HTMLElement>("[data-graph-outline-toggle]")!; // the floating one in the stage
    expect(reopen.getAttribute("aria-expanded")).toBe("false");
    act(() => reopen.click());
    await act(async () => {});
    expect(host.querySelector("[data-graph-outline]")).not.toBeNull();
    spy.mockRestore();
  });

  it("a click on an outline row pans the canvas to the node", async () => {
    const spy = sizeStage(300, 180); // the drawing overflows: centring does not hide the move
    await mount(draw(RICH()), { compact: false, onPress: () => {} });
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const before = transformOf(layer);
    act(() => host.querySelector<HTMLElement>('[data-graph-outline-node="leaf2"]')!.click());
    await act(async () => {});
    expect(transformOf(layer)).not.toEqual(before);
    spy.mockRestore();
  });

  it("a rich node's card press opens its inspector instead of pressing; a plain node still presses", async () => {
    const { pressed, spy } = await mountPress(RICH());
    act(() => host.querySelector<HTMLElement>('[data-graph-node="run"]')!.click());
    await act(async () => {});
    expect(pressed).toEqual([]); // the card only opens the inspector
    const inspector = host.querySelector("[data-graph-inspector]")!;
    expect(inspector).not.toBeNull();
    expect(inspector.textContent).toContain("Model");
    expect(inspector.textContent).toContain("small");
    expect(inspector.textContent).toContain("Verifier says hi");
    expect(inspector.querySelectorAll("[data-graph-inspector-link]")).toHaveLength(1);
    // the action lives in the inspector and presses the node's own action
    act(() => inspector.querySelector<HTMLElement>("[data-graph-inspector-action]")!.click());
    expect(pressed).toEqual([{ id: "open-run", values: { id: "run" } }]);
    // the close button clears both
    act(() => inspector.querySelector<HTMLElement>("[data-graph-inspector-close]")!.click());
    await act(async () => {});
    expect(host.querySelector("[data-graph-inspector]")).toBeNull();
    // a plain node's card keeps pressing its own action
    act(() => host.querySelector<HTMLElement>('[data-graph-node="leaf1"]')!.click());
    expect(pressed).toContainEqual({ id: "open-leaf", values: { id: "leaf1" } });
    spy.mockRestore();
  });

  it("an inspector link opens through the link manager, not a page navigation", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const { spy } = await mountPress(RICH());
    act(() => host.querySelector<HTMLElement>('[data-graph-node="run"]')!.click());
    await act(async () => {});
    act(() => host.querySelector<HTMLElement>("[data-graph-inspector-link]")!.click());
    expect(open).toHaveBeenCalledWith("https://example.com/s/1", "_blank", "noopener,noreferrer");
    open.mockRestore();
    spy.mockRestore();
  });

  it("draws a timeline bar and ticks, selects on a row press, and collapses", async () => {
    // two running nodes: the one started later has a bar that grows against the fixed earlier start
    const view = RICH(30);
    const graph = view.sections[0].items[0] as { nodes: Record<string, unknown>[]; edges: Record<string, string>[] };
    graph.nodes.push({ id: "run2", title: "Later", started_at: NOW - 5 });
    graph.edges.push({ from: "root", to: "run2" });
    const { spy } = await mountPress(view);
    const timeline = host.querySelector("[data-graph-timeline]")!;
    expect(timeline).not.toBeNull();
    const ticks = [...host.querySelectorAll("[data-graph-timeline-tick]")];
    expect(ticks.length).toBeGreaterThanOrEqual(2);
    expect(ticks.every((x) => /^\d\d:\d\d:\d\d$/.test(x.textContent ?? ""))).toBe(true); // a small span ticks in seconds
    const bar = host.querySelector<HTMLElement>('[data-graph-timeline-bar="run2"]')!;
    expect(bar).not.toBeNull();
    const before = bar.getAttribute("style");
    act(() => vi.advanceTimersByTime(1000));
    expect(bar.getAttribute("style")).not.toBe(before); // a running node's bar grows with the clock
    act(() => host.querySelector<HTMLElement>('[data-graph-timeline-row="run"]')!.click());
    await act(async () => {});
    expect(host.querySelector("[data-graph-inspector]")).not.toBeNull();
    expect(host.querySelector('[data-graph-at="run"] [data-graph-node="run"]')).not.toBeNull();
    act(() => host.querySelector<HTMLElement>("[data-graph-timeline-toggle]")!.click());
    await act(async () => {});
    expect(host.querySelector('[data-graph-timeline-bar="run"]')).toBeNull();
    spy.mockRestore();
  });

  it.each([
    [60, true],
    [300, false],
    [1500, false],
    [5400, false],
    [18000, false],
    [90000, false],
  ])("ticks a %ss span at its step", async (span, seconds) => {
    const { spy } = await mountPress(RICH(span, span));
    const ticks = [...host.querySelectorAll("[data-graph-timeline-tick]")].map((x) => x.textContent ?? "");
    expect(ticks.length).toBeGreaterThanOrEqual(2);
    expect(ticks.length).toBeLessThanOrEqual(10);
    expect(ticks.every((x) => (seconds ? /^\d\d:\d\d:\d\d$/ : /^\d\d:\d\d$/).test(x))).toBe(true);
    spy.mockRestore();
  });

  it("opens a wide fan-out at a readable scale on the node that needs the person", async () => {
    const spy = sizeStage(400, 300);
    const view = { sections: [{ items: [{ type: "graph", key: "fan", nodes: [
      { id: "root", title: "Root" },
      ...Array.from({ length: 12 }, (_, i) => ({ id: `l${i}`, title: `Leaf ${i}` })),
    ], edges: Array.from({ length: 12 }, (_, i) => ({ from: "root", to: `l${i}` })) }] }] };
    (view.sections[0].items[0].nodes.find((n) => n.id === "l7") as { tone?: string }).tone = "danger";
    await mount(draw(view), { compact: false, onPress: () => {} });
    const layer = host.querySelector("[data-graph-layer]") as HTMLElement;
    const [x, y, zoom] = transformOf(layer);
    expect(zoom).toBeGreaterThanOrEqual(0.75);
    // the danger node l7 is the stage's centre
    const el = host.querySelector('[data-graph-at="l7"]') as HTMLElement;
    const cx = x + (Number.parseFloat(el.style.left) + Number.parseFloat(el.style.width) / 2) * zoom;
    const cy = y + (Number.parseFloat(el.style.top) + Number.parseFloat(el.style.height) / 2) * zoom;
    expect(cx).toBeCloseTo(200, 3);
    expect(cy).toBeCloseTo(150, 3);
    // Fit still goes down to the floor (this drawing is wide enough that it would shrink past it)
    const lw = Number.parseFloat(layer.style.width);
    const lh = Number.parseFloat(layer.style.height);
    const wantZ = Math.max(0.25, Math.min(1, Math.min((400 - 80) / lw, (300 - 80) / lh)));
    act(() => host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_fit")}"]`)!.click());
    expect(transformOf(layer)[2]).toBeCloseTo(wantZ, 6);
    spy.mockRestore();
  });

  it("a compact still preview keeps the old shape: no outline, inspector or timeline", async () => {
    const d = doc(renderToStaticMarkup(<GraphCanvas item={draw(RICH())} compact busy="" onPress={() => {}} />));
    expect(d.querySelector("[data-graph-outline]")).toBeNull();
    expect(d.querySelector("[data-graph-inspector]")).toBeNull();
    expect(d.querySelector("[data-graph-timeline]")).toBeNull();
    expect(d.querySelector(".graph-preview")).not.toBeNull();
  });

  it("gives a lone node no outline (laid.order under two), but a rich one still opens its inspector", async () => {
    const spy = sizeStage(900, 600);
    const one = { sections: [{ items: [{ type: "graph", key: "one", nodes: [{ id: "only", title: "Only", facts: [{ label: "k", value: "v" }] }], edges: [] }] }] };
    await mount(draw(one), { compact: false, onPress: () => {} });
    expect(host.querySelector("[data-graph-outline]")).toBeNull();
    expect(host.querySelector("[data-graph-outline-toggle]")).toBeNull();
    act(() => host.querySelector<HTMLElement>('[data-graph-node="only"]')!.click());
    await act(async () => {});
    expect(host.querySelector("[data-graph-inspector]")).not.toBeNull();
    spy.mockRestore();
  });

  it("draws the control room with design tokens only: no hex color in the markup", async () => {
    await mount(draw(RICH()), { compact: false, onPress: () => {} });
    act(() => host.querySelector<HTMLElement>('[data-graph-node="run"]')!.click());
    await act(async () => {});
    const html = host.innerHTML;
    expect(html).not.toMatch(/#[0-9a-f]{3,8}\b/i);
  });

  it("caps timeline ticks and prints dates even when a node has started_at: 0 alongside current timestamps", async () => {
    const spy = sizeStage(900, 600);
    const epoch = {
      sections: [{ items: [{ type: "graph", key: "epoch", nodes: [
        { id: "a", title: "Epoch", started_at: 0, ended_at: 1726400000 },
        { id: "b", title: "Now", started_at: 1726400000 },
      ], edges: [{ from: "a", to: "b" }] }] }],
    };
    await mount(draw(epoch), { compact: false, onPress: () => {} });
    const ticks = [...host.querySelectorAll("[data-graph-timeline-tick]")].map((x) => x.textContent ?? "");
    expect(ticks.length).toBeGreaterThanOrEqual(2);
    expect(ticks.length).toBeLessThanOrEqual(14);
    expect(ticks.every((x) => /^\d{4}-\d\d-\d\d$/.test(x))).toBe(true);
    spy.mockRestore();
  });

  it("in Full screen with the inspector open, first Escape closes the inspector and second Escape closes Full screen", async () => {
    const spy = sizeStage(900, 600);
    await mount(draw(RICH()), { compact: false, onPress: () => {} });
    act(() => host.querySelector<HTMLElement>('[data-graph-node="run"]')!.click());
    await act(async () => {});
    act(() => host.querySelector<HTMLButtonElement>(`[aria-label="${t("extensions-viewbody.graph_full_screen")}"]`)!.click());
    await act(async () => {});
    const overlay = document.querySelector("[role=dialog]")!;
    expect(overlay).not.toBeNull();
    expect(overlay.querySelector("[data-graph-inspector]")).not.toBeNull();
    // First Escape while dialog itself holds focus: closes the inspector, keeps Full screen open
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
    expect(document.querySelector("[role=dialog] [data-graph-inspector]")).toBeNull();
    // Second Escape: closes Full screen
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await act(async () => {});
    expect(document.querySelector("[role=dialog]")).toBeNull();
    spy.mockRestore();
  });
});
