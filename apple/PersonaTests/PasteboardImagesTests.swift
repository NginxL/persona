import ImageIO
import UIKit
import UniformTypeIdentifiers
import XCTest
@testable import Persona

#if DEBUG
/// The composer's picture offer (#4573): copies are offered once, returning to the app looks again, and Paste and Close
/// handle only the copy the user acted on. Readers are gated to pin ordering without timing sleeps.
@MainActor final class PasteboardImagesTests: XCTestCase {
    func testPictureCopiedElsewhereIsOfferedWithoutReadingOnMain() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)

        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()

        XCTAssertTrue(model.offered)
        XCTAssertEqual(board.mainThreadReadCount, 0)
    }

    func testOwnPictureWriteIsOfferedAndOwnTextWriteClearsIt() async throws {
        let board = UIPasteboard.general
        board.items = []
        let model = PasteboardImages(center: .default, read: PasteboardImages.readSnapshot)
        await model.idle()
        let bytes = try XCTUnwrap(SyntheticClipboardImage.image().pngData())

        PhonePasteboard.live.write(.image(ClipboardImage(mime: "image/png", bytes: bytes)))
        await model.idle()
        XCTAssertTrue(model.offered)

        PhonePasteboard.live.write(.text("synthetic clipboard text"))
        await model.idle()
        XCTAssertFalse(model.offered)

        board.items = []
        await model.idle()
    }

    func testTakenOrDismissedCopyIsNotOfferedAgain() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered)

        model.take()
        await model.idle()
        XCTAssertFalse(model.offered)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        center.post(name: UIApplication.didBecomeActiveNotification, object: nil)
        await model.idle()
        XCTAssertFalse(model.offered)

        board.set(hasImages: true, changeCount: 11)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered)
        model.dismiss()
        await model.idle()
        XCTAssertFalse(model.offered)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        center.post(name: UIApplication.didBecomeActiveNotification, object: nil)
        await model.idle()
        XCTAssertFalse(model.offered)
    }

    func testBecomingActiveReadsAgainWithoutAChangedNotification() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        model.dismiss()
        await model.idle()
        let reads = board.readCount

        board.set(hasImages: true, changeCount: 11)
        XCTAssertFalse(model.offered)
        XCTAssertEqual(board.readCount, reads)
        center.post(name: UIApplication.didBecomeActiveNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered)
        XCTAssertGreaterThan(board.readCount, reads)

        model.dismiss()
        await model.idle()
        center.post(name: UIApplication.didBecomeActiveNotification, object: nil)
        await model.idle()
        XCTAssertFalse(model.offered)
    }

    func testPreDismissReadCannotOfferTheDismissedCopyAgain() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        let gate = board.gateNextRead()
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await fulfillment(of: [gate.started], timeout: 2)

        model.dismiss()
        XCTAssertFalse(model.offered)
        let trailingGate = board.gateNextRead()
        gate.proceed.signal()
        await fulfillment(of: [trailingGate.started], timeout: 2)
        XCTAssertFalse(model.offered, "the pre-dismiss snapshot was published while the trailing read was pending")
        trailingGate.proceed.signal()
        await model.idle()

        XCTAssertFalse(model.offered)
        XCTAssertFalse(board.gateTimedOut)
    }

    func testCopyChangedDuringDismissalIsOffered() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        let gate = board.gateNextRead()
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await fulfillment(of: [gate.started], timeout: 2)
        model.dismiss()

        board.set(hasImages: true, changeCount: 11)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        gate.proceed.signal()
        await model.idle()

        XCTAssertTrue(model.offered)
        XCTAssertFalse(board.gateTimedOut)
    }

    func testCloseFromAnOlderPresentationDoesNotConsumeTheNewOffer() async throws {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        let presentedCount = try XCTUnwrap(model.offerCount)

        board.set(hasImages: true, changeCount: 11)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertEqual(model.offerCount, 11)

        // The copy-10 chip was rendered, then its already-queued Close action ran after copy 11 was published.
        model.dismiss(presentedCount)
        await model.idle()
        XCTAssertTrue(model.offered)
        XCTAssertEqual(model.offerCount, 11)
        center.post(name: UIApplication.didBecomeActiveNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered, "Close consumed a newer offer which was never presented")

        model.dismiss(model.offerCount)
        await model.idle()
        XCTAssertFalse(model.offered)
        center.post(name: UIApplication.didBecomeActiveNotification, object: nil)
        await model.idle()
        XCTAssertFalse(model.offered)
    }

    func testPasteAfterCopyChangedWhileVisibleDoesNotReofferNewCopy() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered)

        board.set(hasImages: true, changeCount: 11)
        let gate = board.gateNextRead()
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await fulfillment(of: [gate.started], timeout: 2)
        model.take()
        gate.proceed.signal()
        await model.idle()

        XCTAssertFalse(model.offered, "the copy handed to PasteButton must be handled, not the older visible copy")
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertFalse(model.offered)
        XCTAssertFalse(board.gateTimedOut)
    }

    func testCopyArrivingDuringPasteReadIsOffered() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        board.set(hasImages: true, changeCount: 11)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered)

        let gate = board.gateNextRead()
        model.take()
        await fulfillment(of: [gate.started], timeout: 2)
        board.set(hasImages: true, changeCount: 12)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        gate.proceed.signal()
        await model.idle()

        XCTAssertTrue(model.offered, "a copy newer than the one PasteButton handed over must not be consumed")
        XCTAssertFalse(board.gateTimedOut)
    }

    func testCopyArrivingBeforeTrailingPasteReadCancelsTake() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()

        board.set(hasImages: true, changeCount: 11)
        let gate = board.gateNextRead()
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await fulfillment(of: [gate.started], timeout: 2)
        model.take()
        board.set(hasImages: true, changeCount: 12)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        gate.proceed.signal()
        await model.idle()

        XCTAssertTrue(model.offered)
        XCTAssertFalse(board.gateTimedOut)
    }

    func testNewCopyInSnapshotBeforeItsNotificationIsOfferedAfterPaste() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 11)
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered)

        let gate = board.gateNextRead(capturingAfterWait: true)
        model.take()
        await fulfillment(of: [gate.started], timeout: 2)
        board.set(hasImages: true, changeCount: 12)
        gate.proceed.signal()
        await model.idle()
        XCTAssertTrue(model.offered, "a snapshot newer than the taken copy must not be consumed")

        center.post(name: UIPasteboard.changedNotification, object: nil)
        await model.idle()
        XCTAssertTrue(model.offered)
        XCTAssertFalse(board.gateTimedOut)
    }

    func testUnstableSnapshotIsNotPublished() async {
        let (model, board, center) = makeModel()
        await model.idle()
        board.set(hasImages: true, changeCount: 10)
        board.retryNextRead()
        let gate = board.gateNextRead()
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await fulfillment(of: [gate.started], timeout: 2)
        XCTAssertFalse(model.offered, "an unstable image/count pair was published")

        gate.proceed.signal()
        await model.idle()
        XCTAssertTrue(model.offered)
        XCTAssertEqual(board.mainThreadReadCount, 0)
        XCTAssertFalse(board.gateTimedOut)
    }

    func testNotificationBurstUsesOneTrailingRead() async {
        let (model, board, center) = makeModel()
        await model.idle()
        let gate = board.gateNextRead()
        center.post(name: UIPasteboard.changedNotification, object: nil)
        await fulfillment(of: [gate.started], timeout: 2)
        board.set(hasImages: true, changeCount: 10)
        for _ in 0..<5 { center.post(name: UIPasteboard.changedNotification, object: nil) }
        let reads = board.readCount

        gate.proceed.signal()
        await model.idle()

        XCTAssertEqual(board.readCount, reads + 1)
        XCTAssertTrue(model.offered)
        XCTAssertFalse(board.gateTimedOut)
    }

    private func makeModel() -> (PasteboardImages, FakePasteboard, NotificationCenter) {
        let board = FakePasteboard()
        let center = NotificationCenter()
        return (PasteboardImages(center: center, read: board.read, changeCount: board.currentChangeCount), board, center)
    }
}

