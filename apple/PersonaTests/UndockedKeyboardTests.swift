import CoreGraphics
import Testing
import UIKit
@testable import Persona

/// Which keyboard frames float over the page (#2565): the page keeps its height under those, and a docked keyboard
/// still makes it shorter. Most frames are ones an iPad Pro 11-inch simulator (iOS 27.2) sent.
struct UndockedKeyboardTests {
    private let portrait = CGRect(x: 0, y: 0, width: 834, height: 1210)
    private let landscape = CGRect(x: 0, y: 0, width: 1210, height: 834)

    @Test func aDockedKeyboardIsNotUndocked() {
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 870, width: 834, height: 340), screen: portrait) == false)
        // The suggestion bar's height still coming in.
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 886, width: 834, height: 324), screen: portrait) == false)
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 406, width: 1210, height: 428), screen: landscape) == false)
    }

    @Test func aFloatingKeyboardIsUndocked() {
        // Undocked while the composer had it, and the composer focused while it floated.
        #expect(KeyboardState.undocked(CGRect(x: 250, y: 697, width: 334, height: 349.5), screen: portrait) == true)
        #expect(KeyboardState.undocked(CGRect(x: 250, y: 712.5, width: 334, height: 334), screen: portrait) == true)
        // Dragged down to just above the bottom of the screen, it still floats.
        #expect(KeyboardState.undocked(CGRect(x: 250, y: 860, width: 334, height: 334), screen: portrait) == true)
        #expect(KeyboardState.undocked(CGRect(x: 437, y: 321, width: 334, height: 349.5), screen: landscape) == true)
        #expect(KeyboardState.undocked(CGRect(x: 437, y: 336.5, width: 334, height: 334), screen: landscape) == true)
    }

    /// A hardware keyboard's bar of shortcuts is never an undocked keyboard, at the bottom of the screen or not: the
    /// page stays as it was with one.
    @Test func aHardwareKeyboardsBarIsNotUndocked() {
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 1155, width: 834, height: 55), screen: portrait) == false)
        #expect(KeyboardState.undocked(CGRect(x: 700, y: 1100, width: 120, height: 60), screen: portrait) == false)
    }

    /// Part of the keyboard below the screen (on its way down) is docked.
    @Test func aKeyboardGoingDownIsDocked() {
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 1000, width: 834, height: 340), screen: portrait) == false)
    }

    /// A keyboard going away says neither, so the state stays as the last frame on the screen left it: no height
    /// (undocking also moves the docked keyboard off the screen, after the floating one's frame came), wholly below the
    /// screen (a docked keyboard put away), or no screen.
    @Test func aKeyboardGoingAwaySaysNeither() {
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 1210, width: 834, height: 0), screen: portrait) == nil)
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 1210, width: 834, height: 340), screen: portrait) == nil)
        #expect(KeyboardState.undocked(CGRect(x: 0, y: 834, width: 1210, height: 428), screen: landscape) == nil)
        #expect(KeyboardState.undocked(.zero, screen: portrait) == nil)
        #expect(KeyboardState.undocked(CGRect(x: 250, y: 697, width: 334, height: 349.5), screen: nil) == nil)
    }

    // What an iPad Pro 11-inch simulator (iOS 27.2) said, portrait.
    private let docked = CGRect(x: 0, y: 870, width: 834, height: 340)
    private let suggestionsComing = CGRect(x: 0, y: 886, width: 834, height: 324)
    private let undocking = CGRect(x: 250, y: 697, width: 334, height: 349.5)
    private let floating = CGRect(x: 250, y: 712.5, width: 334, height: 334)
    private let away = CGRect(x: 0, y: 1210, width: 834, height: 0)

    private func after(_ notes: [(KeyboardState.Note, CGRect)], from state: (up: Bool, undocked: Bool))
        -> (up: Bool, undocked: Bool) {
        notes.reduce(state) { KeyboardState.after($1.0, frame: $1.1, screen: portrait, up: $0.up, undocked: $0.undocked) }
    }

    /// Up docked, undocked while up, put away and focused again while it floats, docked again: the notifications an iPad
    /// sent, in order.
    @Test func floatingAndDockingAgain() {
        var state = after([(.change, docked), (.show, docked)], from: (false, false))
        #expect(state.up == true && state.undocked == false)
        // Undocking says Will Hide with the floating frame: the keyboard still floats (cleared there, the composer stood
        // up again as if it were docked).
        state = after([(.change, undocking), (.hide, undocking)], from: state)
        #expect(state.up == false && state.undocked == true)
        state = after([(.change, away), (.change, away), (.hide, away)], from: state)
        #expect(state.up == false && state.undocked == true)
        // Put away and brought back, it floats: no Will Hide or Will Show, a frame with no size and then the floating one.
        state = after([(.change, .zero), (.change, floating), (.change, floating)], from: state)
        #expect(state.up == false && state.undocked == true)
        // Docked again: up at the first docked frame, before the Will Show that follows it.
        state = after([(.change, suggestionsComing)], from: state)
        #expect(state.up == true && state.undocked == false)
        state = after([(.show, suggestionsComing), (.change, docked), (.show, docked)], from: state)
        #expect(state.up == true && state.undocked == false)
    }

    /// A docked frame brings the keyboard up only after a floating one: a frame says nothing of a docked keyboard that is
    /// down. A hardware keyboard's bar after a floating keyboard is docked, and down.
    @Test func aDockedFrameAloneDoesNotBringTheKeyboardUp() {
        let down = after([(.change, docked)], from: (false, false))
        #expect(!down.up && !down.undocked)
        let bar = CGRect(x: 0, y: 1155, width: 834, height: 55)
        let hardware = after([(.change, bar), (.show, bar)], from: (false, true))
        #expect(!hardware.up && !hardware.undocked)
    }

    /// A keyboard notification that does not name its screen is read against the screen the app is on, through the
    /// observers the app has from the start.
    @MainActor @Test func aNotificationWithoutItsScreenUsesTheAppsScreen() async throws {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let screen = try #require(scenes.first).screen.bounds
        let state = KeyboardState.shared
        func post(_ name: Notification.Name, _ frame: CGRect) async {
            NotificationCenter.default.post(name: name, object: nil, userInfo: [UIResponder.keyboardFrameEndUserInfoKey: frame])
            for _ in 0..<50 { await Task.yield() }
        }
        let floats = CGRect(x: 40, y: screen.maxY - 520, width: 320, height: 330)
        await post(UIResponder.keyboardWillChangeFrameNotification, floats)
        #expect(state.isUndocked && !state.isUp)
        let docks = CGRect(x: 0, y: screen.maxY - 340, width: screen.width, height: 340)
        await post(UIResponder.keyboardWillChangeFrameNotification, docks)
        #expect(!state.isUndocked && state.isUp)
        await post(UIResponder.keyboardWillHideNotification, CGRect(x: 0, y: screen.maxY, width: screen.width, height: 340))
        #expect(!state.isUndocked && !state.isUp)
    }
}
