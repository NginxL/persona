#if DEBUG && os(iOS)
import QuartzCore
import UIKit

/// Simulator profiling for the sidebar's drag (`-showDrawerProbe YES`): every decision `DrawerPan` makes about a touch
/// (why it did or did not start), and while a drag and its settle run, every frame's interval and the main thread's busy
/// time in it. Appended to Documents/drawer-probe.jsonl for scripts to read (docs/operations/testing.md「Sidebar dragging」).
@MainActor final class DrawerProbe: NSObject {
    static let shared = DrawerProbe()
    static let on = UserDefaults.standard.bool(forKey: "showDrawerProbe")

    private var link: CADisplayLink?
    private var observers: [CFRunLoopObserver] = []
    private var phase = ""
    private var last: CFTimeInterval = 0
    private var passStart: CFTimeInterval?
    /// Main-thread time in the current frame so far.
    private var busy: CFTimeInterval = 0
    private var frames: [String: [(interval: Double, busy: Double)]] = [:]
    private var first: Double?
    private var stopping: DispatchWorkItem?

    /// A decision about a touch: `began` / `refused <why>` / `lost` (another gesture took it before ours was asked).
    static func note(_ what: String, _ detail: [String: Any] = [:]) {
        guard on else { return }
        var line = detail
        line["event"] = what
        shared.write(line)
    }

    /// The drag started: frames count from here.
    func dragBegan() {
        guard Self.on else { return }
        stopping?.cancel()
        frames = [:]
        first = nil
        phase = "drag"
        last = 0
        busy = 0
        passStart = nil
        if link == nil {
            let l = CADisplayLink(target: self, selector: #selector(tick(_:)))
            l.preferredFrameRateRange = CAFrameRateRange(minimum: 60, maximum: 120, preferred: 120)
            l.add(to: .main, forMode: .common)
            link = l
            // A pass starts when the run loop wakes (first of all) and ends after Core Animation's commit (last of all).
            let wake = CFRunLoopObserverCreateWithHandler(nil, CFRunLoopActivity.afterWaiting.rawValue, true, CFIndex.min) { _, _ in
                MainActor.assumeIsolated { DrawerProbe.shared.passStart = CACurrentMediaTime() }
            }
            let sleep = CFRunLoopObserverCreateWithHandler(nil, CFRunLoopActivity.beforeWaiting.rawValue, true, CFIndex.max) { _, _ in
                MainActor.assumeIsolated { DrawerProbe.shared.passEnded() }
            }
            for o in [wake, sleep] { CFRunLoopAddObserver(CFRunLoopGetMain(), o, .commonModes) }
            observers = [wake, sleep].compactMap { $0 }
        }
    }

    /// The finger let go: the settle animation's frames count separately, then the summary is written.
    func dragEnded(_ outcome: String) {
        guard Self.on, link != nil else { return }
        phase = "settle"
        let work = DispatchWorkItem { [weak self] in self?.finish(outcome) }
        stopping = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.6, execute: work)
    }

    private func passEnded() {
        if let s = passStart { busy += CACurrentMediaTime() - s }
        passStart = nil
    }

    @objc private func tick(_ l: CADisplayLink) {
        let now = l.timestamp
        // The main thread's time in the passes that ended since the last frame (this pass counts in the next one).
        if last > 0 {
            let b = busy * 1000
            frames[phase, default: []].append((interval: (now - last) * 1000, busy: b))
            if first == nil { first = b }
        }
        busy = 0
        last = now
    }

