import UIKit

#if os(iOS)
/// Which ways the app turns (#3075): an iPhone stays upright except while the live screen's page is up, which also
/// turns to either landscape; an iPad turns every way. The app delegate answers with `mask`, which replaces Info.plist's
/// lists (an iPhone's says upright: it launches upright however it is held), so both are spelled out here.
@MainActor enum AppOrientation {
    /// A page that may turn is up: the live screen's (the screen-share module's window).
    private(set) static var screenUp = false
    /// The way the page's rotate button turned it (#6196): it stays so, however the phone is held, until the button
    /// turns it back or the page closes. Nil: it turns with the phone.
    private(set) static var screenTurned: UIInterfaceOrientationMask?
    static var phone: Bool { UIDevice.current.userInterfaceIdiom == .phone }

    static var mask: UIInterfaceOrientationMask {
        guard phone else { return .all }
        guard screenUp else { return .portrait }
        return screenTurned ?? .allButUpsideDown
    }

    /// The live screen's page came up (landscape allowed: the phone turns to how it is held) or went (back upright).
    static func screen(_ up: Bool, in scene: UIWindowScene?) {
        guard up != screenUp else { return }
        screenUp = up
        screenTurned = nil
        guard let scene else { return }
        reread(scene)
        // Closed on its side: upright again now, not when the phone next turns.
        if !up, phone, scene.effectiveGeometry.interfaceOrientation != .portrait {
            scene.requestGeometryUpdate(.iOS(interfaceOrientations: .portrait))
        }
    }

    /// The page's rotate button (#6196, iPhone only): the page on its side, or upright again, and kept so while it is up.
    /// Works with Portrait Orientation Lock on, where turning the phone does nothing.
    static func turnScreen(landscape: Bool, in scene: UIWindowScene?) {
        guard phone, screenUp, let scene else { return }
        let to: UIInterfaceOrientationMask = landscape ? .landscape : .portrait
        screenTurned = to
        reread(scene)
        scene.requestGeometryUpdate(.iOS(interfaceOrientations: to))
    }

    /// UIKit asks the shell's controllers, and what they present (a machine's page in a sheet), not the screen window's:
    /// every one of them reads the policy again.
    private static func reread(_ scene: UIWindowScene) {
        for w in scene.windows {
            var c = w.rootViewController
            while let v = c {
                v.setNeedsUpdateOfSupportedInterfaceOrientations()
                c = v.presentedViewController
            }
        }
    }
}

#endif
