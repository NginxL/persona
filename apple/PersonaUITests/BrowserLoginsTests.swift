import XCTest

/// Browser Sign-ins on a Mac's page (#1304) against scripts/mock/mock_hub.py with MOCK_BROWSER=ready (PERSONA_MOCK_HUB): the
/// sites ego lite keeps sign-ins for, a cookie file's sites to tick (none at first; the expired one cannot be), the
/// import, and clearing a site. `-openCookieFile` stands in for the system's file picker. With PERSONA_SHOTS each step is
/// saved there (PERSONA_SHOTS_APPEARANCE names them).
final class BrowserLoginsTests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_BROWSER=ready (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/cookies-\(env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown")-\(name).png"))
    }

    /// Two more made-up sites through the import API, so the section folds into the summary that opens the whole list
    /// (more than `BrowserSiteList.inPlace`, #5821) whichever test ran before against the same mock.
    @MainActor private func addSites() throws {
        var req = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/machines/macbook/browser/cookies")))
        req.httpMethod = "POST"
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let cookies = ["one", "two"].map { ["domain": ".list-\($0).example", "path": "/", "name": "sid", "value": "made-up", "expires": 2000000000] as [String: Any] }
        req.httpBody = try JSONSerialization.data(withJSONObject: ["cookies": cookies])
        var status = 0
        let done = expectation(description: "sites added")
        URLSession.shared.dataTask(with: req) { _, r, _ in status = (r as? HTTPURLResponse)?.statusCode ?? 0; done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
        XCTAssertEqual(status, 200, "the mock did not take the sites")
    }

    @MainActor func testSitesImportAndClear() throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("cookies.txt")
        try [".example.com\tTRUE\t/\tTRUE\t2000000000\tsid\tmade-up-1", "#HttpOnly_.example.com\tTRUE\t/\tTRUE\t2000000000\ttoken\tmade-up-2",
             ".shop.example.org\tTRUE\t/\tFALSE\t2000000000\tcart\tmade-up-3", ".news.example.net\tTRUE\t/\tFALSE\t1000\told\tmade-up-4"]
            .joined(separator: "\n").write(to: file, atomically: true, encoding: .utf8)
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook", "-openCookieFile", file.path]
        app.launchPinned()

        let sites = app.descendants(matching: .any).matching(identifier: "browser-logins-site")
        let page = app.scrollViews.firstMatch
        for _ in 0..<8 where !sites.firstMatch.isHittable { page.swipeUp(velocity: .slow) }
        // the file's sheet comes up once the sites are read
        let confirm = app.buttons["browser-logins-confirm"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 20), "no pick sheet")
        XCTAssertFalse(confirm.isEnabled, "something was ticked before the user did")
        let picks = app.buttons.matching(identifier: "browser-logins-pick")
        XCTAssertEqual(picks.count, 3)
        XCTAssertFalse(picks.element(boundBy: 1).isEnabled, "the expired site can be ticked")
        picks.element(boundBy: 0).tap()
        picks.element(boundBy: 2).tap()
        XCTAssertEqual(confirm.label, "导入 3 条")
        try shot(app, "pick")
        confirm.tap()
        XCTAssertTrue(confirm.waitForNonExistence(timeout: 15), "the sheet stayed")
        // The import makes four sites, more than the section shows in place (BrowserSiteList.inPlace): they fold into a
        // summary that opens the whole list, where a site is cleared (#5821).
        let summary = app.buttons["browser-logins-summary"]
        XCTAssertTrue(summary.waitForExistence(timeout: 10), "the sites did not fold:\n\(app.debugDescription)")
        for _ in 0..<4 where !summary.isHittable { page.swipeUp(velocity: .slow) }
        summary.tap()
        XCTAssertTrue(app.navigationBars["浏览器登录"].waitForExistence(timeout: 10), "the whole list did not open")
        let site = { (domain: String) in app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", domain)).firstMatch }
        XCTAssertTrue(site("example.org").waitForExistence(timeout: 10), "the imported site is not listed")
        try shot(app, "list")
        site("example.com").swipeLeft()
        let clear = app.buttons["清除"].firstMatch
        XCTAssertTrue(clear.waitForExistence(timeout: 5), "no Clear on the site's row")
        clear.tap()
        try shot(app, "clear")
        let sure = app.buttons.matching(identifier: "browser-logins-clear-confirm").firstMatch
        XCTAssertTrue(sure.waitForExistence(timeout: 5), "no confirmation")
        sure.tap()
        XCTAssertTrue(site("example.com").waitForNonExistence(timeout: 10), "the site was not cleared")
        // Three sites fit the machine page's section again, which lists example.org too: only the bar tells them apart.
        XCTAssertTrue(app.navigationBars["浏览器登录"].exists, "the list did not stay open")
        XCTAssertTrue(site("example.org").exists, "the other sites went with it")
    }

    /// iPhone's one top-trailing menu keeps Select and sorting reachable from the sidebar's machine detail.
    @MainActor func testWholeListKeepsSelectAndSortInOneMenu() throws {
        try addSites()
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES",
                               "-openMachineDetail", "macbook", "-openBrowserLogins", "YES"]
        app.launchPinned()
        let detail = app.navigationBars["MacBook"]
        XCTAssertTrue(detail.waitForExistence(timeout: 20), "the machine did not open")
        XCTAssertTrue(detail.buttons["BackButton"].exists)
        XCTAssertFalse(app.buttons["shell-place-sidebar"].exists)
        let summary = app.buttons["browser-logins-summary"]
        let page = app.scrollViews.firstMatch
        for _ in 0..<10 where !(summary.exists && summary.isHittable) { page.swipeUp(velocity: .slow) }
        XCTAssertTrue(summary.isHittable, "Browser Sign-ins summary is not reachable:\n\(app.debugDescription)")
        summary.tap()
        let bar = app.navigationBars["浏览器登录"]
        XCTAssertTrue(bar.waitForExistence(timeout: 20), "the whole Browser Sign-ins list did not open:\n\(app.debugDescription)")
        XCTAssertTrue(bar.buttons["BackButton"].exists)
        XCTAssertFalse(app.buttons["shell-place-sidebar"].exists)
        XCTAssertEqual(bar.buttons.matching(identifier: "BackButton").count, 1, "the child has more than one leading button")
        let trailing = bar.descendants(matching: .button).matching(identifier: "browser-logins-sort").allElementsBoundByIndex
        XCTAssertEqual(trailing.count, 1, "the one trailing menu is not directly accessible:\n\(bar.debugDescription)")
        try shot(app, "one-menu")

        app.buttons["browser-logins-sort"].tap()
        let select = app.buttons["browser-logins-select"]
        XCTAssertTrue(select.waitForExistence(timeout: 5), "Select is not in the one menu:\n\(app.debugDescription)")
        try shot(app, "menu-open")
        select.tap()
        XCTAssertTrue(app.buttons["browser-logins-select-all"].waitForExistence(timeout: 5), "Select did not start selection")
        app.buttons["browser-logins-sort"].tap()
        app.buttons["browser-logins-done"].tap()
        XCTAssertTrue(app.buttons["browser-logins-select-all"].waitForNonExistence(timeout: 5), "Done did not finish selection")

        app.buttons["browser-logins-sort"].tap()
        let expiring = app.buttons["快过期的在前"] // i18n-ignore: the existing localized sort choice
        XCTAssertTrue(expiring.waitForExistence(timeout: 5), "Sort By is not in the one menu")
        expiring.tap()
        XCTAssertTrue(app.buttons["browser-logins-sort"].waitForExistence(timeout: 5))
    }

    /// The node could not read ego's sign-ins (#1926; mock MOCK_COOKIES_FAIL=1, PERSONA_COOKIES_FAIL=1): a sentence and
    /// Try Again, which reads them again.
    @MainActor func testAFailedReadSaysSoAndTriesAgain() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_COOKIES_FAIL"] == "1" else {
            throw XCTSkip("needs the mock with MOCK_COOKIES_FAIL=1 (PERSONA_COOKIES_FAIL=1)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook"]
        app.launchPinned()
        let retry = app.buttons["browser-logins-retry"]
        let page = app.scrollViews.firstMatch
        XCTAssertTrue(retry.waitForExistence(timeout: 20), "no 再试一次")
        for _ in 0..<8 where !retry.isHittable { page.swipeUp(velocity: .slow) }
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS '读不到这台 Mac 上 ego 的登录'")).firstMatch.exists, "not said plainly")
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'Cannot read'")).firstMatch.exists)
        try shot(app, "failed")
        retry.tap()
        let site = app.descendants(matching: .any).matching(identifier: "browser-logins-site").firstMatch
        XCTAssertTrue(site.waitForExistence(timeout: 10), "再试一次 did not read them again")
        XCTAssertFalse(retry.exists)
    }
}

