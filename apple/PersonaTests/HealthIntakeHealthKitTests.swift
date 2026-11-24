import Foundation
import HealthKit
import Testing
@testable import Persona

/// Opt-in integration check, run only on the task's disposable simulator after granting the system write sheet.
private actor IntakeHealthKitProbe {
    private let health = HKHealthStore()
    func count(_ id: String, kind: HealthIntake.Kind) async throws -> Int {
        try await withCheckedThrowingContinuation { continuation in
            let predicate = HKQuery.predicateForObjects(withMetadataKey: HKMetadataKeySyncIdentifier, allowedValues: [id])
            let query = HKSampleQuery(sampleType: HealthIntakeHealthStore.type(kind), predicate: predicate,
                                      limit: HKObjectQueryNoLimit, sortDescriptors: nil) { @Sendable _, samples, error in
                if let error { continuation.resume(throwing: error) }
                else { continuation.resume(returning: samples?.count ?? 0) }
            }
            health.execute(query)
        }
    }
}

struct HealthIntakeHealthKitTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["PERSONA_HEALTH_LIVE"] == "1"))
    func sameVersionReplayAndRepeatedDeletionUseTheRealHealthKitStore() async throws {
        let writer = HealthIntakeHealthStore()
        let probe = IntakeHealthKitProbe()
        try await writer.authorize()
        #expect(await writer.deniedKinds().isEmpty)
        for kind in [HealthIntake.Kind.water, .protein] {
            let id = UUID().uuidString.lowercased()
            var entry = HealthIntake.Entry(id: id, kind: kind, amount: kind == .water ? 321 : 12.3,
                                          at: Date.now.formatted(.iso8601), note: "", seq: 1,
                                          createdAt: Date.now.formatted(.iso8601))
            try await writer.apply(entry)
            #expect(try await probe.count(id, kind: kind) == 1)
            try await writer.apply(entry)
            #expect(try await probe.count(id, kind: kind) == 1, "same identifier and version must stay one sample")
            entry.deletedAt = Date.now.formatted(.iso8601)
            try await writer.apply(entry)
            #expect(try await probe.count(id, kind: kind) == 0)
            try await writer.apply(entry)
            #expect(try await probe.count(id, kind: kind) == 0, "deleting a missing identifier is a successful no-op")
        }
    }
}
