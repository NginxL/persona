#if os(iOS)
import Foundation
import HealthKit
import os
import PersonaWire

/// Where the watched types' snapshots live (#13740). One actor writes them, so a background wake and a foreground
/// catch-up never write the same file at once. Files are protected `untilFirstUserAuthentication`, like the summary
/// snapshot (`HealthSnapshotStore`): readable after the first unlock, including while the phone is locked again, and
/// excluded from backups.
nonisolated protocol HealthSampleStoring: Sendable {
    func load(_ name: String) async -> HealthSampleSnapshot?
    @discardableResult
    func save(_ snapshot: HealthSampleSnapshot) async -> Bool
    func remove(_ name: String) async
}

actor HealthSampleStore: HealthSampleStoring {
    static let shared = HealthSampleStore()

    let directory: URL
    private let files = FileManager.default

    init(directory: URL = URL.applicationSupportDirectory.appending(path: "health-snapshot/samples", directoryHint: .isDirectory)) {
        self.directory = directory
    }

    /// Names come from `HealthCatalog` (lowercase letters, digits and `_`), never from the caller directly.
    private func file(_ name: String) -> URL { directory.appending(path: "\(name).json") }

    func load(_ name: String) -> HealthSampleSnapshot? {
        guard let data = files.contents(atPath: file(name).path) else { return nil }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return try? decoder.decode(HealthSampleSnapshot.self, from: data)
    }

    @discardableResult
    func save(_ snapshot: HealthSampleSnapshot) -> Bool {
        do {
            if !files.fileExists(atPath: directory.path) {
                try files.createDirectory(at: directory, withIntermediateDirectories: true,
                                          attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
            }
            var folder = directory
            var excluded = URLResourceValues()
            excluded.isExcludedFromBackup = true
            try folder.setResourceValues(excluded)
            let encoder = JSONEncoder()
            encoder.dateEncodingStrategy = .iso8601
            var url = file(snapshot.name)
            try encoder.encode(snapshot).write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            try url.setResourceValues(excluded)
            return true
        } catch {
            // A missing snapshot only makes a locked call say device_locked; never fail a read over it.
            return false
        }
    }

    func remove(_ name: String) {
        try? files.removeItem(at: file(name))
    }
}

/// Keeps the watched types' snapshots current (#13740): an `HKObserverQuery` per type, set up at launch where HealthKit
/// wants it for a background wake, and HealthKit background delivery in a build signed with it
/// (`WorkoutReports.deliversInBackground`, config.json `app.health_background_delivery`). A wake reads what is new since
/// the snapshot's anchor and merges it in. A locked iPhone cannot read HealthKit at all: a wake then reads nothing and
/// the samples saved in the meantime arrive with the next wake or foreground catch-up after an unlock.
@MainActor
final class HealthSampleWatcher {
    static let shared = HealthSampleWatcher()
    /// Every watched type can wake the app; keep the number of observers reasonable.
    static let maximumTypes = 40
    /// Foreground catch-ups run at most this often.
    static let foregroundInterval: TimeInterval = 5 * 60
    private static let watchedKey = "health.watchedTypes"
    private nonisolated static let log = Logger(subsystem: "persona", category: "health-watch") // also from HealthKit's queue

    private let health = HKHealthStore()
    private let store: any HealthSampleStoring
    private let defaults: UserDefaults
    private var observers: [String: HKObserverQuery] = [:]
    private var running: Set<String> = []
    private var lastForeground: Date?
    /// HealthKit turned background delivery down for at least one type although the build has it.
    private(set) var backgroundRefused = false

    /// Off in tests: no HealthKit observer runs there.
    private let registersObservers: Bool

    init(store: any HealthSampleStoring = HealthSampleStore.shared, defaults: UserDefaults = .standard, registersObservers: Bool = true) {
        self.store = store
        self.defaults = defaults
        self.registersObservers = registersObservers
    }

    /// The watched type names, in the order they were added. A small list that rarely changes, read synchronously at launch.
    var watched: [String] { defaults.stringArray(forKey: Self.watchedKey) ?? [] }

    /// Background delivery is on: the build has it and HealthKit did not refuse it.
    var deliversInBackground: Bool { WorkoutReports.deliversInBackground && !backgroundRefused }

    /// From `application(_:didFinishLaunchingWithOptions:)`: observers have to be registered again on every launch.
    func start() {
        guard HKHealthStore.isHealthDataAvailable() else { return }
        for name in watched { observe(name) }
    }

    /// Adds (or with `stop`, removes) types; returns the watched list afterwards. Added types are read right away.
    @discardableResult
    func set(_ names: [String], stop: Bool) async -> [String] {
        var list = watched
        if stop {
            list.removeAll(where: names.contains)
            for name in names {
                stopObserving(name)
                await store.remove(name)
            }
        } else {
            for name in names where !list.contains(name) && list.count < Self.maximumTypes { list.append(name) }
        }
        defaults.set(list, forKey: Self.watchedKey)
        if !stop {
            for name in names where list.contains(name) {
                observe(name)
                await catchUp(name)
            }
        }
        return list
    }

    /// When Persona comes to the front: catch every watched type up (throttled).
    func foreground(now: Date = Date()) async {
        if let lastForeground, now.timeIntervalSince(lastForeground) < Self.foregroundInterval { return }
        lastForeground = now
        for name in watched { await catchUp(name, now: now) }
    }

    private func observe(_ name: String) {
        guard registersObservers, observers[name] == nil, let entry = HealthCatalog.entry(named: name),
              let type = HealthCatalog.objectType(entry) as? HKSampleType else { return }
        // HealthKit retries a wake whose completion is never called, so it is called whatever the catch-up did.
        let query = HKObserverQuery(sampleType: type, predicate: nil) { @Sendable _, completion, failure in
            let done = HealthWakeDone(completion)
            if let failure { Self.log.error("health observer failed: \(failure.localizedDescription, privacy: .public)") }
            Task { @MainActor in
                await HealthSampleWatcher.shared.catchUp(name)
                // The summary's locked-phone snapshot too; throttled inside its store.
                await HealthTool.refreshSnapshot()
                done.call()
            }
        }
        observers[name] = query
        health.execute(query)
        guard WorkoutReports.deliversInBackground else { return }
        health.enableBackgroundDelivery(for: type, frequency: .immediate) { @Sendable ok, failure in
            if !ok { Self.log.error("health background delivery refused: \(failure?.localizedDescription ?? "no reason", privacy: .public)") }
            Task { @MainActor in if !ok { HealthSampleWatcher.shared.backgroundRefused = true } }
        }
    }

    private func stopObserving(_ name: String) {
        guard let query = observers.removeValue(forKey: name) else { return }
        health.stop(query)
        if let entry = HealthCatalog.entry(named: name), let type = HealthCatalog.objectType(entry) {
            health.disableBackgroundDelivery(for: type) { @Sendable _, _ in }
        }
    }

    /// Reads what is new since the snapshot's anchor and merges it in. Does nothing while the phone is locked: HealthKit
    /// cannot be read then, and the previous snapshot stays as it is.
    func catchUp(_ name: String, now: Date = Date()) async {
        guard HealthDataLock.isProtectedDataAvailable(), !running.contains(name),
              let entry = HealthCatalog.entry(named: name),
              HealthCatalog.objectType(entry) is HKSampleType else { return }
        running.insert(name)
        defer { running.remove(name) }
        let previous = await store.load(name)
        let keepAfter = now.addingTimeInterval(-Double(HealthSampleSnapshot.keepDays) * 86400)
        var anchor = previous?.anchor
        var samples = previous?.samples ?? []
        let pageSize = 500
        for _ in 0..<10 {
            let page: HealthRecords.Page
            do {
                page = try await HealthRecords.readPage(entry: entry, start: keepAfter, end: nil, anchor: anchor,
                                                        limit: pageSize, store: health)
            } catch {
                return // locked meanwhile or not allowed: keep the previous snapshot and anchor
            }
            samples = HealthSampleSnapshot.merged(samples, added: page.samples, deleted: Set(page.deleted), keepAfter: keepAfter)
            anchor = page.anchor ?? anchor
            if page.samples.count < pageSize { break }
        }
        await store.save(HealthSampleSnapshot(name: name, asOf: now, anchor: anchor, samples: samples))
    }
}

/// HealthKit's completion for one background wake, carried to the main actor that calls it.
private nonisolated final class HealthWakeDone: @unchecked Sendable {
    private let completion: () -> Void
    init(_ completion: @escaping () -> Void) { self.completion = completion }
    func call() { completion() }
}
#endif
