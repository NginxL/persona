package persona.feature.settings

import persona.core.contracts.generated.ExtensionGraphComponent
import persona.core.contracts.generated.ExtensionGraphDirection
import persona.core.contracts.generated.ExtensionGraphEdge
import persona.core.contracts.generated.ExtensionGraphLayout as WireGraphLayout

/** A node's place in slot coordinates; the renderer chooses the pixel pitch on each axis. */
internal data class ExtensionGraphPoint(val x: Double, val y: Double)

/** The drawn canvas' size in slots. */
internal data class ExtensionGraphSize(val width: Double, val height: Double)

/** A titled run of adjacent nodes with the same parents. */
internal data class ExtensionGraphFrame(val group: String, val members: List<String>)

/** Everything the graph renderer and its accessibility order need, without any Android UI types. */
internal data class ExtensionGraphLayoutResult(
    val at: Map<String, ExtensionGraphPoint>,
    val size: ExtensionGraphSize,
    val edges: List<ExtensionGraphEdge>,
    val order: List<String>,
    val depth: Map<String, Int>,
    val folds: Map<String, Int>,
    val frames: List<ExtensionGraphFrame>,
)

/**
 * The shared extension graph layout (UI version 10), matching spec/fixtures/graph-layout/cases.json.
 *
 * All input has already passed the hub's graph validation. The algorithm is iterative, including both depth-first
 * walks, so a valid 300-node chain does not consume 300 stack frames.
 */
internal object ExtensionGraphLayout {
    private const val RootGap = 0.5
    private const val GroupGap = 0.25

    /**
     * Lay out [graph]. A null [folded] uses the view's initial folds; an explicit set, including an empty one, is the
     * renderer's current fold state.
     */
    fun layout(graph: ExtensionGraphComponent, folded: Set<String>? = null): ExtensionGraphLayoutResult {
        val structure = Structure(graph)
        val foldedNodes = (folded ?: graph.folded.orEmpty().toSet())
            .mapNotNullTo(mutableSetOf()) { structure.index[it] }
        val roots = structure.ids.indices.filter { structure.parents[it].isEmpty() }
        val visible = visibleNodes(structure, roots, foldedNodes)
        val reading = readingOrder(structure, roots, visible, foldedNodes)
        val placement = if (graph.layout.isNullOrEmpty() || graph.layout == WireGraphLayout.TREE) {
            treeLayout(structure, roots, visible, foldedNodes)
        } else {
            layeredLayout(structure, visible, foldedNodes)
        }

        val right = graph.direction.isNullOrEmpty() || graph.direction == ExtensionGraphDirection.RIGHT
        val at = linkedMapOf<String, ExtensionGraphPoint>()
        var maxX = Double.NEGATIVE_INFINITY
        var maxY = Double.NEGATIVE_INFINITY
        for (node in structure.ids.indices) {
            if (!visible[node]) continue
            val depth = placement.depth[node].toDouble()
            val slot = placement.slot[node]
            val point = if (right) ExtensionGraphPoint(depth, slot) else ExtensionGraphPoint(slot, depth)
            at[structure.ids[node]] = point
            maxX = maxOf(maxX, point.x)
            maxY = maxOf(maxY, point.y)
        }
        val size = if (at.isEmpty()) {
            ExtensionGraphSize(0.0, 0.0)
        } else {
            ExtensionGraphSize(maxX + 1.0, maxY + 1.0)
        }

        val order = reading.order.map { structure.ids[it] }
        val readingDepth = linkedMapOf<String, Int>()
        for (node in reading.order) readingDepth[structure.ids[node]] = reading.depth.getValue(node)
        val folds = linkedMapOf<String, Int>()
        for (node in structure.ids.indices) {
            if (visible[node] && structure.children[node].isNotEmpty()) {
                folds[structure.ids[node]] = if (node in foldedNodes) {
                    hiddenCount(node, structure, visible)
                } else {
                    0
                }
            }
        }
        val drawn = structure.edges.filter { edge ->
            val from = structure.index.getValue(edge.from)
            val to = structure.index.getValue(edge.to)
            visible[from] && visible[to] && from !in foldedNodes
        }
        return ExtensionGraphLayoutResult(
            at = at,
            size = size,
            edges = drawn,
            order = order,
            depth = readingDepth,
            folds = folds,
            frames = placement.frames,
        )
    }

