// UI version 10's `graph` layout (docs/extensions/extensions.md "UI version 10", #9727): a pure copy of the shared
// rules (spec/fixtures/graph-layout/README.md; scripts/gen/graph_layout.py is the reference and writes the cases this
// is checked against, web/src/lib/graph-layout.test.ts). A layout is in slots — a node and its gap — so every client
// agrees on the shape; each multiplies by its own node pitch. Only equality of ids and group titles is used, never
// their order, and every number is a multiple of 1/2^k, so the copies agree to the bit in any language.
// What a graph looks like when drawn is views/extensions/ViewBody10.tsx.

/** Only what the layout reads of a graph (the view's `graph`, or the cases' stripped copy of one). */
export interface LayoutGraph {
  nodes: { id: string; group?: string }[];
  edges?: { from: string; to: string }[];
  layout?: "tree" | "layered" | string;
  direction?: "right" | "down" | string;
  folded?: string[];
}

/** A laid-out graph: `at` in slots (a client multiplies by its own node pitch on each axis), hidden nodes absent. */
export interface GraphLayout {
  at: Record<string, [number, number]>;
  size: [number, number];
  /** The edges drawn, both ends visible and `from` not folded, in the view's order. */
  edges: [string, string][];
  /** The reading order: what the keyboard and a screen reader walk, and an older app's rows. */
  order: string[];
  /** Each visible node's depth in the reading order (an older app's indent). */
  depth: Record<string, number>;
  /** Every visible node with children: how many nodes it hides, when folded (0 open). */
  folds: Record<string, number>;
  /** Each group's frame: its title and its members, which the layout has put next to each other. */
  frames: { group: string; members: string[] }[];
}

const ROOT_GAP = 0.5; // between two trees (roots) of a tree layout
const GROUP_GAP = 0.25; // between neighbours of different groups, where either has one

interface Structure {
  ids: string[];
  index: Record<string, number>;
  children: Record<string, string[]>;
  parents: Record<string, string[]>;
  group: Record<string, string>;
}

function structure(graph: LayoutGraph): Structure {
  const ids = graph.nodes.map((n) => n.id);
  const index: Record<string, number> = {};
  const children: Record<string, string[]> = {};
  const parents: Record<string, string[]> = {};
  ids.forEach((id, k) => {
    index[id] = k;
    children[id] = [];
    parents[id] = [];
  });
  for (const e of graph.edges ?? []) {
    children[e.from]?.push(e.to);
    parents[e.to]?.push(e.from);
  }
  const group: Record<string, string> = {};
  for (const n of graph.nodes) group[n.id] = n.group ?? "";
  return { ids, index, children, parents, group };
}

/** A node is visible when it is a root, or one of its parents is visible and not folded. */
function visibleNodes(ids: string[], children: Record<string, string[]>, parents: Record<string, string[]>, folded: Set<string>): Set<string> {
  const roots = ids.filter((i) => parents[i].length === 0);
  const seen = new Set(roots);
  const queue = [...roots];
  while (queue.length) {
    const n = queue.shift()!;
    if (folded.has(n)) continue;
    for (const c of children[n]) if (!seen.has(c)) (seen.add(c), queue.push(c));
  }
  return seen;
}

/**
 * The first member of each group pulls the others of its group up beside it, in their order; everything else keeps its
 * place (a None key is no group: never pulled). Stable.
 */
function regroup<T>(items: T[], key: (t: T) => unknown): T[] {
  const out: T[] = [];
  const placed = new Set<unknown>();
  for (const i of items) {
    const k = key(i);
    if (k === null || k === undefined) out.push(i);
    else if (!placed.has(k)) {
      placed.add(k);
      for (const j of items) if (key(j) === k) out.push(j);
    }
  }
  return out;
}

/** How many nodes under n are hidden (each counted once): what a folded node's fold control says. */
function hiddenCount(n: string, children: Record<string, string[]>, visible: Set<string>): number {
  const seen = new Set<string>();
  const stack = [...children[n]];
  while (stack.length) {
    const c = stack.pop()!;
    if (seen.has(c)) continue;
    seen.add(c);
    stack.push(...children[c]);
  }
  let out = 0;
  for (const c of seen) if (!visible.has(c)) out += 1;
  return out;
}