private nonisolated final class FakePasteboard: @unchecked Sendable {
    struct Gate {
        let started = XCTestExpectation(description: "gated pasteboard read started")
        let proceed = DispatchSemaphore(value: 0)
    }

    private let lock = NSLock()
    private var hasImages = false
    private var changeCount = 0
    private var reads = 0
    private var mainReads = 0
    private var nextGate: Gate?
    private var nextGateCapturesAfterWait = false
    private var retryBeforeNextRead = false
    private var timedOut = false

    var readCount: Int { withLock { reads } }
    var mainThreadReadCount: Int { withLock { mainReads } }
    var gateTimedOut: Bool { withLock { timedOut } }

    func set(hasImages: Bool, changeCount: Int) {
        withLock {
            self.hasImages = hasImages
            self.changeCount = changeCount
        }
    }

    func gateNextRead(capturingAfterWait: Bool = false) -> Gate {
        let gate = Gate()
        withLock {
            nextGate = gate
            nextGateCapturesAfterWait = capturingAfterWait
        }
        return gate
    }

    func retryNextRead() {
        withLock { retryBeforeNextRead = true }
    }

    func currentChangeCount() -> Int {
        withLock { changeCount }
    }

    func read() -> PasteboardImages.Snapshot {
        let planned: (Bool, Int, Bool, Gate?, Bool) = withLock {
            reads += 1
            if Thread.isMainThread { mainReads += 1 }
            let retry = retryBeforeNextRead
            retryBeforeNextRead = false
            let gate = retry ? nil : nextGate
            let afterWait = retry ? false : nextGateCapturesAfterWait
            nextGate = retry ? nextGate : nil
            nextGateCapturesAfterWait = retry ? nextGateCapturesAfterWait : false
            return (hasImages, changeCount, retry, gate, afterWait)
        }
        if let gate = planned.3 {
            gate.started.fulfill()
            if gate.proceed.wait(timeout: .now() + 5) == .timedOut {
                withLock { timedOut = true }
            }
        }
        let current: (Bool, Int) = planned.4 ? withLock { (hasImages, changeCount) } : (planned.0, planned.1)
        return PasteboardImages.Snapshot(hasImages: current.0, changeCount: current.0 ? current.1 : nil,
                                         retry: planned.2)
    }

    private func withLock<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }
}
#endif

