#!/usr/bin/env python3
"""The reference layout of an extension view's `graph` component (UI version 10, #9727), and its shared fixtures.

    python3 scripts/gen/graph_layout.py           write spec/fixtures/graph-layout/cases.json
    python3 scripts/gen/graph_layout.py --check   exit 1 when the file differs from what this writes

Every client lays a graph out with its own copy of these rules (web `web/src/lib/graph-layout.ts`, iOS
`ExtensionGraphLayout.swift`, Android `ExtensionGraphLayout.kt`) and the hub reads the reading order and depths from
them for older apps (`server/internal/extension/ui_v10.go`); each checks itself against the cases this writes. The
rules are in spec/fixtures/graph-layout/README.md; this file is the one that says exactly what they mean.

Only equality of strings is used, never their order, and every number is a multiple of 1/2^k, so the copies agree
to the bit in any language. Standard library only.
"""

from __future__ import annotations

import argparse
import json
import sys
from fractions import Fraction
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "spec" / "fixtures" / "graph-layout" / "cases.json"

ROOT_GAP = 0.5  # between two trees (roots) of a tree layout
GROUP_GAP = 0.25  # between neighbours of different groups, where either has one


def structure(graph):
    nodes = graph["nodes"]
    ids = [n["id"] for n in nodes]
    index = {i: k for k, i in enumerate(ids)}
    children = {i: [] for i in ids}
    parents = {i: [] for i in ids}
    for e in graph.get("edges") or []:
        children[e["from"]].append(e["to"])
        parents[e["to"]].append(e["from"])
    group = {n["id"]: n.get("group") or "" for n in nodes}
    return ids, index, children, parents, group


def visible_nodes(ids, children, parents, folded):
    """A node is visible when it is a root, or one of its parents is visible and not folded."""
    roots = [i for i in ids if not parents[i]]
    seen = set(roots)
    queue = list(roots)
    while queue:
        n = queue.pop(0)
        if n in folded:
            continue
        for c in children[n]:
            if c not in seen:
                seen.add(c)
                queue.append(c)
    return seen


def regroup(items, key):
    """Stable: the first member of each group pulls the others of its group up beside it, in their order."""
    out, placed = [], set()
    for i in items:
        k = key(i)
        if k is None:
            out.append(i)
        elif k not in placed:
            placed.add(k)
            out.extend(j for j in items if key(j) == k)
    return out


def hidden_count(n, children, visible):
    """How many nodes under n are hidden (each counted once), shown on a folded node's fold control."""
    seen, stack = set(), list(children[n])
    while stack:
        c = stack.pop()
        if c in seen:
            continue
        seen.add(c)
        stack.extend(children[c])
    return sum(1 for c in seen if c not in visible)


def kids_of(n, children, visible, folded, group):
    if n in folded:
        return []
    kids = [c for c in children[n] if c in visible]
    return regroup(kids, lambda c: group[c] or None)


def reading_order(roots, children, visible, folded, group):
    """Depth-first, parents before children, children in edge order with groups pulled together; each node once, at
    its first visit. Its depth there is how far an older app's row is indented."""
    order, depth = [], {}

    def visit(n, d):
        if n in depth:
            return
        depth[n] = d
        order.append(n)
        for c in kids_of(n, children, visible, folded, group):
            visit(c, d + 1)

    for r in roots:
        visit(r, 0)
    return order, depth


def frames_of(runs, group):
    """A frame for each group: its members, which the layout has put next to each other."""
    frames = []
    for run in runs:
        k = 0
        while k < len(run):
            g = group[run[k]]
            j = k
            while j < len(run) and group[run[j]] == g:
                j += 1
            if g:
                frames.append({"group": g, "members": run[k:j]})
            k = j
    return frames


