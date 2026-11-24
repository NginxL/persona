import Foundation
import HealthKit
import Testing
@testable import Persona
import PersonaWire

/// The generic Apple Health tools (#13740): the type catalog, anchors, the watched snapshot's merging, and what a
/// locked iPhone answers from it. Nothing here asks HealthKit for access or reads real data.
@MainActor
struct HealthRecordsTests {
    actor FakeSampleStore: HealthSampleStoring {
        var snapshots: [String: HealthSampleSnapshot] = [:]
        init(_ snapshots: [HealthSampleSnapshot] = []) {
            for snapshot in snapshots { self.snapshots[snapshot.name] = snapshot }
        }
        func load(_ name: String) -> HealthSampleSnapshot? { snapshots[name] }
        @discardableResult
        func save(_ snapshot: HealthSampleSnapshot) -> Bool {
            snapshots[snapshot.name] = snapshot
            return true
        }
        func remove(_ name: String) { snapshots[name] = nil }
    }

    private func decode(_ content: [ToolContent]) -> JSONValue? {
        guard let text = content.first?.text, let data = text.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(JSONValue.self, from: data)
    }

    private func sample(_ id: String, end: Date, bundle: String = "com.example.band") -> HealthStoredSample {
        HealthStoredSample(id: id, end: end, json: .object([
            "id": .string(id),
            "end": .string(ToolDate.string(end)),
            "value": .number(60),
            "source": .object(["name": .string("Band"), "bundle_id": .string(bundle)]),
            "device": .object(["name": .string("Band"), "manufacturer": .string("Example")]),
        ]))
    }

    // MARK: - Catalog

    @Test func everyNameIsUniqueAndLooksUpBothWays() {
        let names = HealthCatalog.entries.map(\.name)
        #expect(Set(names).count == names.count)
        for entry in HealthCatalog.entries {
            #expect(HealthCatalog.entry(named: entry.name) == entry)
            #expect(HealthCatalog.entry(named: entry.identifier) == entry)
            #expect(entry.name.allSatisfy { $0.isLowercase || $0.isNumber || $0 == "_" }, "\(entry.name)")
        }
        #expect(HealthCatalog.entry(named: "no_such_type") == nil)
        #expect(HealthCatalog.entry(named: "  ") == nil)
    }

    @Test func namesAreSnakeCaseOfTheIdentifier() {
        #expect(HealthCatalog.snakeName("StepCount") == "step_count")
        #expect(HealthCatalog.snakeName("VO2Max") == "vo2_max")
        #expect(HealthCatalog.snakeName("HeartRateVariabilitySDNN") == "heart_rate_variability_sdnn")
        #expect(HealthCatalog.snakeName("UVExposure") == "uv_exposure")
        #expect(HealthCatalog.snakeName("DietaryVitaminB6") == "dietary_vitamin_b6")
        #expect(HealthCatalog.entry(named: "HKQuantityTypeIdentifierHeartRate")?.name == "heart_rate")
    }

    @Test func coversEveryKindTheToolsPromise() {
        let kinds = Set(HealthCatalog.entries.map(\.kind))
        #expect(kinds == Set(HealthCatalog.Kind.allCases))
        // The types this iOS has build real HealthKit types; quantities and categories are most of the catalog.
        let quantities = HealthCatalog.entries.filter { $0.kind == .quantity }
        #expect(quantities.count > 100)
        #expect(quantities.filter { HealthCatalog.objectType($0) != nil }.count > 90)
        for name in ["step_count", "heart_rate", "sleep_analysis", "blood_pressure", "workout", "electrocardiogram", "audiogram"] {
            let entry = HealthCatalog.entry(named: name)
            #expect(entry.flatMap(HealthCatalog.objectType) != nil, "\(name)")
        }
    }

    @Test func correlationsAskForTheirPartsAndRecordsAreNeverAsked() throws {
        let pressure = try #require(HealthCatalog.entry(named: "blood_pressure"))
        let parts = HealthCatalog.authorizationTypes(pressure).map(\.identifier).sorted()
        #expect(parts == [HKQuantityTypeIdentifier.bloodPressureDiastolic.rawValue, HKQuantityTypeIdentifier.bloodPressureSystolic.rawValue])
        let food = try #require(HealthCatalog.entry(named: "food"))
        #expect(HealthCatalog.authorizationTypes(food).contains(HKQuantityType(.dietaryEnergyConsumed)))
        #expect(!HealthCatalog.authorizationTypes(food).contains { $0 is HKCorrelationType })
        for entry in HealthCatalog.entries where entry.kind == .clinical || entry.kind == .workoutRoute {
            #expect(HealthCatalog.unreadableReason(entry) != nil)
            #expect(HealthCatalog.authorizationTypes(entry).isEmpty)
            #expect(!HealthCatalog.readable().contains(entry))
        }
    }

