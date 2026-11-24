import Foundation
import Testing

/// A presented page leaves at its top leading corner through the design system's `SheetClose`, and nothing at its
/// trailing corner closes it (#9653, docs/design/DESIGN_SYSTEM.md sections 3.5 and 3.6). Two counts per file under
/// `Persona/` may only go down:
/// - `ownClose`: a close the page writes itself instead of `SheetClose` / `sheetCancel`: any item at
///   `.cancellationAction` (an accent Cancel there read 2–3:1 in dark, #9634), or a ✕, Cancel, Close or Done at
///   `.topBarLeading` / `.navigation`;
/// - `trailingClose`: a ✕, Cancel, Close or Done at `.confirmationAction`, `.topBarTrailing` or `.primaryAction` (a
///   page that only shows things closes with its ✕; a Done that saves is `SheetConfirm`'s ✓).
/// Mac-only branches are left out (the Mac app is gone, #7228). `SheetCloseBaseline.json` next to this file holds the
/// counts; a file over its count fails. When a change lowers counts the test fails too and says so: delete the
/// baseline, rerun to write it again, and commit it with the change.
struct SheetCloseTests {
    private static let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
    private static let baselineURL = here.appending(path: "SheetCloseBaseline.json")
    /// Where `SheetClose`, `SheetConfirm` and `sheetCancel` themselves live.
    private static let home = "SheetActions.swift"
    static let metrics = ["ownClose", "trailingClose"]

