import Foundation
import HealthKit
import Testing
@testable import Persona
import PersonaWire

private actor IntakePermitGate {
    var pause = false
    var waiting: CheckedContinuation<Void, Never>?
    func pauseNext() { pause = true }
    func release() { waiting?.resume(); waiting = nil }
    func allowed() async -> Bool {
        if pause {
            pause = false
            await withCheckedContinuation { waiting = $0 }
        }
        return true
    }
}

private actor IntakeWriterFake: HealthIntakeWriting {
    var applied: [Int64] = []
    var deleted: [String] = []
    var failure: Int64?
    var authorizations = 0
    var denied: [HealthIntake.Kind] = []
    var pause = false
    var waiting: CheckedContinuation<Void, Never>?
    func authorize() { authorizations += 1 }
    func deniedKinds() -> [HealthIntake.Kind] { denied }
    func fail(_ seq: Int64?) { failure = seq }
    func pauseNext() { pause = true }
    func release() { waiting?.resume(); waiting = nil }
    func apply(_ entry: HealthIntake.Entry) async throws {
        if entry.seq == failure { throw HealthIntakeSyncError.permission }
        if pause {
            pause = false
            await withCheckedContinuation { waiting = $0 }
        }
        applied.append(entry.seq)
        if entry.deletedAt != nil { deleted.append(entry.id) }
    }
}

struct HealthIntakeSyncTests {
    private func paused(_ writer: IntakeWriterFake) async {
        let deadline = Date.now.addingTimeInterval(5)
        while await writer.waiting == nil, Date.now < deadline { await Task.yield() }
        #expect(await writer.waiting != nil)
    }
    private func entry(_ seq: Int64, deleted: Bool = false) -> HealthIntake.Entry {
        .init(id: "00000000-0000-4000-8000-000000000001", kind: .water, amount: 250,
              at: "2026-01-02T12:00:00.123Z", note: "", seq: seq, createdAt: "2026-01-02T12:00:01Z",
              deletedAt: deleted ? "2026-01-02T12:01:00Z" : nil)
    }

    @Test func failureStopsAtTheEntryAndRetryResumesWithoutSkippingTombstones() async {
        let writer = IntakeWriterFake()
        let engine = HealthIntakeSyncEngine(writer: writer, file: nil)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        await writer.fail(3)
        let entries = [entry(1), entry(3), entry(7, deleted: true)]
        let fetch: @Sendable (Int64) async throws -> HealthIntake.Changes = { after in
            let remaining = entries.filter { $0.seq > after }
            return .init(changes: remaining, next: remaining.last?.seq ?? after, hasMore: false)
        }
        let failed = await engine.refresh(group: "a", fetch: fetch, allowed: { true })
        #expect(failed.cursor == 1)
        #expect(failed.lastError != nil)
        #expect(failed.lastSynced == nil)
        #expect(await writer.applied == [1])
        await writer.fail(nil)
        let retried = await engine.refresh(group: "a", fetch: fetch, allowed: { true })
        #expect(retried.cursor == 7)
        #expect(retried.lastError == nil)
        #expect(retried.lastSynced != nil)
        #expect(await writer.applied == [1, 3, 7])
        #expect(await writer.deleted == [entries[2].id])
    }