/** A node's visible children (none when folded), the groups among them pulled together. */
function kidsOf(n: string, s: Structure, visible: Set<string>, folded: Set<string>): string[] {
  if (folded.has(n)) return [];
  return regroup(
    s.children[n].filter((c) => visible.has(c)),
    (c) => s.group[c] || null,
  );
}

/**
 * The reading order (the shared rule 7): depth first from the roots (grouped), each node's visible children in edge
 * order with the groups pulled together, each node once at its first visit; its depth there is its distance from its
 * root. The same for both layouts.
 */
function readingOrder(roots: string[], s: Structure, visible: Set<string>, folded: Set<string>): { order: string[]; depth: Record<string, number> } {
  const order: string[] = [];
  const depth: Record<string, number> = {};
  const seen = new Set<string>();
  const visit = (n: string, d: number) => {
    if (seen.has(n)) return;
    seen.add(n);
    depth[n] = d;
    order.push(n);
    for (const c of kidsOf(n, s, visible, folded)) visit(c, d + 1);
  };
  for (const r of roots) visit(r, 0);
  return { order, depth };
}

/** A frame for each group: its members, which the layout has put next to each other (a run's neighbours of one group). */
function framesOf(runs: string[][], s: Structure): { group: string; members: string[] }[] {
  const frames: { group: string; members: string[] }[] = [];
  for (const run of runs) {
    let k = 0;
    while (k < run.length) {
      const g = s.group[run[k]];
      let j = k;
      while (j < run.length && s.group[run[j]] === g) j += 1;
      if (g) frames.push({ group: g, members: run.slice(k, j) });
      k = j;
    }
  }
  return frames;
}

/** The tree layout (the shared rule 4): leaves take the next free slot, a parent sits halfway between its first and
 *  last child; each root after the first half a slot further on, a quarter slot where the group changes in a run.
 *  `maxLeafRows` (UI version 10's control room) packs a run of more than that many leaf siblings into a grid of columns
 *  instead of one long row, so a wide fan-out stays readable; without it the rules above are byte for byte the same. */
function treeLayout(roots: string[], s: Structure, visible: Set<string>, folded: Set<string>, maxLeafRows?: number): { slot: Record<string, number>; depth: Record<string, number>; frames: { group: string; members: string[] }[] } {
  const slot: Record<string, number> = {};
  const depth: Record<string, number> = {};
  const runs: string[][] = [];
  const nxt = { at: 0 };
  const place = (n: string, d: number) => {
    depth[n] = d;
    const kids = kidsOf(n, s, visible, folded);
    if (!kids.length) {
      slot[n] = nxt.at;
      nxt.at += 1;
      return;
    }
    runs.push(kids);
    if (maxLeafRows === undefined) {
      for (let k = 0; k < kids.length; k++) {
        const c = kids[k];
        if (k > 0 && s.group[c] !== s.group[kids[k - 1]] && (s.group[c] || s.group[kids[k - 1]])) nxt.at += GROUP_GAP;
        place(c, d + 1);
      }
      slot[n] = (slot[kids[0]] + slot[kids[kids.length - 1]]) / 2;
    } else {
      // Segments: a child with visible, unfolded children alone, or a maximal run of leaf siblings of one group. A
      // wide leaf run (more than maxLeafRows) folds into columns: Math.max(maxLeafRows, ceil(sqrt(L * 2.8))) rows
      // (weighted by the ~3:1 horizontal-to-vertical node pitch so the packed block stays near-square in pixels),
      // filled down each column, its leaves one slot apart in the row and one depth further out per column.
      const isLeaf = (c: string) => kidsOf(c, s, visible, folded).length === 0;
      const segments: { items: string[]; group: string }[] = [];
      for (let k = 0; k < kids.length; ) {
        const c = kids[k];
        if (!isLeaf(c)) {
          segments.push({ items: [c], group: s.group[c] });
          k += 1;
          continue;
        }
        const group = s.group[c];
        const run = [c];
        k += 1;
        while (k < kids.length && isLeaf(kids[k]) && s.group[kids[k]] === group) (run.push(kids[k]), (k += 1));
        segments.push({ items: run, group });
      }
      let previous = "";
      let first = true;
      for (const segment of segments) {
        if (!first && segment.group !== previous && (segment.group || previous)) nxt.at += GROUP_GAP;
        first = false;
        previous = segment.group;
        if (segment.items.length > maxLeafRows) {
          const L = segment.items.length;
          const rows = Math.max(maxLeafRows, Math.ceil(Math.sqrt(L * 2.8)));
          const base = nxt.at;
          segment.items.forEach((leaf, idx) => {
            depth[leaf] = d + 1 + Math.floor(idx / rows);
            slot[leaf] = base + (idx % rows);
          });
          nxt.at = base + Math.min(L, rows);
        } else {
          for (const c of segment.items) place(c, d + 1);
        }
      }
      let minSlot = Infinity;
      let maxSlot = -Infinity;
      for (const c of kids) {
        if (slot[c] < minSlot) minSlot = slot[c];
        if (slot[c] > maxSlot) maxSlot = slot[c];
      }
      slot[n] = (minSlot + maxSlot) / 2;
    }
  };
  const rs = regroup(roots, (r) => s.group[r] || null);
  runs.push(rs);
  for (let k = 0; k < rs.length; k++) {
    if (k > 0) nxt.at += ROOT_GAP;
    place(rs[k], 0);
  }
  return { slot, depth, frames: framesOf(runs, s) };
}