    @Test func unitsAreMetricAndCompatible() throws {
        func unit(_ name: String) throws -> String? {
            let entry = try #require(HealthCatalog.entry(named: name))
            let type = try #require(HealthCatalog.objectType(entry) as? HKQuantityType)
            return HealthCatalog.unit(for: type)?.unitString
        }
        #expect(try unit("step_count") == "count")
        #expect(try unit("heart_rate") == "count/min")
        #expect(try unit("body_mass") == "kg")
        #expect(try unit("distance_walking_running") == "m")
        #expect(try unit("active_energy_burned") == "kcal")
        #expect(try unit("dietary_protein") == "g")
        #expect(try unit("oxygen_saturation") == "%")
        #expect(try unit("heart_rate_variability_sdnn") == "ms")
        #expect(try unit("body_temperature") == "degC")
        #expect(try unit("blood_glucose") == "mg/dL")
        // Every quantity this iOS has gets a unit, so values never come back without one.
        for entry in HealthCatalog.entries where entry.kind == .quantity {
            guard let type = HealthCatalog.objectType(entry) as? HKQuantityType else { continue }
            #expect(HealthCatalog.unit(for: type) != nil, "no unit for \(entry.name)")
        }
    }

    @Test func categoryLabels() throws {
        let sleep = try #require(HealthCatalog.entry(named: "sleep_analysis"))
        #expect(HealthCatalog.categoryLabel(sleep, value: HKCategoryValueSleepAnalysis.asleepREM.rawValue) == "asleep_rem")
        #expect(HealthCatalog.categoryLabel(sleep, value: 99) == nil)
        let headache = try #require(HealthCatalog.entry(named: "headache"))
        #expect(HealthCatalog.categoryLabel(headache, value: HKCategoryValueSeverity.moderate.rawValue) == "moderate")
        let mindful = try #require(HealthCatalog.entry(named: "mindful_session"))
        #expect(HealthCatalog.categoryLabel(mindful, value: 0) == nil)
    }

    // MARK: - Encoding