    @Test func paginationUsesNextAndPersistsAcrossLaunches() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let file = folder.appending(path: "state.json")
        let writer = IntakeWriterFake()
        let engine = HealthIntakeSyncEngine(writer: writer, file: file)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        let first = entry(4), second = entry(19, deleted: true)
        let result = await engine.refresh(group: "a", fetch: { after in
            if after == 0 { return .init(changes: [first], next: 4, hasMore: true) }
            #expect(after == 4)
            return .init(changes: [second], next: 19, hasMore: false)
        }, allowed: { true })
        #expect(result.cursor == 19)
        let restarted = HealthIntakeSyncEngine(writer: writer, file: file)
        #expect(await restarted.snapshot().cursor == 19)
        let flags = try file.resourceValues(forKeys: [.isExcludedFromBackupKey])
        #expect(flags.isExcludedFromBackup == true)
        // As in HealthSnapshotTests, Simulator does not expose file-protection attributes.
        #if !targetEnvironment(simulator)
        let attributes = try FileManager.default.attributesOfItem(atPath: file.path)
        #expect(attributes[.protectionKey] as? FileProtectionType == .complete)
        #endif
    }

    @Test func aDifferentHubIsIgnoredAndExplicitOptInStartsItsHistoryAtZero() async {
        let writer = IntakeWriterFake()
        let engine = HealthIntakeSyncEngine(writer: writer, file: nil)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        let ignored = await engine.refresh(group: "b", fetch: { _ in
            Issue.record("another hub must never be read")
            return .init(changes: [], next: 0, hasMore: false)
        }, allowed: { true })
        #expect(ignored.group == "a")
        _ = await engine.setEnabled(true, group: "b", allowed: { true })
        let optedIn = await engine.refresh(group: "b", fetch: { after in
            #expect(after == 0)
            return .init(changes: [], next: after, hasMore: false)
        }, allowed: { true })
        #expect(optedIn.group == "b")
    }

    @Test func aLostCursorReplaysFromTheBeginning() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let file = folder.appending(path: "state.json")
        try JSON.encoder.encode(HealthIntakeSyncEngine.State(enabled: true, group: "a")).write(to: file)
        let engine = HealthIntakeSyncEngine(writer: IntakeWriterFake(), file: file)
        let record = entry(8)
        let result = await engine.refresh(group: "a", fetch: { after in
            #expect(after == 0)
            return .init(changes: [record], next: 8, hasMore: false)
        }, allowed: { true })
        #expect(result.cursor == 8)
    }

    @Test func anotherHubInTheSameGroupCannotInheritConsentOrCursor() async {
        let engine = HealthIntakeSyncEngine(writer: IntakeWriterFake(), file: nil)
        _ = await engine.setEnabled(true, group: "mesh", hubID: "first", allowed: { true })
        let unchanged = await engine.refresh(group: "mesh", hubID: "second", fetch: { _ in
            Issue.record("a group is not a hub identity")
            return .init(changes: [], next: 0, hasMore: false)
        }, allowed: { true })
        #expect(unchanged.hubID == "first")
        _ = await engine.setEnabled(true, group: "mesh", hubID: "second", allowed: { true })
        let second = await engine.refresh(group: "mesh", hubID: "second", fetch: { after in
            #expect(after == 0)
            return .init(changes: [], next: 0, hasMore: false)
        }, allowed: { true })
        #expect(second.hubID == "second")
    }

    @Test func lockedDemoAndDisabledDoNoWork() async {
        let writer = IntakeWriterFake()
        let engine = HealthIntakeSyncEngine(writer: writer, file: nil)
        _ = await engine.setEnabled(true, group: "a", allowed: { false })
        #expect(await writer.authorizations == 0)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        let neverFetch: @Sendable (Int64) async throws -> HealthIntake.Changes = { after in
            Issue.record("locked or disabled sync must not fetch")
            return .init(changes: [], next: after, hasMore: false)
        }
        _ = await engine.refresh(group: "a", fetch: neverFetch, allowed: { false })
        _ = await engine.setEnabled(false, group: "a", allowed: { true })
        _ = await engine.refresh(group: "a", fetch: neverFetch, allowed: { true })
        #expect(await writer.applied.isEmpty)
        #expect(!HealthIntakeSync.isVisible(isPhone: true, isDemo: true, supportsIntake: true))
        #expect(!HealthIntakeSync.isVisible(isPhone: false, isDemo: false, supportsIntake: true))
        #expect(!HealthIntakeSync.isVisible(isPhone: true, isDemo: false, supportsIntake: false))
        #expect(HealthIntakeSync.isVisible(isPhone: true, isDemo: false, supportsIntake: true))
    }

    @Test func invalidPagesCannotAdvanceTheCursor() async {
        let writer = IntakeWriterFake()
        let engine = HealthIntakeSyncEngine(writer: writer, file: nil)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        let record = entry(9)
        let result = await engine.refresh(group: "a", fetch: { _ in
            .init(changes: [record], next: 10, hasMore: false)
        }, allowed: { true })
        #expect(result.cursor == 0)
        #expect(result.lastError != nil)
        #expect(await writer.applied.isEmpty)
    }

    @Test func concurrentTriggersCoalesceAndDisableStopsBeforeTheNextEntry() async {
        let writer = IntakeWriterFake()
        let engine = HealthIntakeSyncEngine(writer: writer, file: nil)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        await writer.pauseNext()
        let entries = [entry(1), entry(2)]
        let fetch: @Sendable (Int64) async throws -> HealthIntake.Changes = { after in
            let records = entries.filter { $0.seq > after }
            return .init(changes: records, next: records.last?.seq ?? after, hasMore: false)
        }
        let first = Task { await engine.refresh(group: "a", fetch: fetch, allowed: { true }) }
        await paused(writer)
        _ = await engine.refresh(group: "a", fetch: fetch, allowed: { true })
        _ = await engine.setEnabled(false, group: "a", allowed: { true })
        await writer.release()
        let state = await first.value
        #expect(!state.enabled)
        #expect(state.cursor == 0, "an in-flight write cannot commit a checkpoint after disable")
        #expect(await writer.applied == [1], "no second HealthKit operation after disable")
    }

    @Test func reEnablingDuringAnOldRunBackfillsTheNewHub() async {
        let writer = IntakeWriterFake()
        let engine = HealthIntakeSyncEngine(writer: writer, file: nil)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        await writer.pauseNext()
        let a = entry(1), b = entry(8)
        let first = Task { await engine.refresh(group: "a", fetch: { _ in .init(changes: [a], next: 1, hasMore: false) }, allowed: { true }) }
        await paused(writer)
        _ = await engine.setEnabled(true, group: "b", allowed: { true })
        let next = Task { await engine.refresh(group: "b", fetch: { after in
            #expect(after == 0)
            return .init(changes: [b], next: 8, hasMore: false)
        }, allowed: { true }) }
        await writer.release()
        _ = await first.value
        let result = await next.value
        #expect(result.group == "b")
        #expect(result.cursor == 8)
        #expect(await writer.applied == [1, 8])
    }

    @Test func disablingDuringTheMainActorGateCannotStartAWrite() async {
        let writer = IntakeWriterFake(), gate = IntakePermitGate()
        let engine = HealthIntakeSyncEngine(writer: writer, file: nil)
        _ = await engine.setEnabled(true, group: "a", allowed: { true })
        let record = entry(1)
        let run = Task {
            await engine.refresh(group: "a", fetch: { _ in
                await gate.pauseNext()
                return .init(changes: [record], next: 1, hasMore: false)
            }, allowed: { await gate.allowed() })
        }
        let deadline = Date.now.addingTimeInterval(5)
        while await gate.waiting == nil, Date.now < deadline { await Task.yield() }
        #expect(await gate.waiting != nil)
        _ = await engine.setEnabled(false, group: "a", allowed: { true })
        await gate.release()
        let state = await run.value
        #expect(!state.enabled)
        #expect(state.cursor == 0)
        #expect(await writer.applied.isEmpty)
    }

    @MainActor @Test func healthKitMetadataAndUnitsAreStableAcrossReplays() throws {
        let water = try HealthIntakeHealthStore.sample(entry(1))
        #expect(water.quantity.doubleValue(for: .literUnit(with: .milli)) == 250)
        #expect(water.startDate == water.endDate)
        #expect(abs(water.startDate.timeIntervalSince1970 - 1767355200.123) < 0.001)
        #expect(water.metadata?[HKMetadataKeySyncIdentifier] as? String == entry(1).id)
        #expect(water.metadata?[HKMetadataKeySyncVersion] as? Int == 1)
        #expect(water.metadata?[HKMetadataKeyWasUserEntered] as? Bool == true)
        var protein = entry(2)
        protein.kind = .protein
        protein.amount = 12.3
        let sample = try HealthIntakeHealthStore.sample(protein)
        #expect(sample.quantityType == HKQuantityType(.dietaryProtein))
        #expect(sample.quantity.doubleValue(for: .gram()) == 12.3)
        #expect(!HealthTool.writeTypes.contains(HKQuantityType(.dietaryWater)))
        #expect(!HealthTool.writeTypes.contains(HKQuantityType(.dietaryProtein)))
    }

    @MainActor @Test func changesRequestUsesTheDefaultManagerAndHealthSpace() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/health/intake/changes", json: #"{"changes":[],"next":27,"has_more":false}"#)
        let result = try await hub.client.showing("another-manager").healthIntakeChanges(after: 27, limit: 2)
        #expect(result.next == 27)
        let sent = try #require(hub.requests.last)
        #expect(sent.query == ["space": "health", "after": "27", "limit": "2"])
        #expect(sent.headers[APIClient.managerHeader] == nil)
    }
}
