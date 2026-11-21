#if os(iOS)
import Foundation
import PersonaKit
import SwiftUI

// Picked messages as text and as a picture (#9468, docs/apple/ios.md "Forwarding a message"): Copy and Export Image in
// the selecting bar, and Include Questions, which brings each picked reply's questions along. What is decided here is
// pure: which questions a pick brings, the copied text, the blocks a message is drawn in, a picture's size in the
// words, and where the pages of a long picture break. `ChatExportRenderer` draws, `ChatExportSheet` shows and shares.

/// One message as it goes into the picture or the copied text: who wrote it, its words as a forward carries them
/// (`ForwardMessage.body`), when, and your own message's pictures (paths on its computer).
nonisolated struct ExportMessage: Identifiable, Equatable, Sendable {
    var id: String
    var role: ForwardMessage.Role
    /// Who wrote it, by name ("Claude", "Persona", "@Patrol"); "Me" in the user's language for your own.
    var author: String
    var text: String
    var time: Date?
    var images: [String] = []
    var prefersLetterMark = false

    var isUser: Bool { role == .user }

    /// The most of one message the picture and the copy take; a longer one is cut with a note, as a forward is.
    static let limit = ForwardMessage.limit
}

/// Include Questions (#9468): a reply's questions are the messages of its turn from the user's side (your own, and a
/// relayed or another source's that opened it), in the chat's order, with those of the turns before it that got no reply
/// (two messages sent one after the other are two turns, the first without an answer). Injected lines and `/loop` ticks
/// are not messages (`ChatRow.pickBar`), so never questions.
enum PickQuestions {
    /// Every reply of every turn (its answer, and the replies its steps hold) → its questions.
    static func map(_ blocks: [ChatBlock]) -> [String: [String]] {
        var out: [String: [String]] = [:]
        var waiting: [String] = [] // asked, no reply yet: they go with the next reply
        for case .turn(let t) in blocks {
            let replies = (t.process + [t.answer].compactMap { $0 }).compactMap { row -> String? in
                if case .assistant(let item) = row { item.id } else { nil }
            }
            let questions = waiting + t.users.compactMap { $0.pickBar?.id }
            guard !replies.isEmpty else {
                waiting = questions
                continue
            }
            waiting = []
            guard !questions.isEmpty else { continue }
            for id in replies { out[id] = questions }
        }
        return out
    }

    /// The questions `picked` brings that are not picked themselves.
    nonisolated static func brought(_ picked: Set<String>, questions: [String: [String]]) -> Set<String> {
        Set(picked.flatMap { questions[$0] ?? [] }).subtracting(picked)
    }

    /// Whether Include Questions means anything for `picked`: a picked reply has a question that is not picked.
    nonisolated static func offered(_ picked: Set<String>, questions: [String: [String]]) -> Bool {
        !brought(picked, questions: questions).isEmpty
    }
}

/// The picked messages as plain text (Copy): who wrote each, then its words, a blank line between them.
nonisolated enum ExportText {
    static func copy(_ messages: [ExportMessage]) -> String {
        messages.map { m in String(localized: "\(m.author): [Copied Messages]") + "\n" + m.text }.joined(separator: "\n\n")
    }
}

/// A message's words cut into what the picture draws one at a time (`ChatExportRenderer`): runs of Markdown, a paragraph
/// with inline formulas (MarkdownUI loads those images later, which an offscreen picture never waits for), a run of
/// pictures, and the cards a reply draws (`ShareCardParts`). Small runs are drawn together; a long code block is drawn
/// in pieces, so no one picture is too big to hold.
nonisolated enum ExportPart: Equatable, Sendable {
    case markdown(String)
    /// A paragraph with `persona-math:` inline images in it (`MathMarkdown`), drawn as text with the formulas in it.
    case mathText(String)
    /// A run of pictures on lines of their own (`ReplyImages`, the chat's rule): all of them.
    case images([ReplyImage])
    case card(ShareCardPart)
}

