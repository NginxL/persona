import Foundation
import Testing
@testable import Persona

/// The assistant and New Agent launcher requests replay the same candidates as the hub, web and Android (#4663).
@MainActor
struct LauncherPickerFixtureTests {
    private let box = MachineRef(id: "box")
    private let path = "/api/v1/machines/box/launchers"

    private struct Fixture: Decodable {
        var version: Int
        var cases: [Case]
    }
    private struct Case: Decodable {
        var name: String
        var candidates: [Launcher]
        var assistant: [String]
        var agentNew: [String]
        enum CodingKeys: String, CodingKey {
            case name, candidates, assistant
            case agentNew = "agent_new"
        }
    }
    private struct Wire: Encodable { var launchers: [Launcher] }

    private func shared() throws -> Case {
        let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let data = try Data(contentsOf: repo.appending(path: "spec/fixtures/assistant-launchers/cases.json"))
        let fixture = try JSONDecoder().decode(Fixture.self, from: data)
        #expect(fixture.version == 1 && fixture.cases.count == 1)
        return try #require(fixture.cases.first)
    }

    private func wire(_ launchers: [Launcher]) throws -> String {
        String(decoding: try JSONEncoder().encode(Wire(launchers: launchers)), as: UTF8.self)
    }

    @Test func sharedCandidatesHaveTheSameEligibleChoicesAndNoLegacyConcreteModel() throws {
        let shared = try shared()
        #expect(shared.candidates.map(\.name) == ["runtime", "codex-gw", "team-api"])
        #expect(shared.candidates.filter(\.runsAssistant).map(\.name) == shared.assistant)

        let response = APIClient.LaunchersResponse(launchers: shared.candidates, default: nil, unavailable: nil)
        let effective = AssistantModelChoice.Effective(launcher: "runtime", model: nil, effort: nil, source: .defaultValue)
        let shown = AssistantModelChoice.displayName(effective, response: response)
        #expect(shown == "runtime")
        #expect(shown?.contains("默认") == false)
    }

    @Test func mainAndStarterAskForAssistantWhileNewAgentKeepsTheGenericRequest() async throws {
        let shared = try shared()
        let hub = FakeHub()
        hub.on("GET", path) { req in
            let eligible = req.query["purpose"] == "assistant"
            let names = eligible ? shared.candidates.map(\.name) : shared.agentNew
            return try Data(self.wire(shared.candidates.filter { names.contains($0.name) }).utf8)
        }
        hub.on("GET", path + "/runtime/models", json: #"{"agent":"claude","models":[]}"#)
        let app = AppModel()
        app.apiForTests = hub.client

        let main = try await app.assistantLaunchers(box)
        #expect(main.launchers.filter(\.runsAssistant).map(\.name) == shared.assistant)
        #expect(hub.sent("GET", path).last?.query == ["purpose": "assistant"])

        let starter = AssistantStarterModels(deps: .init(launchers: { machine, manager, _ in
            try await hub.client.manager(manager).launcherList(machine: machine.id, purpose: "assistant")
        }, api: { _ in hub.client }, cache: AssistantStarterCache()))
        #expect(await starter.load(box, launcher: nil, fallback: "runtime"))
        #expect(starter.launchers.map(\.name) == shared.assistant)
        #expect(hub.sent("GET", path).last?.query == ["purpose": "assistant"])

        let agents = try await app.launchersForStart(box)
        #expect(agents.map(\.name) == shared.agentNew)
        #expect(hub.sent("GET", path).last?.query.isEmpty == true)
    }
}
