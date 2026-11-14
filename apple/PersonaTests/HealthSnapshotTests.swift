import Foundation
import HealthKit
import Testing
@testable import Persona
import PersonaWire

/// The locked-phone health snapshot: what gets written while the device is
/// unlocked, and what `phone_health_summary` answers while it is locked.
@MainActor
struct HealthSnapshotTests {
    /// In-memory store so the locked paths need neither HealthKit nor the real
    /// Application Support directory.
    actor FakeHealthSnapshotStore: HealthSnapshotStoring {
        var snapshot: HealthSnapshot?
        var fresh: HealthSnapshot?
        private(set) var saved: [HealthSnapshot] = []

        func load() -> HealthSnapshot? { snapshot }
        @discardableResult
        func save(_ snapshot: HealthSnapshot) -> Bool {
            saved.append(snapshot)
            self.snapshot = snapshot
            return true
        }
        func cachedIfFresh(now: Date) -> HealthSnapshot? { fresh }
        func markRefreshed(at date: Date) {}
    }

    private let healthAskedKey = "phoneAuth.health.asked"

    private func grantHealthInSettings() {
        // HealthAccess.requireAccess only reads this cached flag, so it is safe to flip in a unit test.
        UserDefaults.standard.set(true, forKey: healthAskedKey)
    }

    private func decode(_ content: [ToolContent]) -> JSONValue? {
        guard let text = content.first?.text, let data = text.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(JSONValue.self, from: data)
    }

    private func makeSnapshot(asOf: Date, calendar: Calendar = .current) -> HealthSnapshot {
        let zone = calendar.timeZone
        let today = ToolDate.day(asOf, timeZone: zone)
        let yesterday = ToolDate.day(calendar.date(byAdding: .day, value: -1, to: asOf) ?? asOf, timeZone: zone)
        let old = ToolDate.day(calendar.date(byAdding: .day, value: -30, to: asOf) ?? asOf, timeZone: zone)
        return HealthSnapshot(
            asOf: asOf,
            windowDays: 90,
            metrics: ["steps", "sleep", "heart_rate"],
            rows: [
                today: [
                    "steps": .number(1200),
                    "heart_rate": .object([
                        "average": .number(72),
                        "min": .number(58),
                        "max": .number(110),
                    ]),
                ],
                yesterday: ["steps": .number(3400), "sleep": .number(7.5)],
                old: ["steps": .number(9000)],
            ],
            timeZone: zone.identifier
        )
    }

    // MARK: - Snapshot response shaping

