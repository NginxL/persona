import Foundation
import PersonaKit
import Testing
import UIKit
@testable import Persona

/// The file viewer's models (docs/apple/ios.md「File viewer」): lines and pages of a text file, colors worked out in chunks,
/// the JSON outline, Markdown cut into pieces, and which mentioned paths are linked.
@MainActor
struct FileViewerTests {
    // MARK: CodeDocument

    @Test func linesAndPages() {
        let doc = CodeDocument(text: "a\r\nbb\n\nccc\n", language: nil)
        #expect(doc.lineCount == 4) // the final newline is not an empty fifth line
        #expect((0..<4).map(doc.text) == ["a", "bb", "", "ccc"]) // CR dropped
        #expect(doc.widestLine == 3)

        let go = CodeDocument(text: "package a\n/* open", language: .go)
        go.highlight { _ in }
        #expect(go.tokens(1)?.first?.kind == .comment)
        // The next page continues the open comment, and the colors of the first page are kept.
        let more = go.appending("still */ func f() {}")
        #expect(more.lineCount == 3)
        #expect(more.highlightedLines == 2)
        more.highlight { _ in }
        #expect(more.tokens(2)?.map(\.kind) == [.comment, .keyword, .function])
    }

    @Test func coloringComesInChunksAndStopsAtTheCap() {
        let text = (0..<5000).map { "let x\($0) = \($0)" }.joined(separator: "\n")
        let doc = CodeDocument(text: text, language: .swift)
        var chunks: [Range<Int>] = []
        doc.highlight(chunk: 2000) { chunks.append($0) }
        #expect(chunks == [0..<2000, 2000..<4000, 4000..<5000])
        #expect(doc.tokens(4999)?.map(\.kind) == [.keyword, .number])
        #expect(!doc.capped)

        let long = String(repeating: "x", count: CodeDocument.maxLineBytes + 100)
        let cut = CodeDocument(text: long, language: nil)
        #expect(cut.isCut(0))
        #expect(cut.text(0).count == CodeDocument.maxLineBytes)
    }

    // MARK: JSON

    static let json = #"{"name": "persona", "tags": ["a", "b"], "deep": {"x": {"y": 1}}, "z": null, "empty": {}}"#

    @Test func outlineKeepsOrderAndFolds() throws {
        let o = try JSONOutline(text: Self.json)
        let top = o.childIndices(0)
        #expect(top.map { o.key(o.nodes[$0]) } == ["name", "tags", "deep", "z", "empty"])
        #expect(o.nodes[0].children == 5)
        #expect(o.value(o.nodes[top[0]]) == "\"persona\"")
        #expect(o.path(o.childIndices(top[1])[1]) == ".tags[1]")
        #expect(o.isLast(top[4]) && !o.isLast(top[3]))

        // Everything open: 1 + every node + a closing line per non-empty container.
        let all = o.lines(open: [Bool](repeating: true, count: o.nodes.count))
        #expect(all.count == o.nodes.count + 4) // closings: top, tags, deep, x
        // Only the top open: its children, folded.
        var onlyTop = [Bool](repeating: false, count: o.nodes.count)
        onlyTop[0] = true
        #expect(o.lines(open: onlyTop).count == 1 + 5 + 1)
    }

