import SwiftUI

/// Persona's main screen says when this device cannot receive notifications, in everyday words with one repair action.
/// The detailed route, relay and APNs diagnostics stay in Settings › Notifications (#9612 item 4).
struct PushHealthNotice: View {
    @Environment(AppModel.self) private var model
    let group: String

    var body: some View {
        Group {
            if let notice = currentHealth.mainNotice(group: group, allowed: nil) {
                NoticeBanner(text: notice.text, tone: .danger, lineLimit: nil) {
                    Button {
                        Haptics.tap()
                        open(notice.repair)
                    } label: {
                        Text(notice.actionTitle)
                            .tapTargetHeight()
                            .contentShape(.rect)
                    }
                    .accessibilityIdentifier("pushHealth.action")
                }
                .accessibilityIdentifier("pushHealth.notice")
            }
        }
    }

    private var currentHealth: PushHealth {
        #if DEBUG
        // `-showPushHealth <name>`: the same sample as Settings › Notifications, for screenshots and UI tests.
        if let sample = UserDefaults.standard.string(forKey: "showPushHealth") {
            return .sample(sample, group: group)
        }
        #endif
        return PushHealthStore.shared.health
    }

    private func open(_ repair: PushNoticeContent.Repair) {
        switch repair {
        case .notificationSettings:
            let pages: [SettingsPage] = [.notifications]
            model.settingsPath = NavigationPath(pages)
            model.showSettings()
        case .systemSettings:
            guard let url = URL(string: UIApplication.openNotificationSettingsURLString) else { return }
            UIApplication.shared.open(url)
        }
    }
}
