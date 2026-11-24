#if os(iOS)
import Foundation
import HealthKit
import Observation
import UIKit
import PersonaWire

nonisolated protocol HealthIntakeWriting: Sendable {
    func authorize() async throws
    func deniedKinds() async -> [HealthIntake.Kind]
    func apply(_ entry: HealthIntake.Entry) async throws
}

/// HealthKit and disk IO stay off the main actor. Only this source's samples can be deleted by HealthKit.
actor HealthIntakeHealthStore: HealthIntakeWriting {
    private lazy var store = HKHealthStore()

    nonisolated static func type(_ kind: HealthIntake.Kind) -> HKQuantityType {
        HKQuantityType(kind == .water ? .dietaryWater : .dietaryProtein)
    }

    func authorize() async throws {
        try await store.requestAuthorization(toShare: [Self.type(.water), Self.type(.protein)], read: [])
    }

    func deniedKinds() -> [HealthIntake.Kind] {
        [HealthIntake.Kind.water, .protein].filter { store.authorizationStatus(for: Self.type($0)) != .sharingAuthorized }
    }

    nonisolated static func sample(_ entry: HealthIntake.Entry) throws -> HKQuantitySample {
        guard let date = ISO8601DateFormatter().date(from: entry.at)
                ?? ISO8601DateFormatter.withFractions.date(from: entry.at),
              entry.amount.isFinite, entry.amount > 0 else { throw HealthIntakeSyncError.invalidEntry }
        return HKQuantitySample(type: type(entry.kind),
                                quantity: HKQuantity(unit: entry.kind == .water ? .literUnit(with: .milli) : .gram(),
                                                     doubleValue: entry.amount), start: date, end: date,
                                metadata: [HKMetadataKeySyncIdentifier: entry.id,
                                           HKMetadataKeySyncVersion: 1, HKMetadataKeyWasUserEntered: true])
    }

    func apply(_ entry: HealthIntake.Entry) async throws {
        let type = Self.type(entry.kind)
        guard store.authorizationStatus(for: type) == .sharingAuthorized else {
            throw HealthIntakeSyncError.permission
        }
        if entry.deletedAt != nil {
            let predicate = HKQuery.predicateForObjects(withMetadataKey: HKMetadataKeySyncIdentifier, allowedValues: [entry.id])
            // No matches is a successful no-op. Permission / database failures must keep the cursor in place.
            _ = try await store.deleteObjects(of: type, predicate: predicate)
        } else {
            try await store.save(Self.sample(entry))
        }
    }
}

private nonisolated extension ISO8601DateFormatter {
    static var withFractions: ISO8601DateFormatter {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }
}

nonisolated enum HealthIntakeSyncError: LocalizedError {
    case invalidEntry, invalidPage, permission
    var errorDescription: String? {
        switch self {
        case .invalidEntry: String(localized: "The intake record has an invalid amount or time.")
        case .invalidPage: String(localized: "Couldn't read the intake changes. Try again later.")
        case .permission: String(localized: "Apple Health hasn't allowed writing this intake type.")
        }
    }
}

