import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct HealthIntakeTests {
    private let path = "/api/v1/assistant/health/intake"
    private let id = "00000000-0000-4000-8000-000000000099"
    private let snapshot = #"{"water":{"today":750,"goal":2000,"entries":[]},"protein":{"today":36,"goal":60,"entries":[]},"days":[{"date":"2026-11-23","water":750,"protein":36}],"zone":"UTC"}"#

    private func change(water: Double = 1050, protein: Double = 36, deleted: Bool = false) -> Data {
        let tombstone = deleted ? #","deleted_at":"2026-11-23T09:00:00Z""# : ""
        return Data("""
        {"entry":{"id":"\(id)","kind":"water","amount":300,"at":"2026-11-23T08:00:00Z","note":"","seq":4,
        "created_at":"2026-11-23T08:00:01Z"\(tombstone)},"today":{"water":\(water),"protein":\(protein)},"zone":"UTC"}
        """.utf8)
    }

    private func setup(_ hub: FakeHub) {
        hub.on("GET", path, json: snapshot)
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"m","conversations":[]}"#)
        hub.on("GET", "/api/v1/assistant/memory", json: #"{"files":[{"name":"allergy.md","content":"Sample"}]}"#)
        hub.on("GET", "/api/v1/machines/m/health/reports", json: #"{"reports":[]}"#)
    }

    private func deps(_ hub: FakeHub, toast: @escaping (Toast) -> Void = { _ in }) -> AssistantDeps {
        AssistantDeps(api: { hub.client.showing("another-manager") }, report: { _ in }, toast: toast, open: { _ in })
    }

    private func settle(_ condition: () -> Bool) async {
        let deadline = Date.now.addingTimeInterval(5)
        while !condition(), Date.now < deadline { await Task.yield() }
        #expect(condition())
    }

    @Test func loadsTotalsAlongsideTheOtherHealthReadsAndGatesOnHelloFeature() async {
        let hub = FakeHub()
        setup(hub)
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m", features: [HealthIntake.feature])
        #expect(page.supportsIntake)
        #expect(page.intake?.water.today == 750)
        #expect(page.intake?.protein.goal == 60)
        #expect(page.memoryCount == 1)
        #expect(page.reportCount == 0)
        #expect(page.loaded)
        await page.load(machine: "m", features: [])
        #expect(!page.supportsIntake)
        #expect(page.intake == nil)
        #expect(hub.sent("GET", path).count == 1, "an old hub is never probed by status code")
    }

    @Test func theFourReadsStartConcurrently() async {
        let hub = FakeHub()
        setup(hub)
        var waiting: [CheckedContinuation<Void, Never>] = []
        var released = false
        for route in [path, "/api/v1/assistant", "/api/v1/assistant/memory", "/api/v1/machines/m/health/reports"] {
            let json = route == path ? snapshot : route.hasSuffix("memory") ? #"{"files":[]}"# : route.hasSuffix("reports") ? #"{"reports":[]}"# : #"{"machine_id":"m","conversations":[]}"#
            hub.onAsync("GET", route) { _ in
                if !released { await withCheckedContinuation { waiting.append($0) } }
                return Data(json.utf8)
            }
        }
        let page = HealthModel(deps: deps(hub))
        let work = Task { await page.load(machine: "m", features: [HealthIntake.feature]) }
        await settle { waiting.count == 4 }
        released = true
        waiting.forEach { $0.resume() }
        await work.value
        #expect(page.intake?.water.today == 750)
    }

    @Test func defaultGlassOmitsAmountUpdatesImmediatelyAndAlignsToTheHub() async throws {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.on("POST", path) { req in
            #expect(page.intake?.water.today == 1000, "optimistic feedback exists before the hub answers")
            #expect(page.intakeBusy)
            let body = try JSON.decoder.decode(JSONValue.self, from: #require(req.body))
            #expect(body["amount"] == nil, "the hub's configured cup decides the actual amount")
            #expect(UUID(uuidString: body["id"]?.string ?? "") != nil)
            return change()
        }
        await page.record(.water)
        #expect(page.intake?.water.today == 1050)
        #expect(!page.intakeBusy)
        #expect(toast?.action?.title == String(localized: "Undo"))
        #expect(page.intake?.water.goal == 2000)
    }

    @Test func customAmountsAndNewActionsHaveTheirOwnUUIDs() async throws {
        let hub = FakeHub()
        setup(hub)
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m", features: [HealthIntake.feature])
        var drafts: [JSONValue] = []
        hub.on("POST", path) { req in
            drafts.append(try JSON.decoder.decode(JSONValue.self, from: #require(req.body)))
            return change()
        }
        await page.record(.water, amount: 125.5)
        await page.record(.protein, amount: 12.3)
        #expect(drafts.map { $0["amount"]?.double } == [125.5, 12.3])
        #expect(drafts.map { $0["kind"]?.string } == ["water", "protein"])
        #expect(drafts[0]["id"] != drafts[1]["id"])
    }

    @Test func failureRollsBackSaysWhyAndRetryKeepsTheExactRequest() async throws {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        var bodies: [Data] = []
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.on("POST", path) { req in
            bodies.append(try #require(req.body))
            if bodies.count == 1 { throw APIError(status: 503, message: "Sample storage is unavailable") }
            return change()
        }
        await page.record(.water)
        #expect(page.intake?.water.today == 750)
        #expect(toast?.message == "Sample storage is unavailable")
        #expect(toast?.action?.title == String(localized: "Try Again"))
        try #require(toast?.action).run()
        await settle { bodies.count == 2 && !page.intakeBusy }
        let first = try JSON.decoder.decode(JSONValue.self, from: bodies[0])
        let second = try JSON.decoder.decode(JSONValue.self, from: bodies[1])
        #expect(first == second, "UUID, amount and timestamp survive an uncertain write")
        #expect(page.intake?.water.today == 1050)
    }

    @Test func undoUsesDELETEAndFailureKeepsTheLastConfirmedTotal() async throws {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.on("POST", path) { _ in change() }
        await page.record(.water)
        let draft = try JSON.decoder.decode(JSONValue.self, from: #require(hub.sent("POST", path).first?.body))
        let identity = try #require(draft["id"]?.string)
        hub.on("DELETE", path + "/" + identity) { _ in change(water: 750) }
        try #require(toast?.action).run()
        await settle { hub.sent("DELETE", path + "/" + identity).count == 1 && !page.intakeBusy }
        #expect(page.intake?.water.today == 750)
        hub.on("DELETE", path + "/" + identity) { _ in throw APIError(status: 403, message: "Sample permission denied") }
        await page.undo(identity)
        #expect(page.intake?.water.today == 750)
        #expect(toast?.message == "Sample permission denied")
        #expect(toast?.action != nil)
    }

    @Test func aTombstonedRetryKeepsTheHubTotalAndSaysItWasUndone() async {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.on("POST", path) { _ in change(water: 750, deleted: true) }
        await page.record(.water)
        #expect(page.intake?.water.today == 750)
        #expect(toast?.message == String(localized: "Intake undone"))
        #expect(toast?.action == nil)
    }

    @Test func undoFromTheFirstToastWaitsForTheSecondPOSTAndPreservesItsTotal() async throws {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        var release: CheckedContinuation<Void, Never>?
        var posts = 0
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.onAsync("POST", path) { _ in
            posts += 1
            if posts == 2 { await withCheckedContinuation { release = $0 } }
            return change(water: posts == 1 ? 1050 : 1350)
        }
        await page.record(.water)
        let undo = try #require(toast?.action)
        let first = try JSON.decoder.decode(JSONValue.self, from: #require(hub.sent("POST", path).first?.body))
        let deletePath = path + "/" + (try #require(first["id"]?.string))
        hub.on("DELETE", deletePath) { _ in change(water: 1050) }
        let second = Task { await page.record(.water) }
        await settle { release != nil }
        #expect(page.intake?.water.today == 1350)
        undo.run() // The toast consumes this click while POST B is still suspended.
        #expect(hub.sent("DELETE", deletePath).isEmpty)
        #expect(page.intakeBusy)
        release?.resume()
        await second.value
        await settle { !page.intakeBusy }
        #expect(hub.sent("DELETE", deletePath).count == 1)
        #expect(hub.requests.filter { ["POST", "DELETE"].contains($0.method) }.map(\.method) == ["POST", "POST", "DELETE"])
        #expect(page.intake?.water.today == 1050, "only A is undone; B remains in the authoritative total")
        #expect(toast?.message == String(localized: "Intake undone"))
    }

    @Test(arguments: [false, true]) func recordRetryWaitsForAnotherWriteAndRollsBackToItsConfirmedTotal(failsAgain: Bool) async throws {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        var release: CheckedContinuation<Void, Never>?
        var bodies: [Data] = []
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.onAsync("POST", path) { request in
            bodies.append(try #require(request.body))
            if bodies.count == 1 { throw APIError(status: 503, message: "Sample write failed") }
            if bodies.count == 2 {
                await withCheckedContinuation { release = $0 }
                return change(water: 1050)
            }
            if failsAgain { throw APIError(status: 503, message: "Sample retry failed") }
            return change(water: 1350)
        }
        await page.record(.water)
        let retry = try #require(toast?.action)
        let second = Task { await page.record(.water) }
        await settle { release != nil }
        retry.run()
        #expect(bodies.count == 2, "retry waits behind the in-flight write")
        release?.resume()
        await second.value
        await settle { !page.intakeBusy }
        #expect(bodies.count == 3)
        let retried = try #require(bodies.last)
        #expect(try JSON.decoder.decode(JSONValue.self, from: bodies[0]) == JSON.decoder.decode(JSONValue.self, from: retried))
        #expect(page.intake?.water.today == (failsAgain ? 1050 : 1350))
        #expect(toast?.action?.title == String(localized: failsAgain ? "Try Again" : "Undo"))
    }

    @Test func undoRetryWaitsForAnotherRecord() async throws {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        var release: CheckedContinuation<Void, Never>?
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.on("POST", path) { _ in change() }
        await page.record(.water)
        let first = try JSON.decoder.decode(JSONValue.self, from: #require(hub.sent("POST", path).first?.body))
        let identity = try #require(first["id"]?.string)
        let deletePath = path + "/" + identity
        var deletes = 0
        hub.on("DELETE", deletePath) { _ in
            deletes += 1
            if deletes == 1 { throw APIError(status: 503, message: "Sample undo failed") }
            return change(water: 1050)
        }
        await page.undo(identity)
        let retry = try #require(toast?.action)
        hub.onAsync("POST", path) { _ in
            await withCheckedContinuation { release = $0 }
            return change(water: 1350)
        }
        let second = Task { await page.record(.water) }
        await settle { release != nil }
        retry.run()
        #expect(deletes == 1)
        release?.resume()
        await second.value
        await settle { !page.intakeBusy }
        #expect(deletes == 2)
        #expect(page.intake?.water.today == 1050)
    }

    @Test func everyIntakeVerbUsesHealthAndDropsTheSelectedOrExplicitManager() async throws {
        let hub = FakeHub()
        setup(hub)
        hub.on("POST", path) { _ in change() }
        hub.on("DELETE", path + "/" + id) { _ in change(water: 750) }
        for api in [hub.client.showing("another-manager"), hub.client.manager("another-manager")] {
            _ = try await api.healthIntake()
            _ = try await api.recordHealthIntake(.init(id: id, kind: .water))
            _ = try await api.undoHealthIntake(id)
        }
        #expect(hub.requests.count == 6)
        for request in hub.requests {
            #expect(request.query["space"] == "health")
            #expect(request.query["manager"] == nil)
            #expect(request.headers[APIClient.managerHeader] == nil)
        }
    }

    @Test func aBroadcastDuringAWritingDefersItsReadUntilTheWriteSettles() async {
        let hub = FakeHub()
        setup(hub)
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.onAsync("POST", path) { _ in
            await page.load(machine: "m", features: [HealthIntake.feature])
            #expect(hub.sent("GET", path).count == 1, "no old GET replaces the optimistic total")
            #expect(page.intake?.water.today == 1000)
            return change()
        }
        await page.record(.water)
        #expect(page.intakeRefresh == 1, "the foreground task must perform the pending broadcast read")
        #expect(!page.intakeBusy)
        hub.on("GET", path, json: snapshot.replacingOccurrences(of: "750", with: "1300"))
        await page.load(machine: "m", features: [HealthIntake.feature])
        #expect(page.intake?.water.today == 1300, "another device's newer total is read after the write")
    }

    @Test func thePayloadFreeBroadcastInvalidatesOnlyItsHub() throws {
        let event = try #require(ServerEvent.decode(Data(#"{"type":"health_intake"}"#.utf8)))
        guard case .healthIntake = event else { Issue.record("missing intake broadcast"); return }
        let folder = FileManager.default.temporaryDirectory.appending(path: "health-intake-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let model = AppModel(stateFolder: folder)
        let hub = FakeHub()
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        let revision = model.healthIntakeRevision
        model.handle(event, group: "not-the-current-hub")
        #expect(model.healthIntakeRevision == revision)
        model.handle(event, group: "")
        #expect(model.healthIntakeRevision == revision + 1)
    }
}
