import SwiftUI

/// The shell's two pages side by side, Assistant on the left and Agents on the right like the switch over them
/// (docs/apple/ios.md「New shell」): a drag to the left on Assistant brings Agents in under the finger, a drag to the right on Agents
/// brings Assistant back, and the switch's pill follows. The whole strip reads drawer | Assistant | Agents: on Assistant a drag to the
/// right still opens the drawer (`DrawerPan` decides which one a drag is).
///
/// Its own object, like `AssistantDrawer`: while the finger moves only the pages' slide, the top bars' pin and the
/// switch's pill read the position, never the pages themselves.
@MainActor @Observable final class ShellPager {
    /// 0 = Assistant in full, 1 = Agents in full; in between while dragging or settling.
    private(set) var position: CGFloat = 0
    private(set) var pageCount = 2

    func setPageCount(_ count: Int) {
        pageCount = max(1, count)
        slide(to: min(page, pageCount - 1), animated: false)
    }
    /// A drag or a settle is under way: both pages are on screen until it ends.
    private(set) var moving = false
    /// An interrupted drag is settling back, but its page may already take the next tap. The moving strip itself
    /// (`goal == nil`, no finger down) is interrupted too; `touching` and `goal` stay unobserved, so the ordinary
    /// release does not redraw the pages before `letGo` has chosen where it settles.
    private(set) var restoringInput = false
    var restoresPageInput: Bool { restoringInput || (moving && goal == nil && !touching) }
    /// The only page an interrupted drag may expose before it settles: where that drag started, not the page nearest
    /// the abandoned position. A drag past halfway must not expose its destination while cancelling back.
    var recoveryPage: Int { Int(dragStart.rounded()) }
    /// The strip came to rest under a finger already down: the moving bar stays in its place until the finger lets go,
    /// so a tap that began on its switch still lands (`ShellMovingBar`). Taken away, the switch under the finger took
    /// nothing: a tap on Agents as the strip came back to Assistant was lost (`ShellRaceTests`).
    private(set) var lingering = false
    /// A finger is on the screen (the window's watcher, `ShellWindowHooks`).
    @ObservationIgnored private var touching = false
    /// The page's width (`ShellView` measures it).
    @ObservationIgnored var width: CGFloat = 390
    @ObservationIgnored private var dragStart: CGFloat = 0
    /// Each settle's number: only the last one's end clears `moving`.
    @ObservationIgnored private var serial = 0
    /// Where the strip rests or is headed (0 or 1); nil while a finger holds it.
    @ObservationIgnored private var goal: CGFloat? = 0
    /// Where a drag settled: the shell selects that page.
    @ObservationIgnored var settled: (Int) -> Void = { _ in }
    /// A drag took hold: the shell builds Agents if it has not yet.
    @ObservationIgnored var began: () -> Void = {}

    /// The page at rest (or nearest).
    var page: Int { min(pageCount - 1, max(0, Int(position.rounded()))) }

    /// The moving bar stands in for the pages' own (`ShellMovingBar`, `ShellBarAtRest`) while the strip moves, or while a
    /// finger that was down as it came to rest stays (`lingering`). In the side column (iPhone Duo, `rail`) it also stays
    /// until the tab bar's lens has glided (`TabBarGlide`, #10270): the strip comes to rest about 0.55 s into the lens's
    /// 0.65 s, and the pages' own rails put back then (their glass and menus) held up a frame of its fade by 33–42 ms on
    /// the simulator. Once the lens is gone, nothing moves as they come back.
    func standsIn(rail: Bool) -> Bool { moving || lingering || (rail && TabBarGlide.shared.running) }