    private func finish(_ outcome: String) {
        link?.invalidate()
        link = nil
        for o in observers { CFRunLoopRemoveObserver(CFRunLoopGetMain(), o, .commonModes) }
        observers = []
        passStart = nil
        var line: [String: Any] = ["event": "frames", "outcome": outcome, "first_busy_ms": round1(first ?? 0),
                                   "drag_start": (frames["drag"] ?? []).prefix(4).map { round1($0.busy) },
                                   "settle_start": (frames["settle"] ?? []).prefix(4).map { round1($0.busy) }]
        for (phase, fs) in frames {
            let intervals = fs.map(\.interval).sorted(), busies = fs.map(\.busy).sorted()
            func pct(_ a: [Double], _ p: Double) -> Double { a.isEmpty ? 0 : round1(a[min(a.count - 1, Int(Double(a.count) * p))]) }
            line[phase] = ["frames": fs.count,
                           "busy_p50": pct(busies, 0.5), "busy_p95": pct(busies, 0.95), "busy_max": round1(busies.last ?? 0),
                           "interval_p50": pct(intervals, 0.5), "interval_max": round1(intervals.last ?? 0),
                           "late": intervals.filter { $0 > 1000.0 / 60 * 1.5 }.count]
        }
        write(line)
    }

    private func round1(_ x: Double) -> Double { (x * 10).rounded() / 10 }

    private func write(_ line: [String: Any]) {
        guard let dir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first,
              var data = try? JSONSerialization.data(withJSONObject: line, options: [.sortedKeys]) else { return }
        data.append(0x0A)
        let url = dir.appending(path: "drawer-probe.jsonl")
        if let h = try? FileHandle(forWritingTo: url) {
            h.seekToEndOfFile()
            h.write(data)
            try? h.close()
        } else {
            try? data.write(to: url)
        }
    }
}
/// The same drags without a finger (`-showDrawerAutoDrag <rounds>`, launched without XCUITest, whose accessibility
/// queries make SwiftUI do extra work on every frame): each round drags open and lets go, drags back, goes half way
/// and lets go (it springs back), flicks open, and closes as the page's tap does — through the drawer's own drag steps,
/// one per display frame, with `DrawerProbe` counting.
@MainActor final class DrawerAutoDrag: NSObject {
    private let drawer: AssistantDrawer
    private var link: CADisplayLink?
    private var waiting: CheckedContinuation<Void, Never>?

    init(drawer: AssistantDrawer) { self.drawer = drawer }

    func run(rounds: Int) async {
        try? await Task.sleep(for: .seconds(6)) // the conversation loads and settles
        let w = drawer.width
        for _ in 0..<rounds {
            await move(w * 0.7, seconds: 0.45, velocity: 600)
            try? await Task.sleep(for: .seconds(1))
            await move(-w * 0.7, seconds: 0.4, velocity: -600)
            try? await Task.sleep(for: .seconds(1))
            await move(w * 0.3, seconds: 0.5, velocity: 100)
            try? await Task.sleep(for: .seconds(1))
            await move(w * 0.2, seconds: 0.12, velocity: 1200)
            try? await Task.sleep(for: .seconds(1))
            drawer.close()
            try? await Task.sleep(for: .seconds(1))
        }
        DrawerProbe.note("auto done")
    }

    private func move(_ dx: CGFloat, seconds: Double, velocity: CGFloat) async {
        DrawerProbe.shared.dragBegan()
        drawer.dragBegan()
        let t0 = CACurrentMediaTime()
        var f = 0.0
        while f < 1 {
            await frame()
            f = min(1, (CACurrentMediaTime() - t0) / seconds)
            drawer.drag(dx * f)
        }
        let opens = drawer.letGo(velocity: velocity)
        DrawerProbe.shared.dragEnded(opens ? "open" : "closed")
    }

    private func frame() async {
        if link == nil {
            let l = CADisplayLink(target: self, selector: #selector(tick))
            l.preferredFrameRateRange = CAFrameRateRange(minimum: 60, maximum: 120, preferred: 120)
            l.add(to: .main, forMode: .common)
            link = l
        }
        await withCheckedContinuation { waiting = $0 }
    }

    @objc private func tick() {
        let w = waiting
        waiting = nil
        w?.resume()
    }
}
#endif
