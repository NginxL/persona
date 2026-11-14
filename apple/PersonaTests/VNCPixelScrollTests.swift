import CoreGraphics
import Foundation
import Testing
@testable import Persona

/// Two fingers as pixel scroll steps for the Mac's Persona Input (#7775, VNCPixelScroll): the trackpad's phases in order,
/// the same distances as the wheel's curve without its ceiling, the coast as momentum, a touch ending it; and what a Mac
/// says it can do (`screen_input`).
struct VNCPixelScrollTests {
    /// `points` in moves of `each` points at `speed` points a second, a display frame (60 a second) after each, then the
    /// lift at `lift` points a second and frames until nothing is left: every step.
    private func gesture(_ points: CGFloat, speed: CGFloat = 120, each: CGFloat = 2, perPoint: CGFloat = 1,
                         lift: CGFloat = 0) -> [VNCPixelScroll.Step] {
        var p = VNCPixelScroll()
        p.began()
        var steps: [VNCPixelScroll.Step] = []
        var gone: CGFloat = 0
        while abs(gone) < abs(points) {
            let d = points < 0 ? -each : each
            p.moved(d, velocity: points < 0 ? -speed : speed, perPoint: perPoint)
            if let s = p.frame(1.0 / 60) { steps.append(s) }
            gone += d
        }
        p.released(velocity: lift, perPoint: perPoint)
        var frames = 0
        while p.busy, frames < 2000 {
            if let s = p.frame(1.0 / 60) { steps.append(s) }
            frames += 1
        }
        return steps
    }

    @Test func aSlowDragIsBeganChangedEndedAndFollowsTheFingers() {
        let steps = gesture(100, perPoint: 4.9)
        #expect(steps.first?.phase == .began && steps.last?.phase == .ended)
        #expect(steps.dropFirst().dropLast().allSatisfy { $0.phase == .changed && $0.momentum == .none })
        #expect(abs(steps.reduce(0) { $0 + $1.dy } - 490) < 0.001, "100 pt over the picture, 490 Mac points")
        #expect(steps.count == 51, "a step a frame, then the end: \(steps.count)")
        // Up the screen shows what is below: negative.
        #expect(gesture(-100).reduce(0) { $0 + $1.dy } < 0)
    }

    /// Quick fingers go further by the wheel's curve, with no ceiling: one step a frame carries any distance.
    @Test func quickFingersGoFurtherWithNoCeiling() {
        let fast = gesture(300, speed: 1500, each: 25, perPoint: 4.9)
        let total = fast.filter { $0.momentum == .none }.reduce(0) { $0 + $1.dy }
        #expect(abs(total - 300 * VNCWheel.gain(1500) * 4.9) < 0.01, "\(total)")
        #expect((fast.map { abs($0.dy) }.max() ?? 0) > CGFloat(VNCWheel.perFrame(1.0 / 60)), "more than the wheel's 33 in a frame")
    }

    /// After a flick: ended, then the coast as momentum (begin, continue…, end), as far as UIScrollView's deceleration
    /// takes it from the speed the fingers went at; past coastMost it dies away faster.
    @Test func aFlickCoastsAsMomentum() {
        let steps = gesture(40, lift: 600)
        guard let end = steps.firstIndex(where: { $0.phase == .ended }) else { Issue.record("no ended step"); return }
        let coast = Array(steps[(end + 1)...])
        #expect(coast.first?.momentum == .begin && coast.last?.momentum == .end)
        #expect(coast.dropFirst().dropLast().allSatisfy { $0.momentum == .continue && $0.phase == .none })
        let distance = coast.reduce(0) { $0 + $1.dy }
        let tau = -1 / log(VNCWheel.decay) / 1000
        #expect(abs(distance - 600 * VNCWheel.gain(600) * tau) < 600 * VNCWheel.gain(600) * tau * 0.05, "\(distance)")
        let hard = gesture(40, perPoint: 4.9, lift: 5000)
        let far = hard.filter { $0.momentum != .none }.reduce(0) { $0 + $1.dy }
        #expect(far > 3000 && far <= VNCPixelScroll.coastMost, "\(far)")
        #expect(gesture(40, lift: 200).allSatisfy { $0.momentum == .none }, "a slow lift doesn't coast")
    }

    /// A touch ends a coast where it is (its end step goes); moved and let go within a frame still gives began, then ended.
    @Test func aTouchEndsTheCoastAndAQuickLiftStillEnds() {
        var p = VNCPixelScroll()
        p.began()
        p.moved(10, velocity: 900, perPoint: 1)
        #expect(p.frame(1.0 / 60)?.phase == .began)
        p.released(velocity: 900, perPoint: 1)
        #expect(p.frame(1.0 / 60)?.phase == .ended)
        #expect(p.frame(1.0 / 60)?.momentum == .begin)
        #expect(p.stop() == VNCPixelScroll.Step(dy: 0, momentum: .end))
        #expect(!p.busy && p.frame(1.0 / 60) == nil && p.stop() == nil)

        p.began()
        p.moved(6, velocity: 100, perPoint: 1)
        p.released(velocity: 100, perPoint: 1)
        #expect(p.frame(1.0 / 60) == VNCPixelScroll.Step(dy: 6, phase: .began))
        #expect(p.frame(1.0 / 60) == VNCPixelScroll.Step(dy: 0, phase: .ended))
        #expect(!p.busy)

        p.began() // a touch that never scrolled: nothing at all
        p.released(velocity: 0, perPoint: 1)
        #expect(p.frame(1.0 / 60) == nil && !p.busy)
    }

    /// What a Mac says: its node reads records (version 1) and whether Persona Input may post them; an unreadable value
    /// costs only pixel scrolling, not the machine.
    @Test func aMacSaysWhetherItScrollsByPixels() throws {
        func computer(_ json: String) throws -> MachineComputer {
            try JSONDecoder().decode(MachineComputer.self, from: Data(json.utf8))
        }
        let granted = try computer(#"{"enabled":true,"screen_sharing":true,"screen_input":{"version":1,"permission":"granted"}}"#)
        #expect(granted.screenInput?.granted == true && granted.screenInput?.asked == false)
        let missing = try computer(#"{"enabled":true,"screen_input":{"version":1,"permission":"missing","asked":true}}"#)
        #expect(missing.screenInput?.granted == false && missing.screenInput?.reads == true && missing.screenInput?.asked == true)
        #expect(try computer(#"{"enabled":true}"#).screenInput == nil, "an older node")
        let odd = try computer(#"{"enabled":true,"screen_sharing":true,"screen_input":"yes"}"#)
        #expect(odd.screenSharing && odd.screenInput == nil)
        let browser = try JSONDecoder().decode(MachineBrowser.self, from: Data(#"{"enabled":true,"screen_input":{"version":1,"permission":"granted"}}"#.utf8))
        #expect(browser.screenInput?.granted == true)
    }
}