    @Test func aQuantitySampleCarriesValueUnitSourceAndMetadata() throws {
        let entry = try #require(HealthCatalog.entry(named: "heart_rate"))
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let sample = HKQuantitySample(type: HKQuantityType(.heartRate),
                                      quantity: HKQuantity(unit: .count().unitDivided(by: .minute()), doubleValue: 64),
                                      start: start, end: start,
                                      metadata: [HKMetadataKeyHeartRateMotionContext: 1, "Note": String(repeating: "x", count: 900),
                                                 HKMetadataKeyWasUserEntered: true])
        // An unsaved sample has no source revision yet: the kind's fields and the metadata are checked on their own.
        let json = Output.object(HealthSampleJSON.fields(sample, entry: entry, unit: nil))
        #expect(json["value"]?.double == 64)
        #expect(json["unit"]?.string == "count/min")
        #expect(json["count"] == nil)
        let metadata = try #require(HealthSampleJSON.metadata(sample.metadata))
        #expect(metadata["Note"]?.string?.count == HealthSampleJSON.metadataTextLimit)
        #expect(metadata[HKMetadataKeyWasUserEntered]?.bool == true)
        #expect(metadata[HKMetadataKeyHeartRateMotionContext]?.double == 1)
        #expect(HealthSampleJSON.metadata([:]) == nil)
        #expect(HealthSampleJSON.device(nil) == nil)
        #expect(HealthSampleJSON.device(HKDevice(name: "Band", manufacturer: "Example", model: nil, hardwareVersion: nil,
                                                 firmwareVersion: nil, softwareVersion: "2.1", localIdentifier: "secret",
                                                 udiDeviceIdentifier: "secret"))
                == .object(["name": .string("Band"), "manufacturer": .string("Example"), "software_version": .string("2.1")]))
    }

    @Test func aCorrelationListsItsReadings() throws {
        let entry = try #require(HealthCatalog.entry(named: "blood_pressure"))
        let now = Date()
        let mmHg = HKUnit.millimeterOfMercury()
        let systolic = HKQuantitySample(type: HKQuantityType(.bloodPressureSystolic), quantity: HKQuantity(unit: mmHg, doubleValue: 120), start: now, end: now)
        let diastolic = HKQuantitySample(type: HKQuantityType(.bloodPressureDiastolic), quantity: HKQuantity(unit: mmHg, doubleValue: 80), start: now, end: now)
        let correlation = HKCorrelation(type: try #require(HKObjectType.correlationType(forIdentifier: .bloodPressure)), start: now, end: now,
                                        objects: [systolic, diastolic])
        let objects = try #require(Output.object(HealthSampleJSON.fields(correlation, entry: entry, unit: nil))["objects"]?.array)
        #expect(Set(objects.compactMap { $0["type"]?.string }) == ["blood_pressure_systolic", "blood_pressure_diastolic"])
        #expect(Set(objects.compactMap { $0["value"]?.double }) == [120, 80])
        #expect(objects.allSatisfy { $0["unit"]?.string == "mmHg" })
    }

    // MARK: - Anchors

    @Test func anchorsRoundTripAndForeignOnesAreRefused() {
        let data = Data((0..<200).map { UInt8($0) })
        let text = HealthRecords.anchorText(data)
        #expect(text?.hasPrefix("a1.") == true)
        #expect(text?.contains("+") == false && text?.contains("/") == false && text?.contains("=") == false)
        #expect(text.flatMap(HealthRecords.anchorData) == data)
        #expect(HealthRecords.anchorData("garbage") == nil)
        #expect(HealthRecords.snapshotOffset("s1.20") == 20)
        #expect(HealthRecords.snapshotOffset("s1.-1") == nil)
        #expect(HealthRecords.snapshotOffset(text) == nil)
    }

    // MARK: - Snapshot

    @Test func mergingReplacesRemovesTrimsAndCaps() {
        let now = Date()
        let old = sample("old", end: now.addingTimeInterval(-10 * 86400))
        let a = sample("a", end: now.addingTimeInterval(-3600))
        let b = sample("b", end: now.addingTimeInterval(-1800))
        var a2 = sample("a", end: now.addingTimeInterval(-60))
        a2.json = .object(["id": .string("a"), "value": .number(70)])
        let c = sample("c", end: now)
        let merged = HealthSampleSnapshot.merged([old, a, b], added: [a2, c], deleted: ["b"], keepAfter: now.addingTimeInterval(-7 * 86400))
        #expect(merged.map(\.id) == ["a", "c"])
        #expect(merged.first?.json["value"]?.double == 70)
        let capped = HealthSampleSnapshot.merged([], added: (0..<10).map { sample("\($0)", end: now) }, deleted: [], keepAfter: .distantPast, maximum: 3)
        #expect(capped.map(\.id) == ["7", "8", "9"])
    }

    @Test func aLockedPhoneAnswersFromTheWatchedSnapshotInPages() async throws {
        let now = Date()
        let asOf = now.addingTimeInterval(-600)
        let samples = (0..<5).map { sample("s\($0)", end: now.addingTimeInterval(Double($0 - 5) * 3600)) }
        let store = FakeSampleStore([HealthSampleSnapshot(name: "heart_rate", asOf: asOf, anchor: nil, samples: samples)])
        let first = try #require(decode(try await HealthRecords.samples(
            .object(["type": .string("heart_rate"), "limit": .number(3)]), snapshots: store, isDataAvailable: { false }, now: now)))
        #expect(first["source"]?.string == "snapshot")
        #expect(first["as_of"]?.string == ToolDate.string(asOf))
        #expect(first["samples"]?.array?.compactMap { $0["id"]?.string } == ["s0", "s1", "s2"])
        #expect(first["more"]?.bool == true)
        let next = try #require(first["anchor"]?.string)
        let second = try #require(decode(try await HealthRecords.samples(
            .object(["type": .string("heart_rate"), "limit": .number(3), "anchor": .string(next)]),
            snapshots: store, isDataAvailable: { false }, now: now)))
        #expect(second["samples"]?.array?.compactMap { $0["id"]?.string } == ["s3", "s4"])
        #expect(second["more"]?.bool == false)
        #expect(second["anchor"] == nil)
        // A live anchor passed while locked is handed back unchanged, so incremental reads resume after the unlock.
        let live = try #require(HealthRecords.anchorText(Data([1, 2, 3])))
        let kept = try #require(decode(try await HealthRecords.samples(
            .object(["type": .string("heart_rate"), "anchor": .string(live)]), snapshots: store, isDataAvailable: { false }, now: now)))
        #expect(kept["anchor"]?.string == live)
        #expect(kept["samples"]?.array?.count == 5)
        // The window applies to the snapshot too.
        let windowed = try #require(decode(try await HealthRecords.samples(
            .object(["type": .string("heart_rate"), "start": .string(ToolDate.string(now.addingTimeInterval(-2.5 * 3600)))]),
            snapshots: store, isDataAvailable: { false }, now: now)))
        #expect(windowed["samples"]?.array?.compactMap { $0["id"]?.string } == ["s3", "s4"])
    }

    @Test func aLockedPhoneWithoutSnapshotSaysSoWithoutFailing() async throws {
        let answer = try #require(decode(try await HealthRecords.samples(
            .object(["type": .string("step_count")]), snapshots: FakeSampleStore(), isDataAvailable: { false })))
        #expect(answer["status"]?.string == "device_locked")
        #expect(answer["samples"]?.array?.isEmpty == true)
        let sources = try #require(decode(try await HealthRecords.sources(
            .object([:]), snapshots: FakeSampleStore(), isDataAvailable: { false }, watched: ["step_count"])))
        #expect(sources["status"]?.string == "device_locked")
    }

    @Test func unreadableTypesAnswerWithAReasonAndBadArgumentsFail() async throws {
        let route = try #require(decode(try await HealthRecords.samples(
            .object(["type": .string("workout_route")]), snapshots: FakeSampleStore(), isDataAvailable: { false })))
        #expect(route["access"]?.string == "unavailable")
        #expect(route["reason"]?.string?.isEmpty == false)
        await #expect(throws: ToolFailure.self) {
            _ = try await HealthRecords.samples(.object(["type": .string("nope")]), snapshots: FakeSampleStore(), isDataAvailable: { false })
        }
        await #expect(throws: ToolFailure.self) {
            _ = try await HealthRecords.samples(.object([:]), snapshots: FakeSampleStore(), isDataAvailable: { false })
        }
        #expect(throws: ToolFailure.self) { _ = try HealthRecords.typeList(.object(["types": .array([])])) }
        #expect(try HealthRecords.typeList(.object(["types": .array([.string("all")])])) == HealthCatalog.readable())
    }

    @Test func lockedSourcesComeFromTheSnapshots() async throws {
        let now = Date()
        let store = FakeSampleStore([
            HealthSampleSnapshot(name: "heart_rate", asOf: now, anchor: nil, samples: [sample("a", end: now), sample("b", end: now, bundle: "com.example.scale")]),
            HealthSampleSnapshot(name: "body_mass", asOf: now.addingTimeInterval(-60), anchor: nil, samples: [sample("c", end: now, bundle: "com.example.scale")]),
        ])
        let answer = try #require(decode(try await HealthRecords.sources(
            .object([:]), snapshots: store, isDataAvailable: { false }, watched: ["heart_rate", "body_mass"], now: now)))
        #expect(answer["source"]?.string == "snapshot")
        let rows = try #require(answer["sources"]?.array)
        #expect(rows.compactMap { $0["bundle_id"]?.string } == ["com.example.band", "com.example.scale"])
        #expect(rows.last?["types"]?.array?.compactMap(\.string) == ["heart_rate", "body_mass"])
        #expect(answer["devices"]?.array?.count == 1)
        #expect(answer["as_of"]?.string == ToolDate.string(now.addingTimeInterval(-60)))
    }

    @Test func theSampleStoreKeepsSnapshotsOnDisk() async throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: "health-samples-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = HealthSampleStore(directory: directory)
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(await store.load("heart_rate") == nil)
        #expect(await store.save(HealthSampleSnapshot(name: "heart_rate", asOf: now, anchor: Data([9]), samples: [sample("a", end: now)])))
        let reloaded = try #require(await HealthSampleStore(directory: directory).load("heart_rate"))
        #expect(reloaded.anchor == Data([9]))
        #expect(reloaded.samples.map(\.id) == ["a"])
        #expect(reloaded.asOf == now)
        let excluded = try directory.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup
        #expect(excluded == true)
        await store.remove("heart_rate")
        #expect(await store.load("heart_rate") == nil)
    }

    @Test func theWatchedListIsKeptAndCapped() async {
        let suite = "health-watch-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        // isProtectedDataAvailable false: adding a type never reads HealthKit in a test.
        let previous = HealthDataLock.isProtectedDataAvailable
        HealthDataLock.isProtectedDataAvailable = { false }
        defer { HealthDataLock.isProtectedDataAvailable = previous }
        let watcher = HealthSampleWatcher(store: FakeSampleStore(), defaults: defaults, registersObservers: false)
        #expect(await watcher.set(["heart_rate", "step_count", "heart_rate"], stop: false) == ["heart_rate", "step_count"])
        #expect(await watcher.set(["heart_rate"], stop: true) == ["step_count"])
        let many = HealthCatalog.readable(kind: .quantity).map(\.name)
        #expect(await watcher.set(many, stop: false).count == HealthSampleWatcher.maximumTypes)
    }
}
