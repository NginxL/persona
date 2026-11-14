#if os(iOS)
import SwiftUI

/// Settings › Live Screen (#6396, #6398): how the live screen of a computer reaches this iPhone. One page, two sections:
/// the route (`ScreenRoutePrefs`: choose the fastest route automatically) and the picture's quality on mobile data
/// (`ScreenMobileData`). Both are this iPhone's, for every computer.
struct LiveScreenSettingsView: View {
    /// Nothing is written until a choice is made here: an absent key still reads as Data Saver and keeps the page's
    /// one-time question (`ScreenMobileData.shouldAsk`).
    @StoredValue(ScreenMobileData.key) private var mobileData: ScreenMobileData = .saver
    @StoredValue(ScreenRoutePrefs.automaticKey) private var automatic = true

    /// Settings shows the page only when it can change something for a computer here: with direct routes offered, one
    /// that shows its live screen (the route's section); else one that offers the live screen's video channel (quality on
    /// mobile data applies to it). No computer, no page: someone using only the assistant never sees it.
    static func isShown(machines: [Machine], routesOffered: Bool = ScreenRoutes.offered) -> Bool {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-showScreenVideoDemo") { return true }
        #endif
        return machines.contains(where: routesOffered ? AppModel.showsScreen : AppModel.advertisesVideo)
    }

    var body: some View {
        SettingsForm {
            // The route (#6398): while direct routes are offered.
            if ScreenRoutes.offered {
                Section {
                    SettingsSwitchRow(title: String(localized: "Choose the Fastest Route Automatically"), symbol: Theme.Icon.screen,
                                      titleLines: 2, accessibilityID: "settings-live-screen-routes", isOn: $automatic)
                } footer: {
                    Text("On the same Wi-Fi or Tailscale as your computer, the screen comes straight from it, faster and steadier. Turned off, it always goes through your hub.")
                }
            }
            Section {
                Picker(String(localized: "Quality on Mobile Data"), selection: $mobileData) {
                    ForEach(ScreenMobileData.allCases, id: \.self) { m in
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(m.title)
                            Text(m.usage).font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                        .tag(m)
                    }
                }
                .pickerStyle(.inline)
                .labelsHidden()
                .accessibilityIdentifier("settings-live-screen-mobile-data")
            } header: {
                Text("Quality on Mobile Data")
            } footer: {
                Text("For watching a computer's screen on mobile data or a personal hotspot. On Wi-Fi the picture is always as clear as the connection allows, and with Low Data Mode on it always saves data.")
            }
        }
        .detailTitle(String(localized: "Live Screen"))
        .navigationBarTitleDisplayMode(.inline) // like the other pages of Settings
        .onChange(of: automatic) { _, on in
            if !on { ScreenRoutes.turnedOff() }
        }
    }
}

/// The route switch (#6398) as it is kept on this device: on unless turned off.
enum ScreenRoutePrefs {
    nonisolated static let automaticKey = "screen.routes.automatic"

    nonisolated static var automatic: Bool { UserDefaults.standard.object(forKey: automaticKey) as? Bool ?? true }
}

/// Settings' row for the page, only while `LiveScreenSettingsView.isShown`. Its own view, so machines changing redraws
/// this row and not the whole of Settings (docs/apple/performance.md, P5).
struct LiveScreenSettingsRow: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if LiveScreenSettingsView.isShown(machines: model.machines) {
            NavigationLink(value: SettingsPage.liveScreen) {
                SettingsRow(title: String(localized: "Live Screen"), symbol: Theme.Icon.screen, tint: Theme.accent, look: .oneLine)
            }
            .settingsListRow(SettingsPage.liveScreen)
            .accessibilityIdentifier("settings-live-screen")
        }
    }
}
#endif
