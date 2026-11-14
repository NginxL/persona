#if os(iOS) && canImport(PersonaScreenKit)
import CryptoKit
import Foundation
import Network
import os
import PersonaScreenKit

// The live screen's routes on the iPhone (#6398, docs/extensions/computer-use.md "Live screen routes (#6398)"): the
// hub's relays opened as a screen session when the hub and the node offer one, the node's direct candidates probed
// and measured, the route policy run, and the session's channels (picture and input) moved to the route it picks.

enum ScreenRoutes {
    nonisolated static let log = Logger(subsystem: "persona", category: "screen-link")

    /// Direct routes ship on since the acceptance before real devices passed (#6398); false again turns them off in the
    /// next build, and the hub's `screen_link_off` keeps every live screen on its relays without one. A DEBUG build
    /// launched with -showScreenRoutes has them whatever this says.
    nonisolated static let releaseEnabled = true

    /// Whether this app offers direct routes at all (Settings shows the switch only then).
    nonisolated static var offered: Bool {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-showScreenRoutes") { return true }
        #endif
        return releaseEnabled
    }

    /// Whether this phone asks the hub for a screen session: offered, and the Settings switch on (it is by default).
    nonisolated static var enabled: Bool { offered && ScreenRoutePrefs.automatic }

    /// The video over UDP on direct routes (#7873) stays off in release builds until the check on real devices; a
    /// DEBUG build launched with -showScreenUDP has it. It needs direct routes on: the same Settings switch covers it.
    nonisolated static let udpReleaseEnabled = false

    nonisolated static var udpEnabled: Bool {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-showScreenUDP") { return enabled }
        #endif
        return udpReleaseEnabled && enabled
    }

    /// The Settings switch went off: live sessions go back to their relays and close their direct connections now.
    @MainActor static func turnedOff() {
        for pair in probers.keys {
            if let s = ScreenLinkRegistry.shared.session(for: pair) { Task { await s.directOff() } }
        }
    }

    /// The probers of the sessions open now, by pair.
    @MainActor static var probers: [String: ScreenLinkProber] = [:]
    /// Who hears of each live screen's route changes, by pair (its RFBSession's video link).
    @MainActor static var listeners: [String: @MainActor (PathChange) -> Void] = [:]
    /// And of the route in use as measured each second, with why the node has no direct routes when it says.
    @MainActor static var meters: [String: @MainActor (LinkStats?, String?) -> Void] = [:]
    /// And of the UDP flow carrying the video, about once a second (#7873): the mobile data budget leaves room for its
    /// parity and resends.
    @MainActor static var flows: [String: @MainActor (ScreenUDPStatus?) -> Void] = [:]
    /// Each live screen's routes as last measured and the one in use, by pair (the DEBUG command channel reads them).
    @MainActor static var latest: [String: (routes: [String: LinkStats], current: String, change: PathChange?)] = [:]

    /// A session opened for pair: register it for the video stream and start probing its direct routes.
    nonisolated static func opened(_ s: ScreenLinkSession, pair: String, group: String, machine: String) {
        ScreenLinkRegistry.shared.set(s, for: pair)
        if udpEnabled { Task { await s.enableUDP(ScreenUDPSetup { ScreenUDPAESGCM(key: $0) }) } }
        Task { @MainActor in
            let p = ScreenLinkProber(session: s, pair: pair, group: group, machine: machine)
            probers[pair] = p
            await p.run()
            probers[pair] = nil
            listeners[pair] = nil
            meters[pair] = nil
            flows[pair] = nil
            latest[pair] = nil
            ScreenLinkRegistry.shared.remove(pair)
        }
    }

    /// One of the hub's relays of pair's live screen opened again to rejoin its session (attach), nil without the hub.
    @MainActor static func reopenRelay(group: String, machine: String, pair: String, video: Bool) -> ScreenLinkRelaySocket? {
        guard let api = AppModel.shared.api(group) else { return nil }
        let path = video ? "/api/v1/machines/\(machine)/screen/video/connect" : "/api/v1/machines/\(machine)/ports/5900/connect"
        return ScreenLinkRelaySocket(RFBWebSocket(api: api, path: path, pair: pair, protocols: [ScreenLinkWire.subprotocol], attach: true))
    }
}

