import SwiftUI

/// Agent tools and automation retain their existing routes for saved links and launch arguments.
struct SettingsAgentView: View {
    var body: some View {
        SettingsForm {
            DefaultPermissionsSettingsSection()
            Section {
                NavigationLink { HiddenAgentDirsView() } label: {
                    SettingsRow(title: String(localized: "agentDirs.title", defaultValue: "Agent Folders"), symbol: Theme.Icon.options, tint: Theme.ai)
                }
                .lineLimit(1)
                NavigationLink(value: SettingsPage.claude) {
                    SettingsRow(title: String(localized: "Claude Code Settings"), symbol: Theme.Icon.options, tint: Theme.Hue.orange)
                }
                .lineLimit(1)
                .accessibilityIdentifier("settings-agent-claude")
                NavigationLink(value: SettingsPage.codex) {
                    SettingsRow(title: String(localized: "Codex Settings"), symbol: Theme.Icon.options, tint: Theme.Hue.gray)
                }
                .lineLimit(1)
                .accessibilityIdentifier("settings-agent-codex")
                NavigationLink(value: SettingsPage.agy) {
                    SettingsRow(title: String(localized: "Antigravity Settings"), symbol: Theme.Icon.options, tint: Theme.Hue.blue)
                }
                .lineLimit(1)
                .accessibilityIdentifier("settings-agent-agy")
                NavigationLink(value: SettingsPage.triage) {
                    SettingsRow(title: String(localized: "Session Triage"), symbol: Theme.Icon.triage, tint: Theme.ai)
                }
                .lineLimit(1)
                .accessibilityIdentifier("settings-triage")
            } footer: {
                Text("Agent tools and automation for this assistant's computers.")
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Agent"))
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A settings page keeps the assistant and hub it opened for, even while a request is suspended.
/// This is only request routing and list ownership; the existing models still own all data and saves.
nonisolated struct AssistantSettingsRequestScope: Equatable, Sendable {
    var group: String
    var manager: String?
    var roster: ManagerRoster?

    @MainActor init(_ app: AppModel) {
        group = app.hub?.group ?? ""
        manager = app.assistantManagerID
        roster = app.managerRoster
    }

    init(group: String, manager: String?, roster: ManagerRoster? = nil) {
        self.group = group
        self.manager = manager
        self.roster = roster
    }

    @MainActor func api(_ app: AppModel) -> APIClient? { app.api(group)?.manager(manager) }

    func machines(_ all: [Machine]) -> [Machine] {
        all.filter { machine in
            guard machine.ref.group == group else { return false }
            guard let roster else { return manager == nil || machine.manager == manager }
            return roster.owner(machine.manager) == roster.owner(manager)
        }
    }

    @MainActor func machines(_ app: AppModel) -> [Machine] { machines(app.machines) }
}
