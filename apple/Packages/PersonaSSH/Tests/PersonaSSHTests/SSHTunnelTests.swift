import Foundation
import Testing
@testable import PersonaSSH

/// The tunnel against a real OpenSSH server (this machine's sshd, run as this user on a free port) and the repository's
/// mock hub: the cases docs/operations/network.md「Connecting to hub through SSH」 promises.
@Suite(.serialized, .timeLimit(.minutes(1)))
struct SSHTunnelTests {
    let hub: TestHub
    let me = SSHIdentity.generate()

    init() throws {
        hub = try TestHub()
    }

    /// A server that lets `me` forward to the hub's port only, as the app's authorized_keys line says.
    func server(permit port: Int? = nil, keys: [String]? = nil) throws -> TestSSHD {
        try TestSSHD(authorizedKeys: keys ?? [me.authorizedKeysLine(targetHost: "127.0.0.1", targetPort: port ?? hub.port, comment: "persona-test")])
    }

    func tunnel(_ sshd: TestSSHD, pinned: String?, identity: SSHIdentity? = nil, targetPort: Int? = nil,
                timings: SSHTunnelTimings = .fast) -> SSHTunnel {
        SSHTunnel(config: SSHTunnelConfig(host: "127.0.0.1", port: sshd.port, user: sshd.user, targetHost: "127.0.0.1",
                                          targetPort: targetPort ?? hub.port),
                  identity: identity ?? me, pinnedHostKey: pinned, timings: timings)
    }

    static func health(_ port: Int) async throws -> [String: Any] {
        var req = URLRequest(url: URL(string: "http://127.0.0.1:\(port)/api/v1/health")!, timeoutInterval: 5)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let (data, resp) = try await URLSession(configuration: .ephemeral).data(for: req)
        #expect((resp as? HTTPURLResponse)?.statusCode == 200)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    @Test func linesMatchOpenSSH() throws {
        let pub = FileManager.default.temporaryDirectory.appending(path: "persona-key-\(UUID().uuidString.prefix(6)).pub")
        defer { try? FileManager.default.removeItem(at: pub) }
        try me.publicKeyLine(comment: "persona-Alex's iPhone").write(to: pub, atomically: true, encoding: .utf8)
        let printed = try Shell.run("/usr/bin/ssh-keygen", ["-lf", pub.path])
        // "256 SHA256:… persona-Alex-s-iPhone (ED25519)"
        let parts = printed.split(separator: " ")
        #expect(String(parts[1]) == me.fingerprint)
        #expect(String(parts[2]) == "persona-Alex-s-iPhone")
        #expect(printed.hasSuffix("(ED25519)"))
        let line = me.authorizedKeysLine(targetHost: "127.0.0.1", targetPort: 18660, comment: "persona-x")
        #expect(line.hasPrefix("restrict,port-forwarding,permitopen=\"127.0.0.1:18660\" ssh-ed25519 AAAA"))
        #expect(me.authorizedKeysLine(targetHost: "::1", targetPort: 18660, comment: "x").contains("permitopen=\"[::1]:18660\""))
        // The key the keychain keeps comes back the same.
        #expect(try SSHIdentity(rawRepresentation: me.rawRepresentation).fingerprint == me.fingerprint)
    }

    @Test func forwardsRequestsToTheHub() async throws {
        let sshd = try server()
        let t = tunnel(sshd, pinned: sshd.fingerprint)
        trace("forwarding test begin: \(t.traceID)")
        defer { t.stop() }
        let port = try await t.start()
        #expect(port > 0 && port != hub.port)
        #expect(t.phase == .connected)
        #expect(try await Self.health(port)["ok"] as? Bool == true)
        // Many at once: one channel each over the one session.
        try await withThrowingTaskGroup(of: Bool.self) { group in
            for _ in 0..<20 { group.addTask { try await Self.health(port)["ok"] as? Bool == true } }
            for try await ok in group { #expect(ok) }
        }
        trace("forwarding health complete: \(t.traceID)")
        let log = sshd.log
        let accepted = log.components(separatedBy: "Accepted publickey").count - 1
        trace("forwarding session count \(accepted): \(t.traceID)")
        if accepted != 1 { sshd.traceFailure() }
        #expect(accepted == 1, "unexpected SSH sessions; server log: \(log)")
    }

    @Test func firstConnectionAsksForTheHostKey() async throws {
        let sshd = try server()
        let t = tunnel(sshd, pinned: nil)
        await #expect(throws: SSHTunnelError.hostKeyUnknown(fingerprint: sshd.fingerprint, algorithm: "ED25519")) {
            try await t.verify()
        }
        // Confirmed: pinned, it goes through, and says which key it saw.
        let key = try await tunnel(sshd, pinned: sshd.fingerprint).verify()
        #expect(key.fingerprint == sshd.fingerprint)
        #expect(key.algorithm == "ED25519")
    }

    @Test func changedHostKeyIsRefused() async throws {
        let sshd = try server()
        let other = SSHIdentity.generate().fingerprint
        let t = tunnel(sshd, pinned: other)
        defer { t.stop() }
        await #expect(throws: SSHTunnelError.hostKeyChanged(expected: other, actual: sshd.fingerprint)) { try await t.start() }
        #expect(t.phase == .failed(.hostKeyChanged(expected: other, actual: sshd.fingerprint)))
        // Not tried again on its own: the user has to look at it.
        await #expect(throws: SSHTunnelError.hostKeyChanged(expected: other, actual: sshd.fingerprint)) { try await t.start() }
        #expect(!sshd.log.contains("Accepted publickey"))
    }

