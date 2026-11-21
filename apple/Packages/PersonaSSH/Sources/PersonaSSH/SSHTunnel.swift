import Foundation
import Network
import NIOCore
import NIOSSH
import NIOTransportServices
import Synchronization

/// The app's SSH tunnel to its hub (docs/operations/network.md「Connecting to hub through SSH」), in-process (no system ssh: the sandboxed
/// Mac app can neither run /usr/bin/ssh nor read ~/.ssh). It listens on 127.0.0.1 (a port of its own) and carries
/// every connection made there — HTTP requests and WebSockets alike — through one SSH session to the hub, one
/// direct-tcpip channel each.
///
/// The session is dialled when first needed and again after it drops, with backoff: requests that come during the
/// backoff fail at once with the last error. A host key that is not the pinned one, or a key the server does not take,
/// stops it for good (`SSHTunnelError.needsUser`) until a new tunnel is made with what the user fixed.
///
/// Everything mutable lives on the tunnel's event loop.
public final class SSHTunnel: @unchecked Sendable {
    public enum Phase: Sendable, Equatable {
        case idle
        case connecting
        case connected
        case failed(SSHTunnelError)
    }

    public let config: SSHTunnelConfig
    private let identity: SSHIdentity
    private let pinnedHostKey: String?
    private let timings: SSHTunnelTimings
    private let loop: EventLoop
    let traceID = PersonaSSH.traceID("tunnel")

    /// Told of every new phase, on the tunnel's event loop. Set before `start`.
    public var onPhase: (@Sendable (Phase) -> Void)?

    private let port = Mutex(0)
    private let current = Mutex(Phase.idle)
    // On `loop` only.
    private var listener: Channel?
    private var session: Channel?
    private var dialing: EventLoopFuture<Channel>?
    private var connecting: DialAttempt?
    private var failures = 0
    private var retryAt = NIODeadline.uptimeNanoseconds(0)
    private var lastError: SSHTunnelError?
    private var stopped = false
    private var seenHostKey: (fingerprint: String, algorithm: String)?

    /// One thread for every tunnel: the traffic is a phone's, and channels joined by `Glue` must share a loop.
    public static let sharedGroup = NIOTSEventLoopGroup(loopCount: 1, defaultQoS: .userInitiated)

    /// `pinnedHostKey`: the server's fingerprint (`SHA256:…`) the user confirmed; nil when not known yet (the first
    /// connection then fails with `.hostKeyUnknown` carrying it).
    public init(config: SSHTunnelConfig, identity: SSHIdentity, pinnedHostKey: String?, timings: SSHTunnelTimings = .init(),
                group: NIOTSEventLoopGroup = SSHTunnel.sharedGroup) {
        self.config = config
        self.identity = identity
        self.pinnedHostKey = pinnedHostKey
        self.timings = timings
        loop = group.next()
    }

    /// The loopback port it listens on (0 before `start`).
    public var localPort: Int { port.withLock { $0 } }
    public var phase: Phase { current.withLock { $0 } }
    /// `http://127.0.0.1:<port>`: the hub's address through the tunnel.
    public var localURL: URL? { localPort == 0 ? nil : URL(string: "http://127.0.0.1:\(localPort)") }

    /// Connects, logs in and opens one channel to the hub's address (so a server that will not forward there fails
    /// here, not on the first request), then listens on the loopback: at `preferring` when it is free (the port the
    /// last tunnel had, so addresses handed out stay good), else on any free one.
    @discardableResult
    public func start(preferring preferred: Int = 0) async throws -> Int {
        try await withTaskCancellationHandler {
            try Task.checkCancellation()
            let port = try await loop.flatSubmit { self.connection() }
                .flatMap { self.open(on: $0, joining: nil) }
                .flatMap { self.listen(preferred) }
                .get()
            try Task.checkCancellation()
            return port
        } onCancel: {
            self.stop()
        }
    }

