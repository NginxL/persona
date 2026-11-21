import Foundation

/// A path a message names (docs/apple/ios.md「File viewer」), with the line it points at: `path:12`, `path:12:5`,
/// `path:12-20` (the first line), `path#L12`. The same rules as the web's `web/src/lib/path-refs.ts`.
public nonisolated struct PathRef: Hashable, Sendable {
    /// Where it is in the text (UTF-16): the path and its line.
    public var range: NSRange
    /// The path as written, without the line: what the machine is asked about (`FilePresence`).
    public var path: String
    public var line: Int?
    public var column: Int?
    /// A code span that is one path (`~/My Notes/a.md`), not an ASCII path found in text.
    public var whole = false
}

/// Finds the paths in text:
///
/// - `~/x`, `/x/y` (two parts at least: `/tmp` alone is a word), or relative with a folder and an extension (`src/a.ts`;
///   a letter-led extension, so `deepseek/v4.1` is not a file).
/// - Its names may be in any script (「~/报告/第四季度.docx」, 「~/ドキュメント/メモ.txt」). Outside backticks it ends at a
///   space, at punctuation other than `_ . @ + -` (full-width 「，。、（）」 too) and before a Chinese word right after an
///   extension (「/tmp/a.md里」); a relative one starts with an ASCII folder (「notes/报告.md」), so the words before it stay
///   out; a sentence's full stop after it is not part of it. In backticks the whole span is one, whatever it is written
///   in, spaces too; the paths in a span are candidates too (「/a.md 和 /b.md」): what is there decides.
/// - Not inside a URL or a longer word.
public nonisolated enum PathRefs {
    /// A name: ASCII, or a letter or digit of any other script not right after an extension. The two never take the
    /// same character: an ASCII letter or digit could otherwise go either way, and a long run of them that ends in no
    /// match (a hash with no extension) is tried every way, which hangs the chat (#8903, the web's #8894).
    private static let seg = #"(?:[A-Za-z0-9_.@+\-]|(?<!\.[A-Za-z][A-Za-z0-9]{0,7})(?![A-Za-z0-9])[\p{L}\p{M}\p{N}])+"#
    private static let line = #"(?::(\d{1,7})(?::(\d{1,5}))?(?:[-–]\d{1,7})?|#L(\d{1,7})(?:-L?\d{1,7})?)?"#
    /// A relative path starts with an ASCII folder, so the Chinese words before it stay out.
    private static let text = try! NSRegularExpression(pattern:
        #"(?<![A-Za-z0-9_/:.\-~])(~(?:/"# + seg + #")+/?|/"# + seg + "(?:/" + seg + #")+/?|[A-Za-z0-9_.@\-]+(?:/"# + seg
        + #")+\.[A-Za-z][A-Za-z0-9]{0,7})"# + line + #"(?![A-Za-z0-9_/\-])"#)
    private static let codeSpan = try! NSRegularExpression(pattern: "`([^`\\n]+)`")
    /// A line at the end of a code span (`~/My Notes/a.md:3`).
    private static let trailingLine = try! NSRegularExpression(pattern: "(?:" + line.dropFirst(3).dropLast(2) + ")$")
    /// A whole code span that is a path: `~/` or `/` then words separated by single spaces.
    private static let whole = try! NSRegularExpression(pattern: "^~?/[^\\s`]+(?: [^\\s`]+)*$")
    private static let fullStops = try! NSRegularExpression(pattern: "(?<=[^./])\\.+$")

    /// The paths in plain text, in order (the longer first at one place), at most `limit`. All of it within
    /// `BoundedRegex.budget`: past that, what was found by then (#9108: a row's paths kept a recording's app at full CPU
    /// in the background until iOS killed it).
    public static func find(in text: String, limit: Int = 40) -> [PathRef] {
        let ns = text as NSString
        let deadline = BoundedRegex.deadline()
        var out: [PathRef] = []
        for m in BoundedRegex.matches(codeSpan, in: text, until: deadline).matches {
            let inner = m.range(at: 1)
            if let r = codePathRef(ns.substring(with: inner), until: deadline) { out.append(shifted(r, by: inner.location)) }
        }
        for r in textRefs(in: text, until: deadline) where !out.contains(where: { $0.range == r.range }) { out.append(r) }
        out.sort { $0.range.location != $1.range.location ? $0.range.location < $1.range.location : $0.range.length > $1.range.length }
        return Array(out.prefix(limit))
    }

    /// The paths of text as outside backticks, there or in them, as far as they are found by `deadline`.
    public static func textRefs(in text: String, until deadline: ContinuousClock.Instant = BoundedRegex.deadline()) -> [PathRef] {
        let ns = text as NSString
        return BoundedRegex.matches(Self.text, in: text, until: deadline).matches.compactMap { ref(m: $0, in: ns) }
    }

    /// The path a whole code span is (`/tmp/a.md`, `src/a.ts:12`, 「~/报告/第四季度.docx」), or nil; its range is in `code`.
    /// Nil too when `deadline` comes first.
    public static func codePathRef(_ code: String, until deadline: ContinuousClock.Instant = BoundedRegex.deadline()) -> PathRef? {
        let ns = code as NSString
        let lead = ns.length - (code.drop { $0 == " " } as Substring).utf16.count
        let t = code.trimmingCharacters(in: .whitespaces)
        let tn = t as NSString
        guard !t.isEmpty, !t.hasPrefix("//"), !t.contains("://") else { return nil }
        // Whole: the line taken off its end, the rest a path from the root or home, Chinese and spaces too.
        var path = t, lineNo: Int?, col: Int?
        if let m = BoundedRegex.firstMatch(trailingLine, in: t, until: deadline), m.range.length > 0 {
            path = tn.substring(to: m.range.location)
            lineNo = number(m, 1, tn) ?? number(m, 3, tn)
            col = lineNo == nil ? nil : number(m, 2, tn)
        }
        if BoundedRegex.firstMatch(whole, in: path, until: deadline) != nil {
            let parts = path.split(separator: "/").count
            if parts >= (path.hasPrefix("~") ? 1 : 2), !isRoot(path) {
                return PathRef(range: NSRange(location: lead, length: tn.length), path: path, line: lineNo == 0 ? nil : lineNo,
                               column: lineNo == nil || lineNo == 0 ? nil : col, whole: true)
            }
        }
        // Or one path that is the whole span (`ios/Persona/Foo.swift`, 「docs/设计/说明.md」).
        if var r = textRefs(in: t, until: deadline).first, r.range.location == 0, r.range.length == tn.length {
            r.whole = true
            return shifted(r, by: lead)
        }
        return nil
    }

    /// Each path once, in order, for asking the machine.
    public static func unique(_ refs: [PathRef], limit: Int = 24) -> [String] { unique(refs.map(\.path), limit: limit) }

    public static func unique(_ paths: [String], limit: Int) -> [String] {
        var out: [String] = []
        for p in paths where !out.contains(p) {
            out.append(p)
            if out.count >= limit { break }
        }
        return out
    }

    /// The refs to link: the ones `linkable` says yes to, in order, none overlapping (the first, the longer at one place).
    public static func pick(_ refs: [PathRef], linkable: (String) -> Bool) -> [PathRef] {
        var out: [PathRef] = []
        var at = 0
        for r in refs where r.range.location >= at && linkable(r.path) {
            out.append(r)
            at = NSMaxRange(r.range)
        }
        return out
    }

    /// A streaming text without its last word, which may be a path still being written (`/tmp/jar…`).
    public static func written(_ text: String) -> String {
        guard let r = text.range(of: #"\s\S*$"#, options: .regularExpression) else { return "" }
        return String(text[..<r.lowerBound])
    }

    private static func ref(m: NSTextCheckingResult, in ns: NSString) -> PathRef? {
        var path = ns.substring(with: m.range(at: 1))
        let hasLine = m.range.length > m.range(at: 1).length
        // A sentence's full stop (or an ellipsis) is not part of the path, unless the path is only dots.
        if !hasLine { path = fullStops.stringByReplacingMatches(in: path, range: NSRange(location: 0, length: (path as NSString).length), withTemplate: "") }
        guard !path.isEmpty, path != "~", !isRoot(path) else { return nil }
        let lineNo = number(m, 2, ns) ?? number(m, 4, ns)
        let length = hasLine ? m.range.length : (path as NSString).length
        return PathRef(range: NSRange(location: m.range.location, length: length), path: path,
                       line: lineNo == 0 ? nil : lineNo, column: lineNo == nil || lineNo == 0 ? nil : number(m, 3, ns))
    }

    private static func isRoot(_ p: String) -> Bool { p.range(of: "^~?/+$", options: .regularExpression) != nil }

    private static func number(_ m: NSTextCheckingResult, _ group: Int, _ ns: NSString) -> Int? {
        let r = m.range(at: group)
        return r.location == NSNotFound ? nil : Int(ns.substring(with: r))
    }

    private static func shifted(_ r: PathRef, by n: Int) -> PathRef {
        var r = r
        r.range.location += n
        return r
    }
}

/// The link a path in a message is drawn with (`persona-path:<path>?line=N`): the chat's `openURL` opens it in the
/// file viewer, at its line.
public nonisolated enum PathLinkURL {
    public static let scheme = "persona-path"
    /// ASCII letters and digits and `/-._~` stay; everything else (spaces, Chinese, parentheses) is percent-encoded,
    /// so the link stays one Markdown destination.
    private static let kept = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789/-._~")

    public static func make(_ path: String, line: Int?) -> URL? {
        guard let p = path.addingPercentEncoding(withAllowedCharacters: kept) else { return nil }
        return URL(string: scheme + ":" + p + (line.map { "?line=\($0)" } ?? ""))
    }

    public static func parse(_ url: URL) -> (path: String, line: Int?)? {
        guard url.scheme == scheme else { return nil }
        var rest = String(url.absoluteString.dropFirst(scheme.count + 1))
        var line: Int?
        if let q = rest.range(of: "?line=") {
            line = Int(rest[q.upperBound...])
            rest = String(rest[..<q.lowerBound])
        }
        guard let path = rest.removingPercentEncoding, !path.isEmpty else { return nil }
        return (path, line)
    }

    /// A Markdown destination that names a file on the session's computer, absolute or relative to its cwd. URLs with
    /// a scheme or a host keep their existing handling; in particular http(s), file, data and attachment URLs are not
    /// machine paths here.
    public static func localPath(_ url: URL) -> String? {
        guard url.scheme == nil, url.host == nil else { return nil }
        let path = url.path(percentEncoded: false)
        return path.isEmpty ? nil : path
    }
}

/// An answer's Markdown, ready to have its paths linked (`linked`): where the paths are in it, outside code blocks,
/// formulas and links (written or bare), and the code spans that are a whole path (the span is the link).
public nonisolated struct PathMarkdown: Sendable {
    public struct Spot: Sendable {
        public var range: NSRange
        public var ref: PathRef
        /// A whole code span: linked as written, backticks and all.
        public var code: Bool
    }

    public let source: String
    public let spots: [Spot]
    /// Each path once, for asking the machine.
    public let paths: [String]

    public init(_ source: String, limit: Int = 40) {
        self.source = source
        // One budget for the whole answer, however many lines it has (#9108).
        let deadline = BoundedRegex.deadline()
        var spots: [Spot] = []
        var fence: MarkdownFence?
        var at = 0
        for line in source.split(separator: "\n", omittingEmptySubsequences: false).map(String.init) {
            defer { at += (line as NSString).length + 1 }
            let t = line.trimmingCharacters(in: .whitespaces)
            if let f = fence {
                if f.isClosed(by: t) { fence = nil }
                continue
            }
            if let f = MarkdownFence.opening(t) { fence = f; continue }
            if line.hasPrefix("    ") || line.hasPrefix("\t") { continue } // indented code (as `Links.markdown`)
            for s in Self.spots(in: line, until: deadline) {
                var s = s
                s.range.location += at
                s.ref.range.location += at
                spots.append(s)
            }
            if spots.count >= limit { break }
        }
        self.spots = Array(spots.prefix(limit))
        paths = PathRefs.unique(self.spots.map(\.ref))
    }

    /// The source with the paths `linkable` says yes to as links, and which paths those are. `plainCode`: a whole code
    /// span is linked as its words (Markdown's marks escaped), not as code: where code is drawn in the body's color
    /// (iOS, #2984), a path that opens keeps the link's.
    public func linked(plainCode: Bool = false, _ linkable: (String) -> Bool) -> (markdown: String, paths: Set<String>) {
        let ns = source as NSString
        var out = ""
        var at = 0
        var linked: Set<String> = []
        for s in spots where s.range.location >= at && linkable(s.ref.path) {
            guard let url = PathLinkURL.make(s.ref.path, line: s.ref.line) else { continue }
            out += ns.substring(with: NSRange(location: at, length: s.range.location - at))
            out += "[" + (s.code && plainCode ? Self.words(of: ns.substring(with: s.range)) : ns.substring(with: s.range)) + "](" + url.absoluteString + ")"
            at = NSMaxRange(s.range)
            linked.insert(s.ref.path)
        }
        guard !linked.isEmpty else { return (source, []) }
        out += ns.substring(from: at)
        return (out, linked)
    }

    /// A code span's words as Markdown text: the backticks off (and the one space CommonMark takes off each end),
    /// the marks Markdown reads inside a line escaped.
    static func words(of span: String) -> String {
        var inner = span.drop { $0 == "`" }
        while inner.last == "`" { inner = inner.dropLast() }
        var text = String(inner)
        if text.count > 2, text.hasPrefix(" "), text.hasSuffix(" "), !text.allSatisfy({ $0 == " " }) {
            text = String(text.dropFirst().dropLast())
        }
        return text.reduce(into: "") { out, c in
            if "\\`*_[]()<>!|~#$".contains(c) { out.append("\\") }
            out.append(c)
        }
    }

    /// Links written ([text](url), images: the formulas are `![tex](persona-math:…)`), reference links, and <autolinks>
    /// (bare links are made those by `Links.markdown` first), and a line that defines a reference (`[a]: url`): where
    /// paths and code spans are left alone. Ranges in UTF-16, in order.
    ///
    /// The same matches as `!?\[[^\]\n]*\]\([^)\n]*\)|!?\[[^\]\n]*\]\[[^\]\n]*\]|<[^\s<>]+>|^\s*\[[^\]\n]+\]:.*$`, but
    /// in one pass: the expression looked for each `[`'s `]` from scratch, so a line of 8,000 unclosed `[` took 1.5 s
    /// (#9173), with no time limit when copying code (`CodeCopy`).
    static func protectedRanges(in line: String) -> [NSRange] {
        let u = Array(line.utf16)
        let n = u.count
        guard n > 0 else { return [] }
        let newline: UInt16 = 0x0A, open: UInt16 = 0x5B, close: UInt16 = 0x5D, paren: UInt16 = 0x28, closeParen: UInt16 = 0x29
        // From each place, the first `]` (or `)`) before the line ends; n when there is none.
        var nextClose = [Int](repeating: n, count: n + 1), nextParen = [Int](repeating: n, count: n + 1)
        for i in stride(from: n - 1, through: 0, by: -1) {
            nextClose[i] = u[i] == close ? i : u[i] == newline ? n : nextClose[i + 1]
            nextParen[i] = u[i] == closeParen ? i : u[i] == newline ? n : nextParen[i + 1]
        }
        func space(_ c: UInt16) -> Bool { Unicode.Scalar(c)?.properties.isWhitespace ?? false }
        /// `[text](url)` or `[text][ref]` from the `[` at `i`: where it ends (past its last character).
        func bracketed(_ i: Int) -> Int? {
            let j = nextClose[i + 1]
            guard j + 1 < n else { return nil }
            if u[j + 1] == paren, nextParen[j + 2] < n { return nextParen[j + 2] + 1 }
            if u[j + 1] == open, nextClose[j + 2] < n { return nextClose[j + 2] + 1 }
            return nil
        }
        var out: [NSRange] = []
        var i = 0
        while i < n {
            var end: Int?
            if u[i] == 0x21, i + 1 < n, u[i + 1] == open { end = bracketed(i + 1) } // ![image](url)
            if end == nil, u[i] == open { end = bracketed(i) }
            if end == nil, u[i] == 0x3C { // <autolink>
                var k = i + 1
                while k < n, u[k] != 0x3C, u[k] != 0x3E, !space(u[k]) { k += 1 }
                if k < n, u[k] == 0x3E, k > i + 1 { end = k + 1 }
            }
            if end == nil, i == 0 { // [ref]: url, the whole line
                var k = 0
                while k < n, space(u[k]), u[k] != newline { k += 1 }
                if k < n, u[k] == open, nextClose[k + 1] < n, nextClose[k + 1] > k + 1,
                   nextClose[k + 1] + 1 < n, u[nextClose[k + 1] + 1] == 0x3A {
                    var e = n
                    if let nl = u[(nextClose[k + 1] + 2)...].firstIndex(where: { $0 == newline || $0 == 0x0D }) { e = nl }
                    end = e
                }
            }
            if let e = end, e > i {
                out.append(NSRange(location: i, length: e - i))
                i = e
            } else {
                i += 1
            }
        }
        return out
    }

    /// The paths of one line (outside a code block), as far as they are found by `deadline`.
    private static func spots(in line: String, until deadline: ContinuousClock.Instant) -> [Spot] {
        let ns = line as NSString
        let code = codeSpans(Array(line.utf16))
        let links = protectedRanges(in: line)
            .filter { r in !code.contains { NSLocationInRange(r.location, $0.whole) } }
        func covered(_ r: NSRange) -> Bool {
            links.contains { NSIntersectionRange($0, r).length > 0 } || code.contains { NSIntersectionRange($0.whole, r).length > 0 }
        }
        var out: [Spot] = []
        for c in code where !links.contains(where: { NSIntersectionRange($0, c.whole).length > 0 }) {
            guard var r = PathRefs.codePathRef(ns.substring(with: c.inner), until: deadline) else { continue }
            r.range.location += c.inner.location
            out.append(Spot(range: c.whole, ref: r, code: true))
        }
        for r in PathRefs.textRefs(in: line, until: deadline) where !covered(r.range) {
            out.append(Spot(range: r.range, ref: r, code: false))
        }
        return out.sorted { $0.range.location < $1.range.location }
    }

    /// A line's code spans: a run of backticks to the next run of the same length (the span, and what is in it).
    static func codeSpans(_ u: [UInt16]) -> [(whole: NSRange, inner: NSRange)] {
        var spans: [(NSRange, NSRange)] = []
        var i = 0
        while i < u.count {
            guard u[i] == 0x60 else { i += 1; continue }
            var n = 0
            while i + n < u.count, u[i + n] == 0x60 { n += 1 }
            var j = i + n
            var closed = false
            while j < u.count {
                if u[j] == 0x60 {
                    var m = 0
                    while j + m < u.count, u[j + m] == 0x60 { m += 1 }
                    if m == n {
                        spans.append((NSRange(location: i, length: j + m - i), NSRange(location: i + n, length: j - i - n)))
                        i = j + m
                        closed = true
                        break
                    }
                    j += m
                } else {
                    j += 1
                }
            }
            if !closed { i += n }
        }
        return spans
    }
}
