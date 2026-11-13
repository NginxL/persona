import CoreGraphics
import Testing
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
}