enum ExportParts {
    /// Markdown drawn together up to about this many characters.
    static let run = 1_200
    /// A code block longer than this many lines is drawn as several blocks one after another.
    static let codeLines = 60

    /// The parts of a reply: cards as the picture draws them, the words as the chat reads them (its breaks, CJK
    /// emphasis and formulas) cut into blocks.
    static func of(_ text: String) -> [ExportPart] {
        ShareCardParts.of(text, limit: ExportMessage.limit).flatMap { part -> [ExportPart] in
            guard case .markdown(let words) = part else { return [.card(part)] }
            return blocks(MathMarkdown.rewrite(CJKEmphasis.apply(ReplyImages.apply(ReplyBreaks.apply(words)))))
        }
    }

    /// Markdown cut at its blank lines (never inside a fence), each block sorted, plain ones joined up to `run`.
    static func blocks(_ md: String) -> [ExportPart] {
        var out: [ExportPart] = []
        var pending: [String] = []
        func flush() {
            guard !pending.isEmpty else { return }
            out.append(.markdown(pending.joined(separator: "\n\n")))
            pending = []
        }
        for block in split(md) {
            let lines = block.components(separatedBy: "\n")
            if let run = pictures(lines) {
                flush()
                out.append(.images(run))
            } else if block.contains("](\(MathMarkdown.scheme):") {
                if isParagraph(lines) {
                    flush()
                    out.append(.mathText(block))
                } else {
                    // In a list, a table or a heading the formula is its TeX in code: still readable.
                    pending.append(inlineTeX(block))
                }
            } else if MarkdownFence.opening(lines[0].trimmingCharacters(in: .whitespaces)) != nil, lines.count - 2 > codeLines {
                flush()
                out += codePieces(lines, fence: lines[0])
            } else {
                if pending.reduce(0, { $0 + $1.count }) + block.count > run { flush() }
                pending.append(block)
            }
        }
        flush()
        return out
    }

    /// Blocks separated by blank lines, a fenced block whole however many blank lines it holds.
    static func split(_ md: String) -> [String] {
        var blocks: [String] = []
        var current: [String] = []
        var fence: MarkdownFence?
        for line in md.components(separatedBy: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            if let f = fence {
                current.append(line)
                if f.isClosed(by: t) { fence = nil }
                continue
            }
            if let f = MarkdownFence.opening(t) {
                // A fence right under a paragraph is a block of its own.
                if !current.isEmpty {
                    blocks.append(current.joined(separator: "\n"))
                    current = []
                }
                fence = f
                current.append(line)
                continue
            }
            if t.isEmpty {
                if !current.isEmpty { blocks.append(current.joined(separator: "\n")) }
                current = []
            } else {
                current.append(line)
            }
        }
        if !current.isEmpty { blocks.append(current.joined(separator: "\n")) }
        return blocks
    }

    /// A run of pictures as `ReplyImages.apply` fences it: the run's pictures; nil for any other block.
    static func pictures(_ lines: [String]) -> [ReplyImage]? {
        guard lines.count >= 2, let fence = MarkdownFence.opening(lines[0].trimmingCharacters(in: .whitespaces)),
              lines[0].contains(ReplyImages.language),
              fence.isClosed(by: lines[lines.count - 1].trimmingCharacters(in: .whitespaces)),
              let block = ReplyImages.block(lines.dropFirst().dropLast().joined(separator: "\n")), !block.images.isEmpty
        else { return nil }
        return Array(block.images)
    }

    /// A plain paragraph: not a list, a quote, a heading, a table or code.
    static func isParagraph(_ lines: [String]) -> Bool {
        lines.allSatisfy { line in
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix("#") || t.hasPrefix(">") || t.hasPrefix("|") || t.hasPrefix("```") || t.hasPrefix("~~~") { return false }
            if t.hasPrefix("- ") || t.hasPrefix("* ") || t.hasPrefix("+ ") { return false }
            if let dot = t.firstIndex(where: { $0 == "." || $0 == ")" }), t[..<dot].allSatisfy(\.isNumber), !t[..<dot].isEmpty { return false }
            return true
        }
    }

