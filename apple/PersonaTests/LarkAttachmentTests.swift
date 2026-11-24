import Foundation
import ImageIO
import Testing
import UIKit
@testable import Persona

@MainActor struct LarkAttachmentTests {
    private func file(_ name: String = "notes.txt", text: String = "notes") throws -> URL {
        let dir = URL.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appending(path: name)
        try Data(text.utf8).write(to: url)
        return url
    }

    private nonisolated final class ThumbnailDecodeLog: @unchecked Sendable {
        private let lock = NSLock()
        private var decodes = 0
        private var mainThreadDecodes = 0
        var count: Int { lock.withLock { decodes } }
        var mainThreadCount: Int { lock.withLock { mainThreadDecodes } }

        func decode(_ data: Data, maxPixel: CGFloat) -> UIImage? {
            lock.withLock {
                decodes += 1
                if Thread.isMainThread { mainThreadDecodes += 1 }
            }
            return LarkMedia.decode(data, maxPixel: maxPixel)
        }
    }

    private func thumbnailAttachment(id: UUID = UUID(), data: Data?) -> LarkAttachment {
        LarkAttachment(id: id, kind: .image, file: URL.temporaryDirectory.appending(path: id.uuidString),
                       name: "Photo.jpg", mime: "image/jpeg", size: data?.count ?? 0, thumbnail: data, temporary: false)
    }

    private func thumbnailJPEG(width: Int, height: Int, orientation: Int? = nil) throws -> Data {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        let image = UIGraphicsImageRenderer(size: CGSize(width: width, height: height), format: format).image { context in
            UIColor.systemTeal.setFill()
            context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        }
        let out = NSMutableData()
        let destination = try #require(CGImageDestinationCreateWithData(out, "public.jpeg" as CFString, 1, nil))
        let properties = orientation.map { [kCGImagePropertyOrientation: $0] as CFDictionary }
        CGImageDestinationAddImage(destination, try #require(image.cgImage), properties)
        #expect(CGImageDestinationFinalize(destination))
        return out as Data
    }

    @Test func selectedFileIsAnImmutableSnapshotAndOnlyThatCopyIsRemoved() async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        var item: LarkAttachment? = try await .stageFile(source)
        let copy = item!.file
        #expect(copy.path.hasPrefix(URL.applicationSupportDirectory.path))
        #expect(try copy.deletingLastPathComponent().resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        try Data("changed".utf8).write(to: source)
        #expect(try String(contentsOf: copy, encoding: .utf8) == "notes")
        item = nil
        #expect(!FileManager.default.fileExists(atPath: copy.path))
        #expect(FileManager.default.fileExists(atPath: source.path))
    }

    @Test func rejectsEmptyDirectoryAndOversizedFile() async throws {
        let empty = try file(text: "")
        defer { try? FileManager.default.removeItem(at: empty.deletingLastPathComponent()) }
        await #expect(throws: LarkAttachment.Failure.empty) { try await LarkAttachment.stageFile(empty) }
        await #expect(throws: LarkAttachment.Failure.invalid) { try await LarkAttachment.stageFile(empty.deletingLastPathComponent()) }
        let handle = try FileHandle(forWritingTo: empty)
        try handle.truncate(atOffset: UInt64(LarkAttachment.fileLimit + 1)); try handle.close()
        await #expect(throws: LarkAttachment.Failure.tooLarge) { try await LarkAttachment.stageFile(empty) }
    }