    /// Connects, logs in and checks the hub's address opens, then closes: what 「测试连接」 does. Returns the
    /// server's key (its fingerprint and algorithm).
    public func verify() async throws -> (fingerprint: String, algorithm: String) {
        defer { stop() }
        return try await withTaskCancellationHandler {
            try Task.checkCancellation()
            try await loop.flatSubmit { self.connection() }.flatMap { self.open(on: $0, joining: nil) }.get()
            try Task.checkCancellation()
            return try await loop.submit { self.seenHostKey ?? ("", "") }.get()
        } onCancel: {
            self.stop()
        }
    }

    /// Closes the listener and the session; nothing dials again.
    public func stop() {
        loop.execute {
            self.stopped = true
            // The TCP channel exists before authentication puts it in `session`. Closing while logging in must
            // finish its wait now, rather than leave the next foreground visit waiting for the old timeout.
            let connecting = self.connecting
            self.connecting = nil
            connecting?.ready.fail(SSHTunnelError.stopped)
            connecting?.channel?.close(promise: nil)
            self.listener?.close(promise: nil)
            self.listener = nil
            self.session?.close(promise: nil)
            self.session = nil
            self.set(.idle)
        }
    }

    /// Drops the SSH session (the network changed under it): the next request dials a new one.
    public func dropSession() {
        loop.execute { self.session?.close(promise: nil) }
    }

    // MARK: Session

    private func set(_ phase: Phase) {
        let changed = current.withLock { old in
            defer { old = phase }
            return old != phase
        }
        if changed { onPhase?(phase) }
    }

    /// The logged-in session: the one there is, the one being dialled, or a new one (not during the backoff).
    private func connection() -> EventLoopFuture<Channel> {
        loop.preconditionInEventLoop()
        if stopped { return loop.makeFailedFuture(SSHTunnelError.stopped) }
        if let session, session.isActive { return loop.makeSucceededFuture(session) }
        if let dialing { return dialing }
        if let lastError, NIODeadline.now() < retryAt { return loop.makeFailedFuture(lastError) }
        set(.connecting)
        let attemptID = PersonaSSH.traceID("dial")
        trace("\(traceID ?? "tunnel") connection requested: \(session == nil ? "no current session" : "current session inactive")")
        let f = dial(attemptID)
        dialing = f
        f.whenComplete { result in
            self.dialing = nil
            guard !self.stopped else {
                if case .success(let channel) = result { channel.close(promise: nil) }
                return
            }
            switch result {
            case .success(let channel):
                trace("\(self.traceID) \(attemptID) session authenticated")
                self.session = channel
                self.failures = 0
                self.lastError = nil
                self.set(.connected)
                channel.closeFuture.whenComplete { _ in
                    trace("\(self.traceID) \(attemptID) close observed")
                    guard self.session === channel else { return }
                    self.session = nil
                    trace("\(self.traceID) \(attemptID) current owner cleared")
                    if !self.stopped { self.set(.idle) }
                }
            case .failure(let error):
                let e = error as? SSHTunnelError ?? .unreachable(String(describing: error))
                self.lastError = e
                self.failures += 1
                // What the user has to fix waits for them (a new tunnel); anything else is tried again later.
                self.retryAt = e.needsUser ? .distantFuture : .now() + .milliseconds(Int64(self.timings.backoff(after: self.failures) * 1000))
                self.set(.failed(e))
            }
        }
        return f
    }

