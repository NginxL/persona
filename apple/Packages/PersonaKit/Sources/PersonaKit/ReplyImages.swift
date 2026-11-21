import CoreGraphics
import Foundation

/// A picture a reply names by address (Markdown `![alt](https://…)` or `![alt](path/on/the/machine.png)`).
public nonisolated struct ReplyImage: Sendable, Hashable {
    public let url: String
    public let alt: String
    /// The decoded machine path when `url` is an absolute or cwd-relative local destination; nil for web and other schemes.
    public let localPath: String?

    public init(url: String, alt: String) {
        self.url = url
        self.alt = alt
        localPath = URL(string: url).flatMap(PathLinkURL.localPath)
    }

    public var isLocal: Bool { localPath != nil }
}

/// One run of pictures as the theme reads it back from its fence (`ReplyImages.apply`): every picture of the reply in
/// order (what the viewer pages through) and which of them this run draws.
public nonisolated struct ReplyImageBlock: Sendable, Equatable {
    public let all: [ReplyImage]
    public let shown: Range<Int>

    public init(all: [ReplyImage], shown: Range<Int>) {
        self.all = all
        self.shown = shown
    }

    public var images: ArraySlice<ReplyImage> { all[shown] }
}

/// The pictures in a reply, drawn to fit the text (#9474; the web's are the same rules, #9475). A line that holds only
/// pictures on the web (http or https) or the session machine is a block picture, however it sits in its paragraph; block
/// pictures with only line breaks or blank lines between them are one run. Before the reply is parsed each run becomes one
/// fenced block in `language`, which the theme draws (one picture, or a grid). A web picture inside a line of text stays in
/// it; a local one is lifted into its own block so the machine-file loader and failure reason are visible. Code (fenced
/// or indented) and quotes are left as they are.
public nonisolated enum ReplyImages {
    /// The fence's language: the theme's code block draws it as pictures.
    public static let language = "persona-images"
    static let fence = "~~~~"

    /// `text` with each run of block pictures replaced by one `language` fence. The fence holds `show <first> <last>`
    /// (this run's pictures, by their place among all of the reply's), then every picture of the reply, one per line,
    /// `<url>\t<alt>`: a block knows the whole reply's pictures, for the viewer, wherever it is drawn.
    public static func apply(_ text: String) -> String {
        guard text.contains("![") else { return text }
        let lines = Self.liftLocalInline(text)
        let scan = Self.scan(lines)
        guard scan.blocks.contains(true) else { return text }
        var out: [String] = []
        var i = 0
        while i < lines.count {
            guard scan.blocks[i] else {
                out.append(lines[i])
                i += 1
                continue
            }
            // The run: this line, and every later block line of the same indent with only blank lines between.
            let indent = Self.indentPrefix(lines[i])
            var last = i
            var j = i + 1
            while j < lines.count {
                if scan.blocks[j], Self.indentPrefix(lines[j]) == indent {
                    last = j
                } else if !lines[j].trimmingCharacters(in: .whitespaces).isEmpty {
                    break
                }
                j += 1
            }
            let first = scan.firstImage[i]
            let end = scan.firstImage[last] + scan.count[last] - 1
            if let prev = out.last, !prev.trimmingCharacters(in: .whitespaces).isEmpty { out.append("") }
            out.append(indent + fence + language)
            out.append(indent + "show \(first) \(end)")
            for image in scan.all {
                out.append(indent + image.url + "\t" + image.alt)
            }
            out.append(indent + fence)
            if last + 1 < lines.count, !lines[last + 1].trimmingCharacters(in: .whitespaces).isEmpty { out.append("") }
            i = last + 1
        }
        return out.joined(separator: "\n")
    }

    /// Every web or machine-local picture the reply names outside code, in order (block and inline).
    public static func all(in text: String) -> [ReplyImage] {
        guard text.contains("![") else { return [] }
        return scan(text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)).all
    }

    /// The run a `language` fence's content describes, or nil when it does not read.
    public static func block(_ content: String) -> ReplyImageBlock? {
        var lines = content.split(separator: "\n", omittingEmptySubsequences: true).map(String.init)
        guard let head = lines.first?.split(separator: " "), head.count == 3, head[0] == "show",
              let first = Int(head[1]), let last = Int(head[2]), first >= 0, last >= first else { return nil }
        lines.removeFirst()
        let all = lines.map { line -> ReplyImage in
            let parts = line.split(separator: "\t", maxSplits: 1, omittingEmptySubsequences: false)
            return ReplyImage(url: String(parts[0]).trimmingCharacters(in: .whitespaces), alt: parts.count > 1 ? String(parts[1]) : "")
        }
        guard last < all.count, all.allSatisfy({ isWeb($0.url) || $0.isLocal }) else { return nil }
        return ReplyImageBlock(all: all, shown: first ..< last + 1)
    }

    /// Whether `url` is a picture on the web (http or https).
    public static func isWeb(_ url: String) -> Bool {
        let lower = url.lowercased()
        return (lower.hasPrefix("https://") || lower.hasPrefix("http://")) && url.count > 8
    }

    /// Whether `url` is an absolute or cwd-relative file on the session's machine, rather than a web or custom-scheme image.
    public static func isLocal(_ url: String) -> Bool {
        URL(string: url).flatMap(PathLinkURL.localPath) != nil
    }

    // MARK: Scanning

    /// Lift a local image out of a line of text so the chat's machine-file tile can show it (and any failure) with a
    /// manual retry. Web images keep their inline treatment; the words before and after stay in their original order.
    static func liftLocalInline(_ text: String) -> [String] {
        var out: [String] = []
        var open: MarkdownFence?
        var previousBlank = true
        for line in text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            defer { previousBlank = trimmed.isEmpty }
            if let f = open {
                out.append(line)
                if f.isClosed(by: trimmed) { open = nil }
                continue
            }
            let indent = Self.indentWidth(line)
            if indent < 4, let f = MarkdownFence.opening(trimmed) {
                open = f
                out.append(line)
                continue
            }
            if indent >= 4 && previousBlank || trimmed.hasPrefix(">") {
                out.append(line)
                continue
            }
            out.append(contentsOf: Self.liftLocalInlineLine(line))
        }
        return out
    }

    static func liftLocalInlineLine(_ line: String) -> [String] {
        let chars = Array(line)
        var pieces: [String] = []
        var start = 0
        var i = 0
        var inCode = false
        while i < chars.count {
            let c = chars[i]
            if c == "`" { inCode.toggle() }
            if !inCode, c == "!", i + 1 < chars.count, chars[i + 1] == "[", (i == 0 || chars[i - 1] != "["),
               let (image, next) = Self.image(chars, at: i), image.isLocal {
                let before = String(chars[start..<i]).replacingOccurrences(of: #"\s+$"#, with: "", options: .regularExpression)
                if !before.isEmpty { pieces.append(before) }
                pieces.append(String(chars[i..<next]))
                start = next
                i = next
                continue
            }
            i += 1
        }
        guard !pieces.isEmpty else { return [line] }
        let rest = String(chars[start...]).replacingOccurrences(of: #"^\s+"#, with: "", options: .regularExpression)
        if !rest.isEmpty { pieces.append(rest) }
        return pieces
    }

    struct Scan {
        var all: [ReplyImage] = []
        /// Per line: it holds only supported pictures (and spaces), outside code and quotes.
        var blocks: [Bool] = []
        /// Per line: the place of its first picture among `all`, and how many it has.
        var firstImage: [Int] = []
        var count: [Int] = []
    }

    static func scan(_ lines: [String]) -> Scan {
        var s = Scan()
        var open: MarkdownFence?
        var previousBlank = true
        for line in lines {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            s.firstImage.append(s.all.count)
            defer { previousBlank = trimmed.isEmpty }
            if let f = open {
                if f.isClosed(by: trimmed) { open = nil }
                s.blocks.append(false)
                s.count.append(0)
                continue
            }
            let indent = Self.indentWidth(line)
            if indent < 4, let f = MarkdownFence.opening(trimmed) {
                open = f
                s.blocks.append(false)
                s.count.append(0)
                continue
            }
            if indent >= 4 && previousBlank || trimmed.hasPrefix(">") {
                // indented code, or a quote: left to the renderer as it is
                s.blocks.append(false)
                s.count.append(0)
                continue
            }
            let (images, rest) = Self.images(in: trimmed)
            let supported = images.filter { isWeb($0.url) || $0.isLocal }
            s.all.append(contentsOf: supported)
            s.count.append(supported.count)
            s.blocks.append(!images.isEmpty && supported.count == images.count && indent < 4
                && rest.trimmingCharacters(in: .whitespaces).isEmpty)
        }
        return s
    }

    /// The pictures in `line` and what is left of it without them. A picture is `![alt](url)` or `![alt](url "title")`,
    /// the address possibly in `<…>`; a picture inside a link (`[![a](x)](y)`) or inline code is left in the rest.
    static func images(in line: String) -> ([ReplyImage], String) {
        guard line.contains("![") else { return ([], line) }
        var images: [ReplyImage] = []
        var rest = ""
        let chars = Array(line)
        var i = 0
        var inCode = false
        while i < chars.count {
            let c = chars[i]
            if c == "`" { inCode.toggle() }
            if !inCode, c == "!", i + 1 < chars.count, chars[i + 1] == "[", (i == 0 || chars[i - 1] != "["),
               let (image, next) = Self.image(chars, at: i) {
                images.append(image)
                i = next
                continue
            }
            rest.append(c)
            i += 1
        }
        return (images, rest)
    }

    /// The picture whose `![` is at `start`, and the index after its `)`.
    static func image(_ chars: [Character], at start: Int) -> (ReplyImage, Int)? {
        var i = start + 2
        var alt = ""
        while i < chars.count, chars[i] != "]" {
            if chars[i] == "[" || chars[i] == "\n" { return nil }
            alt.append(chars[i] == "\t" ? " " : chars[i])
            i += 1
        }
        guard i + 1 < chars.count, chars[i + 1] == "(" else { return nil }
        i += 2
        while i < chars.count, chars[i] == " " { i += 1 }
        var url = ""
        if i < chars.count, chars[i] == "<" {
            i += 1
            while i < chars.count, chars[i] != ">" {
                url.append(chars[i])
                i += 1
            }
            guard i < chars.count else { return nil }
            i += 1
        } else {
            while i < chars.count, chars[i] != " ", chars[i] != ")" {
                url.append(chars[i])
                i += 1
            }
        }
        while i < chars.count, chars[i] == " " { i += 1 }
        if i < chars.count, chars[i] == "\"" || chars[i] == "'" { // a title: skipped
            let quote = chars[i]
            i += 1
            while i < chars.count, chars[i] != quote { i += 1 }
            guard i < chars.count else { return nil }
            i += 1
            while i < chars.count, chars[i] == " " { i += 1 }
        }
        guard i < chars.count, chars[i] == ")", !url.isEmpty, !url.contains("\t") else { return nil }
        return (ReplyImage(url: url, alt: alt.trimmingCharacters(in: .whitespaces)), i + 1)
    }

    static func indentPrefix(_ line: String) -> String { String(line.prefix(while: { $0 == " " || $0 == "\t" })) }

    static func indentWidth(_ line: String) -> Int {
        var n = 0
        for c in line {
            if c == " " { n += 1 } else if c == "\t" { n += 4 } else { break }
        }
        return n
    }
}

/// Where a reply's pictures go and what size to ask for (#9474; the web's `reply-images.ts` keeps the same numbers).
/// One picture is a fixed height (so its box is its final height before it arrives; only its width changes), as wide
/// as its shape allows from 3:4 up to the column, and never larger than itself. Two are squares side by side; three or
/// more are squares three to a row (four as 2 × 2), at most two rows, the sixth showing how many are not drawn.
public nonisolated enum ReplyImageLayout {
    /// The sizes the hub draws (`GET /api/v1/remote-image`, docs/protocol/protocol.md "Remote images (#9473)").
    public static let sizes = [256, 512, 768, 1024, 1536, 2048]
    /// The narrowest a lone picture is drawn, as a share of its height (3:4).
    public static let narrowest: CGFloat = 3 / 4
    /// A lone picture's box before it arrives, as a share of its height (4:3).
    public static let placeholder: CGFloat = 4 / 3
    /// At most this many squares are drawn; the last of them says how many more there are.
    public static let mostShown = 6

    /// The hub's size for a picture drawn `points` on its long side at `scale`: the smallest of `sizes` that covers it.
    public static func size(points: CGFloat, scale: CGFloat) -> Int {
        let px = points * max(1, scale)
        return sizes.first { CGFloat($0) >= px } ?? sizes[sizes.count - 1]
    }

    /// A lone picture's frame in a column `column` wide: `height` tall and its shape's width (between 3:4 and the
    /// column) once its `pixels` are known, the placeholder's 4:3 before. A picture smaller than that box keeps its own
    /// size (a pixel a point, as the web counts it).
    public static func single(pixels: CGSize?, column: CGFloat, height: CGFloat) -> CGSize {
        let column = max(1, column)
        guard let p = pixels, p.width > 0, p.height > 0 else {
            return CGSize(width: min(column, height * placeholder), height: height)
        }
        if p.height <= height, p.width <= column { return p }
        let width = min(column, max(height * narrowest, height * p.width / p.height))
        return CGSize(width: width, height: height)
    }

    /// The squares of a run of `count` pictures (2 or more) in a column `column` wide, at most `widest`.
    public struct Grid: Equatable, Sendable {
        public let columns: Int
        public let tile: CGFloat
        /// How many pictures are drawn (the rest are counted on the last).
        public let shown: Int
        /// How many are not drawn (0: all are).
        public let more: Int

        public var rows: Int { (shown + columns - 1) / columns }
    }

    public static func grid(count: Int, column: CGFloat, widest: CGFloat, gap: CGFloat) -> Grid {
        let width = max(1, min(column, widest))
        if count <= 2 { return Grid(columns: 2, tile: max(1, (width - gap) / 2), shown: max(0, count), more: 0) }
        let tile = max(1, (width - 2 * gap) / 3)
        if count == 4 { return Grid(columns: 2, tile: tile, shown: 4, more: 0) }
        let shown = min(count, mostShown)
        return Grid(columns: 3, tile: tile, shown: shown, more: count - shown)
    }
}
