import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The Sidebar layout's column lines its head and main conversation up with the list's rows (`AssistantSidebarUI.rowsLeading`,
/// measured on a row in the list). An iPhone Duo grown from split screen to full screen while an Agent's reply streamed
/// froze until the watchdog ended it (#12318): the list's frame swung between two widths 9 pt apart for ever inside one
/// layout pass, because the measured line moved the head, the head moved the list, and the list moved the line.
@MainActor struct SidebarRowsLineTests {
    /// A measure that only swings back to the one before it is the layout answering its own write, and is not written.
    @Test func aMeasureThatSwingsBackIsNotWrittenAgain() {
        var line = LayoutEchoGuard()
        let written = [(72.3, 0.0), (72.4, 0.01), (81.7, 0.02), (72.3, 0.03), (81.7, 0.04)].map { line.accepts($0.0, now: $0.1) }
        // The first line; the same line again; a new one; back to the one before at once (the echo the Duo looped on);
        // the line it kept.
        #expect(written == [true, false, true, false, false])
    }

    /// A real change is written: a new line, or the old one again after a while (turned there and back).
    @Test func aRealChangeIsWritten() {
        var line = LayoutEchoGuard()
        let written = [(16.0, 0.0), (88.0, 0.1), (16.0, 2.0), (24.0, 2.1)].map { line.accepts($0.0, now: $0.1) }
        #expect(written == [true, true, true, true])
    }

    /// However far in the line stands, the head and the main row never make the column wider than it is offered, so the
    /// line cannot move the list it is measured in (the frame the device's log showed swinging).
    @Test func theLineNeverMovesTheList() throws {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "rows-line-" + UUID().uuidString))
        let state = UnifiedSidebar()
        let column = UnifiedSidebarColumn(state: state) { rows in
            List { if let rows { rows } }.listStyle(.plain)
        }
        let host = UIHostingController(rootView: column.environment(model))
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 320, height: 640)
        window.rootViewController = host
        window.isHidden = false
        defer { window.isHidden = true }
        func listFrame() throws -> CGRect {
            window.layoutIfNeeded()
            let list = try #require(Self.collectionView(in: host.view), "no list in the column")
            return list.convert(list.bounds, to: window)
        }
        let before = try listFrame()
        // The line far past the column's edge: a head or a main row standing on it would widen the column.
        state.ui.placeRows(at: 400)
        let after = try listFrame()
        #expect(abs(after.minX - before.minX) < 0.5 && abs(after.width - before.width) < 0.5,
                "the list moved from \(before) to \(after) when the line moved")
        #expect(after.width <= 320.5, "the list took \(after.width) pt of a 320 pt column")
    }

    private static func collectionView(in view: UIView) -> UICollectionView? {
        if let list = view as? UICollectionView { return list }
        for sub in view.subviews { if let list = collectionView(in: sub) { return list } }
        return nil
    }
}
