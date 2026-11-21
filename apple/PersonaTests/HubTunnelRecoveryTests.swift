import Foundation
import Testing
@testable import Persona

/// No wall-clock sleeps: each probe stays pending until the test answers it, or the recovery cancels its waiter.
@MainActor
struct HubTunnelRecoveryTests {
    private let candidate = HubRoute.Candidate(config: ServerConfig(url: URL(string: "https://hub.example.com")!, token: "t"))

    @Test func aRestoredSSHWinsBeforeTheDirectBlackholeFinishes() async {
        let direct = RecoveryGate<HubRoute.Candidate?>()
        let ssh = RecoveryGate<Bool>()
        var closes = 0
        let recovery = HubTunnelRecovery { closes += 1 }
        let pick = Task { await recovery.pick(direct: { await direct.wait() ?? nil }, tunnel: { await ssh.wait() ?? false }) }
        await direct.started()
        await ssh.started()
        ssh.answer(true)

        guard case .tunnel = await pick.value else { Issue.record("The restored tunnel did not win"); return }
        #expect(direct.ended && direct.wasCancelled)
        #expect(ssh.ended && !ssh.wasCancelled)
        #expect(closes == 0)
        recovery.keepTunnel()
        recovery.cancel()
        #expect(closes == 0)
    }

    @Test func anAnsweringDirectAddressClosesTheSSHLoserWithoutWaitingForItsTimeout() async {
        let direct = RecoveryGate<HubRoute.Candidate?>()
        let ssh = RecoveryGate<Bool>()
        var closes = 0
        let recovery = HubTunnelRecovery { closes += 1 }
        let pick = Task { await recovery.pick(direct: { await direct.wait() ?? nil }, tunnel: { await ssh.wait() ?? false }) }
        await direct.started()
        await ssh.started()
        direct.answer(candidate)

        guard case .direct(let chosen) = await pick.value else { Issue.record("The direct address did not win"); return }
        #expect(chosen == candidate)
        #expect(ssh.ended && ssh.wasCancelled)
        #expect(closes == 1)
        // A late answer cannot pick another result or run the owner's close a second time.
        ssh.answer(true)
        recovery.cancel()
        #expect(closes == 1)
    }

    @Test func aFailedSSHStillWaitsForTheDirectAnswer() async {
        let direct = RecoveryGate<HubRoute.Candidate?>()
        let ssh = RecoveryGate<Bool>()
        let recovery = HubTunnelRecovery {}
        let pick = Task { await recovery.pick(direct: { await direct.wait() ?? nil }, tunnel: { await ssh.wait() ?? false }) }
        await direct.started()
        await ssh.started()
        ssh.answer(false)
        await ssh.finished()
        #expect(!direct.ended)
        direct.answer(candidate)

        guard case .direct(let chosen) = await pick.value else { Issue.record("A failed SSH hid the direct answer"); return }
        #expect(chosen == candidate)
    }

    @Test func aFailedDirectProbeStillWaitsForTheSSH() async {
        let direct = RecoveryGate<HubRoute.Candidate?>()
        let ssh = RecoveryGate<Bool>()
        let recovery = HubTunnelRecovery {}
        let pick = Task { await recovery.pick(direct: { await direct.wait() ?? nil }, tunnel: { await ssh.wait() ?? false }) }
        await direct.started()
        await ssh.started()
        direct.answer(nil)
        await direct.finished()
        #expect(!ssh.ended)
        ssh.answer(true)

        guard case .tunnel = await pick.value else { Issue.record("A failed direct probe hid the restored SSH"); return }
        recovery.keepTunnel()
    }

    @Test func twoFailuresAllowTheRemainingFallbacks() async {
        var closes = 0
        let recovery = HubTunnelRecovery { closes += 1 }
        let result = await recovery.pick(direct: { nil }, tunnel: { false })
        guard case .unavailable = result else { Issue.record("Two failures must allow fallback"); return }
        #expect(closes == 1)
    }

    @Test func backgroundCancellationEndsBothWaitersAndClosesTheOwnedSSH() async {
        let direct = RecoveryGate<HubRoute.Candidate?>()
        let ssh = RecoveryGate<Bool>()
        var closes = 0
        let recovery = HubTunnelRecovery { closes += 1 }
        let pick = Task { await recovery.pick(direct: { await direct.wait() ?? nil }, tunnel: { await ssh.wait() ?? false }) }
        await direct.started()
        await ssh.started()
        pick.cancel()

        guard case .cancelled = await pick.value else { Issue.record("Cancellation chose a route"); return }
        #expect(direct.ended && ssh.ended)
        #expect(direct.wasCancelled && ssh.wasCancelled)
        #expect(closes == 1)
    }

    @Test func aNewRecoveryCanCloseItsPredecessorBeforeStartingAnotherSSH() async {
        let direct = RecoveryGate<HubRoute.Candidate?>()
        let ssh = RecoveryGate<Bool>()
        var closes = 0
        let recovery = HubTunnelRecovery { closes += 1; ssh.answer(false) }
        let pick = Task { await recovery.pick(direct: { await direct.wait() ?? nil }, tunnel: { await ssh.wait() ?? false }) }
        await direct.started()
        await ssh.started()
        recovery.cancel()
        #expect(closes == 1)

        guard case .cancelled = await pick.value else { Issue.record("A superseded recovery chose a route"); return }
        #expect(direct.ended && ssh.ended)
        #expect(direct.wasCancelled)
    }

    @Test func aRecoveryCancelledBeforeStartingOpensNothing() async {
        var calls = 0
        let recovery = HubTunnelRecovery {}
        recovery.cancel()
        let result = await recovery.pick(direct: { calls += 1; return nil }, tunnel: { calls += 1; return false })
        guard case .cancelled = result else { Issue.record("A cancelled recovery started"); return }
        #expect(calls == 0)
    }
}

@MainActor private final class RecoveryGate<Value: Sendable> {
    private let input = AsyncStream<Value>.makeStream()
    private let beginning = AsyncStream<Void>.makeStream()
    private let ending = AsyncStream<Void>.makeStream()
    private(set) var ended = false
    private(set) var wasCancelled = false

    func wait() async -> Value? {
        beginning.continuation.yield(())
        beginning.continuation.finish()
        defer {
            wasCancelled = Task.isCancelled
            ended = true
            ending.continuation.yield(())
            ending.continuation.finish()
        }
        var iterator = input.stream.makeAsyncIterator()
        return await iterator.next()
    }

    func answer(_ value: Value) {
        input.continuation.yield(value)
        input.continuation.finish()
    }

    func started() async {
        for await _ in beginning.stream { return }
    }

    func finished() async {
        for await _ in ending.stream { return }
    }
}