    @Test func aBigDocumentOpensFolded() throws {
        let items = (0..<300).map { #"{"id": \#($0), "attrs": {"a": [1, 2, 3], "b": {"c": "d"}}}"# }.joined(separator: ",")
        let o = try JSONOutline(text: "[" + items + "]")
        let open = o.defaultOpen(budget: 400)
        #expect(open[0]) // the top
        // 300 items fit; opening each (3 more lines apiece) would not.
        #expect(o.childIndices(0).allSatisfy { !open[$0] })
        #expect(o.lines(open: open).count == 302)
    }

    @Test func notJSON() {
        for bad in ["{\"a\": }", "[1, 2", "{\"a\" 1}", "", "{} trailing"] {
            #expect(throws: JSONOutline.ParseError.self) { try JSONOutline(text: bad) }
        }
    }

    @Test func outlineLinesRead() throws {
        let source = JSONOutlineSource(try JSONOutline(text: Self.json))
        let shown = (0..<source.lineCount).map { source.line($0).string }
        #expect(shown.first == "{")
        #expect(shown.contains(#"  "name": "persona","#))
        #expect(shown.contains(#"  "empty": {}"#))
        #expect(shown.last == "}")
        // Tapping a container's line folds it.
        let tags = try #require(shown.firstIndex(of: #"  "tags": ["#))
        #expect(source.tap(tags))
        #expect(source.line(tags).string.hasPrefix(#"  "tags": [ … ],"#))
    }

    // MARK: Markdown

    @Test func markdownIsCutBetweenBlocksOnly() {
        var lines: [String] = ["[ref]: https://example.com", ""]
        for i in 0..<40 { lines += ["## Part \(i)", "", "Text \(i).", ""] }
        lines += ["```", "code", "", "more code", "```", "", "- item", "", "    continued", ""]
        let pieces = MarkdownChunks.split(lines.joined(separator: "\n"), lines: 20)
        #expect(pieces.count > 3)
        for p in pieces {
            // Never inside a fence: every piece has its fences in pairs.
            #expect(p.components(separatedBy: "```").count % 2 == 1)
            #expect(p.hasSuffix("[ref]: https://example.com")) // the definition goes with every piece
        }
        #expect(pieces.joined().contains("- item\n\n    continued"))
    }

    @Test func markdownLongerFenceStaysOpenPastAShorterOne() {
        // A ```` block that shows a ``` one (a README about Markdown): only ```` ends it (CommonMark, #3625).
        let block = "````md\n```html\na\nb\nc\nd\n\n<p>x</p>\n```\n````"
        let lines: [String] = (0..<6).map { "Intro \($0)" } + [""] + block.components(separatedBy: "\n") + [""] + (0..<6).map { "Tail \($0)" }
        let pieces = MarkdownChunks.split(lines.joined(separator: "\n"), lines: 4)
        #expect(pieces.count > 1)
        #expect(pieces.contains { $0.contains(block) })
        // A ``` line with a language name inside a ``` block does not end it either.
        let inner = "```\n```js\na\nb\nc\n\nlet a = 1\n```"
        let more: [String] = (0..<6).map { "Intro \($0)" } + [""] + inner.components(separatedBy: "\n") + [""] + (0..<6).map { "Tail \($0)" }
        #expect(MarkdownChunks.split(more.joined(separator: "\n"), lines: 4).contains { $0.contains(inner) })
    }

    // MARK: Paths

    @Test func pathsAreLinkedOnlyWhenThereAndAskedInOneBatch() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/stat") { @Sendable req in // called off the main actor (APIClient.stat is nonisolated)
            let paths = (req.query["paths"] ?? "").split(separator: "\n").map(String.init)
            #expect(req.query["cwd"] == "/w")
            let stats = paths.map { p in #"{"path": "\#(p)", "kind": "\#(p.hasSuffix("gone.go") ? "missing" : "file")"}"# }
            return Data(#"{"stats": [\#(stats.joined(separator: ","))]}"#.utf8)
        }
        let presence = FilePresence()
        let scope = FileScope(machine: MachineRef(id: "mac"), cwd: "/w")
        presence.ask(["a.go", "gone.go"], in: scope, api: hub.client)
        presence.ask(["b.go"], in: scope, api: hub.client)
        #expect(!presence.linkable("a.go", in: scope)) // not known yet: plain
        try await Task.sleep(for: .milliseconds(400))
        #expect(hub.sent("GET", "/api/v1/machines/mac/stat").count == 1)
        #expect(presence.linkable("a.go", in: scope) && presence.linkable("b.go", in: scope))
        #expect(!presence.linkable("gone.go", in: scope))
        // Known paths are not asked again.
        presence.ask(["a.go", "b.go"], in: scope, api: hub.client)
        try await Task.sleep(for: .milliseconds(300))
        #expect(hub.sent("GET", "/api/v1/machines/mac/stat").count == 1)
        // Another directory is another question.
        #expect(!presence.linkable("a.go", in: FileScope(machine: MachineRef(id: "mac"), cwd: "/other")))
    }

    @Test func noAnswerLinksNothing() async throws {
        let hub = FakeHub() // no stat route: an old node
        let presence = FilePresence()
        let scope = FileScope(machine: MachineRef(id: "mac"), cwd: "")
        presence.ask(["~/a.go"], in: scope, api: hub.client)
        try await Task.sleep(for: .milliseconds(400))
        #expect(!presence.linkable("~/a.go", in: scope))
        #expect(presence.remembered("~/a.go", in: scope)?.kind == nil) // asked again in a minute
        #expect(FilePresence.lifetime(nil) < FilePresence.lifetime(.missing))
    }

    @Test func noAnswerIsAskedAgainAfterItsGroupReconnects() async throws {
        let hub = FakeHub() // no stat route: every lookup fails
        let presence = FilePresence()
        let scope = FileScope(machine: MachineRef(id: "mac"), cwd: "")
        let path = "/api/v1/machines/mac/stat"
        presence.ask(["~/a.go"], in: scope, api: hub.client)
        try await Task.sleep(for: .milliseconds(400))
        #expect(hub.sent("GET", path).count == 1)

        // Another group's reconnect keeps the failure: nothing is sent.
        presence.reconnected(group: "other")
        presence.ask(["~/a.go"], in: scope, api: hub.client)
        try await Task.sleep(for: .milliseconds(400))
        #expect(hub.sent("GET", path).count == 1)

        // This group's reconnect drops it: the path is looked up again.
        presence.reconnected(group: scope.machine.group)
        #expect(presence.remembered("~/a.go", in: scope) == nil)
        presence.ask(["~/a.go"], in: scope, api: hub.client)
        try await Task.sleep(for: .milliseconds(400))
        #expect(hub.sent("GET", path).count == 2)
    }

    // MARK: Local Markdown files

    private let localPNG = Data(base64Encoded: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")!

    @Test func markdownImageTargetsReadValidPNGThroughTheSessionMachineAndWorkingDirectory() async throws {
        let hub = FakeHub()
        let route = "/api/v1/machines/studio/file/download"
        hub.on("GET", route) { [localPNG] _ in localPNG }
        let scope = FileScope(machine: MachineRef(group: "work", id: "studio"), cwd: "/Users/me/code/reports")
        let markdown = "![absolute](/tmp/shot.png)\n![relative](shots/shot.png)"
        let images = ReplyImages.all(in: markdown)
        #expect(images.map(\.url) == ["/tmp/shot.png", "shots/shot.png"])

        var decoded: [String: UIImage] = [:]
        for image in images {
            let bytes = try await hub.client.fileBytes(machine: scope.machine.id, path: image.url, cwd: scope.cwd)
            decoded[image.url] = try #require(UIImage(data: bytes), "the machine returned a valid PNG")
        }
        #expect(decoded.count == 2)
        #expect(decoded.values.allSatisfy { $0.size == CGSize(width: 1, height: 1) })
        let requests = hub.sent("GET", route)
        #expect(requests.map { $0.query["path"] } == ["/tmp/shot.png", "shots/shot.png"])
        #expect(requests.allSatisfy { $0.query["cwd"] == scope.cwd })
    }

    @Test func markdownImageReadFailuresKeepTheirLocalizedReasonsAndDoNotRetryAutomatically() async {
        let hub = FakeHub()
        let route = "/api/v1/machines/studio/file/download"
        let expected = [
            "shots/missing.png": "The image does not exist on this computer.",
            "private/denied.png": "Permission denied. Allow Persona to read this folder on the computer.",
            "/tmp/unreadable.png": "The image exists but its contents could not be read.",
        ]
        hub.on("GET", route) { req in
            let path = req.query["path"] ?? ""
            let status = path.contains("missing") ? 404 : path.contains("denied") ? 403 : 422
            throw APIError(status: status, message: expected[path] ?? "Unknown fixture path")
        }
        let scope = FileScope(machine: MachineRef(id: "studio"), cwd: "/Users/me/code/reports")
        let markdown = expected.keys.sorted().map { "![failed](\($0))" }.joined(separator: "\n")
        var reasons: [String: String] = [:]
        for image in ReplyImages.all(in: markdown) {
            do {
                _ = try await hub.client.fileBytes(machine: scope.machine.id, path: image.url, cwd: scope.cwd)
                Issue.record("the \(image.url) fixture should fail")
            } catch {
                reasons[image.url] = error.shown
            }
        }
        #expect(reasons == expected)
        let requests = hub.sent("GET", route)
        #expect(requests.count == expected.count)
        #expect(Set(requests.map { $0.query["path"] ?? "" }) == Set(expected.keys))
        #expect(requests.allSatisfy { $0.query["cwd"] == scope.cwd })
    }

    @Test(arguments: [
        (path: "missing.md", status: 404, reason: "The file does not exist on this computer."),
        (path: "private/denied.md", status: 403, reason: "Permission denied. Allow Persona to read this folder on the computer."),
        (path: "/tmp/unreadable.md", status: 422, reason: "The file exists but its contents could not be read."),
    ])
    func localLinkFailuresReachTheFileViewer(path: String, status: Int, reason: String) async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/studio/file") { _ in throw APIError(status: status, message: reason) }
        let model = FileViewerModel(ref: FileRef(machineID: "studio", path: path, cwd: "/Users/me/code/reports"), api: hub.client)
        await model.load()
        #expect(model.file == nil)
        #expect(model.error == reason)
    }
}

@MainActor
struct AttachmentImageRegressionTests {
    @Test func inlineAttachmentBytesStillDecodeWithoutAMachineFile() async throws {
        let png = Data(base64Encoded: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")!
        let payload = ImagePayload(mime: "image/png", data: png.base64EncodedString())
        let image = try #require(await ChatImageLoader.image(payload, side: nil))
        #expect(image.size == CGSize(width: 1, height: 1))
        #expect(payload.ref == nil)
    }
}
