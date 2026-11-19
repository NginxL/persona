import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct SettingsNavigationTests {
    private static let roster = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true),
        HubManager(id: "m-example", name: "Example", online: true),
    ], defaultID: "assistant")

    /// A model on one made-up hub: no socket, its requests go to `hub`.
    private func model(_ hub: FakeHub = FakeHub()) throws -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "settings-navigation-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        model.managerRosters[""] = Self.roster
        return model
    }

    @Test func searchIsEmptyUntilAQueryIsEntered() {
        #expect(SettingsCatalog.search("").isEmpty)
        #expect(SettingsCatalog.search("  \n ").isEmpty)
    }

    @Test func everyEntryHasAUniqueStableIDAndSearchesByItsName() {
        let entries = SettingsCatalog.entries
        #expect(Set(entries.map(\.id)).count == entries.count)
        for entry in entries {
            #expect(SettingsCatalog.search(entry.title).contains { $0.id == entry.id })
        }
    }

    @Test func commonAliasesFindTheirDestination() {
        for (query, id) in [("API Key", "models"), ("Claude Code", "agents"), ("Codex", "agents"),
                            ("Antigravity", "agents"), ("agy", "agents"), ("Antigravity", "agy"), ("agy", "agy"),
                            ("commit", "about"), ("Watch", "devices")] {
            #expect(SettingsCatalog.search(query).contains { $0.id == id })
        }
    }

    @Test func multipleWordsMustAllMatchAndUnknownWordsDoNotMatch() {
        let entry = SettingsEntry(id: "test", title: "Model Choices", symbol: Theme.Icon.model,
                                  group: .assistant, page: .models, aliases: "API Key")
        #expect(entry.matches("model key"))
        #expect(entry.matches("MODEL KEY"))
        #expect(!entry.matches("model nonexistent"))
        #expect(SettingsCatalog.search("not-a-setting-5236").isEmpty)
    }

    @Test func onlyAssistantPagesAreBlockedByTheCurrentAssistantsOfflineState() throws {
        let model = try model()
        for page in [SettingsPage.models, .agents, .assistant, .claude, .codex, .agy, .connectors, .plugins, .channels, .triage, .computers] {
            #expect(page.belongsToCurrentAssistant(in: model))
        }
        for page in [SettingsPage.appearance, .language, .notifications, .health, .phoneData, .about, .persona, .pairing, .managers, .hub] {
            #expect(!page.belongsToCurrentAssistant(in: model))
        }
        #expect(SettingsPage.plugin("example").belongsToCurrentAssistant(in: model))
        #expect(!SettingsPage.plugin("example", manager: "m-example").belongsToCurrentAssistant(in: model))
        #expect(!SettingsPage.connector("example", manager: "m-example").belongsToCurrentAssistant(in: model))
        #expect(!SettingsPage.channel("example", manager: "m-example").belongsToCurrentAssistant(in: model))
    }

    @Test func oldRoutesRemainIndependentFromNewContainers() {
        #expect(SettingsPage.modelConfiguration != .models)
        #expect(SettingsPage.claude != .agents)
        #expect(SettingsPage.codex != .agents)
        #expect(SettingsPage.agy != .agents)
        #expect(SettingsPage.triage != .agents)
        #expect(SettingsPage.devices != .pairing)
        #expect(SettingsPage.sentPath == NavigationPath([SettingsPage.connectors, .sent]))
    }

    @Test func agySettingsKindReadsAndWritesAgyEndpoint() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/agy-settings", json: #"{"settings":[{"key":"toolPermission","title":"Tool permission","detail":"Whether agy asks before running tools","type":"enum","options":["ask","always-proceed"],"default":"ask","value":"ask"}]}"#)
        hub.on("PUT", "/api/v1/machines/mac/agy-settings") { req in
            let body = try #require(req.body.flatMap { String(data: $0, encoding: .utf8) })
            #expect(body.contains(#""key":"toolPermission""#))
            #expect(body.contains(#""value":"always-proceed""#))
            return Data(#"{"settings":[{"key":"toolPermission","title":"Tool permission","detail":"Whether agy asks before running tools","type":"enum","options":["ask","always-proceed"],"default":"ask","value":"always-proceed"}]}"#.utf8)
        }
        let initial = try await AgentSettingsKind.agy.read(from: hub.client, machine: "mac")
        #expect(initial.map(\.key) == ["toolPermission"])
        #expect(initial.first?.value?.string == "ask")
        let updated = try await AgentSettingsKind.agy.write(to: hub.client, machine: "mac", key: "toolPermission", value: .string("always-proceed"))
        #expect(updated.first?.value?.string == "always-proceed")
        #expect(hub.sent("GET", "/api/v1/machines/mac/agy-settings").count == 1)
        #expect(hub.sent("PUT", "/api/v1/machines/mac/agy-settings").count == 1)
        #expect(AgentSettingsKind.agy.footer.contains("~/.gemini/antigravity-cli/settings.json"))

        DemoHub.install()
        DemoHub.shared.reset()
        #expect(try await APIClient(config: .demo).agySettings(machine: "macbook").isEmpty)
    }
}