    private class Structure(graph: ExtensionGraphComponent) {
        val ids = graph.nodes.map { it.id }
        val index = ids.withIndex().associate { (position, id) -> id to position }
        val groups = graph.nodes.map { it.group.orEmpty() }
        val edges = graph.edges.orEmpty()
        val children: List<List<Int>>
        val parents: List<List<Int>>

        init {
            val childLists = List(ids.size) { mutableListOf<Int>() }
            val parentLists = List(ids.size) { mutableListOf<Int>() }
            for (edge in edges) {
                val from = index.getValue(edge.from)
                val to = index.getValue(edge.to)
                childLists[from] += to
                parentLists[to] += from
            }
            children = childLists
            parents = parentLists
        }
    }

    private class Reading(val order: List<Int>, val depth: Map<Int, Int>)

    private class Placement(
        val slot: DoubleArray,
        val depth: IntArray,
        val frames: List<ExtensionGraphFrame>,
    )

    private data class Visit(val node: Int, val depth: Int)

    private data class LayerGroup(val parents: List<Int>, val title: String)

    /** Cross multiplication keeps barycenter comparisons exact instead of rounding a mean to a Double. */
    private data class Fraction(val numerator: Int, val denominator: Int) : Comparable<Fraction> {
        override fun compareTo(other: Fraction): Int =
            (numerator.toLong() * other.denominator).compareTo(other.numerator.toLong() * denominator)
    }

    private sealed interface TreeStep {
        data class Place(val node: Int, val depth: Int) : TreeStep
        data class Finish(val node: Int, val firstChild: Int, val lastChild: Int) : TreeStep
        data class Shift(val slots: Double) : TreeStep
    }

    private fun visibleNodes(structure: Structure, roots: List<Int>, folded: Set<Int>): BooleanArray {
        val visible = BooleanArray(structure.ids.size)
        val queue = ArrayDeque<Int>()
        for (root in roots) {
            visible[root] = true
            queue.addLast(root)
        }
        while (queue.isNotEmpty()) {
            val node = queue.removeFirst()
            if (node in folded) continue
            for (child in structure.children[node]) {
                if (!visible[child]) {
                    visible[child] = true
                    queue.addLast(child)
                }
            }
        }
        return visible
    }

    /** Stable: the first member pulls every later member of its non-empty group beside it, in their original order. */
    private fun <K> regroup(items: List<Int>, key: (Int) -> K?): List<Int> {
        val out = mutableListOf<Int>()
        val placed = mutableSetOf<K>()
        for (item in items) {
            val group = key(item)
            if (group == null) {
                out += item
            } else if (placed.add(group)) {
                for (candidate in items) {
                    if (key(candidate) == group) out += candidate
                }
            }
        }
        return out
    }

    private fun treeGroup(structure: Structure, node: Int): String? =
        structure.groups[node].takeIf { it.isNotEmpty() }

    private fun kidsOf(structure: Structure, node: Int, visible: BooleanArray, folded: Set<Int>): List<Int> {
        if (node in folded) return emptyList()
        return regroup(structure.children[node].filter { visible[it] }) { treeGroup(structure, it) }
    }

