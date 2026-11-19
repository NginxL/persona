import Foundation
import XCTest
@testable import PersonaScreenKit

/// One end of an in-memory byte pipe, as a transport.
final class PipeEnd: ScreenLinkTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var inbox: [Data] = []
    private var waiters: [CheckedContinuation<Data, Error>] = []
    private var closed = false
    weak var peer: PipeEnd?

    static func pair() -> (PipeEnd, PipeEnd) {
        let a = PipeEnd(), b = PipeEnd()
        a.peer = b
        b.peer = a
        return (a, b)
    }

    func send(_ data: Data) async throws {
        guard let peer, !isClosed else { throw ScreenLinkError.closed }
        peer.put(data)
    }

    func receive() async throws -> Data {
        try await withCheckedThrowingContinuation { k in
            lock.lock()
            if !inbox.isEmpty { let d = inbox.removeFirst(); lock.unlock(); k.resume(returning: d); return }
            if closed { lock.unlock(); k.resume(throwing: ScreenLinkError.closed); return }
            waiters.append(k)
            lock.unlock()
        }
    }

    func close() {
        shut()
        peer?.shut()
    }

    private var isClosed: Bool { lock.withLock { closed } }

    private func put(_ d: Data) {
        lock.lock()
        if let k = waiters.first { waiters.removeFirst(); lock.unlock(); k.resume(returning: d); return }
        inbox.append(d)
        lock.unlock()
    }

    private func shut() {
        lock.lock()
        closed = true
        let ws = waiters
        waiters = []
        lock.unlock()
        ws.forEach { $0.resume(throwing: ScreenLinkError.closed) }
    }
}

/// The node's end of one transport in a test: it reads whole messages and says what the test tells it.
final class FakeNode: @unchecked Sendable {
    let end: PipeEnd
    private var parser = ScreenVideoParser()
    private var pending: [ScreenLinkMessage] = []

    init(_ end: PipeEnd) { self.end = end }

    func say(_ m: ScreenLinkMessage) async throws { try await end.send(m.framed()) }

    /// The next message the phone sent, skipping acks and the session's own pings unless asked for.
    func next(skippingAcks: Bool = true, skippingPings: Bool = true) async throws -> ScreenLinkMessage {
        while true {
            while pending.isEmpty {
                for raw in try parser.push(try await end.receive()) {
                    pending.append(try ScreenLinkMessage.decode(type: raw.type, body: raw.body))
                }
            }
            let m = pending.removeFirst()
            if skippingAcks, case .ack = m { continue }
            if skippingPings, case .ping = m { continue }
            return m
        }
    }
}

/// Raw frames from the phone, recorded independently of ScreenLinkMessage's codec so a new wire type can be tested
/// before that codec implements it.
private final class RawNodeLog: @unchecked Sendable {
    private let end: PipeEnd
    private let lock = NSLock()
    private var seen: [(type: UInt8, body: Data)] = []

    init(_ end: PipeEnd) {
        self.end = end
        Task { await self.read() }
    }

    private func read() async {
        var parser = ScreenVideoParser()
        while let chunk = try? await end.receive() {
            guard let raws = try? parser.push(chunk) else { return }
            lock.withLock { for raw in raws { seen.append((raw.type, raw.body)) } }
        }
    }

    func count(type: UInt8, body: Data) -> Int {
        lock.withLock { seen.filter { $0.type == type && $0.body == body }.count }
    }

    func wait(type: UInt8, body: Data, count wanted: Int, timeout: Double = 1) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if count(type: type, body: body) >= wanted { return true }
            try? await Task.sleep(nanoseconds: 20_000_000)
        }
        return count(type: type, body: body) >= wanted
    }
}

final class ScreenLinkSessionTests: XCTestCase {
    private let paths = ScreenLinkPaths(token: "tok", cert: "pin", port: 4000, candidates: [.init(kind: "lan", addr: "192.0.2.10", prefix: 24)])

