import SwiftUI
import UIKit

// Both supported personas use the default app icon. Paper's fixed light and dark sets remain available.

nonisolated enum AppIconChoice: String, CaseIterable, Identifiable, Sendable {
    case standard

    var id: String { rawValue }
    var iconName: String? { nil }
    var preview: String { "app-icon-default" }
    var lookID: String { PersonaSelection.builtinID }

    init(lookID: String?) { self = .standard }
    init(iconName: String?) { self = .standard }

    var followsScheme: Bool { true }

    func iconName(for scheme: ColorSchemeChoice) -> String? {
        switch scheme {
        case .system: nil
        case .light: "AppIcon-PaperLight"
        case .dark: "AppIcon-PaperDark"
        }
    }

    func homeScreenLook(_ scheme: ColorSchemeChoice, system: ColorScheme) -> ColorScheme {
        switch scheme {
        case .system: system
        case .light: .light
        case .dark: .dark
        }
    }

    var iconNames: [String] { ["AppIcon-PaperLight", "AppIcon-PaperDark"] }
}

/// What the picker needs from the system, so tests can stand in for it.
@MainActor protocol AppIconSetting: AnyObject {
    var supportsAlternateIcons: Bool { get }
    var alternateIconName: String? { get }
    func setAlternateIconName(_ name: String?) async throws
}

extension UIApplication: AppIconSetting {}

/// The home screen icon kept on the Avatar's (`follow`): what is on now, and the last failure to switch.
@MainActor @Observable final class AppIconModel {
    private let system: AppIconSetting?
    /// The icon on the home screen: the system's at launch, then each switch that went through.
    private(set) var current: AppIconChoice
    private(set) var switching = false
    private(set) var failure: String?

    /// Off where the system cannot change the icon (the Mac, an app with alternate icons turned off).
    var available: Bool { system?.supportsAlternateIcons ?? false }

    init(system: AppIconSetting?) {
        self.system = system
        current = AppIconChoice(iconName: system?.alternateIconName)
    }

    /// The Avatar's icon the home screen should have; nil until the Avatar is known (a package the list has not named yet).
    @ObservationIgnored private var wanted: AppIconChoice? = .standard
    /// The app's Light & Dark the icon should match (#710).
    @ObservationIgnored private var scheme: ColorSchemeChoice = .system

    /// The Avatar or Light & Dark changed, or the app came to the front: the home screen moves to the Avatar's icon (`look`, else
    /// the last one given) in the set for Light & Dark (`scheme`, else the last one given). The system confirms with its
    /// alert, as for any switch; an icon already right is left alone (no alert). This is also the migration from the
    /// old Settings › Appearance › App Icon: the first time the updated app is in front, the icon goes to the Avatar's. A change
    /// while a switch is in flight is not lost: that switch settles on the latest when it ends.
    func follow(_ look: AppIconChoice?, scheme: ColorSchemeChoice? = nil) async {
        if let look { wanted = look }
        if let scheme { self.scheme = scheme }
        guard let system, available, !switching else { return }
        await settle(system)
    }

    /// Switches until the icon is on the set for the latest `wanted` and `scheme`.
    private func settle(_ system: AppIconSetting) async {
        while let wanted, !switching {
            let name = wanted.iconName(for: scheme)
            let shown = system.alternateIconName
            // Reset removed alternate icons explicitly, even when the system reports a name absent from this build.
            if name == shown {
                current = wanted
                return
            }
            switching = true
            failure = nil
            do {
                try await system.setAlternateIconName(name)
                current = wanted
                switching = false
            } catch {
                switching = false
                failure = String(localized: "Couldn't change the app icon: \(error.localizedDescription)")
                return
            }
        }
    }
}

extension AppIconChoice {
    /// The system's look, the home screen's: read off the screen, as the window carries the app's own Light & Dark.
    @MainActor static var systemLook: ColorScheme {
        #if os(visionOS)
        return .dark // visionOS draws its windows on glass in the room: there is no light home screen to follow
        #else
        let screen = UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.screen }.first
        return screen?.traitCollection.userInterfaceStyle == .dark ? .dark : .light
        #endif
    }
}

extension AppIconModel {
    /// The app's own: UIApplication's.
    static func live() -> AppIconModel {
        #if DEBUG
        // Unit hosts must not read or change LaunchServices icons; an in-flight system switch can block its queue.
        // UI-test target apps are not injected XCTest hosts and still use the real UIApplication below.
        if ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] != nil {
            return AppIconModel(system: nil)
        }
        // UI tests that switch Light & Dark (-ui.scheme) keep the icon as it is (-showSameIcon YES): the switch would raise
        // the system's "You have changed the icon" alert over the page, and nothing in a test answers it.
        if UserDefaults.standard.bool(forKey: "showSameIcon") {
            return AppIconModel(system: nil)
        }
        #endif
        return AppIconModel(system: UIApplication.shared)
    }
}