    /// A finger came down on the window, or the last one let go.
    func fingers(down: Bool) {
        // A new tap must not land on the page that an abandoned drag left out of reach. Keep it in reach while
        // cancelling back to the drag's start; an actual new drag clears this again in `dragBegan`.
        let abandoned = down && moving && goal == nil && !touching
        // Record the new finger before cancelling: if the strip is already at its start, `rest` returns at once and
        // must leave the moving switch in place until this finger lets go (`lingering`).
        touching = down
        if abandoned {
            restoringInput = true
            cancel()
        }
        if !down {
            if lingering {
                #if DEBUG
                ShellTrace.shared.note("bar let go")
                #endif
                lingering = false
            }
            guard moving, goal == nil else { return }
            // `letGo` normally chooses the goal in this same event. Only a drag whose recognizer never ended needs
            // cancelling; checking on the next turn keeps an ordinary release on its chosen page.
            DispatchQueue.main.async { [weak self] in
                guard let self, moving, goal == nil, !touching else { return }
                restoringInput = true
                cancel()
            }
        }
    }

    /// The strip comes to rest; a finger down keeps the moving bar standing in (`lingering`).
    private func rest() {
        if moving, touching {
            #if DEBUG
            ShellTrace.shared.note("bar held")
            #endif
            lingering = true
        }
        moving = false
        restoringInput = false
    }

    /// The spring is over, but the destination's own bar has not necessarily laid out at the strip's resting offset yet.
    /// Keep the stationary moving bar (and only its switch) taking taps through two main-queue turns, so SwiftUI can
    /// commit that transparent bar's layout before it takes over. Otherwise the first build of Assistant can put its bar
    /// back about 30 pt out of place, and a tap meant for Agents lands on New Chat or nowhere (#10227). A new slide changes
    /// `serial`, cancelling this handoff; immediate routes still call `rest()` directly. The settle fallback uses this
    /// handoff too, whether the completion arrived just before it or was lost.
    private func restAfterPageLayout() {
        let mine = serial
        DispatchQueue.main.async { [weak self] in
            DispatchQueue.main.async { [weak self] in
                guard let self, serial == mine, moving else { return }
                #if DEBUG
                ShellTrace.shared.note("bar ready \(position)")
                #endif
                rest()
            }
        }
    }

    func dragBegan() {
        #if DEBUG
        ShellTrace.shared.note("drag began pos=\(String(format: "%.2f", position))")
        #endif
        serial += 1
        goal = nil
        dragStart = position
        restoringInput = false
        moving = true
        began()
    }

