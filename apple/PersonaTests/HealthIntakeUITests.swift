import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct HealthIntakeUITests {
    @Test func reservedRoutesKeepHealthOnTheDefaultManager() async throws {
        let hub = FakeHub()
        hub.on("POST", HealthIntakeUI.path, json: #"{"view":{"title":"Water","sections":[],"state":"opaque-server-state"}}"#)
        hub.on("POST", SubscriptionsUI.path, json: #"{"view":{"sections":[]}}"#)
        for api in [hub.client.showing("another-manager"), hub.client.manager("another-manager")] {
            for kind in [HealthIntake.Kind.water, .protein] {
                _ = try await api.pluginUI(HealthIntakeUI.id, HealthIntakeUI.page, context: .here().forPage(),
                                          state: HealthIntakeUI.state(kind), manager: "explicit-manager", assistant: "Sample")
            }
        }
        #expect(ReservedExtensionUI.paths[HealthIntakeUI.id] == HealthIntakeUI.path)
        #expect(ReservedExtensionUI.paths[SubscriptionsUI.id] == SubscriptionsUI.path)
        #expect(hub.requests.count == 4)
        for (index, request) in hub.requests.enumerated() {
            #expect(request.path == HealthIntakeUI.path)
            #expect(request.query == ["space": "health"])
            #expect(request.headers[APIClient.managerHeader] == nil)
            let body = try JSON.decoder.decode(JSONValue.self, from: #require(request.body))
            #expect(body["state"]?.string == HealthIntakeUI.state(index.isMultiple(of: 2) ? .water : .protein))
            #expect(body["context"]?["assistant"] == nil)
        }
        _ = try await hub.client.pluginUI(SubscriptionsUI.id, SubscriptionsUI.page, context: .here(), state: nil,
                                         manager: "subscription-manager", assistant: "Sample")
        let subscription = try #require(hub.requests.last)
        #expect(subscription.query["manager"] == "subscription-manager")
        let sent = try JSON.decoder.decode(JSONValue.self, from: #require(subscription.body))
        #expect(sent["context"]?["assistant"]?.string == "Sample")
    }

    @Test func pagesUseHTTPAndReturnTheServersStateUnchanged() async throws {
        let hub = FakeHub()
        hub.on("POST", HealthIntakeUI.path, json: #"{"view":{"title":"Water","sections":[],"state":"{\"kind\":\"water\",\"days\":30,\"cursor\":\"opaque\"}"}}"#)
        let page = ExtensionViewModel(plugin: HealthIntakeUI.id, ui: HealthIntakeUI.page, context: .here().forPage(),
                                      state: HealthIntakeUI.state(.water),
                                      deps: .init(api: { hub.client }, connection: { LiveConnection() }))
        #expect(page.stream == nil)
        await page.load()
        _ = await page.perform(.init(id: "range"), with: ["days": "30"])
        let request = try #require(hub.sent("POST", HealthIntakeUI.path).last)
        let sent = try JSON.decoder.decode(JSONValue.self, from: #require(request.body))
        #expect(sent["state"]?.string == #"{"kind":"water","days":30,"cursor":"opaque"}"#)
        #expect(sent["action"]?.string == "range")
        #expect(sent["values"]?["days"]?.string == "30")
    }

    @Test func intakeBroadcastInvalidatesPagesOnlyForItsHub() async throws {
        let hub = FakeHub()
        hub.on("POST", HealthIntakeUI.path, json: #"{"view":{"title":"Before","sections":[]}}"#)
        let page = ExtensionViewModel(plugin: HealthIntakeUI.id, ui: HealthIntakeUI.page, deps: .init(api: { hub.client }))
        await page.load()
        let other = ExtensionViewModel(plugin: SubscriptionsUI.id, ui: SubscriptionsUI.page, deps: .init(api: { hub.client }))
        let folder = FileManager.default.temporaryDirectory.appending(path: "intake-ui-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let model = AppModel(stateFolder: folder)
        let event = try #require(ServerEvent.decode(Data(#"{"type":"health_intake"}"#.utf8)))
        model.handle(event, group: "another-hub")
        #expect(page.invalidationVersion == 0)
        model.handle(event, group: "")
        #expect(page.invalidationVersion == 1)
        #expect(page.stale)
        #expect(other.invalidationVersion == 0)
        hub.on("POST", HealthIntakeUI.path, json: #"{"view":{"title":"After","sections":[]}}"#)
        await page.refresh()
        #expect(page.view?.title == "After")
        #expect(hub.sent("POST", HealthIntakeUI.path).count == 2)
        #expect(!page.stale)
    }
}
