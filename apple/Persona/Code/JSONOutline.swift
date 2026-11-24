import SwiftUI
import UIKit

/// A JSON document as a flat list of nodes in document order (docs/apple/ios.md「File viewer」): each node knows its depth,
/// its key and where its subtree ends, so showing it folded or open only walks what is on screen. Parsed from the
/// bytes in one pass, keys kept in their order (JSONSerialization loses it), off the main thread.
nonisolated struct JSONOutline: Sendable {
    enum Kind: UInt8, Sendable { case object, array, string, number, bool, null }

    struct Node: Sendable {
        var kind: Kind
        var depth: Int32
        /// The key's bytes without quotes (-1 when the parent is an array or this is the top).
        var keyStart: Int32 = -1
        var keyEnd: Int32 = -1
        /// The value's bytes: a scalar as written, a container from its bracket to the matching one.
        var valueStart: Int32
        var valueEnd: Int32 = 0
        /// The index after the last node in this one's subtree.
        var end: Int32 = 0
        var children: Int32 = 0
        var parent: Int32
    }

    struct ParseError: Error, Sendable {
        var offset: Int
        var message: String
    }

    let bytes: [UInt8]
    let nodes: [Node]

    init(text: String) throws {
        let bytes = Array(text.utf8)
        self.bytes = bytes
        nodes = try bytes.withUnsafeBufferPointer { try Self.parse($0) }
    }

    func key(_ n: Node) -> String? {
        guard n.keyStart >= 0 else { return nil }
        return slice(Int(n.keyStart), Int(n.keyEnd))
    }

    func value(_ n: Node) -> String { slice(Int(n.valueStart), Int(n.valueEnd)) }

    func slice(_ s: Int, _ e: Int) -> String {
        bytes.withUnsafeBufferPointer { String(decoding: UnsafeBufferPointer(rebasing: $0[s..<e]), as: UTF8.self) }
    }

    // MARK: Parsing

    private static func parse(_ b: UnsafeBufferPointer<UInt8>) throws -> [Node] {
        var nodes: [Node] = []
        nodes.reserveCapacity(b.count / 12)
        var stack: [Int32] = [] // open containers
        var i = 0
        var pendingKey: (Int32, Int32)?
        var expectValue = true
        func fail(_ m: String) -> ParseError { ParseError(offset: i, message: m) }
        func skip() {
            while i < b.count, b[i] == 32 || b[i] == 9 || b[i] == 10 || b[i] == 13 { i += 1 }
        }
        func stringEnd(_ from: Int) throws -> Int {
            var j = from + 1
            while j < b.count {
                if b[j] == 92 { j += 2; continue }
                if b[j] == 34 { return j + 1 }
                j += 1
            }
            throw ParseError(offset: from, message: String(localized: "Unterminated string"))
        }
        func add(_ kind: Kind, _ start: Int, _ end: Int) {
            let parent = stack.last ?? -1
            var n = Node(kind: kind, depth: Int32(stack.count), valueStart: Int32(start), valueEnd: Int32(end), parent: parent)
            if let k = pendingKey { n.keyStart = k.0; n.keyEnd = k.1 }
            pendingKey = nil
            n.end = Int32(nodes.count + 1)
            if parent >= 0 { nodes[Int(parent)].children += 1 }
            nodes.append(n)
        }
        func readKey() throws {
            guard i < b.count, b[i] == 34 else { throw fail(String(localized: "Expected a key (a quoted string) here")) }
            let e = try stringEnd(i)
            pendingKey = (Int32(i + 1), Int32(e - 1))
            i = e
            skip()
            guard i < b.count, b[i] == 58 else { throw fail(String(localized: "Expected a colon after the key")) }
            i += 1
            skip()
        }
        skip()
        while i < b.count {
            let c = b[i]
            if !expectValue {
                // After a value: a comma, or the end of the container.
                if c == 44 {
                    i += 1
                    expectValue = true
                    skip()
                    if let top = stack.last, nodes[Int(top)].kind == .object { try readKey() }
                    continue
                }
                if c == 125 || c == 93 {
                    guard let top = stack.popLast() else { let bracket = c == 125 ? "}" : "]"; throw fail(String(localized: "Unexpected \(bracket)")) }
                    nodes[Int(top)].valueEnd = Int32(i + 1)
                    nodes[Int(top)].end = Int32(nodes.count)
                    i += 1
                    skip()
                    if stack.isEmpty, i < b.count { throw fail(String(localized: "Unexpected content after the JSON")) }
                    continue
                }
                throw fail(String(localized: "Expected a comma or bracket here"))
            }
            switch c {
            case 123, 91: // { [
                add(c == 123 ? .object : .array, i, i + 1)
                stack.append(Int32(nodes.count - 1))
                i += 1
                skip()
                if i < b.count, b[i] == (c == 123 ? 125 : 93) { // empty
                    expectValue = false
                    continue
                }
                if c == 123 { try readKey() }
                continue
            case 34:
                let e = try stringEnd(i)
                add(.string, i, e)
                i = e
            case 116, 102, 110: // true false null
                let word: [UInt8] = c == 116 ? Array("true".utf8) : c == 102 ? Array("false".utf8) : Array("null".utf8)
                guard SyntaxLexer.at(b, i, b.count, word) else { throw fail(String(localized: "Unknown value")) }
                add(c == 110 ? .null : .bool, i, i + word.count)
                i += word.count
            case 45, 48...57:
                let s = i
                i += 1
                while i < b.count, (b[i] >= 48 && b[i] <= 57) || b[i] == 46 || b[i] == 101 || b[i] == 69 || b[i] == 43 || b[i] == 45 { i += 1 }
                add(.number, s, i)
            default:
                throw fail(String(localized: "Unknown value"))
            }
            expectValue = false
            skip()
            if stack.isEmpty, i < b.count { throw fail(String(localized: "Unexpected content after the JSON")) }
        }
        if !stack.isEmpty { throw ParseError(offset: b.count, message: String(localized: "Unclosed bracket")) }
        if nodes.isEmpty { throw ParseError(offset: 0, message: String(localized: "Empty file")) }
        return nodes

    }

    // MARK: Folding

    /// What is open by default: the top, then whole levels while the outline stays under `budget` lines (a big
    /// document opens folded below its first levels).
    func defaultOpen(budget: Int = 400) -> [Bool] {
        var open = [Bool](repeating: false, count: nodes.count)
        guard !nodes.isEmpty else { return open }
        open[0] = true
        var shown = 2 + Int(nodes[0].children)
        var level = Array(childIndices(0)).filter { nodes[$0].kind == .object || nodes[$0].kind == .array }
        while !level.isEmpty {
            let more = level.reduce(0) { $0 + Int(nodes[$1].children) + 1 }
            if shown + more > budget { break }
            shown += more
            var next: [Int] = []
            for n in level {
                open[n] = true
                next += childIndices(n).filter { nodes[$0].kind == .object || nodes[$0].kind == .array }
            }
            level = next
        }
        return open
    }

    func childIndices(_ n: Int) -> [Int] {
        var out: [Int] = []
        var c = n + 1
        while c < Int(nodes[n].end) {
            out.append(c)
            c = Int(nodes[c].end)
        }
        return out
    }

    /// One line of the outline: a node, or (closing) the bracket that ends an open container.
    struct Line: Sendable {
        var node: Int32
        var closing: Bool
    }

    /// The lines to show with these nodes open.
    func lines(open: [Bool]) -> [Line] {
        var out: [Line] = []
        var stack: [Int] = []
        var i = 0
        while i < nodes.count {
            while let top = stack.last, Int(nodes[top].end) <= i {
                out.append(Line(node: Int32(top), closing: true))
                stack.removeLast()
            }
            out.append(Line(node: Int32(i), closing: false))
            let n = nodes[i]
            if (n.kind == .object || n.kind == .array) && n.children > 0 && open[i] {
                stack.append(i)
                i += 1
            } else {
                i = Int(n.end)
            }
        }
        while let top = stack.popLast() { out.append(Line(node: Int32(top), closing: true)) }
        return out
    }

    /// Whether a node is the last in its parent (no comma after it).
    func isLast(_ i: Int) -> Bool {
        let p = Int(nodes[i].parent)
        return p < 0 || nodes[i].end == nodes[p].end
    }

    /// `.a.b[3]` for a node.
    func path(_ i: Int) -> String {
        var parts: [String] = []
        var n = i
        while nodes[n].parent >= 0 {
            let p = Int(nodes[n].parent)
            if nodes[p].kind == .array {
                parts.append("[\(childIndices(p).firstIndex(of: n) ?? 0)]")
            } else {
                parts.append("." + (key(nodes[n]) ?? ""))
            }
            n = p
        }
        return parts.reversed().joined()
    }
}

