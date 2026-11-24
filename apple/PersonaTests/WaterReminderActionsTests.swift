import Foundation
import PersonaWire
import Testing
import UserNotifications
@testable import Persona

struct WaterReminderActionsTests {
    private let path = "/api/v1/assistant/health/water/reminder"

    @Test func categoryRegistersThreeBackgroundButtonsAndTextInput() throws {
        let category = try #require(NotificationActions.categories.first { $0.identifier == "HEALTH_WATER" })
        #expect(category.actions.map(\.identifier) == ["HEALTH_WATER_DRINK", "HEALTH_WATER_SNOOZE", "HEALTH_WATER_LOG"])
        #expect(category.actions.allSatisfy { !$0.options.contains(.foreground) })
        let input = try #require(category.actions.last as? UNTextInputNotificationAction)
        #expect(input.textInputPlaceholder == String(localized: "Millilitres, for example 300"))
        #expect(!input.textInputButtonTitle.isEmpty)
    }

    @Test(arguments: [("HEALTH_WATER_DRINK", "drink"), ("HEALTH_WATER_SNOOZE", "snooze"), ("HEALTH_WATER_LOG", "log")])
    func eachButtonSendsItsExactRequest(identifier: String, action: String) async throws {
        let hub = FakeHub()
        hub.on("POST", path, json: #"{"toast":"Sample result"}"#)
        let request = try #require(WaterReminderActions.request(category: "HEALTH_WATER", action: identifier,
                                  info: ["health": ["reminder": "sample-reminder"]], text: "300 ml"))
        let answer = try await WaterReminderDelivery().send(request, api: hub.client.showing("another-manager"))
        #expect(answer == "Sample result")
        let sent = try #require(hub.sent("POST", path).first)
        let body = try JSON.decoder.decode(JSONValue.self, from: #require(sent.body))
        #expect(body["reminder"]?.string == "sample-reminder")
        #expect(body["action"]?.string == action)
        #expect(body["amount"]?.string == (action == "log" ? "300 ml" : nil))
        #expect(sent.query == ["space": "health"])
        #expect(sent.headers[APIClient.managerHeader] == nil)
    }

    @Test func duplicateCallbacksAndChangedTextSendOnceButOtherButtonsStillWork() async throws {
        let hub = FakeHub()
        hub.on("POST", path, json: #"{"toast":"Recorded"}"#)
        let delivery = WaterReminderDelivery()
        let request = WaterReminderActions.Request(reminder: "sample", action: "log", amount: "300")
        async let a = delivery.send(request, api: hub.client)
        async let b = delivery.send(request, api: hub.client)
        let results = try await [a, b]
        #expect(results.compactMap { $0 }.count == 1)
        #expect(try await delivery.send(.init(reminder: "sample", action: "log", amount: "400"), api: hub.client) == nil)
        #expect(hub.sent("POST", path).count == 1)
        _ = try await delivery.send(.init(reminder: "sample", action: "drink"), api: hub.client)
        var other = hub.client
        other.group = "other"
        _ = try await delivery.send(request, api: other)
        #expect(hub.sent("POST", path).count == 3)
    }

    @Test func failedInputDoesNotConsumeTheButton() async throws {
        let hub = FakeHub()
        let delivery = WaterReminderDelivery()
        hub.on("POST", path) { _ in throw APIError(status: 400, message: "Sample invalid amount") }
        do { _ = try await delivery.send(.init(reminder: "sample", action: "log", amount: "invalid"), api: hub.client) }
        catch { #expect((error as? APIError)?.status == 400) }
        hub.on("POST", path, json: #"{"toast":"Recorded"}"#)
        #expect(try await delivery.send(.init(reminder: "sample", action: "log", amount: "300"), api: hub.client) == "Recorded")
        #expect(hub.sent("POST", path).count == 2)
    }

    @Test func malformedPushAndOrdinaryTapsNeverBecomeWrites() {
        for action in [UNNotificationDefaultActionIdentifier, UNNotificationDismissActionIdentifier, "OTHER"] {
            #expect(WaterReminderActions.request(category: "HEALTH_WATER", action: action, info: ["health": ["reminder": "sample"]], text: nil) == nil)
        }
        for reminder in ["", " padded", "bad\nvalue", String(repeating: "a", count: 129)] {
            #expect(WaterReminderActions.request(category: "HEALTH_WATER", action: WaterReminderActions.drink, info: ["health": ["reminder": reminder]], text: nil) == nil)
        }
        #expect(WaterReminderActions.request(category: "OTHER", action: WaterReminderActions.drink, info: ["health": ["reminder": "sample"]], text: nil) == nil)
    }

    @Test func originNeverFallsBackAcrossHubs() {
        #expect(WaterReminderOrigin([:]).group(hubGroups: [:], groups: ["one"]) == "one")
        #expect(WaterReminderOrigin([:]).group(hubGroups: [:], groups: ["one", "two"]) == nil)
        #expect(WaterReminderOrigin(["hub_id": "unknown"]).group(hubGroups: [:], groups: ["one"]) == nil)
        #expect(WaterReminderOrigin(["hub_id": "hub", PushCrypto.groupKey: "two"]).group(hubGroups: ["hub": "one"], groups: ["one", "two"]) == nil)
        #expect(WaterReminderOrigin(["hub_id": "hub"]).group(hubGroups: ["hub": "one"], groups: ["one", "two"]) == "one")
    }
}
