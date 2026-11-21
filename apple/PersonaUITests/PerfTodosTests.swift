import XCTest

/// A tick on the To-dos list (#10465, a to-do's row ticked and taken back): the V1 list's tick, drawn at once and held on
/// the device for its Undo, against today's list's checkbox, which writes at once. The tick is tapped where it was found
/// before the frames start, so the test's own accessibility queries stay out of the measured window.
/// Mock: MOCK_TODO_VIEW=1 (by hand, as docs/apple/performance.md "Measuring" says; `perf-test.sh` has no suite for it yet).
final class PerfTodosTests: PerfTestCase {
    /// The list in the iPhone's own layout, scrolled until Renew passport's row is in sight; where its tick is.
    @MainActor private func tickPoint(v1: Bool) -> XCUICoordinate {
        launch(["-tab", "assistant", "-openShellPage", "todos", "-app.navigation", "separate", "-AppleLanguages", "(en)"]
               + (v1 ? ["-showTodosV1", "YES"] : []))
        if v1 { _ = see(app.descendants(matching: .any)["todos.v1"].firstMatch, "the V1 list", timeout: 60) }
        let row = app.descendants(matching: .any)["todos.row.t11"].firstMatch
        for _ in 0..<6 where !(row.exists && row.isHittable) { app.collectionViews.firstMatch.swipeUp(velocity: .slow) }
        _ = see(row, "Renew passport's row")
        sleep(1)
        // A point on the window, not on the element: a coordinate on an element resolves it in the app at each tap, which
        // costs the app's main thread its accessibility snapshot inside the measured window.
        let origin = app.coordinate(withNormalizedOffset: .zero)
        if v1 {
            let tick = see(app.descendants(matching: .any)["todos.tick.t11"].firstMatch, "its tick").frame
            return origin.withOffset(CGVector(dx: tick.midX, dy: tick.midY))
        }
        // Today's row: its checkbox is the 44-pt square at the row's leading edge.
        let frame = row.frame
        return origin.withOffset(CGVector(dx: frame.minX + 22, dy: frame.minY + 22))
    }

    /// Each iteration ticks the row, or takes the tick back: the strike, the filled glyph and the toast.
    @MainActor func testTickFramesV1() throws {
        let point = tickPoint(v1: true)
        measureFrames(metrics()) { point.tap() }
    }

    /// The same tap on today's list (before): its checkbox closes or reopens the to-do at once.
    @MainActor func testTickFramesToday() throws {
        let point = tickPoint(v1: false)
        measureFrames(metrics()) { point.tap() }
    }
}
