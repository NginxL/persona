#if os(iOS)
import SwiftUI

struct HealthIntakeSyncSettings: View {
    @Environment(AppModel.self) private var model
    @State private var sync = HealthIntakeSync.shared

    var body: some View {
        if sync.visible(model: model) {
            Section {
                SettingsSwitchRow(title: String(localized: "Sync to Apple Health"), symbol: Theme.Icon.health,
                                  accessibilityID: "health-intake-sync-toggle",
                                  isOn: Binding(get: { preview != nil || sync.enabled(model: model) },
                                                set: { value in Task { await sync.setEnabled(value, model: model) } }))
                    .disabled(sync.changing || preview != nil)
                if sync.enabled(model: model) || preview != nil || sync.error(model: model) != nil {
                    status.font(Theme.Font.secondary)
                        .accessibilityIdentifier("health-intake-sync-status")
                }
            } footer: {
                Text("Off by default. When enabled, all your water and protein records, including earlier records, are written to Apple Health on this iPhone. Undoing a record removes its sample. Turning this off keeps samples already written.")
            }
            .task { await sync.load(); await sync.refresh(model: model) }
        }
    }

    private var preview: String? {
        #if DEBUG
        UserDefaults.standard.string(forKey: "showIntakeSyncStatus")
        #else
        nil
        #endif
    }

    @ViewBuilder private var status: some View {
        if preview == "denied" || (preview == nil && !sync.denied.isEmpty) {
            if preview == "denied" || sync.denied.count == 2 {
                Text("Apple Health hasn't allowed writing water or protein.").foregroundStyle(Theme.caution)
            } else if sync.denied.first == .water {
                Text("Apple Health hasn't allowed writing water.").foregroundStyle(Theme.caution)
            } else {
                Text("Apple Health hasn't allowed writing protein.").foregroundStyle(Theme.caution)
            }
        } else if let error = preview == "failed" ? String(localized: "Couldn't connect. Try again later.")
                    : (preview == nil ? sync.error(model: model) : nil) {
            Text("Last write failed: \(error)").foregroundStyle(Theme.caution)
        } else if let date = preview == "synced" ? Date.now.addingTimeInterval(-180) : sync.state.lastSynced {
            (Text("Synced") + Text(" · ") + Text(date, format: .relative(presentation: .named))).foregroundStyle(.secondary)
        } else {
            Text("Waiting to sync").foregroundStyle(.secondary)
        }
    }
}

struct HealthIntakeSyncLink: View {
    @Environment(AppModel.self) private var model
    @State private var sync = HealthIntakeSync.shared

    var body: some View {
        if sync.visible(model: model), !sync.enabled(model: model) {
            NavigationLink { WorkoutReportSettingsView() } label: {
                Label { Text("Sync to Apple Health").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.health) }
            }
            .accessibilityIdentifier("health-intake-sync-link")
            .task { await sync.load() }
        }
    }
}
#endif