    /// The finger is `x` points to the right of where the drag took hold: the strip moves the other way. Straight to
    /// the value, no animation; a little resistance past either end.
    func drag(_ x: CGFloat) {
        var p = dragStart - x / max(width, 1)
        let end = CGFloat(pageCount - 1)
        if p < 0 { p /= 3 } else if p > end { p = end + (p - end) / 3 }
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t) { position = p }
    }

    /// The finger let go at `velocity` points per second (to the right is positive): the same rule as the drawer and
    /// the system's back swipe, a flick goes its way, otherwise past half way.
    func letGo(velocity: CGFloat) {
        let from = dragStart.rounded()
        // A reversal cancels a partial drag rather than skipping to the other neighbor.
        let direction: CGFloat = position == from ? (velocity < 0 ? 1 : -1) : (position > from ? 1 : -1)
        let toward = min(CGFloat(pageCount - 1), max(0, from + direction))
        let along = max(0, (position - from) * direction)
        let speed = -velocity * direction
        let goes = SwipeRelease.goes(progress: along, velocity: speed, width: max(width, 1))
        let target = goes ? toward : from
        if goes { Haptics.tap() }
        #if DEBUG
        ShellTrace.shared.note("let go along=\(String(format: "%.2f", along)) v=\(Int(velocity)) goes=\(goes)")
        #endif
        settle(at: target, velocity: velocity)
        if goes { settled(Int(target)) }
    }

    /// To `page` (0 or 1): the switch was tapped, or something routed to the other page. Nothing to do when the strip
    /// rests there or is already on its way (a drag that settled there, the same tap twice); a slide the other way
    /// that has not started yet is replaced, one under way turns around.
    func slide(to page: Int, animated: Bool = true) {
        let target = CGFloat(min(pageCount - 1, max(0, page)))
        #if DEBUG
        ShellTrace.shared.note("slide \(page) pos=\(String(format: "%.2f", position)) moving=\(moving)")
        #endif
        guard goal != target else { return }
        serial += 1
        goal = target
        guard animated else {
            position = target
            rest()
            return
        }
        if moving {
            settle(at: target, velocity: 0)
        } else {
            // At rest the other page lies under this one (`ShellPageSlide`): put it beside first, then slide.
            moving = true
            let mine = serial
            DispatchQueue.main.async { [weak self] in
                guard let self, serial == mine else { return }
                settle(at: target, velocity: 0)
            }
        }
    }

    #if DEBUG
    /// `-showShellPagerAt <0…1>`: the strip held part way, as under a finger, for screenshots of a drag.
    func hold(at p: CGFloat) {
        goal = nil
        moving = true
        position = p
    }
    #endif

    /// The drag was taken away (a call, the system): back to where it started. A newer slide owns the strip already.
    func cancel() {
        guard goal == nil else { return }
        if moving && !touching { restoringInput = true }
        let target = Int(dragStart.rounded())
        settle(at: dragStart.rounded(), velocity: 0)
        // Ordinarily this is already the selected page. Reassert the drag's starting page just as a release that
        // returns there does, so selection, strip and input converge on one page again.
        settled(target)
    }

    private func settle(at target: CGFloat, velocity: CGFloat) {
        goal = target
        guard position != target else {
            // Nothing to animate, so no animation would end and clear `moving`.
            rest()
            return
        }
        let way = (target - position) * max(width, 1)
        // The spring's initial velocity is a share of the way per second (the strip moves against the finger).
        let share = abs(way) > 1 ? max(0, Double(-velocity / way)) : 0
        let motion = Theme.Motion.drawer(velocity: share)
        let mine = serial
        moving = true
        // Ends with the spring itself, not when it is "logically" complete: that comes while ~5 pt of the way are left,
        // and `moving` going false then puts each page's own top bar back on a page still sliding, so the bar jumped
        // that far sideways and crept back after the switch looked done.
        withAnimation(motion, completionCriteria: .removed) {
            position = target
        } completion: { [weak self] in
            #if DEBUG
            ShellTrace.shared.note("settled \(target) current=\(self?.serial == mine)")
            #endif
            guard let self, serial == mine else { return }
            restAfterPageLayout()
        }
        // `.removed` waits for every view that took the animation, and one out of the window (under a pushed page) never
        // runs it: opening an agent from the inbox the slide never ended (2026-11-03, `ShellBarAtRest`, `PagerPill`).
        // Should another such view turn up, the strip still rests, long after any spring of this length.
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.settleLimit) { [weak self] in
            guard let self, serial == mine, moving, position == target else { return }
            #if DEBUG
            ShellTrace.shared.note("settle lost \(target)")
            #endif
            restAfterPageLayout()
        }
    }

    /// The longest a settle takes (the spring is 0.32 s; ended as `.removed`, up to ~0.85 s seen), with room to spare.
    static let settleLimit: DispatchTimeInterval = .milliseconds(1200)
}

/// Where a drag that follows the finger lets go, like the system's back swipe: a flick goes its way, otherwise past half
/// way (counting a little of where it is heading) goes. The drawer and the pager both use it. `progress` 0…1 along the
/// way, `velocity` in points per second, positive towards the end.
enum SwipeRelease {
    nonisolated static func goes(progress: CGFloat, velocity: CGFloat, width: CGFloat) -> Bool {
        if abs(velocity) > 300 { return velocity > 0 }
        return progress + velocity * 0.2 / max(width, 1) > 0.5
    }
}

