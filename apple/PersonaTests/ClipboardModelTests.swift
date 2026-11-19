import Foundation
import ImageIO
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// Clipboard sharing with a computer (#3064): wire decoding, Send / Get / the switch against a FakeHub, the hub's
/// refusals in words, the once-only paste hint, and the fetched picture kept at its preview's size (#7925).
@MainActor struct ClipboardModelTests {
    /// A pasteboard in memory; `refuses`: the user tapped Don't Allow (the read comes back empty).
    @MainActor final class Board {
        var content: ClipboardContent?
        var refuses = false
        var reads = 0
        var pasteboard: PhonePasteboard {
            PhonePasteboard(read: { [self] in
                reads += 1
                return refuses ? nil : content
            }, write: { [self] in
                // Match the live pasteboard: unknown, undecodable bytes do not replace what the phone already holds.
                if case .image = $0.kind, let bytes = $0.image?.bytes {
                    let png = bytes.starts(with: [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
                    let jpeg = bytes.starts(with: [0xff, 0xd8, 0xff])
                    if !png && !jpeg && UIImage(data: bytes) == nil { return }
                }
                content = $0
            }, hasContent: { [self] in content != nil })
        }
    }

    @MainActor final class Hints {
        var dismissed = false
        func store(applies: Bool = true) -> ClipboardHints {
            ClipboardHints(applies: applies, dismissed: { [self] in dismissed }, dismiss: { [self] in dismissed = true })
        }
    }

    private func model(_ hub: FakeHub, _ board: Board, hints: ClipboardHints = Hints().store(),
                       updated: @escaping (MachineClipboard) -> Void = { _ in }) -> ClipboardModel {
        ClipboardModel(deps: .init(api: { hub.client }, pasteboard: board.pasteboard, updated: updated, hints: hints))
    }

    /// A picture of one color, `width` × `height` pixels, as the computer's clipboard sends it (PNG, or JPEG carrying
    /// an EXIF orientation).
    private func picture(_ width: Int, _ height: Int, orientation: Int? = nil) throws -> ClipboardContent {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        let image = UIGraphicsImageRenderer(size: CGSize(width: width, height: height), format: format).image { context in
            UIColor.systemTeal.setFill()
            context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        }
        guard let orientation else { return .image(ClipboardImage(mime: "image/png", bytes: try #require(image.pngData()))) }
        let out = NSMutableData()
        let dest = try #require(CGImageDestinationCreateWithData(out, "public.jpeg" as CFString, 1, nil))
        CGImageDestinationAddImage(dest, try #require(image.cgImage), [kCGImagePropertyOrientation: orientation] as CFDictionary)
        #expect(CGImageDestinationFinalize(dest))
        return .image(ClipboardImage(mime: "image/jpeg", bytes: out as Data))
    }

    @Test func decodesMachinesAndContent() throws {
        let old = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"mac","name":"Mac","kind":"local","online":true}"#.utf8))
        #expect(old.clipboard == nil)
        let m = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"mac","name":"Mac","kind":"local","online":true,"clipboard":{"supported":false,"enabled":false,"reason":"没有图形会话"}}"#.utf8))
        #expect(m.clipboard == MachineClipboard(supported: false, enabled: false, reason: "没有图形会话"))
        #expect(m.clipboard?.usable == false)
        #expect(try JSON.decoder.decode(Machine.self, from: JSON.encoder.encode(m)) == m)

        let image = try JSON.decoder.decode(ClipboardContent.self, from: Data(#"{"kind":"image","image":{"mime":"image/png","data":"AAEC"}}"#.utf8))
        #expect(image.kind == .image && image.image?.bytes == Data([0, 1, 2]))
        let future = try JSON.decoder.decode(ClipboardContent.self, from: Data(#"{"kind":"rtf","text":"hi"}"#.utf8))
        #expect(future.kind == .text && future.text == "hi")
        #expect(try JSON.decoder.decode(ClipboardContent.self, from: Data(#"{"kind":"concealed"}"#.utf8)).kind == .concealed)
    }

    @Test func sendsTextAndShowsTheHintOnce() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/machines/mac/clipboard") { req in
            let body = try JSON.decoder.decode([String: String].self, from: #require(req.body))
            #expect(body == ["text": "https://example.com"])
            return Data()
        }
        let board = Board()
        board.content = .text("https://example.com")
        let hints = Hints()
        let m = model(hub, board, hints: hints.store())
        #expect(await m.sendPasteboard(machineID: "mac") == .sent(.text))
        #expect(m.showsPasteHint && m.busy == nil)
        m.dismissPasteHint()
        #expect(!m.showsPasteHint && hints.dismissed)
        #expect(await m.sendPasteboard(machineID: "mac") == .sent(.text))
        #expect(!m.showsPasteHint, "dismissed for good")
        #expect(hub.sent("PUT", "/api/v1/machines/mac/clipboard").count == 2)
    }

    @Test func refusedPasteSendsNothingAndGivesNoHint() async {
        let hub = FakeHub()
        let board = Board()
        board.content = .text("secret")
        board.refuses = true
        let m = model(hub, board)
        #expect(await m.sendPasteboard(machineID: "mac") == .nothingToSend)
        #expect(!m.showsPasteHint && hub.requests.isEmpty)
        #expect(m.outcome?.isFailure == true)
    }

    @Test func pasteButtonSendsWithoutReadingAndWithoutHint() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/machines/mac/clipboard") { req in
            let body = try JSON.decoder.decode(JSONValue.self, from: #require(req.body))
            #expect(body["image"]?["mime"]?.string == "image/png" && body["text"] == nil)
            return Data()
        }
        let board = Board()
        let m = model(hub, board)
        #expect(await m.send(.image(ClipboardImage(mime: "image/png", bytes: Data([1, 2]))), machineID: "mac") == .sent(.image))
        #expect(board.reads == 0 && !m.showsPasteHint)
        #expect(await m.send(nil, machineID: "mac") == .nothingToSend)
    }

    @Test func fetchWritesThePhoneAndKeepsThePreview() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/clipboard", json: #"{"kind":"text","text":"from the Mac"}"#)
        let board = Board()
        let m = model(hub, board)
        #expect(await m.fetch(machineID: "mac", scale: 3) == .fetched(.text))
        #expect(board.content == .text("from the Mac") && m.fetched == .text("from the Mac"))
        #expect(board.reads == 0, "getting never reads the phone's clipboard")

        hub.on("GET", "/api/v1/machines/mac/clipboard", json: #"{"kind":"concealed"}"#)
        #expect(await m.fetch(machineID: "mac", scale: 3) == .remoteConcealed)
        hub.on("GET", "/api/v1/machines/mac/clipboard", json: #"{"kind":"empty"}"#)
        #expect(await m.fetch(machineID: "mac", scale: 3) == .remoteEmpty)
        #expect(board.content == .text("from the Mac"), "nothing overwritten")
    }

    /// The phone gets the computer's picture as it is; the preview keeps it at the size it draws, once (#7925).
    @Test func aFetchedPictureIsKeptAtThePreviewsSize() async throws {
        let hub = FakeHub()
        let board = Board()
        let m = model(hub, board)
        let large = try picture(1600, 800)
        hub.on("GET", "/api/v1/machines/mac/clipboard") { _ in try JSON.encoder.encode(large) }
        #expect(await m.fetch(machineID: "mac", scale: 1) == .fetched(.image))
        #expect(board.content == large && m.fetched == large, "the original goes to the phone and stays with the preview")
        let kept = try #require(m.fetchedPicture?.cgImage)
        #expect(kept.width == 400 && kept.height == 200, "200 pt high at 1 pixel a point: 400 × 200")

        let small = try picture(40, 20)
        hub.on("GET", "/api/v1/machines/mac/clipboard") { _ in try JSON.encoder.encode(small) }
        #expect(await m.fetch(machineID: "mac", scale: 3) == .fetched(.image))
        #expect(m.fetched == small && m.fetchedPicture?.cgImage?.width == 40, "a smaller picture keeps its size")

        hub.on("GET", "/api/v1/machines/mac/clipboard", json: #"{"kind":"text","text":"from the Mac"}"#)
        #expect(await m.fetch(machineID: "mac", scale: 3) == .fetched(.text))
        #expect(m.fetchedPicture == nil, "text has no picture")
    }

    @Test func thePreviewsSizeFollowsTheHeightLimit() async throws {
        #expect(ClipboardModel.previewPixels(CGSize(width: 4000, height: 2000), height: 600) == 1200, "a wide picture: the height × its aspect ratio")
        #expect(ClipboardModel.previewPixels(CGSize(width: 1000, height: 3000), height: 600) == 600, "a tall one: the height")
        #expect(ClipboardModel.previewPixels(CGSize(width: 40000, height: 1000), height: 600) == ClipboardModel.previewPixelLimit, "a panorama")

        // Turned a quarter by its EXIF orientation, a 400 × 200 JPEG is drawn 200 wide and 400 high.
        let turned = try #require(try picture(400, 200, orientation: 6).image?.bytes)
        let kept = try #require(await ClipboardModel.previewPicture(turned, height: 200)?.cgImage)
        #expect(kept.width == 100 && kept.height == 200)
    }

    @Test func clearingThePreviewDropsItsPicture() async throws {
        let hub = FakeHub()
        let m = model(hub, Board())
        let content = try picture(40, 20)
        hub.on("GET", "/api/v1/machines/mac/clipboard") { _ in try JSON.encoder.encode(content) }
        #expect(await m.fetch(machineID: "mac", scale: 3) == .fetched(.image))
        #expect(m.fetchedPicture != nil)
        hub.on("PUT", "/api/v1/machines/mac/clipboard/settings", json: #"{"supported":true,"enabled":false}"#)
        #expect(await m.setEnabled(false, machineID: "mac"))
        #expect(m.fetched == nil && m.fetchedPicture == nil, "switched off: no preview, and its picture let go")
    }

    @Test func bytesThatAreNoPictureAreAVisibleFailure() async throws {
        let hub = FakeHub()
        let board = Board()
        let m = model(hub, board)
        let broken = ClipboardContent.image(ClipboardImage(mime: "image/png", bytes: Data("not a picture".utf8)))
        hub.on("GET", "/api/v1/machines/mac/clipboard") { _ in try JSON.encoder.encode(broken) }
        let result = await m.fetch(machineID: "mac", scale: 3)
        #expect(result == .failed(.badImage) && m.outcome == result)
        #expect(m.outcome?.message(machine: "MacBook").isEmpty == false, "the failure says what went wrong")
        #expect(board.content == nil, "the live pasteboard cannot store unreadable image bytes")
        #expect(m.fetched == nil && m.fetchedPicture == nil, "no successful but empty image preview")
    }

    #if DEBUG
    @Test func debugImagePreviewDecodesItsPicture() async throws {
        let m = model(FakeHub(), Board())
        let content = try picture(40, 20)
        await m.preview(fetched: content, hint: false)
        #expect(m.fetched == content && m.outcome == .fetched(.image))
        let kept = try #require(m.fetchedPicture?.cgImage, "the screenshot's image preview is not blank")
        #expect(kept.width == 40 && kept.height == 20)
    }
    #endif

    @Test func hubRefusalsInWords() async {
        let hub = FakeHub()
        let board = Board()
        board.content = .text("x")
        let m = model(hub, board)
        hub.on("GET", "/api/v1/machines/mac/clipboard") { _ in
            throw APIError(status: 403, message: "clipboard is off", fields: ["code": "clipboard_disabled"])
        }
        #expect(await m.fetch(machineID: "mac", scale: 3) == .failed(.disabled))
        hub.on("GET", "/api/v1/machines/mac/clipboard") { _ in
            throw APIError(status: 409, message: "no graphical session", fields: ["code": "clipboard_unsupported"])
        }
        #expect(await m.fetch(machineID: "mac", scale: 3) == .failed(.unsupported("no graphical session")))
        hub.on("PUT", "/api/v1/machines/mac/clipboard") { _ in throw APIError(status: 503, message: "offline") }
        #expect(await m.sendPasteboard(machineID: "mac") == .failed(.offline))
        #expect(ClipboardOutcome.failed(.offline).message(machine: "MacBook").contains("MacBook"))
        #expect(ClipboardOutcome.failed(.unsupported("no graphical session")).message(machine: "Box").contains("no graphical session"))
    }

    @Test func switchSavesAndTellsTheList() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/machines/mac/clipboard/settings") { req in
            let body = try JSON.decoder.decode([String: Bool].self, from: #require(req.body))
            return Data(#"{"supported":true,"enabled":\#(body["enabled"] == true)}"#.utf8)
        }
        var told: [MachineClipboard] = []
        let m = model(hub, Board(), updated: { told.append($0) })
        let listed = MachineClipboard(supported: true, enabled: false)
        #expect(await m.setEnabled(true, machineID: "mac"))
        #expect(m.state(listed)?.enabled == true && told == [MachineClipboard(supported: true, enabled: true)])
        m.synchronize(MachineClipboard(supported: true, enabled: true))
        #expect(m.state(listed) == listed, "the list caught up: it is the truth again")

        hub.on("PUT", "/api/v1/machines/mac/clipboard/settings") { _ in throw APIError(status: 503, message: "offline") }
        #expect(!(await m.setEnabled(false, machineID: "mac")))
        #expect(m.outcome == .failed(.offline) && told.count == 1)
    }

    @Test func noHintWhereThereIsNoPastePrompt() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/machines/mac/clipboard") { _ in Data() }
        let board = Board()
        board.content = .text("x")
        let m = model(hub, board, hints: Hints().store(applies: false))
        #expect(await m.sendPasteboard(machineID: "mac") == .sent(.text))
        #expect(!m.showsPasteHint)
    }

    /// docs/operations/clipboard.md: `reason_code`, the extra refusals, and sizes checked before anything is sent.
    @Test func reasonCodesAndExtraRefusals() throws {
        let m = try JSON.decoder.decode(MachineClipboard.self, from: Data(#"{"supported":false,"enabled":true,"reason":"没有桌面会话","reason_code":"no_display"}"#.utf8))
        #expect(m.reasonCode == "no_display" && m.why == "没有桌面会话" && !m.usable)
        #expect(MachineClipboard(reasonCode: "node_outdated").why?.isEmpty == false, "a code alone still says why")
        #expect(MachineClipboard(reasonCode: "future").why == nil)

        func failure(_ status: Int, _ code: String?, _ message: String = "x") -> ClipboardFailure {
            ClipboardFailure(APIError(status: status, message: message, fields: code.map { ["code": $0] } ?? [:]))
        }
        #expect(failure(403, "read_only") == .readOnly)
        #expect(failure(403, "clipboard_disabled") == .disabled)
        #expect(failure(413, "too_large") == .tooLarge && failure(413, nil) == .tooLarge)
        #expect(failure(400, "clipboard_bad_image") == .badImage)
        #expect(failure(502, "clipboard_failed", "pasteboard busy") == .failed("pasteboard busy"))
        #expect(failure(503, "offline") == .offline)
        #expect(failure(400, "bad_request", "need text or image") == .other("need text or image"))
        #expect(ClipboardFailure.failed("pasteboard busy").sentence(machine: "MacBook").contains("pasteboard busy"))
    }

    @Test func tooLargeIsSaidBeforeSending() async {
        let hub = FakeHub()
        let board = Board()
        board.content = .text(String(repeating: "a", count: ClipboardLimits.textBytes + 1))
        let m = model(hub, board)
        #expect(await m.sendPasteboard(machineID: "mac") == .failed(.tooLarge))
        let big = ClipboardImage(mime: "image/jpeg", bytes: Data(count: ClipboardLimits.imageBytes + 3))
        #expect(await m.send(.image(big), machineID: "mac") == .failed(.tooLarge))
        #expect(hub.requests.isEmpty, "nothing uploaded")
        #expect(!ClipboardLimits.tooLarge(.text(String(repeating: "中", count: 1000))))
        #expect(!ClipboardLimits.tooLarge(.image(ClipboardImage(mime: "image/png", bytes: Data(count: ClipboardLimits.imageBytes)))))
    }

    @Test func refreshReadsTheSwitchFromTheHub() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/clipboard/settings", json: #"{"supported":true,"enabled":true}"#)
        let m = model(hub, Board())
        let listed = MachineClipboard(supported: true, enabled: false)
        await m.refresh(machineID: "mac")
        #expect(m.state(listed)?.enabled == true)
        m.synchronize(MachineClipboard(supported: true, enabled: false))
        #expect(m.state(listed) == listed, "a pushed machine list wins again")
    }
}
