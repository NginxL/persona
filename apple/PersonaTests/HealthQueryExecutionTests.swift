import Foundation
import HealthKit
import Testing
@testable import Persona

@MainActor struct HealthQueryExecutionTests {
    nonisolated private final class Probe: @unchecked Sendable {
        private let lock = NSLock()
        private var gate: SingleResume<Int>?
        private var executed: HKQuery?
        private var stopped: HKQuery?

        var didExecute: Bool { lock.withLock { executed != nil } }
        var stoppedTheExecutedQuery: Bool { lock.withLock { executed != nil && stopped === executed } }

        func make(_ gate: SingleResume<Int>) -> HKQuery {
            lock.withLock { self.gate = gate }
            return HKSampleQuery(sampleType: HKObjectType.categoryType(forIdentifier: .sleepAnalysis)!,
                                 predicate: nil, limit: 1, sortDescriptors: nil) { @Sendable _, _, _ in }
        }

        func execution() -> HealthQueryExecution<Int> {
            HealthQueryExecution(execute: { query in self.lock.withLock { self.executed = query } },
                                 stop: { query in self.lock.withLock { self.stopped = query } })
        }

        func answer(_ result: Result<Int, any Error>) { lock.withLock { gate }?.resume(result) }
    }

    private func waitForStart(_ probe: Probe) async {
        let deadline = Date.now.addingTimeInterval(5)
        while !probe.didExecute, Date.now < deadline { await Task.yield() }
        #expect(probe.didExecute)
    }

    @Test func cancellationStopsTheQueryAndFinishesWithoutAHealthKitCallback() async {
        let probe = Probe()
        let execution = probe.execution()
        let work = Task { try await execution.result { probe.make($0) } }
        await waitForStart(probe)
        work.cancel()
        switch await work.result {
        case .failure(let error): #expect(error is CancellationError)
        case .success: Issue.record("cancelled query succeeded")
        }
        #expect(probe.stoppedTheExecutedQuery)
        probe.answer(.success(42)) // A late callback cannot resume the cancelled continuation again.
        probe.answer(.failure(CancellationError()))
    }

    @Test func cancellationDuringRegistrationNeverExecutesTheQuery() async {
        let probe = Probe()
        let execution = probe.execution()
        let work = Task {
            try await execution.result { gate in
                withUnsafeCurrentTask { $0?.cancel() }
                return probe.make(gate)
            }
        }
        switch await work.result {
        case .failure(let error): #expect(error is CancellationError)
        case .success: Issue.record("cancelled registration succeeded")
        }
        #expect(!probe.didExecute)
    }

    @Test func alreadyCancelledQueriesNeverRegister() async {
        let probe = Probe()
        let execution = probe.execution()
        let work = Task {
            withUnsafeCurrentTask { $0?.cancel() }
            return try await execution.result { gate in
                Issue.record("query registered after cancellation")
                return probe.make(gate)
            }
        }
        switch await work.result {
        case .failure(let error): #expect(error is CancellationError)
        case .success: Issue.record("already cancelled query succeeded")
        }
        #expect(!probe.didExecute)
    }

    @Test(arguments: [false, true]) func aNormalCallbackStillReturnsItsResult(fails: Bool) async {
        let probe = Probe()
        let execution = probe.execution()
        let work = Task { try await execution.result { probe.make($0) } }
        await waitForStart(probe)
        probe.answer(fails ? .failure(CancellationError()) : .success(42))
        switch await work.result {
        case .failure(let error): #expect(fails && error is CancellationError)
        case .success(let value): #expect(!fails && value == 42)
        }
        #expect(!probe.stoppedTheExecutedQuery)
    }
}