def spaced(run, start, gap_between):
    """Slots for a run of neighbours: one apart, plus GROUP_GAP where the group changes and either has one."""
    slots, at = [], start
    for k, n in enumerate(run):
        if k > 0:
            at += 1 + gap_between(run[k - 1], n)
        slots.append(at)
    return slots


def tree_layout(roots, children, visible, folded, group):
    slot, depth = {}, {}
    runs = []
    nxt = [0.0]

    def place(n, d):
        depth[n] = d
        kids = kids_of(n, children, visible, folded, group)
        if not kids:
            slot[n] = nxt[0]
            nxt[0] += 1
            return
        runs.append(kids)
        for k, c in enumerate(kids):
            if k > 0 and group[c] != group[kids[k - 1]] and (group[c] or group[kids[k - 1]]):
                nxt[0] += GROUP_GAP
            place(c, d + 1)
        slot[n] = (slot[kids[0]] + slot[kids[-1]]) / 2

    rs = regroup(roots, lambda r: group[r] or None)
    runs.append(rs)
    for k, r in enumerate(rs):
        if k > 0:
            nxt[0] += ROOT_GAP
        place(r, 0)
    return slot, depth, frames_of(runs, group)


def layered_layout(ids, index, children, parents, visible, folded, group):
    vis = [i for i in ids if i in visible]
    vparents = {i: [p for p in parents[i] if p in visible and p not in folded] for i in vis}
    vchildren = {i: ([] if i in folded else [c for c in children[i] if c in visible]) for i in vis}
    # Longest path from a root: Kahn's order over the visible edges.
    layer = {}
    pending = {i: len(vparents[i]) for i in vis}
    queue = [i for i in vis if pending[i] == 0]
    while queue:
        n = queue.pop(0)
        layer[n] = max((layer[p] + 1 for p in vparents[n]), default=0)
        for c in vchildren[n]:
            pending[c] -= 1
            if pending[c] == 0:
                queue.append(c)
    count = max(layer.values()) + 1
    layers = [[i for i in vis if layer[i] == L] for L in range(count)]  # node order to start

    def pos():
        return {n: k for row in layers for k, n in enumerate(row)}

    def bary(ns, p):
        return (sum(p[m] for m in ns), len(ns))

    def sort_by(row, key):
        # stable sort on fractions sum/count compared by cross-multiplying
        return sorted(row, key=lambda n: Fraction(*key(n)))

    for L in range(1, count):  # down: by the mean place of the parents
        p = pos()
        layers[L] = sort_by(layers[L], lambda n: bary(vparents[n], p))
    for L in range(count - 2, -1, -1):  # up: by the mean place of the children; a node without any keeps its own
        p = pos()
        layers[L] = sort_by(layers[L], lambda n: bary(vchildren[n], p) if vchildren[n] else (p[n], 1))

    def gkey(n):
        if not group[n]:
            return None
        return (tuple(sorted(index[q] for q in vparents[n])), group[n])

    layers = [regroup(row, gkey) for row in layers]

    def gap(a, b):
        return GROUP_GAP if gkey(a) != gkey(b) and (gkey(a) or gkey(b)) else 0

    slot, widths = {}, []
    for row in layers:
        s = spaced(row, 0.0, gap)
        widths.append(s[-1] + 1)
        for n, v in zip(row, s):
            slot[n] = v
    widest = max(widths)
    for L, row in enumerate(layers):
        off = (widest - widths[L]) / 2
        for n in row:
            slot[n] += off
    frames = []
    for row in layers:
        k = 0
        while k < len(row):
            g = gkey(row[k])
            j = k
            while j < len(row) and gkey(row[j]) == g:
                j += 1
            if g:
                frames.append({"group": g[1], "members": row[k:j]})
            k = j
    return slot, layer, frames