/// One ordered change stream. A failed write or checkpoint stops at that entry; replay is idempotent.
actor HealthIntakeSyncEngine {
    nonisolated struct State: Codable, Sendable, Equatable {
        var enabled = false
        var group = ""
        var cursor: Int64? = nil
        var lastSynced: Date? = nil
        var lastError: String? = nil
        var hubID: String? = nil
    }
    private let writer: any HealthIntakeWriting
    private let file: URL?
    private var state = State()
    private var loaded = false
    private var generation = 0
    private var running = false
    private var pending = false
    private var activeGeneration = 0
    private var waiters: [CheckedContinuation<Void, Never>] = []
    private(set) var denied: [HealthIntake.Kind] = []

    init(writer: any HealthIntakeWriting = HealthIntakeHealthStore(), file: URL? = URL.applicationSupportDirectory
        .appending(path: "HealthIntakeSync/state.json")) {
        self.writer = writer
        self.file = file
    }

    func snapshot() -> State {
        if !loaded {
            loaded = true
            if let file, let data = try? Data(contentsOf: file) {
                state = (try? JSON.decoder.decode(State.self, from: data)) ?? State()
            }
        }
        return state
    }

    private func save(_ next: State) throws {
        if let file {
            let root = file.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            try JSON.encoder.encode(next).write(to: file, options: [.atomic, .completeFileProtection])
            for var url in [root, file] {
                var values = URLResourceValues()
                values.isExcludedFromBackup = true
                try url.setResourceValues(values)
            }
        }
        state = next
    }

    func setEnabled(_ enabled: Bool, group: String, hubID: String? = nil,
                    allowed: @Sendable () async -> Bool) async -> State {
        _ = snapshot()
        generation += 1
        let request = generation
        do {
            if enabled {
                guard await allowed(), request == generation else { return state }
                try await writer.authorize()
                guard await allowed(), request == generation else { return state }
                try save(State(enabled: true, group: group, cursor: 0, hubID: hubID))
            } else {
                var next = state
                next.enabled = false
                try save(next)
            }
            denied = await writer.deniedKinds()
        } catch { state.lastError = error.localizedDescription }
        return state
    }

    private func canContinue(_ run: Int, allowed: @Sendable () async -> Bool) async -> Bool {
        guard !Task.isCancelled, await allowed() else { return false }
        // Consent may have changed while the main actor checked the hub and protected-data state.
        return run == generation && state.enabled && !Task.isCancelled
    }

    func refresh(group: String, hubID: String? = nil, fetch: @Sendable (Int64) async throws -> HealthIntake.Changes,
                 allowed: @Sendable () async -> Bool) async -> State {
        _ = snapshot()
        guard await allowed(), state.enabled, group == state.group, hubID == state.hubID else { return state }
        if running {
            if activeGeneration != generation {
                await withCheckedContinuation { waiters.append($0) }
                return await refresh(group: group, hubID: hubID, fetch: fetch, allowed: allowed)
            }
            pending = true
            return state
        }
        running = true
        activeGeneration = generation
        defer {
            running = false
            let done = waiters
            waiters.removeAll()
            done.forEach { $0.resume() }
        }
        let run = generation
        repeat {
            pending = false
            do {
                denied = await writer.deniedKinds()
                var more = true
                while more {
                    guard await canContinue(run, allowed: allowed) else { return state }
                    let after = state.cursor ?? 0
                    let page = try await fetch(after)
                    var previous = after
                    for entry in page.changes {
                        guard entry.seq > previous else { throw HealthIntakeSyncError.invalidPage }
                        previous = entry.seq
                    }
                    guard page.next == previous, !page.hasMore || !page.changes.isEmpty else {
                        throw HealthIntakeSyncError.invalidPage
                    }
                    for entry in page.changes {
                        guard await canContinue(run, allowed: allowed) else { return state }
                        try await writer.apply(entry)
                        guard await canContinue(run, allowed: allowed) else { return state }
                        var next = state
                        next.cursor = entry.seq
                        next.lastError = nil
                        try save(next)
                    }
                    guard await canContinue(run, allowed: allowed) else { return state }
                    var next = state
                    next.cursor = page.next
                    next.lastError = nil
                    if !page.hasMore { next.lastSynced = .now }
                    try save(next)
                    more = page.hasMore
                }
            } catch {
                guard run == generation else { return state }
                state.lastError = error.localizedDescription
                try? save(state)
                denied = await writer.deniedKinds()
                return state
            }
        } while pending && run == generation
        return state
    }
}

/// The small observable projection used by Settings and Health's opt-in link.
@Observable final class HealthIntakeSync {
    static let shared = HealthIntakeSync()
    private let engine: HealthIntakeSyncEngine
    private(set) var state = HealthIntakeSyncEngine.State()
    private(set) var denied: [HealthIntake.Kind] = []
    private(set) var changing = false
    private var settingError: String?
    private var settingErrorGroup = ""
    private var settingErrorHubID: String?

    init(engine: HealthIntakeSyncEngine = HealthIntakeSyncEngine()) { self.engine = engine }

    nonisolated static func isVisible(isPhone: Bool, isDemo: Bool, supportsIntake: Bool) -> Bool {
        isPhone && !isDemo && supportsIntake
    }

    func visible(model: AppModel) -> Bool {
        Self.isVisible(isPhone: UIDevice.current.userInterfaceIdiom == .phone,
                       isDemo: model.isDemo || model.hub?.isDemo == true,
                       supportsIntake: model.hubFeatures.contains(HealthIntake.feature))
    }

    func enabled(model: AppModel) -> Bool {
        state.enabled && state.group == model.hub?.group && state.hubID == model.hub?.hello.hubID
    }

    func error(model: AppModel) -> String? {
        if settingErrorGroup == model.hub?.group, settingErrorHubID == model.hub?.hello.hubID,
           let settingError { return settingError }
        return enabled(model: model) ? state.lastError : nil
    }

    func load() async { state = await engine.snapshot() }

    private func allowed(model: AppModel, group: String, hubID: String?) -> Bool {
        visible(model: model) && model.hub?.group == group && model.hub?.hello.hubID == hubID
            && HealthDataLock.isProtectedDataAvailable()
    }

    func setEnabled(_ value: Bool, model: AppModel) async {
        guard !changing, let hub = model.hub, visible(model: model) else { return }
        settingError = nil
        settingErrorGroup = hub.group
        settingErrorHubID = hub.hello.hubID
        changing = true
        let group = hub.group, hubID = hub.hello.hubID
        state = await engine.setEnabled(value, group: group, hubID: hubID) { @Sendable [weak self, weak model] in
            await MainActor.run { guard let self, let model else { return false }; return self.allowed(model: model, group: group, hubID: hubID) }
        }
        denied = await engine.denied
        if value && !enabled(model: model) { settingError = state.lastError }
        changing = false
        if value { await refresh(model: model) }
    }

    func refresh(model: AppModel) async {
        guard let hub = model.hub, allowed(model: model, group: hub.group, hubID: hub.hello.hubID) else { return }
        let api = hub.api, group = hub.group, hubID = hub.hello.hubID
        state = await engine.refresh(group: group, hubID: hubID, fetch: { try await api.healthIntakeChanges(after: $0) }) { @Sendable [weak self, weak model] in
            await MainActor.run { guard let self, let model else { return false }; return self.allowed(model: model, group: group, hubID: hubID) }
        }
        denied = await engine.denied
    }
}
#endif