    /// `![tex](persona-math:…)` as the TeX in a code span.
    static func inlineTeX(_ md: String) -> String {
        var out = md
        let marker = "](\(MathMarkdown.scheme):"
        while let hit = out.range(of: marker), let open = out[..<hit.lowerBound].range(of: "![", options: .backwards),
              let close = out[hit.upperBound...].firstIndex(of: ")") {
            let tex = String(out[hit.upperBound..<close]).removingPercentEncoding ?? String(out[open.upperBound..<hit.lowerBound])
            out.replaceSubrange(open.lowerBound...close, with: "`" + tex + "`")
        }
        return out
    }

    /// One of the pieces a long code block is drawn in (`codePieces`): the next one follows it closely.
    static func isCodePiece(_ part: ExportPart) -> Bool {
        guard case .markdown(let md) = part, !md.contains("\n\n"), md.hasSuffix("```") || md.hasSuffix("~~~") else { return false }
        return MarkdownFence.opening(md.prefix { $0 != "\n" }.trimmingCharacters(in: .whitespaces)) != nil
    }

    /// A long fenced block as several, `codeLines` each, in the same language.
    static func codePieces(_ lines: [String], fence opening: String) -> [ExportPart] {
        let marker = String(opening.trimmingCharacters(in: .whitespaces).prefix { $0 == "`" || $0 == "~" })
        let closed = lines.count > 1 && lines.last.map { $0.trimmingCharacters(in: .whitespaces).hasPrefix(marker) } == true
        let body = Array(lines.dropFirst().dropLast(closed ? 1 : 0))
        return stride(from: 0, to: body.count, by: codeLines).map { start in
            .markdown(([opening] + body[start..<min(start + codeLines, body.count)] + [marker]).joined(separator: "\n"))
        }
    }
}

/// Where a long picture's pages break (#9468): blocks (the heading, each message's parts) stacked with `gap` between
/// them, each page holding `room` points of them; a block that does not fit what is left of a page starts the next
/// one, and one taller than a page is cut where `cut` says (a blank row of its pixels), on as many pages as it takes.
nonisolated enum ExportPages {
    struct Slice: Equatable, Sendable {
        var block: Int
        var from: CGFloat
        var to: CGFloat
        var height: CGFloat { to - from }
    }

    /// Less than this left on a page is not worth starting a cut block on.
    static let least: CGFloat = 120

    /// `cut(block, from, limit)`: where to end a slice of `block` that starts at `from` and may run to `limit` (a point
    /// in (from, limit]); `gaps[i]`: the room before block i (none before a page's first).
    static func split(_ heights: [CGFloat], gaps: [CGFloat], room: CGFloat,
                      cut: (Int, CGFloat, CGFloat) -> CGFloat = { _, _, limit in limit }) -> [[Slice]] {
        var pages: [[Slice]] = []
        var page: [Slice] = []
        var used: CGFloat = 0
        func next() {
            if !page.isEmpty { pages.append(page) }
            page = []
            used = 0
        }
        for (i, h) in heights.enumerated() where h > 0 {
            let gap = page.isEmpty ? 0 : (i < gaps.count ? gaps[i] : 0)
            if used + gap + h <= room {
                page.append(Slice(block: i, from: 0, to: h))
                used += gap + h
                continue
            }
            if h <= room {
                next()
                page.append(Slice(block: i, from: 0, to: h))
                used = h
                continue
            }
            // Taller than a page: from what is left of this one (if worth it), then page by page.
            var from: CGFloat = 0
            if room - used - gap < least { next() }
            while from < h {
                let lead = page.isEmpty ? 0 : (i < gaps.count ? gaps[i] : 0)
                let left = room - used - lead
                if h - from <= left {
                    page.append(Slice(block: i, from: from, to: h))
                    used += lead + h - from
                    break
                }
                let to = min(max(cut(i, from, from + left), from + least), from + left)
                page.append(Slice(block: i, from: from, to: to))
                from = to
                next()
            }
        }
        next()
        return pages
    }
}
#endif
