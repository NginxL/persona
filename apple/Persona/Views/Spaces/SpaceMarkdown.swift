import Foundation

/// A space's SPACE.md as the editor shows it (#13500, the web's lib/space-md.ts): the parts the user writes, which the
/// editor lets them change, and the blocks Persona keeps up to date (`<!-- persona:managed:<name>:begin -->` …
/// `:end -->`), shown read-only. The hub refuses a write that changes a managed block or its markers (400), so the editor
/// never lets one change: it edits the human parts and puts the blocks back byte for byte.
///
/// Byte for byte: the file is split on its UTF-8 bytes (the markers are ASCII), never compared as `String`s, which Swift
/// compares by canonical equivalence (an NFD "é" equals an NFC one) and whose `hasSuffix("\n")` is false for "\r\n".
nonisolated enum SpaceMarkdown {
    enum Part: Equatable {
        case human(String)
        /// `text`: the whole block, markers included; `body`: what is between them.
        case managed(name: String, text: String, body: String)

        var text: String {
            switch self {
            case .human(let t): t
            case .managed(_, let t, _): t
            }
        }

        var isManaged: Bool {
            if case .managed = self { true } else { false }
        }
    }

    /// The parts the editor shows as fields (`editable`, indexes into `parts`).
    struct Shape: Equatable {
        var parts: [Part]
        var editable: [Int]
    }

    private static let beginHead = Array("<!-- persona:managed:".utf8)
    private static let beginTail = Array(":begin -->".utf8)

    /// Splits the file into human parts and managed blocks, in order; joining them gives the file back unchanged. A
    /// block whose end marker is missing runs to the end of the file, so nothing of it becomes editable.
    static func split(_ content: String) -> [Part] {
        let bytes = Array(content.utf8)
        var parts: [Part] = []
        var at = 0
        var search = 0
        while let begin = find(beginHead, in: bytes, from: search) {
            // the name: one or more of [a-z0-9_-], then ":begin -->"
            var i = begin + beginHead.count
            while i < bytes.count, isNameByte(bytes[i]) { i += 1 }
            let nameEnd = i
            guard nameEnd > begin + beginHead.count, starts(beginTail, in: bytes, at: nameEnd) else {
                search = begin + 1
                continue
            }
            let name = text(bytes, begin + beginHead.count, nameEnd)
            let bodyStart = nameEnd + beginTail.count
            let endMarker = Array("<!-- persona:managed:\(name):end -->".utf8)
            let endAt = find(endMarker, in: bytes, from: bodyStart)
            let stop = endAt.map { $0 + endMarker.count } ?? bytes.count
            if begin > at { parts.append(.human(text(bytes, at, begin))) }
            parts.append(.managed(name: name, text: text(bytes, begin, stop), body: text(bytes, bodyStart, endAt ?? bytes.count)))
            at = stop
            search = stop
        }
        if at < bytes.count { parts.append(.human(text(bytes, at, bytes.count))) }
        return parts
    }

    /// The file again from its parts.
    static func join(_ parts: [Part]) -> String {
        parts.map(\.text).joined()
    }

    /// Every human part with words in it; when there is none, one empty part at the start (added when missing), so the
    /// user always has somewhere to write. Whitespace-only parts between blocks stay as they are, unshown.
    static func editableParts(_ parts: [Part]) -> Shape {
        let editable = parts.indices.filter { i in
            if case .human(let t) = parts[i] { return !isBlank(t) }
            return false
        }
        if !editable.isEmpty { return Shape(parts: parts, editable: editable) }
        if case .human = parts.first { return Shape(parts: parts, editable: [0]) }
        return Shape(parts: [.human("")] + parts, editable: [0])
    }

    /// The file from `parts` with the fields' words (`edits`, by part index) in place of their human parts. A field
    /// written right before a block keeps a line break between them, so the block's begin marker stays on its own line.
    static func assemble(_ parts: [Part], edits: [Int: String]) -> String {
        parts.indices.map { i -> String in
            guard case .human = parts[i], let text = edits[i] else { return parts[i].text }
            let beforeBlock = i + 1 < parts.count && parts[i + 1].isManaged
            if beforeBlock, !text.utf8.isEmpty, text.utf8.last != UInt8(ascii: "\n") { return text + "\n\n" }
            return text
        }
        .joined()
    }

    /// Whether every managed block of `after` is byte for byte one of `before`'s, in the same order (what the hub checks).
    static func sameManagedBlocks(_ before: String, _ after: String) -> Bool {
        func blocks(_ s: String) -> [[UInt8]] { split(s).filter(\.isManaged).map { Array($0.text.utf8) } }
        return blocks(before) == blocks(after)
    }

    /// SPACE.md without the blocks Persona keeps: what the user wrote, as the space's home shows it.
    static func human(_ content: String) -> String {
        split(content).compactMap { part -> String? in
            guard case .human(let t) = part else { return nil }
            let trimmed = t.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? nil : trimmed
        }
        .joined(separator: "\n\n")
    }

    /// The user's part of SPACE.md without a leading `# <title>` that only repeats the space's name shown above it.
    static func body(_ content: String, title: String) -> String {
        let text = human(content)
        let scalars = text.unicodeScalars
        guard scalars.first == "#" else { return text }
        let lineEnd = scalars.firstIndex(of: "\n") ?? scalars.endIndex
        var line = Substring(scalars[scalars.index(after: scalars.startIndex)..<lineEnd])
        // "#", then at least one space or tab: "#tag" is not a heading
        guard let first = line.unicodeScalars.first, first == " " || first == "\t", !line.unicodeScalars.contains("\r") else { return text }
        // the closing hashes and the blanks around them, as Markdown allows (`# Money ##`)
        line = trimEnd(line, [" ", "\t"])
        while line.unicodeScalars.last == "#" { line = Substring(line.unicodeScalars.dropLast()) }
        let heading = line.trimmingCharacters(in: .whitespaces)
        guard !heading.isEmpty,
              heading.lowercased() == title.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() else { return text }
        let rest = lineEnd == scalars.endIndex ? "" : String(Substring(scalars[scalars.index(after: lineEnd)...]))
        return String(rest.drop(while: { $0.isWhitespace }))
    }

    // MARK: Bytes

    private static func isNameByte(_ b: UInt8) -> Bool {
        (b >= UInt8(ascii: "a") && b <= UInt8(ascii: "z")) || (b >= UInt8(ascii: "0") && b <= UInt8(ascii: "9"))
            || b == UInt8(ascii: "_") || b == UInt8(ascii: "-")
    }

    private static func isBlank(_ s: String) -> Bool {
        s.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private static func starts(_ needle: [UInt8], in bytes: [UInt8], at i: Int) -> Bool {
        i + needle.count <= bytes.count && bytes[i..<(i + needle.count)].elementsEqual(needle)
    }

    private static func find(_ needle: [UInt8], in bytes: [UInt8], from start: Int) -> Int? {
        guard !needle.isEmpty, start >= 0, bytes.count >= needle.count else { return nil }
        var i = start
        while i + needle.count <= bytes.count {
            if bytes[i] == needle[0], starts(needle, in: bytes, at: i) { return i }
            i += 1
        }
        return nil
    }

    /// The bytes `from..<to` as text: cut at ASCII markers only, so always whole UTF-8 sequences.
    private static func text(_ bytes: [UInt8], _ from: Int, _ to: Int) -> String {
        String(decoding: bytes[from..<to], as: UTF8.self)
    }

    private static func trimEnd(_ s: Substring, _ chars: Set<Unicode.Scalar>) -> Substring {
        var view = s.unicodeScalars
        while let last = view.last, chars.contains(last) { view = view.dropLast() }
        return Substring(view)
    }
}
