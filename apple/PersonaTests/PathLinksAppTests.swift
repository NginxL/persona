import Foundation
import PersonaKit
import SwiftUI
import Testing
@testable import Persona

// What of the paths in messages is the app's: the bubble's links and the chips' list. PathRefs itself and its tests
// are in PersonaKit (apple/Packages/PersonaKit, #2095).

/// The user's bubble: plain text with the paths there as links.
@MainActor
struct PathTextTests {
    @Test func linksThePickedPathsAndKeepsTheRest() {
        let t = "看一下 server/handlers.go:120，还有 /tmp/x/y.md"
        let refs = PathRefs.pick(PathRefs.find(in: t)) { $0 == "server/handlers.go" }
        let a = PathText.attributed(t, refs, link: .blue)
        #expect(String(a.characters) == t)
        let links = a.runs.compactMap { run in run.link.map { (String(a[run.range].characters), $0) } }
        #expect(links.count == 1)
        #expect(links.first?.0 == "server/handlers.go:120")
        #expect(links.first.flatMap { PathLinkURL.parse($0.1) }.map { "\($0.path):\($0.line ?? 0)" } == "server/handlers.go:120")
    }
}

/// The chips' list keeps a span that is one path whole: not its folder or the words before a space as well.
struct FilePathsWholeSpanTests {
    @Test func asksWithoutTheLines() {
        #expect(FilePaths.find(in: "见 server/handlers.go:120 和 `README.md`") == ["server/handlers.go"])
    }

    @Test func aWholeSpanIsOneChip() {
        #expect(FilePaths.find(in: "`/tmp/a.md 和 /tmp/b.md`") == ["/tmp/a.md 和 /tmp/b.md"])
        #expect(FilePaths.find(in: "先看 /var/log/app.log:3，再看 `src/a.ts:12`") == ["/var/log/app.log", "src/a.ts"])
    }
}

/// Destinations already written as Markdown links reach the same opener as bare paths (`PathLinkURL`). In particular,
/// a missing or refused file still opens the viewer's explanation instead of falling through to a silent system action.
@MainActor
struct MarkdownLocalLinkTests {
    private func accepted(_ actions: ChatActions, _ url: URL) -> Bool {
        var accepted = false
        actions.openURL.callAsFunction(url) { accepted = $0 }
        return accepted
    }

    @Test(arguments: ["/tmp/report.md", "相对/路径/文件.md"])
    func absoluteAndWorkingDirectoryRelativeDestinationsOpenTheFileViewer(path: String) throws {
        let actions = ChatActions()
        var opened: [(path: String, line: Int?)] = []
        actions.set(.init(openFileAt: { opened.append(($0, $1)) }))
        let url = try #require(URL(string: path))
        #expect(accepted(actions, url))
        #expect(opened.map { $0.path } == [path])
        #expect(opened.first?.line == nil)
    }

    @Test(arguments: ["shots/missing.png", "private/denied.png", "/tmp/unreadable.png"])
    func failedLocalDestinationsStillOpenTheirFeedback(path: String) throws {
        let actions = ChatActions()
        var opened: [String] = []
        actions.set(.init(openFileAt: { path, _ in opened.append(path) }))
        let url = try #require(URL(string: path))
        #expect(accepted(actions, url))
        #expect(opened == [path])
    }

    @Test func webDestinationsKeepOpeningInApp() throws {
        let actions = ChatActions()
        var files: [String] = [], links: [URL] = []
        actions.set(.init(openFileAt: { path, _ in files.append(path) }, openLink: { links.append($0) }))
        for string in ["http://example.com/report", "https://example.com/report"] {
            let url = try #require(URL(string: string))
            #expect(accepted(actions, url))
        }
        #expect(files.isEmpty)
        #expect(links.map(\.absoluteString) == ["http://example.com/report", "https://example.com/report"])
    }

    @Test(arguments: ["file:///tmp/shot.png", "mailto:person@example.com", "persona-attachment:shot.png"])
    func unrelatedSchemesDoNotBecomeMachineFiles(string: String) throws {
        let actions = ChatActions()
        var files: [String] = [], links: [URL] = []
        actions.set(.init(openFileAt: { path, _ in files.append(path) }, openLink: { links.append($0) }))
        let url = try #require(URL(string: string))
        #expect(!accepted(actions, url))
        #expect(files.isEmpty && links.isEmpty)
    }
}
