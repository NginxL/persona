import Foundation
import PersonaWire

/// UI version 10's graph layout, in the slots shared by every client (`spec/fixtures/graph-layout`). The hub has
/// already checked the graph: unique ids, known edge ends, no cycles, and at most one parent per node in a tree.
/// Nothing here depends on a node's title, a string's sort order, SwiftUI, or the screen's geometry.
nonisolated enum ExtensionGraphLayouter {
    struct Point: Equatable, Sendable {
        var x: Double
        var y: Double
    }

    struct Size: Equatable, Sendable {
        var width: Double
        var height: Double
    }

    struct Frame: Equatable, Hashable, Sendable {
        var group: String
        var members: [String]
    }

    struct Result: Equatable, Sendable {
        var at: [String: Point]
        var size: Size
        var edges: [ExtensionGraphEdge]
        var order: [String]
        var depth: [String: Int]
        var folds: [String: Int]
        var frames: [Frame]
    }

    private static let rootGap = 0.5
    private static let groupGap = 0.25

    private struct LayerGroup: Hashable {
        var parents: [Int]
        var title: String
    }

    /// Lays out `graph` with `folded`, or with the view's starting folds when that is nil. Positions and the total
    /// size are in slots: the canvas multiplies each axis by its own node pitch.
    static func layout(_ graph: ExtensionGraphComponent, folded overrideFolds: Set<String>? = nil) -> Result {
        let nodes = graph.nodes
        let ids = nodes.map(\.id)
        let index = Dictionary(uniqueKeysWithValues: ids.enumerated().map { ($0.element, $0.offset) })
        var children = Dictionary(uniqueKeysWithValues: ids.map { ($0, [String]()) })
        var parents = Dictionary(uniqueKeysWithValues: ids.map { ($0, [String]()) })
        for edge in graph.edges ?? [] {
            children[edge.from, default: []].append(edge.to)
            parents[edge.to, default: []].append(edge.from)
        }
        let group = Dictionary(uniqueKeysWithValues: nodes.map { ($0.id, $0.group ?? "") })
        let folded = overrideFolds ?? Set(graph.folded ?? [])
        let roots = ids.filter { parents[$0]?.isEmpty == true }
        let visible = visibleNodes(roots: roots, children: children, folded: folded)
        let groupedRoots: [String] = regroup(roots) { group[$0, default: ""].nilIfEmpty }
        let reading = readingOrder(groupedRoots, children: children, visible: visible, folded: folded, group: group)

        let placed: (slot: [String: Double], depth: [String: Int], frames: [Frame])
        if graph.layout == nil || graph.layout == ExtensionGraphLayout.tree {
            placed = tree(roots: roots, children: children, visible: visible, folded: folded, group: group)
        } else {
            placed = layered(ids: ids, index: index, children: children, parents: parents,
                             visible: visible, folded: folded, group: group)
        }

        let right = graph.direction != ExtensionGraphDirection.down
        var at: [String: Point] = [:]
        for id in ids where visible.contains(id) {
            guard let slot = placed.slot[id], let depth = placed.depth[id] else { continue }
            at[id] = right ? Point(x: Double(depth), y: slot) : Point(x: slot, y: Double(depth))
        }
        let width = (at.values.map(\.x).max() ?? 0) + 1
        let height = (at.values.map(\.y).max() ?? 0) + 1
        var folds: [String: Int] = [:]
        for id in ids where visible.contains(id) && children[id]?.isEmpty == false {
            folds[id] = folded.contains(id) ? hiddenCount(id, children: children, visible: visible) : 0
        }
        let edges = (graph.edges ?? []).filter {
            visible.contains($0.from) && visible.contains($0.to) && !folded.contains($0.from)
        }
        return Result(at: at, size: Size(width: width, height: height), edges: edges,
                      order: reading.order, depth: reading.depth, folds: folds, frames: placed.frames)
    }

    private static func visibleNodes(roots: [String], children: [String: [String]], folded: Set<String>) -> Set<String> {
        var seen = Set(roots)
        var queue = roots
        var next = 0
        while next < queue.count {
            let node = queue[next]
            next += 1
            if folded.contains(node) { continue }
            for child in children[node] ?? [] where !seen.contains(child) {
                seen.insert(child)
                queue.append(child)
            }
        }
        return seen
    }

    /// The first member of a group pulls its peers beside it, preserving their order in the original run.
    private static func regroup<K: Hashable>(_ items: [String], by key: (String) -> K?) -> [String] {
        var out: [String] = []
        var placed: Set<K> = []
        for item in items {
            guard let group = key(item) else {
                out.append(item)
                continue
            }
            if placed.insert(group).inserted {
                out.append(contentsOf: items.filter { key($0) == group })
            }
        }
        return out
    }

    private static func hiddenCount(_ root: String, children: [String: [String]], visible: Set<String>) -> Int {
        var seen: Set<String> = []
        var stack = children[root] ?? []
        while let node = stack.popLast() {
            guard seen.insert(node).inserted else { continue }
            stack.append(contentsOf: children[node] ?? [])
        }
        return seen.subtracting(visible).count
    }

    private static func kids(of node: String, children: [String: [String]], visible: Set<String>,
                             folded: Set<String>, group: [String: String]) -> [String] {
        guard !folded.contains(node) else { return [] }
        let kids = (children[node] ?? []).filter { visible.contains($0) }
        return regroup(kids) { group[$0, default: ""].nilIfEmpty }
    }

    private static func readingOrder(_ roots: [String], children: [String: [String]], visible: Set<String>,
                                     folded: Set<String>, group: [String: String]) -> (order: [String], depth: [String: Int]) {
        var order: [String] = []
        var depth: [String: Int] = [:]
        func visit(_ node: String, _ level: Int) {
            guard depth[node] == nil else { return }
            depth[node] = level
            order.append(node)
            for child in kids(of: node, children: children, visible: visible, folded: folded, group: group) {
                visit(child, level + 1)
            }
        }
        for root in roots { visit(root, 0) }
        return (order, depth)
    }

    private static func frames(of runs: [[String]], group: [String: String]) -> [Frame] {
        var out: [Frame] = []
        for run in runs {
            var start = 0
            while start < run.count {
                let title = group[run[start]] ?? ""
                var end = start
                while end < run.count && group[run[end]] == title { end += 1 }
                if !title.isEmpty { out.append(Frame(group: title, members: Array(run[start..<end]))) }
                start = end
            }
        }
        return out
    }

    private static func tree(roots: [String], children: [String: [String]], visible: Set<String>,
                             folded: Set<String>, group: [String: String])
        -> (slot: [String: Double], depth: [String: Int], frames: [Frame]) {
        var slot: [String: Double] = [:]
        var depth: [String: Int] = [:]
        var runs: [[String]] = []
        var next = 0.0

        func place(_ node: String, _ level: Int) {
            depth[node] = level
            let kids = kids(of: node, children: children, visible: visible, folded: folded, group: group)
            guard !kids.isEmpty else {
                slot[node] = next
                next += 1
                return
            }
            runs.append(kids)
            for (offset, child) in kids.enumerated() {
                if offset > 0 {
                    let before = kids[offset - 1]
                    if group[child] != group[before] && (group[child]?.isEmpty == false || group[before]?.isEmpty == false) {
                        next += groupGap
                    }
                }
                place(child, level + 1)
            }
            slot[node] = ((slot[kids.first!] ?? 0) + (slot[kids.last!] ?? 0)) / 2
        }

        let grouped: [String] = regroup(roots) { group[$0, default: ""].nilIfEmpty }
        runs.append(grouped)
        for (offset, root) in grouped.enumerated() {
            if offset > 0 { next += rootGap }
            place(root, 0)
        }
        return (slot, depth, frames(of: runs, group: group))
    }

    private static func layered(ids: [String], index: [String: Int], children: [String: [String]],
                                parents: [String: [String]], visible: Set<String>, folded: Set<String>,
                                group: [String: String])
        -> (slot: [String: Double], depth: [String: Int], frames: [Frame]) {
        let visibleIDs = ids.filter { visible.contains($0) }
        let visibleParents = Dictionary(uniqueKeysWithValues: visibleIDs.map { id in
            (id, (parents[id] ?? []).filter { visible.contains($0) && !folded.contains($0) })
        })
        let visibleChildren = Dictionary(uniqueKeysWithValues: visibleIDs.map { id in
            (id, folded.contains(id) ? [] : (children[id] ?? []).filter { visible.contains($0) })
        })

        // Kahn's order over the visible edges gives each node one layer past its furthest visible parent.
        var layer: [String: Int] = [:]
        var pending = visibleParents.mapValues(\.count)
        var queue = visibleIDs.filter { pending[$0] == 0 }
        var next = 0
        while next < queue.count {
            let node = queue[next]
            next += 1
            layer[node] = (visibleParents[node] ?? []).map { (layer[$0] ?? 0) + 1 }.max() ?? 0
            for child in visibleChildren[node] ?? [] {
                pending[child, default: 0] -= 1
                if pending[child] == 0 { queue.append(child) }
            }
        }
        let count = (layer.values.max() ?? 0) + 1
        var layers = (0..<count).map { level in visibleIDs.filter { layer[$0] == level } }

        func positions() -> [String: Int] {
            Dictionary(uniqueKeysWithValues: layers.flatMap { row in row.enumerated().map { ($0.element, $0.offset) } })
        }
        func barycenter(_ neighbours: [String], _ places: [String: Int]) -> (sum: Int, count: Int) {
            (neighbours.reduce(0) { $0 + (places[$1] ?? 0) }, neighbours.count)
        }
        func sort(_ row: [String], by key: (String) -> (sum: Int, count: Int)) -> [String] {
            row.enumerated().sorted { a, b in
                let left = key(a.element), right = key(b.element)
                let product = left.sum * right.count
                let other = right.sum * left.count
                // The reference sort is stable: equal fractions keep the row's current order.
                return product == other ? a.offset < b.offset : product < other
            }.map(\.element)
        }
        for level in 1..<max(count, 1) {
            let places = positions()
            layers[level] = sort(layers[level]) { barycenter(visibleParents[$0] ?? [], places) }
        }
        if count >= 2 {
            for level in stride(from: count - 2, through: 0, by: -1) {
                let places = positions()
                layers[level] = sort(layers[level]) { node in
                    let kids = visibleChildren[node] ?? []
                    return kids.isEmpty ? (places[node] ?? 0, 1) : barycenter(kids, places)
                }
            }
        }

        func groupKey(_ node: String) -> LayerGroup? {
            guard group[node]?.isEmpty == false else { return nil }
            let parents = (visibleParents[node] ?? []).map { index[$0] ?? 0 }.sorted()
            return LayerGroup(parents: parents, title: group[node] ?? "")
        }
        layers = layers.map { regroup($0, by: groupKey) }
        func gap(_ a: String, _ b: String) -> Double {
            let left = groupKey(a), right = groupKey(b)
            return left != right && (left != nil || right != nil) ? groupGap : 0
        }

        var slot: [String: Double] = [:]
        var widths: [Double] = []
        for row in layers {
            var at = 0.0
            for (offset, node) in row.enumerated() {
                if offset > 0 { at += 1 + gap(row[offset - 1], node) }
                slot[node] = at
            }
            widths.append(at + 1)
        }
        let widest = widths.max() ?? 0
        for (level, row) in layers.enumerated() {
            let offset = (widest - widths[level]) / 2
            for node in row { slot[node, default: 0] += offset }
        }

        var frames: [Frame] = []
        for row in layers {
            var start = 0
            while start < row.count {
                let key = groupKey(row[start])
                var end = start
                while end < row.count && groupKey(row[end]) == key { end += 1 }
                if let key { frames.append(Frame(group: key.title, members: Array(row[start..<end]))) }
                start = end
            }
        }
        return (slot, layer, frames)
    }
}

nonisolated private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
