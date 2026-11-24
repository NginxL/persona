import Foundation
import Network
import PersonaSSH
import PersonaWire
import Synchronization
import Testing
@testable import Persona

/// A count the loopback server's callback, its tasks and the test share: a reference, since a captured local `Mutex` is
/// a 'sending' data race to Xcode 26's checker (#13374).
private final class Count: Sendable {
    private let value = Mutex(0)
    func withLock<R: Sendable>(_ body: (inout Int) -> R) -> R { value.withLock { body(&$0) } }
}

@MainActor
struct SSHStartCancellationTests {
    private let hub = URL(string: "http://ssh-cancellation.example.test")!

    @Test func closingDuringLoginLetsTheNextForegroundAttemptStart() async throws {
        let accepted = Count(), closed = Count()
        let server = try LoopbackServer { connection in
            accepted.withLock { $0 += 1 }
            Task {
                while await Wire.readSome(connection) != nil {}
                closed.withLock { $0 += 1 }
            }
        }
        try await server.start()
        defer { server.stop() }
        let identity = SSHIdentity.generate()
        let tunnels = SSHTunnels(identity: { identity })
        defer { tunnels.close("hub") }
        let route = SSHRoute(host: "127.0.0.1", port: server.port, user: "test", hostKey: "SHA256:test")
        let oldOwner = UUID(), newOwner = UUID()
        var firstFinished = false, secondFinished = false
        let first = Task {
            defer { firstFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub, owner: oldOwner)
        }
        try await until { accepted.withLock { $0 } == 1 }
        tunnels.close("hub", forget: false)
        let second = Task {
            defer { secondFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub, owner: newOwner)
        }
        try await until { firstFinished && accepted.withLock { $0 } == 2 && closed.withLock { $0 } == 1 }
        // A route race's late cleanup belongs only to the old attempt.
        tunnels.close("hub", openedBy: oldOwner)
        #expect(tunnels.states["hub"] == .starting)
        #expect(!secondFinished)
        tunnels.close("hub", openedBy: newOwner)
        try await until { secondFinished && closed.withLock { $0 } == 2 }
        if case .success = await first.result { Issue.record("closed login succeeded") }
        if case .success = await second.result { Issue.record("closed replacement succeeded") }
        #expect(tunnels.states["hub"] == nil)
        #expect(!tunnels.routed.contains("hub"))
    }