// MARK: Relays

/// The hub's relay as a session socket: the WebSocket offers the subprotocol and says which one the hub selected.
nonisolated final class ScreenLinkRelaySocket: ScreenLinkSocket, @unchecked Sendable {
    let ws: RFBWebSocket
    init(_ ws: RFBWebSocket) { self.ws = ws }
    var subprotocol: String? { ws.subprotocol }
    func send(_ data: Data) async throws { try await ws.send([UInt8](data)) }
    func receive() async throws -> Data { Data(try await ws.receive()) }
    func close() { ws.close() }
}

/// A screen stream as the RFB client and the video stream read it.
nonisolated final class ScreenLinkRFBTransport: RFBTransport, @unchecked Sendable {
    let stream: ScreenLinkByteStream
    init(_ stream: ScreenLinkByteStream) { self.stream = stream }
    func send(_ bytes: [UInt8]) async throws { try await stream.send(Data(bytes)) }
    func receive() async throws -> [UInt8] { [UInt8](try await stream.receive()) }
    func close() { stream.close() }
}

// MARK: Direct connections

/// A direct connection to one of the node's candidates: TLS 1.3 with the session's ALPN, accepting only the node's
/// certificate (its SHA-256 from paths), on the one phone interface the candidate may use.
nonisolated final class ScreenLinkDirect: ScreenLinkTransport, ScreenLinkKeyExporter, @unchecked Sendable {
    private let conn: NWConnection
    private static let queue = DispatchQueue(label: "persona.screen-link.direct")

    private init(_ conn: NWConnection) { self.conn = conn }

    /// Connected and verified within `timeout`, or nil.
    static func dial(host: String, port: Int, pin: String, interface: NWInterface, timeout: Double = 2) async -> ScreenLinkDirect? {
        guard let p = NWEndpoint.Port(rawValue: UInt16(clamping: port)) else { return nil }
        let tls = NWProtocolTLS.Options()
        let sec = tls.securityProtocolOptions
        sec_protocol_options_set_min_tls_protocol_version(sec, .TLSv13)
        sec_protocol_options_add_tls_application_protocol(sec, ScreenLinkWire.alpn)
        sec_protocol_options_set_verify_block(sec, { @Sendable _, trust, complete in
            let t = sec_trust_copy_ref(trust).takeRetainedValue()
            guard let chain = SecTrustCopyCertificateChain(t) as? [SecCertificate], let leaf = chain.first else {
                complete(false)
                return
            }
            let sum = SHA256.hash(data: SecCertificateCopyData(leaf) as Data)
            complete(Data(sum).base64URL == pin)
        }, queue)
        let params = NWParameters(tls: tls)
        params.requiredInterface = interface
        params.prohibitExpensivePaths = false
        let conn = NWConnection(host: NWEndpoint.Host(host), port: p, using: params)
        let ready: Bool = await withCheckedContinuation { k in
            let once = Once()
            conn.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready: once.run { k.resume(returning: true) }
                case .failed, .cancelled, .waiting: once.run { k.resume(returning: false) }
                default: break
                }
            }
            conn.start(queue: queue)
            queue.asyncAfter(deadline: .now() + timeout) { once.run { k.resume(returning: false) } }
        }
        guard ready else {
            conn.cancel()
            return nil
        }
        conn.stateUpdateHandler = nil
        return ScreenLinkDirect(conn)
    }

    func send(_ data: Data) async throws {
        try await withCheckedThrowingContinuation { (k: CheckedContinuation<Void, Error>) in
            conn.send(content: data, completion: .contentProcessed { @Sendable e in
                if let e { k.resume(throwing: e) } else { k.resume() }
            })
        }
    }

    func receive() async throws -> Data {
        try await withCheckedThrowingContinuation { k in
            conn.receive(minimumIncompleteLength: 1, maximumLength: 64 << 10) { @Sendable data, _, done, error in
                if let data, !data.isEmpty { k.resume(returning: data) } else if let error { k.resume(throwing: error) } else if done {
                    k.resume(throwing: ScreenLinkError.closed)
                } else {
                    k.resume(returning: Data())
                }
            }
        }
    }

    func close() { conn.cancel() }

    /// The TLS connection's exporter (RFC 8446): a UDP flow's keys (#7873), the same bytes the node's Go TLS gives.
    func exportKeyingMaterial(label: String, context: Data, length: Int) -> Data? {
        guard let meta = conn.metadata(definition: NWProtocolTLS.definition) as? NWProtocolTLS.Metadata else { return nil }
        let secret = label.withCString { l in
            context.withUnsafeBytes { c in
                sec_protocol_metadata_create_secret_with_context(meta.securityProtocolMetadata, label.utf8.count, l, c.count,
                                                                 c.bindMemory(to: UInt8.self).baseAddress!, length)
            }
        }
        guard let secret else { return nil }
        var out = Data()
        (secret as DispatchData).enumerateBytes { buf, _, _ in out.append(contentsOf: buf) }
        return out.count == length ? out : nil
    }

    private final class Once: @unchecked Sendable {
        private let lock = NSLock()
        private var done = false
        func run(_ f: () -> Void) {
            let first = lock.withLock { defer { done = true }; return !done }
            if first { f() }
        }
    }
}

