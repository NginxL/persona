import CoreGraphics
import Foundation

/// Two fingers turned into pixel scroll steps for the Mac's Persona Input (#7775, docs/extensions/computer-use.md "Pixel
/// scrolling"), when its node reads them and the helper may post them; else VNCWheel's notches. A step a display frame
/// carries that frame's distance in Mac points, with the trackpad's phases, so apps on the Mac scroll by the point with
/// their own momentum and rubber-banding:
/// - while the fingers are down, the distance as VNCWheel's curve has it (the content follows slow fingers one to one,
///   quick ones go further), but with no ceiling: one step a frame carries any distance; `began` first, then `changed`;
/// - at the lift `ended`; then, after a flick, the coast as momentum steps (`begin`, `continue`, `end`) dying away as a
///   UIScrollView's normal deceleration does; one that would go past `coastMost` dies away faster instead of starting
///   slower;
/// - a touch during the coast ends it.
nonisolated struct VNCPixelScroll {
    /// CGScrollPhase's values.
    enum Phase: UInt8, Sendable { case none = 0, began = 1, changed = 2, ended = 4, cancelled = 8 }
    /// CGMomentumScrollPhase's values.
    enum Momentum: UInt8, Sendable { case none = 0, begin = 1, `continue` = 2, end = 3 }

    /// One step: dy Mac points (> 0 shows what is above) and its phases.
    struct Step: Equatable, Sendable {
        var dy: CGFloat
        var phase: Phase = .none
        var momentum: Momentum = .none
    }

    /// The most Mac points a coast adds (a few screens).
    static let coastMost: CGFloat = 6000
    /// Below this (Mac points a second) the coast ends.
    static let stopSpeed: CGFloat = 20

    /// Points the fingers moved since the last step.
    private var owed: CGFloat = 0
    private var down = false
    private var begun = false // a began step went for this touch
    private var lifted = false // up, its ended step still to go
    private var momentumBegun = false
    /// The coast's speed, Mac points a second; 0: not coasting.
    private(set) var speed: CGFloat = 0
    /// What is left of the coasting speed after a millisecond (UIScrollView's normal deceleration, or less for a long one).
    private var dying = VNCWheel.decay

    var coasting: Bool { speed != 0 }
    /// Steps still to come: the display frames go on.
    var busy: Bool { down || lifted || coasting }

    /// Fingers down: anything before goes.
    mutating func began() {
        self = VNCPixelScroll()
        down = true
    }

    /// The fingers moved dy points down at velocity points a second, perPoint Mac points a view point.
    mutating func moved(_ dy: CGFloat, velocity: CGFloat, perPoint: CGFloat) {
        owed += dy * VNCWheel.gain(velocity) * max(perPoint, 0)
    }

    /// The fingers let go at velocity points a second: after the ended step, a coast if that is fast enough.
    mutating func released(velocity: CGFloat, perPoint: CGFloat) {
        down = false
        lifted = true
        guard abs(velocity) >= VNCWheel.flick else { return }
        let v = velocity * VNCWheel.gain(velocity) * max(perPoint, 0)
        let tau = -1 / log(VNCWheel.decay) / 1000
        dying = abs(v) * tau > Self.coastMost ? exp(-abs(v) / (Self.coastMost * 1000)) : VNCWheel.decay
        speed = abs(v) < Self.stopSpeed ? 0 : v
    }

    /// A display frame dt seconds long: the step to send now, if any.
    mutating func frame(_ dt: TimeInterval) -> Step? {
        if down {
            guard owed != 0 else { return nil }
            defer { owed = 0; begun = true }
            return Step(dy: owed, phase: begun ? .changed : .began)
        }
        if lifted {
            if !begun {
                guard owed != 0 else { // a touch that never scrolled
                    lifted = false
                    speed = 0
                    return nil
                }
                defer { owed = 0; begun = true }
                return Step(dy: owed, phase: .began) // moved and let go within a frame: ended comes next frame
            }
            lifted = false
            defer { owed = 0 }
            return Step(dy: owed, phase: .ended)
        }
        guard coasting else { return nil }
        let after = speed * pow(dying, CGFloat(dt) * 1000)
        let dy = (speed - after) * (-1 / log(dying) / 1000)
        speed = abs(after) < Self.stopSpeed ? 0 : after
        defer { momentumBegun = true }
        return Step(dy: dy, momentum: !momentumBegun ? .begin : coasting ? .continue : .end)
    }

    /// A touch: a coast ends where it is (its end step, if it had begun), and an ended step still owed goes now.
    mutating func stop() -> Step? {
        if lifted {
            lifted = false
            speed = 0
            return begun ? Step(dy: owed, phase: .ended) : nil
        }
        guard coasting else { return nil }
        speed = 0
        return momentumBegun ? Step(dy: 0, momentum: .end) : nil
    }
}
