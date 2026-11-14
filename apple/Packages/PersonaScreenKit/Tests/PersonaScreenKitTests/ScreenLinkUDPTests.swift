import Foundation
import XCTest
@testable import PersonaScreenKit

/// In-memory datagram sockets: the phone's end is a `ScreenUDPSocket`; the node's end drops what `drop` picks.
final class DatagramEnd: ScreenUDPSocket, @unchecked Sendable {
    private let lock = NSLock()
    private var inbox: [Data] = []
    private var waiters: [CheckedContinuation<Data, Error>] = []
    private var closed = false
    weak var peer: DatagramEnd?
    /// Drops a datagram this end sends when it says so (by its order).
    var drop: (@Sendable (Int) -> Bool)?
    private var sent = 0

    static func pair() -> (DatagramEnd, DatagramEnd) {
        let a = DatagramEnd(), b = DatagramEnd()
        a.peer = b
        b.peer = a
        return (a, b)
    }

    func send(_ packet: Data) {
        let n = lock.withLock { () -> Int in sent += 1; return sent - 1 }
        if let drop, drop(n) { return }
        peer?.put(packet)
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
        lock.lock()
        closed = true
        let ws = waiters
        waiters = []
        lock.unlock()
        ws.forEach { $0.resume(throwing: ScreenLinkError.closed) }
    }

    private func put(_ d: Data) {
        lock.lock()
        if closed { lock.unlock(); return }
        if let k = waiters.first { waiters.removeFirst(); lock.unlock(); k.resume(returning: d); return }
        inbox.append(d)
        lock.unlock()
    }
}

/// A direct connection's pipe end that exports keys as its TLS connection would: both ends compute the same bytes.
final class ExportingPipe: ScreenLinkTransport, ScreenLinkKeyExporter, @unchecked Sendable {
    let end: PipeEnd
    init(_ end: PipeEnd) { self.end = end }
    func send(_ data: Data) async throws { try await end.send(data) }
    func receive() async throws -> Data { try await end.receive() }
    func close() { end.close() }
    func exportKeyingMaterial(label: String, context: Data, length: Int) -> Data? { Self.material(context, length) }
    static func material(_ context: Data, _ length: Int) -> Data {
        Data((0 ..< length).map { UInt8(truncatingIfNeeded: $0 * 13 + Int(context.first ?? 0)) })
    }
}

/// A direct route's node in a test: its TLS connection (attach, pongs, `use`, `udp_open`) and its UDP flow (the proof,
/// `use`, pongs, video as fragments with parity, NACKs, give-ups).
final class UDPRouteNode: @unchecked Sendable {
    let tls: PipeEnd
    let udp: DatagramEnd
    let flowID = Data([9, 8, 7, 6, 5, 4, 3, 2])
    private let lock = NSLock()
    private var tlsSeen: [ScreenLinkMessage] = []
    private var udpSeen: [ScreenUDPMessage] = []
    private var sealer: ScreenUDPSealer
    private let opener: TestAESGCM
    private let openIV: Data
    private var window = ScreenUDPReplayWindow()
    /// What the node sent of the video, by offset, so NACKs can be answered.
    private var sentMessages: [UInt64: (Data, Bool)] = [:]
    /// The flow goes silent: its packets vanish (the TLS connection still answers).
    var udpSilent: Bool { get { lock.withLock { mute } } set { lock.withLock { mute = newValue } } }
    private var mute = false
    /// Where the stream goes on after a give-up.
    var resetTo: UInt64 = 0

    init(tls: PipeEnd, udp: DatagramEnd) {
        self.tls = tls
        self.udp = udp
        let keys = ScreenUDPKeyMaterial(ExportingPipe.material(flowID, ScreenUDPWire.keyMaterialLength))!
        sealer = ScreenUDPSealer(cipher: TestAESGCM(keys.toPhoneKey), iv: keys.toPhoneIV, flow: flowID)
        opener = TestAESGCM(keys.toNodeKey)
        openIV = keys.toNodeIV
        Task { await tlsLoop() }
        Task { await udpLoop() }
    }

