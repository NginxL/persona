import Foundation

// The phone's end of a screen session (#6398, docs/extensions/computer-use.md "Live screen routes (#6398)"): its
// channels (RFB, then a video stream) over its transports (the hub's relays, direct connections), the pings that
// measure each route, and the route policy. The app gives it transports and takes channel ends that look like a byte
// stream, so the RFB client and the video stream don't know a session is there.
//
// This stage measures and decides; channels stay on their relays (moving them is the next stage).

/// A transport of a screen session as the session needs it: the hub's relays and direct connections alike.
public protocol ScreenLinkTransport: AnyObject, Sendable {
    func send(_ data: Data) async throws
    /// The next bytes that came; throws once closed.
    func receive() async throws -> Data
    func close()
}

public enum ScreenLinkError: Error, Equatable {
    /// The channel or the session ended.
    case closed
    /// The relay's first message wasn't what a session starts with.
    case notASession
}

public actor ScreenLinkSession {
    /// What the app hears about: the node's routes, and about once a second each route's numbers and the policy's
    /// decision.
    public enum Event: Sendable {
        case paths(ScreenLinkPaths)
        case routes([String: LinkStats], current: String, decision: PathPolicy.Decision)
        /// A direct route's connection ended (it failed, or the app dropped it): dial it again later.
        case lost(String)
        case ended
    }

    /// The route id of the hub's relay in `PathPolicy` and in `Event.routes`.
    public static let relayRoute = "relay"

    public nonisolated let events: AsyncStream<Event>
    private let emit: AsyncStream<Event>.Continuation
    private let now: @Sendable () -> Double

    private struct Link {
        let transport: ScreenLinkTransport
        let writer: AsyncStream<Data>.Continuation
        var relayFor: UInt8? // a relay: the channel the hub opened it for (0: a video relay not named yet)
        var route: String? // a direct one: its route
        var kind: LinkStats.Kind
        var via: LinkStats.Via
        var attached: Bool
        var meter = LinkMeter()
        var lastPing = -Double.infinity
    }

    private struct Chan {
        let id: UInt8
        var receiver = LinkReceiver()
        var sender: LinkSender
        var active: Int
        var assembler: VideoMessageAssembler?
        var inbox: [Data] = []
        var readers: [CheckedContinuation<Data, Error>] = []
        var roomWaiters: [CheckedContinuation<Void, Error>] = []
        var closeAt: UInt64?
        var ended = false
    }

    private var links: [Int: Link] = [:]
    private var nextLink = 0
    private var chans: [UInt8: Chan] = [:]
    private var paths: ScreenLinkPaths?
    private var relayVia: LinkStats.Via = .wifi
    private var policy = PathPolicy(relay: ScreenLinkSession.relayRoute)
    private var lost: Set<String> = []
    private var networkChanged = false
    private var pendingVideo: [Int: CheckedContinuation<UInt8, Error>] = [:]
    private var ended = false
    private var ticker: Task<Void, Never>?

    /// A session over the RFB relay the hub selected the subprotocol on. `first` is what the relay already said (the
    /// app read it to see the subprotocol); `now` is a monotonic clock in seconds.
    public init(rfbRelay: ScreenLinkTransport, first: Data = Data(), now: @escaping @Sendable () -> Double = ScreenLinkSession.monotonic) {
        (events, emit) = AsyncStream.makeStream(of: Event.self)
        self.now = now
        chans[ScreenLinkWire.rfb] = Chan(id: ScreenLinkWire.rfb, sender: LinkSender(window: 256 << 10), active: 0)
        Task { await self.start(rfbRelay, first: first) }
    }

    public static let monotonic: @Sendable () -> Double = {
        Double(DispatchTime.now().uptimeNanoseconds) / 1e9
    }

    private func start(_ t: ScreenLinkTransport, first: Data) {
        _ = register(t, relayFor: ScreenLinkWire.rfb, kind: .relay, via: relayVia, first: first)
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 250_000_000)
                guard let self, await self.tick() else { return }
            }
        }
    }

    /// A transport joins: its reader, and a writer that sends its messages in order. Ids start at 0 (the RFB relay).
    private func register(_ t: ScreenLinkTransport, relayFor: UInt8?, route: String? = nil, kind: LinkStats.Kind,
                          via: LinkStats.Via, first: Data = Data()) -> Int {
        let id = nextLink
        nextLink += 1
        let (stream, writer) = AsyncStream.makeStream(of: Data.self)
        links[id] = Link(transport: t, writer: writer, relayFor: relayFor, route: route, kind: kind, via: via, attached: relayFor != nil)
        Task { [weak self] in
            for await frame in stream {
                do { try await t.send(frame) } catch { await self?.lose(id); return }
            }
        }
        read(id, t, first: first)
        // Bytes the app sent before this transport was there (the RFB relay registers on the session's first turn)
        // go out now.
        for (ch, c) in chans where c.active == id { pump(ch) }
        return id
    }

    // MARK: Channel ends

    /// The RFB channel as a byte stream.
    public nonisolated func rfb() -> ScreenLinkChannelEnd { ScreenLinkChannelEnd(session: self, channel: ScreenLinkWire.rfb) }

    /// A new video stream's relay (the hub selected the subprotocol on …/screen/video/connect): its first message names
    /// the video channel; the channel as a byte stream of whole video messages.
    public func attachVideo(_ relay: ScreenLinkTransport, first: Data = Data()) async throws -> ScreenLinkChannelEnd {
        guard !ended else { throw ScreenLinkError.closed }
        let id = register(relay, relayFor: 0, kind: .relay, via: relayVia, first: first)
        let ch = try await withCheckedThrowingContinuation { pendingVideo[id] = $0 }
        return ScreenLinkChannelEnd(session: self, channel: ch)
    }

    func receive(_ ch: UInt8) async throws -> Data {
        guard var c = chans[ch] else { throw ScreenLinkError.closed }
        if !c.inbox.isEmpty {
            let d = c.inbox.removeFirst()
            chans[ch] = c
            return d
        }
        if c.ended || closeReached(c) {
            if !c.ended {
                chans[ch]?.ended = true
                if ch == ScreenLinkWire.rfb { end() }
            }
            throw ScreenLinkError.closed
        }
        return try await withCheckedThrowingContinuation { k in
            chans[ch]?.readers.append(k)
        }
    }

    func send(_ ch: UInt8, _ data: Data) async throws {
        while let c = chans[ch], !c.ended, !c.sender.hasRoom {
            try await withCheckedThrowingContinuation { (k: CheckedContinuation<Void, Error>) in chans[ch]?.roomWaiters.append(k) }
        }
        guard var c = chans[ch], !c.ended else { throw ScreenLinkError.closed }
        c.sender.append(data, at: now())
        chans[ch] = c
        pump(ch)
    }

    func close(_ ch: UInt8) {
        guard var c = chans[ch], !c.ended else { return }
        out(c.active, .close(channel: ch, offset: c.sender.end))
        c.ended = true
        chans[ch] = c
        wake(ch, error: ScreenLinkError.closed)
        if ch == ScreenLinkWire.rfb { end() }
    }

    // MARK: Routes

    /// The phone's interface the relay leaves on, as the app's path monitor sees it.
    public func setRelayVia(_ via: LinkStats.Via) {
        if via != relayVia { networkChanged = true }
        relayVia = via
    }

    /// A direct connection to one of the node's candidates, for route `route` over the phone's `via` interface: it
    /// attaches with the session's token, then is pinged and measured.
    public func addDirect(_ t: ScreenLinkTransport, route: String, kind: LinkStats.Kind, via: LinkStats.Via) {
        guard !ended, let token = paths?.token else { t.close(); return }
        let id = register(t, relayFor: nil, route: route, kind: kind, via: via)
        out(id, .attach(token: token))
    }

    /// The routes the app no longer has (their interface went away), so the policy treats them as lost now.
    public func dropDirect(route: String, networkChanged changed: Bool) {
        networkChanged = networkChanged || changed
        for (id, l) in links where l.route == route { lose(id) }
    }

    public var currentRoute: String { policy.current }

    // MARK: Transports

    private func read(_ id: Int, _ t: ScreenLinkTransport, first: Data) {
        Task { [weak self] in
            var parser = ScreenVideoParser()
            var chunk = first
            do {
                while true {
                    if chunk.isEmpty { chunk = try await t.receive() }
                    let raws = try parser.push(chunk)
                    chunk = Data()
                    for raw in raws {
                        guard let self else { return }
                        await self.handle(try ScreenLinkMessage.decode(type: raw.type, body: raw.body), from: id)
                    }
                }
            } catch {
                await self?.lose(id)
            }
        }
    }

    private func out(_ id: Int, _ m: ScreenLinkMessage) {
        links[id]?.writer.yield(m.framed())
    }

    private func pump(_ ch: UInt8) {
        guard var c = chans[ch], links[c.active] != nil else { return }
        while let (off, bytes) = c.sender.next(max: 16 << 10) {
            out(c.active, .data(channel: ch, offset: off, bytes: bytes))
        }
        chans[ch] = c
    }

    private func handle(_ m: ScreenLinkMessage, from id: Int) {
        guard !ended else { return }
        switch m {
        case let .data(ch, off, bytes):
            guard var c = chans[ch] else { return }
            do {
                let fresh = try c.receiver.accept(bytes, at: off)
                if !fresh.isEmpty {
                    if c.assembler != nil {
                        let whole = try c.assembler!.push(fresh)
                        if !whole.isEmpty { c.inbox.append(whole) }
                    } else {
                        c.inbox.append(fresh)
                    }
                }
                chans[ch] = c
                out(id, .ack(channel: ch, offset: c.receiver.offset))
                deliver(ch)
                finishIfClosed(ch)
            } catch {
                lose(id) // a gap: this transport skipped bytes
            }
        case let .ack(ch, off):
            guard var c = chans[ch] else { return }
            c.sender.acknowledged(through: off)
            let waiters = c.sender.hasRoom ? c.roomWaiters : []
            if c.sender.hasRoom { c.roomWaiters = [] }
            chans[ch] = c
            waiters.forEach { $0.resume() }
        case let .paths(p):
            paths = p
            emit.yield(.paths(p))
        case .attached:
            links[id]?.attached = true
            for _ in 0..<5 { ping(id) } // the first probe: five round trips at once
        case let .pong(sent, retrans, packets):
            guard var l = links[id] else { return }
            let rtt = (now() * 1e6 - Double(sent)) / 1000
            l.meter.pong(rttMs: max(0, rtt), retransmitted: retrans, sent: packets, counts: l.route != nil, at: now())
            links[id] = l
        case let .resumed(entries):
            // A new video relay's first message names its channel.
            if let k = pendingVideo.removeValue(forKey: id), let e = entries.first {
                chans[e.channel] = Chan(id: e.channel, sender: LinkSender(window: 64 << 10), active: id, assembler: VideoMessageAssembler())
                links[id]?.relayFor = e.channel
                k.resume(returning: e.channel)
                return
            }
            for e in entries {
                chans[e.channel]?.sender.resume(from: e.offset)
                pump(e.channel)
            }
        case let .close(ch, off):
            chans[ch]?.closeAt = off
            finishIfClosed(ch)
        case let .reset(ch, off):
            chans[ch]?.receiver.reset(to: off)
            chans[ch]?.assembler?.reset()
            if let c = chans[ch] { out(id, .ack(channel: ch, offset: c.receiver.offset)) }
        case .use, .attach, .ping, .unknown:
            break // the node's to receive, or a type this side doesn't know
        }
    }

    private func deliver(_ ch: UInt8) {
        guard var c = chans[ch] else { return }
        while !c.inbox.isEmpty, !c.readers.isEmpty {
            c.readers.removeFirst().resume(returning: c.inbox.removeFirst())
        }
        chans[ch] = c
    }

    private func closeReached(_ c: Chan) -> Bool { c.closeAt.map { c.receiver.offset >= $0 } ?? false }

    /// The node closed the channel and everything before the close was read: the channel ends.
    private func finishIfClosed(_ ch: UInt8) {
        guard let c = chans[ch], closeReached(c), c.inbox.isEmpty, !c.ended else { return }
        chans[ch]?.ended = true
        wake(ch, error: ScreenLinkError.closed)
        if ch == ScreenLinkWire.rfb { end() }
    }

    private func wake(_ ch: UInt8, error: Error) {
        guard var c = chans[ch] else { return }
        let readers = c.readers, waiters = c.roomWaiters
        c.readers = []
        c.roomWaiters = []
        chans[ch] = c
        readers.forEach { $0.resume(throwing: error) }
        waiters.forEach { $0.resume(throwing: error) }
    }

    /// A transport ended. A relay ends the channels it carries (as the stream ending does today); a direct one is a
    /// route lost.
    private func lose(_ id: Int) {
        guard let l = links.removeValue(forKey: id) else { return }
        l.writer.finish()
        l.transport.close()
        if let k = pendingVideo.removeValue(forKey: id) { k.resume(throwing: ScreenLinkError.closed) }
        if let route = l.route {
            lost.insert(route)
            if !ended { emit.yield(.lost(route)) }
        }
        for (ch, c) in chans where c.active == id && !c.ended {
            chans[ch]?.ended = true
            wake(ch, error: ScreenLinkError.closed)
            if ch == ScreenLinkWire.rfb { end() }
        }
    }

    private func end() {
        guard !ended else { return }
        ended = true
        ticker?.cancel()
        for id in Array(links.keys) { lose(id) }
        for ch in Array(chans.keys) {
            chans[ch]?.ended = true
            wake(ch, error: ScreenLinkError.closed)
        }
        pendingVideo.values.forEach { $0.resume(throwing: ScreenLinkError.closed) }
        pendingVideo = [:]
        emit.yield(.ended)
        emit.finish()
    }

    // MARK: Measuring

    private func ping(_ id: Int) {
        links[id]?.lastPing = now()
        out(id, .ping(UInt64(now() * 1e6)))
    }

    private var lastLook = -Double.infinity

    /// Every 250 ms: pings due (the RFB relay every second, others every two), and once a second the policy.
    private func tick() -> Bool {
        guard !ended else { return false }
        let t = now()
        for (id, l) in links where l.attached && l.relayFor != 0 {
            let every = l.relayFor == ScreenLinkWire.rfb ? 1.0 : 2.0
            if t - l.lastPing >= every { ping(id) }
        }
        guard t - lastLook >= 1 else { return true }
        lastLook = t
        var routes: [String: LinkStats] = [:]
        for l in links.values {
            if l.relayFor == ScreenLinkWire.rfb {
                routes[Self.relayRoute] = l.meter.stats(kind: .relay, via: relayVia, at: t)
            } else if let r = l.route, l.attached {
                routes[r] = l.meter.stats(kind: l.kind, via: l.via, at: t)
            }
        }
        let decision = policy.decide(routes, lost: lost, networkChanged: networkChanged, now: t)
        lost = []
        networkChanged = false
        emit.yield(.routes(routes, current: policy.current, decision: decision))
        return true
    }
}

/// One channel of a session as a byte stream (`RFBTransport`'s shape): RFB's bytes, or a video stream's whole messages.
public final class ScreenLinkChannelEnd: ScreenLinkTransport, @unchecked Sendable {
    public let session: ScreenLinkSession
    public let channel: UInt8

    init(session: ScreenLinkSession, channel: UInt8) {
        self.session = session
        self.channel = channel
    }

    public func send(_ data: Data) async throws { try await session.send(channel, data) }
    public func receive() async throws -> Data { try await session.receive(channel) }
    public func close() {
        let s = session, ch = channel
        Task { await s.close(ch) }
    }
}
