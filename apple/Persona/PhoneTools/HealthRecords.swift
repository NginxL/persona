#if os(iOS)
import Foundation
import HealthKit
import PersonaWire

/// The generic Apple Health tools (#13740): every HealthKit type, read sample by sample with its source, device and
/// metadata, paged and incremental through HealthKit's anchors; the apps and devices behind them; read access asked per
/// type; and an on-device snapshot of watched types for calls that come while the iPhone is locked. Health data goes back
/// only in the tool result to the hub that asked, and nothing here writes it to a log. `phone_health_summary`
/// (`HealthTool`) stays for daily totals.
enum HealthRecords {
    static let privacyNote = "Health data goes only to the hub that asked and is not written to logs."

    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_health_types",
                    description: """
                    List the health data types this phone can read, from Apple Health on an iPhone or Health Connect on \
                    Android: quantities, categories, workouts, correlations, ECGs, audiograms and the rest. Each row has \
                    the type name to pass to phone_health_samples, the store's own identifier, the kind, the unit values \
                    come in, and access: not_requested (ask with phone_health_request_access), requested (the user was \
                    asked; Apple Health never tells apps whether reading was allowed, so no data can also mean it was \
                    refused), granted or not_granted (Health Connect tells), or unavailable with a reason. Health data \
                    goes only to the hub that asked and is not written to logs.
                    """,
                    inputSchema: Schema.object([
                        "kind": Schema.string("Only types of this kind, for example quantity, category or workout; every kind when absent."),
                    ])
                ),
                source: .health,
                run: { args in try await types(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_health_samples",
                    description: """
                    Read individual health samples of one type in a time window, in the order the health store returns \
                    them: id, start, end, value and unit (or the kind's own fields: a category's value and value_label, \
                    a workout's activity and totals, the readings inside a correlation, an ECG's classification and \
                    average heart rate, an audiogram's hearing levels), source (app name and bundle id or package), \
                    device (name, manufacturer, model, versions) and metadata. Pass type from phone_health_types. \
                    Returns at most limit samples with more: true when there are more, and an anchor: pass it back with \
                    the same type and window for the next page, and later for only what was added since, with the ids \
                    of deleted samples in deleted. A type Persona may not read yet answers its access (not_requested, \
                    or not_granted on Android) instead of failing. Health data goes only to the hub that asked and is not written to \
                    logs. When the iPhone is locked the app cannot read Apple Health: a type kept current with \
                    phone_health_watch answers from the on-device snapshot ("source": "snapshot" with "as_of"; its \
                    samples may repeat ones you already have, so match them by id), any other type answers status: \
                    device_locked.
                    """,
                    inputSchema: Schema.object([
                        "type": Schema.string("The type name from phone_health_types, for example heart_rate."),
                        "start": Schema.string("Window start, ISO 8601 with a timezone. Default: 7 days ago."),
                        "end": Schema.string("Window end, ISO 8601 with a timezone. Default: none, so an anchor keeps finding new samples."),
                        "limit": Schema.integer("At most this many samples, default 100, max 500.", min: 1, max: 500),
                        "anchor": Schema.string("The anchor from the previous answer for this type and window: the next page, or what changed since."),
                    ], required: ["type"])
                ),
                source: .health,
                run: { args in try await samples(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_health_sources",
                    description: """
                    List the apps and devices that saved health data on this phone: each source's name, bundle id (or \
                    Android package) and the types it saved. With type, only that type's sources in the last days, plus \
                    the devices its samples name (name, manufacturer, model, hardware and software versions). Without \
                    type, every type the user was asked about is looked at. Health data goes only to the hub that asked \
                    and is not written to logs. When the iPhone is locked it answers from the on-device snapshots of the \
                    types phone_health_watch keeps current ("source": "snapshot").
                    """,
                    inputSchema: Schema.object([
                        "type": Schema.string("Only this type (a name from phone_health_types); every type the user was asked about when absent."),
                        "days": Schema.integer("How many days back to look, default 30, max 3650.", min: 1, max: 3650),
                    ])
                ),
                source: .health,
                run: { args in try await sources(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_health_request_access",
                    description: """
                    Ask the user on the phone to allow reading the given health types (names from phone_health_types; \
                    ["all"] for every type the phone can read). Only types not asked about yet bring up the system's \
                    permission screen, so the user decides once and later calls ask nothing. Needs Persona open on the \
                    phone's screen; otherwise it answers status: needs_foreground. Returns each type's access afterwards.
                    """,
                    inputSchema: Schema.object([
                        "types": Schema.array("Type names from phone_health_types, or [\"all\"].", of: Schema.string("One type name.")),
                    ], required: ["types"])
                ),
                source: .health,
                run: { args in try await requestAccess(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_health_watch",
                    description: """
                    Keep an on-device snapshot of the given Apple Health types current, so phone_health_samples and \
                    phone_health_sources can still answer while the iPhone is locked or Persona is in the background: \
                    new samples are read as Apple Health reports them (in the background when this build has HealthKit \
                    background delivery, otherwise when Persona opens), the last 7 days and at most 2000 per type, at \
                    most 40 types. stop: true stops watching them and deletes their snapshots. Returns the watched types \
                    and whether background delivery is on. A locked iPhone cannot be read even in the background: what \
                    arrives meanwhile is read after the next unlock.
                    """,
                    inputSchema: Schema.object([
                        "types": Schema.array("Type names from phone_health_types.", of: Schema.string("One type name.")),
                        "stop": Schema.boolean("Stop watching these types instead. Default false."),
                    ], required: ["types"])
                ),
                source: .health,
                run: { args in try await watch(args) }
            ),
        ]
    }

    // MARK: - Arguments

    private static func requireHealth() throws {
        guard HKHealthStore.isHealthDataAvailable() else {
            throw ToolFailure(.unavailable, "此设备不支持健康数据 / Health data is not available on this device") // i18n-ignore
        }
    }

    static func typeArgument(_ args: JSONValue, _ key: String = "type") throws -> HealthCatalog.Entry {
        guard let raw = ToolArgs.string(args, key) else {
            throw ToolFailure(.invalidArgument, "\(key) is required: a type name from phone_health_types")
        }
        guard let entry = HealthCatalog.entry(named: raw) else {
            throw ToolFailure(.invalidArgument, "unknown health type \"\(raw)\"; call phone_health_types for the names")
        }
        return entry
    }

    /// The entries a `types` array names, each once; `"all"` is every readable type.
    static func typeList(_ args: JSONValue) throws -> [HealthCatalog.Entry] {
        let names = (args["types"]?.array ?? []).compactMap(\.string)
        guard !names.isEmpty else { throw ToolFailure(.invalidArgument, "types is required: type names from phone_health_types") }
        if names.contains(where: { $0.lowercased() == "all" }) { return HealthCatalog.readable() }
        var out: [HealthCatalog.Entry] = []
        for name in names {
            guard let entry = HealthCatalog.entry(named: name) else {
                throw ToolFailure(.invalidArgument, "unknown health type \"\(name)\"; call phone_health_types for the names")
            }
            if !out.contains(entry) { out.append(entry) }
        }
        return out
    }

    // MARK: - Access

    /// One type's access as the tools report it. HealthKit says only whether asking would still show the sheet: that is
    /// `not_requested` against `requested`; whether the user allowed reading stays hidden from every app.
    @concurrent nonisolated static func accessStatus(_ entry: HealthCatalog.Entry, store: HKHealthStore) async -> String {
        if HealthCatalog.unreadableReason(entry) != nil { return "unavailable" }
        let types = HealthCatalog.authorizationTypes(entry)
        guard !types.isEmpty else { return "unsupported" }
        guard let status = try? await store.statusForAuthorizationRequest(toShare: [], read: types) else { return "unknown" }
        switch status {
        case .shouldRequest: return "not_requested"
        case .unnecessary: return "requested"
        default: return "unknown"
        }
    }

    static func accessRow(_ entry: HealthCatalog.Entry, access: String) -> JSONValue {
        var unit: String?
        if entry.kind == .quantity, let type = HealthCatalog.objectType(entry) as? HKQuantityType {
            unit = HealthCatalog.unit(for: type)?.unitString
        }
        let reason = HealthCatalog.unreadableReason(entry)
            ?? (access == "unsupported" ? "This iOS version does not have this type." : nil)
        return Output.object([
            ("type", .string(entry.name)),
            ("identifier", .string(entry.identifier)),
            ("kind", .string(entry.kind.rawValue)),
            ("unit", Output.opt(unit)),
            ("access", .string(access)),
            ("reason", Output.opt(reason)),
        ])
    }

    static func types(_ args: JSONValue) async throws -> [ToolContent] {
        try requireHealth()
        var kind: HealthCatalog.Kind?
        if let raw = ToolArgs.string(args, "kind") {
            guard let parsed = HealthCatalog.Kind(rawValue: raw) else {
                throw ToolFailure(.invalidArgument, "unknown kind \"\(raw)\"; use one of \(HealthCatalog.Kind.allCases.map(\.rawValue).joined(separator: ", "))")
            }
            kind = parsed
        }
        let store = HKHealthStore()
        var rows: [JSONValue] = []
        for entry in HealthCatalog.entries where kind == nil || entry.kind == kind {
            rows.append(accessRow(entry, access: await accessStatus(entry, store: store)))
        }
        return Output.json(Output.object([
            ("types", .array(rows)),
            ("count", .number(Double(rows.count))),
            ("note", .string("Apple Health never tells apps whether the user allowed reading a type: requested means the user "
                + "was asked, and a type without data may have been refused. " + privacyNote)),
        ]))
    }

    static func requestAccess(_ args: JSONValue) async throws -> [ToolContent] {
        try requireHealth()
        let wanted = try typeList(args)
        let store = HKHealthStore()
        let read = Set(wanted.flatMap(HealthCatalog.authorizationTypes))
        guard Foreground.isActive else {
            return Output.json(Output.object([
                ("status", .string("needs_foreground")),
                ("note", .string("Persona is not open on the iPhone, so the permission screen cannot show. Ask the user to "
                    + "open Persona, then call phone_health_request_access again.")),
            ]))
        }
        var asked = false
        if !read.isEmpty, (try? await store.statusForAuthorizationRequest(toShare: [], read: read)) == .shouldRequest {
            do {
                try await store.requestAuthorization(toShare: [], read: read)
                asked = true
            } catch {
                throw ToolFailure(.unavailable, "Apple Health could not ask for access: \(ToolErrors.describe(error))")
            }
        }
        var rows: [JSONValue] = []
        for entry in wanted { rows.append(accessRow(entry, access: await accessStatus(entry, store: store))) }
        return Output.json(Output.object([
            ("asked", .bool(asked)),
            ("types", .array(rows)),
            ("note", .string(asked
                ? "The user saw Apple Health's permission screen. Apple Health never says which types they allowed: read one to see."
                : "Nothing new to ask: every readable type here was asked about before. The user can change it in iOS Settings › Health › Data Access & Devices › Persona.")),
        ]))
    }

    // MARK: - Samples

    nonisolated struct Page: Sendable {
        var samples: [HealthStoredSample]
        var deleted: [String]
        var anchor: Data?
    }

    /// Anchors travel as `a1.` + base64url of HealthKit's archived anchor; `s1.<offset>` pages through a snapshot answer.
    nonisolated static func anchorText(_ data: Data?) -> String? {
        guard let data else { return nil }
        let base64 = data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
        return "a1." + base64
    }

    nonisolated static func anchorData(_ text: String) -> Data? {
        guard text.hasPrefix("a1.") else { return nil }
        var base64 = String(text.dropFirst(3)).replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        while base64.count % 4 != 0 { base64 += "=" }
        return Data(base64Encoded: base64)
    }

    nonisolated static func snapshotOffset(_ text: String?) -> Int? {
        guard let text, text.hasPrefix("s1.") else { return nil }
        return Int(text.dropFirst(3)).flatMap { $0 >= 0 ? $0 : nil }
    }

    /// One anchored read. HealthKit's anchor carries on from where the last read stopped: a page when `limit` cut it
    /// short, or what was added and deleted since.
    @concurrent nonisolated static func readPage(
        entry: HealthCatalog.Entry,
        start: Date,
        end: Date?,
        anchor: Data?,
        limit: Int,
        store: HKHealthStore
    ) async throws -> Page {
        try Task.checkCancellation()
        // Resolved here, so no HealthKit type crosses from the caller's actor.
        guard let type = HealthCatalog.objectType(entry) as? HKSampleType else {
            throw ToolFailure(.unavailable, "\(entry.name) has no samples to read")
        }
        return try await HealthQueryExecution<Page>(store: store).result { gate in
            let predicate = HKQuery.predicateForSamples(withStart: start, end: end, options: [])
            let previous = anchor.flatMap { try? NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: $0) }
            return HKAnchoredObjectQuery(type: type, predicate: predicate, anchor: previous, limit: limit) { @Sendable _, added, deleted, next, error in
                if let error {
                    gate.fail(error)
                    return
                }
                let unit = (type as? HKQuantityType).flatMap(HealthCatalog.unit(for:))
                let samples = (added ?? []).map {
                    HealthStoredSample(id: $0.uuid.uuidString, end: $0.endDate, json: HealthSampleJSON.encode($0, entry: entry, unit: unit))
                }
                let archived = next.flatMap { try? NSKeyedArchiver.archivedData(withRootObject: $0, requiringSecureCoding: true) }
                gate.succeed(Page(samples: samples, deleted: (deleted ?? []).map(\.uuid.uuidString), anchor: archived))
            }
        }
    }

    static func samples(
        _ args: JSONValue,
        snapshots: any HealthSampleStoring = HealthSampleStore.shared,
        isDataAvailable: () -> Bool = HealthDataLock.isProtectedDataAvailable,
        now: Date = Date()
    ) async throws -> [ToolContent] {
        try requireHealth()
        let entry = try typeArgument(args)
        let start = try ToolDate.parse(ToolArgs.string(args, "start")) ?? now.addingTimeInterval(-7 * 86400)
        let end = try ToolDate.parse(ToolArgs.string(args, "end"))
        if let end, end <= start { throw ToolFailure(.invalidArgument, "end must be after start") }
        let limit = ToolArgs.int(args, "limit", fallback: 100, min: 1, max: 500)
        let anchorArg = ToolArgs.string(args, "anchor")
        let base: [(String, JSONValue?)] = [
            ("type", .string(entry.name)),
            ("identifier", .string(entry.identifier)),
            ("kind", .string(entry.kind.rawValue)),
            ("start", .string(ToolDate.string(start))),
            ("end", end.map { .string(ToolDate.string($0)) }),
        ]
        if let reason = HealthCatalog.unreadableReason(entry) {
            return Output.json(Output.object(base + [("access", .string("unavailable")), ("reason", .string(reason))]))
        }
        // Locked phone: never touch HealthKit (it answers errorDatabaseInaccessible); answer from the watched snapshot.
        if !isDataAvailable() {
            return try await snapshotAnswer(entry: entry, base: base, start: start, end: end, limit: limit, anchor: anchorArg,
                                            snapshots: snapshots)
        }
        guard let type = HealthCatalog.objectType(entry) else {
            return Output.json(Output.object(base + [("access", .string("unsupported")),
                                                      ("reason", .string("This iOS version does not have this type."))]))
        }
        let store = HKHealthStore()
        let status = await accessStatus(entry, store: store)
        if status == "not_requested" { return notRequested(base) }
        if entry.kind == .activitySummary {
            let rows = try await activitySummaries(start: start, end: end ?? now, store: store)
            return Output.json(Output.object(base + [("access", .string(status)), ("summaries", .array(rows)), ("source", .string("live")),
                                                      ("note", .string("Activity summaries are one row per day; they take no anchor."))]))
        }
        guard let sampleType = type as? HKSampleType else {
            throw ToolFailure(.unavailable, "\(entry.name) has no samples to read")
        }
        var anchor: Data?
        if let anchorArg, snapshotOffset(anchorArg) == nil {
            guard let data = anchorData(anchorArg) else {
                throw ToolFailure(.invalidArgument, "anchor is not one this phone returned; leave it out to start from the beginning")
            }
            anchor = data
        }
        let page: Page
        do {
            page = try await readPage(entry: entry, start: start, end: end, anchor: anchor, limit: limit, store: store)
        } catch {
            if HealthTool.isDatabaseInaccessible(error) {
                return try await snapshotAnswer(entry: entry, base: base, start: start, end: end, limit: limit, anchor: anchorArg,
                                                snapshots: snapshots)
            }
            if (error as? HKError)?.code == .errorAuthorizationNotDetermined { return notRequested(base) }
            throw error
        }
        var unit: String?
        if let quantity = sampleType as? HKQuantityType { unit = HealthCatalog.unit(for: quantity)?.unitString }
        return Output.json(Output.object(base + [
            ("access", .string(status)),
            ("unit", Output.opt(unit)),
            ("samples", .array(page.samples.map(\.json))),
            ("deleted", .array(page.deleted.map { .string($0) })),
            ("more", .bool(page.samples.count >= limit)),
            ("anchor", Output.opt(anchorText(page.anchor ?? anchor))),
            ("source", .string("live")),
        ]))
    }

    private static func notRequested(_ base: [(String, JSONValue?)]) -> [ToolContent] {
        Output.json(Output.object(base + [
            ("access", .string("not_requested")),
            ("samples", .array([])),
            ("note", .string("The user has not been asked to allow reading this type: call phone_health_request_access with it "
                + "while Persona is open on the iPhone.")),
        ]))
    }

    /// The locked-phone answer: the watched type's snapshot inside the window, or an explicit device_locked status.
    static func snapshotAnswer(
        entry: HealthCatalog.Entry,
        base: [(String, JSONValue?)],
        start: Date,
        end: Date?,
        limit: Int,
        anchor: String?,
        snapshots: any HealthSampleStoring
    ) async throws -> [ToolContent] {
        guard let snapshot = await snapshots.load(entry.name) else {
            return Output.json(Output.object(base + [
                ("status", .string("device_locked")),
                ("samples", .array([])),
                ("note", .string("The iPhone is locked, so Apple Health cannot be read, and this type has no on-device snapshot. "
                    + "Ask again after the user unlocks the phone, or keep the type current with phone_health_watch.")),
            ]))
        }
        let inWindow = snapshot.samples.filter { sample in
            sample.end >= start && (end.map { sample.end <= $0 } ?? true)
        }
        let offset = min(snapshotOffset(anchor) ?? 0, inWindow.count)
        let page = Array(inWindow[offset..<min(offset + limit, inWindow.count)])
        let more = offset + page.count < inWindow.count
        // A live anchor the caller passed stays theirs: incremental reads resume from it once the phone is unlocked.
        let next = more ? "s1.\(offset + page.count)" : (snapshotOffset(anchor) == nil ? anchor : nil)
        return Output.json(Output.object(base + [
            ("samples", .array(page.map(\.json))),
            ("deleted", .array([])),
            ("more", .bool(more)),
            ("anchor", Output.opt(next)),
            ("source", .string("snapshot")),
            ("as_of", .string(ToolDate.string(snapshot.asOf))),
            ("note", .string("The iPhone is locked: these are the samples Persona saved on the phone up to as_of, the last "
                + "\(HealthSampleSnapshot.keepDays) days at most. They may repeat samples you already have; match them by id.")),
        ]))
    }

    @concurrent nonisolated static func activitySummaries(start: Date, end: Date, store: HKHealthStore) async throws -> [JSONValue] {
        let calendar = Calendar.current
        var fromDay = calendar.dateComponents([.era, .year, .month, .day], from: start)
        var toDay = calendar.dateComponents([.era, .year, .month, .day], from: end)
        fromDay.calendar = calendar
        toDay.calendar = calendar
        let from = fromDay, to = toDay
        return try await HealthQueryExecution<[JSONValue]>(store: store).result { gate in
            HKActivitySummaryQuery(predicate: HKQuery.predicate(forActivitySummariesBetweenStart: from, end: to)) { @Sendable _, summaries, error in
                if let error {
                    gate.fail(error)
                    return
                }
                gate.succeed((summaries ?? []).map { summary in
                    let day = summary.dateComponents(for: calendar)
                    let date = calendar.date(from: day).map { ToolDate.day($0) }
                    return Output.object([
                        ("date", Output.opt(date)),
                        ("active_energy_kcal", .number(summary.activeEnergyBurned.doubleValue(for: .kilocalorie()))),
                        ("active_energy_goal_kcal", .number(summary.activeEnergyBurnedGoal.doubleValue(for: .kilocalorie()))),
                        ("exercise_min", .number(summary.appleExerciseTime.doubleValue(for: .minute()))),
                        ("exercise_goal_min", .number(summary.exerciseTimeGoal?.doubleValue(for: .minute()) ?? 0)),
                        ("stand_hours", .number(summary.appleStandHours.doubleValue(for: .count()))),
                        ("stand_goal_hours", .number(summary.standHoursGoal?.doubleValue(for: .count()) ?? 0)),
                    ])
                })
            }
        }
    }

    // MARK: - Sources

    @concurrent nonisolated static func sourceList(entry: HealthCatalog.Entry, start: Date, store: HKHealthStore) async throws -> [JSONValue] {
        guard let type = HealthCatalog.objectType(entry) as? HKSampleType else { return [] }
        return try await HealthQueryExecution<[JSONValue]>(store: store).result { gate in
            HKSourceQuery(sampleType: type, samplePredicate: HKQuery.predicateForSamples(withStart: start, end: nil, options: [])) { @Sendable _, sources, error in
                if let error {
                    gate.fail(error)
                    return
                }
                gate.succeed((sources ?? []).sorted { $0.name < $1.name }.map { source in
                    Output.object([("name", .string(source.name)), ("bundle_id", .string(source.bundleIdentifier))])
                })
            }
        }
    }

    /// The distinct devices (and app revisions) the newest samples of `type` name.
    @concurrent nonisolated static func deviceList(entry: HealthCatalog.Entry, start: Date, store: HKHealthStore) async throws -> [JSONValue] {
        guard let type = HealthCatalog.objectType(entry) as? HKSampleType else { return [] }
        return try await HealthQueryExecution<[JSONValue]>(store: store).result { gate in
            HKSampleQuery(sampleType: type, predicate: HKQuery.predicateForSamples(withStart: start, end: nil, options: []), limit: 1000,
                          sortDescriptors: [NSSortDescriptor(key: HKSampleSortIdentifierEndDate, ascending: false)]) { @Sendable _, samples, error in
                if let error {
                    gate.fail(error)
                    return
                }
                var seen: Set<String> = []
                var out: [JSONValue] = []
                for sample in samples ?? [] {
                    guard let device = HealthSampleJSON.device(sample.device) else { continue }
                    let key = device.pretty
                    if seen.insert(key).inserted { out.append(device) }
                }
                gate.succeed(out)
            }
        }
    }

    static func sources(
        _ args: JSONValue,
        snapshots: any HealthSampleStoring = HealthSampleStore.shared,
        isDataAvailable: () -> Bool = HealthDataLock.isProtectedDataAvailable,
        watched: [String]? = nil,
        now: Date = Date()
    ) async throws -> [ToolContent] {
        try requireHealth()
        let days = ToolArgs.int(args, "days", fallback: 30, min: 1, max: 3650)
        let start = now.addingTimeInterval(-Double(days) * 86400)
        var only: HealthCatalog.Entry?
        if ToolArgs.string(args, "type") != nil { only = try typeArgument(args) }
        if !isDataAvailable() {
            let names = only.map { [$0.name] } ?? watched ?? HealthSampleWatcher.shared.watched
            return Output.json(await snapshotSources(names: names, snapshots: snapshots))
        }
        let store = HKHealthStore()
        var byBundle: [String: (name: String, types: [String])] = [:]
        var devices: [JSONValue]?
        let candidates = only.map { [$0] } ?? HealthCatalog.readable()
        for entry in candidates {
            guard HealthCatalog.objectType(entry) is HKSampleType else { continue }
            guard await accessStatus(entry, store: store) != "not_requested" else { continue }
            guard let found = try? await sourceList(entry: entry, start: start, store: store) else { continue }
            for source in found {
                guard let bundle = source["bundle_id"]?.string else { continue }
                var row = byBundle[bundle] ?? (name: source["name"]?.string ?? bundle, types: [])
                row.types.append(entry.name)
                byBundle[bundle] = row
            }
            if only != nil { devices = try? await deviceList(entry: entry, start: start, store: store) }
        }
        let rows: [JSONValue] = byBundle.keys.sorted().map { bundle in
            let row = byBundle[bundle]!
            return Output.object([
                ("name", .string(row.name)),
                ("bundle_id", .string(bundle)),
                ("types", .array(row.types.map { .string($0) })),
            ])
        }
        return Output.json(Output.object([
            ("days", .number(Double(days))),
            ("type", only.map { .string($0.name) }),
            ("sources", .array(rows)),
            ("devices", devices.map { .array($0) }),
            ("source", .string("live")),
            ("note", .string(only == nil
                ? "Types the user was never asked about are left out. Pass type for the devices behind one type. " + privacyNote
                : privacyNote)),
        ]))
    }

    /// Sources and devices gathered from the watched types' snapshots, for a locked phone.
    static func snapshotSources(names: [String], snapshots: any HealthSampleStoring) async -> JSONValue {
        var byBundle: [String: (name: String, types: [String])] = [:]
        var devices: [String: JSONValue] = [:]
        var asOf: Date?
        for name in names {
            guard let snapshot = await snapshots.load(name) else { continue }
            asOf = min(asOf ?? snapshot.asOf, snapshot.asOf)
            for sample in snapshot.samples {
                if let source = sample.json["source"], let bundle = source["bundle_id"]?.string {
                    var row = byBundle[bundle] ?? (name: source["name"]?.string ?? bundle, types: [])
                    if !row.types.contains(name) { row.types.append(name) }
                    byBundle[bundle] = row
                }
                if let device = sample.json["device"] { devices[device.pretty] = device }
            }
        }
        guard let asOf else {
            return Output.object([
                ("status", .string("device_locked")),
                ("sources", .array([])),
                ("note", .string("The iPhone is locked, so Apple Health cannot be read, and no watched type has an on-device "
                    + "snapshot. Ask again after the user unlocks the phone.")),
            ])
        }
        return Output.object([
            ("sources", .array(byBundle.keys.sorted().map { bundle in
                let row = byBundle[bundle]!
                return Output.object([("name", .string(row.name)), ("bundle_id", .string(bundle)), ("types", .array(row.types.map { .string($0) }))])
            })),
            ("devices", .array(devices.keys.sorted().compactMap { devices[$0] })),
            ("source", .string("snapshot")),
            ("as_of", .string(ToolDate.string(asOf))),
            ("note", .string("The iPhone is locked: these come from the on-device snapshots of the watched types only.")),
        ])
    }

    // MARK: - Watch

    static func watch(_ args: JSONValue) async throws -> [ToolContent] {
        try requireHealth()
        let wanted = try typeList(args).filter { HealthCatalog.objectType($0) is HKSampleType && HealthCatalog.unreadableReason($0) == nil }
        let stop = ToolArgs.bool(args, "stop") ?? false
        let watcher = HealthSampleWatcher.shared
        let before = watcher.watched
        let list = await watcher.set(wanted.map(\.name), stop: stop)
        let refused = stop ? [] : wanted.map(\.name).filter { !list.contains($0) && !before.contains($0) }
        let background = watcher.deliversInBackground
        return Output.json(Output.object([
            ("watched", .array(list.map { .string($0) })),
            ("not_added", refused.isEmpty ? nil : .array(refused.map { .string($0) })),
            ("background_delivery", .bool(background)),
            ("note", .string(background
                ? "New samples are saved on the phone as Apple Health reports them, also while Persona is in the background; a locked iPhone is read after its next unlock."
                : "This build has no HealthKit background delivery: the snapshots catch up when Persona opens or is woken for a call while the iPhone is unlocked.")),
        ]))
    }
}
#endif
