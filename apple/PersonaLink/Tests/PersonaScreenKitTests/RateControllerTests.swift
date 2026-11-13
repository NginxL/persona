import PersonaScreenKit
import XCTest

/// Bandwidth adaptation from receiver reports (#3954, docs/extensions/computer-use.md "Adaptation"): cut 30% over
/// 150 ms of queueing, skip frames over 500 ms, raise 10% after 2 s under 50 ms, never past the phone's cap.
final class RateControllerTests: XCTestCase {
    /// A report whose one-way delay is `offset` (the clocks' difference, any size) plus `queue` ms of queueing.
    private func report(at now: UInt64, queue ms: Int64, offset: Int64 = 7_000_000_000, decodingError: Bool = false) -> ReceiverReport {
        let capture = now
        let arrival = UInt64(Int64(capture) + offset + ms * 1000)
        return ReceiverReport(lastFrame: 1, captureMicros: capture, arrivalMicros: arrival, bytesSinceLast: 0,
                              flags: decodingError ? .decodingError : [])
    }

    private let second: UInt64 = 1_000_000

    func testStartsAtTheStartBitrateUnderACap() {
        XCTAssertEqual(RateController(cap: 12_000).kbps, 4_000)
        XCTAssertEqual(RateController(cap: 1_500).kbps, 1_500)
    }

    func testRaisesTenPercentAfterTwoCalmSecondsNeverPastTheCap() {
        var rate = RateController(cap: 4_500)
        var now: UInt64 = 10 * second
        var raises: [Int] = []
        for _ in 0..<(7 * 60) { // 7 s of reports at 60 fps, all calm
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 5), now: now).kbps { raises.append(k) }
        }
        XCTAssertEqual(raises, [4_400, 4_500], "+10% each 2 calm seconds, then held at the cap")
        XCTAssertEqual(rate.kbps, 4_500)
    }

    func testTheClocksOffsetDoesNotCount() {
        var rate = RateController(cap: 12_000)
        var now: UInt64 = 10_000 * second // late enough that a negative offset keeps arrivals positive
        // A huge constant offset, negative even: the floor cancels it, so this is calm, not congested.
        for _ in 0..<130 {
            now += second / 60
            let decision = rate.receive(report(at: now, queue: 0, offset: -3_600_000_000), now: now)
            XCTAssertFalse((decision.kbps ?? 4_000) < 4_000, "never cut")
        }
        XCTAssertGreaterThan(rate.kbps, 4_000, "raised after 2 calm seconds")
    }

    func testCutsThirtyPercentOnceForOneCongestedMoment() {
        var rate = RateController(cap: 12_000)
        var now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now) // the floor
        var cuts: [Int] = []
        for _ in 0..<20 { // 20 reports over ~330 ms, all 200 ms queued
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 200), now: now).kbps { cuts.append(k) }
        }
        XCTAssertEqual(cuts, [2_800], "one cut of 30%, held for 500 ms")
        for _ in 0..<20 { // still congested past the hold: cut again
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 200), now: now).kbps { cuts.append(k) }
        }
        XCTAssertEqual(cuts, [2_800, 1_960])
    }

    func testNeverCutsBelowTheFloor() {
        var rate = RateController(cap: 300)
        var now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now)
        var kbps: [Int] = []
        for _ in 0..<40 { // 4 s of congestion, inside the 10 s window, so the floor stays the 0 ms one
            now += 100_000
            if let k = rate.receive(report(at: now, queue: 300), now: now).kbps { kbps.append(k) }
        }
        XCTAssertEqual(kbps, [210, CapturePlan.minKbps], "cut to 210, then clamped at the floor and held")
        XCTAssertEqual(rate.kbps, CapturePlan.minKbps)
    }

    func testSkipsFramesOverHalfASecondAndProbesUntilItDrains() {
        var rate = RateController(cap: 12_000)
        var now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now)
        XCTAssertTrue(rate.admit(now: now))
        now += 10_000
        XCTAssertTrue(rate.receive(report(at: now, queue: 600), now: now).skipping)

        // While skipping, one frame per 250 ms goes, so reports keep coming.
        var admitted = 0
        for _ in 0..<60 { // one second of 60 fps frames
            now += second / 60
            if rate.admit(now: now) { admitted += 1 }
        }
        XCTAssertEqual(admitted, 4, "a probe each 250 ms")

        now += 10_000
        XCTAssertTrue(rate.receive(report(at: now, queue: 300), now: now).skipping, "300 ms: still draining")
        now += 10_000
        XCTAssertFalse(rate.receive(report(at: now, queue: 100), now: now).skipping, "under 150 ms: drained")
        XCTAssertTrue(rate.admit(now: now + 1) && rate.admit(now: now + 2), "every frame goes again")
    }

    func testADecodingErrorAsksForAKeyframe() {
        var rate = RateController(cap: 12_000)
        let now: UInt64 = 10 * second
        XCTAssertFalse(rate.receive(report(at: now, queue: 0), now: now).keyframe)
        XCTAssertTrue(rate.receive(report(at: now + 1_000, queue: 0, decodingError: true), now: now + 1_000).keyframe)
    }

    func testALowerCapTakesEffectAtOnce() {
        var rate = RateController(cap: 12_000)
        XCTAssertEqual(rate.setCap(1_500), 1_500, "below the current bitrate: lowered now")
        XCTAssertEqual(rate.kbps, 1_500)
        XCTAssertNil(rate.setCap(12_000), "a higher cap is reached by raising")
        XCTAssertEqual(rate.kbps, 1_500)
    }

    func testAnOldFloorAgesOut() {
        var rate = RateController(cap: 12_000)
        let start: UInt64 = 10 * second
        _ = rate.receive(report(at: start, queue: 0), now: start) // the floor
        // 180 ms above that floor, once a second: congested, so it cuts each time, down to the minimum...
        for s in 1...10 {
            let now = start + UInt64(s) * second
            _ = rate.receive(report(at: now, queue: 180), now: now)
        }
        XCTAssertEqual(rate.kbps, CapturePlan.minKbps)
        // ...until the floor is more than 10 s old: then 180 ms is the floor itself (a path or clock that moved), and
        // the queue reads empty, so there is no cut, and after 2 calm seconds a raise.
        for s in 11...12 {
            let now = start + UInt64(s) * second
            XCTAssertNil(rate.receive(report(at: now, queue: 180), now: now).kbps, "second \(s): no cut")
        }
        let now = start + 13 * second
        XCTAssertEqual(rate.receive(report(at: now, queue: 180), now: now).kbps, 220, "raised 10% after 2 calm seconds")
    }
}
