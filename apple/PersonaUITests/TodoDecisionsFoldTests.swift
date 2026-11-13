import XCTest

/// A to-do's Decisions fold (#6447, docs/assistant/todos.md "Decision records"): the first five with Show All under them,
/// Show All and Show First 5, Collapse All folding the list away and Expand All showing it whole, the header's count
/// unchanged. Against scripts/mock/mock_hub.py with MOCK_TODO_DECISIONS=1 (PERSONA_MOCK_HUB); the test gives the weather
/// to-do twelve decisions first (eleven in effect, made-up words). TEST_RUNNER_PERSONA_SHOTS=<dir> keeps each screen,
/// TEST_RUNNER_PERSONA_SCHEMES picks the appearances (light and dark by default).
final class TodoDecisionsFoldTests: XCTestCase {
    private let env = ProcessInfo.processInfo.environment
    private let todo = "todo_weather"
    /// The weather to-do's first decision in effect (the mock's first one is revised by its third).
    private let firstRow = "todo.decision.dec_tests"

    override func setUp() { continueAfterFailure = false }

    @MainActor func testFirstFiveShowAllCollapseAndExpand() throws {
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        try seed(hub)
        for scheme in (env["PERSONA_SCHEMES"] ?? "light,dark").split(separator: ",").map(String.init) {
            let app = launch(hub, scheme: scheme)
            let add = element(app, "todo.decisions.add")
            reveal(app, add)
            XCTAssertTrue(add.exists, "no Decisions section:\n\(app.debugDescription)")
            XCTAssertTrue(text(app, "决策 · 11").exists, "the header counts the eleven in effect")
            shot(app, "\(scheme)-1-first-five")

            let more = element(app, "todo.decisions.more")
            reveal(app, more, top: 520)
            shot(app, "\(scheme)-2-show-all-row")
            XCTAssertTrue(more.exists, "no Show All under the first five:\n\(app.debugDescription)")
            XCTAssertEqual(more.label, "显示全部 11 条")
            XCTAssertFalse(text(app, "测试设备覆盖到哪一代？").exists, "the eleventh shows before Show All")

            more.tap()
            reveal(app, more, top: 520)
            XCTAssertEqual(more.label, "只显示前 5 条")
            XCTAssertTrue(text(app, "测试设备覆盖到哪一代？").exists, "Show All leaves out the eleventh")
            shot(app, "\(scheme)-3-all")

            reveal(app, add, above: true)
            let fold = element(app, "todo.decisions.fold")
            XCTAssertTrue(fold.exists, "no fold button in the header:\n\(app.debugDescription)")
            XCTAssertEqual(fold.label, "全部折叠")
            fold.tap()
            XCTAssertTrue(element(app, firstRow).waitForNonExistence(timeout: 5), "Collapse All leaves the decisions")
            XCTAssertFalse(more.exists)
            XCTAssertTrue(text(app, "决策 · 11").exists, "folding changes the header's count")
            XCTAssertEqual(fold.label, "全部展开")
            shot(app, "\(scheme)-4-folded")

            fold.tap()
            XCTAssertTrue(element(app, firstRow).waitForExistence(timeout: 5), "Expand All shows no decisions")
            XCTAssertEqual(fold.label, "全部折叠")
            reveal(app, more, top: 520)
            XCTAssertEqual(more.label, "只显示前 5 条", "Expand All shows them all")
            shot(app, "\(scheme)-5-expanded")
            app.terminate()
        }
    }

    // MARK: - Helpers

    /// Records decisions on the weather to-do until it has twelve (made-up words, as the mock's own).
    private func seed(_ hub: String) throws {
        let questions = ["图标用哪套风格？", "温度单位默认哪个？", "离线时显示什么？", "预报数据多久刷新一次？", "先支持哪些语言？",
                         "要不要做锁屏小组件？", "动画放在哪里？", "崩溃上报用什么？", "测试设备覆盖到哪一代？"]
        let have = try count(hub)
        for q in questions.suffix(max(0, 12 - have)) {
            _ = try call(hub, "/api/v1/todos/\(todo)/decisions", body: ["question": q, "choice": "已定"])
        }
        XCTAssertEqual(try count(hub), 12)
    }

    private func count(_ hub: String) throws -> Int {
        let data = try call(hub, "/api/v1/todos/\(todo)/decisions?all=1")
        let list = (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["decisions"] as? [Any]
        return list?.count ?? 0
    }

    private func call(_ hub: String, _ path: String, body: [String: String]? = nil) throws -> Data {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        if let body {
            request.httpMethod = "POST"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        let result = Reply()
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { data, _, _ in
            result.data = data ?? Data()
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 15)
        return result.data
    }

    /// What the hub answered, handed back from URLSession's queue once the wait above is over.
    private final class Reply: @unchecked Sendable { var data = Data() }

    @MainActor private func launch(_ hub: String, scheme: String) -> XCUIApplication {
        addUIInterruptionMonitor(withDescription: "notifications") { alert in
            for label in ["允许", "Allow", "好", "OK"] where alert.buttons[label].exists { alert.buttons[label].tap(); return true }
            return false
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", scheme,
                               "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000002", "-openTodoDetail", "YES"]
        app.launchPinned()
        dismissSystemAlerts(wait: 4)
        return app
    }

    /// The notification question is SpringBoard's.
    @MainActor private func dismissSystemAlerts(wait: TimeInterval = 1) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        var deadline = Date.now.addingTimeInterval(wait)
        while Date.now < deadline {
            let alert = springboard.alerts.firstMatch
            if alert.waitForExistence(timeout: 0.5) {
                for label in ["允许", "Allow", "好", "OK"] where alert.buttons[label].exists { alert.buttons[label].tap(); break }
                deadline = Date.now.addingTimeInterval(1.5)
            }
        }
    }

    /// Scrolls the page until `el` is about `top` points from the top; a row not drawn yet is looked for further down,
    /// or further up with `above`.
    @MainActor private func reveal(_ app: XCUIApplication, _ el: XCUIElement, top: CGFloat = 150, above: Bool = false) {
        for _ in 0..<14 {
            guard el.exists else {
                drag(app, by: above ? -360 : 360)
                continue
            }
            let dy = el.frame.minY - top
            if abs(dy) < 50 { return }
            drag(app, by: max(-420, min(420, dy)))
        }
    }

    /// Moves the page up by `dy` points (down when negative), holding at the end so it does not coast.
    @MainActor private func drag(_ app: XCUIApplication, by dy: CGFloat) {
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: dy > 0 ? 0.8 : 0.3))
        start.press(forDuration: 0.05, thenDragTo: start.withOffset(CGVector(dx: 0, dy: -dy)), withVelocity: .slow, thenHoldForDuration: 0.3)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        dismissSystemAlerts()
        let png = app.screenshot()
        let a = XCTAttachment(screenshot: png)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = env["PERSONA_SHOTS"] {
            try? png.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("todo-decisions-\(name).png"))
        }
    }

    @MainActor private func text(_ app: XCUIApplication, _ s: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }
}