/** A sum over integers compared exactly as the fraction a/b: a·d < c·b, never a rounded float. */
function fractionLess(a: number, b: number, c: number, d: number): boolean {
  return a * d < c * b;
}

/** The layered layout (the shared rule 5): a node one layer past its furthest visible, unfolded parent; layers sorted
 *  by the mean place of the parents (one sweep down) then of the children (one up, own place when without), stably
 *  comparing fractions; groups pulled together in each layer; each layer centred on the widest. */
function layeredLayout(ids: string[], s: Structure, visible: Set<string>, folded: Set<string>): { slot: Record<string, number>; depth: Record<string, number>; frames: { group: string; members: string[] }[] } {
  const vis = ids.filter((i) => visible.has(i));
  const vparents: Record<string, string[]> = {};
  const vchildren: Record<string, string[]> = {};
  for (const i of vis) {
    vparents[i] = s.parents[i].filter((p) => visible.has(p) && !folded.has(p));
    vchildren[i] = folded.has(i) ? [] : s.children[i].filter((c) => visible.has(c));
  }
  // Longest path from a root: Kahn's order over the visible edges.
  const layer: Record<string, number> = {};
  const pending: Record<string, number> = {};
  const queue: string[] = [];
  for (const i of vis) {
    pending[i] = vparents[i].length;
    if (pending[i] === 0) queue.push(i);
  }
  while (queue.length) {
    const n = queue.shift()!;
    let best = 0;
    for (const p of vparents[n]) best = Math.max(best, layer[p] + 1);
    layer[n] = best;
    for (const c of vchildren[n]) if (--pending[c] === 0) queue.push(c);
  }
  const count = Math.max(...Object.values(layer)) + 1;
  let layers: string[][] = [];
  for (let L = 0; L < count; L++) layers.push(vis.filter((i) => layer[i] === L)); // node order to start

  const pos = () => {
    const at: Record<string, number> = {};
    for (const row of layers) row.forEach((n, k) => (at[n] = k));
    return at;
  };
  // stable sort by a sum over a count compared as fractions (a/b < c/d by a·d < c·b)
  const sortBy = (row: string[], key: (n: string) => [number, number]) =>
    row
      .map((n, k) => ({ n, k, key: key(n) }))
      .sort((x, y) => {
        const [a, b] = x.key;
        const [c, d] = y.key;
        if (a * d === c * b) return x.k - y.k; // equal fractions: the order they were in
        return fractionLess(a, b, c, d) ? -1 : 1;
      })
      .map((x) => x.n);

  for (let L = 1; L < count; L++) {
    // down: by the mean place of the parents
    const p = pos();
    layers[L] = sortBy(layers[L], (n) => [vparents[n].reduce((sum, m) => sum + p[m], 0), vparents[n].length]);
  }
  for (let L = count - 2; L >= 0; L--) {
    // up: by the mean place of the children; a node without any keeps its own place as its key
    const p = pos();
    layers[L] = sortBy(layers[L], (n) => (vchildren[n].length ? [vchildren[n].reduce((sum, m) => sum + p[m], 0), vchildren[n].length] : [p[n], 1]));
  }

  // A layer's group key: its title among nodes with the same visible, unfolded parents, as one string so two nodes of
  // the same group compare equal whatever their parents' order (the reference compares the tuples by value).
  const gkey = (n: string): string | null => (!s.group[n] ? null : `${vparents[n].map((q) => s.index[q]).sort((a, b) => a - b).join(",")}\0${s.group[n]}`);
  layers = layers.map((row) => regroup(row, gkey));

  const slot: Record<string, number> = {};
  const widths: number[] = [];
  for (const row of layers) {
    // slots for a run of neighbours: one apart, plus GROUP_GAP where the group changes and either has one
    let at = 0;
    const s2: number[] = [];
    row.forEach((n, k) => {
      if (k > 0) at += 1 + (gkey(row[k - 1]) !== gkey(n) && (gkey(row[k - 1]) || gkey(n)) ? GROUP_GAP : 0);
      s2.push(at);
    });
    widths.push(s2[s2.length - 1] + 1);
    row.forEach((n, k) => (slot[n] = s2[k]));
  }
  const widest = Math.max(...widths);
  layers.forEach((row, L) => {
    const off = (widest - widths[L]) / 2;
    for (const n of row) slot[n] += off;
  });
  const frames: { group: string; members: string[] }[] = [];
  for (const row of layers) {
    let k = 0;
    while (k < row.length) {
      const g = gkey(row[k]);
      let j = k;
      while (j < row.length && gkey(row[j]) === g) j += 1;
      if (g) frames.push({ group: s.group[row[k]], members: row.slice(k, j) });
      k = j;
    }
  }
  return { slot, depth: layer, frames };
}

