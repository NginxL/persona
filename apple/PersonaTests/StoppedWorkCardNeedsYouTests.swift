import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Resolving the only pending work card lets the hub's next session snapshot take the Agent out of Needs you (#6839).
@MainActor
struct StoppedWorkCardNeedsYouTests {
    private let question = "模型请求失败了，要怎么处理？" // i18n-ignore: hub-provided fixture text

    private func session(state: String) -> Session {
        let json = #"{"machine_id":"m","session_id":"s","title":"Agent","cwd":"/tmp/work","status":"idle","state":""# + state +
            #"","source_kind":"claude","capabilities":{"approve":false}}"#
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    private func card() throws -> Interaction {
        let option = try JSON.decoder.decode(QuestionOption.self, from: Data(#"{"label":"稍后再说","action":"defer"}"#.utf8)) // i18n-ignore
        return Interaction(
            id: "work", machineID: "m", sessionID: "s", sessionTitle: nil, cwd: nil,
            kind: .question, createdAt: Date(timeIntervalSince1970: 1), expiresAt: nil, toolName: "persona_triage",
            toolUseID: nil, questions: [Question(question: question, header: nil, multiSelect: false, options: [option])],
            plan: nil, toolInput: nil, summary: nil, screen: nil, dialog: nil
        )
    }

    private func needsYouRows(_ model: AppModel) -> [AgentsNeedsYou.Row] {
        AgentsNeedsYou.rows(cards: model.pendingInteractions, waiting: AgentsNeedsYou.waiting(model.allSessions))
            .filter { $0.key == SessionKey(machineID: "m", sessionID: "s") }
    }

    @Test func aResolvedOnlyWorkCardLeavesNeedsYou() async throws {
        let hub = FakeHub()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "stopped-work-" + UUID().uuidString))
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.applySessions([MachineRef(group: "", id: "m"): [session(state: "needs_you")]])
        model.replaceInteractions("", [try card()])
        #expect(needsYouRows(model).count == 1)

        hub.on("POST", "/api/v1/interactions/work/resolve") { _ in
            // The hub accepts the manager-local answer and its next authoritative session push is idle.
            model.applySessions([MachineRef(group: "", id: "m"): [self.session(state: "idle")]])
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.resolve(try card(), Resolution(answers: [question: "稍后再说"])) // i18n-ignore

        #expect(hub.sent("POST", "/api/v1/interactions/work/resolve").count == 1)
        #expect(model.pendingInteractions.isEmpty)
        #expect(needsYouRows(model).isEmpty)
    }
}
