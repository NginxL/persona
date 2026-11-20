import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Reads a decoded stored field without making the untouched client fail to compile. The wire field is the contract;
/// `tiersProfile` is the expected Swift spelling of `tiers_profile`.
enum TierWireProbe {
    private static func unwrapped(_ value: Any) -> Any? {
        let mirror = Mirror(reflecting: value)
        guard mirror.displayStyle == .optional else { return value }
        guard let wrapped = mirror.children.first?.value else { return nil }
        return unwrapped(wrapped)
    }

    static func field(_ names: [String], in value: Any) -> Any? {
        let mirror = Mirror(reflecting: value)
        if mirror.displayStyle == .optional {
            guard let wrapped = mirror.children.first?.value else { return nil }
            return field(names, in: wrapped)
        }
        for name in names {
            guard let child = mirror.children.first(where: { $0.label == name })?.value else { continue }
            return unwrapped(child)
        }
        return nil
    }

    static func field(_ names: String..., in value: Any) -> Any? {
        field(names, in: value)
    }

    static func rows(_ names: String..., in value: Any) -> [Any]? {
        guard let field = field(names, in: value) else { return nil }
        return Mirror(reflecting: field).children.map(\.value)
    }

    static func string(_ names: String..., in value: Any) -> String? {
        field(names, in: value) as? String
    }
}

/// New Agent's Agent presets (#2225), from GET /machines/{mid}/launchers to the unchanged POST /sessions body.
/// Selection, manual edits and compatibility explanations are driven through the real form in AgentPresetUITests.
@MainActor struct AgentPresetTests {
    private func response(_ json: String) throws -> APIClient.LaunchersResponse {
        try JSON.decoder.decode(APIClient.LaunchersResponse.self, from: Data(json.utf8))
    }

    private func startBody(launcher: String, model: String, effort: String) throws -> [String: Any] {
        let request = APIClient.StartRequest(machineID: "macbook", cwd: "/Users/me/code/blog", launcher: launcher,
                                             effort: effort, model: model, async: true)
        return try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(request)) as? [String: Any])
    }

    @Test func decodesTheActiveProfilesNamedTiers() throws {
        let decoded = try response("""
        {"launchers":[{"name":"claude"},{"name":"sonnetcc"}],
         "default":{"provider":"launcher.claude","model":"model-chat"},
         "tiers":[
           {"name":"quick","launcher":"sonnetcc","description":"Small, quick fixes"},
           {"name":"review","launcher":"claude","model":"opus","effort":"high","description":"Code review and design"}
         ],
         "tiers_profile":{"id":"performance","name":"Performance"}}
        """)
        let tiers = try #require(TierWireProbe.rows("tiers", in: decoded), "the launchers response must retain tiers")
        #expect(tiers.count == 2)
        #expect(tiers.compactMap { TierWireProbe.string("name", in: $0) } == ["quick", "review"])
        #expect(TierWireProbe.string("launcher", in: tiers[0]) == "sonnetcc")
        #expect(TierWireProbe.string("description", in: tiers[0]) == "Small, quick fixes")
        #expect(TierWireProbe.string("model", in: tiers[0]) == nil)
        #expect(TierWireProbe.string("effort", in: tiers[0]) == nil)
        #expect(TierWireProbe.string("launcher", in: tiers[1]) == "claude")
        #expect(TierWireProbe.string("model", in: tiers[1]) == "opus")
        #expect(TierWireProbe.string("effort", in: tiers[1]) == "high")
        #expect(TierWireProbe.string("description", in: tiers[1]) == "Code review and design")
        let profile = try #require(TierWireProbe.field("tiersProfile", "tiers_profile", in: decoded),
                                   "the active profile accompanies its tiers")
        #expect(TierWireProbe.string("id", in: profile) == "performance")
        #expect(TierWireProbe.string("name", in: profile) == "Performance")
    }

    @Test func anOlderHubKeepsTheExistingDefaultsAndHasNoPresetOffering() throws {
        let decoded = try response(#"{"launchers":[{"name":"claude","model":"opus"}],"default":{"provider":"launcher.claude","model":"model-chat"}}"#)
        #expect(decoded.launchers.map(\.name) == ["claude"])
        #expect(decoded.default?.launcher == "claude")
        #expect(decoded.default?.model == "model-chat")
        #expect(TierWireProbe.field("tiers", in: decoded) == nil)
        #expect(TierWireProbe.field("tiersProfile", "tiers_profile", in: decoded) == nil)
    }

    @Test func theAPIClientReadsTiersForNewAgent() async throws {
        let hub = FakeHub()
        let path = "/api/v1/machines/macbook/launchers"
        hub.on("GET", path, json: #"{"launchers":[{"name":"claude"}],"tiers":[{"name":"review","launcher":"claude","model":"opus","effort":"high"}],"tiers_profile":{"id":"performance","name":"Performance"}}"#)
        let decoded = try await hub.client.launcherList(machine: "macbook")
        let tiers = try #require(TierWireProbe.rows("tiers", in: decoded))
        #expect(TierWireProbe.string("name", in: tiers[0]) == "review")
        #expect(TierWireProbe.string("model", in: tiers[0]) == "opus")
        #expect(TierWireProbe.string("effort", in: tiers[0]) == "high")
        #expect(TierWireProbe.string("name", in: try #require(TierWireProbe.field("tiersProfile", "tiers_profile", in: decoded))) == "Performance")
        let request = try #require(hub.sent("GET", path).first)
        #expect(request.query.isEmpty, "no purpose query keeps the endpoint's agent.new default")
    }

    /// A preset fills exactly the fields a manual pick would. In particular, an explicit `default` is still a model:
    /// it must not collapse to nil when Model Settings would otherwise choose another model.
    @Test(arguments: [("claude", "opus", "high"), ("claude", "default", "low")])
    func presetFieldsUseTheUnchangedStartRequest(launcher: String, model: String, effort: String) throws {
        let body = try startBody(launcher: launcher, model: model, effort: effort)
        #expect(body["machine_id"] as? String == "macbook")
        #expect(body["cwd"] as? String == "/Users/me/code/blog")
        #expect(body["launcher"] as? String == launcher)
        #expect(body["model"] as? String == model)
        #expect(body["effort"] as? String == effort)
        #expect(body["async"] as? Bool == true)
        #expect(body["tier"] == nil)
        #expect(body["tiers"] == nil)
        #expect(body["tiers_profile"] == nil)
    }
}