/// A JSON file as a foldable outline in `CodeLinesView`: formatted two spaces a level, keys, strings, numbers and
/// constants in their colors, a tap on a container's line opens or closes it (the gutter's arrow shows which),
/// a long press copies the value or its path.
@MainActor final class JSONOutlineSource: CodeLineSource {
    let outline: JSONOutline
    private var open: [Bool]
    private var lines: [JSONOutline.Line]
    private var cache: [Int: NSAttributedString] = [:]
    var changed: ((Range<Int>?) -> Void)?
    private(set) var columns = 80

    init(_ outline: JSONOutline) {
        self.outline = outline
        open = outline.defaultOpen()
        lines = outline.lines(open: open)
        measure()
    }

    var lineCount: Int { lines.count }
    var gutterColumns: Int { 1 }

    private func measure() {
        // Indentation plus the longest key and scalar shown, capped: long strings run off to the right.
        var widest = 0
        for l in lines.prefix(20_000) {
            let n = outline.nodes[Int(l.node)]
            let key = n.keyStart >= 0 ? Int(n.keyEnd - n.keyStart) + 4 : 0
            let value = n.kind == .object || n.kind == .array ? 16 : Int(n.valueEnd - n.valueStart)
            widest = max(widest, Int(n.depth) * 2 + key + value + 2)
        }
        columns = min(widest, CodeDocument.maxLineBytes)
    }