    @Test func photoUsesBoundedJPEGAndStableIDOnRetry() async throws {
        let source = Data(base64Encoded: "R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==")!
        let photo = try await LarkAttachment.stagePhoto(source)
        #expect(photo.kind == .image && photo.mime == "image/jpeg" && photo.size <= LarkAttachment.imageLimit)
        #expect(photo.thumbnail != nil)
        #expect(photo.file.path.hasPrefix(URL.applicationSupportDirectory.path))
        let hub = FakeHub()
        let path = "/api/v1/lark/conversations/123/images"
        hub.on("POST", path, json: #"{"message_id":"999"}"#)
        var item = LarkOutgoing(text: "", attachment: photo)
        let first = try await hub.client.larkSendAttachment("123", attachment: photo, clientID: item.clientID)
        item.retry()
        let second = try await hub.client.larkSendAttachment("123", attachment: photo, clientID: item.clientID)
        #expect(first.messageID == second.messageID)
        let bodies = try hub.sent("POST", path).map { try JSONSerialization.jsonObject(with: $0.body!) as! [String: String] }
        #expect(bodies.count == 2 && bodies[0] == bodies[1])
        #expect(bodies[0]["client_id"] == item.clientID)
    }

    @Test func thumbnailDecodeIsOffMainAndReusedByAttachmentID() async throws {
        let bytes = try thumbnailJPEG(width: 640, height: 320)
        let attachment = thumbnailAttachment(data: bytes)
        let sameID = thumbnailAttachment(id: attachment.id, data: bytes)
        let otherID = thumbnailAttachment(data: bytes)
        let log = ThumbnailDecodeLog()
        let cache = LarkAttachmentThumbnailCache(decode: log.decode)

        let first = try #require(await cache.image(for: attachment, maxPixel: 216))
        let repeated = try #require(await cache.image(for: sameID, maxPixel: 216))
        #expect(first === repeated)
        #expect(log.count == 1, "one attachment ID has one decode across repeated renders")

        _ = try #require(await cache.image(for: otherID, maxPixel: 216))
        #expect(log.count == 2, "equal bytes belonging to another attachment ID have their own decode")
        #expect(log.mainThreadCount == 0)
    }

    @Test func thumbnailDownsamplingBoundsCompactAndFullAndAppliesOrientation() async throws {
        // EXIF right: the displayed image is 600 × 1200, not the encoded 1200 × 600 landscape.
        let attachment = thumbnailAttachment(data: try thumbnailJPEG(width: 1200, height: 600, orientation: 6))
        let cache = LarkAttachmentThumbnailCache()
        let full = try #require(await cache.image(for: attachment, maxPixel: 900))
        let compact = try #require(await cache.image(for: attachment, maxPixel: 216))

        #expect(full.cgImage?.width == 450 && full.cgImage?.height == 900)
        #expect(compact.cgImage?.width == 108 && compact.cgImage?.height == 216)
        #expect(full.imageOrientation == .up && compact.imageOrientation == .up)
        let display = LarkImagePart.displaySize(width: Double(full.size.width), height: Double(full.size.height))
        #expect(display == CGSize(width: 150, height: 300), "full rendering keeps its existing aspect-ratio sizing")
    }

    @Test func malformedOrMissingThumbnailAlwaysUsesTheFileCardFallback() async {
        let malformed = thumbnailAttachment(data: Data("not an image".utf8))
        let missing = thumbnailAttachment(data: nil)
        let cache = LarkAttachmentThumbnailCache()

        let first = await cache.image(for: malformed, maxPixel: 216)
        let repeated = await cache.image(for: malformed, maxPixel: 216)
        let absent = await cache.image(for: missing, maxPixel: 216)
        #expect(first == nil)
        #expect(repeated == nil, "a failed decode must not turn into a cached image")
        #expect(absent == nil)
    }

    @Test func fileUsesRawBytesAndItsExactAcknowledgement() async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let attachment = try await LarkAttachment.stageFile(source)
        let hub = FakeHub(), path = "/api/v1/lark/conversations/123/files"
        hub.on("POST", path, json: #"{"message_id":"999"}"#)
        var outgoing = LarkOutgoing(text: "", attachment: attachment)
        let receipt = try await hub.client.larkSendAttachment("123", attachment: attachment, clientID: outgoing.clientID)
        let sent = hub.sent("POST", path)
        #expect(sent.count == 1 && sent[0].body == Data("notes".utf8))
        #expect(sent[0].query["name"] == "notes.txt" && sent[0].query["client_id"] == outgoing.clientID)
        outgoing.messageID = receipt.messageID; outgoing.state = .sent
        let unrelated = LarkMessage(id: "998", position: "1", senderName: "我", isMine: true, type: "FILE", text: "", recalled: false, replyCount: 0)
        // Empty text, identical file names, and elapsed time must never remove an unconfirmed attachment.
        #expect(LarkOutbox.remaining([outgoing], messages: [unrelated], now: .now.addingTimeInterval(120)).count == 1)
        var echoed = unrelated; echoed.id = "999"
        #expect(LarkOutbox.remaining([outgoing], messages: [echoed]).isEmpty)
    }

    @Test func queueDoesNotWaitForEchoBeforeSendingNextAndSurvivesReopening() async throws {
        let hub = FakeHub(), path = "/api/v1/lark/conversations/123/messages"
        hub.on("GET", "/api/v1/lark/raw/accounts", json: #"{"activeId":"1","accounts":[{"id":"1","userId":"fixture-account","signedIn":true}]}"#)
        hub.on("POST", path, json: #"{"message_id":"999"}"#)
        let root = URL.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let queue = LarkChatOutbox.shared(identity: .init(group: "", account: "fixture-account"), feed: "123", base: root)
        try await queue.prepare()
        try await queue.enqueue([LarkOutgoing(text: "first"), LarkOutgoing(text: "second")])
        for item in queue.items { queue.deliver(item.id, using: hub.client, feed: "123") {} }
        for _ in 0..<100 where hub.sent("POST", path).count < 2 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(hub.sent("POST", path).count == 2)
        #expect(queue === LarkChatOutbox.shared(identity: .init(group: "", account: "fixture-account"), feed: "123", base: root))
        let another = LarkChatOutbox.shared(identity: .init(group: "", account: "other-account"), feed: "123", base: root)
        try await another.prepare()
        #expect(another.items.isEmpty)
        for item in queue.items { try await queue.discard(item.id) }
    }

    @Test func queuePreservesQuoteRoutingAndAcknowledgement() async throws {
        let hub = FakeHub(), path = "/api/v1/lark/messages/source/replies"
        hub.on("POST", path, json: #"{"message_id":"reply-copy"}"#)
        let quote = LarkReplyTo(id: "source", senderName: "Sender", text: "Question")
        let item = LarkOutgoing(text: "Answer", replyTo: quote)
        let queue = LarkChatOutbox()
        try await queue.enqueue([item])
        queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<100 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(queue.items.first?.state == .sent)
        #expect(queue.items.first?.messageID == "reply-copy")
        #expect(hub.sent("POST", "/api/v1/lark/conversations/123/messages").isEmpty)
        let sent = try #require(hub.sent("POST", path).first)
        let body = try JSONSerialization.jsonObject(with: sent.body!) as! [String: Any]
        #expect(body["thread"] as? Bool == false)
        #expect(body["client_id"] as? String == item.clientID)
        #expect(queue.items.first?.message(like: nil).replyTo == quote)
    }

    @Test func confirmedPushAfterTimeoutReplacesFailedRowButPendingEchoDoesNot() {
        var outgoing = LarkOutgoing(text: "hello")
        outgoing.state = .failed("timeout")
        var echo = LarkMessage(id: "local:" + outgoing.clientID, position: "1", senderName: "我", isMine: true,
                               type: "TEXT", text: "hello", recalled: false, replyCount: 0,
                               clientID: outgoing.clientID, localState: "pending")
        #expect(LarkOutbox.remaining([outgoing], messages: [echo]).count == 1)
        echo.id = "999"; echo.localState = "synced"
        #expect(LarkOutbox.remaining([outgoing], messages: [echo]).isEmpty)
    }

    @Test func timedOutFileRetainsAttachmentWithoutAllowingAnotherUpload() async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let attachment = try await LarkAttachment.stageFile(source)
        let queue = LarkChatOutbox(), hub = FakeHub(), path = "/api/v1/lark/conversations/123/files"
        hub.on("POST", path) { _ in throw URLError(.timedOut) }
        let item = LarkOutgoing(text: "", attachment: attachment)
        try await queue.enqueue([item]); queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<100 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        guard case .unconfirmed = queue.items.first?.state else { Issue.record("Expected an unconfirmed upload after timeout"); return }
        #expect(FileManager.default.fileExists(atPath: attachment.file.path))
        await #expect(throws: (any Error).self) { try await queue.retry(item.id) }
        #expect(hub.sent("POST", path).count == 1)
    }

    @Test func rejectedFileCanRetryWithItsOriginalID() async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let attachment = try await LarkAttachment.stageFile(source)
        let queue = LarkChatOutbox(), hub = FakeHub(), path = "/api/v1/lark/conversations/123/files"
        let item = LarkOutgoing(text: "", state: .failed("rejected"), attachment: attachment)
        try await queue.enqueue([item])
        hub.on("POST", path, json: #"{"message_id":"999"}"#)
        try await queue.retry(item.id); queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<100 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(queue.items.first?.state == .sent)
        #expect(hub.sent("POST", path).allSatisfy { $0.query["client_id"] == item.clientID })
    }

    @Test(arguments: [#"{"message_id":"local:pending"}"#])
    func unusableFileReceiptIsUnconfirmedAndCannotUploadAgain(_ response: String) async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let queue = LarkChatOutbox(), hub = FakeHub(), path = "/api/v1/lark/conversations/123/files"
        hub.on("POST", path, json: response)
        let item = LarkOutgoing(text: "", attachment: try await .stageFile(source))
        try await queue.enqueue([item]); queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<100 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(queue.items.first?.state == .unconfirmed)
        await #expect(throws: (any Error).self) { try await queue.retry(item.id) }
        #expect(hub.sent("POST", path).count == 1)
    }
    @Test(arguments: [#"{}"#, #"{"message_id":""}"#])
    func acceptedFileWithoutMessageIDIsSentAndCannotRetry(_ response: String) async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let queue = LarkChatOutbox(), hub = FakeHub(), path = "/api/v1/lark/conversations/123/files"
        hub.on("POST", path, json: response)
        let item = LarkOutgoing(text: "", attachment: try await .stageFile(source))
        try await queue.enqueue([item]); queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<100 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(queue.items.first?.state == .sent)
        await #expect(throws: (any Error).self) { try await queue.retry(item.id) }
        #expect(hub.sent("POST", path).count == 1)
    }

    @Test(arguments: [URLError.Code.notConnectedToInternet, .cannotFindHost, .cannotConnectToHost, .dnsLookupFailed])
    func fileThatCouldNotReachServerRemainsRetryable(_ code: URLError.Code) async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let queue = LarkChatOutbox(), hub = FakeHub(), path = "/api/v1/lark/conversations/123/files"
        hub.on("POST", path) { _ in throw URLError(code) }
        let item = LarkOutgoing(text: "", attachment: try await .stageFile(source))
        try await queue.enqueue([item]); queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<100 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        guard case .failed = queue.items.first?.state else { Issue.record("Expected an explicit offline failure"); return }
        #expect(queue.items.first?.canRetry == true)
        try await queue.retry(item.id)
        #expect(queue.items.first?.attempted == false)
    }

    @Test func abandonedStagingIsSweptOnceWithoutRemovingCurrentFilesOrDurableMedia() throws {
        let root = URL.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let staging = LarkAttachmentStaging(root: root.appending(path: "staging"))
        let old = staging.root.appending(path: UUID().uuidString)
        let durable = root.appending(path: "durable")
        try FileManager.default.createDirectory(at: old, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: durable, withIntermediateDirectories: true)
        staging.sweep()
        #expect(!FileManager.default.fileExists(atPath: old.path))
        let current = staging.root.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: current, withIntermediateDirectories: true)
        staging.sweep()
        #expect(FileManager.default.fileExists(atPath: current.path))
        #expect(FileManager.default.fileExists(atPath: durable.path))
        #expect(LarkAttachment.uploadTimeout(bytes: LarkAttachment.fileLimit) > 13 * 60)
        #expect(LarkAttachment.uploadTimeout(bytes: 1) == 180)
    }

    @Test func orphanedGatewayPendingAndFailedCopiesAreNeverRenderedAsSent() {
        let queue = LarkChatOutbox()
        var message = LarkMessage(id: "local:orphan", position: "1", senderName: "我", isMine: true, type: "IMAGE", text: "", recalled: false, replyCount: 0)
        #expect(queue.hidesEcho(message))
        message.id = "confirmed"; message.localState = "failed"
        #expect(queue.hidesEcho(message))
        message.localState = "pending"
        #expect(queue.hidesEcho(message))
        message.localState = "synced"
        #expect(!queue.hidesEcho(message))
    }


    // MARK: Quoting and threads (#1035)

    @Test func aQuotedOrThreadedPhotoSaysSoAndAPlainOneDoesNot() async throws {
        let photo = try await LarkAttachment.stagePhoto(Data(base64Encoded: "R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==")!)
        let hub = FakeHub(), path = "/api/v1/lark/conversations/123/images"
        hub.on("POST", path, json: #"{"message_id":"999"}"#)
        let quote = LarkReplyTo(id: "m7", senderName: "示例成员", text: "原消息")
        _ = try await LarkOutbox.deliver(LarkOutgoing(text: "", attachment: photo), feed: "123", api: hub.client)
        _ = try await LarkOutbox.deliver(LarkOutgoing(text: "", replyTo: quote, attachment: photo), feed: "123", api: hub.client)
        _ = try await LarkOutbox.deliver(LarkOutgoing(text: "", replyTo: quote, thread: true, threadRoot: "m1", attachment: photo),
                                         feed: "123", api: hub.client)
        let bodies = try hub.sent("POST", path).map { try JSONSerialization.jsonObject(with: $0.body!) as! [String: Any] }
        #expect(bodies.count == 3)
        #expect(bodies[0]["reply_to"] == nil && bodies[0]["thread"] == nil, "a plain photo is unchanged")
        #expect(bodies[1]["reply_to"] as? String == "m7" && bodies[1]["thread"] == nil)
        #expect(bodies[2]["reply_to"] as? String == "m7" && bodies[2]["thread"] as? Bool == true)
    }

    @Test func aQuotedOrThreadedFileSaysSoInItsQuery() async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let attachment = try await LarkAttachment.stageFile(source)
        let hub = FakeHub(), path = "/api/v1/lark/conversations/123/files"
        hub.on("POST", path, json: #"{"message_id":"999"}"#)
        let quote = LarkReplyTo(id: "m7", senderName: "示例成员", text: "原消息")
        _ = try await LarkOutbox.deliver(LarkOutgoing(text: "", replyTo: quote, attachment: attachment), feed: "123", api: hub.client)
        _ = try await LarkOutbox.deliver(LarkOutgoing(text: "", replyTo: quote, thread: true, threadRoot: "m1", attachment: attachment),
                                         feed: "123", api: hub.client)
        _ = try await LarkOutbox.deliver(LarkOutgoing(text: "", replyTo: quote, thread: true, attachment: attachment), feed: "123", api: hub.client)
        let sent = hub.sent("POST", path)
        #expect(sent.count == 3)
        #expect(sent[0].query["reply_to"] == "m7" && sent[0].query["thread_root"] == nil)
        #expect(sent[1].query["reply_to"] == "m7" && sent[1].query["thread_root"] == "m1")
        #expect(sent[2].query["thread_root"] == "m7", "a thread started on the quoted message")
    }

    @Test func onlyAHubThatSaysSoCarriesAnAttachmentsQuote() {
        #expect(LarkFeatures(hub: ["lark", "lark_attachment_reply"]).attachmentReplies)
        #expect(!LarkFeatures(hub: ["lark"]).attachmentReplies)
        #expect(!LarkFeatures(hub: []).attachmentReplies, "unknown is not this")
        #expect(!LarkFeatures(hub: ["lark_attachments"]).attachmentReplies, "a hub from before the plugin")
    }

    @Test func theQuoteAndThreadOfAQueuedAttachmentSurviveARestart() async throws {
        let source = try file()
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        let root = URL.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let identity = LarkOutboxIdentity(group: "", account: "fixture-account")
        let quote = LarkReplyTo(id: "m7", senderName: "示例成员", text: "原消息")
        let item = LarkOutgoing(text: "", replyTo: quote, thread: true, threadRoot: "m1", attachment: try await LarkAttachment.stageFile(source))
        _ = try await LarkOutboxStore(identity: identity, feed: "123", base: root).save(.init(items: [item]))
        let back = try await LarkOutboxStore(identity: identity, feed: "123", base: root).load()
        let restored = try #require(back?.items.first)
        #expect(restored.replyTo?.id == "m7" && restored.thread && restored.threadRoot == "m1" && restored.attachment != nil)
    }
}
