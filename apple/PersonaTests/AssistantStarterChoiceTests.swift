import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// A new Persona conversation's model pick on iOS (#11472): a model picked under a launcher goes with the start, the
/// launcher's default entry sends no model, and with nothing picked the start names neither and the starter shows Main
/// chat's default model.
@MainActor
struct AssistantStarterChoiceTests {
    private let box = MachineRef(id: "box")
    private let startPath = "/api/v1/assistant/conversations"

    /// Main chat runs `claude` with `sonnet`; the launcher's own fixed model is `opus` (the issue's L/M vs L/D).
    private static let launchers = #"""
    {"launchers":[{"name":"claude","model":"opus"},{"name":"codex","model":"gpt-5.5","agent":"codex"}],
     "default":{"provider":"launcher.claude","model":"sonnet","effort":""}}
    """#
    private static let claudeModels = #"""
    {"agent":"claude","models":[
      {"id":"default","name":"Default (recommended)","resolved":"claude-opus-5-5","default":true,"efforts":["low","high","max"]},
      {"id":"fable","name":"Fable","resolved":"claude-fable-5-1","efforts":["low","high","max","xhigh"]},
      {"id":"sonnet","name":"Sonnet 5.5","resolved":"claude-sonnet-5-5","efforts":["low","medium"]}]}
    """#
    private static let codexModels = #"""
    {"agent":"codex","models":[{"id":"gpt-5.5","name":"GPT-5.5","default":true,"efforts":["low","medium","high"]}]}
    """#

    private static func started(pick: String? = nil) -> String {
        #"{"session":{"session_id":"s1","status":"working","state":"working","started_by_persona":true,"assistant":true,"pending_interactions":0,"launcher":"claude","model":"fable""#
            + (pick.map { #","model_pick":\#($0)"# } ?? "") + "}}"
    }

    private func hub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/launchers", json: Self.launchers)
        hub.on("GET", "/api/v1/machines/box/launchers/claude/models", json: Self.claudeModels)
        hub.on("GET", "/api/v1/machines/box/launchers/codex/models", json: Self.codexModels)
        return hub
    }

    private func response() throws -> APIClient.LaunchersResponse {
        try JSON.decoder.decode(APIClient.LaunchersResponse.self, from: Data(Self.launchers.utf8))
    }

    private func list(_ json: String) throws -> LauncherModels {
        try JSON.decoder.decode(LauncherModels.self, from: Data(json.utf8))
    }

    private func body(_ hub: FakeHub) throws -> [String: Any] {
        let data = try #require(hub.sent("POST", startPath).last?.body)
        return try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    /// A request or a pick as one word ("launcher/model", "-" for none), so it compares in one expectation.
    private func text(_ p: (launcher: String?, model: String?)) -> String { "\(p.launcher ?? "-")/\(p.model ?? "-")" }
    private func text(_ p: (launcher: String, model: String?)?) -> String? { p.map { "\($0.launcher)/\($0.model ?? "-")" } }

    /// Starts the way `AssistantView.start` does: the pick through `AssistantStarterChoice.request`.
    private func start(_ hub: FakeHub, launcher: String?, model: String?) async throws -> Session {
        let pick = AssistantStarterChoice.request(launcher: launcher, model: model)
        return try await hub.client.startConversation(prompt: "Hello", launcher: pick.launcher, model: pick.model, main: true)
    }

    // MARK: P1 Defaults first

    @Test func withNothingPickedTheStartNamesNeitherLauncherNorModel() async throws {
        let hub = hub()
        hub.on("POST", startPath, json: Self.started())
        _ = try await start(hub, launcher: nil, model: nil)
        let sent = try body(hub)
        #expect(sent["launcher"] == nil && sent["model"] == nil, "nothing kept over Main chat's default")
        #expect(sent["main"] as? Bool == true && sent["prompt"] as? String == "Hello")
        // A model left behind without its launcher never goes either.
        #expect(text(AssistantStarterChoice.request(launcher: nil, model: "fable")) == "-/-")
        #expect(text(AssistantStarterChoice.request(launcher: "", model: "fable")) == "-/-")
        #expect(text(AssistantStarterChoice.request(launcher: "claude", model: "")) == "claude/-")
        _ = try await hub.client.startConversation(prompt: "Hello", model: "fable")
        #expect(try body(hub)["model"] == nil)
    }

    @Test func withNothingPickedTheStarterShowsMainChatsDefaultModel() throws {
        let response = try response()
        let current = try #require(AssistantStarterChoice.effective(launcher: nil, model: nil, fallback: "claude", mainDefault: response.default))
        #expect(current == .init(launcher: "claude", model: "sonnet", effort: nil, source: .defaultValue))
        #expect(!current.picked)
        // Not the launcher's own fixed model (opus): the conversation runs Main chat's model.
        #expect(AssistantModelChoice.displayName(current, response: response) == "Sonnet")
        // Before the picker answers, the launcher Persona settings name; nothing known yet is nil (#4200).
        #expect(AssistantStarterChoice.effective(launcher: nil, model: nil, fallback: "claude", mainDefault: nil)?.source == .running)
        #expect(AssistantStarterChoice.effective(launcher: nil, model: nil, fallback: nil, mainDefault: nil) == nil)
    }

    @Test func theStartersTitleIsMainChatsDefaultModel() async throws {
        let chosen = UserDefaults.standard.object(forKey: "assistant.current") // reading the settings picks the main chat
        defer { UserDefaults.standard.set(chosen, forKey: "assistant.current") }
        let hub = hub()
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[]}"#)
        let transport = try #require(hub.client.transport)
        let app = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "starter-choice-" + UUID().uuidString))
        app.linkHooksForTests = (transport: transport, socket: { SyncSocket() })
        app.connectOnly(.demo)
        await app.refreshAssistant()
        #expect(app.assistantConfig?.launcher == "claude")
        let response = try response()
        let none = StarterComposer.ShellModes(launcher: .constant(nil), launcherModel: .constant(nil), mode: .constant(""), effort: .constant(""))
        let title = StarterModesMenu.title(modes: none, launchers: response.launchers, mainDefault: response.default, model: app)
        #expect(title?.model == "Sonnet", "Main chat's default model, not the launcher's own opus")
        let picked = StarterComposer.ShellModes(launcher: .constant("claude"), launcherModel: .constant("fable"), mode: .constant(""), effort: .constant(""))
        #expect(StarterModesMenu.title(modes: picked, launchers: response.launchers, mainDefault: response.default, model: app)?.model == "Fable")
        let launcherOnly = StarterComposer.ShellModes(launcher: .constant("claude"), launcherModel: .constant(nil), mode: .constant(""), effort: .constant(""))
        #expect(StarterModesMenu.title(modes: launcherOnly, launchers: response.launchers, mainDefault: response.default, model: app)?.model == "Opus",
                "a launcher's default entry reads as the running chat's model button reads a pick without a model")
    }

    // MARK: P3 User control

    @Test func aModelPickedUnderALauncherIsWhatStartsAndIsKept() async throws {
        let hub = hub()
        hub.on("POST", startPath, json: Self.started(pick: #"{"launcher":"claude","model":"fable"}"#))
        let starter = AssistantStarterModels(deps: .init(launchers: { machine, _, _ in
            try await hub.client.launcherList(machine: machine.id, purpose: "assistant")
        }, api: { _ in hub.client }, cache: AssistantStarterCache()))
        #expect(await starter.load(box, launcher: nil, fallback: "claude"))
        await starter.loadLists(box)
        #expect(starter.mainDefault?.launcher == "claude" && starter.mainDefault?.model == "sonnet")
        #expect(Set(starter.lists.keys) == ["claude", "codex"], "every launcher's models, for the rows under it")
        #expect(starter.visibleModels["claude"]?.map(\.id) == ["default", "fable", "sonnet"])
        #expect(starter.aliases["claude"]?["claude-sonnet-5-5"] == "sonnet", "a resolved model ticks its row")
        #expect(hub.sent("GET", "/api/v1/machines/box/launchers/codex/models").first?.query["purpose"] == "assistant")

        let current = AssistantStarterChoice.effective(launcher: nil, model: nil, fallback: "claude", mainDefault: starter.mainDefault)
        let aliases = AssistantModelChoice.selectionAliases(starter.lists["claude"])
        let pick = try #require(AssistantStarterChoice.pick(launcher: "claude", model: "fable", current: current, aliases: aliases))
        #expect(text(pick) == "claude/fable")
        let session = try await start(hub, launcher: pick.launcher, model: pick.model)
        let sent = try body(hub)
        #expect(sent["launcher"] as? String == "claude" && sent["model"] as? String == "fable")
        #expect(session.modelPick == ModelPick(launcher: "claude", model: "fable", effort: nil), "the conversation's kept pick")
    }

    @Test func aLaunchersDefaultEntrySendsNoModel() async throws {
        let hub = hub()
        hub.on("POST", startPath, json: Self.started(pick: #"{"launcher":"claude"}"#))
        let claude = try list(Self.claudeModels)
        let current = AssistantStarterChoice.effective(launcher: nil, model: nil, fallback: "claude", mainDefault: try response().default)
        let aliases = AssistantModelChoice.selectionAliases(claude)
        // Main chat runs claude · sonnet: its Default row (claude's own model) is another choice, sent without a model.
        let pick = try #require(AssistantStarterChoice.pick(launcher: "claude", model: "", current: current, aliases: aliases))
        #expect(text(pick) == "claude/-")
        _ = try await start(hub, launcher: pick.launcher, model: pick.model)
        let sent = try body(hub)
        #expect(sent["launcher"] as? String == "claude" && sent["model"] == nil)
        // Another launcher's default entry too.
        #expect(text(AssistantStarterChoice.pick(launcher: "codex", model: nil, current: current)) == "codex/-")
    }

    @Test func theDefaultsOwnRowKeepsFollowingTheDefault() throws {
        let claude = try list(Self.claudeModels)
        let aliases = AssistantModelChoice.selectionAliases(claude)
        let current = AssistantStarterChoice.effective(launcher: nil, model: nil, fallback: "claude", mainDefault: try response().default)
        #expect(AssistantModelChoice.selectedModel(try #require(current), launcher: "claude", aliases: aliases) == "sonnet",
                "the default's row is the one ticked")
        #expect(text(AssistantStarterChoice.pick(launcher: "claude", model: "sonnet", current: current, aliases: aliases)) == nil,
                "no pick: the start still names nothing")
        // A pick made: the same row again changes nothing; following the default is the menu's own row.
        let picked = AssistantStarterChoice.effective(launcher: "claude", model: "fable", fallback: "claude", mainDefault: try response().default)
        #expect(picked?.picked == true)
        #expect(text(AssistantStarterChoice.pick(launcher: "claude", model: "fable", current: picked, aliases: aliases)) == nil)
    }

    @Test func theEffortLevelsAreThePickedModels() throws {
        let response = try response()
        let claude = try list(Self.claudeModels)
        // No model picked: Main chat's model (sonnet), which the hub runs on Main chat's launcher.
        #expect(AssistantStarterEffort.options(launcher: "claude", response: response, list: claude).map(\.id) == ["low", "medium"])
        // A model picked under it: that model's levels.
        #expect(AssistantStarterEffort.options(launcher: "claude", model: "fable", response: response, list: claude).map(\.id)
                == ["low", "high", "max", "xhigh"])

        let folded = LauncherModels(agent: "claude", models: [
            AgentModel(id: "claude-opus-5-5", name: "Opus 5.5", efforts: ["low", "high"], aliases: ["fable", "opus"]),
        ], locked: nil, machine: nil)
        #expect(AssistantStarterEffort.options(launcher: "claude", model: "fable", response: response, list: folded).map(\.id)
                == ["low", "high"], "a saved folded alias keeps the row's effort levels")
        let foldedCurrent = AssistantStarterChoice.effective(launcher: "claude", model: "fable", fallback: "claude",
                                                             mainDefault: response.default)
        let foldedAliases = AssistantModelChoice.selectionAliases(folded)
        #expect(AssistantModelChoice.selectedModel(try #require(foldedCurrent), launcher: "claude", aliases: foldedAliases)
                == "claude-opus-5-5")
        #expect(AssistantStarterChoice.pick(launcher: "claude", model: "claude-opus-5-5", current: foldedCurrent,
                                            aliases: foldedAliases) == nil)
        // The effort check's key tells the models apart, and a model never counts without its launcher.
        let fable = AssistantStarterModels.source(box, launcher: "claude", model: "fable", fallback: "claude")
        #expect(fable != AssistantStarterModels.source(box, launcher: "claude", model: nil, fallback: "claude"))
        #expect(AssistantStarterModels.source(box, launcher: nil, model: "fable", fallback: "claude")
                == AssistantStarterModels.source(box, launcher: nil, fallback: "claude"))
    }

    @Test func aListThatCannotBeReadLeavesTheLauncherPickable() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/launchers", json: Self.launchers)
        hub.on("GET", "/api/v1/machines/box/launchers/claude/models", json: Self.claudeModels)
        var fail = true
        hub.on("GET", "/api/v1/machines/box/launchers/codex/models") { _ in
            if fail { throw APIError(status: 502, message: "the node did not answer") }
            return Data(Self.codexModels.utf8)
        }
        let starter = AssistantStarterModels(deps: .init(launchers: { machine, _, _ in
            try await hub.client.launcherList(machine: machine.id, purpose: "assistant")
        }, api: { _ in hub.client }, cache: AssistantStarterCache()))
        #expect(await starter.load(box, launcher: nil, fallback: "claude"))
        await starter.loadLists(box)
        #expect(starter.lists["codex"] == nil && starter.listFailures == ["codex"])
        #expect(starter.launchers.map(\.name) == ["claude", "codex"])
        fail = false
        await starter.loadLists(box) // Retry
        #expect(starter.lists["codex"] != nil && starter.listFailures.isEmpty)
        #expect(hub.sent("GET", "/api/v1/machines/box/launchers/claude/models").count == 1, "a list read once is not asked again")
    }
}