/// Signature-selected PNG/JPEG writes stay byte-exact; every other format keeps UIKit's decoding fallback.
@MainActor final class PhonePasteboardImageWriteTests: XCTestCase {
    func testPNGWriteReadsBackExactBytesImageAndLiveContent() throws {
        try assertRawImageRoundTrip(mime: "image/png", type: .png,
                                    bytes: try XCTUnwrap(SyntheticClipboardImage.image().pngData()))
    }

    func testJPEGWriteReadsBackExactBytesImageAndLiveContent() throws {
        let bytes = try XCTUnwrap(SyntheticClipboardImage.image().jpegData(compressionQuality: 0.73))
        try assertRawImageRoundTrip(mime: "image/jpeg", type: .jpeg, bytes: bytes)
    }

    func testJPEGSignatureWinsOverDeclaredMIME() throws {
        let bytes = try XCTUnwrap(SyntheticClipboardImage.image().jpegData(compressionQuality: 0.73))
        try assertRawImageRoundTrip(mime: "image/png", type: .jpeg, bytes: bytes)
    }

    func testTIFFWithoutARawSignatureUsesTheUIKitFallback() throws {
        let bytes = try makeTIFF()
        let board = UIPasteboard.general
        defer { board.items = [] }
        board.string = "synthetic text to replace"
        let changeCount = board.changeCount

        PhonePasteboard.live.write(.image(ClipboardImage(mime: "image/tiff", bytes: bytes)))

        XCTAssertNotEqual(board.changeCount, changeCount)
        XCTAssertTrue(board.hasImages)
        let image = try XCTUnwrap(board.image)
        XCTAssertEqual(image.size, CGSize(width: 2, height: 2))
        let content = try XCTUnwrap(PhonePasteboard.live.read())
        XCTAssertEqual(content.kind, .image)
        XCTAssertNotNil(content.image?.bytes)
    }