    @Test func cancellingOneWaiterKeepsTheSharedStartForAnotherCaller() async throws {
        let relay = PendingRelays()
        let tunnels = manager(relay)
        defer { tunnels.close("hub"); relay.finishAll() }
        let route = SSHRoute(host: "ssh.example.test", user: "test", hostKey: "SHA256:test", viaInAppTunnel: true)
        var cancelledFinished = false, sharedFinished = false, sharedEntered = false
        let cancelled = Task {
            defer { cancelledFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub)
        }
        try await until { relay.calls.count == 1 }
        let shared = Task {
            sharedEntered = true
            defer { sharedFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub)
        }
        try await until { sharedEntered }
        cancelled.cancel()
        try await until { cancelledFinished }
        if case .failure(let error) = await cancelled.result { #expect(error is CancellationError) }
        else { Issue.record("cancelled waiter succeeded") }
        relay.finish(0, with: .failure(RelayFailure()))
        try await until { sharedFinished }
        if case .failure(let error) = await shared.result { #expect(error is RelayFailure) }
        else { Issue.record("shared relay unexpectedly succeeded") }
        #expect(relay.calls.count == 1)
    }

    @Test func anOldRelayCannotClearTheReplacementOrReleaseItsLease() async throws {
        let relay = PendingRelays()
        let tunnels = manager(relay)
        defer { tunnels.close("hub"); relay.finishAll() }
        let route = SSHRoute(host: "ssh.example.test", user: "test", hostKey: "SHA256:test", viaInAppTunnel: true)
        let oldOwner = UUID(), newOwner = UUID()
        var firstFinished = false, secondFinished = false, joinedFinished = false, joinedEntered = false
        let first = Task {
            defer { firstFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub, owner: oldOwner)
        }
        try await until { relay.calls.count == 1 }
        tunnels.close("hub", openedBy: oldOwner)
        let second = Task {
            defer { secondFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub, owner: newOwner)
        }
        try await until { relay.calls.count == 2 }
        let oldLease = relay.calls[0].id, newLease = relay.calls[1].id
        #expect(oldLease != newLease)
        // The old relay did not observe Task.cancel and finished after the new one started. Its result must not
        // start an SSH connection, replace state, release the new lease, or remove the new pending task.
        relay.finish(0, with: .success(9))
        try await until { firstFinished }
        if case .failure(let error) = await first.result { #expect(error is CancellationError) }
        else { Issue.record("retired relay was accepted") }
        #expect(relay.released.contains(oldLease))
        #expect(!relay.released.contains(newLease))
        #expect(tunnels.states["hub"] == .starting)
        tunnels.close("hub", openedBy: oldOwner)
        let joined = Task {
            joinedEntered = true
            defer { joinedFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub)
        }
        // Both calls must finish from this one relay completion. A stale defer deleting the replacement's entry
        // would start a third relay and leave `joined` waiting, which the bounded wait catches.
        try await until { joinedEntered }
        relay.finish(1, with: .failure(RelayFailure()))
        try await until { secondFinished && joinedFinished }
        _ = await second.result
        _ = await joined.result
        #expect(relay.calls.count == 2)
        #expect(relay.released.contains(newLease))
        #expect(!tunnels.routed.contains("hub"))
    }

    @Test(arguments: [false, true])
    func recoveryOwnsThePendingStartItReturns(previouslyOwned: Bool) async throws {
        let relay = PendingRelays()
        let tunnels = manager(relay)
        defer { tunnels.close("hub"); relay.finishAll() }
        let route = SSHRoute(host: "ssh.example.test", user: "test", hostKey: "SHA256:test", viaInAppTunnel: true)
        let oldOwner = previouslyOwned ? UUID() : nil
        let newOwner = UUID()
        var firstFinished = false, replacementFinished = false
        let first = Task {
            defer { firstFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub, owner: oldOwner)
        }
        try await until { relay.calls.count == 1 }
        // The new race may need to close its SSH attempt if the direct route wins. Joining an unowned or differently
        // owned start would make that close a no-op and let the obsolete start route traffic after the winner returned.
        let replacement = Task {
            defer { replacementFinished = true }
            return try await tunnels.open("hub", route: route, hub: hub, owner: newOwner)
        }
        try await until { relay.calls.count == 2 }
        tunnels.close("hub", openedBy: newOwner)
        relay.finish(0, with: .success(9))
        relay.finish(1, with: .success(9))
        try await until { firstFinished && replacementFinished }
        if case .success = await first.result { Issue.record("retired pending route succeeded") }
        if case .success = await replacement.result { Issue.record("losing recovery route succeeded") }
        #expect(tunnels.states["hub"] == nil)
        #expect(!tunnels.routed.contains("hub"))
        #expect(relay.calls.allSatisfy { relay.released.contains($0.id) })
    }

    private func manager(_ relay: PendingRelays) -> SSHTunnels {
        let identity = SSHIdentity.generate()
        let tunnels = SSHTunnels(identity: { identity })
        tunnels.relayForTests = { id, _, _ in try await relay.open(id) }
        tunnels.releaseRelayForTests = { relay.released.append($0) }
        return tunnels
    }

    private func until(_ condition: () -> Bool) async throws {
        let deadline = ContinuousClock.now + .seconds(2)
        while !condition() {
            try #require(ContinuousClock.now < deadline, "the foreground attempt waited for a retired start")
            try await Task.sleep(for: .milliseconds(10))
        }
    }

    private struct RelayFailure: Error {}

    @MainActor private final class PendingRelays {
        struct Call {
            let id: String
            var continuation: CheckedContinuation<Int, Error>?
        }
        var calls: [Call] = []
        var released: [String] = []

        func open(_ id: String) async throws -> Int {
            try await withCheckedThrowingContinuation { calls.append(Call(id: id, continuation: $0)) }
        }

        func finish(_ index: Int, with result: Result<Int, Error>) {
            let continuation = calls[index].continuation
            calls[index].continuation = nil
            continuation?.resume(with: result)
        }

        func finishAll() {
            for index in calls.indices { finish(index, with: .failure(CancellationError())) }
        }
    }
}