    private func tlsLoop() async {
        var parser = ScreenVideoParser()
        while let chunk = try? await tls.receive() {
            guard let raws = try? parser.push(chunk) else { return }
            for raw in raws {
                guard let m = try? ScreenLinkMessage.decode(type: raw.type, body: raw.body) else { return }
                lock.withLock { tlsSeen.append(m) }
                switch m {
                case let .ping(v): try? await say(.pong(v, retransmitted: 0, sent: 100))
                case .attach: try? await say(.attached)
                case let .use(entries): try? await say(.resumed(entries.map { ScreenLinkOffset($0.channel, 0) }))
                case .udpOpen:
                    let id = flowID.base64EncodedString().replacingOccurrences(of: "=", with: "")
                    try? await say(.udpReady(ScreenLinkUDPReady(port: 5000, flow: id)))
                default: break
                }
            }
        }
    }

    func say(_ m: ScreenLinkMessage) async throws { try await tls.send(m.framed()) }

    private func send(_ m: ScreenUDPMessage) {
        let p = lock.withLock { sealer.seal(m.encoded()) }
        if let p { udp.send(p) }
    }

    private func udpLoop() async {
        while let p = try? await udp.receive() {
            guard let head = ScreenUDPPacket.read(p), head.flow == flowID,
                  let body = ScreenUDPPacket.open(p, cipher: opener, iv: openIV, number: head.number),
                  lock.withLock({ window.accept(head.number) }),
                  let m = try? ScreenUDPMessage.decode(body, fromNode: false) else { continue }
            lock.withLock { udpSeen.append(m) }
            if udpSilent { continue }
            switch m {
            case let .helloPhone(echo):
                send(echo == 77 ? .helloNode(challenge: 0, validated: true) : .helloNode(challenge: 77, validated: false))
            case let .use(entries):
                send(.resumed(entries.map { ScreenLinkOffset($0.channel, 0) }))
            case let .ping(micros, _, _):
                send(.pong(micros: micros, dataBytes: 0, parityBytes: 0, resentBytes: 0))
            case let .nack(ch, entries):
                for e in entries {
                    guard let (msg, droppable) = lock.withLock({ sentMessages[e.offset] }) else { continue }
                    for (h, frag) in Self.fragments(msg, ch: ch, at: e.offset, ratio: 0, droppable: droppable)
                    where h.block == e.block && h.index == e.index {
                        send(.data(h, fragment: frag))
                    }
                }
            case let .skip(ch, _):
                send(.reset(channel: ch, offset: resetTo))
            default:
                break
            }
        }
    }

    /// Sends a whole video message as the node does, with `ratio` parity; `lose` drops some of its packets on the way,
    /// and a message not kept can't be sent again (as one the node dropped).
    func video(_ msg: Data, ch: UInt8, at off: UInt64, ratio: Int = 0, droppable: Bool = true, lose: Set<Int> = [], keep: Bool = true) {
        if keep { lock.withLock { sentMessages[off] = (msg, droppable) } }
        for (i, f) in Self.fragments(msg, ch: ch, at: off, ratio: ratio, droppable: droppable).enumerated()
        where !lose.contains(i) {
            send(.data(f.0, fragment: f.1))
        }
    }

    static func fragments(_ msg: Data, ch: UInt8, at off: UInt64, ratio: Int, droppable: Bool) -> [(ScreenUDPFragmentHead, Data)] {
        let bytes = [UInt8](msg)
        var out: [(ScreenUDPFragmentHead, Data)] = []
        for (bi, b) in ScreenUDPLayout.blocks(length: bytes.count).enumerated() {
            let m = ScreenUDPLayout.parity(data: b.data, ratio: ratio)
            let shards = (0 ..< b.data).map { j -> [UInt8] in
                let from = (b.first + j) * ScreenUDPWire.fragment
                return Array(bytes[from ..< min(from + ScreenUDPWire.fragment, bytes.count)])
            }
            let parity = try! ReedSolomon.encode(shards.map { $0 + [UInt8](repeating: 0, count: b.shardLength - $0.count) }, parity: m)
            let head = { (i: Int) in
                ScreenUDPFragmentHead(channel: ch, offset: off, length: UInt32(bytes.count), block: UInt16(bi), index: UInt16(i),
                                      data: UInt8(b.data), parity: UInt8(m), flags: droppable ? ScreenUDPWire.droppable : 0)
            }
            out += shards.enumerated().map { (head($0.offset), Data($0.element)) }
            out += parity.enumerated().map { (head(b.data + $0.offset), Data($0.element)) }
        }
        return out
    }