    private func open() async throws -> (ScreenLinkSession, FakeNode) {
        let (phone, node) = PipeEnd.pair()
        let n = FakeNode(node)
        let first = ScreenLinkMessage.paths(paths).framed()
        let s = ScreenLinkSession(rfbRelay: phone, first: first)
        return (s, n)
    }

    /// The session's events as they came: one task reads the stream (it allows one reader).
    private final class Events: @unchecked Sendable {
        private let lock = NSLock()
        private var seen: [ScreenLinkSession.Event] = []
        init(_ s: ScreenLinkSession) {
            Task { for await e in s.events { self.lock.withLock { self.seen.append(e) } } }
        }
        func wait(timeout: Double = 3, _ match: @escaping (ScreenLinkSession.Event) -> Bool) async -> Bool {
            let deadline = Date().addingTimeInterval(timeout)
            while Date() < deadline {
                if lock.withLock({ seen.contains(where: match) }) { return true }
                try? await Task.sleep(nanoseconds: 20_000_000)
            }
            return false
        }
    }

    func testRFBGoesBothWaysAndCopiesAreTrimmed() async throws {
        let (s, node) = try await open()
        let rfb = s.rfb()
        try await node.say(.data(channel: 1, offset: 0, bytes: Data("RFB ".utf8)))
        try await node.say(.data(channel: 1, offset: 2, bytes: Data("B 003.889\n".utf8))) // overlaps by two
        var got = Data()
        while got.count < 12 { got += try await rfb.receive() }
        XCTAssertEqual(String(decoding: got, as: UTF8.self), "RFB 003.889\n")
        try await rfb.send(Data("RFB 003.008\n".utf8))
        let got1 = try await node.next()
        XCTAssertEqual(got1, .data(channel: 1, offset: 0, bytes: Data("RFB 003.008\n".utf8)))
    }

    func testAGapDropsTheRelayAndEndsTheSession() async throws {
        let (s, node) = try await open()
        let rfb = s.rfb()
        try await node.say(.data(channel: 1, offset: 5, bytes: Data("x".utf8)))
        do {
            _ = try await rfb.receive()
            XCTFail("a gap was delivered")
        } catch {}
        let ended = await Events(s).wait { if case .ended = $0 { true } else { false } }
        XCTAssertTrue(ended)
    }

