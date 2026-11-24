import Foundation
import PersonaWire
import Testing
@testable import Persona

@MainActor
struct AssistantModelPanelModelTests {
    private final class Recorder {
        var launcherRefreshes: [Bool] = []
        var modelRefreshes: [String: [Bool]] = [:]
        var kept: ModelPick?
        var dropped = false
        var launcherError: APIError?
        var modelErrors: [String: APIError] = [:]
        var canSwitch = false
        var switchState = APIClient.ConversationSwitchState(enabled: true, epoch: 8, supervisionRequired: nil)
        var switchError: APIError?
        var segmentReads = 0
        var switchRequests: [APIClient.ConversationSwitchRequest] = []
    }

    private let key = SessionKey(machineID: "box", sessionID: "main")
    private let machine = MachineRef(id: "box")
    private let response = APIClient.LaunchersResponse(
        launchers: [
            Launcher(name: "claude", model: "claude-opus-5-5"),
            Launcher(name: "team-api", model: "claude-sonnet-5-5", agent: "codex")
        ],
        default: nil,
        unavailable: nil
    )

    private func deps(_ recorder: Recorder) -> AssistantModelPanelModel.Deps {
        .init(
            launchers: { _, refresh in
                recorder.launcherRefreshes.append(refresh)
                if let error = recorder.launcherError { throw error }
                return response
            },
            models: { _, launcher, refresh in
                recorder.modelRefreshes[launcher, default: []].append(refresh)
                if let error = recorder.modelErrors[launcher] { throw error }
                return LauncherModels(agent: launcher == "claude" ? "claude" : "codex",
                                      models: [], locked: nil, machine: nil)
            },
            keep: { _, pick in
                recorder.kept = pick
                return true
            },
            drop: { _ in
                recorder.dropped = true
                return true
            },
            canSwitch: { _ in recorder.canSwitch },
            segments: { _ in
                recorder.segmentReads += 1
                return recorder.switchState
            },
            switchConversation: { _, request in
                recorder.switchRequests.append(request)
                if let error = recorder.switchError { throw error }
            }
        )
    }

