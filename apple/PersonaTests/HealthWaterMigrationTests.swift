import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct HealthWaterMigrationTests {
    private let path = "/api/v1/assistant/health/intake"

    private func snapshot(pending: Bool = true, water: Int = 750) -> String {
        let flag = pending ? #","water_extension":"pending""# : ""
        return """
        {"water":{"today":\(water),"goal":2000,"entries":[]},"protein":{"today":36,"goal":60,"entries":[]},"days":[],"zone":"UTC"\(flag)}
        """
    }

    private func setup(_ hub: FakeHub, pending: Bool = true) {
        hub.on("GET", path, json: snapshot(pending: pending))
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"m","conversations":[]}"#)
        hub.on("GET", "/api/v1/assistant/memory", json: #"{"files":[]}"#)
        hub.on("GET", "/api/v1/machines/m/health/reports", json: #"{"reports":[]}"#)
    }

    private func deps(_ hub: FakeHub, toast: @escaping (Toast) -> Void = { _ in }) -> AssistantDeps {
        AssistantDeps(api: { hub.client }, report: { _ in }, toast: toast, open: { _ in })
    }

    @Test(arguments: [false, true]) func onlyPendingShowsTheCard(pending: Bool) async {
        let hub = FakeHub()
        setup(hub, pending: pending)
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m", features: [HealthIntake.feature])
        #expect(page.showsWaterMigration == pending)
    }

    @Test func demoNeverShowsOrImportsEvenIfThePayloadSaysPending() async {
        let hub = FakeHub()
        setup(hub)
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m", features: [HealthIntake.feature], isDemo: true)
        #expect(page.intake?.waterExtension == "pending")
        #expect(!page.showsWaterMigration)
        await page.importWaterExtension()
        #expect(hub.sent("POST", path + "/import").isEmpty)
    }

    @Test func successfulImportRefreshesTodayRemovesTheCardAndExplainsWhatToReplace() async {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        var invalidations = 0
        var dependencies = deps(hub, toast: { toast = $0 })
        dependencies.intakeChanged = { invalidations += 1 }
        let page = HealthModel(deps: dependencies)
        await page.load(machine: "m", features: [HealthIntake.feature])
        #expect(hub.sent("POST", path + "/import").isEmpty, "loading never imports")
        hub.on("POST", path + "/import") { _ in
            hub.on("GET", path, json: snapshot(pending: false, water: 1200))
            return Data(#"{"imported":2,"extension_disabled":true,"settings":{}}"#.utf8)
        }
        await page.importWaterExtension()
        #expect(page.intake?.water.today == 1200)
        #expect(!page.showsWaterMigration)
        #expect(!page.intakeBusy && !page.waterImportBusy)
        #expect(hub.sent("GET", path).count == 2)
        #expect(invalidations == 1)
        #expect(toast?.style == .success)
        let count = 2
        #expect(toast?.message == String(localized: "Moved \(count) records; water extension disabled. Replace widgets, watch faces and shortcuts."))
    }

    @Test func failureKeepsTheCardAndReasonAndAllowsRetry() async {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.on("POST", path + "/import") { _ in throw APIError(status: 409, message: "Sample extension could not be stopped") }
        await page.importWaterExtension()
        #expect(page.showsWaterMigration)
        #expect(page.intake?.water.today == 750)
        #expect(toast?.message == "Sample extension could not be stopped")
        #expect(toast?.style == .error)
        #expect(!page.intakeBusy && !page.waterImportBusy)
        hub.on("POST", path + "/import") { _ in
            hub.on("GET", path, json: snapshot(pending: false, water: 1200))
            return Data(#"{"imported":2,"extension_disabled":true}"#.utf8)
        }
        await page.importWaterExtension()
        #expect(!page.showsWaterMigration)
        #expect(hub.sent("POST", path + "/import").count == 2)
    }

    @Test func anInFlightImportRejectsRepeatedTapsAndDefersBroadcastReads() async throws {
        let hub = FakeHub()
        setup(hub)
        var release: CheckedContinuation<Void, Never>?
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.onAsync("POST", path + "/import") { _ in
            await withCheckedContinuation { release = $0 }
            hub.on("GET", path, json: snapshot(pending: false, water: 1200))
            return Data(#"{"imported":2,"extension_disabled":true}"#.utf8)
        }
        let first = Task { await page.importWaterExtension() }
        let deadline = Date.now.addingTimeInterval(5)
        while release == nil, Date.now < deadline { await Task.yield() }
        let continuation = try #require(release)
        #expect(page.intakeBusy && page.waterImportBusy)
        await page.importWaterExtension()
        await page.load(machine: "m", features: [HealthIntake.feature])
        #expect(hub.sent("POST", path + "/import").count == 1)
        #expect(hub.sent("GET", path).count == 1)
        continuation.resume()
        await first.value
        #expect(!page.waterImportBusy && !page.intakeBusy)
        #expect(page.intakeRefresh == 1)
        #expect(page.intake?.water.today == 1200)
        #expect(!page.showsWaterMigration)
    }

    @Test func importUsesHealthAndTheDefaultManagerForEveryClientScope() async throws {
        let hub = FakeHub()
        hub.on("POST", path + "/import", json: #"{"imported":0,"extension_disabled":true,"settings":{}}"#)
        for api in [hub.client.showing("another-manager"), hub.client.manager("another-manager")] {
            let result = try await api.importWaterExtension()
            #expect(result.imported == 0)
        }
        #expect(hub.requests.count == 2)
        for request in hub.requests {
            #expect(request.query["space"] == "health")
            #expect(request.query["manager"] == nil)
            #expect(request.headers[APIClient.managerHeader] == nil)
            #expect(request.body == nil)
        }
    }

    @Test func aRefreshFailureDoesNotUndoACompletedImport() async {
        let hub = FakeHub()
        setup(hub)
        var toast: Toast?
        let page = HealthModel(deps: deps(hub, toast: { toast = $0 }))
        await page.load(machine: "m", features: [HealthIntake.feature])
        hub.on("POST", path + "/import") { _ in
            hub.on("GET", path) { _ in throw APIError(status: 503, message: "Sample refresh unavailable") }
            return Data(#"{"imported":2,"extension_disabled":true}"#.utf8)
        }
        await page.importWaterExtension()
        #expect(!page.showsWaterMigration)
        #expect(page.intakeError == "Sample refresh unavailable")
        #expect(page.intakeRefresh == 1)
        #expect(toast?.style == .success)
    }
}