    func testAVideoStreamComesAsWholeMessagesAndAResetDropsAPartialOne() async throws {
        let (s, _) = try await open()
        let (phoneV, nodeV) = PipeEnd.pair()
        let vnode = FakeNode(nodeV)
        try await vnode.say(.resumed([ScreenLinkOffset(2, 0)]))
        let video = try await s.attachVideo(phoneV)
        XCTAssertEqual(video.channel, 2)
        let a = ScreenVideoWire.frame(4, body: Data(#"{"last":1}"#.utf8))
        let b = ScreenVideoWire.frame(3, body: Data(count: 30))
        try await vnode.say(.data(channel: 2, offset: 0, bytes: a.prefix(3)))
        try await vnode.say(.data(channel: 2, offset: 3, bytes: a.dropFirst(3) + b.prefix(7)))
        let got2 = try await video.receive()
        XCTAssertEqual(got2, a)
        let at = UInt64(a.count + 100)
        try await vnode.say(.reset(channel: 2, offset: at)) // the rest of b and more were dropped by the node
        let c = ScreenVideoWire.frame(4, body: Data(#"{"last":2}"#.utf8))
        try await vnode.say(.data(channel: 2, offset: at, bytes: c))
        let got3 = try await video.receive()
        XCTAssertEqual(got3, c, "the partial b is gone; c comes whole")
        try await video.send(ScreenVideoWire.frame(18, body: Data()))
        let got4 = try await vnode.next()
        XCTAssertEqual(got4, .data(channel: 2, offset: 0, bytes: ScreenVideoWire.frame(18, body: Data())))
    }

    func testCloseDeliversEverythingThenEnds() async throws {
        let (s, node) = try await open()
        let rfb = s.rfb()
        try await node.say(.data(channel: 1, offset: 0, bytes: Data("bye".utf8)))
        try await node.say(.close(channel: 1, offset: 3))
        let got5 = try await rfb.receive()
        XCTAssertEqual(got5, Data("bye".utf8))
        do { _ = try await rfb.receive(); XCTFail("past the close") } catch {}
    }

    /// Every received close, including one for an already ended channel, gets close_ack type 14 with the same channel
    /// and final offset. The ordinary data ack at that offset is a different frame and cannot confirm the close.
    func testCloseAndRepeatedCloseAreConfirmedAtTheFinalOffset() async throws {
        let (s, _) = try await open()
        let (phoneV, nodeV) = PipeEnd.pair()
        let vnode = FakeNode(nodeV)
        try await vnode.say(.resumed([ScreenLinkOffset(2, 0)]))
        let video = try await s.attachVideo(phoneV)
        let log = RawNodeLog(nodeV)
        defer { nodeV.close() }

        let bytes = ScreenVideoWire.frame(4, body: Data("bye".utf8))
        let final = UInt64(bytes.count)
        try await vnode.say(.data(channel: 2, offset: 0, bytes: bytes))
        try await vnode.say(.close(channel: 2, offset: final))
        let received = try await video.receive()
        XCTAssertEqual(received, bytes)
        do { _ = try await video.receive(); XCTFail("past the close") } catch {}
        let body = Data(ScreenLinkMessage.close(channel: 2, offset: final).framed().dropFirst(5))
        let first = await log.wait(type: 14, body: body, count: 1)
        XCTAssertTrue(first, "the phone did not confirm close with close_ack(channel: 2, final offset: \(final))")
        guard first else { return }
        XCTAssertEqual(log.count(type: 14, body: body), 1)
        XCTAssertEqual(log.count(type: 2, body: body), 1, "the final-data ack remains a separate, ordinary ack")

        try await vnode.say(.close(channel: 2, offset: final))
        do { _ = try await video.receive(); XCTFail("a repeated close reopened the channel") } catch {}
        let second = await log.wait(type: 14, body: body, count: 2)
        XCTAssertTrue(second, "a repeated close for an ended channel was not confirmed again")
        XCTAssertEqual(log.count(type: 14, body: body), 2)
    }

    func testTheWindowHoldsSendsUntilTheNodeAcknowledges() async throws {
        let (s, node) = try await open()
        let rfb = s.rfb()
        let big = Data(count: 256 << 10)
        try await rfb.send(big) // fills the window
        let second = Task { try await rfb.send(Data("more".utf8)) }
        try await Task.sleep(nanoseconds: 100_000_000)
        var sent = 0
        while sent < big.count {
            guard case let .data(_, _, bytes) = try await node.next() else { return XCTFail() }
            sent += bytes.count
        }
        try await node.say(.ack(channel: 1, offset: UInt64(big.count)))
        try await second.value
        let got6 = try await node.next()
        XCTAssertEqual(got6, .data(channel: 1, offset: UInt64(big.count), bytes: Data("more".utf8)))
    }

    /// A direct route: it attaches with the token, answers the first five pings, and the policy picks it; the channels
    /// stay on the relay in this stage.
    func testADirectRouteIsProbedAndChosen() async throws {
        let (s, node) = try await open()
        let events = Events(s)
        let pathsSeen = await events.wait { if case .paths = $0 { true } else { false } }
        XCTAssertTrue(pathsSeen)
        let (phoneD, nodeD) = PipeEnd.pair()
        let direct = FakeNode(nodeD)
        await s.addDirect(phoneD, route: "lan:192.0.2.10", kind: .lan, via: .wifi)
        let got7 = try await direct.next()
        XCTAssertEqual(got7, .attach(token: "tok"))
        try await direct.say(.attached)
        // Answer pings on both: the direct route at once, the relay 30 ms later.
        let answer = { (n: FakeNode, delay: UInt64) in
            Task {
                while case let .ping(v)? = try? await n.next(skippingPings: false) {
                    try? await Task.sleep(nanoseconds: delay)
                    try? await n.say(.pong(v, retransmitted: 0, sent: 10))
                }
            }
        }
        let a = answer(direct, 0), b = answer(node, 30_000_000)
        defer { a.cancel(); b.cancel() }
        let chosen = await events.wait(timeout: 6) { e in
            if case let .routes(routes, current, _) = e, current == "lan:192.0.2.10", routes["lan:192.0.2.10"]?.healthy == true { return true }
            return false
        }
        XCTAssertTrue(chosen, "the direct route measured and chosen")
    }
}

/// A relay socket in a test: a pipe end that says which subprotocol the hub selected.
final class FakeSocket: ScreenLinkSocket, @unchecked Sendable {
    let end: PipeEnd
    let subprotocol: String?
    init(_ end: PipeEnd, subprotocol: String?) {
        self.end = end
        self.subprotocol = subprotocol
    }
    func send(_ data: Data) async throws { try await end.send(data) }
    func receive() async throws -> Data { try await end.receive() }
    func close() { end.close() }
}

final class ScreenLinkStreamsTests: XCTestCase {
    /// Today's hub or node, or the web: no subprotocol selected, the stream is the relay's bytes unchanged.
    func testWithoutTheSubprotocolTheStreamIsRaw() async throws {
        let (phone, hub) = PipeEnd.pair()
        let saw = SessionBox()
        let stream = ScreenLinkRFBStream(socket: FakeSocket(phone, subprotocol: nil)) { saw.set($0) }
        try await hub.send(Data("RFB 003.889\n".utf8))
        let first = try await stream.receive()
        XCTAssertEqual(first, Data("RFB 003.889\n".utf8))
        try await stream.send(Data("RFB 003.008\n".utf8))
        let up = try await hub.receive()
        XCTAssertEqual(up, Data("RFB 003.008\n".utf8))
        try await hub.send(Data([1, 2, 3]))
        let next = try await stream.receive()
        XCTAssertEqual(next, Data([1, 2, 3]))
        XCTAssertNil(saw.get(), "no session")
    }

    /// The hub selected it: the stream is the session's RFB channel, and the video stream of the same pair joins it.
    func testWithTheSubprotocolTheStreamsAreChannels() async throws {
        let (phone, hub) = PipeEnd.pair()
        let node = FakeNode(hub)
        let saw = SessionBox()
        let registry = ScreenLinkRegistry()
        let stream = ScreenLinkRFBStream(socket: FakeSocket(phone, subprotocol: ScreenLinkWire.subprotocol)) {
            saw.set($0)
            registry.set($0, for: "K")
        }
        try await node.say(.paths(ScreenLinkPaths(token: "tok")))
        try await node.say(.data(channel: 1, offset: 0, bytes: Data("RFB 003.889\n".utf8)))
        let got = try await stream.receive()
        XCTAssertEqual(got, Data("RFB 003.889\n".utf8))
        XCTAssertNotNil(saw.get())
        XCTAssertTrue(registry.session(for: "K") === saw.get())

        let (phoneV, hubV) = PipeEnd.pair()
        let vnode = FakeNode(hubV)
        let video = ScreenLinkVideoStream(socket: FakeSocket(phoneV, subprotocol: ScreenLinkWire.subprotocol), session: registry.session(for: "K"))
        try await vnode.say(.resumed([ScreenLinkOffset(2, 0)]))
        let hello = ScreenVideoWire.frame(1, body: Data("{}".utf8))
        try await vnode.say(.data(channel: 2, offset: 0, bytes: hello))
        let v = try await video.receive()
        XCTAssertEqual(v, hello)
    }

    func testTheProbePlan() {
        var p = ProbePlan()
        XCTAssertEqual(p.due(["a", "b"], now: 0), ["a", "b"])
        XCTAssertEqual(p.due(["a", "b"], now: 1), [], "already open")
        p.failed("a", now: 2)
        XCTAssertEqual(p.due(["a"], now: 20), [])
        XCTAssertEqual(p.due(["a"], now: 32), ["a"])
        p.failed("a", now: 33)
        XCTAssertEqual(p.due(["a"], now: 80), [], "the wait doubled to 60 s")
        XCTAssertEqual(p.due(["a"], now: 93), ["a"])
        for i in 0..<10 { p.failed("a", now: 100 + Double(i)) }
        XCTAssertEqual(p.due(["a"], now: 400), [], "at most 5 min")
        XCTAssertEqual(p.due(["a"], now: 410), ["a"])
        p.failed("a", now: 411)
        p.networkChanged()
        XCTAssertEqual(p.due(["a"], now: 412), ["a"], "a network change tries again at once")
    }
}

final class SessionBox: @unchecked Sendable {
    private let lock = NSLock()
    private var s: ScreenLinkSession?
    func set(_ v: ScreenLinkSession) { lock.withLock { s = v } }
    func get() -> ScreenLinkSession? { lock.withLock { s } }
}

/// A node's end of one transport that answers by itself: pongs (after `delay`), `resumed` for `use` with what it
/// received of each channel, and `attached` for an attach; it keeps each channel's bytes once, by offset.
final class AutoNode: @unchecked Sendable {
    let end: PipeEnd
    private let lock = NSLock()
    private var got: [UInt8: Data] = [:]
    private var seen: [ScreenLinkMessage] = []
    private let delay: UInt64
    private let shared: SharedReceive
    /// The route went silent: nothing is answered (packets vanish, the connection stays open).
    var silent: Bool { get { lock.withLock { mute } } set { lock.withLock { mute = newValue } } }
    private var mute = false

    /// What the node received of each channel over all its transports (a node has one count per channel).
    final class SharedReceive: @unchecked Sendable {
        let lock = NSLock()
        var bytes: [UInt8: Data] = [:]
    }

    init(_ end: PipeEnd, delayMs: UInt64 = 0, shared: SharedReceive) {
        self.end = end
        delay = delayMs * 1_000_000
        self.shared = shared
        Task { await loop() }
    }

    private func loop() async {
        var parser = ScreenVideoParser()
        while let chunk = try? await end.receive() {
            guard let raws = try? parser.push(chunk) else { return }
            for raw in raws {
                guard let m = try? ScreenLinkMessage.decode(type: raw.type, body: raw.body) else { return }
                lock.withLock { seen.append(m) }
                if silent { continue }
                switch m {
                case let .ping(v):
                    if delay > 0 { try? await Task.sleep(nanoseconds: delay) }
                    try? await say(.pong(v, retransmitted: 0, sent: 100))
                case let .data(ch, off, bytes):
                    shared.lock.withLock {
                        let have = UInt64(shared.bytes[ch]?.count ?? 0)
                        if off <= have, off + UInt64(bytes.count) > have {
                            shared.bytes[ch, default: Data()].append(bytes.dropFirst(Int(have - off)))
                        }
                    }
                case let .use(entries):
                    let back = shared.lock.withLock { entries.map { ScreenLinkOffset($0.channel, UInt64(shared.bytes[$0.channel]?.count ?? 0)) } }
                    try? await say(.resumed(back))
                case .attach:
                    try? await say(.attached)
                default:
                    break
                }
            }
        }
    }

    func say(_ m: ScreenLinkMessage) async throws { try await end.send(m.framed()) }

    func messages() -> [ScreenLinkMessage] { lock.withLock { seen } }

    func used() -> Bool { messages().contains { if case .use = $0 { true } else { false } } }
}

final class ScreenLinkSwitchTests: XCTestCase {
    private final class Collected: @unchecked Sendable {
        private let lock = NSLock()
        private(set) var events: [ScreenLinkSession.Event] = []
        init(_ s: ScreenLinkSession) { Task { for await e in s.events { self.lock.withLock { self.events.append(e) } } } }
        func all() -> [ScreenLinkSession.Event] { lock.withLock { events } }
        func moves() -> [PathChange] { all().compactMap { if case let .moved(c) = $0 { c } else { nil } } }
        func reopens() -> Int { all().filter { if case .reopen = $0 { true } else { false } }.count }
        func lost() -> Int { all().filter { if case .lost = $0 { true } else { false } }.count }
    }

    private func until(_ what: String, timeout: Double = 6, _ cond: () async -> Bool) async {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if await cond() { return }
            try? await Task.sleep(nanoseconds: 20_000_000)
        }
        XCTFail("timed out: \(what)")
    }

    /// The whole story on one session: it starts on the relay; a faster direct route is used; RFB goes both ways over
    /// it byte for byte; the direct route drops and RFB goes on over the relay at once, still byte for byte.
    func testMovesToADirectRouteAndBackWithoutLosingAByte() async throws {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        let relay = AutoNode(nodeR, delayMs: 30, shared: shared)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(ScreenLinkPaths(token: "tok")).framed())
        let events = Collected(s)
        let rfb = s.rfb()
        var down = Data()
        func nodeSends(_ n: AutoNode, _ text: String) async throws {
            let bytes = Data(text.utf8)
            try await n.say(.data(channel: 1, offset: UInt64(down.count), bytes: bytes))
            down += bytes
        }
        var got = Data()
        func read(_ n: Int) async throws { while got.count < n { got += try await rfb.receive() } }

        try await nodeSends(relay, "RFB 003.889\n")
        try await read(down.count)
        try await rfb.send(Data("up-1 ".utf8))
        await until("the first route: the relay") { events.moves().first?.reason == .start }

        let (phoneD, nodeD) = PipeEnd.pair()
        let direct = AutoNode(nodeD, shared: shared)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(phoneD, route: "lan:a", kind: .lan, via: .wifi)
        await until("moved to the direct route") { events.moves().contains { $0.to.kind == .lan && $0.reason == .faster } }
        await until("use went out on the direct route") { direct.used() }
        try await Task.sleep(nanoseconds: 100_000_000) // the node's resumed arrives
        try await rfb.send(Data("up-2 ".utf8))
        try await nodeSends(direct, "over direct ")
        try await read(down.count)

        phoneD.close() // the direct route drops
        await until("back on the relay") { relay.used() }
        try await Task.sleep(nanoseconds: 100_000_000)
        try await rfb.send(Data("up-3".utf8))
        try await nodeSends(relay, "over the relay again")
        try await read(down.count)
        XCTAssertEqual(got, down, "every byte the node sent, once and in order")
        await until("the node got every byte") { shared.lock.withLock { shared.bytes[1] } == Data("up-1 up-2 up-3".utf8) }
        await until("the policy saw the route lost") { events.moves().contains { $0.reason == .lost && $0.to.kind == .relay } }
        XCTAssertEqual(events.reopens(), 0, "the relay never dropped")
    }

    /// A direct route that goes silent (its connection stays open, nothing comes back) is left for the relay within
    /// about two seconds.
    func testASilentDirectRouteIsLeft() async throws {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        let relay = AutoNode(nodeR, delayMs: 30, shared: shared)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(ScreenLinkPaths(token: "tok")).framed())
        let events = Collected(s)
        let (phoneD, nodeD) = PipeEnd.pair()
        let direct = AutoNode(nodeD, shared: shared)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(phoneD, route: "lan:a", kind: .lan, via: .wifi)
        await until("on the direct route") { direct.used() }
        let at = Date()
        direct.silent = true
        await until("back on the relay", timeout: 5) { relay.used() }
        XCTAssertLessThan(Date().timeIntervalSince(at), 3.0, "the 1.5 s silence rule, plus a tick")
        await until("the move says lost") { events.moves().contains { $0.reason == .lost } }
    }

    /// #9218: a direct connection whose attach is never answered (here: added while its route is muted, so the node's
    /// `attached` vanishes, as when a network drops packets for a moment) is lost within `attachWithin`, so the app dials
    /// the route again. Before, it was never pinged and never lost, the route stayed counted as open, and after the
    /// network came back the stream stayed on the hub's relay.
    func testADirectConnectionWhoseAttachIsNeverAnsweredIsLost() async throws {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        let relay = AutoNode(nodeR, delayMs: 30, shared: shared)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(ScreenLinkPaths(token: "tok")).framed())
        let events = Collected(s)
        let (phoneD, nodeD) = PipeEnd.pair()
        let direct = AutoNode(nodeD, shared: shared)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(phoneD, route: "lan:a", kind: .lan, via: .wifi)
        await until("on the direct route") { direct.used() }
        await s.muteDirect(kind: .lan, true)
        await until("back on the relay", timeout: 5) { relay.used() }
        await until("the route lost") { events.lost() == 1 }

        // The app dials it again while it is still muted: the connection opens, its attach is never heard.
        let (phoneD2, nodeD2) = PipeEnd.pair()
        _ = AutoNode(nodeD2, shared: shared)
        let added = Date()
        await s.addDirect(phoneD2, route: "lan:a", kind: .lan, via: .wifi)
        try await Task.sleep(nanoseconds: 500_000_000) // the node's `attached` came and vanished
        await s.muteDirect(kind: .lan, false) // the network is back: only a new dial finds out
        await until("the unanswered attach is lost, so the app dials again", timeout: 6) { events.lost() == 2 }
        let took = Date().timeIntervalSince(added)
        XCTAssertGreaterThanOrEqual(took, ScreenLinkSession.attachWithin - 0.3)
        XCTAssertLessThan(took, ScreenLinkSession.attachWithin + 1.5, "attachWithin, plus a tick")

        // The next dial attaches, and the route is measured again.
        let (phoneD3, nodeD3) = PipeEnd.pair()
        _ = AutoNode(nodeD3, shared: shared)
        await s.addDirect(phoneD3, route: "lan:a", kind: .lan, via: .wifi)
        await until("the route measured again", timeout: 5) {
            events.all().contains { if case let .routes(r, _, _) = $0 { r["lan:a"] != nil } else { false } }
        }
        XCTAssertEqual(events.lost(), 2, "the attached one stays")
    }