    @Test func unknownKeyIsNotAuthorized() async throws {
        let sshd = try server()
        let stranger = SSHIdentity.generate()
        let t = tunnel(sshd, pinned: sshd.fingerprint, identity: stranger)
        defer { t.stop() }
        await #expect(throws: SSHTunnelError.notAuthorized) { try await t.start() }
    }

    @Test func closedTargetPortIsReported() async throws {
        let closed = try FreePort.pick()
        let sshd = try server(permit: closed)
        let t = tunnel(sshd, pinned: sshd.fingerprint, targetPort: closed)
        defer { t.stop() }
        do {
            try await t.start()
            Issue.record("started though nothing listens on the target")
        } catch SSHTunnelError.targetRefused(let why) {
            #expect(why.localizedCaseInsensitiveContains("refused") || why.contains("connect failed"), "\(why)")
        }
    }

    @Test func permitopenKeepsTheKeyToTheHubPort() async throws {
        let sshd = try server(permit: hub.port)
        let elsewhere = try FreePort.pick()
        let t = tunnel(sshd, pinned: sshd.fingerprint, targetPort: elsewhere)
        defer { t.stop() }
        do {
            try await t.start()
            Issue.record("forwarded outside permitopen")
        } catch SSHTunnelError.targetRefused(let why) {
            #expect(why.contains("prohibited"), "\(why)")
        }
    }

    @Test func unreachableServer() async throws {
        let nobody = try FreePort.pick()
        let t = SSHTunnel(config: SSHTunnelConfig(host: "127.0.0.1", port: nobody, user: "x"), identity: me, pinnedHostKey: nil,
                          timings: .fast)
        defer { t.stop() }
        do {
            try await t.start()
            Issue.record("connected to nothing")
        } catch SSHTunnelError.unreachable {
        }
    }

    @Test func reconnectsAfterTheSessionDrops() async throws {
        let sshd = try server()
        let t = tunnel(sshd, pinned: sshd.fingerprint)
        defer { t.stop() }
        let port = try await t.start()
        #expect(try await Self.health(port)["ok"] as? Bool == true)

        // The session drops (the network went): the next request dials again on its own.
        try sshd.dropSessions()
        try await Self.until { t.phase != .connected }
        #expect(try await Self.health(port)["ok"] as? Bool == true)
        #expect(sshd.log.components(separatedBy: "Accepted publickey").count - 1 == 2)

        // The server goes away: requests fail, with the reason, and come back once it is there again (after the backoff).
        sshd.stop()
        await #expect(throws: (any Error).self) { try await Self.health(port) }
        if case .failed(.unreachable) = t.phase {} else { Issue.record("phase \(t.phase)") }
        try sshd.start()
        try await Self.until(timeout: 10) { (try? await Self.health(port))?["ok"] as? Bool == true }
        #expect(t.phase == .connected)
        #expect(t.localPort == port) // the same address all along
    }

    @Test func streamsTheWebSocket() async throws {
        let sshd = try server()
        let t = tunnel(sshd, pinned: sshd.fingerprint)
        defer { t.stop() }
        let port = try await t.start()
        var req = URLRequest(url: URL(string: "ws://127.0.0.1:\(port)/api/v1/ws?features=sessions_delta")!)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let ws = URLSession(configuration: .ephemeral).webSocketTask(with: req)
        ws.resume()
        defer { ws.cancel(with: .goingAway, reason: nil) }
        var types: [String] = []
        let deadline = Date.now + 10
        while types.count < 5, Date.now < deadline {
            let message = try await ws.receive()
            let text: String = switch message {
            case .string(let s): s
            case .data(let d): String(decoding: d, as: UTF8.self)
            @unknown default: ""
            }
            let obj = try JSONSerialization.jsonObject(with: Data(text.utf8)) as? [String: Any]
            types.append(obj?["type"] as? String ?? "?")
        }
        #expect(types.first == "hello", "\(types)")
        #expect(types.count >= 5, "\(types)")
        // The app's side talks too: a ping goes up and its pong comes back on the same socket (whatever else the hub
        // sends meanwhile is skipped).
        try await ws.send(.string(#"{"type":"ping","id":"tunnel-1"}"#))
        var pong = false
        while !pong, Date.now < deadline + 10 {
            if case .string(let s) = try await ws.receive() { pong = s.contains("\"pong\"") && s.contains("tunnel-1") }
        }
        #expect(pong)
    }

    @Test func stopClosesTheListener() async throws {
        let sshd = try server()
        let t = tunnel(sshd, pinned: sshd.fingerprint)
        let port = try await t.start()
        t.stop()
        try await Self.until { !FreePort.isListening(port) && t.phase == .idle }
        #expect(t.phase == .idle)
    }

    /// Waits for `ok` (polling), failing the test after `timeout` seconds.
    static func until(timeout: TimeInterval = 5, _ ok: () async throws -> Bool) async throws {
        let deadline = Date.now + timeout
        while Date.now < deadline {
            if try await ok() { return }
            try await Task.sleep(for: .milliseconds(100))
        }
        Issue.record("timed out waiting")
    }
}

extension SSHTunnelTimings {
    /// Short timeouts and backoff for the tests.
    static var fast: SSHTunnelTimings {
        var t = SSHTunnelTimings()
        t.connect = 3
        t.handshake = 5
        t.open = 3
        t.backoffBase = 0.2
        t.backoffCap = 1
        return t
    }
}