/// One of the shell's pages on the strip: beside the other one while the strip moves, alone and in reach at rest. At
/// rest the hidden page lies under the shown one (transparent, out of reach), not off the screen: UI tests find a
/// page's controls by id, and the first match off screen could not be tapped (`ShellTests`). Only the page in front
/// (`ShellState.page`) with the strip at rest on it takes taps and is read by VoiceOver.
struct ShellPageSlide: ViewModifier {
    let shell: ShellState
    /// 0 = Assistant, 1 = Agents.
    let index: Int
    /// iPhone Duo's bars down the side (`shellRailEdge`): the pages cross-fade in place there instead of sliding. A page
    /// that moved sideways under those bars, by any means (an offset, a render-only transform, under a mask), made both
    /// pages' navigation controllers lay out again on every frame: about 27 ms a frame, 140 ms of hitches a second through
    /// a switch on the simulator; in place it is 58 ms and 2.8 ms, as on any iPhone (2026-11-09). A drag fades with the finger.
    @Environment(\.shellRailEdge) private var railEdge

    func body(content: Content) -> some View {
        let pager = shell.pager
        let p = pager.position
        let delta: CGFloat = CGFloat(index) - p
        let distance: CGFloat = abs(delta)
        let shown = pager.moving ? distance < 1 : index == pager.page
        let inReach = (!pager.moving && index == pager.page && index == shell.page)
            || (pager.restoresPageInput && index == pager.recoveryPage)
        let offset: CGFloat = pager.moving && railEdge == nil ? delta * pager.width : 0
        let opacity: CGFloat = railEdge != nil && pager.moving
            ? max(CGFloat(0), CGFloat(1) - distance)
            : (shown ? CGFloat(1) : CGFloat(0))
        content
            // The page behind is never taken out of the window, so `onDisappear` does not fire for it: `tabIsActive`
            // tells its polling and read reports to stop (docs/apple/ios.md「只在前台做的事」). The page in front, so a drag
            // that does not settle on the other page does not restart them.
            .environment(\.tabIsActive, index == shell.page)
            .offset(x: offset)
            .opacity(opacity)
            .allowsHitTesting(inReach)
            .accessibilityHidden(!inReach)
            .zIndex(index == pager.page ? 1 : 0)
    }
}

/// A page's own top bar steps aside while the strip moves (and until a finger down as it came to rest lets go,
/// `ShellPager.lingering`): `ShellMovingBar` stands in for both (drawn in its page, each
/// bar was covered by the other page where it reached over it). While the strip rests on the other page the bar is not
/// there at all. Hidden (`accessibilityHidden`, opacity, hit testing) was not enough: its switch is a menu and its
/// buttons are glass, all UIKit, and after a page pushed on Assistant was swiped back Agents' bar was in the accessibility
/// tree again over Assistant's, invisible and "hittable" — VoiceOver could land on it, and a tap on the switch the tree
/// offered first hit neither segment (`ShellSwitchTests`). It comes back, transparent, as soon as a slide starts.
struct ShellBarAtRest: ViewModifier {
    @Environment(\.shell) private var shell
    @Environment(\.shellRailEdge) private var railEdge
    /// The bar's page on the strip: 0 = Assistant, 1 = Agents.
    let page: Int

    func body(content: Content) -> some View {
        if let pager = shell?.pager {
            let standIn = pager.standsIn(rail: railEdge != nil)
            // The bar comes and goes with `pager.page`, which a slide changes in its animated transaction, and a page pushed
            // over it (now or before the spring ends) takes it out of the window, where that transition never runs: the
            // slide waited for it (`ShellPager.settle`). Opening an agent from the inbox, pushed on Assistant, left the strip
            // moving, both pages out of reach and the moving bar's switch over the chat (2026-11-03,
            // `ShellInboxAgentTests`). It is transparent while the strip moves (`ShellMovingBar` stands in): no animation.
            Group {
                if standIn || pager.page == page {
                    content.opacity(standIn ? 0 : 1)
                }
            }
            .transaction { $0.animation = nil }
        } else {
            content
        }
    }
}
