import Foundation
import NIOCore
import NIOSSH
import NIOTransportServices

/// Offers this device's key once; the server turning it down ends the login (no passwords, no prompts).
final class KeyAuth: NIOSSHClientUserAuthenticationDelegate, @unchecked Sendable {
    // Touched on the connection's event loop only.
    private let user: String
    private let key: NIOSSHPrivateKey
    private var offered = false
    private(set) var rejected = false

    init(user: String, key: NIOSSHPrivateKey) {
        self.user = user
        self.key = key
    }

    func nextAuthenticationType(availableMethods: NIOSSHAvailableUserAuthenticationMethods,
                                nextChallengePromise: EventLoopPromise<NIOSSHUserAuthenticationOffer?>) {
        guard availableMethods.contains(.publicKey), !offered else {
            rejected = true
            nextChallengePromise.fail(SSHTunnelError.notAuthorized)
            return
        }
        offered = true
        nextChallengePromise.succeed(NIOSSHUserAuthenticationOffer(username: user, serviceName: "",
                                                                   offer: .privateKey(.init(privateKey: key))))
    }
}

/// Takes the server's key only when it is the pinned one. Not pinned yet: refused too, with the fingerprint, for the
/// user to confirm (docs/operations/network.md「Connecting to hub through SSH」).
final class HostKeyCheck: NIOSSHClientServerAuthenticationDelegate, @unchecked Sendable {
    private let pinned: String?
    // Set on the connection's event loop, read once the connection attempt is over.
    private(set) var seen: (fingerprint: String, algorithm: String)?
    private(set) var verdict: SSHTunnelError?

    init(pinned: String?) {
        self.pinned = pinned
    }

    func validateHostKey(hostKey: NIOSSHPublicKey, validationCompletePromise: EventLoopPromise<Void>) {
        let key = SSHFingerprint.of(hostKey)
        seen = key
        if let pinned, !pinned.isEmpty {
            if pinned == key.fingerprint {
                validationCompletePromise.succeed(())
                return
            }
            verdict = .hostKeyChanged(expected: pinned, actual: key.fingerprint)
        } else {
            verdict = .hostKeyUnknown(fingerprint: key.fingerprint, algorithm: key.algorithm)
        }
        validationCompletePromise.fail(verdict!)
    }
}

/// Completes `ready` once the server accepted the login, fails it with the first error, or when the connection ends
/// before that. After login: a connection the network says is no longer usable is closed (the next request dials
/// again instead of waiting on a dead socket).
final class SessionHandler: ChannelInboundHandler {
    typealias InboundIn = Any
    private let ready: EventLoopPromise<Void>
    private let traceID: String?
    private var error: Error?

    init(ready: EventLoopPromise<Void>, traceID: String?) {
        self.ready = ready
        self.traceID = traceID
    }

    func userInboundEventTriggered(context: ChannelHandlerContext, event: Any) {
        if event is UserAuthSuccessEvent {
            ready.succeed(())
        } else if let v = event as? NIOTSNetworkEvents.ViabilityUpdate {
            trace("\(traceID) session viability \(v.isViable)")
            if !v.isViable { context.close(promise: nil) }
        }
        context.fireUserInboundEventTriggered(event)
    }

    func errorCaught(context: ChannelHandlerContext, error: Error) {
        if self.error == nil { self.error = error }
        trace("\(traceID) session error \(String(reflecting: error))")
        ready.fail(error)
        context.close(promise: nil)
    }

    func channelInactive(context: ChannelHandlerContext) {
        trace("\(traceID) session inactive \(error.map { String(reflecting: $0) } ?? "without an error")")
        ready.fail(error ?? SSHTunnelError.handshake("服务器断开了连接"))
        context.fireChannelInactive()
    }
}

/// The forwarded connection's SSH side: channel data in and out as plain bytes.
final class ChannelDataUnwrapper: ChannelDuplexHandler {
    typealias InboundIn = SSHChannelData
    typealias InboundOut = ByteBuffer
    typealias OutboundIn = ByteBuffer
    typealias OutboundOut = SSHChannelData

    func channelRead(context: ChannelHandlerContext, data: NIOAny) {
        let data = unwrapInboundIn(data)
        // Only the channel's own stream: stderr-style extended data has no place in a TCP forward.
        guard case .channel = data.type, case .byteBuffer(let buffer) = data.data else { return }
        context.fireChannelRead(wrapInboundOut(buffer))
    }

    func write(context: ChannelHandlerContext, data: NIOAny, promise: EventLoopPromise<Void>?) {
        let buffer = unwrapOutboundIn(data)
        context.write(wrapOutboundOut(SSHChannelData(type: .channel, data: .byteBuffer(buffer))), promise: promise)
    }
}

/// Joins two channels on one event loop: what one reads the other writes, a read waits while the other side cannot
/// take more (back pressure), an end on one side ends the other.
final class Glue: ChannelDuplexHandler {
    typealias InboundIn = NIOAny
    typealias OutboundIn = NIOAny
    typealias OutboundOut = NIOAny

    private var partner: Glue?
    private var context: ChannelHandlerContext?
    private var pendingRead = false

    private init() {}

    static func pair() -> (Glue, Glue) {
        let a = Glue(), b = Glue()
        a.partner = b
        b.partner = a
        return (a, b)
    }

    private func partnerWrite(_ data: NIOAny) { context?.write(data, promise: nil) }
    private func partnerFlush() { context?.flush() }
    private func partnerWriteEOF() { context?.close(mode: .output, promise: nil) }
    private func partnerClose() { context?.close(promise: nil) }
    private var partnerWritable: Bool { context?.channel.isWritable ?? false }

    private func partnerBecameWritable() {
        if pendingRead {
            pendingRead = false
            context?.read()
        }
    }

    func handlerAdded(context: ChannelHandlerContext) {
        self.context = context
        if context.channel.isWritable { partner?.partnerBecameWritable() }
    }

    func handlerRemoved(context: ChannelHandlerContext) {
        self.context = nil
        partner = nil
    }

    func channelRead(context: ChannelHandlerContext, data: NIOAny) { partner?.partnerWrite(data) }
    func channelReadComplete(context: ChannelHandlerContext) { partner?.partnerFlush() }
    func channelInactive(context: ChannelHandlerContext) { partner?.partnerClose() }
    func errorCaught(context: ChannelHandlerContext, error: Error) { partner?.partnerClose() }

    func userInboundEventTriggered(context: ChannelHandlerContext, event: Any) {
        if let e = event as? ChannelEvent, case .inputClosed = e { partner?.partnerWriteEOF() }
        context.fireUserInboundEventTriggered(event)
    }

    func channelWritabilityChanged(context: ChannelHandlerContext) {
        if context.channel.isWritable { partner?.partnerBecameWritable() }
    }

    func read(context: ChannelHandlerContext) {
        if let partner, partner.partnerWritable {
            context.read()
        } else {
            pendingRead = true
        }
    }
}
