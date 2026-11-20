import SwiftUI

struct SettingsAssistantHeading: View {
    @Environment(AppModel.self) private var model

    struct Content {
        let manager: HubManager?
        let title: String
        let footnote: String?
        let showsSwitch: Bool
    }

    /// The roster, not the number of assistants, decides whether Settings names an assistant and offers the switcher.
    static func content(for model: AppModel) -> Content {
        if model.managerRoster != nil, let manager = model.currentAssistant {
            return Content(manager: manager,
                           title: String(localized: "\(manager.name)'s Settings"),
                           footnote: String(localized: "Only \(manager.name)'s own settings are shown. To change another assistant's, switch to it first."),
                           showsSwitch: true)
        }
        return Content(manager: nil, title: String(localized: "Current Assistant's Settings"), footnote: nil, showsSwitch: false)
    }

    static var commonGroupsFooter: String {
        String(localized: "These two groups are the same whichever assistant is current.")
    }

    var body: some View {
        let content = Self.content(for: model)
        Group {
            if let manager = content.manager {
                AssistantSwitcherHeader(manager: manager, nameFont: Theme.Font.labelStrong, showsSwitchLabel: content.showsSwitch,
                                        identityStyle: .settings(title: content.title, defaultID: model.managerRoster?.defaultID ?? manager.id),
                                        openSettings: {})
            } else {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    HStack(spacing: Theme.Space.m) {
                        PersonaAvatar(size: Theme.Size.avatar)
                        Text(model.persona.cardTitle).font(Theme.Font.bodyStrong).lineLimit(1)
                    }
                    Text(content.title).font(Theme.Font.labelStrong)
                }
            }
        }
        .textCase(nil)
    }
}

/// Kept apart from the heading so the footer's roster read does not redraw the switcher.
struct SettingsAssistantFooter: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if let footnote = SettingsAssistantHeading.content(for: model).footnote {
            Text(footnote)
        }
    }
}

struct SettingsPairingPage: View {
    @Environment(AppModel.self) private var model
    @State private var showPair = false
    @State private var confirmUnpair = false

    var body: some View {
        SettingsForm {
            Section {
                NavigationLink(value: SettingsPage.devices) {
                    SettingsRow(title: String(localized: "Devices"), symbol: Theme.Icon.credentials, tint: Theme.Hue.gray)
                }
                .settingsListRow(SettingsPage.devices)
                Button { showPair = true } label: {
                    SettingsRow(title: String(localized: "Pair Again"), symbol: Theme.Icon.pairing, tint: Theme.accent)
                }
                .modifier(SettingsButtonLook())
                .disabled(model.hubSignOut != nil)
            }
            #if os(iOS) || os(visionOS)
            if !model.isDemo {
                Section {
                    Button { Task { await syncWatch() } } label: {
                        SettingsRow(title: String(localized: "Sync to Apple Watch"), symbol: Theme.Icon.watch, tint: Theme.accent)
                    }
                    .modifier(SettingsButtonLook())
                    .accessibilityIdentifier("settings-watch-sync")
                } footer: {
                    Text("Persona on Apple Watch connects to the hub directly using your iPhone's pairing. If the watch shows “Not Paired”, tap here, or tap “Sync from iPhone” on the watch.")
                }
            }
            #endif
            if !model.isDemo {
                Section {
                    Button(role: .destructive) { confirmUnpair = true } label: {
                        SettingsRow(title: model.isMultiHub ? String(localized: "Disconnect All Hubs") : String(localized: "Disconnect and Forget Hub"),
                                    symbol: Theme.Icon.end, tint: Theme.danger)
                            .trailingProgress(model.hubSignOut == .all)
                    }
                    .modifier(SettingsButtonLook())
                    .disabled(model.hubSignOut != nil)
                }
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Devices & Pairing"))
        .accessibleConfirmationDialog("After disconnecting, you'll need to scan the code to pair again.", isPresented: $confirmUnpair, titleVisibility: .visible, hasCancel: true) {
            Button("Disconnect", role: .destructive) { Task { await model.disconnectAll() } }
            Button("Cancel", role: .cancel) {}
        }
        .sheet(isPresented: $showPair) { PairView() }
    }

    #if os(iOS) || os(visionOS)
    private func syncWatch() async {
        guard model.isPaired else { return }
        guard WatchSync.shared.watchAppInstalled else {
            Haptics.error()
            model.toast = Toast(String(localized: "No Apple Watch with Persona found. Install it first in the Watch app on your iPhone."), style: .error)
            return
        }
        await model.upgradeCredentials()
        model.summariesChanged()
        if model.watchConfigs().missing?.isEmpty == false {
            Haptics.error()
            model.toast = Toast(String(localized: "Couldn't get credentials for the watch. Check your network and try again; if the hub is too old, update it first."), style: .error)
        } else {
            Haptics.success()
            model.toast = Toast(String(localized: "Sent to Apple Watch. Open Persona on your watch."), style: .success)
        }
    }
    #endif
}
