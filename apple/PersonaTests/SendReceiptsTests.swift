import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The hub's send receipts (docs/protocol/protocol.md "Message feedback (#5258)", its "To-do writes"; #10465): what the
/// two events decode to, which hello vouches for them, the table that waits on them, and the socket that drops them
/// unless its own hello vouches for their kind.
@MainActor @Suite(.serialized)
struct SendReceiptsTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private static let op = "0A1B2C3D-0000-4000-8000-0000000000AB"
    private static let todoKey = SendReceipt.Key(group: "", kind: .todo, id: op)

    static func todoFrame(_ state: String, op: String = op, todo: String = "t01") -> String {
        #"{"type":"todo_send_state","todo_id":"\#(todo)","client_op_id":"\#(op.lowercased())","state":"\#(state)","at":"2026-11-22T01:02:03.123456789Z"}"#
    }

    static func messageFrame(_ state: String, id: String = "msg-1", session: String = "s1") -> String {
        #"{"type":"message_send_state","machine_id":"m1","session_id":"\#(session)","client_msg_id":"\#(id)","state":"\#(state)","at":"2026-11-22T01:02:03Z"}"#
    }

    private static let trusting = #"{"type":"hello","features":["sessions_list"],"message_send_state":1,"todo_send_state":1}"#

    private func decoded(_ json: String) -> SendReceipt? {
        guard case .sendReceipt(let receipt)? = ServerEvent.decode(Data(json.utf8)) else { return nil }
        return receipt
    }

    private func hello(_ json: String) -> HubHello? {
        guard case .hello(let hello)? = ServerEvent.decode(Data(json.utf8)) else { return nil }
        return hello
    }

    // MARK: Decoding

    @Test func decodesATodoWritesReceipt() throws {
        let receipt = try #require(decoded(Self.todoFrame("processing")))
        #expect(receipt.key == SendReceipt.Key(group: "", kind: .todo, id: Self.op))
        #expect(receipt.key.id == Self.op.lowercased(), "op ids are compared without case")
        #expect(receipt.target == .todo("t01"))
        #expect(receipt.state == .processing && !receipt.state.isFinal)
        for state in ["delivered", "failed", "unknown"] {
            #expect(decoded(Self.todoFrame(state))?.state.rawValue == state)
        }
    }

    @Test func decodesASessionMessagesReceipt() throws {
        let receipt = try #require(decoded(Self.messageFrame("kept")))
        #expect(receipt.key == SendReceipt.Key(group: "", kind: .message, id: "MSG-1"))
        #expect(receipt.target == .session(machineID: "m1", sessionID: "s1"))
        #expect(receipt.state == .kept && receipt.state.isFinal)
    }

    @Test func dropsReceiptsItCannotPlace() {
        #expect(decoded(Self.todoFrame("queued")) == nil, "a state this build does not know")
        #expect(decoded(Self.todoFrame("kept")) == nil, "to-do writes are never kept")
        #expect(decoded(#"{"type":"todo_send_state","client_op_id":"x","state":"processing"}"#) == nil, "no to-do")
        #expect(decoded(#"{"type":"todo_send_state","todo_id":"t01","state":"processing"}"#) == nil, "no op id")
        #expect(decoded(#"{"type":"message_send_state","session_id":"s1","client_msg_id":"m","state":"processing"}"#) == nil, "no machine")
        #expect(decoded(#"{"type":"message_send_state","machine_id":"m1","session_id":"s1","state":"processing"}"#) == nil, "no id")
    }

    // MARK: The hello's markers

    @Test func onlyTheHellosOwnMarkersVouch() throws {
        #expect(hello(Self.trusting)?.sendReceipts == SendReceiptTrust(messages: true, todos: true))
        #expect(hello(#"{"type":"hello","features":["message_send_state","todo_send_state"]}"#)?.sendReceipts == SendReceiptTrust.untrusted,
                "a feature-array entry is not proof")
        #expect(hello(#"{"type":"hello","message_send_state":1}"#)?.sendReceipts == SendReceiptTrust(messages: true, todos: false))
        // Another shape or version is no trust, never a lost hello.
        let odd = try #require(hello(#"{"type":"hello","features":["x"],"message_send_state":"1","todo_send_state":2}"#))
        #expect(odd.features == ["x"] && odd.sendReceipts == SendReceiptTrust.untrusted)
    }

    /// The hub's own hello (spec/fixtures/wire/connection.json, `Hub.hello`) vouches for both.
    @Test func theHubsHelloVouchesForBoth() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/wire/connection.json"))
        let cases = try #require(try JSONSerialization.jsonObject(with: data) as? [[String: Any]])
        let input = try #require(cases.first { $0["name"] as? String == "hello_features" }?["input"])
        let golden = try #require(hello(String(decoding: try JSONSerialization.data(withJSONObject: input), as: UTF8.self)))
        #expect(golden.sendReceipts == SendReceiptTrust(messages: true, todos: true))
    }

    @Test func onlyTheIOSAppAsksForTodoReceipts() {
        let phone = RequestClientMetadata(platform: .iOS, surface: .app, version: "1")
        #expect(LiveConnection.helloFeatures(phone).contains("todo_send_state"))
        #expect(LiveConnection.asksTodoReceipts(phone))
        for other in [RequestClientMetadata(platform: .watchOS, surface: .app, version: "1"),
                      RequestClientMetadata(platform: .visionOS, surface: .app, version: "1"),
                      RequestClientMetadata(platform: .iOS, surface: .widget, version: "1")] {
            #expect(!LiveConnection.helloFeatures(other).contains("todo_send_state"))
            #expect(!LiveConnection.asksTodoReceipts(other))
        }
    }

    /// The socket's URL says it too, so the hub knows from the upgrade on, before it has read `client_hello`.
    @Test func theSocketsURLAsksForTodoReceipts() {
        let features = { (todos: Bool) in
            LiveConnection.queryItems(EventCursor(), todoReceipts: todos).first { $0.name == "features" }?.value?.split(separator: ",") ?? []
        }
        #expect(features(true).contains("todo_send_state"))
        #expect(!features(false).contains("todo_send_state"))
    }

    /// Receipts are stamped with their link's group (`AppModel.makeLink`), as a sender's key names it.
    @Test func aReceiptIsStampedWithItsLinksGroup() throws {
        let event = try #require(ServerEvent.decode(Data(Self.todoFrame("processing").utf8)))
        guard case .sendReceipt(let receipt) = event.stamped("g2") else {
            Issue.record("not a receipt")
            return
        }
        #expect(receipt.key == SendReceipt.Key(group: "g2", kind: .todo, id: Self.op))
    }

    // MARK: The table

    @Test func ignoresWhatItDoesNotWaitOn() throws {
        var table = SendReceipts()
        #expect(table.apply(try #require(decoded(Self.todoFrame("processing")))) == nil, "an id nobody sent")
        table.expect(Self.todoKey, target: .todo("t01"))
        #expect(table.apply(try #require(decoded(Self.todoFrame("processing", todo: "t02")))) == nil, "another to-do, same id")
        var otherGroup = try #require(decoded(Self.todoFrame("processing")))
        otherGroup.key.group = "g2"
        #expect(table.apply(otherGroup) == nil, "another hub group")
        #expect(table.state(Self.todoKey) == nil)
    }

    @Test func aFinalStateEndsTheEntry() throws {
        var table = SendReceipts()
        table.expect(Self.todoKey, target: .todo("t01"))
        #expect(table.apply(try #require(decoded(Self.todoFrame("processing")))) == .processing)
        #expect(table.apply(try #require(decoded(Self.todoFrame("processing")))) == nil, "nothing new")
        #expect(table.state(Self.todoKey) == .processing)
        #expect(table.apply(try #require(decoded(Self.todoFrame("delivered")))) == .delivered)
        #expect(!table.expects(Self.todoKey) && table.count == 0)
        #expect(table.apply(try #require(decoded(Self.todoFrame("processing")))) == nil, "after its end")
        // A final state without Processing (the event was lost) ends it too.
        table.expect(Self.todoKey, target: .todo("t01"))
        #expect(table.apply(try #require(decoded(Self.todoFrame("unknown")))) == .unknown)
        #expect(table.count == 0)
    }

    @Test func expectingAgainStartsOver() throws {
        var table = SendReceipts()
        table.expect(Self.todoKey, target: .todo("t01"))
        _ = table.apply(try #require(decoded(Self.todoFrame("processing"))))
        table.expect(Self.todoKey, target: .todo("t01"))
        #expect(table.state(Self.todoKey) == nil && table.count == 1)
    }

    @Test func staysBounded() {
        var table = SendReceipts()
        let keys = (0...SendReceipts.limit).map { SendReceipt.Key(group: "", kind: .message, id: "m\($0)") }
        var gone: [SendReceipt.Key] = []
        for key in keys { if let out = table.expect(key, target: .session(machineID: "m1", sessionID: "s1")) { gone.append(out) } }
        #expect(table.count == SendReceipts.limit)
        #expect(gone == [keys[0]], "the oldest leaves")
        table.forget(keys[1])
        #expect(table.count == SendReceipts.limit - 1 && !table.expects(keys[1]))
    }

    // MARK: Through the socket

    /// What the app sent over the socket.
    final class Sent {
        var messages: [[String: Any]] = []
    }

    /// Each link's socket, in the order the links connected.
    @MainActor final class Sockets {
        var all: [SyncSocket] = []

        func make() -> SyncSocket {
            let socket = SyncSocket()
            all.append(socket)
            return socket
        }
    }

    @MainActor final class Changes {
        var count = 0
    }

    private func app() throws -> (AppModel, SyncSocket, Sent) {
        let hub = FakeHub()
        let transport = try #require(hub.client.transport)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "send-receipts-" + UUID().uuidString))
        let socket = SyncSocket()
        let sent = Sent()
        socket.onReceive = { sent.messages.append($0) }
        model.linkHooksForTests = (transport: transport, socket: { socket })
        model.connectOnly(.demo)
        return (model, socket, sent)
    }

    private func deliver(_ socket: SyncSocket, _ json: String) { socket.deliver?(Data(json.utf8)) }

    @Test func aTrustedReceiptMovesItsSendersCell() throws {
        let (model, socket, _) = try app()
        deliver(socket, Self.trusting)
        let cell = model.sendReceipts.expect(Self.todoKey, target: .todo("t01"))
        let other = model.sendReceipts.expect(SendReceipt.Key(group: "", kind: .message, id: "msg-1"),
                                               target: .session(machineID: "m1", sessionID: "s1"))
        #expect(cell.state == nil, "Sending until the hub says anything")
        deliver(socket, Self.todoFrame("processing"))
        #expect(cell.state == .processing && other.state == nil)
        deliver(socket, Self.messageFrame("processing", id: "msg-2"))
        #expect(other.state == nil, "another send's receipt")
        deliver(socket, Self.todoFrame("delivered"))
        #expect(cell.state == .delivered, "the cell keeps the final state")
        #expect(model.sendReceipts.waiting == 1, "the table let the to-do write go")
        deliver(socket, Self.messageFrame("failed"))
        #expect(other.state == .failed && model.sendReceipts.waiting == 0)
    }

    @Test func noVersionNoState() throws {
        let (model, socket, _) = try app()
        deliver(socket, #"{"type":"hello","features":["message_send_state","todo_send_state"],"message_send_state":1}"#)
        let todo = model.sendReceipts.expect(Self.todoKey, target: .todo("t01"))
        let message = model.sendReceipts.expect(SendReceipt.Key(group: "", kind: .message, id: "msg-1"),
                                                 target: .session(machineID: "m1", sessionID: "s1"))
        deliver(socket, Self.todoFrame("processing"))
        deliver(socket, Self.messageFrame("processing"))
        #expect(todo.state == nil, "this hello does not vouch for to-do receipts")
        #expect(message.state == .processing)
    }

    @Test func aReconnectNeedsItsOwnHello() throws {
        let (model, socket, _) = try app()
        deliver(socket, Self.trusting)
        let cell = model.sendReceipts.expect(Self.todoKey, target: .todo("t01"))
        socket.drop?()
        #expect(model.live.receiptTrust == SendReceiptTrust.untrusted)
        model.live.reconnectNow()
        deliver(socket, Self.todoFrame("processing")) // before the new socket's hello
        #expect(cell.state == nil)
        deliver(socket, #"{"type":"hello","features":[]}"#) // a hub switched back to a build before receipts
        deliver(socket, Self.todoFrame("processing"))
        #expect(cell.state == nil)
        socket.drop?()
        model.live.reconnectNow()
        deliver(socket, Self.trusting)
        deliver(socket, Self.todoFrame("processing"))
        #expect(cell.state == .processing)
    }

    @Test func aNewCredentialNeedsItsOwnHello() throws {
        let (model, socket, _) = try app()
        deliver(socket, Self.trusting)
        #expect(model.live.receiptTrust == SendReceiptTrust(messages: true, todos: true))
        let cell = model.sendReceipts.expect(Self.todoKey, target: .todo("t01"))
        #expect(model.live.renewCredential(from: ServerConfig.demo.token, to: "device-token"))
        #expect(model.live.receiptTrust == SendReceiptTrust.untrusted)
        deliver(socket, Self.todoFrame("processing"))
        #expect(cell.state == nil)
        deliver(socket, Self.trusting)
        deliver(socket, Self.todoFrame("processing"))
        #expect(cell.state == .processing)
    }

    /// What offers to check a send reads the trust while it draws: it sees it come with the hello and go with the drop,
    /// and a new attempt, which clears a trust the socket no longer had, tells it nothing.
    @Test func whatDrawsFromTheTrustSeesItComeAndGo() throws {
        let (model, socket, _) = try app()
        withExtendedLifetime(model) { // the socket's closures hold the model weakly
            let seen = Changes()
            let watch = {
                withObservationTracking { _ = model.live.receiptTrust } onChange: { MainActor.assumeIsolated { seen.count += 1 } }
            }
            watch()
            deliver(socket, Self.trusting)
            #expect(seen.count == 1 && model.live.receiptTrust.todos)
            watch()
            socket.drop?()
            #expect(seen.count == 2 && model.live.receiptTrust == SendReceiptTrust.untrusted)
            watch()
            model.live.reconnectNow()
            #expect(seen.count == 2, "nothing changed")
            deliver(socket, Self.trusting)
            #expect(seen.count == 3)
        }
    }

    /// Two hubs in two groups: a receipt on the second group's socket moves the send expected under that group, and not
    /// the default group's send under the same op id.
    @Test func aReceiptLandsInItsOwnHubsGroup() throws {
        let hub = FakeHub()
        let transport = try #require(hub.client.transport)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "send-receipts-groups-" + UUID().uuidString))
        let sockets = Sockets()
        model.linkHooksForTests = (transport: transport, socket: { sockets.make() })
        model.registry = HubRegistry(hubs: [PairedHub(id: "a", config: .demo, group: ""), PairedHub(id: "b", config: .demo, group: "g2")])
        model.buildLinks()
        try withExtendedLifetime(model) {
            #expect(model.links.map(\.group) == ["", "g2"])
            for link in model.links { link.live.connect(.demo) }
            #expect(sockets.all.count == 2)
            let home = try #require(sockets.all.first), other = try #require(sockets.all.last)
            deliver(home, Self.trusting)
            deliver(other, Self.trusting)
            let theirs = model.sendReceipts.expect(SendReceipt.Key(group: "g2", kind: .todo, id: Self.op), target: .todo("t01"))
            let ours = model.sendReceipts.expect(Self.todoKey, target: .todo("t01"))
            deliver(other, Self.todoFrame("processing"))
            #expect(theirs.state == .processing && ours.state == nil)
            deliver(home, Self.todoFrame("delivered"))
            #expect(ours.state == .delivered && theirs.state == .processing)
        }
    }

    @Test func theAppAsksForTodoReceiptsInItsHello() throws {
        let (model, socket, sent) = try app()
        try withExtendedLifetime(model) { // the socket's closures hold the model weakly
            deliver(socket, Self.trusting)
            let hello = try #require(sent.messages.first { $0["type"] as? String == "client_hello" })
            let features = try #require(hello["features"] as? [String])
            let iOSApp = RequestClientMetadata.Platform.iOS.rawValue + "-" + RequestClientMetadata.Surface.app.rawValue
            #expect(features.contains("todo_send_state") == (RequestClientMetadata.current.platform == iOSApp))
        }
    }
}

/// The receipts' cost on the main thread (docs/apple/performance.md P12, #10465): the time per event while a burst
/// arrives, against the same frames under a type no build reads, which is what an app before this one did with them
/// (decoded to nothing and dropped). It compares the two paths in one build, in whatever build the test host is (an
/// optimized one for numbers worth reporting), and prints `SEND-RECEIPT-COST` lines. Three runs of 3,128 frames: 3,000
/// receipts for other devices' sends (ignored), and Processing then a final state for 64 sends this app waits on.
@MainActor @Suite(.serialized)
struct SendReceiptsBurstTests {
    private func app() throws -> (AppModel, SyncSocket) {
        let hub = FakeHub()
        let transport = try #require(hub.client.transport)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "send-receipts-burst-" + UUID().uuidString))
        let socket = SyncSocket()
        model.linkHooksForTests = (transport: transport, socket: { socket })
        model.connectOnly(.demo)
        socket.deliver?(Data(#"{"type":"hello","features":["sessions_list"],"message_send_state":1,"todo_send_state":1}"#.utf8))
        return (model, socket)
    }

    private struct Burst {
        var frames: [Data] = []
        var waits: [(SendReceipt.Key, SendReceipt.Target)] = []
    }

    private func burst() -> Burst {
        var b = Burst()
        for i in 0..<1500 {
            b.frames.append(Data(SendReceiptsTests.messageFrame("processing", id: "other-\(i)").utf8))
            b.frames.append(Data(SendReceiptsTests.todoFrame("delivered", op: UUID().uuidString).utf8))
        }
        for i in 0..<32 {
            let op = UUID().uuidString, id = "mine-\(i)"
            b.waits.append((SendReceipt.Key(group: "", kind: .todo, id: op), .todo("t01")))
            b.waits.append((SendReceipt.Key(group: "", kind: .message, id: id), .session(machineID: "m1", sessionID: "s1")))
            for state in ["processing", "delivered"] {
                b.frames.append(Data(SendReceiptsTests.todoFrame(state, op: op).utf8))
                b.frames.append(Data(SendReceiptsTests.messageFrame(state, id: id).utf8))
            }
        }
        return b
    }

    /// Per event, in ms: median, 99th percentile, worst, and the whole burst's mean.
    private func summary(_ d: [Duration]) -> (median: Double, p99: Double, worst: Double, mean: Double) {
        let ms = d.map { Double($0.components.seconds) * 1000 + Double($0.components.attoseconds) / 1e15 }.sorted()
        return (ms[ms.count / 2], ms[min(ms.count - 1, Int(Double(ms.count) * 0.99))], ms.last ?? 0, ms.reduce(0, +) / Double(ms.count))
    }

    private func line(_ name: String, _ d: [Duration]) -> String {
        let s = summary(d)
        return String(format: "%@ n=%d median=%.4f p99=%.4f worst=%.3f mean=%.4f ms", name, d.count, s.median, s.p99, s.worst, s.mean)
    }

    @Test func aBurstCostsLittleOnTheMainThread() throws {
        let clock = ContinuousClock()
        for run in 1...3 {
            let (model, socket) = try app()
            let b = burst()
            let cells = b.waits.map { model.sendReceipts.expect($0.0, target: $0.1) }
            // Before: the same frames under a type no build reads, through the same socket.
            let before = b.frames.map { Data(String(decoding: $0, as: UTF8.self).replacingOccurrences(of: "_send_state\"", with: "_send_state_old\"").utf8) }
            let old = before.map { frame in clock.measure { socket.deliver?(frame) } }
            // After, the whole local path: decoding included, which a real socket does off the main thread.
            let whole = b.frames.map { frame in clock.measure { socket.deliver?(frame) } }
            #expect(cells.allSatisfy { $0.state == .delivered }, "every send this app waits on got its final state")
            #expect(model.sendReceipts.waiting == 0)
            // After, the main thread's part of a real socket: the event decoded already, then `AppModel.handle`.
            let again = b.waits.map { model.sendReceipts.expect($0.0, target: $0.1) }
            let events = b.frames.compactMap { ServerEvent.decode($0) }
            #expect(events.count == b.frames.count)
            let handled = events.map { event in clock.measure { model.handle(event, group: "") } }
            #expect(again.allSatisfy { $0.state == .delivered })
            for (name, d) in [("before(unknown type, socket path)", old), ("after(socket path, decode on main)", whole),
                              ("after(main thread: handle)", handled)] {
                print("SEND-RECEIPT-COST run=\(run) " + line(name, d))
            }
            // The budget: no main-thread item over 16 ms while typing or scrolling. The worst is printed, not held to
            // 100 ms: on a shared machine a preempted event measures the scheduler.
            #expect(summary(whole).p99 < 16)
            #expect(summary(handled).p99 < 16)
        }
    }
}
