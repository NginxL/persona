#if os(iOS) || os(watchOS)
import Foundation
import Darwin
import WidgetKit

nonisolated enum WatchIntakeKind: String, CaseIterable, Sendable {
    case water, protein
    var widgetKind: String { "persona.watch.\(rawValue)" }
    static var widgetKinds: [String] { allCases.map(\.widgetKind) }
    static let families: [WidgetFamily] = [.accessoryCircular, .accessoryRectangular, .accessoryInline]

    /// Call after an accepted watch intake write. Providers always re-read, even if the cache is still fresh.
    static func reloadTimelines() {
        for kind in widgetKinds { WidgetCenter.shared.reloadTimelines(ofKind: kind) }
    }
}

/// Decode only what a complication draws; records and notes never enter its persisted snapshot.
nonisolated struct WatchIntakeReading: Codable, Equatable, Sendable {
    struct Portion: Codable, Equatable, Sendable {
        var today: Double
        var goal: Double
        var fraction: Double { goal > 0 ? max(0, min(1, today / goal)) : 0 }
    }
    var water: Portion
    var protein: Portion
    var nextDay: Date
    var zone: String

    func portion(_ kind: WatchIntakeKind, at date: Date) -> Portion {
        var value = kind == .water ? water : protein
        if date >= nextDay { value.today = 0 }
        return value
    }

    struct Answer: Decodable, Sendable {
        struct Day: Decodable, Sendable { var date: String }
        var water: Portion
        var protein: Portion
        var days: [Day]
        var zone: String
    }

    init(_ value: Answer, at now: Date) throws {
        guard [value.water.today, value.water.goal, value.protein.today, value.protein.goal]
            .allSatisfy({ $0.isFinite && $0 >= 0 }) else { throw URLError(.cannotParseResponse) }
        // Go's unresolved "Local" label is not the watch's zone. Keep the hub's authoritative bucket until
        // the next GET rather than clearing its current amounts at a guessed UTC/phone midnight.
        guard value.zone != "Local", let zone = TimeZone(identifier: value.zone) else {
            self.init(water: value.water, protein: value.protein, nextDay: .distantFuture, zone: value.zone)
            return
        }
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = zone
        let fields = value.days.last?.date.split(separator: "-").compactMap { Int($0) } ?? []
        let start: Date
        if fields.count == 3, let day = calendar.date(from: DateComponents(year: fields[0], month: fields[1], day: fields[2])) {
            start = day
        } else { start = calendar.startOfDay(for: now) }
        let boundary = calendar.date(byAdding: .day, value: 1, to: start) ?? now
        self.init(water: value.water, protein: value.protein, nextDay: boundary, zone: value.zone)
    }

    init(water: Portion, protein: Portion, nextDay: Date, zone: String) {
        self.water = water; self.protein = protein; self.nextDay = nextDay; self.zone = zone
    }
}

nonisolated struct WatchIntakeRecord: Codable, Sendable {
    var generation: UUID
    var requestedAt: Date
    var reading: WatchIntakeReading?
    var needsHubUpdate = false
}

/// A separate, bounded file in the watch App Group. No credentials and no extension catalog/cache mutations.
nonisolated struct WatchIntakeStore: Sendable {
    var root: URL?
    static var shared: Self {
        #if DEBUG
        if ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] != nil { return Self(root: nil) }
        #endif
        return Self(root: ExtensionSurfaceStore.shared.root?.deletingLastPathComponent()
            .appendingPathComponent("WatchIntake", isDirectory: true))
    }

    func intakeRecord(group: String, generation: UUID) -> WatchIntakeRecord? {
        let record = try? transaction(write: false) { $0[group] }
        return record?.generation == generation ? record : nil
    }

    func storeIntake(_ record: WatchIntakeRecord, group: String, current: [String: ExtensionSurfaceGroup]) throws {
        try transaction { values in
            values = values.filter { current[$0.key]?.generation == $0.value.generation }
            guard current[group]?.generation == record.generation else { return }
            if let old = values[group], old.requestedAt > record.requestedAt { return }
            values[group] = record
            // A configured device normally has only a few groups; retain at most 32 small projections.
            let keep = Set(values.sorted { $0.value.requestedAt > $1.value.requestedAt }.prefix(32).map(\.key))
            values = values.filter { keep.contains($0.key) }
        }
    }

    private func transaction<T>(write: Bool = true, _ body: (inout [String: WatchIntakeRecord]) throws -> T) throws -> T {
        guard let root else { throw CocoaError(.fileNoSuchFile) }
        let fm = FileManager.default
        try fm.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        var directory = root
        var attributes = URLResourceValues(); attributes.isExcludedFromBackup = true
        try directory.setResourceValues(attributes)
        let descriptor = open(root.appendingPathComponent("store.lock").path, O_CREAT | O_RDWR, 0o600)
        guard descriptor >= 0 else { throw CocoaError(.fileReadNoPermission) }
        defer { close(descriptor) }
        guard flock(descriptor, LOCK_EX) == 0 else { throw CocoaError(.fileReadUnknown) }
        defer { flock(descriptor, LOCK_UN) }
        let file = root.appendingPathComponent("snapshot.json")
        var values: [String: WatchIntakeRecord] = [:]
        if fm.fileExists(atPath: file.path) {
            let data = try Data(contentsOf: file)
            guard data.count <= 65_536 else { throw CocoaError(.fileReadTooLarge) }
            values = try JSONDecoder().decode([String: WatchIntakeRecord].self, from: data)
        }
        let result = try body(&values)
        if write {
            var options: Data.WritingOptions = [.atomic]
            #if os(iOS) || os(watchOS) || os(visionOS)
            options.insert(.completeFileProtectionUntilFirstUserAuthentication)
            #endif
            try JSONEncoder().encode(values).write(to: file, options: options)
            try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        }
        return result
    }
}

