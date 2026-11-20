# Graph layout fixtures

An extension view's `graph` component (UI version 10, #9727; `docs/extensions/extensions.md`, "UI version 10") is
laid out by each app itself, and an older app gets its nodes as indented rows from the hub. These cases hold every copy
of the rules to one definition. `scripts/gen/graph_layout.py` is the reference: it states the rules exactly and writes
`cases.json` (`python3 scripts/gen/graph_layout.py --check` fails when the file is stale).

| Reader | What it checks |
| --- | --- |
| web `web/src/lib/graph-layout.test.ts` | everything |
| iOS `apple/PersonaTests/ExtensionGraphLayoutTests.swift` | everything |
| Android `ExtensionGraphLayoutTest.kt` in `android/feature/settings` | everything |
| hub `TestGraphReadingOrderFollowsTheSharedCases` in `server/internal/extension/ui_v10_test.go` | `order`, `depth`, `folds` |

## Structure

`{"version": 1, "cases": [{"name", "graph", "expected"}]}`. `graph` holds only what the layout reads: `nodes` (`id`,
`group`), `edges`, `layout`, `direction` and `folded` (the fold set to lay out with). `expected`:

- `at`: each visible node's place `[x, y]` in **slots**: a client multiplies by its own node pitch (the node's size
  plus the gap) on each axis. Hidden nodes are absent.
- `size`: `[width, height]` in slots: the largest x and y plus one.
- `edges`: the edges drawn, `[from, to]`, in the view's order: both ends visible and `from` not folded.
- `order`: the reading order (what VoiceOver, TalkBack and the keyboard walk, and an older app's rows).
- `depth`: each visible node's depth in the reading order (an older app's indent).
- `folds`: every visible node with children: `0`, or when folded how many nodes it hides.
- `frames`: each group's frame: `group` (its title) and `members`, in order, next to each other.

Numbers are multiples of 1/2^k, so every language gets them exactly; compare with a tolerance of 1e-9 all the same.

## The rules

1. **Structure.** A node's children are the ends of its edges in the edges' order; its parents likewise. Roots are the
   nodes without parents, in node order. The hub has already refused repeated ids, unknown ends, a node joined to
   itself, a repeated edge, a cycle and (layout `tree`) a second parent.
2. **Visible.** A root is visible; any other node is visible when one of its parents is visible and not folded. A
   folded node keeps its fold control and hides what only it leads to.
3. **Groups pulled together.** In a list of neighbours (a node's children, the roots, a layer), the first member of a
   group pulls the others of the same group up beside it, in their order; everything else keeps its place. In a tree
   a group is its title among one node's children; in `layered` it is its title among nodes with the same parents.
   Only equality of ids and titles is used, never their sort order.
4. **Tree** (`layout` absent or `tree`). Walk the roots (grouped) and, depth first, each node's visible children
   (grouped). A node with no visible child takes the next free slot; a parent sits halfway between its first and last
   child. Each root after the first starts half a slot further on; within a list of neighbours, a quarter slot more
   wherever the group changes and either side has one. Its depth is its distance from its root.
5. **Layered.** A node's layer is one past its furthest visible, unfolded parent (roots are layer 0). Each layer starts
   in node order; then one sweep down (layers 1 to the last, each sorted by the mean place of its parents in their
   layers) and one up (the last but one to 0, each by the mean place of its children; a node without children keeps
   its own place as its key). Sorting is stable and compares the means as fractions (`a/b < c/d` by `a·d < c·b`).
   Then groups are pulled together in each layer, and the slots are the places plus a quarter slot where the group
   changes; each layer is centred on the widest.
6. **Direction.** `right` (the default): x is the depth or layer, y the slot. `down`: x is the slot, y the depth or
   layer.
7. **Reading order.** Depth first from the roots (grouped), each node's visible children in edge order (grouped by
   title), each node once, at its first visit; its depth there. The same for both layouts.

All graphs are made up.

Run: `python3 scripts/gen/graph_layout.py --check`, then each reader's test.
