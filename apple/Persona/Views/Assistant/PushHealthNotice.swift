import SwiftUI

/// Persona's main screen says when this device cannot receive notifications, in everyday words with one repair action.
/// The detailed route, relay and APNs diagnostics stay in Settings › Notifications (#9612 item 4). Its ✕ hides it on this
/// device until the cause changes or pushes come back (#12319, `PushHealthStore.dismiss`): a developer build that can
/// never get pushes would otherwise keep it above the composer for good.
struct PushHealthNotice: View {
    @Environment(AppModel.self) private var model
    let group: String
    var store: PushHealthStore = .shared

    var body: some View {
        Group {
            if let notice = store.notice(group: group) {
                NoticeBanner(text: notice.text, tone: .danger, lineLimit: nil, dismiss: {
                    withAnimation(Theme.Motion.standard) { store.dismiss(notice, group: group) }
                }) {
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
