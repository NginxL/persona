import XCTest

/// The base of the performance UI tests (#5730, docs/operations/testing.md "Jank measurement"): every page and main transition
/// of the iPhone app measured with `XCTHitchMetric` (and the scroll / navigation signposts where they apply) against a
/// mock hub loaded with heavy data. Each suite expects its own mock (`scripts/app/perf-test.sh` starts one per suite
/// with the env flags listed on the suite) at `TEST_RUNNER_PERSONA_PERF_HUB=http://127.0.0.1:<port>`; without it the
/// tests skip, so they never run in the ordinary UI test pass.
///
/// Numbers only mean something from an optimized build (`SWIFT_OPTIMIZATION_LEVEL=-O`, whole module), which the script
/// makes. The tests never assert a frame rate: a simulator's numbers vary from run to run; the report compares them.
/// Uses the iPhone's default Shell, without a layout override.
class PerfTestCase: XCTestCase {
    var hub = ""
    var app: XCUIApplication!

    /// Pairing resets the route arguments (`ComposerKeyboardTests`), so each suite pairs once with a bare launch first.
    nonisolated(unsafe) private static var pairedHubs: Set<String> = []

    var iterations: Int { Int(ProcessInfo.processInfo.environment["PERSONA_PERF_ITERATIONS"] ?? "") ?? 5 }
    var pair: String { "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock" }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_PERF_HUB"] else {
            throw XCTSkip("performance tests: run scripts/app/perf-test.sh (PERSONA_PERF_HUB)")
        }
        hub = h
        _ = mock("reset")
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let s = XCTAttachment(screenshot: app.screenshot())
            s.name = "failure"
            s.lifetime = .keepAlways
            add(s)
            let t = XCTAttachment(string: app.debugDescription)
            t.name = "view tree"
            t.lifetime = .keepAlways
            add(t)
        }
        super.tearDown()
    }

    // MARK: Launching

    /// Launches paired with the suite's mock, with `args` as the route. `ready` is what shows the page is up; after it
    /// the test waits `settle` seconds so first loads (history pages, images, metrics) are done before measuring.
    @MainActor @discardableResult
    func launch(_ args: [String], ready: XCUIElement? = nil, timeout: TimeInterval = 30, settle: UInt32 = 3) -> XCUIApplication {
        if !Self.pairedHubs.contains(hub) {
            let first = XCUIApplication()
            first.launchArguments = ["-pairURL", pair, "-dev.openChat", ""]
            first.launchPinned()
            acceptIconAlert()
            sleep(4)
            first.terminate()
            Self.pairedHubs.insert(hub)
        }
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-showFrameProbe", "YES"] + args
        app.launchPinned()
        allowNotifications()
        acceptIconAlert()
        if let ready { see(ready, "the page", timeout: timeout) }
        sleep(settle)
        return app
    }

    /// The notification prompt a fresh install shows over the first page.
    @MainActor func allowNotifications() {
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.buttons["允许"]
        if allow.waitForExistence(timeout: 2) { allow.tap() }
    }

    // MARK: Measuring

    /// XCTHitchMetric and the scroll signposts give no numbers on a simulator, so every measured block is also framed
    /// by the app's own `FrameStats` (`FrameProbe`, Darwin notifications in, results through the mock hub). After
    /// the iterations the test prints one `perf-frames:` line (medians, the first iteration dropped as XCTest does)
    /// and attaches every iteration as `frames.json`; `scripts/app/perf_report.py` reads both.
    @MainActor func measureFrames(_ metrics: [XCTMetric], assertMainThreadBudget: Bool = false, _ block: () -> Void) {
        let name = testName
        mock("frames_label?name=" + name)
        measure(metrics: metrics, options: options) {
            darwinPost("com.persona.perf.frames.start")
            block()
            usleep(300_000) // the last frames of a fling or an animation
            darwinPost("com.persona.perf.frames.stop")
        }
        sleep(1)
        let runs = (mock("frames?name=" + name)["runs"] as? [[String: Any]]) ?? []
        if let data = try? JSONSerialization.data(withJSONObject: runs, options: [.prettyPrinted, .sortedKeys]) {
            let a = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
            a.name = "frames.json"
            a.lifetime = .keepAlways
            add(a)
        }
        let kept = runs.count > 1 ? Array(runs.dropFirst()) : runs
        func median(_ key: String) -> Double {
            let v = kept.compactMap { ($0[key] as? NSNumber)?.doubleValue }.sorted()
            return v.isEmpty ? .nan : v[v.count / 2]
        }
        let fps = kept.compactMap { r -> Double? in
            guard let f = (r["frames"] as? NSNumber)?.doubleValue, let s = (r["seconds"] as? NSNumber)?.doubleValue, s > 0 else { return nil }
            return f / s
        }.sorted()
        let line = String(format: "perf-frames: %@ runs=%d fps=%.1f hitch_ms_per_s=%.1f hitches=%.0f p95_ms=%.1f max_ms=%.1f busy_p95_ms=%.1f busy_over_16ms=%.0f",
                          name, kept.count, fps.isEmpty ? .nan : fps[fps.count / 2], median("hitch_ms_per_s"), median("hitches"),
                          median("p95_ms"), median("max_ms"), median("busy_p95_ms"), median("busy_over_16ms"))
        print(line)
        if median("text_changes") > 0 { // a streamed reply was on screen (#6651)
            print(String(format: "perf-stream: %@ runs=%d fps=%.1f hitches=%.0f max_ms=%.1f text_changes=%.0f text_gap_p50_ms=%.0f text_gap_p95_ms=%.0f text_gap_max_ms=%.0f text_added_p50=%.0f text_added_p95=%.0f text_added_max=%.0f main_cpu_ms_per_s=%.0f busy_ms_per_s=%.0f",
                         name, kept.count, fps.isEmpty ? .nan : fps[fps.count / 2], median("hitches"), median("max_ms"), median("text_changes"),
                         median("text_gap_p50_ms"), median("text_gap_p95_ms"), median("text_gap_max_ms"), median("text_added_p50"),
                         median("text_added_p95"), median("text_added_max"), median("main_cpu_ms_per_s"), median("busy_ms_per_s")))
        }
        XCTAssertFalse(runs.isEmpty, "the app sent no frames (is -showFrameProbe in this build?)")
        if assertMainThreadBudget {
            for run in runs {
                let blocks = run["main_thread_blocks"] as? [String: Any]
                let count = (blocks?["count"] as? NSNumber)?.intValue
                XCTAssertEqual(count, 0, "main-thread block over 100 ms: \(blocks ?? [:])")
            }
        }
    }

    /// A Darwin notification: the runner and the app share the simulator's notification center.
    func darwinPost(_ name: String) {
        CFNotificationCenterPostNotification(CFNotificationCenterGetDarwinNotifyCenter(), CFNotificationName(name as CFString),
                                             nil, nil, true)
    }

    /// `-[PersonaUITests.PerfAgentsTests testAgentsListScroll]` → `PerfAgentsTests/testAgentsListScroll`.
    var testName: String {
        let parts = name.trimmingCharacters(in: CharacterSet(charactersIn: "-[]")).split(separator: " ")
        let cls = parts.first.map { $0.split(separator: ".").last.map(String.init) ?? String($0) } ?? ""
        return cls + "/" + (parts.count > 1 ? String(parts[1]) : "")
    }

    @MainActor func metrics(scroll: Bool = false, navigation: Bool = false) -> [XCTMetric] {
        var m: [XCTMetric] = []
        if #available(iOS 26.0, *) { m.append(XCTHitchMetric(application: app)) }
        if scroll { m.append(XCTOSSignpostMetric.scrollingAndDecelerationMetric) }
        if navigation { m.append(XCTOSSignpostMetric.navigationTransitionMetric) }
        if m.isEmpty { m.append(XCTClockMetric()) }
        return m
    }

    var options: XCTMeasureOptions {
        let o = XCTMeasureOptions()
        o.iterationCount = iterations
        return o
    }

    /// Fast flicks over `view`: `first` direction `flicks` times, then back. The list is upside down in chats
    /// (newest at the bottom, history above), so a chat passes `.down` to go into history first.
    @MainActor func measureScroll(_ view: XCUIElement, first: Direction = .up, flicks: Int = 3) {
        see(view, "the scroll view")
        measureFrames(metrics(scroll: true)) {
            for _ in 0..<flicks { swipe(view, first) }
            for _ in 0..<flicks { swipe(view, first.opposite) }
        }
    }

    /// A separate, tagged workload: the app holds one UIScrollView and drives it at 2000 pt/s in both directions.
    /// No accessibility queries or gestures occur inside its six-second frame window. This is a glide, not a
    /// finger fling / deceleration metric, and CADisplayLink cadence must not be called presented GPU FPS.
    @MainActor func measureVerifiedGlide() {
        let name = testName.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
        var runs: [[String: Any]] = []
        var iteration = 0
        measure(metrics: metrics(), options: options) {
            let id = UUID().uuidString
            let setup = mock("frames_glide_context?name=" + name + "&run_id=" + id
                             + "&warm_up=" + (iteration == 0 ? "1" : "0")
                             + "&expected_iterations=" + String(iterations))
            XCTAssertEqual(setup["run_id"] as? String, id, "the mock did not accept the glide command")
            darwinPost("com.persona.perf.frames.glide")
            let deadline = Date.now.addingTimeInterval(18)
            var result: [String: Any]?
            repeat {
                let all = mock("frames?name=" + name)["runs"] as? [[String: Any]] ?? []
                result = all.first { ($0["run_id"] as? String) == id }
                if result == nil { usleep(200_000) }
            } while result == nil && Date.now < deadline
            guard let result else { XCTFail("no tagged glide result within 18 seconds"); return }
            runs.append(result)
            iteration += 1
        }
        if let data = try? JSONSerialization.data(withJSONObject: runs, options: [.prettyPrinted, .sortedKeys]) {
            let attachment = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
            attachment.name = "frames.json"
            attachment.lifetime = .keepAlways
            add(attachment)
        }
        XCTAssertEqual(runs.count, iterations + 1, "one warm-up and every measured iteration must arrive")
        XCTAssertEqual(runs.filter { $0["warm_up"] as? Bool == true }.count, 1)
        for run in runs {
            XCTAssertNil(run["error"], "the app could not run the requested glide: \(run)")
            XCTAssertEqual(run["name"] as? String, testName)
            XCTAssertEqual(run["sample_source"] as? String, "app-list-glide")
            XCTAssertNotNil(run["main_thread_blocks"] as? [String: Any], "every measured run includes the watchdog report")
            XCTAssertGreaterThan((run["frames"] as? NSNumber)?.doubleValue ?? 0, 0)
            XCTAssertGreaterThan((run["seconds"] as? NSNumber)?.doubleValue ?? 0, 5)
            let viewport = (run["viewport_height_pt"] as? NSNumber)?.doubleValue ?? 0
            XCTAssertGreaterThan(viewport, 0)
            XCTAssertGreaterThan((run["content_height_pt"] as? NSNumber)?.doubleValue ?? 0, viewport)
            XCTAssertGreaterThan((run["scroll_offset_delta_pt"] as? NSNumber)?.doubleValue ?? 0, 24,
                                 "the selected scroll view did not actually move")
        }
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "verified-glide-page"
        shot.lifetime = .keepAlways
        add(shot)
    }

    /// A transition (push / pop, sheet, tab switch) and its way back, once per iteration.
    @MainActor func measureTransition(navigation: Bool = true, _ round: () -> Void) {
        measureFrames(metrics(navigation: navigation), round)
    }

    /// Typing into `field` at a person's pace (XCUITest types a character every few frames), then clearing it.
    @MainActor func measureTyping(_ field: XCUIElement, text: String = "看一下这个页面滚动的时候为什么会卡，顺便测一下输入的延迟") {
        see(field, "the text field")
        field.tap()
        sleep(1)
        measureFrames(metrics()) {
            field.typeText(text)
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: text.count))
        }
    }

    /// The page sitting still while the hub pushes (`/mock/<what>`, e.g. `churn?n=8&ms=300`), for `seconds`.
    @MainActor func measureIdle(push what: String, seconds: UInt32 = 4, assertMainThreadBudget: Bool = false) {
        measureFrames(metrics(), assertMainThreadBudget: assertMainThreadBudget) {
            _ = mock(what)
            sleep(seconds)
        }
    }

    enum Direction {
        case up, down, left, right
        var opposite: Direction {
            switch self { case .up: .down; case .down: .up; case .left: .right; case .right: .left }
        }
    }

    @MainActor func swipe(_ e: XCUIElement, _ d: Direction) {
        switch d {
        case .up: e.swipeUp(velocity: .fast)
        case .down: e.swipeDown(velocity: .fast)
        case .left: e.swipeLeft(velocity: .fast)
        case .right: e.swipeRight(velocity: .fast)
        }
    }

    // MARK: Finding things

    @discardableResult
    @MainActor func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        if !e.waitForExistence(timeout: timeout) { XCTFail("\(what) not found") }
        return e
    }

    @MainActor func any(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    @MainActor func button(labelContains s: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor func text(_ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    /// The largest scroll view on screen: a chat's list, a List (collection view), a web or text view's scroller.
    @MainActor func mainScroller() -> XCUIElement {
        let window = app.windows.firstMatch.frame
        let candidates = app.collectionViews.allElementsBoundByIndex + app.scrollViews.allElementsBoundByIndex
            + app.tables.allElementsBoundByIndex
        let big = candidates.filter { $0.exists && $0.frame.height > window.height * 0.4 }
            .max { $0.frame.width * $0.frame.height < $1.frame.width * $1.frame.height }
        return big ?? app.scrollViews.firstMatch
    }

    /// Back one page: the visible Back button (a chat's own header has one outside a navigation bar), the
    /// navigation bar's, or an edge swipe.
    @MainActor func back() {
        if let b = app.buttons.matching(NSPredicate(format: "label IN %@ OR identifier == %@", ["返回", "Back"], "BackButton"))
            .allElementsBoundByIndex.first(where: { $0.exists && $0.isHittable }) {
            b.tap()
        } else {
            let w = app.windows.firstMatch
            w.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.85, dy: 0.5)))
        }
    }

    /// Each entered/returned screen must appear within the bound, or the transition case fails.
    @discardableResult
    @MainActor func wait(_ e: XCUIElement, shown: Bool = true, timeout: TimeInterval = 8) -> Bool {
        let matched: Bool
        if shown {
            matched = e.waitForExistence(timeout: timeout)
        } else {
            let deadline = Date.now.addingTimeInterval(timeout)
            while e.exists, Date.now < deadline { usleep(100_000) }
            matched = !e.exists
        }
        XCTAssertTrue(matched, "transition target did not \(shown ? "appear" : "disappear") within \(timeout) seconds")
        return matched
    }

    @discardableResult
    func mock(_ what: String) -> [String: Any] {
        guard let url = URL(string: hub + "/mock/" + what), let data = try? Data(contentsOf: url),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return obj
    }
}