def lay_out(graph, folded=None):
    """What a client draws for graph with this fold set (the view's `folded` when None)."""
    ids, index, children, parents, group = structure(graph)
    folded = set(graph.get("folded") or []) if folded is None else set(folded)
    visible = visible_nodes(ids, children, parents, folded)
    roots = [i for i in ids if not parents[i]]
    order, rdepth = reading_order(regroup(roots, lambda r: group[r] or None), children, visible, folded, group)
    if (graph.get("layout") or "tree") == "tree":
        slot, depth, frames = tree_layout(roots, children, visible, folded, group)
    else:
        slot, depth, frames = layered_layout(ids, index, children, parents, visible, folded, group)
    right = (graph.get("direction") or "right") == "right"
    at = {}
    for n in ids:
        if n in visible:
            at[n] = [depth[n], slot[n]] if right else [slot[n], depth[n]]
    width = max(x for x, _ in at.values()) + 1
    height = max(y for _, y in at.values()) + 1
    # A node with children has a fold control; folded, it says how many nodes it hides.
    folds = {n: (hidden_count(n, children, visible) if n in folded else 0) for n in ids if n in visible and children[n]}
    drawn = [[x["from"], x["to"]] for x in graph.get("edges") or []
             if x["from"] in visible and x["to"] in visible and x["from"] not in folded]
    return {
        "at": at,
        "edges": drawn,
        "size": [width, height],
        "order": order,
        "depth": rdepth,
        "folds": folds,
        "frames": frames,
    }


# ---------------------------------------------------------------------------------------------------------------------
# Cases. Every graph is valid by the hub's checks (unique ids, known ends, no cycle, at most one parent in a tree).


def n(i, **kw):
    return {"id": i, "title": i, **kw}


def e(a, b):
    return {"from": a, "to": b}


def big_tree():
    """200 nodes: a root, 8 parts, each with 6 steps (the first two a group: one task tried two ways), some steps with
    up to 4 checks; made up, but shaped like a wide fan-out of work."""
    nodes, edges = [n("root")], []

    def add(node, parent):
        if len(nodes) < 200:
            nodes.append(node)
            edges.append(e(parent, node["id"]))

    for p in range(8):
        pid = f"p{p}"
        add(n(pid), "root")
        for s in range(6):
            sid = f"{pid}.s{s}"
            add(n(sid, group="ways") if s < 2 else n(sid), pid)
            for c in range((p + s) % 5):
                add(n(f"{sid}.c{c}"), sid)
    k = 0
    while len(nodes) < 200:  # what is left goes under the parts' last steps, so the count is exactly 200
        add(n(f"p{k % 8}.s5.x{k // 8}"), f"p{k % 8}.s5")
        k += 1
    return {"nodes": nodes, "edges": edges}


