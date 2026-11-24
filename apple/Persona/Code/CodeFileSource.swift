import SwiftUI
import UIKit

/// How each kind of token looks: `Theme.Syntax` colors (and the diff colors), bold headings and strong text,
/// italic emphasis, in the code font.
@MainActor enum SyntaxStyle {
    static let font = CodeMetrics.font
    private static let bold = UIFont.monospacedSystemFont(ofSize: font.pointSize, weight: .bold)
    private static let italic: UIFont = {
        guard let d = font.fontDescriptor.withSymbolicTraits(.traitItalic) else { return font }
        return UIFont(descriptor: d, size: font.pointSize)
    }()

    static let plain: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: UIColor.label]
    static let gutter: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: UIColor.tertiaryLabel]

    private static let byKind: [SyntaxKind: [NSAttributedString.Key: Any]] = {
        let regular = font
        func c(_ color: Color, _ f: UIFont? = nil) -> [NSAttributedString.Key: Any] { [.font: f ?? regular, .foregroundColor: UIColor(color)] }
        return [
            .keyword: c(Theme.Syntax.keyword), .type: c(Theme.Syntax.type), .variable: c(Theme.Syntax.type),
            .string: c(Theme.Syntax.string), .code: c(Theme.Syntax.string),
            .number: c(Theme.Syntax.number), .link: c(Theme.Syntax.number),
            .function: c(Theme.Syntax.function), .meta: c(Theme.Syntax.function),
            .comment: c(Theme.Syntax.comment), .quote: c(Theme.Syntax.comment),
            .tag: c(Theme.Syntax.tag), .attribute: c(Theme.Syntax.attribute),
            .heading: c(Theme.Syntax.keyword, bold), .strong: [.font: bold, .foregroundColor: UIColor.label],
            .emphasis: [.font: italic, .foregroundColor: UIColor.label],
            .added: c(Theme.Diff.add), .removed: c(Theme.Diff.del),
        ]
    }()

    static func attributes(_ kind: SyntaxKind) -> [NSAttributedString.Key: Any] { byKind[kind] ?? plain }

    /// A line's text with its tokens' colors; byte offsets turned into the string's UTF-16 ones.
    static func line(_ text: String, bytes: UnsafeBufferPointer<UInt8>, tokens: ArraySlice<SyntaxToken>?, cut: Bool) -> NSAttributedString {
        let out = NSMutableAttributedString(string: text, attributes: plain)
        if let tokens, !tokens.isEmpty {
            let map = utf16Offsets(bytes)
            let length = out.length
            for t in tokens {
                let s = Int(t.start), e = Int(t.start + t.length)
                guard e <= bytes.count else { continue }
                let a = map.map { Int($0[s]) } ?? s, b = map.map { Int($0[e]) } ?? e
                guard a < b, b <= length else { continue }
                out.setAttributes(attributes(t.kind), range: NSRange(location: a, length: b - a))
            }
        }
        if cut { out.append(NSAttributedString(string: String(localized: "  … line too long, the rest is cut off"), attributes: attributes(.comment))) }
        return out
    }

    /// For each byte offset in a line, its UTF-16 offset; nil when the line is ASCII (they are the same).
    static func utf16Offsets(_ b: UnsafeBufferPointer<UInt8>) -> [Int32]? {
        guard b.contains(where: { $0 >= 0x80 }) else { return nil }
        var map = [Int32](repeating: 0, count: b.count + 1)
        var u: Int32 = 0
        for k in 0..<b.count {
            map[k] = u
            let c = b[k]
            if c < 0x80 || c >= 0xC0 && c < 0xF0 {
                u += 1 // ASCII, or the lead byte of a 2- or 3-byte character: one UTF-16 unit
            } else if c >= 0xF0 {
                u += 2 // a 4-byte character: a surrogate pair
            }
        }
        map[b.count] = u
        return map
    }
}

/// A text file in `CodeLinesView`: its lines as they come, colored as `CodeDocument.highlight` gets to them
/// (in the background, the screen's lines refreshed as each chunk is done). Further pages append.
@MainActor final class CodeFileSource: CodeLineSource {
    private(set) var doc: CodeDocument
    var changed: ((Range<Int>?) -> Void)?
    /// When coloring finished (or nil while it runs); the viewer shows when the caps stopped it.
    private(set) var coloredAt: Date?
    var onColored: (() -> Void)?
    private var cache: [Int: NSAttributedString] = [:]
    nonisolated(unsafe) private var task: Task<Void, Never>?

    init(_ doc: CodeDocument) {
        self.doc = doc
        color()
    }

    deinit { task?.cancel() }

    var lineCount: Int { doc.lineCount }
    /// Room for the note on a cut line ("… the rest is cut off") when a line is that long.
    var columns: Int { doc.widestLine + (doc.widestLine >= CodeDocument.maxLineBytes ? 30 : 2) }
    var gutterColumns: Int { max(2, String(doc.lineCount).count) }

    /// The next page of the file, after the lines already there.
    func append(_ text: String) {
        task?.cancel()
        doc = doc.appending(text)
        cache = [:]
        changed?(nil)
        color()
    }

    private func color() {
        guard doc.language != nil else {
            coloredAt = .now
            return
        }
        coloredAt = nil
        let doc = doc
        task = Task.detached(priority: .userInitiated) { [weak self] in
            doc.highlight(cancelled: { Task.isCancelled }) { range in
                // Its own weak capture, as in MainActor.run below: Swift 6.2 (Xcode 26.0) treats the outer `self`
                // as this task's variable and will not send it to the main actor ("sending 'self' risks causing
                // data races"); 6.3 accepts either.
                Task { @MainActor [weak self] in self?.colored(range, in: doc) }
            }
            await MainActor.run { [weak self] in
                guard let self, self.doc === doc else { return }
                self.coloredAt = .now
                self.onColored?()
            }
        }
    }

    private func colored(_ range: Range<Int>, in d: CodeDocument) {
        guard d === doc else { return }
        for i in range { cache[i] = nil }
        changed?(range)
    }

    func line(_ i: Int) -> NSAttributedString {
        if let a = cache[i] { return a }
        let r = doc.lineRange(i)
        let tokens = doc.tokens(i)
        let a = doc.bytes.withUnsafeBufferPointer { b in
            SyntaxStyle.line(doc.text(i), bytes: UnsafeBufferPointer(rebasing: b[r]), tokens: tokens, cut: doc.isCut(i))
        }
        // Only lines whose colors are final are kept (the rest are asked for again when theirs come).
        if tokens != nil || doc.language == nil {
            if cache.count > 600 { cache = [:] }
            cache[i] = a
        }
        return a
    }

    func gutter(_ i: Int) -> NSAttributedString { NSAttributedString(string: String(i + 1), attributes: SyntaxStyle.gutter) }

    func tap(_ i: Int) -> Bool { false }

    func menu(_ i: Int) -> UIMenu? {
        let line = doc.text(i)
        let all = doc
        return UIMenu(title: String(localized: "Line \(i + 1)"), children: [
            UIAction(title: String(localized: "Copy Line"), image: UIImage(systemName: Theme.Icon.copy)) { _ in UIPasteboard.general.string = line },
            UIAction(title: String(localized: "Copy All [Code File Source 197a4d]"), image: UIImage(systemName: Theme.Icon.paste)) { _ in UIPasteboard.general.string = all.string },
        ])
    }
}
