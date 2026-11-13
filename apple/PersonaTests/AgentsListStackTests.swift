import Darwin
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The Agents page fits an iPhone's main-thread stack in a Debug build (#6735). On a device that stack is about 1 MB; the
/// simulator's is 8 MB, so a page that needs most of a megabyte only crashes on the phone (every launch into Agents,
/// `EXC_BAD_ACCESS` in the stack guard). Unoptimized, a function reserves a stack slot on entry for every opaque
/// intermediate of a modifier chain, each holding everything before it: `AgentsList`'s ~45 modifiers took 422 KB in one
/// frame, and the first render reached 941 KB (448 KB after the fix). This measures how deep the page's first render
/// reaches into the stack instead of waiting for it to overflow.
@MainActor struct AgentsListStackTests {
    /// The device's main-thread stack is 1008 KB, and the run loop, UIKit and the attribute graph sit below the page's
    /// body when it renders. Half of it leaves room for both.
    static let budget = 512 * 1024

    @Test func theAgentsPageRendersWellWithinAPhonesMainThreadStack() async throws {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "agents-stack-" + UUID().uuidString))
        let host = UIHostingController(rootView: ChatsView().environment(model))
        let used = StackPeak.measure {
            window.rootViewController = host
            window.isHidden = false
            window.layoutIfNeeded()
        }
        #expect(Self.collectionViews(in: window) > 0, "the Agents list did not render, so the measure says nothing")
        #expect(used < Self.budget, "the Agents page's first render reached \(used / 1024) KB into the stack (budget \(Self.budget / 1024) KB)")
        print("AgentsListStackTests: first render reached \(used / 1024) KB of stack")
    }

    private static func collectionViews(in view: UIView) -> Int {
        (view is UICollectionView ? 1 : 0) + view.subviews.reduce(0) { $0 + collectionViews(in: $1) }
    }
}

/// How far below its caller's frame `body` reaches into this thread's stack: the unused stack is painted with a
/// pattern first, and the lowest word no longer holding it after `body` is the deepest point reached.
enum StackPeak {
    private static let pattern: UInt64 = 0x5EED_5EED_5EED_5EED

    @inline(never) static func measure(_ body: () -> Void) -> Int {
        let thread = pthread_self()
        let top = UInt(bitPattern: pthread_get_stackaddr_np(thread))
        let bottom = top - UInt(pthread_get_stacksize_np(thread))
        var marker = 0
        let here = withUnsafeMutablePointer(to: &marker) { UInt(bitPattern: $0) }
        // Below this frame and the calls that paint and scan; above the guard pages.
        let start = (here - 32 * 1024) & ~UInt(7)
        let end = (bottom + 64 * 1024) & ~UInt(7)
        paint(from: end, to: start)
        body()
        return Int(here - lowestTouched(from: end, to: start))
    }

    @inline(never) private static func paint(from low: UInt, to high: UInt) {
        var p = low
        while p < high { UnsafeMutablePointer<UInt64>(bitPattern: p)!.pointee = pattern; p += 8 }
    }

    @inline(never) private static func lowestTouched(from low: UInt, to high: UInt) -> UInt {
        var p = low
        while p < high, UnsafeMutablePointer<UInt64>(bitPattern: p)!.pointee == pattern { p += 8 }
        return p
    }
}
