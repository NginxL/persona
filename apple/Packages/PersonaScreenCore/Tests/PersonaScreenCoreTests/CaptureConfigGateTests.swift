import XCTest
@testable import PersonaScreenCore

/// A configuration change before capture has started waits for it, and goes once (#7856).
final class CaptureConfigGateTests: XCTestCase {
    func testAChangeBeforeCaptureStartsWaitsAndIsAppliedOnce() {
        var g = CaptureConfigGate()
        XCTAssertFalse(g.changed(), "the first tier's update, right after hello: not before capture runs")
        XCTAssertFalse(g.changed(), "a second change waits with it: the configuration applied is the latest")
        XCTAssertTrue(g.captureStarted(), "applied once capture has started")
        XCTAssertFalse(g.pending)
        XCTAssertTrue(g.changed(), "later changes go at once")
    }

    func testNothingWaitingAppliesNothingAtStart() {
        var g = CaptureConfigGate()
        XCTAssertFalse(g.captureStarted())
        XCTAssertTrue(g.started)
    }

    func testANewCaptureStartsOver() {
        var g = CaptureConfigGate()
        _ = g.captureStarted()
        g.reset()
        XCTAssertFalse(g.changed(), "a restarted capture (a new display, a resume) waits again")
        g.reset()
        XCTAssertFalse(g.captureStarted(), "a reset drops a waiting change: the new capture is made with the latest configuration")
    }
}