    func tlsMessages() -> [ScreenLinkMessage] { lock.withLock { tlsSeen } }
    func udpMessages() -> [ScreenUDPMessage] { lock.withLock { udpSeen } }
    func usedOnTLS(_ ch: UInt8) -> UInt64? {
        tlsMessages().reversed().lazy.compactMap { m -> UInt64? in
            if case let .use(e) = m { return e.first { $0.channel == ch }?.offset }
            return nil
        }.first
    }
    func usedOnUDP() -> Bool { udpMessages().contains { if case .use = $0 { true } else { false } } }
}

final class ScreenLinkUDPTests: XCTestCase {
    private final class Collected: @unchecked Sendable {
        private let lock = NSLock()
        private var events: [ScreenLinkSession.Event] = []
        init(_ s: ScreenLinkSession) { Task { for await e in s.events { self.lock.withLock { self.events.append(e) } } } }
        func all() -> [ScreenLinkSession.Event] { lock.withLock { events } }
        func moves() -> [PathChange] { all().compactMap { if case let .moved(c) = $0 { c } else { nil } } }
        func openFlow() -> String? { all().lazy.compactMap { if case let .openFlow(r, _) = $0 { r } else { nil } }.first }
    }

    private func until(_ what: String, timeout: Double = 8, _ cond: () async -> Bool) async {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if await cond() { return }
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTFail("timed out: \(what)")
    }

    private struct Rig {
        let s: ScreenLinkSession
        let events: Collected
        let route: UDPRouteNode
        let video: ScreenLinkChannelEnd
        let videoNode: FakeNode
    }