nonisolated struct WatchIntakeEntry: TimelineEntry, Sendable {
    var date: Date
    var reading: WatchIntakeReading?
    var paired = false
    var needsHubUpdate = false
    var cached = false
}

nonisolated enum WatchIntakeTimeline {
    static let refreshInterval: TimeInterval = 15 * 60

    static func group(_ selected: String?, in snapshot: ExtensionSurfaceSnapshot) -> String? {
        if let selected { return snapshot.groups[selected] == nil ? nil : selected }
        if snapshot.groups[""] != nil { return "" }
        return snapshot.groups.count == 1 ? snapshot.groups.keys.first : nil
    }

    @concurrent static func cached(group selected: String?, surfaces: ExtensionSurfaceStore = .shared,
                                   cache: WatchIntakeStore = .shared, now: Date = .now) async -> WatchIntakeEntry {
        let snapshot = surfaces.read()
        guard let id = group(selected, in: snapshot), let state = snapshot.groups[id] else { return .init(date: now) }
        let saved = cache.intakeRecord(group: id, generation: state.generation)
        return .init(date: now, reading: saved?.reading, paired: true, needsHubUpdate: saved?.needsHubUpdate ?? false,
                     cached: saved.map { now >= $0.requestedAt.addingTimeInterval(refreshInterval) } ?? false)
    }

    @concurrent static func load(group selected: String?, surfaces: ExtensionSurfaceStore = .shared,
                                cache: WatchIntakeStore = .shared, transport: ExtensionSurfaceClient.Transport? = nil,
                                now: Date = .now) async -> WatchIntakeEntry {
        let snapshot = surfaces.read()
        guard let id = group(selected, in: snapshot), let state = snapshot.groups[id] else { return .init(date: now) }
        let client = ExtensionSurfaceClient(credential: state.credential, transport: transport)
        do {
            // This narrow wrapper decodes through the existing authenticated, redirect-refusing client.
            let answer: WatchIntakeReading.Answer = try await client.request("/api/v1/assistant/health/intake",
                query: [.init(name: "space", value: "health"), .init(name: "days", value: "1")])
            let reading = try WatchIntakeReading(answer, at: now)
            try Task.checkCancellation()
            let current = surfaces.read()
            guard current.groups[id]?.generation == state.generation,
                  current.groups[id]?.credential.sameAuthorization(as: state.credential) == true else { return .init(date: now) }
            let record = WatchIntakeRecord(generation: state.generation, requestedAt: now, reading: reading)
            try? cache.storeIntake(record, group: id, current: current.groups)
            if cache.intakeRecord(group: id, generation: state.generation) != nil {
                return await cached(group: selected, surfaces: surfaces, cache: cache, now: now)
            }
            return .init(date: now, reading: reading, paired: true)
        } catch {
            if let error = error as? ExtensionSurfaceHTTPError, [403, 404].contains(error.status), !Task.isCancelled {
                let current = surfaces.read()
                try? cache.storeIntake(.init(generation: state.generation, requestedAt: now, needsHubUpdate: true),
                                group: id, current: current.groups)
                var entry = await cached(group: selected, surfaces: surfaces, cache: cache, now: now)
                // Even an unavailable protected cache must not turn a hub's 403 into an invented zero.
                let newer = cache.intakeRecord(group: id, generation: state.generation).map { $0.requestedAt > now } ?? false
                if current.groups[id]?.generation == state.generation, !newer { entry.needsHubUpdate = true }
                return entry
            }
            if (error as? ExtensionSurfaceHTTPError)?.status == 401 { return .init(date: now) }
            var entry = await cached(group: selected, surfaces: surfaces, cache: cache, now: now)
            entry.cached = entry.reading != nil
            return entry
        }
    }

    static func timeline(_ entry: WatchIntakeEntry) -> Timeline<WatchIntakeEntry> {
        var entries = [entry]
        let due = entry.date.addingTimeInterval(refreshInterval)
        if entry.reading != nil, !entry.cached, due < (entry.reading?.nextDay ?? .distantFuture) {
            var stale = entry; stale.date = due; stale.cached = true
            entries.append(stale)
        }
        if let boundary = entry.reading?.nextDay, boundary > entry.date, boundary != .distantFuture {
            var next = entry; next.date = boundary; next.cached = true
            entries.append(next)
        }
        let boundary = entry.reading?.nextDay
        let refresh = boundary.flatMap { $0 > entry.date ? min(due, $0) : nil } ?? due
        return Timeline(entries: entries, policy: .after(refresh))
    }
}
#endif