    func testBytesThatAreNoPictureLeaveThePasteboardUntouched() throws {
        let board = UIPasteboard.general
        defer { board.items = [] }
        board.string = "synthetic text to preserve"
        let changeCount = board.changeCount

        PhonePasteboard.live.write(.image(ClipboardImage(mime: "image/png", bytes: Data([0, 1, 2, 3]))))

        XCTAssertEqual(board.string, "synthetic text to preserve")
        XCTAssertEqual(board.changeCount, changeCount)
        XCTAssertFalse(board.hasImages)
    }

    func testShortInputsUseOnlyTheirSignature() throws {
        let png = Data([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
        let cases: [(String, Data, UTType?)] = [
            ("empty", Data(), nil),
            ("one byte", Data([0x89]), nil),
            ("JPEG prefix", Data([0xff, 0xd8]), nil),
            ("JPEG signature only", Data([0xff, 0xd8, 0xff]), .jpeg),
            ("seven PNG signature bytes", Data(png.prefix(7)), nil),
            ("PNG signature only", png, .png),
            ("short JPEG segment", Data([0xff, 0xd8, 0xff, 0xc0, 0x00, 0x02, 0xff, 0xd9]), .jpeg),
        ]
        let board = UIPasteboard.general
        defer { board.items = [] }
        for (name, bytes, expectedType) in cases {
            board.string = "synthetic text to preserve"
            let changeCount = board.changeCount

            PhonePasteboard.live.write(.image(ClipboardImage(mime: "image/png", bytes: bytes)))

            if let expectedType {
                XCTAssertEqual(board.data(forPasteboardType: expectedType.identifier), bytes, name)
                XCTAssertTrue(board.hasImages, name)
                XCTAssertNotEqual(board.changeCount, changeCount, name)
            } else {
                XCTAssertEqual(board.string, "synthetic text to preserve", name)
                XCTAssertEqual(board.changeCount, changeCount, name)
                XCTAssertFalse(board.hasImages, name)
            }
        }
    }

    private func assertRawImageRoundTrip(mime: String, type: UTType, bytes: Data) throws {
        let board = UIPasteboard.general
        defer { board.items = [] }
        board.string = "synthetic text to replace"

        PhonePasteboard.live.write(.image(ClipboardImage(mime: mime, bytes: bytes)))

        XCTAssertTrue(board.hasImages)
        XCTAssertEqual(try XCTUnwrap(board.data(forPasteboardType: type.identifier)), bytes)
        let image = try XCTUnwrap(board.image)
        XCTAssertEqual(image.size, CGSize(width: 2, height: 2))
        XCTAssertEqual(image.cgImage?.width, 2)
        XCTAssertEqual(image.cgImage?.height, 2)
        let content = try XCTUnwrap(PhonePasteboard.live.read())
        XCTAssertEqual(content.kind, .image)
        XCTAssertNotNil(content.image?.bytes)
    }

    private func makeTIFF() throws -> Data {
        let image = try XCTUnwrap(SyntheticClipboardImage.image().cgImage)
        let bytes = NSMutableData()
        let destination = try XCTUnwrap(CGImageDestinationCreateWithData(
            bytes, UTType.tiff.identifier as CFString, 1, nil))
        CGImageDestinationAddImage(destination, image, nil)
        XCTAssertTrue(CGImageDestinationFinalize(destination))
        return bytes as Data
    }
}

private enum SyntheticClipboardImage {
    static func image() -> UIImage {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: CGSize(width: 2, height: 2), format: format).image { context in
            UIColor.systemTeal.setFill()
            context.fill(CGRect(x: 0, y: 0, width: 2, height: 2))
        }
    }
}