    private fun readingOrder(
        structure: Structure,
        roots: List<Int>,
        visible: BooleanArray,
        folded: Set<Int>,
    ): Reading {
        val order = mutableListOf<Int>()
        val depth = linkedMapOf<Int, Int>()
        val stack = ArrayDeque<Visit>()
        val groupedRoots = regroup(roots) { treeGroup(structure, it) }
        for (root in groupedRoots.asReversed()) stack.addLast(Visit(root, 0))
        while (stack.isNotEmpty()) {
            val (node, currentDepth) = stack.removeLast()
            if (node in depth) continue
            depth[node] = currentDepth
            order += node
            val children = kidsOf(structure, node, visible, folded)
            for (child in children.asReversed()) stack.addLast(Visit(child, currentDepth + 1))
        }
        return Reading(order, depth)
    }

    private fun treeLayout(
        structure: Structure,
        roots: List<Int>,
        visible: BooleanArray,
        folded: Set<Int>,
    ): Placement {
        val slot = DoubleArray(structure.ids.size)
        val depth = IntArray(structure.ids.size)
        val runs = mutableListOf<List<Int>>()
        val stack = ArrayDeque<TreeStep>()
        var next = 0.0
        val groupedRoots = regroup(roots) { treeGroup(structure, it) }
        runs += groupedRoots
        for (position in groupedRoots.indices.reversed()) {
            stack.addLast(TreeStep.Place(groupedRoots[position], 0))
            if (position > 0) stack.addLast(TreeStep.Shift(RootGap))
        }
        while (stack.isNotEmpty()) {
            when (val step = stack.removeLast()) {
                is TreeStep.Shift -> next += step.slots
                is TreeStep.Finish -> {
                    slot[step.node] = (slot[step.firstChild] + slot[step.lastChild]) / 2.0
                }
                is TreeStep.Place -> {
                    val node = step.node
                    depth[node] = step.depth
                    val children = kidsOf(structure, node, visible, folded)
                    if (children.isEmpty()) {
                        slot[node] = next
                        next += 1.0
                    } else {
                        runs += children
                        stack.addLast(TreeStep.Finish(node, children.first(), children.last()))
                        for (position in children.indices.reversed()) {
                            val child = children[position]
                            stack.addLast(TreeStep.Place(child, step.depth + 1))
                            if (position > 0 && structure.groups[child] != structure.groups[children[position - 1]] &&
                                (structure.groups[child].isNotEmpty() || structure.groups[children[position - 1]].isNotEmpty())
                            ) {
                                stack.addLast(TreeStep.Shift(GroupGap))
                            }
                        }
                    }
                }
            }
        }
        return Placement(slot, depth, treeFrames(structure, runs))
    }

    private fun treeFrames(structure: Structure, runs: List<List<Int>>): List<ExtensionGraphFrame> {
        val frames = mutableListOf<ExtensionGraphFrame>()
        for (run in runs) {
            var start = 0
            while (start < run.size) {
                val group = structure.groups[run[start]]
                var end = start
                while (end < run.size && structure.groups[run[end]] == group) end++
                if (group.isNotEmpty()) {
                    frames += ExtensionGraphFrame(group, run.subList(start, end).map(structure.ids::get))
                }
                start = end
            }
        }
        return frames
    }

