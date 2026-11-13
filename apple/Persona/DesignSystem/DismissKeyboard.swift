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
        center.addObserver(forName: UIResponder.keyboardWillShowNotification, object: nil, queue: .main) { @Sendable note in
            let (frame, screen) = KeyboardState.frame(note)
            MainActor.assumeIsolated {
                KeyboardState.shared.isUp = frame.height > KeyboardState.least && KeyboardState.undocked(frame, screen: screen) != true
            }
        }
        center.addObserver(forName: UIResponder.keyboardWillHideNotification, object: nil, queue: .main) { @Sendable _ in
            MainActor.assumeIsolated { KeyboardState.shared.isUp = false }
        }
        // Undocking says Will Hide with the floating keyboard's frame; focusing a field while it floats and docking it
        // again change the frame first.
        center.addObserver(forName: UIResponder.keyboardWillChangeFrameNotification, object: nil, queue: .main) { @Sendable note in
            let (frame, screen) = KeyboardState.frame(note)
            MainActor.assumeIsolated {
                guard let undocked = KeyboardState.undocked(frame, screen: screen) else { return }
                let state = KeyboardState.shared
                if state.isUndocked != undocked { state.isUndocked = undocked }
                if undocked, state.isUp { state.isUp = false }
            }
        }
        #endif
    }

    /// Whether a keyboard frame (the screen's coordinates) is undocked: an on-screen keyboard, taller than a hardware
    /// keyboard's bar, ending above the bottom of the screen. Nil for a frame with no height, a keyboard going away
    /// (undocking also moves the docked one off the screen, after the floating one's frame came): it says neither.
    nonisolated static func undocked(_ frame: CGRect, screen: CGRect?) -> Bool? {
        guard let screen, frame.height > 0, frame.width > 0 else { return nil }
        return frame.height > least && frame.maxY < screen.maxY - 1
    }

    #if os(iOS)
    /// The keyboard's frame at the end of the change and the bounds of the screen it is on (the notification's object).
    nonisolated private static func frame(_ note: Notification) -> (CGRect, CGRect?) {
        (note.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect ?? .zero, (note.object as? UIScreen)?.bounds)
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
