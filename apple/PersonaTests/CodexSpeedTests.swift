import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Codex speed in the iPhone main conversation (#10244): advertised choices, the hub-acknowledged tier, and its own
/// POST …/modes payload. Speed and thinking effort are independent; a summary never erases either speed field.
@MainActor struct CodexSpeedTests {
    private static let tiers = #","service_tiers":[{"id":"default","name":"Native Standard"},{"id":"priority","name":"Native Fast","description":"Increased usage"}]"#
    private static let modesPath = "/api/v1/sessions/box/s1/modes"

    private func session(_ extra: String = "", state: String = "idle", assistant: Bool = true) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"\#(state)","state":"\#(state)","assistant":\#(assistant),"agent":"codex","launcher":"codex","model":"gpt-5.5","effort":"medium","efforts":["low","medium","high"]\#(extra)}"#.utf8))
    }

    private func body(_ request: LocalRequest?) throws -> [String: String] {
        let data = try #require(request?.body)
        return try #require(try JSONSerialization.jsonObject(with: data) as? [String: String])
    }

    @Test func sessionReadsAdvertisedTiersAndTheAcknowledgedTier() throws {
        let s = try session(Self.tiers + #","service_tier":"priority""#)
        #expect(s.serviceTier == "priority")
        #expect(s.serviceTiers?.map(\.id) == ["default", "priority"])
        #expect(s.serviceTiers?.map(\.name) == ["Native Standard", "Native Fast"])
        #expect(s.serviceTiers?.last?.description == "Increased usage")

        let olderHub = try session()
        #expect(olderHub.serviceTier == nil)
        #expect(olderHub.serviceTiers == nil)
    }

    @Test func summaryUpdatesPreserveBothSpeedFields() throws {
        let full = try session(Self.tiers + #","service_tier":"priority""#)
        let summary = try session(#","waiting":false"#, state: "working")
        let merged = summary.preservingDetails(from: full)
        #expect(merged.serviceTier == "priority")
        #expect(merged.serviceTiers?.map(\.id) == ["default", "priority"])
        #expect(merged.serviceTiers?.last?.description == "Increased usage")
        #expect(merged.state == .working)
    }

    @Test func speedIsMainConversationOnlyAndUsesPlainLocalizedLabels() throws {
        let s = try session(Self.tiers)
        let options = SessionModes.speedOptions(s, mainConversation: true)
        #expect(options.map(\.id) == ["default", "priority"])
        #expect(options.map(\.title) == [String(localized: "Standard"), String(localized: "Fast")])
        #expect(options.last?.detail.isEmpty == false, "Fast briefly explains its increased quota use")
        #expect(!options.map(\.title).contains("Native Standard"))
        #expect(!options.map(\.title).contains("Native Fast"))

        #expect(SessionModes.speedOptions(s, mainConversation: false).isEmpty, "another assistant conversation has no Speed action")
        #expect(SessionModes.speedOptions(try session(Self.tiers, assistant: false), mainConversation: true).isEmpty)
        #expect(SessionModes.speedOptions(try session(), mainConversation: true).isEmpty, "no advertised tiers, no inferred Speed action")
        #expect(SessionModes.speedOptions(try session(#","service_tiers":[]"#), mainConversation: true).isEmpty)
        #expect(SessionModes.speedOptions(nil, mainConversation: true).isEmpty)
    }

    @Test func workingSpeedAppliesNextTurnAndAnUnavailableActionSaysWhyInOneLine() throws {
        #expect(SessionModes.speedUnavailable(try session(Self.tiers, state: "working")) == nil)
        let offline = try #require(SessionModes.speedUnavailable(try session(Self.tiers, state: "offline")))
        #expect(!offline.isEmpty)
        #expect(!offline.contains("\n"))
    }

    @Test func speedRequestsAreSeparateAndContainOnlyServiceTier() async throws {
        let hub = FakeHub()
        hub.on("POST", Self.modesPath, json: #"{"ok":true}"#)
        let key = SessionKey(machineID: "box", sessionID: "s1")
        try await hub.client.setServiceTier(key, tier: "priority")
        try await hub.client.setServiceTier(key, tier: "default")
        try await hub.client.setModes(key, effort: "high")
        try await hub.client.setModes(key, model: "gpt-5.5")

        let sent = hub.sent("POST", Self.modesPath)
        #expect(sent.count == 4)
        #expect(try body(sent[0]) == ["service_tier": "priority"])
        #expect(try body(sent[1]) == ["service_tier": "default"])
        #expect(try body(sent[2]) == ["effort": "high"], "the older effort payload stays independent")
        #expect(try body(sent[3]) == ["model": "gpt-5.5"], "the older model payload stays independent")
    }

    @Test func acknowledgmentOwnsTheSelectionAndEffortLeavesSpeedAlone() async throws {
        let hub = FakeHub()
        hub.on("POST", Self.modesPath, json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        let ref = MachineRef(group: "", id: "box")
        let key = SessionKey(machineID: "box", sessionID: "s1")
        model.applySessions([ref: [try session(Self.tiers + #","service_tier":"default""#)]])

        #expect(await model.setServiceTier(key, tier: "priority"))
        #expect(model.session(key)?.serviceTier == "default", "an HTTP success is not a native acknowledgment")
        #expect(model.session(key)?.effort == "medium")
        #expect(model.session(key)?.model == "gpt-5.5")

        model.applySessions([ref: [try session(Self.tiers + #","service_tier":"priority""#)]])
        #expect(model.session(key)?.serviceTier == "priority", "the session push owns the checkmark")
        #expect(await model.setModes(key, effort: "high"))
        #expect(model.session(key)?.serviceTier == "priority")
        #expect(model.session(key)?.effort == "high")
        #expect(model.session(key)?.model == "gpt-5.5")
    }

    @Test(arguments: ["refusal", "timeout"])
    func aFailedChangeIsVisibleAndKeepsTheLastAcknowledgedTier(_ failure: String) async throws {
        let hub = FakeHub()
        hub.on("POST", Self.modesPath) { _ in
            if failure == "timeout" { throw URLError(.timedOut) }
            throw APIError(status: 409, message: "Mock speed refusal")
        }
        let model = AppModel()
        model.apiForTests = hub.client
        let ref = MachineRef(group: "", id: "box")
        let key = SessionKey(machineID: "box", sessionID: "s1")
        model.applySessions([ref: [try session(Self.tiers + #","service_tier":"priority""#)]])

        #expect(await !model.setServiceTier(key, tier: "default"))
        #expect(model.session(key)?.serviceTier == "priority")
        #expect(model.session(key)?.effort == "medium")
        #expect(model.session(key)?.model == "gpt-5.5")
        #expect(model.toast?.style == .error, "\(failure) must be visible even though ordinary background timeouts are quiet")
        #expect(model.toast?.message.isEmpty == false)
        #expect(try body(hub.sent("POST", Self.modesPath).first) == ["service_tier": "default"])
    }
}