    /// The relay drops while the direct route carries the session: the session goes on, and the app is asked to open
    /// the relay again; the reopened one attaches with the token and becomes the channel's relay.
    func testARelayThatDropsUnderADirectRouteIsOpenedAgain() async throws {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        _ = AutoNode(nodeR, delayMs: 30, shared: shared)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(ScreenLinkPaths(token: "tok")).framed())
        let events = Collected(s)
        let rfb = s.rfb()
        let (phoneD, nodeD) = PipeEnd.pair()
        let direct = AutoNode(nodeD, shared: shared)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(phoneD, route: "lan:a", kind: .lan, via: .wifi)
        await until("on the direct route") { direct.used() }
        phoneR.close() // the hub restarted
        await until("asked to reopen the relay") { events.reopens() == 1 }
        try await rfb.send(Data("still here".utf8))
        await until("RFB went on over the direct route") { shared.lock.withLock { shared.bytes[1] } == Data("still here".utf8) }

        let (phoneR2, nodeR2) = PipeEnd.pair()
        let relay2 = AutoNode(nodeR2, shared: shared)
        await s.reattach(phoneR2, video: false)
        await until("the reopened relay attached with the token") {
            relay2.messages().contains { if case .attach(token: "tok") = $0 { true } else { false } }
        }
        await until("and is measured again") { relay2.messages().contains { if case .ping = $0 { true } else { false } } }
    }
}
