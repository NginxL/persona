import Foundation
#if os(iOS)
import UIKit
#endif
import PersonaWire

/// Whether HealthKit can be read right now. A locked phone answers HealthKit
/// collection queries with `HKError.errorDatabaseInaccessible`: the app must not
/// even start the queries then, and serve the on-disk snapshot instead.
nonisolated enum HealthDataLock {
    /// Indirected so tests can simulate a locked device without locking the Mac
    /// the tests run on. The app target defaults to MainActor, so the
    /// UIApplication access is wrapped in assumeIsolated: every production call
    /// site runs on the main actor, and tests replace the closure anyway.
    nonisolated(unsafe) static var isProtectedDataAvailable: @Sendable () -> Bool = {
        #if os(iOS)
        MainActor.assumeIsolated { UIApplication.shared.isProtectedDataAvailable }
        #else
        true
        #endif
    }
}

/// One persisted per-day health summary, the same granularity as
/// `phone_health_summary` gives live: day ("yyyy-MM-dd") -> metric -> value
/// (a number, or heart_rate's average/min/max object). Written while the app is
/// in the foreground and the device is unlocked, read at any time, including
/// while the phone is locked.
nonisolated struct HealthSnapshot: Codable, Sendable {
    /// Locked-phone answers must not present old health data as if it were current.
    static let maximumAge: TimeInterval = 48 * 60 * 60

    var asOf: Date
    var windowDays: Int
    var metrics: [String]
    var rows: [String: [String: JSONValue]]
    /// The save-time zone keeps day rows and `partial` stable after travel.
    /// Optional so snapshots written before this field existed still decode.
    var timeZone: String? = nil

    /// Builds a `phone_health_summary`-shaped answer from the stored rows,
    /// restricted to the requested metrics and day window ending at `asOf`.
    /// Returns nil when the snapshot is expired or holds nothing inside the
    /// window (callers then say `device_locked`).
    func response(metrics requested: [String], days: Int, now: Date, calendar: Calendar = .current) -> JSONValue? {
        guard now.timeIntervalSince(asOf) <= Self.maximumAge else { return nil }
        var snapshotCalendar = calendar
        if let timeZone, let savedTimeZone = TimeZone(identifier: timeZone) {
            snapshotCalendar.timeZone = savedTimeZone
        }
        let zone = snapshotCalendar.timeZone
        let end = asOf
        let start = snapshotCalendar.startOfDay(for: snapshotCalendar.date(byAdding: .day, value: -(max(days, 1) - 1), to: end) ?? end)
        let firstDay = ToolDate.day(start, timeZone: zone)
        let lastDay = ToolDate.day(end, timeZone: zone)

        var byDay: [String: [String: JSONValue]] = [:]
        for (day, metrics) in rows where day >= firstDay && day <= lastDay {
            var kept: [String: JSONValue] = [:]
            for metric in requested {
                if let value = metrics[metric] { kept[metric] = value }
            }
            if !kept.isEmpty { byDay[day] = kept }
        }
        if byDay.isEmpty { return nil }

        let rows: [JSONValue] = byDay.keys.sorted().map { day in
            var row: [String: JSONValue] = ["date": .string(day)]
            for (metric, value) in byDay[day] ?? [:] { row[metric] = value }
            if day == lastDay { row["partial"] = .bool(true) }
            return .object(row)
        }
        return .object([
            "start": .string(ToolDate.string(start, timeZone: zone)),
            "end": .string(ToolDate.string(end, timeZone: zone)),
            "days": .number(Double(days)),
            "metrics": .array(requested.map { .string($0) }),
            "data": .array(rows),
            "source": .string("snapshot"),
            "as_of": .string(ToolDate.string(asOf, timeZone: zone)),
        ])
    }
}

/// The store behind the locked-phone fallback. Everything goes through one actor
/// so a foreground refresh and a background tool call never write the file at
/// once. The file is protected `untilFirstUserAuthentication`: the system keeps
/// it readable after the first unlock, including while the phone is locked again.
nonisolated protocol HealthSnapshotStoring: Sendable {
    func load() async -> HealthSnapshot?
    @discardableResult
    func save(_ snapshot: HealthSnapshot) async -> Bool
    /// Returns the current snapshot when a refresh ran less than the throttle interval ago.
    func cachedIfFresh(now: Date) async -> HealthSnapshot?
    func markRefreshed(at date: Date) async
}

actor HealthSnapshotStore: HealthSnapshotStoring {
    static let shared = HealthSnapshotStore()

    /// Foreground refreshes are cheap (a handful of collection queries) but must
    /// not run on every scene-phase change or every tool call.
    nonisolated static let refreshInterval: TimeInterval = 15 * 60

    /// How many days the persisted snapshot covers. The tool allows up to 90, so
    /// the snapshot can answer every window a caller may ask for.
    nonisolated static let windowDays = 90

    let directory: URL
    private let fileURL: URL
    private let files = FileManager.default
    private var lastRefresh: Date?
    private var cached: HealthSnapshot?

    init(directory: URL = URL.applicationSupportDirectory.appending(path: "health-snapshot", directoryHint: .isDirectory)) {
        self.directory = directory
        self.fileURL = directory.appending(path: "summary.json")
    }

    func load() -> HealthSnapshot? {
        if let cached { return cached }
        guard let data = files.contents(atPath: fileURL.path) else { return nil }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        guard let snapshot = try? decoder.decode(HealthSnapshot.self, from: data) else { return nil }
        cached = snapshot
        return snapshot
    }

    @discardableResult
    func save(_ snapshot: HealthSnapshot) -> Bool {
        do {
            if !files.fileExists(atPath: directory.path) {
                try files.createDirectory(at: directory, withIntermediateDirectories: true)
                // Readable while locked after the first unlock, like the Lark outbox stores.
                try files.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                                        ofItemAtPath: directory.path)
            }
            // Reassert both flags on every save so snapshots written by older builds are excluded too.
            try excludeFromBackup(at: directory)
            let encoder = JSONEncoder()
            encoder.dateEncodingStrategy = .iso8601
            let data = try encoder.encode(snapshot)
            try data.write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            // The file is already new; keep the in-memory copy consistent even if setting its flag fails.
            cached = snapshot
            try excludeFromBackup(at: fileURL)
            return true
        } catch {
            // A missing snapshot only makes the first locked call say device_locked; never fail the live tool over it.
            return false
        }
    }

    private func excludeFromBackup(at url: URL) throws {
        var url = url
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try url.setResourceValues(values)
    }

    func cachedIfFresh(now: Date) -> HealthSnapshot? {
        guard let lastRefresh, now.timeIntervalSince(lastRefresh) < Self.refreshInterval else { return nil }
        return cached ?? load()
    }

    func markRefreshed(at date: Date) {
        lastRefresh = date
    }
}