    private func dial(_ attemptID: String?) -> EventLoopFuture<Channel> {
        trace("\(traceID) \(attemptID) dial start")
        let attempt = DialAttempt(on: loop)
        connecting = attempt
        let ready = attempt.ready
        let hostKeys = HostKeyCheck(pinned: pinnedHostKey)
        let auth = KeyAuth(user: config.user, key: identity.nioKey)
        let tcp = NWProtocolTCP.Options()
        tcp.noDelay = true
        // A session the network dropped without a word (Wi-Fi to cellular) is found dead in about 35 s.
        tcp.enableKeepalive = true
        tcp.keepaliveIdle = 20
        tcp.keepaliveInterval = 5
        tcp.keepaliveCount = 3
        let handshake = loop.scheduleTask(in: .milliseconds(Int64(timings.connect + timings.handshake) * 1000)) {
            ready.fail(SSHTunnelError.timedOut)
        }
        let connected = NIOTSConnectionBootstrap(group: loop)
            .connectTimeout(.milliseconds(Int64(timings.connect * 1000)))
            .channelOption(NIOTSChannelOptions.waitForActivity, value: false)
            .tcpOptions(tcp)
            .channelInitializer { channel in
                guard !self.stopped else { return channel.eventLoop.makeFailedFuture(SSHTunnelError.stopped) }
                attempt.channel = channel
                return channel.eventLoop.makeCompletedFuture {
                    let sync = channel.pipeline.syncOperations
                    try sync.addHandler(NIOSSHHandler(role: .client(.init(userAuthDelegate: auth, serverAuthDelegate: hostKeys)),
                                                      allocator: channel.allocator, inboundChildChannelInitializer: nil))
                    try sync.addHandler(SessionHandler(ready: ready, traceID: "\(self.traceID) \(attemptID)"))
                }
            }
            .connect(host: config.host, port: config.port)
        let reached = LoopFlag()
        connected.whenComplete { result in
            switch result {
            case .success: reached.value = true
            case .failure(let error): ready.fail(error)
            }
        }
        return ready.futureResult
            .flatMap { connected }
            .always { _ in
                handshake.cancel()
                if self.connecting === attempt { self.connecting = nil }
            }
            .flatMapError { error in
                connected.whenSuccess { $0.close(promise: nil) }
                return self.loop.makeFailedFuture(Self.classify(error, hostKeys: hostKeys, auth: auth, reached: reached.value))
            }
            .map { channel in
                self.seenHostKey = hostKeys.seen
                return channel
            }
    }

    private static func classify(_ error: Error, hostKeys: HostKeyCheck, auth: KeyAuth, reached: Bool) -> SSHTunnelError {
        if let verdict = hostKeys.verdict { return verdict }
        if auth.rejected { return .notAuthorized }
        if let e = error as? SSHTunnelError {
            // The TCP connection never came up within the whole budget: the server is out of reach.
            if e == .timedOut, !reached { return .unreachable("超时") }
            return e
        }
        if let e = error as? NIOSSHError {
            if e.type == .keyExchangeNegotiationFailure { return .handshake("没有双方都支持的算法") }
            return .handshake(String(describing: e))
        }
        if let e = error as? ChannelError, case .connectTimeout = e { return .unreachable("超时") }
        if let e = error as? NWError { return .unreachable(e.debugDescription) }
        return .unreachable(String(describing: error))
    }

    // MARK: Forwarding