CASES = [
    ("one node", {"nodes": [n("a")]}),
    ("a root and three children, the middle one with two", {
        "nodes": [n("r"), n("a"), n("b"), n("c"), n("b1"), n("b2")],
        "edges": [e("r", "a"), e("r", "b"), e("r", "c"), e("b", "b1"), e("b", "b2")]}),
    ("the same, downwards", {
        "direction": "down",
        "nodes": [n("r"), n("a"), n("b"), n("c"), n("b1"), n("b2")],
        "edges": [e("r", "a"), e("r", "b"), e("r", "c"), e("b", "b1"), e("b", "b2")]}),
    ("children follow the edges' order, not the nodes'", {
        "nodes": [n("r"), n("a"), n("b"), n("c")],
        "edges": [e("r", "c"), e("r", "a"), e("r", "b")]}),
    ("three roots, half a slot apart", {
        "nodes": [n("a"), n("b"), n("b1"), n("c")],
        "edges": [e("b", "b1")]}),
    ("a folded node hides everything under it and counts it", {
        "folded": ["b"],
        "nodes": [n("r"), n("a"), n("b"), n("b1"), n("b2"), n("b21")],
        "edges": [e("r", "a"), e("r", "b"), e("b", "b1"), e("b", "b2"), e("b2", "b21")]}),
    ("a folded leaf has nothing to hide", {
        "folded": ["a"],
        "nodes": [n("r"), n("a")],
        "edges": [e("r", "a")]}),
    ("a group's members are pulled together, framed, and set apart from their neighbours", {
        "nodes": [n("r"), n("x", group="Two ways"), n("plain"), n("y", group="Two ways"), n("z", group="Other")],
        "edges": [e("r", "x"), e("r", "plain"), e("r", "y"), e("r", "z")]}),
    ("the same group under two parents is two frames", {
        "nodes": [n("r"), n("a"), n("b"), n("a1", group="ways"), n("a2", group="ways"), n("b1", group="ways"),
                  n("b2", group="ways")],
        "edges": [e("r", "a"), e("r", "b"), e("a", "a1"), e("a", "a2"), e("b", "b1"), e("b", "b2")]}),
    ("grouped roots", {
        "nodes": [n("a", group="g"), n("b"), n("c", group="g")]}),
    ("layered: a diamond", {
        "layout": "layered",
        "nodes": [n("a"), n("b"), n("c"), n("d")],
        "edges": [e("a", "b"), e("a", "c"), e("b", "d"), e("c", "d")]}),
    ("layered: a node sits one past its furthest parent", {
        "layout": "layered",
        "nodes": [n("a"), n("b"), n("c"), n("d")],
        "edges": [e("a", "b"), e("b", "c"), e("a", "d"), e("c", "d")]}),
    ("layered: the sweeps uncross edges", {
        "layout": "layered",
        "nodes": [n("a"), n("b"), n("c"), n("x"), n("y"), n("z")],
        "edges": [e("a", "z"), e("b", "y"), e("c", "x")]}),
    ("layered: a node with a folded parent stays while another parent shows it", {
        "layout": "layered", "folded": ["a"],
        "nodes": [n("a"), n("b"), n("c"), n("d")],
        "edges": [e("a", "c"), e("b", "c"), e("a", "d")]}),
    ("layered downwards with a group and an unequal layer", {
        "layout": "layered", "direction": "down",
        "nodes": [n("s"), n("p1", group="ways"), n("q"), n("p2", group="ways"), n("j")],
        "edges": [e("s", "p1"), e("s", "q"), e("s", "p2"), e("p1", "j"), e("p2", "j")]}),
    ("the view's example: a release checklist", json.loads(
        (ROOT / "schema/extension/examples/View.v10-work-tree.json").read_text())["sections"][0]["items"][1]),
    ("the view's example: a layered plan", json.loads(
        (ROOT / "schema/extension/examples/View.v10-plan-layered.json").read_text())["sections"][0]["items"][0]),
    ("200 nodes", big_tree()),
    ("200 nodes with two parts folded", {**big_tree(), "folded": ["p1", "p5"]}),
]


def strip(graph):
    """Only what the layout reads, so the cases stay small."""
    keep = {k: graph[k] for k in ("layout", "direction", "folded") if graph.get(k)}
    keep["nodes"] = [{k: v for k, v in x.items() if k in ("id", "group")} for x in graph["nodes"]]
    if graph.get("edges"):
        keep["edges"] = graph["edges"]
    return keep


def build():
    cases = []
    for name, graph in CASES:
        g = strip(graph)
        cases.append({"name": name, "graph": g, "expected": lay_out(g)})
    return {"version": 1, "cases": cases}


def render(doc):
    # One case per line block, numbers as JSON writes them; stable for review.
    return json.dumps(doc, ensure_ascii=False, indent=1) + "\n"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    a = ap.parse_args()
    text = render(build())
    if a.check:
        if not OUT.exists() or OUT.read_text() != text:
            print(f"{OUT.relative_to(ROOT)} is out of date: run python3 scripts/gen/graph_layout.py", file=sys.stderr)
            return 1
        return 0
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(text)
    print(f"wrote {OUT.relative_to(ROOT)} ({len(CASES)} cases)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
