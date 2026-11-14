#if os(iOS)
import Foundation
import HealthKit
import Observation
import os

/// Consent and the HealthKit cursor are committed together. Failed uploads leave the cursor in place for retry.
/// A report the hub rejects as invalid is dropped instead: the same data gets the same answer.
@Observable
final class WorkoutReports {
    static let shared = WorkoutReports()
    private static let log = Logger(subsystem: "persona", category: "workout-reports")
    private let health = HKHealthStore()
    private let file: URL
    private var state: State
    private(set) var busy = false
    private(set) var error: String?
    var enabled: Bool { state.enabledAt != nil }

    nonisolated struct State: Codable {
        var enabledAt: Date?
        var group: String = ""
        var anchor: Data?
        var pending: [Report] = []
    }
    typealias HeartRate = WorkoutHeartRate
    nonisolated struct Report: Codable {
        let id: String
        let title: String
        let activity: String
        let source: String
        let start: Date
        let end: Date
        let enabled_at: Date
        let duration: Double
        let distance: Double?
        let energy: Double?
        let heart_rate: [HeartRate]
        // Since #2526's second step; a report queued by an older version has none of them.
        var elevation_gain: Double? = nil
        var heart_rate_avg: Double? = nil
        var heart_rate_max: Double? = nil
        var splits: [WorkoutSplit]? = nil
    }

    init(folder: URL? = nil) {
        let root = folder ?? URL.applicationSupportDirectory.appending(path: "WorkoutReports")
        file = root.appending(path: "state.json")
        do {
            state = FileManager.default.fileExists(atPath: file.path)
                ? try JSON.decoder.decode(State.self, from: Data(contentsOf: file)) : State()
        } catch {
            state = State()
            self.error = String(localized: "Couldn't read workout report settings. Turn the feature on again to start fresh.")
        }
    }

    private func save(_ next: State) throws {
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try JSON.encoder.encode(next).write(to: file, options: [.atomic, .completeFileProtection])
        state = next
    }

    func setEnabled(_ enabled: Bool, model: AppModel) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            if enabled {
                guard let hub = model.hub, !hub.isDemo, HKHealthStore.isHealthDataAvailable() else { return }
                try await health.requestAuthorization(toShare: [], read: WorkoutReader.readTypes)
                try save(State(enabledAt: Date(), group: hub.group))
            } else {
                try save(State())
            }
            error = nil
        } catch { self.error = error.localizedDescription }
    }

    func refresh(model: AppModel) async {
        guard !busy, let since = state.enabledAt, let hub = model.hub,
              !hub.isDemo, hub.group == state.group else { return }
        busy = true
        defer { busy = false }
        do {
            // Upload a previous batch first: a network outage cannot grow an unbounded outbox.
            var skipped = try await upload(api: hub.api)
            await WorkoutReader.askForNewTypes() // allowed before splits were read: the distances too
            let anchor = try state.anchor.map {
                try NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: $0)
            } ?? nil
            let (workouts, nextAnchor) = try await read(since: since, anchor: anchor)
            var next = state
            for workout in workouts where workout.startDate >= since && workout.duration >= 600 && workout.endDate <= Date() && workout.endDate.timeIntervalSince(workout.startDate) <= 7 * 86400 {
                let rates = try await WorkoutReader.heartRates(workout)
                let rate = WorkoutReader.heartRate(workout)
                let splits = (try? await WorkoutReader.splits(workout)) ?? [] // no splits rather than no report
                next.pending.append(Report(
                    id: workout.uuid.uuidString, title: String(localized: "Workout report"),
                    activity: WorkoutReader.activityName(workout.workoutActivityType), source: workout.sourceRevision.source.name,
                    start: workout.startDate, end: workout.endDate, enabled_at: since, duration: workout.duration,
                    distance: WorkoutReader.distance(workout), energy: WorkoutReader.energy(workout), heart_rate: rates,
                    elevation_gain: WorkoutReader.elevationGain(workout), heart_rate_avg: rate?.average,
                    heart_rate_max: rate?.maximum, splits: splits.isEmpty ? nil : splits))
            }
            next.anchor = try nextAnchor.map { try NSKeyedArchiver.archivedData(withRootObject: $0, requiringSecureCoding: true) }
            try save(next)
            skipped += try await upload(api: hub.api)
            error = skipped == 0 ? nil
                : String(localized: "Some workouts were skipped because the hub couldn't accept their data. Later workouts are still reported.")
        } catch { self.error = error.localizedDescription }
    }

    /// Sends the outbox in order and returns how many reports the hub rejected. A rejected report is dropped: kept at
    /// the head, it would hold back every later workout. Anything else (offline, 401 / 403 / 404, 5xx) stops the batch
    /// with the report still queued, for the next foreground visit.
    func upload(api: APIClient) async throws -> Int {
        var skipped = 0
        while let report = state.pending.first {
            do {
                let _: Data = try await api.post("/api/v1/assistant/health/workouts", body: report)
            } catch let error as APIError where [400, 413, 422].contains(error.status) {
                Self.log.error("hub rejected a workout report (HTTP \(error.status)): \(error.message, privacy: .public)")
                skipped += 1
            }
            var next = state
            next.pending.removeFirst()
            try save(next)
        }
        return skipped
    }

    private func read(since: Date, anchor: HKQueryAnchor?) async throws -> ([HKWorkout], HKQueryAnchor?) {
        try await withCheckedThrowingContinuation { continuation in
            let predicate = HKQuery.predicateForSamples(withStart: since, end: nil, options: .strictStartDate)
            let query = HKAnchoredObjectQuery(type: HKObjectType.workoutType(), predicate: predicate, anchor: anchor, limit: 50) {
                _, samples, _, next, error in
                if let error { continuation.resume(throwing: error) }
                else { continuation.resume(returning: (samples as? [HKWorkout] ?? [], next)) }
            }
            health.execute(query)
        }
    }
}
#endif