    /// Opens a direct-tcpip channel to the hub's address on the session. `inbound`: the loopback connection to join
    /// it to; nil: a check, closed as soon as it opened. A session that does not answer in time is closed (dead).
    private func open(on session: Channel, joining inbound: Channel?) -> EventLoopFuture<Void> {
        let target = SSHChannelType.DirectTCPIP(targetHost: config.targetHost, targetPort: config.targetPort,
                                                 originatorAddress: inbound?.remoteAddress ?? Self.loopbackOrigin)
        let result = loop.makePromise(of: Channel.self)
        let over = LoopFlag()
        result.futureResult.whenComplete { _ in over.value = true }
        let timeout = loop.scheduleTask(in: .milliseconds(Int64(timings.open * 1000))) {
            trace("\(self.traceID) channel open timed out after \(self.timings.open) seconds; closing its session")
            result.fail(SSHTunnelError.timedOut)
            session.close(promise: nil)
        }
        session.pipeline.handler(type: NIOSSHHandler.self).whenComplete { found in
            switch found {
            case .failure(let error):
                result.fail(error)
            case .success(let ssh):
                let opened = self.loop.makePromise(of: Channel.self)
                ssh.createChannel(opened, channelType: .directTCPIP(target)) { child, _ in
                    child.setOption(ChannelOptions.allowRemoteHalfClosure, value: true).flatMapThrowing {
                        try child.pipeline.syncOperations.addHandler(ChannelDataUnwrapper())
                        guard let inbound else { return }
                        let (ours, theirs) = Glue.pair()
                        try child.pipeline.syncOperations.addHandler(ours)
                        try inbound.pipeline.syncOperations.addHandler(theirs)
                    }
                }
                opened.futureResult.whenComplete { r in
                    switch r {
                    case .success(let child):
                        if over.value { child.close(promise: nil) } // too late: timed out
                        result.succeed(child)
                    case .failure(let error):
                        result.fail(error)
                    }
                }
            }
        }
        return result.futureResult
            .always { _ in timeout.cancel() }
            .map { child in if inbound == nil { child.close(promise: nil) } }
            .flatMapErrorThrowing { error in
                if let e = error as? NIOSSHError, e.type == .channelSetupRejected {
                    throw SSHTunnelError.targetRefused(Self.reason(e))
                }
                throw error as? SSHTunnelError ?? .handshake(String(describing: error))
            }
    }

    /// Why the server refused the channel: the reason code's name (RFC 4254 5.1) and the server's own words
    /// ("administratively prohibited: open failed", "connect failed: Connection refused").
    static func reason(_ e: NIOSSHError) -> String {
        let s = String(describing: e)
        guard let r = s.range(of: "Reason: ") else { return s }
        var text = String(s[r.upperBound...]).trimmingCharacters(in: CharacterSet(charactersIn: " )\""))
        var code: Int?
        if let space = text.firstIndex(of: " "), let c = Int(text[..<space]) {
            code = c
            text = String(text[text.index(after: space)...])
        }
        let name: String? = switch code {
        case 1: "administratively prohibited"
        case 2: "connect failed"
        case 3: "unknown channel type"
        case 4: "resource shortage"
        default: nil
        }
        guard let name, !text.hasPrefix(name) else { return text }
        return text.isEmpty ? name : name + ": " + text
    }

    /// A flag set and read on the tunnel's event loop only.
    private final class LoopFlag: @unchecked Sendable {
        var value = false
    }

    /// Owned by `loop`, including the channel initializer and cancellation. Keeps the pre-authentication channel
    /// reachable until the dial finishes, so `stop` also stops an unfinished TCP connection or SSH handshake.
    private final class DialAttempt: @unchecked Sendable {
        let ready: EventLoopPromise<Void>
        var channel: Channel?

        init(on loop: EventLoop) { ready = loop.makePromise(of: Void.self) }
    }

    private static let loopbackOrigin = try! SocketAddress(ipAddress: "127.0.0.1", port: 0)

    private func listen(_ preferred: Int) -> EventLoopFuture<Int> {
        if let listener, listener.isActive { return loop.makeSucceededFuture(localPort) }
        @Sendable func bind(_ port: Int) -> EventLoopFuture<Channel> {
            NIOTSListenerBootstrap(group: loop)
                .childChannelOption(ChannelOptions.allowRemoteHalfClosure, value: true)
                .childChannelInitializer { inbound in
                    // The connection is read only once this is done: nothing it sends is lost while the channel opens.
                    self.connection().flatMap { self.open(on: $0, joining: inbound) }
                }
                .bind(host: "127.0.0.1", port: port)
        }
        let bound = preferred == 0 ? bind(0) : bind(preferred).flatMapError { _ in bind(0) }
        return bound.flatMapThrowing { channel in
            if self.stopped {
                channel.close(promise: nil)
                throw SSHTunnelError.stopped
            }
            self.listener = channel
            let p = channel.localAddress?.port ?? 0
            self.port.withLock { $0 = p }
            return p
        }
    }
}
