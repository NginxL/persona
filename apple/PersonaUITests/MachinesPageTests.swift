import XCTest

/// Machines (docs/server/machines.md「App」): the Agents list's machine menu opens every machine on one page, what needs a look
/// first (the offline NAS, then the busy server called out); a card opens the machine's page: its agents on top (one
/// opens the chat), the charts below, the range switches, the system last. Against scripts/mock/mock_hub.py
/// (TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port>); TEST_RUNNER_PERSONA_SHOT_DIR=<dir> also saves the screens
/// there as PNG for a look.
final class MachinesPageTests: XCTestCase {
    private func hub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        return hub
    }

    private func launch(_ hub: String, _ args: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-dev.openChat", ""] + args
        app.launchPinned()
        return app
    }

    private func shot(_ name: String) {
        let png = XCUIScreen.main.screenshot()
        let a = XCTAttachment(screenshot: png)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOT_DIR"] {
            try? png.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    /// A machine's card (in the shell the Agents page under it shows the machine names too).
    private func card(_ app: XCUIApplication, _ name: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", name + "、")).firstMatch
    }

    private func anything(_ app: XCUIApplication, _ label: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", label)).firstMatch
    }

    /// A card always has CPU / memory cells; only a loaded sample gives them numbers, rates and a memory total.
    @MainActor private func waitForCurrentMetrics(_ app: XCUIApplication, _ card: XCUIElement, _ name: String) {
        let numbers = ".*CPU.*[0-9][0-9.]*%.*(Memory|内存).*[0-9][0-9.]*%.*GB / .*GB.*↓.*↑.*"
        let loaded = XCTNSPredicateExpectation(predicate: NSPredicate(format: "label MATCHES %@", numbers), object: card)
        XCTAssertEqual(XCTWaiter.wait(for: [loaded], timeout: 20), .completed,
                       "\(name) did not get its current metrics: \(card.label)\n\(app.debugDescription)")
    }

    /// Bring evidence into the shot without a settling sleep; swipes are bounded and a missing target stays a failure.
    @MainActor private func reveal(_ app: XCUIApplication, _ element: XCUIElement) {
        let window = app.windows.firstMatch
        for _ in 0..<12 where !element.isHittable || element.frame.maxY > window.frame.maxY - 24 {
            app.swipeUp(velocity: .slow)
        }
        XCTAssertTrue(element.isHittable, "could not bring the metric evidence on screen:\n\(app.debugDescription)")
    }

    @MainActor private func openMachines(_ app: XCUIApplication) {
        let drawer = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["assistant-sidebar-button", "work-sidebar-button"])).firstMatch
        XCTAssertTrue(drawer.waitForExistence(timeout: 20), "no visible drawer button:\n\(app.debugDescription)")
        drawer.tapCenter()
        let item = app.buttons["sidebar-machines"].firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 10), app.debugDescription)
        item.tap()
    }

    /// #4031: one poll loads every manager. Each assistant's page shows its own computers, so check the default's server
    /// first, then the work assistant's MacBook. The old unscoped request leaves the MacBook at dashes instead of current metrics.
    @MainActor func testNonDefaultManagerMetricsInLightAndDark() throws {
        continueAfterFailure = false
        let hub = try hub()
        let text = ProcessInfo.processInfo.environment["PERSONA_TEXT_SIZE"].map { ["-UIPreferredContentSizeCategoryName", $0] } ?? []
        let suffix = text.isEmpty ? "" : "-ax5"
        for scheme in ["light", "dark"] {
            var app = launch(hub, ["-ui.scheme", scheme, "-openAssistantManager", "assistant"] + text)
            acceptIconAlert()
            openMachines(app)
            let server = card(app, "服务器")
            XCTAssertTrue(server.waitForExistence(timeout: 20), "the default manager's server is missing:\n\(app.debugDescription)")
            waitForCurrentMetrics(app, server, "the default manager's server")
            app.terminate()

            app = launch(hub, ["-ui.scheme", scheme, "-openAssistantManager", "m-3fa21c"] + text)
            acceptIconAlert()
            openMachines(app)

            let macBook = card(app, "MacBook")
            XCTAssertTrue(macBook.waitForExistence(timeout: 20), "工作's MacBook is missing:\n\(app.debugDescription)")
            waitForCurrentMetrics(app, macBook, "工作's MacBook")
            reveal(app, macBook)
            XCTAssertFalse(XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch.exists,
                           "a system alert covers the restored MacBook metrics")
            shot("manager-metrics-macbook-" + scheme + suffix)

            // The card is the overview regression; its detail also exposes the fixture's disk-backed current sample.
            macBook.tap()
            XCTAssertTrue(app.navigationBars["MacBook"].waitForExistence(timeout: 15), app.debugDescription)
            let disk = anything(app, "710 GB / 1000 GB")
            for _ in 0..<24 where !disk.exists { app.swipeUp(velocity: .slow) }
            XCTAssertTrue(disk.waitForExistence(timeout: 5), "the MacBook's disk sample is missing:\n\(app.debugDescription)")
            reveal(app, disk)
            shot("manager-metrics-macbook-detail-" + scheme + suffix)
            app.terminate()
        }
    }

    @MainActor func testListThenMachineThenRange() throws {
        let app = launch(try hub())
        let drawer = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["assistant-sidebar-button", "work-sidebar-button"]))
            .allElementsBoundByIndex.first(where: \.isHittable)
        XCTAssertNotNil(drawer, "no visible drawer button:\n\(app.debugDescription)")
        drawer?.tapCenter()
        XCTAssertFalse(app.buttons["机器监控"].exists, "the old name is gone")
        let item = app.buttons["sidebar-machines"].firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5), app.debugDescription)
        item.tap()

        let server = card(app, "服务器"), macBook = card(app, "MacBook")
        XCTAssertTrue(server.waitForExistence(timeout: 10), "no machines:\n\(app.debugDescription)")
        XCTAssertTrue(app.navigationBars["机器"].exists, "titled 机器")
        XCTAssertTrue(macBook.exists)
        XCTAssertTrue(app.staticTexts["3/4 台在线"].exists, "the offline NAS should not count")
        XCTAssertTrue(anything(app, "CPU 高").waitForExistence(timeout: 10), "the busy server is not called out")
        XCTAssertTrue(anything(app, "1 个 Agent 需要你").exists, "MacBook's agent waiting is not counted")
        // What needs a look first: offline, then too busy, then an agent waiting.
        let nas = card(app, "NAS")
        XCTAssertTrue(nas.exists)
        XCTAssertLessThan(nas.frame.minY, server.frame.minY, "the offline NAS goes first")
        XCTAssertLessThan(server.frame.minY, macBook.frame.minY, "the busy server before MacBook")
        shot("list")
        // With MOCK_NODE_MISMATCH=1 Mac Studio's card says its personad is not the hub's deploy, with Redeploy
        // (DeployButtonTests taps it); a look at it, then back up.
        let redeploy = app.buttons["重新部署"]
        if redeploy.exists {
            for _ in 0..<3 where !redeploy.isHittable { app.swipeUp() }
            sleep(1)
            shot("list-deploy")
            for _ in 0..<3 where !server.isHittable { app.swipeDown() }
        }

        server.tap()
        let range = app.descendants(matching: .any)["metric-range"].firstMatch
        XCTAssertTrue(range.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.navigationBars["服务器"].exists)
        // Its agent on top, the charts and the system on the same page.
        let agent = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "排查 /orders 接口变慢")).firstMatch
        XCTAssertTrue(agent.exists, "the server's agent")
        XCTAssertLessThan(agent.frame.minY, range.frame.minY, "agents above the charts")
        XCTAssertTrue(app.staticTexts["Ubuntu 24.04.1 LTS"].exists, "the system facts")
        XCTAssertFalse(app.buttons["系统与监控"].exists, "no second page for the charts")
        XCTAssertTrue(range.buttons["1 小时"].isSelected)
        shot("detail")

        app.swipeUp()
        sleep(1)
        shot("detail-1h")
        range.buttons["24 小时"].tap()
        XCTAssertTrue(range.buttons["24 小时"].isSelected)
        let cpu = app.descendants(matching: .any)["chart-CPU"]
        XCTAssertTrue(cpu.waitForExistence(timeout: 10), app.debugDescription)
        sleep(1)
        shot("detail-24h")

        // Hold a chart: the moment's time shows in its header.
        app.swipeUp()
        sleep(1)
        let chart = app.descendants(matching: .any)["chart-下行"]
        XCTAssertTrue(chart.waitForExistence(timeout: 5), app.debugDescription)
        let at = chart.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
        at.press(forDuration: 1.2, thenDragTo: chart.coordinate(withNormalizedOffset: CGVector(dx: 0.45, dy: 0.7)))
        shot("detail-24h-lower")
        app.swipeUp()
        app.swipeUp()
        sleep(1)
        XCTAssertTrue(app.staticTexts["AMD EPYC 9354 32-Core Processor"].waitForExistence(timeout: 5), "no system facts")
        XCTAssertTrue(anything(app, "ssh").exists || anything(app, "hub 所在机器").exists, "how the hub reaches it")
        shot("detail-system")

        // Back up: the agent opens its chat on top.
        for _ in 0..<6 where !agent.isHittable { app.swipeDown() }
        agent.tap()
        XCTAssertTrue((app.textFields.firstMatch.exists ? app.textFields.firstMatch : app.textViews.firstMatch).waitForExistence(timeout: 10),
                      "the agent's chat:\n\(app.debugDescription)")
    }

    /// An offline machine's page: its OS still known, Offline, Reconnect right under its card, history up to when it went away.
    @MainActor func testOfflineMachine() throws {
        let app = launch(try hub(), ["-openMonitor", "nas", "-openMonitorRange", "6h"])
        XCTAssertTrue(app.navigationBars["NAS"].waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(app.staticTexts["离线"].exists)
        let reconnect = app.buttons["重新连接"]
        XCTAssertTrue(reconnect.exists)
        XCTAssertTrue(reconnect.isHittable, "重新连接 on the first screen")
        XCTAssertTrue(anything(app, "机器离线，连上后显示它的 Agent").exists)
        XCTAssertTrue(app.staticTexts["Debian GNU/Linux 12 (bookworm)"].exists, "the system facts")
        shot("offline")
        app.swipeUp()
        sleep(1)
        shot("offline-6h")
    }
}
