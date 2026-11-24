import Foundation
import HealthKit

/// Couples a one-shot HealthKit query to its task. Stopping a query need not deliver a result callback, so cancellation
/// also resumes the waiter. The lock orders execute before stop even if cancellation races query registration.
nonisolated final class HealthQueryExecution<Value: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private let execute: @Sendable (HKQuery) -> Void
    private let stop: @Sendable (HKQuery) -> Void
    private var cancelled = false
    private var pending: (HKQuery, SingleResume<Value>)?

    init(execute: @escaping @Sendable (HKQuery) -> Void, stop: @escaping @Sendable (HKQuery) -> Void) {
        self.execute = execute
        self.stop = stop
    }

    convenience init(store: HKHealthStore) {
        self.init(execute: { store.execute($0) }, stop: { store.stop($0) })
    }

    @concurrent func result(makeQuery: @Sendable (SingleResume<Value>) -> HKQuery) async throws -> Value {
        defer { lock.withLock { pending = nil } }
        let value: Value = try await withTaskCancellationHandler {
            try Task.checkCancellation()
            return try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Value, any Error>) in
                let gate = SingleResume(continuation)
                let query = makeQuery(gate)
                lock.withLock {
                    if cancelled {
                        gate.fail(CancellationError())
                    } else {
                        pending = (query, gate)
                        execute(query)
                    }
                }
            }
        } onCancel: {
            let pending = self.lock.withLock {
                self.cancelled = true
                let pending = self.pending
                self.pending = nil
                return pending
            }
            if let (query, gate) = pending {
                self.stop(query)
                gate.fail(CancellationError())
            }
        }
        try Task.checkCancellation()
        return value
    }
}