/**
 * What a client draws for a graph with this fold set (the view's `folded` when the argument is omitted). Follows the
 * shared rules exactly; graph-layout.test.ts checks every field against every case in
 * spec/fixtures/graph-layout/cases.json. `opts.maxLeafRows`, given only by the control-room canvas, packs wide leaf
 * runs into columns; without it the layout is unchanged.
 */
export function layOutGraph(graph: LayoutGraph, folded?: string[], opts?: { maxLeafRows?: number }): GraphLayout {
  const s = structure(graph);
  const fold = new Set(folded ?? graph.folded ?? []);
  const visible = visibleNodes(s.ids, s.children, s.parents, fold);
  const roots = s.ids.filter((i) => s.parents[i].length === 0);
  const { order, depth } = readingOrder(regroup(roots, (r) => s.group[r] || null), s, visible, fold);
  const laid = (graph.layout ?? "tree") === "tree" ? treeLayout(roots, s, visible, fold, opts?.maxLeafRows) : layeredLayout(s.ids, s, visible, fold);
  const right = (graph.direction ?? "right") === "right";
  const at: Record<string, [number, number]> = {};
  for (const n of s.ids) {
    if (!visible.has(n)) continue;
    const d = laid.depth[n];
    at[n] = right ? [d, laid.slot[n]] : [laid.slot[n], d];
  }
  let width = 0;
  let height = 0;
  for (const p of Object.values(at)) {
    width = Math.max(width, p[0]);
    height = Math.max(height, p[1]);
  }
  // A node with children has a fold control; folded, it says how many nodes it hides.
  const folds: Record<string, number> = {};
  for (const n of s.ids) if (visible.has(n) && s.children[n].length) folds[n] = fold.has(n) ? hiddenCount(n, s.children, visible) : 0;
  const edges: [string, string][] = (graph.edges ?? []).filter((e) => visible.has(e.from) && visible.has(e.to) && !fold.has(e.from)).map((e) => [e.from, e.to]);
  return { at, edges, size: [width + 1, height + 1], order, depth, folds, frames: laid.frames };
}