    @Test func responseUsesSnapshotTimeAndMarksOnlyItsDayPartial() throws {
        let asOf = Date(timeIntervalSince1970: 1_760_000_000)
        let now = asOf.addingTimeInterval(6 * 60 * 60)
        let calendar = Calendar.current
        let answer = try #require(makeSnapshot(asOf: asOf, calendar: calendar).response(
            metrics: ["steps"], days: 7, now: now, calendar: calendar
        ))
        guard case .object(let object) = answer else { Issue.record("answer is not an object"); return }
        #expect(object["source"] == .string("snapshot"))
        #expect(object["as_of"] == .string(ToolDate.string(asOf)))
        #expect(object["end"] == .string(ToolDate.string(asOf)))
        let expectedStart = calendar.date(byAdding: .day, value: -6, to: asOf) ?? asOf
        #expect(object["start"] == .string(ToolDate.string(calendar.startOfDay(for: expectedStart))))
        #expect(object["days"] == .number(7))
        #expect(object["metrics"] == .array([.string("steps")]))
        guard case .array(let data) = object["data"] else { Issue.record("no data rows"); return }
        #expect(data.count == 2) // snapshot day and yesterday are inside the 7-day window; the 30-day-old row is not
        var snapshotDayRows = 0
        for case .object(let row) in data {
            #expect(row["steps"] != nil, "only the requested metric is carried into a row")
            #expect(row["sleep"] == nil && row["heart_rate"] == nil)
            if row["date"] == .string(ToolDate.day(asOf)) {
                snapshotDayRows += 1
                #expect(row["partial"] == .bool(true))
            } else {
                #expect(row["partial"] == nil)
            }
        }
        #expect(snapshotDayRows == 1)
    }

    @Test func responsePassesHeartRateObjectsThrough() throws {
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let answer = try #require(makeSnapshot(asOf: now).response(metrics: ["heart_rate"], days: 7, now: now))
        guard case .object(let object) = answer, case .array(let data) = object["data"],
              case .object(let row)? = data.first, case .object(let heartRate)? = row["heart_rate"] else {
            Issue.record("heart_rate row missing")
            return
        }
        #expect(heartRate["average"] == .number(72))
        #expect(heartRate["min"] == .number(58))
        #expect(heartRate["max"] == .number(110))
    }

    @Test func responseIsNilWhenNothingCoversTheWindow() {
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let calendar = Calendar(identifier: .gregorian)
        let snapshot = HealthSnapshot(asOf: now, windowDays: 90, metrics: ["steps"], rows: [
            ToolDate.day(calendar.date(byAdding: .day, value: -30, to: now) ?? now): ["steps": .number(10)],
        ])
        #expect(snapshot.response(metrics: ["steps"], days: 7, now: now, calendar: calendar) == nil)
    }

    @Test func responseRejectsSnapshotsOlderThanFortyEightHours() {
        let asOf = Date(timeIntervalSince1970: 1_760_000_000)
        let snapshot = makeSnapshot(asOf: asOf)
        #expect(snapshot.response(
            metrics: ["steps"], days: 7, now: asOf.addingTimeInterval(HealthSnapshot.maximumAge)
        ) != nil)
        #expect(snapshot.response(
            metrics: ["steps"], days: 7, now: asOf.addingTimeInterval(HealthSnapshot.maximumAge + 1)
        ) == nil)
    }

    @Test func responseKeepsSnapshotDayAndPartialAcrossTimeZones() throws {
        var savingCalendar = Calendar(identifier: .gregorian)
        let savingZone = try #require(TimeZone(secondsFromGMT: 14 * 60 * 60))
        savingCalendar.timeZone = savingZone
        let asOf = try #require(savingCalendar.date(from: DateComponents(
            year: 2026, month: 11, day: 14, hour: 0, minute: 30
        )))
        let snapshot = makeSnapshot(asOf: asOf, calendar: savingCalendar)
        var readingCalendar = Calendar(identifier: .gregorian)
        readingCalendar.timeZone = try #require(TimeZone(secondsFromGMT: -10 * 60 * 60))

        let answer = try #require(snapshot.response(
            metrics: ["steps"], days: 7, now: asOf.addingTimeInterval(6 * 60 * 60), calendar: readingCalendar
        ))
        guard case .object(let object) = answer, case .array(let data) = object["data"] else {
            Issue.record("snapshot rows missing")
            return
        }
        #expect(object["end"] == .string(ToolDate.string(asOf, timeZone: savingZone)))
        #expect(data.count == 2)
        var snapshotDayRows = 0
        for case .object(let row) in data {
            if row["date"] == .string(ToolDate.day(asOf, timeZone: savingZone)) {
                snapshotDayRows += 1
                #expect(row["partial"] == .bool(true))
            } else {
                #expect(row["partial"] == nil)
            }
        }
        #expect(snapshotDayRows == 1)
    }

    // MARK: - Locked phone paths

    @Test func lockedPhoneServesTheSnapshotWithoutTouchingHealthKit() async throws {
        let wasAsked = UserDefaults.standard.bool(forKey: healthAskedKey)
        defer { UserDefaults.standard.set(wasAsked, forKey: healthAskedKey) }
        grantHealthInSettings()
        let now = Date()
        let store = FakeHealthSnapshotStore()
        await store.set(snapshot: makeSnapshot(asOf: now))

        let content = try await HealthTool.summary(
            .object(["metrics": .array([.string("steps")]), "days": .number(7)]),
            snapshotStore: store,
            isDataAvailable: { false }
        )
        let answer = try #require(decode(content))
        guard case .object(let object) = answer else { Issue.record("answer is not an object"); return }
        #expect(object["source"] == .string("snapshot"))
        #expect(object["as_of"] == .string(ToolDate.string(now)))
    }

    @Test func lockedPhoneWithoutSnapshotSaysDeviceLocked() async {
        let wasAsked = UserDefaults.standard.bool(forKey: healthAskedKey)
        defer { UserDefaults.standard.set(wasAsked, forKey: healthAskedKey) }
        grantHealthInSettings()
        await #expect(throws: ToolFailure.self) {
            _ = try await HealthTool.summary(
                .object(["days": .number(7)]),
                snapshotStore: FakeHealthSnapshotStore(),
                isDataAvailable: { false }
            )
        }
        do {
            _ = try await HealthTool.summary(
                .object(["days": .number(7)]),
                snapshotStore: FakeHealthSnapshotStore(),
                isDataAvailable: { false }
            )
        } catch {
            let text = ToolErrors.describe(error)
            #expect(text.contains("device_locked"))
            #expect(!text.contains("Protected health data is inaccessible"), "the raw English HKError must not reach the fields")
        }
    }

    @Test func lockedPhoneWithExpiredSnapshotSaysDeviceLocked() async {
        let wasAsked = UserDefaults.standard.bool(forKey: healthAskedKey)
        defer { UserDefaults.standard.set(wasAsked, forKey: healthAskedKey) }
        grantHealthInSettings()
        let store = FakeHealthSnapshotStore()
        let asOf = Date().addingTimeInterval(-HealthSnapshot.maximumAge - 60)
        await store.set(snapshot: makeSnapshot(asOf: asOf))
        do {
            _ = try await HealthTool.summary(
                .object(["days": .number(7)]),
                snapshotStore: store,
                isDataAvailable: { false }
            )
            Issue.record("an expired snapshot must not produce a snapshot answer")
        } catch {
            #expect(ToolErrors.describe(error).contains("device_locked"))
        }
    }

    @Test func databaseInaccessibleIsRecognized() {
        #expect(HealthTool.isDatabaseInaccessible(HKError(.errorDatabaseInaccessible)))
        #expect(!HealthTool.isDatabaseInaccessible(HKError(.errorInvalidArgument)))
        #expect(!HealthTool.isDatabaseInaccessible(ToolFailure(.unavailable, "other")))
    }

    // MARK: - On-disk store

    @Test func storeRoundTripsWithALockSafeProtectionLevel() async throws {
        let directory = FileManager.default.temporaryDirectory
            .appending(path: "health-snapshot-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = HealthSnapshotStore(directory: directory)
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let snapshot = makeSnapshot(asOf: now)

        await store.save(snapshot)
        let loaded = try #require(await store.load())
        #expect(loaded.asOf == snapshot.asOf)
        #expect(loaded.rows == snapshot.rows)
        #expect(loaded.timeZone == snapshot.timeZone)

        // The simulator does not implement data protection: attributesOfItem never reports
        // .protectionKey there, so the level can only be verified on a physical device. The
        // round-trip above still proves the file is written and read back on every platform.
        #if !targetEnvironment(simulator)
        let files = FileManager.default
        let file = directory.appending(path: "summary.json")
        let fileProtection = try files.attributesOfItem(atPath: file.path)[.protectionKey] as? FileProtectionType
        let dirProtection = try files.attributesOfItem(atPath: directory.path)[.protectionKey] as? FileProtectionType
        #expect(fileProtection == .completeUntilFirstUserAuthentication)
        #expect(dirProtection == .completeUntilFirstUserAuthentication)
        #endif
    }

    @Test func storeExcludesDirectoryAndFileFromBackupOnEverySave() async throws {
        let directory = FileManager.default.temporaryDirectory
            .appending(path: "health-snapshot-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appending(path: "summary.json")
        let store = HealthSnapshotStore(directory: directory)
        let snapshot = makeSnapshot(asOf: Date(timeIntervalSince1970: 1_760_000_000))
        await store.save(snapshot)

        // Simulate files left by a build that did not exclude either location.
        var values = URLResourceValues()
        values.isExcludedFromBackup = false
        var writableDirectory = directory
        try writableDirectory.setResourceValues(values)
        var writableFile = file
        try writableFile.setResourceValues(values)

        await store.save(snapshot)
        #expect(try directory.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        #expect(try file.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
    }

    @Test func storeReturnsFalseWhenSnapshotCannotBeWritten() async throws {
        let blocker = FileManager.default.temporaryDirectory
            .appending(path: "health-snapshot-blocker-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: blocker) }
        try Data().write(to: blocker)
        let store = HealthSnapshotStore(directory: blocker.appending(path: "health-snapshot", directoryHint: .isDirectory))

        let saved = await store.save(makeSnapshot(asOf: Date(timeIntervalSince1970: 1_760_000_000)))
        #expect(!saved)
        #expect(await store.load() == nil)
    }

    @Test func freshSnapshotSkipsARefreshAndAgedOneDoesNot() async throws {
        let directory = FileManager.default.temporaryDirectory
            .appending(path: "health-snapshot-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = HealthSnapshotStore(directory: directory)
        let now = Date()
        let snapshot = makeSnapshot(asOf: now)
        await store.save(snapshot)

        await store.markRefreshed(at: now.addingTimeInterval(-60))
        #expect(await store.cachedIfFresh(now: now)?.asOf == snapshot.asOf)

        await store.markRefreshed(at: now.addingTimeInterval(-HealthSnapshotStore.refreshInterval - 60))
        #expect(await store.cachedIfFresh(now: now) == nil)
    }
}

private extension HealthSnapshotTests.FakeHealthSnapshotStore {
    func set(snapshot: HealthSnapshot) { self.snapshot = snapshot }
}
