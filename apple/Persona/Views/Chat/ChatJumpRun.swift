import PersonaKit
import UIKit

/// The chat's animated jumps to its newest end, run on its scroll view itself: ↓ Jump to Latest (#3027), and since #5076
/// the others too (after sending, following, a held row let go; DEBUG `-showAutoScroll`'s sweep goes to offsets the same
/// way). Jumps to a line, a quote or a call keep SwiftUI's animated scroll (docs/apple/ios-state.md「Every other jump
/// (#5076)」). It stops a glide where it is, puts the list a
/// screen short of the target when it is far (`ChatJump`), and moves it the rest of the way frame by frame, on whole
/// pixels, so it starts the moment the tap lands, takes as long as `ChatJump` says and says when it is over.
/// SwiftUI's `ScrollPosition` is not asked: during a glide it may still hold the newest end from the last time the app
/// went there, and asking for it again changed nothing (docs/lessons/ios.md「Jump to Latest during a glide」); and its
/// animated scroll drew every text on screen again on every frame, which in a long Chinese answer was most of each
/// frame (docs/lessons/ios.md「Animated jumps redraw every text they pass」). A finger taking the list ends it where it is.
@MainActor final class ChatJumpRun: NSObject {
    /// Where a jump goes: the newest end of the flipped list (read again every frame: an inset that changes moves it),
    /// or a content offset (kept within the list; `-showAutoScroll`'s sweep).
    enum Goal: Equatable {
        case newest
        case offset(CGFloat)
    }

    private weak var list: UIScrollView?
    private var link: CADisplayLink?
    private var goal = Goal.newest
    private var from: CGFloat = 0
    private var began: CFTimeInterval = 0
    private var seconds: Double = 0
    private var done: ((Bool) -> Void)?

    /// A jump is moving the list.
    var running: Bool { link != nil }

    /// The newest end of the flipped list (offset 0 of its content, under its inset).
    static func newest(_ list: UIScrollView) -> CGFloat { -list.adjustedContentInset.top }

    /// The oldest end: the content's far edge, over its inset.
    static func oldest(_ list: UIScrollView) -> CGFloat {
        max(newest(list), list.contentSize.height - list.bounds.height + list.adjustedContentInset.bottom)
    }

    /// The offset `goal` stands for now.
    static func target(_ list: UIScrollView, _ goal: Goal) -> CGFloat {
        switch goal {
        case .newest: newest(list)
        case .offset(let y): min(max(y, newest(list)), oldest(list))
        }
    }

    /// Stops `list` where it is (a glide too) and takes it to the newest end. `done(true)` once there, `done(false)` if
    /// a finger took the list first or another jump replaced this one. Reduce Motion: there at once.
    func start(_ list: UIScrollView, done: @escaping (Bool) -> Void) {
        start(list, to: .newest, done: done)
    }

    /// Stops `list` where it is and takes it to `goal`, as ↓ goes to the newest end.
    func start(_ list: UIScrollView, to goal: Goal, done: @escaping (Bool) -> Void) {
        end(arrived: false)
        // Setting the offset it already has, through the animated-scroll API, is UIKit's way to stop a glide.
        list.setContentOffset(list.contentOffset, animated: false)
        let target = Self.target(list, goal)
        if goal == .newest {
            // Past the newest end (a rubber band) is there already, as it always was for ↓.
            switch ChatJump.plan(gap: list.contentOffset.y - target, screen: list.bounds.height) {
            case .none:
                put(list, target)
                return done(true)
            case .glide(let s):
                seconds = s
            case .leap(let gap, let s):
                put(list, target + gap)
                seconds = s
            }
        } else {
            let path = ChatJump.path(from: list.contentOffset.y, to: target, screen: list.bounds.height)
            put(list, path.start)
            guard path.seconds > 0 else { return done(true) }
            seconds = path.seconds
        }
        if Theme.Motion.reduced {
            put(list, target)
            return done(true)
        }
        self.list = list
        self.goal = goal
        self.done = done
        from = list.contentOffset.y
        began = CACurrentMediaTime()
        let link = CADisplayLink(target: self, selector: #selector(tick))
        link.add(to: .main, forMode: .common)
        self.link = link
    }

    /// Ends a running jump where it is.
    func cancel() { end(arrived: false) }

    @objc private func tick() {
        guard let list else { return end(arrived: false) }
        if list.isTracking { return end(arrived: false) } // the user has the list
        let t = seconds > 0 ? (CACurrentMediaTime() - began) / seconds : 1
        let target = Self.target(list, goal)
        if t >= 1 {
            put(list, target)
            return end(arrived: true)
        }
        // On whole pixels on the way: the rows' drawings move as they are; the last frame is the exact target.
        put(list, ChatJump.pixel(from + (target - from) * CGFloat(ChatJump.progress(t)), scale: list.traitCollection.displayScale))
    }

    private func put(_ list: UIScrollView, _ y: CGFloat) {
        list.contentOffset = CGPoint(x: list.contentOffset.x, y: y)
    }

    private func end(arrived: Bool) {
        link?.invalidate()
        link = nil
        list = nil
        let done = self.done
        self.done = nil
        done?(arrived)
    }
}