    private static let item = try! NSRegularExpression(pattern: #"ToolbarItem(?:Group)?\s*\(\s*placement:\s*\.(\w+)"#)
    /// A button that only closes: ✕, a close or cancel role, the words Cancel or Close (a catalog key may carry a
    /// bracketed note), or Done whose action is one plain statement (`dismiss()`, `shown = nil`). A Done that does more
    /// is a page's own action: Select's Done in a list, an Inbox item's Mark as Done.
    private static let closes = try! NSRegularExpression(
        pattern: #"Icon\.close\b|role: *\.(?:close|cancel)\b|"(?:Cancel|Close)(?: \[[^"\]]*\])?""#
            + #"|Button\((?:String\(localized: *)?"Done(?: \[[^"\]]*\])?"\)?\)\s*\{\s*[^{}\n;]+?\s*\}"#
            + #"|Button\s*\{\s*[^{}\n;]+?\s*\}\s*label:\s*\{[^{}]*?"Done(?: \[[^"\]]*\])?""#)
    private static let leading: Set = ["cancellationAction", "topBarLeading", "navigation"]
    private static let trailing: Set = ["confirmationAction", "topBarTrailing", "primaryAction"]

    static func counts() throws -> [String: [String: Int]] {
        var out: [String: [String: Int]] = [:]
        // The app's own sources and its feature modules' (apple/Modules, docs/apple/ios.md "Modules").
        for dir in ["Persona", "Modules"] {
            let files = FileManager.default.enumerator(at: here.deletingLastPathComponent().appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL }
                .filter { $0.pathExtension == "swift" && $0.lastPathComponent != home } ?? []
            for url in files {
                let c = count(iPhoneSource(try String(contentsOf: url, encoding: .utf8)))
                if !c.isEmpty { out["\(dir)/\(url.path.components(separatedBy: "/\(dir)/").last!)"] = c }
            }
        }
        return out
    }

    /// The counts of one file's source.
    static func count(_ text: String) -> [String: Int] {
        let s = text as NSString
        var c: [String: Int] = [:]
        for m in item.matches(in: text, range: NSRange(location: 0, length: s.length)) {
            let placement = s.substring(with: m.range(at: 1))
            let body = block(s, from: m.range.upperBound) as NSString
            let closesHere = closes.firstMatch(in: body as String, range: NSRange(location: 0, length: body.length)) != nil
            if placement == "cancellationAction" || (leading.contains(placement) && closesHere) {
                c["ownClose", default: 0] += 1
            } else if trailing.contains(placement) && closesHere {
                c["trailingClose", default: 0] += 1
            }
        }
        return c
    }

    /// The braced block that starts after `from`: the item's content.
    private static func block(_ s: NSString, from: Int) -> String {
        let open = s.range(of: "{", range: NSRange(location: from, length: s.length - from))
        guard open.location != NSNotFound else { return "" }
        var depth = 0
        for i in open.location..<s.length {
            switch s.character(at: i) {
            case 0x7B: depth += 1
            case 0x7D:
                depth -= 1
                if depth == 0 { return s.substring(with: NSRange(location: open.location, length: i - open.location + 1)) }
            default: break
            }
        }
        return s.substring(from: open.location)
    }

    /// The source as the iPhone and iPad build sees it: Mac-only `#if` branches and whole-line comments dropped.
    static func iPhoneSource(_ text: String) -> String {
        let macOnly: Set = ["os(macOS)", "canImport(AppKit)"]
        let notMac: Set = ["!os(macOS)", "os(iOS)", "canImport(UIKit)", "os(iOS) || os(visionOS)"]
        // One frame per open `#if`: whether its first branch is about the Mac, and whether the current branch is Mac-only.
        var frames: [(firstIsMac: Bool?, macBranch: Bool)] = []
        var kept: [Substring] = []
        func condition(_ t: String, after n: Int) -> String {
            String(t.dropFirst(n)).components(separatedBy: "//")[0].trimmingCharacters(in: .whitespaces)
        }
        for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix("#if ") {
                let cond = condition(t, after: 4)
                let first: Bool? = macOnly.contains(cond) ? true : notMac.contains(cond) ? false : nil
                frames.append((first, first == true))
            } else if t.hasPrefix("#elseif "), !frames.isEmpty {
                frames[frames.count - 1].macBranch = macOnly.contains(condition(t, after: 8))
            } else if t == "#else", !frames.isEmpty {
                frames[frames.count - 1].macBranch = frames[frames.count - 1].firstIsMac == false
            } else if t.hasPrefix("#endif"), !frames.isEmpty {
                frames.removeLast()
            } else if !t.hasPrefix("//"), !frames.contains(where: \.macBranch) {
                kept.append(line)
            }
        }
        return kept.joined(separator: "\n")
    }

    @Test func presentedPagesCloseAtTheLeadingCorner() throws {
        let now = try Self.counts()
        guard let data = try? Data(contentsOf: Self.baselineURL) else {
            let enc = JSONEncoder()
            enc.outputFormatting = [.prettyPrinted, .sortedKeys]
            try enc.encode(now).write(to: Self.baselineURL)
            Issue.record("wrote \(Self.baselineURL.lastPathComponent); commit it")
            return
        }
        let base = try JSONDecoder().decode([String: [String: Int]].self, from: data)
        var up: [String] = [], down: [String] = []
        for file in Set(now.keys).union(base.keys).sorted() {
            for m in Self.metrics {
                let a = base[file]?[m] ?? 0, b = now[file]?[m] ?? 0
                if b > a { up.append("\(file) \(m): \(a) → \(b)") }
                if b < a { down.append("\(file) \(m): \(a) → \(b)") }
            }
        }
        #expect(up.isEmpty, """
            a presented page's close went outside SheetClose or to the trailing edge; use SheetClose / sheetCancel at the \
            leading corner and SheetConfirm for a Save (docs/design/DESIGN_SYSTEM.md 3.6):
            \(up.joined(separator: "\n"))
            """)
        #expect(down.isEmpty, "counts went down, good: delete SheetCloseBaseline.json and rerun to record them:\n\(down.joined(separator: "\n"))")
    }

    /// The counting itself, on small sources: what counts, and what a Mac-only branch hides.
    @Test func countsWhatItShould() {
        let own = #"ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }"#
        #expect(Self.count(own) == ["ownClose": 1])
        let trailingX = #"ToolbarItem(placement: .topBarTrailing) { Button { x = false } label: { Label("Close", systemImage: Theme.Icon.close) } }"#
        #expect(Self.count(trailingX) == ["trailingClose": 1])
        let done = #"ToolbarItem(placement: .confirmationAction) { Button { dismiss() } label: { Text("Done [Tool Views 1a2b3c]") } }"#
        #expect(Self.count(done) == ["trailingClose": 1])
        let verb = #"ToolbarItem(placement: .confirmationAction) { Button("Send") { send() } }"#
        #expect(Self.count(verb).isEmpty)
        let doneLabel = #"ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { app.open = nil } }"#
        #expect(Self.count(doneLabel) == ["trailingClose": 1])
        let selectDone = #"ToolbarItem(placement: .topBarTrailing) { Button("Done") { withAnimation(Theme.Motion.standard) { picked = nil } } }"#
        #expect(Self.count(selectDone).isEmpty)
        let markDone = "ToolbarItem(placement: .topBarTrailing) { Button {\n store.setDone(item, true)\n dismiss()\n } label: { Label(\"Done [Inbox View 59f6c8]\", systemImage: Theme.Icon.done) } }"
        #expect(Self.count(markDone).isEmpty)
        let menu = #"ToolbarItem(placement: .topBarTrailing) { Menu { Button("Rename") {} } label: { ThemedIcon(Theme.Icon.more) } }"#
        #expect(Self.count(menu).isEmpty)
        let back = #"ToolbarItem(placement: .topBarLeading) { Button { pop() } label: { ThemedIcon(Theme.Icon.back) } }"#
        #expect(Self.count(back).isEmpty)
        let mac = "#if os(macOS)\n\(own)\n#else\n\(verb)\n#endif"
        #expect(Self.count(Self.iPhoneSource(mac)).isEmpty)
        let iOS = "#if !os(macOS)\n\(trailingX)\n#else\n\(own)\n#endif"
        #expect(Self.count(Self.iPhoneSource(iOS)) == ["trailingClose": 1])
    }
}