    @Test func pullToRefreshRefreshesTheLauncherAndEveryModelCatalog() async throws {
        let recorder = Recorder()
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))

        await panel.load(refresh: true)

        #expect(recorder.launcherRefreshes == [true])
        #expect(recorder.modelRefreshes["claude"] == [true])
        #expect(recorder.modelRefreshes["team-api"] == [true])
        #expect(panel.loading == false)
        #expect(panel.loadError == nil)
    }

    @Test func a404BecomesAnEmptyCatalogInsteadOfLoadingForever() async throws {
        let recorder = Recorder()
        recorder.modelErrors["team-api"] = APIError(status: 404, message: "no models")
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))

        await panel.load()

        #expect(panel.list("team-api")?.models == [])
        #expect(panel.error("team-api") == nil)
        #expect(panel.list("claude")?.models == [])
    }

    @Test func aTemporaryRefreshErrorKeepsTheLastGoodCatalogAndShowsRetry() async throws {
        let recorder = Recorder()
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))
        await panel.load()
        #expect(panel.list("claude") != nil)

        recorder.modelErrors["claude"] = APIError(status: 502, message: "hub down")
        await panel.load(refresh: true)

        #expect(panel.list("claude") != nil)
        #expect(panel.error("claude") == "hub down")
        #expect(recorder.modelRefreshes["claude"] == [false, true])
    }

    @Test func aLauncherRefreshErrorKeepsTheResponseAndShowsRetry() async throws {
        let recorder = Recorder()
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))
        await panel.load()
        #expect(panel.response != nil)

        recorder.launcherError = APIError(status: 502, message: "launcher hub down")
        await panel.load(refresh: true)

        #expect(panel.response != nil)
        #expect(panel.loadError == "launcher hub down")
    }

    @Test func aProviderMissingFromBothListsNeedsRecovery() async throws {
        let recorder = Recorder()
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))

        await panel.load()

        #expect(panel.isMissing("old-gw"))
        #expect(!panel.isMissing("claude"))
        #expect(!panel.isMissing("team-api"))
    }

    @Test func chooseAndFollowDefaultCallTheConversationModelEndpoints() async throws {
        let recorder = Recorder()
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))
        let session = try session()

        let changed = await panel.choose(launcher: "claude", model: "haiku", session: session)
        let reset = await panel.followDefault()

        #expect(changed)
        #expect(recorder.kept == ModelPick(launcher: "claude", model: "haiku", effort: "high"))
        #expect(reset)
        #expect(recorder.dropped)
    }

    @Test func crossEngineCancelSendsNoPickSegmentReadOrSwitch() async throws {
        let recorder = Recorder()
        recorder.canSwitch = true
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))
        await panel.load()

        let changed = await panel.choose(launcher: "team-api", model: "gpt-standard", session: try session())

        #expect(!changed)
        #expect(panel.switchPick == ModelPick(launcher: "team-api", model: "gpt-standard", effort: "high"))
        #expect(panel.switchDestination == "team-api")
        #expect(recorder.kept == nil && recorder.segmentReads == 0 && recorder.switchRequests.isEmpty)
        panel.cancelSwitch()
        #expect(panel.switchPick == nil && panel.switchDestination.isEmpty && !panel.switchUncertain && panel.switchError == nil)
        #expect(recorder.kept == nil && recorder.segmentReads == 0 && recorder.switchRequests.isEmpty)
    }

    @Test func uncertainSwitchRetryKeepsTheExactOperation() async throws {
        let recorder = Recorder()
        recorder.canSwitch = true
        recorder.switchError = APIError(status: 502, message: "response unconfirmed", fields: ["delivered": "unknown"])
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))
        await panel.load()
        _ = await panel.choose(launcher: "team-api", model: "gpt-standard", session: try session())

        let first = await panel.confirmSwitch(session: try session())
        #expect(!first && panel.switchUncertain && panel.switchPick != nil)
        #expect(panel.switchError?.contains("response unconfirmed") == true)
        #expect(panel.switchError?.contains("Optional(") == false)
        #expect(recorder.kept == nil && recorder.segmentReads == 1 && recorder.switchRequests.count == 1)
        let request = try #require(recorder.switchRequests.first)
        #expect(request.expectedEpoch == 8 && request.sourceMachineID == "box" && request.machineID == "box")
        #expect(request.launcher == "team-api" && request.model == "gpt-standard" && request.effort == "high")
        #expect(request.permissionMode == "acceptEdits")

        recorder.switchError = nil
        let second = await panel.confirmSwitch(session: try session())
        #expect(second && panel.switchPick == nil && !panel.switchUncertain)
        #expect(recorder.switchRequests == [request, request])
        #expect(recorder.segmentReads == 1 && recorder.kept == nil)
    }

    @Test func aDefinitelyUndeliveredSwitchReportsTheRefusalWithoutUncertainty() async throws {
        let recorder = Recorder()
        recorder.canSwitch = true
        recorder.switchError = APIError(status: 409, message: "conversation changed", fields: ["delivered": "none"])
        let panel = AssistantModelPanelModel(key: key, machine: machine, deps: deps(recorder))
        await panel.load()
        _ = await panel.choose(launcher: "team-api", model: "gpt-standard", session: try session())

        let changed = await panel.confirmSwitch(session: try session())
        #expect(!changed && !panel.switchUncertain && panel.switchError == "conversation changed")
        #expect(recorder.switchRequests.count == 1 && recorder.kept == nil)
        panel.cancelSwitch()
        #expect(panel.switchPick == nil && panel.switchError == nil)
    }

    @Test func switchEndpointsCarryMachineEpochAndTheExactRequestBody() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/conversations/chat/segments", json: #"{"enabled":true,"epoch":8}"#)
        hub.on("POST", "/api/v1/assistant/conversations/chat/switch", json: #"{"phase":"preparing"}"#)
        let state = try await hub.client.conversationSwitchState(machine: "box", conversation: "chat")
        let request = APIClient.ConversationSwitchRequest(
            operationID: "3fa85f64-5717-4562-b3fc-2c963f66afa6", expectedEpoch: state.epoch,
            sourceMachineID: "box", machineID: "box", launcher: "agy", model: nil, effort: nil,
            permissionMode: "acceptEdits")
        try await hub.client.switchConversation(machine: "box", conversation: "chat", request: request)

        #expect(state.enabled && state.epoch == 8)
        #expect(hub.sent("GET", "/api/v1/assistant/conversations/chat/segments").first?.query["machine_id"] == "box")
        let sent = try #require(hub.sent("POST", "/api/v1/assistant/conversations/chat/switch").first?.body)
        let body = try #require(JSONSerialization.jsonObject(with: sent) as? [String: Any])
        #expect(body["operation_id"] as? String == request.operationID)
        #expect(body["expected_epoch"] as? Int == 8 && body["source_machine_id"] as? String == "box")
        #expect(body["machine_id"] as? String == "box" && body["launcher"] as? String == "agy")
        #expect(body["permission_mode"] as? String == "acceptEdits")
    }

    private func session() throws -> Session {
        let json = #"{"machine_id":"box","session_id":"main","launcher":"claude","model":"opus","effort":"high","permission_mode":"acceptEdits"}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }
}
