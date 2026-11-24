import SwiftUI
import UIKit

/// The Theme Color on the app's windows (`tintColor`), under RootView's `.tint`. Not everything SwiftUI draws through UIKit
/// takes the environment's tint: the icons of a `List`'s rows (Settings' `Label`s) take the tint of the views they sit in,
/// so after the tree was rebuilt for a new look under the open Settings sheet, or a page of it was pushed and popped, they
/// came back in the window's, the AccentColor asset (system blue) whatever the Theme Color. With the window in the Theme Color,
/// that tint is the right one too (#1188).
enum WindowTint {
    /// On every window of every scene (an iPad can have several); RootView runs it when it shows and on every new look.
    @MainActor static func apply() {
        let tint = UIColor(Theme.accent)
        for case let scene as UIWindowScene in UIApplication.shared.connectedScenes {
            for w in scene.windows { w.tintColor = tint }
        }
    }
}
