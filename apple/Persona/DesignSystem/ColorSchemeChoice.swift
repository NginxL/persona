import SwiftUI
import UIKit

/// Settings › Appearance › Light & Dark (docs/apple/ios.md「Design language」): the app light or dark whatever the system is, or following it (the
/// default: what the app always did). Set on the app's windows (`overrideUserInterfaceStyle`) rather than with
/// `preferredColorScheme`: sheets, alerts, the keyboard and web pages follow a window's style, and going back to nil
/// through `preferredColorScheme` does not always bring an open sheet back to the system's. The Live Activity,
/// widgets and the watch are drawn by the system and keep its look.
nonisolated enum ColorSchemeChoice: String, CaseIterable, Identifiable, Sendable {
    case system, light, dark

    var id: String { rawValue }

    var title: String {
        switch self {
        case .system: String(localized: "Match System")
        case .light: String(localized: "Light")
        case .dark: String(localized: "Dark")
        }
    }

    var symbol: String {
        switch self {
        case .system: Theme.Icon.colorScheme
        case .light: Theme.Icon.lightMode
        case .dark: Theme.Icon.darkMode
        }
    }

    static let key = "ui.scheme"

    /// `ui.scheme` (also a launch argument: `-ui.scheme dark`).
    static var current: ColorSchemeChoice { ColorSchemeChoice(rawValue: UserDefaults.standard.string(forKey: key) ?? "") ?? .system }

    var interfaceStyle: UIUserInterfaceStyle {
        switch self {
        case .system: .unspecified
        case .light: .light
        case .dark: .dark
        }
    }

    /// On every window of every scene (an iPad can have several); a window already on it is left alone.
    @MainActor func apply() {
        for case let scene as UIWindowScene in UIApplication.shared.connectedScenes {
            for w in scene.windows where w.overrideUserInterfaceStyle != interfaceStyle {
                w.overrideUserInterfaceStyle = interfaceStyle
            }
        }
    }
}

#if DEBUG && (os(iOS) || os(visionOS))
/// `-showLookBlink <seconds>`: every that many seconds the windows go to the other look and back, laid out in between
/// (what iOS does for its app-switcher snapshots as the app goes to the background: one in each look). The sidebar's
/// own hosting controller kept the other look's text colors after it (#4496); `SidebarContrastTests` runs with it.
@MainActor enum LookBlink {
    static func run() async {
        let every = UserDefaults.standard.double(forKey: "showLookBlink")
        guard every > 0 else { return }
        while !Task.isCancelled {
            try? await Task.sleep(for: .seconds(every))
            for case let scene as UIWindowScene in UIApplication.shared.connectedScenes {
                for w in scene.windows {
                    let kept = w.overrideUserInterfaceStyle
                    let other: UIUserInterfaceStyle = w.traitCollection.userInterfaceStyle == .dark ? .light : .dark
                    w.overrideUserInterfaceStyle = other
                    w.layoutIfNeeded()
                    w.overrideUserInterfaceStyle = kept
                    w.layoutIfNeeded()
                }
            }
        }
    }
}
#endif
