import XCTest
@testable import PersonaScreenKit

/// The helper's side of the overlay's latency (#3954): medians over a second, the network as half the round trip.
final class TimingWindowTests: XCTestCase {
    func testASecondsMedians() {
        var w = TimingWindow()
        let encodes: [Int64] = [4_000, 5_000, 6_000, 7_000, 30_000]
        for (i, e) in encodes.enumerated() {
            w.sent(number: UInt32(i + 1), at: 1_000_000 + UInt64(i) * 200_000, captureMicros: -4_500, encodeMicros: e, bytes: 25_000)
        }
        // Reports on three of them, 10, 12 and 14 ms after each went out.
        w.reported(frame: 1, at: 1_010_000)
        w.reported(frame: 2, at: 1_212_000)
        w.reported(frame: 3, at: 1_414_000)
        let t = w.take(now: 2_000_000)
        XCTAssertEqual(t, TimingMessage(captureMs: 0, encodeMs: 6, networkMs: 6, kbps: 1000, fps: 5),
                       "capture before display time counts as 0; one slow encode doesn't move the median")
    }

    func testNothingWentOutIsNoTiming() {
        var w = TimingWindow()
        XCTAssertNil(w.take(now: 5_000_000))
        w.sent(number: 1, at: 5_100_000, captureMicros: 0, encodeMicros: 3_000, bytes: 100)
        XCTAssertNotNil(w.take(now: 6_000_000))
        XCTAssertNil(w.take(now: 7_000_000), "a still screen: nothing to say")
    }

    func testAReportCountsOnceAndOnlyForAFrameItKnows() {
        var w = TimingWindow()
        w.sent(number: 1, at: 1_000, captureMicros: 0, encodeMicros: 1_000, bytes: 10)
        w.reported(frame: 1, at: 21_000)
        w.reported(frame: 1, at: 99_000)
        w.reported(frame: 77, at: 50_000)
        XCTAssertEqual(w.take(now: 1_001_000)?.networkMs, 10)
    }

    func testOldFramesAreForgotten() {
        var w = TimingWindow()
        for n in 1 ... UInt32(TimingWindow.remembered + 10) {
            w.sent(number: n, at: UInt64(n) * 1_000, captureMicros: 0, encodeMicros: 1_000, bytes: 1)
        }
        w.reported(frame: 1, at: 900_000) // long gone
        XCTAssertNil(w.take(now: 1_000_000)?.networkMs)
    }

    func testTheWindowStartsOverAtEachTake() {
        var w = TimingWindow()
        w.sent(number: 1, at: 0, captureMicros: 0, encodeMicros: 9_000, bytes: 1_000)
        _ = w.take(now: 1_000_000)
        w.sent(number: 2, at: 1_500_000, captureMicros: 0, encodeMicros: 3_000, bytes: 1_000)
        w.sent(number: 3, at: 1_600_000, captureMicros: 0, encodeMicros: 5_000, bytes: 1_000)
        let t = w.take(now: 2_000_000)
        XCTAssertEqual(t?.encodeMs, 4, "only this second's frames")
        XCTAssertEqual(t?.fps, 2)
        XCTAssertEqual(t?.kbps, 16)
    }
}