/// A UDP flow's socket (#7873): datagrams to the node's address on the route's own interface, as the direct
/// connection's.
nonisolated final class ScreenLinkUDPSocket: ScreenUDPSocket, @unchecked Sendable {
    private let conn: NWConnection
    private static let queue = DispatchQueue(label: "persona.screen-link.udp")

    private init(_ conn: NWConnection) { self.conn = conn }

    static func open(host: String, port: Int, interface: NWInterface) -> ScreenLinkUDPSocket? {
        guard let p = NWEndpoint.Port(rawValue: UInt16(clamping: port)) else { return nil }
        let params = NWParameters.udp
        params.requiredInterface = interface
        params.prohibitExpensivePaths = false
        let conn = NWConnection(host: NWEndpoint.Host(host), port: p, using: params)
        conn.start(queue: queue)
        return ScreenLinkUDPSocket(conn)
    }

    func send(_ packet: Data) { conn.send(content: packet, completion: .idempotent) }

    func receive() async throws -> Data {
        try await withCheckedThrowingContinuation { k in
            conn.receiveMessage { @Sendable data, _, _, error in
                if let error { k.resume(throwing: error) } else { k.resume(returning: data ?? Data()) }
            }
        }
    }

    func close() { conn.cancel() }
}

extension Data {
    nonisolated var base64URL: String {
        base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

// MARK: The phone's interfaces

/// The phone's interfaces now: the ones Network.framework can pin a connection to, with their addresses.
nonisolated struct PhoneInterfaces: Sendable {
    var list: [PhoneInterface] = []
    var byName: [String: NWInterface] = [:]
    /// The interface the hub's relay leaves on.
    var relayVia: LinkStats.Via = .other

    static func read(_ paths: [NWPath]) -> PhoneInterfaces {
        var out = PhoneInterfaces()
        let addresses = Self.addresses()
        for p in paths {
            for i in p.availableInterfaces where out.byName[i.name] == nil {
                out.byName[i.name] = i
                let via: LinkStats.Via = switch i.type {
                case .wifi: .wifi
                case .cellular: .cellular
                case .wiredEthernet: .wired
                default: .other
                }
                out.list.append(PhoneInterface(name: i.name, via: via, addresses: addresses[i.name] ?? []))
            }
        }
        if let first = paths.first, first.status == .satisfied {
            out.relayVia = first.usesInterfaceType(.wifi) ? .wifi : first.usesInterfaceType(.cellular) ? .cellular
                : first.usesInterfaceType(.wiredEthernet) ? .wired : .other
        }
        return out
    }

    /// Each interface's IPv4 and IPv6 addresses with their prefix lengths (getifaddrs).
    private static func addresses() -> [String: [PhoneInterface.Address]] {
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let first = head else { return [:] }
        defer { freeifaddrs(head) }
        var out: [String: [PhoneInterface.Address]] = [:]
        for p in sequence(first: first, next: { $0.pointee.ifa_next }) {
            let ifa = p.pointee
            guard let sa = ifa.ifa_addr, let mask = ifa.ifa_netmask else { continue }
            let family = Int32(sa.pointee.sa_family)
            guard family == AF_INET || family == AF_INET6 else { continue }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            let len = socklen_t(family == AF_INET ? MemoryLayout<sockaddr_in>.size : MemoryLayout<sockaddr_in6>.size)
            guard getnameinfo(sa, len, &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 else { continue }
            let chars = host.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }
            let text = String(decoding: chars, as: UTF8.self).split(separator: "%").first.map(String.init) ?? ""
            let prefix = Self.prefix(mask, family: family)
            if let a = PhoneInterface.Address(text, prefix: prefix) {
                out[String(cString: ifa.ifa_name), default: []].append(a)
            }
        }
        return out
    }

    private static func prefix(_ mask: UnsafeMutablePointer<sockaddr>, family: Int32) -> Int {
        let bytes: [UInt8] = family == AF_INET
            ? mask.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { withUnsafeBytes(of: $0.pointee.sin_addr) { Array($0) } }
            : mask.withMemoryRebound(to: sockaddr_in6.self, capacity: 1) { withUnsafeBytes(of: $0.pointee.sin6_addr) { Array($0) } }
        return bytes.reduce(0) { $0 + $1.nonzeroBitCount }
    }
}

// MARK: Probing

/// Probes a session's direct routes: dials each candidate the phone may reach, on its interface, as the probe plan
/// says, and hands what connects to the session, which measures it and runs the route policy. Ends with the session.
/// Its own actor: reading the phone's interfaces (getifaddrs) and dialing never touch the main actor.
actor ScreenLinkProber {
    private let session: ScreenLinkSession
    private let pair: String, group: String, machine: String
    private var paths: ScreenLinkPaths?
    private var plan = ProbePlan()
    private var interfaces = PhoneInterfaces()
    private var monitors: [NWPathMonitor] = []
    private var latest: [ObjectIdentifier: NWPath] = [:]
    private let wake = RetryWake()

    init(session: ScreenLinkSession, pair: String, group: String, machine: String) {
        self.session = session
        self.pair = pair
        self.group = group
        self.machine = machine
    }

    /// Runs until the session ends.
    func run() async {
        startMonitors()
        defer {
            monitors.forEach { $0.cancel() }
            wake.cancel()
        }
        for await e in session.events {
            switch e {
            case let .paths(p):
                paths = p
                if let why = p.unavailable { ScreenRoutes.log.info("no direct routes: \(why, privacy: .public)") }
                probe()
            case let .routes(routes, current, decision):
                let stats = routes[current], why = paths?.unavailable, pair = pair
                await MainActor.run {
                    ScreenRoutes.meters[pair]?(stats, why)
                    ScreenRoutes.latest[pair] = (routes, current, ScreenRoutes.latest[pair]?.change)
                }
                if case let .move(to, reason) = decision {
                    let kind = routes[to]?.kind.rawValue ?? "?"
                    ScreenRoutes.log.info("route: \(kind, privacy: .public) (\(reason.rawValue, privacy: .public)); \(routes.count) measured; in use \(current == to ? "yes" : "no", privacy: .public)")
                }
            case let .lost(route):
                plan.failed(route, now: ScreenLinkSession.monotonic())
                armWake()
            case let .moved(change):
                let kind = change.to.kind.rawValue, why = change.reason.rawValue, pair = pair
                ScreenRoutes.log.info("moved to \(kind, privacy: .public) (\(why, privacy: .public))")
                await MainActor.run {
                    ScreenRoutes.listeners[pair]?(change)
                    if let l = ScreenRoutes.latest[pair] { ScreenRoutes.latest[pair] = (l.routes, l.current, change) }
                }
            case let .openFlow(route, port):
                openFlow(route, port: port)
            case let .flow(status):
                let pair = pair
                await MainActor.run { ScreenRoutes.flows[pair]?(status) }
            case let .reopen(video):
                let group = group, machine = machine, pair = pair
                if let relay = await MainActor.run(body: { ScreenRoutes.reopenRelay(group: group, machine: machine, pair: pair, video: video) }) {
                    await session.reattach(relay, video: video)
                }
            case .ended:
                return
            }
        }
    }

    private func startMonitors() {
        for m in [NWPathMonitor(), NWPathMonitor(requiredInterfaceType: .other)] {
            let id = ObjectIdentifier(m)
            m.pathUpdateHandler = { @Sendable [weak self] path in
                Task { await self?.network(id, path) }
            }
            m.start(queue: .global(qos: .utility))
            monitors.append(m)
        }
    }

    private func network(_ id: ObjectIdentifier, _ path: NWPath) {
        let before = interfaces.list
        latest[id] = path
        let ordered = monitors.compactMap { latest[ObjectIdentifier($0)] }
        interfaces = PhoneInterfaces.read(ordered)
        let changed = before != interfaces.list
        let s = session, via = interfaces.relayVia
        Task { await s.setRelayVia(via) }
        if changed {
            plan.networkChanged()
            // Routes whose interface went away are lost now, not after their pings time out.
            let names = Set(interfaces.list.map(\.name))
            for c in paths?.candidates ?? [] {
                if let i = RouteEligibility.interface(for: c, among: before), !names.contains(i.name) {
                    let route = Self.route(c)
                    Task { await s.dropDirect(route: route, networkChanged: true) }
                    plan.forget(route)
                }
            }
            probe()
        }
    }

    private static func route(_ c: ScreenLinkPaths.Candidate) -> String { "\(c.kind):\(c.addr)" }

    /// The node opened a UDP flow beside a direct route (#7873): a socket to the same address, on the route's
    /// interface. Without one the session gives the flow up and the route keeps its TLS connection.
    private func openFlow(_ route: String, port: Int) {
        guard let c = paths?.candidates.first(where: { Self.route($0) == route }),
              let i = RouteEligibility.interface(for: c, among: interfaces.list), let nw = interfaces.byName[i.name],
              let socket = ScreenLinkUDPSocket.open(host: c.addr, port: port, interface: nw) else { return }
        ScreenRoutes.log.info("udp flow on \(c.kind, privacy: .public)")
        let s = session
        Task { await s.addFlow(socket, route: route) }
    }

    private func dialed(_ conn: ScreenLinkDirect?, route: String, kind: LinkStats.Kind, via: LinkStats.Via) async {
        guard let conn else {
            ScreenRoutes.log.info("direct \(kind.rawValue, privacy: .public): no connection")
            plan.failed(route, now: ScreenLinkSession.monotonic())
            armWake()
            return
        }
        plan.attached(route)
        await session.addDirect(conn, route: route, kind: kind, via: via)
    }

    private func probe() {
        guard let paths, let pin = paths.cert, let port = paths.port else { return }
        var eligible: [String: (ScreenLinkPaths.Candidate, NWInterface)] = [:]
        for c in paths.candidates {
            if let i = RouteEligibility.interface(for: c, among: interfaces.list), let nw = interfaces.byName[i.name] {
                eligible[Self.route(c)] = (c, nw)
            }
        }
        let now = ScreenLinkSession.monotonic()
        for route in plan.due(Array(eligible.keys).sorted(), now: now) {
            guard let (c, nw) = eligible[route] else { continue }
            let kind = LinkStats.Kind(rawValue: c.kind) ?? .lan
            let via = interfaces.list.first { $0.name == nw.name }?.via ?? .other
            Task {
                let conn = await ScreenLinkDirect.dial(host: c.addr, port: port, pin: pin, interface: nw)
                await self.dialed(conn, route: route, kind: kind, via: via)
            }
        }
        armWake()
    }

    /// Probes again when the next candidate waiting after a failure is due (#7791). Without it only new paths or a
    /// network change dialed again, and a lost direct route stayed lost: the live screen kept the hub's relay.
    private func armWake() {
        let now = ScreenLinkSession.monotonic()
        wake.arm(in: plan.nextRetry(after: now).map { $0 - now }) { [weak self] in await self?.probe() }
    }
}
#endif