    func line(_ i: Int) -> NSAttributedString {
        if let a = cache[i] { return a }
        let l = lines[i]
        let idx = Int(l.node)
        let n = outline.nodes[idx]
        let out = NSMutableAttributedString(string: String(repeating: "  ", count: Int(n.depth)), attributes: SyntaxStyle.plain)
        let comma = outline.isLast(idx) ? "" : ","
        let container = n.kind == .object || n.kind == .array
        if l.closing {
            out.append(NSAttributedString(string: (n.kind == .object ? "}" : "]") + comma, attributes: SyntaxStyle.plain))
        } else {
            if let key = outline.key(n) {
                out.append(NSAttributedString(string: "\"\(key)\"", attributes: SyntaxStyle.attributes(.tag)))
                out.append(NSAttributedString(string: ": ", attributes: SyntaxStyle.plain))
            }
            if container {
                let (o, c) = n.kind == .object ? ("{", "}") : ("[", "]")
                if n.children == 0 {
                    out.append(NSAttributedString(string: o + c + comma, attributes: SyntaxStyle.plain))
                } else if open[idx] {
                    out.append(NSAttributedString(string: o, attributes: SyntaxStyle.plain))
                } else {
                    out.append(NSAttributedString(string: o + " … " + c + comma, attributes: SyntaxStyle.plain))
                    let what = n.kind == .object ? String(localized: "\(n.children) keys") : String(localized: "\(n.children) items")
                    out.append(NSAttributedString(string: "  " + what, attributes: SyntaxStyle.attributes(.comment)))
                }
            } else {
                var v = outline.value(n)
                if v.utf8.count > CodeDocument.maxLineBytes { v = String(v.prefix(CodeDocument.maxLineBytes / 2)) + "…" }
                let kind: SyntaxKind = switch n.kind {
                case .string: .string
                case .number: .number
                default: .keyword
                }
                out.append(NSAttributedString(string: v, attributes: SyntaxStyle.attributes(kind)))
                out.append(NSAttributedString(string: comma, attributes: SyntaxStyle.plain))
            }
        }
        if cache.count > 600 { cache = [:] }
        cache[i] = out
        return out
    }

    func gutter(_ i: Int) -> NSAttributedString {
        let l = lines[i]
        let idx = Int(l.node)
        let n = outline.nodes[idx]
        guard !l.closing, n.kind == .object || n.kind == .array, n.children > 0 else { return NSAttributedString() }
        return NSAttributedString(string: open[idx] ? "▾" : "▸", attributes: SyntaxStyle.gutter)
    }

    func tap(_ i: Int) -> Bool {
        let idx = Int(lines[i].node)
        let n = outline.nodes[idx]
        guard n.kind == .object || n.kind == .array, n.children > 0 else { return false }
        open[idx].toggle()
        relayout()
        return true
    }

    /// Every node open (only for outlines small enough to show whole) or every node below the top closed.
    func setAll(open o: Bool) {
        open = [Bool](repeating: o, count: outline.nodes.count)
        if !open.isEmpty { open[0] = true }
        relayout()
        changed?(nil)
    }

    var canOpenAll: Bool { outline.nodes.count <= 50_000 }

    /// Opens the top's biggest container (the measurements scroll through it, `AutoScroller`).
    func openBiggest() {
        guard let big = outline.childIndices(0).max(by: { outline.nodes[$0].children < outline.nodes[$1].children }) else { return }
        open[big] = true
        relayout()
        changed?(nil)
    }

    private func relayout() {
        lines = outline.lines(open: open)
        cache = [:]
        measure()
    }

    func menu(_ i: Int) -> UIMenu? {
        let idx = Int(lines[i].node)
        let n = outline.nodes[idx]
        let value = outline.value(n)
        let path = outline.path(idx)
        var items: [UIMenuElement] = [
            UIAction(title: String(localized: "Copy Value"), image: UIImage(systemName: Theme.Icon.copy)) { _ in UIPasteboard.general.string = value },
        ]
        if !path.isEmpty {
            let shownPath = path.count > 40 ? "…" + path.suffix(39) : path
            items.append(UIAction(title: String(localized: "Copy Path \(shownPath)"), image: UIImage(systemName: "point.topleft.down.to.point.bottomright.curvepath")) { _ in
                UIPasteboard.general.string = path
            })
        }
        return UIMenu(title: "", children: items)
    }
}
