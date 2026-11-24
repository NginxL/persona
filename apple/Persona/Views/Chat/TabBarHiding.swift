import SwiftUI
import UIKit

extension View {
    /// Hides the tab bar while this pushed page shows, the UIKit way: animated, with the push.
    ///
    /// `.toolbar(.hidden, for: .tabBar)` on a pushed page took the tab bar away on the push's first frame, before
    /// anything moved, and the page underneath lost its navigation bar and search field in the same frame: opening a
    /// chat from the Agents list flashed (2026-10-31; filmed with `simctl io recordVideo`, `OpenChatTabBarTests`).
    func hidesTabBarWhenShown(_ hides: Bool = true) -> some View {
        background { if hides { TabBarHider() } }
    }
}

private struct TabBarHider: UIViewControllerRepresentable {
    func makeUIViewController(context: Context) -> Probe { Probe() }
    func updateUIViewController(_ vc: Probe, context: Context) {}

    final class Probe: UIViewController {
        /// Whether the bar was away already when the page came, to leave it so: putting it back here brought it back
        /// where the page under it kept it hidden (the open iPhone Duo's tabs before #7543, #2623: down the side again
        /// on Back).
        private var wasHidden: Bool?

        override func viewWillAppear(_ animated: Bool) {
            super.viewWillAppear(animated)
            // Once per stay: back from a full-screen page of its own the bar is this page's hidden one.
            if wasHidden == nil { wasHidden = tabBarController?.isTabBarHidden ?? false }
            tabBarController?.setTabBarHidden(true, animated: animated)
        }

        override func viewWillDisappear(_ animated: Bool) {
            super.viewWillDisappear(animated)
            // Covered by a full-screen page of its own (the terminal): still this page, the bar stays away.
            if view.window?.rootViewController?.presentedViewController != nil { return }
            tabBarController?.setTabBarHidden(wasHidden ?? false, animated: animated)
            wasHidden = nil
        }
    }
}