    /// A session whose video rides a direct route's UDP flow.
    private func rig(udp: Bool = true, file: StaticString = #filePath, line: UInt = #line) async throws -> Rig {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        _ = AutoNode(nodeR, delayMs: 30, shared: shared)
        let paths = ScreenLinkPaths(token: "tok", cert: "pin", port: 4000, candidates: [.init(kind: "lan", addr: "192.0.2.10", prefix: 24)], udp: 1)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(paths).framed())
        let events = Collected(s)
        if udp { await s.enableUDP(ScreenUDPSetup { TestAESGCM($0) }) }
        let (phoneV, nodeV) = PipeEnd.pair()
        let vnode = FakeNode(nodeV)
        try await vnode.say(.resumed([ScreenLinkOffset(2, 0)]))
        let video = try await s.attachVideo(phoneV)
        let (phoneD, nodeD) = PipeEnd.pair()
        let (phoneU, nodeU) = DatagramEnd.pair()
        let route = UDPRouteNode(tls: nodeD, udp: nodeU)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(ExportingPipe(phoneD), route: "lan:a", kind: .lan, via: .wifi)
        if udp {
            await until("the session asked for a flow") { events.openFlow() == "lan:a" }
            await s.addFlow(phoneU, route: "lan:a")
        }
        return Rig(s: s, events: events, route: route, video: video, videoNode: vnode)
    }

    private func frame(_ n: Int, size: Int) -> Data {
        ScreenVideoWire.frame(3, body: Data((0 ..< size).map { UInt8(truncatingIfNeeded: $0 + n) }))
    }

    /// The video moves onto the flow once the route carries the session, comes whole and in order over it, and goes
    /// back to the route's TLS connection when the flow goes silent, from where the phone was.
    func testTheVideoRidesTheFlowAndFallsBackToTheRoute() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        await until("onto udp") { r.events.moves().contains { $0.to.transport == .udp && $0.reason == .faster } }
        var off: UInt64 = 0
        var sent = Data()
        for i in 0 ..< 6 {
            let m = frame(i, size: [200, 5000, 30000][i % 3])
            r.route.video(m, ch: 2, at: off)
            off += UInt64(m.count)
            sent += m
        }
        var got = Data()
        while got.count < sent.count { got += try await r.video.receive() }
        XCTAssertEqual(got, sent)
        await until("the phone acknowledged it all") {
            r.route.udpMessages().contains { if case let .ack(2, o) = $0 { o == off } else { false } }
        }

        r.route.udpSilent = true // the flow's packets vanish
        await until("back on the route's TLS connection", timeout: 5) { r.route.usedOnTLS(2) == off }
        await until("the move says lost") { r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp } }
        let next = frame(9, size: 800)
        try await r.route.say(.data(channel: 2, offset: off, bytes: next))
        let after = try await r.video.receive()
        XCTAssertEqual(after, next)
    }

    /// Packets lost on the flow are rebuilt from parity or asked for again; the video still comes whole and in order.
    func testLossOnTheFlowIsRebuiltOrAskedFor() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        try await Task.sleep(nanoseconds: 50_000_000)
        var off: UInt64 = 0
        var sent = Data()
        for i in 0 ..< 8 {
            let m = frame(i, size: 12000) // 11 shards
            r.route.video(m, ch: 2, at: off, ratio: i < 4 ? 2000 : 0, lose: [1, 5]) // with parity, then without
            off += UInt64(m.count)
            sent += m
        }
        var got = Data()
        while got.count < sent.count { got += try await r.video.receive() }
        XCTAssertEqual(got, sent)
        let nacks = r.route.udpMessages().filter { if case .nack = $0 { true } else { false } }.count
        XCTAssertGreaterThan(nacks, 0, "the messages without parity were asked for")
    }

    /// A frame that holds the picture back too long is given up; the node's reset says where the stream goes on.
    func testAGivenUpFrameGoesOnAtTheReset() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        try await Task.sleep(nanoseconds: 50_000_000)
        let a = frame(1, size: 400), lost = frame(2, size: 5000), b = frame(3, size: 400)
        r.route.video(a, ch: 2, at: 0)
        let first = try await r.video.receive()
        XCTAssertEqual(first, a)
        let lostAt = UInt64(a.count)
        // One shard of it comes (the phone knows it is a frame); the rest never does: the node has dropped it, so the
        // NACKs find nothing, and a later frame shows the gap.
        r.route.video(lost, ch: 2, at: lostAt, lose: Set(0 ..< 4), keep: false)
        r.route.resetTo = lostAt + UInt64(lost.count) + 1000
        r.route.video(b, ch: 2, at: lostAt + UInt64(lost.count))
        await until("given up") { r.route.udpMessages().contains { if case .skip(2, lostAt) = $0 { true } else { false } } }
        let c = frame(4, size: 600)
        r.route.video(c, ch: 2, at: r.route.resetTo)
        let next = try await r.video.receive()
        XCTAssertEqual(next, c, "the stream goes on at the reset")
    }

    /// On cellular (the route's interface, or the phone's path under Tailscale) no flow is asked for; back on Wi-Fi one is.
    func testNoFlowOnCellular() async throws {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        _ = AutoNode(nodeR, delayMs: 30, shared: shared)
        let paths = ScreenLinkPaths(token: "tok", cert: "pin", port: 4000, candidates: [.init(kind: "tailscale", addr: "100.64.0.9")], udp: 1)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(paths).framed())
        let events = Collected(s)
        await s.enableUDP(ScreenUDPSetup { TestAESGCM($0) })
        await s.setRelayVia(.cellular)
        let (phoneD, nodeD) = PipeEnd.pair()
        let (_, nodeU) = DatagramEnd.pair()
        let route = UDPRouteNode(tls: nodeD, udp: nodeU)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(ExportingPipe(phoneD), route: "tailscale:a", kind: .tailscale, via: .other)
        try await Task.sleep(nanoseconds: 1_000_000_000)
        XCTAssertFalse(route.tlsMessages().contains { if case .udpOpen = $0 { true } else { false } }, "asked for a flow on cellular")
        await s.setRelayVia(.wifi)
        await until("asked for a flow back on Wi-Fi") { route.tlsMessages().contains { if case .udpOpen = $0 { true } else { false } } }
    }

    /// Without the app turning UDP on, no flow is asked for and the route carries the video as before.
    func testWithoutUDPNoFlowIsAskedFor() async throws {
        let r = try await rig(udp: false)
        try await Task.sleep(nanoseconds: 1_500_000_000)
        XCTAssertFalse(r.route.tlsMessages().contains { if case .udpOpen = $0 { true } else { false } })
        XCTAssertNil(r.events.openFlow())
    }
}
