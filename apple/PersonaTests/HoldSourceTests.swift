import Foundation
import Testing

/// Pages keep no hold of their own (docs/apple/ios-state.md「Step 4」): how long a change waits while the user scrolls or
/// cannot see the page, and when it lands, is `Hold`'s; a page only says what it is doing (`Hold.set`). Each page used
/// to wait out its scrolls and cap its waits itself (#131, #178, #215), and one left out came back as a bug. Reads the
/// sources next to this file, like `BackgroundCallbackTests`.
struct HoldSourceTests {
    private static let ios = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    private static let waits = try! NSRegularExpression(pattern: #"Task\.sleep|asyncAfter|Timer\.(scheduled|publish)"#)

    private static func sources(_ dirs: [String]) -> [(name: String, text: String)] {
        dirs.flatMap { dir in
            (FileManager.default.enumerator(at: ios.appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL } ?? [])
                .filter { $0.pathExtension == "swift" && $0.lastPathComponent != "Hold.swift" }
                .compactMap { url in (try? String(contentsOf: url, encoding: .utf8)).map { (url.lastPathComponent, $0) } }
        }
    }

    /// The body of each `{ … }` that starts on a line matching `start` (comments taken out), with its line number.
    private static func blocks(_ text: String, starting start: String) -> [(line: Int, body: String)] {
        let lines = text.components(separatedBy: "\n").map { l -> String in
            l.range(of: "//").map { String(l[..<$0.lowerBound]) } ?? l
        }
        var out: [(Int, String)] = []
        for (n, line) in lines.enumerated() where line.contains(start) {
            var depth = 0, opened = false, body = ""
            scan: for l in lines[n...] {
                for c in l {
                    if c == "{" { depth += 1; opened = true }
                    if c == "}" { depth -= 1 }
                    if opened && depth == 0 { break scan }
                }
                body += l + "\n"
            }
            out.append((n + 1, body))
        }
        return out
    }

    private static func waits(_ s: String) -> Bool {
        waits.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)) != nil
    }

    /// Waiting for a list to rest after a scroll is `Hold.settle`: a page reports the phase and does not wait itself.
    /// The folders it reads are there (after a move, a check reading nothing passes for nothing).
    @Test func readsTheSources() {
        #expect(Self.sources(["Persona/Views"]).count > 50)
    }

    @Test func scrollPhasesAreReportedNotWaitedOut() {
        var found: [String] = []
        for (name, text) in Self.sources(["Persona"]) {
            for b in Self.blocks(text, starting: ".onScrollPhaseChange") where Self.waits(b.body) {
                found.append("\(name):\(b.line)")
            }
        }
        #expect(found.isEmpty, "report the scroll to the page's Hold (`hold.set(.scrolling, phase != .idle)`) instead of waiting:\n\(found.joined(separator: "\n"))")
    }

    /// A page's feed (an object following the model outside SwiftUI, as `AgentsFeed` does) decides what it shows,
    /// not when: no timers in it, the cap and the settling are its `Hold`'s.
    @Test func feedsLeaveTheTimingToTheirHold() {
        var found: [String] = []
        for (name, text) in Self.sources(["Persona/Views"]) {
            for b in Self.blocks(text, starting: "final class ") where b.body.contains("withObservationTracking") && Self.waits(b.body) {
                found.append("\(name):\(b.line)")
            }
        }
        #expect(found.isEmpty, "let a Hold time what waits:\n\(found.joined(separator: "\n"))")
    }

    /// The Agents list goes through its hold: scrolling and sight reported, nothing else.
    @Test func theAgentsListReportsToItsHold() throws {
        let text = try #require(Self.sources(["Persona/Views/Chats"]).first { $0.name == "ChatsView.swift" }?.text)
        #expect(text.contains("feed.hold.set(.scrolling"))
        #expect(text.contains("feed.hold.set(.outOfSight"))
    }
}
