import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

enum Keyboard {
    /// Closes the keyboard of whatever field has it (the terminal's own input included: callers decide where this runs).
    static func dismiss() {
        #if canImport(UIKit)
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        #endif
    }
}

/// Whether the on-screen keyboard is up (a hardware keyboard's bar of shortcuts does not count). Always false where
/// there is none (the Mac). The pending cards take the conversation's room while it is up (#1912).
@Observable final class KeyboardState {
    static let shared = KeyboardState()
    /// Docked at the bottom of the screen: an undocked one (`isUndocked`) floats over the page and counts as down.
    private(set) var isUp = false
    /// The iPad's keyboard is undocked: floating (or split) over the page, its frame ending above the bottom of the screen
    /// (#2565). The page keeps its height then (`keepsHeightUnderUndockedKeyboard`), as UIKit's keyboard layout guide does
    /// by default: SwiftUI's keyboard safe area took the floating keyboard's height off the bottom of the page as if it
    /// were docked, and the composer stood that far up, half under the keyboard.
    private(set) var isUndocked = false
    /// Taller than a hardware keyboard's bar, shorter than any on-screen keyboard.
    nonisolated private static let least: CGFloat = 120

    private init() {
        #if os(iOS)
        let center = NotificationCenter.default
        let notes: [(Notification.Name, Note)] = [
            (UIResponder.keyboardWillShowNotification, .show),
            (UIResponder.keyboardWillHideNotification, .hide),
            // Undocking says Will Hide with the floating keyboard's frame; focusing a field while it floats and docking
            // it again change the frame first.
            (UIResponder.keyboardWillChangeFrameNotification, .change),
        ]
        for (name, kind) in notes {
            center.addObserver(forName: name, object: nil, queue: .main) { @Sendable note in
                let (frame, screen) = KeyboardState.frame(note)
                MainActor.assumeIsolated { KeyboardState.shared.apply(kind, frame: frame, screen: screen) }
            }
        }
        #endif
    }

    /// Which keyboard notification came.
    enum Note: Sendable { case show, hide, change }

    /// The state after a keyboard notification, from its end frame in the coordinates of the screen it is on. What an
    /// iPad (iOS 27.2) says: undocking, Will Change Frame and Will Hide with the floating frame; putting the floating
    /// keyboard away and bringing it back, only Will Change Frame (no size, then the floating frame); docking it again,
    /// Will Change Frame and Will Show with the docked frame.
    /// - Will Show: up unless the keyboard is a hardware keyboard's bar or undocked.
    /// - Will Hide: down, and still undocked if it was: undocking itself says Will Hide, and a floating keyboard comes
    ///   back floating. The next frame on the screen says where it is.
    /// - Will Change Frame: undocked or docked, as the frame says (nothing for a keyboard going away). Undocked counts as
    ///   down; a docked frame after a floating one is the keyboard docked again, up, whether Will Show follows or not.
    nonisolated static func after(_ note: Note, frame: CGRect, screen: CGRect?, up: Bool,
                                  undocked: Bool) -> (up: Bool, undocked: Bool) {
        let floats = Self.undocked(frame, screen: screen)
        switch note {
        case .show: return (frame.height > least && floats != true, undocked)
        case .hide: return (false, undocked)
        case .change:
            guard let floats else { return (up, undocked) }
            return (!floats && (up || (undocked && frame.height > least)), floats)
        }
    }

    /// Whether a keyboard frame (the screen's coordinates) is undocked: an on-screen keyboard, taller than a hardware
    /// keyboard's bar, ending above the bottom of the screen. Nil for a keyboard going away, a frame with no height or
    /// wholly below the screen: it says neither (undocking also moves the docked keyboard off the screen, after the
    /// floating one's frame came).
    nonisolated static func undocked(_ frame: CGRect, screen: CGRect?) -> Bool? {
        guard let screen, frame.height > 0, frame.width > 0, frame.minY < screen.maxY else { return nil }
        return frame.height > least && frame.maxY < screen.maxY - 1
    }

    #if os(iOS)
    private func apply(_ note: Note, frame: CGRect, screen: CGRect?) {
        let next = Self.after(note, frame: frame, screen: screen ?? Self.appScreen, up: isUp, undocked: isUndocked)
        if isUp != next.up { isUp = next.up }
        if isUndocked != next.undocked { isUndocked = next.undocked }
    }

    /// The keyboard's frame at the end of the change and the bounds of the screen it is on (the notification's object).
    nonisolated private static func frame(_ note: Notification) -> (CGRect, CGRect?) {
        (note.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect ?? .zero, (note.object as? UIScreen)?.bounds)
    }

    /// The screen the app is on, for a keyboard notification that does not name its screen: the foreground scene's,
    /// or any scene's before one is active.
    private static var appScreen: CGRect? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        return (scenes.first { $0.activationState == .foregroundActive } ?? scenes.first)?.screen.bounds
    }
    #endif
}

extension View {
    /// Keeps the page's height while the iPad's keyboard is undocked (`KeyboardState.isUndocked`, #2565): the composer
    /// stays at the bottom and the floating keyboard floats over the page, as UIKit's keyboard layout guide does by
    /// default. A docked keyboard still makes the page shorter, as before.
    func keepsHeightUnderUndockedKeyboard() -> some View {
        #if os(iOS)
        modifier(UndockedKeyboardHeight())
        #else
        self
        #endif
    }
}

#if os(iOS)
private struct UndockedKeyboardHeight: ViewModifier {
    func body(content: Content) -> some View {
        content.ignoresSafeArea(KeyboardState.shared.isUndocked ? .keyboard : [], edges: .bottom)
    }
}
#endif

extension View {
    /// A tap on the page, anywhere the content did not take it, closes the keyboard. Runs next to the content's own
    /// gestures (`simultaneousGesture`), so rows and buttons still get their taps.
    func dismissesKeyboardOnTap() -> some View {
        #if canImport(UIKit)
        simultaneousGesture(TapGesture().onEnded { Keyboard.dismiss() })
        #else
        self
        #endif
    }
}