    private fun layeredLayout(structure: Structure, visible: BooleanArray, folded: Set<Int>): Placement {
        val visibleNodes = structure.ids.indices.filter { visible[it] }
        val visibleParents = List(structure.ids.size) { node ->
            structure.parents[node].filter { visible[it] && it !in folded }
        }
        val visibleChildren = List(structure.ids.size) { node ->
            if (node in folded) emptyList() else structure.children[node].filter { visible[it] }
        }

        // Kahn's order gives each node one layer beyond its furthest visible, unfolded parent.
        val depth = IntArray(structure.ids.size) { -1 }
        val pending = IntArray(structure.ids.size) { visibleParents[it].size }
        val queue = ArrayDeque<Int>()
        for (node in visibleNodes) {
            if (pending[node] == 0) queue.addLast(node)
        }
        while (queue.isNotEmpty()) {
            val node = queue.removeFirst()
            depth[node] = visibleParents[node].maxOfOrNull { depth[it] + 1 } ?: 0
            for (child in visibleChildren[node]) {
                pending[child]--
                if (pending[child] == 0) queue.addLast(child)
            }
        }
        val layerCount = (visibleNodes.maxOfOrNull { depth[it] } ?: -1) + 1
        var layers = MutableList(layerCount) { layer ->
            visibleNodes.filter { depth[it] == layer }
        }

        for (layer in 1 until layerCount) {
            val positions = positions(layers, structure.ids.size)
            layers[layer] = layers[layer].sortedWith(Comparator { a, b ->
                mean(visibleParents[a], positions).compareTo(mean(visibleParents[b], positions))
            })
        }
        for (layer in layerCount - 2 downTo 0) {
            val positions = positions(layers, structure.ids.size)
            layers[layer] = layers[layer].sortedWith(Comparator { a, b ->
                meanOrOwnPlace(a, visibleChildren[a], positions).compareTo(
                    meanOrOwnPlace(b, visibleChildren[b], positions),
                )
            })
        }

        fun groupKey(node: Int): LayerGroup? {
            val title = structure.groups[node]
            if (title.isEmpty()) return null
            return LayerGroup(visibleParents[node].sorted(), title)
        }

        layers = layers.map { regroup(it, ::groupKey) }.toMutableList()
        val slot = DoubleArray(structure.ids.size)
        val widths = DoubleArray(layers.size)
        for (layer in layers.indices) {
            val row = layers[layer]
            var place = 0.0
            for (position in row.indices) {
                val node = row[position]
                if (position > 0) {
                    val previous = row[position - 1]
                    val previousGroup = groupKey(previous)
                    val currentGroup = groupKey(node)
                    place += 1.0 + if (previousGroup != currentGroup && (previousGroup != null || currentGroup != null)) {
                        GroupGap
                    } else {
                        0.0
                    }
                }
                slot[node] = place
            }
            if (row.isNotEmpty()) widths[layer] = slot[row.last()] + 1.0
        }
        val widest = widths.maxOrNull() ?: 0.0
        for (layer in layers.indices) {
            val offset = (widest - widths[layer]) / 2.0
            for (node in layers[layer]) slot[node] += offset
        }

        val frames = mutableListOf<ExtensionGraphFrame>()
        for (row in layers) {
            var start = 0
            while (start < row.size) {
                val key = groupKey(row[start])
                var end = start
                while (end < row.size && groupKey(row[end]) == key) end++
                if (key != null) frames += ExtensionGraphFrame(key.title, row.subList(start, end).map(structure.ids::get))
                start = end
            }
        }
        return Placement(slot, depth, frames)
    }

    private fun positions(layers: List<List<Int>>, nodeCount: Int): IntArray {
        val positions = IntArray(nodeCount)
        for (row in layers) {
            for (position in row.indices) positions[row[position]] = position
        }
        return positions
    }

    private fun mean(nodes: List<Int>, positions: IntArray): Fraction {
        var sum = 0
        for (node in nodes) sum += positions[node]
        return Fraction(sum, nodes.size)
    }

    private fun meanOrOwnPlace(node: Int, children: List<Int>, positions: IntArray): Fraction =
        if (children.isEmpty()) Fraction(positions[node], 1) else mean(children, positions)

    private fun hiddenCount(node: Int, structure: Structure, visible: BooleanArray): Int {
        val seen = BooleanArray(structure.ids.size)
        val stack = ArrayDeque<Int>()
        for (child in structure.children[node]) stack.addLast(child)
        while (stack.isNotEmpty()) {
            val current = stack.removeLast()
            if (seen[current]) continue
            seen[current] = true
            for (child in structure.children[current]) stack.addLast(child)
        }
        var count = 0
        for (current in seen.indices) {
            if (seen[current] && !visible[current]) count++
        }
        return count
    }
}
