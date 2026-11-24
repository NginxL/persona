import XCTest

/// Native-client E2E against an isolated, actual manager/source process. No injected trees or
/// transport: the DEBUG accessibility value only reports the committed renderer revision.
final class ExtensionStreamUITests: XCTestCase {
    @MainActor private func launch(_ appearance: String, ui: String = "showcase") throws -> XCUIApplication {
        let env = ProcessInfo.processInfo.environment
        guard let pair = env["PERSONA_STREAM_TEST_PAIR"], let parts = URLComponents(string: pair),
              let address = parts.queryItems?.first(where: { $0.name == "url" })?.value,
              let url = URL(string: address), ["127.0.0.1", "localhost"].contains(url.host() ?? "") else {
            throw XCTSkip("A dedicated local source hub pairing is required")
        }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-ApplePersistenceIgnoreState", "YES"]
        app.launchArguments += ["-openExtension", "showcase", "-openExtensionPage", ui, "-ui.scheme", appearance]
        app.launchPinned()
        let committed = waitForNativeReady(app, timeout: 30)
        let ready = app.descendants(matching: .any)["extension-ui-ready"].firstMatch
        let waiting = app.descendants(matching: .any)["extension-ui-waiting"].firstMatch
        let value = ready.exists ? (ready.value ?? "none") : (waiting.exists ? (waiting.value ?? "none") : "no page")
        if !committed {
            try shot(app, name: "diagnostic-inactive-" + appearance + "-" + ui)
            #if os(iOS)
            let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
            XCTFail("No active committed native tree: \(value)\n" + springboard.debugDescription + "\n" + app.debugDescription)
            #else
            XCTFail("No active committed native tree: \(value)")
            #endif
        }
        return app
    }

    @MainActor private func waitForNativeReady(_ app: XCUIApplication, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        let native = app.descendants(matching: .any).matching(NSPredicate(format: "value MATCHES %@", "stream:[1-9][0-9]*")).firstMatch
        while Date() < deadline {
            #if os(iOS)
            dismissKnownSystemPrompt()
            #endif
            if native.waitForExistence(timeout: 0.5) { return true }
        }
        return false
    }

    #if os(iOS)
    @MainActor private func dismissKnownSystemPrompt() {
        let brand = NSPredicate(format: "label CONTAINS[c] %@", "Persona")
        let icon = NSPredicate(format: "label CONTAINS[c] %@ OR label CONTAINS[c] %@ OR label CONTAINS %@", "图标", "icon", "アイコン")
        let notifications = NSPredicate(format: "label CONTAINS %@ OR label CONTAINS[c] %@", "通知", "notification")
        for owner in [XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
            let alert = owner.alerts.firstMatch
            guard alert.exists, alert.staticTexts.matching(brand).firstMatch.exists else { continue }
            // Only acknowledge UIKit's icon-change message or this test app's notification prompt.
            // Never automatically answer an extension's saved-action confirmation.
            let titles: [String]
            if alert.staticTexts.matching(notifications).firstMatch.exists { titles = ["允许", "Allow"] }
            else if alert.staticTexts.matching(icon).firstMatch.exists { titles = ["好", "OK", "了解"] }
            else { continue }
            for title in titles where alert.buttons[title].exists { alert.buttons[title].tap(); return }
        }
    }
    #endif

    @MainActor private func page(_ app: XCUIApplication, named title: String) {
        XCTAssertTrue(waitForNativeReady(app, timeout: 15), app.debugDescription)
        let row = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", title)).firstMatch
        #if os(iOS)
        for _ in 0..<12 where !row.isHittable { app.swipeUp() }
        #endif
        XCTAssertTrue(row.waitForExistence(timeout: 10), app.debugDescription)
        let usable = XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == true"), object: row)
        let ready = app.descendants(matching: .any)["extension-ui-ready"].firstMatch
        let result = XCTWaiter.wait(for: [usable], timeout: 15)
        XCTAssertEqual(result, .completed, "Native page lifecycle: \(ready.exists ? (ready.value ?? "none") : "waiting")")
        row.tap()
        XCTAssertTrue(waitForNativeReady(app, timeout: 20), app.debugDescription)
    }

    @MainActor private func shot(_ app: XCUIApplication, name: String) throws {
        if !name.hasPrefix("diagnostic-") { XCTAssertTrue(waitForNativeReady(app, timeout: 15), app.debugDescription) }
        let capture = app.screenshot()
        let attachment = XCTAttachment(screenshot: capture); attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_STREAM_SHOTS"] {
            try capture.pngRepresentation.write(to: URL(fileURLWithPath: directory).appending(path: name + ".png"))
        }
    }

    @MainActor private func buttons(_ appearance: String) throws {
        let app = try launch(appearance); defer { app.terminate() }
        page(app, named: "按钮")
        let ordinary = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "普通按钮")).firstMatch
        XCTAssertTrue(ordinary.waitForExistence(timeout: 20), app.debugDescription)
        let revision = NSPredicate(format: "value == %@", "stream:1")
        XCTAssertTrue(app.descendants(matching: .any).matching(revision).firstMatch.waitForExistence(timeout: 10))
        try shot(app, name: "ios-showcase-" + appearance)
        XCTAssertTrue(waitForNativeReady(app, timeout: 15), app.debugDescription)
        ordinary.tap()
        let advanced = NSPredicate(format: "value == %@", "stream:2")
        XCTAssertTrue(app.descendants(matching: .any).matching(advanced).firstMatch.waitForExistence(timeout: 15), "The accepted no-UI-change callback did not commit its exact +1 fence")
        let dangerous = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "危险按钮")).firstMatch
        dangerous.tap()
        let cancel = app.buttons.matching(NSPredicate(format: "label IN %@", ["取消", "Cancel"])).firstMatch
        XCTAssertTrue(cancel.waitForExistence(timeout: 5), app.debugDescription)
        cancel.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(advanced).firstMatch.exists, "Cancellation must not run the saved callback")
        XCTAssertTrue(dangerous.waitForExistence(timeout: 5))
        expectation(for: NSPredicate(format: "enabled == true"), evaluatedWith: dangerous)
        waitForExpectations(timeout: 5)
        dangerous.tap()
        let confirm = app.buttons["plugin-ui-confirm"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), app.debugDescription)
        confirm.tap()
        let approved = NSPredicate(format: "value == %@", "stream:3")
        XCTAssertTrue(app.descendants(matching: .any).matching(approved).firstMatch.waitForExistence(timeout: 15), "Explicit approval must execute the frozen callback once")
        #if os(iOS)
        if appearance == "light" { try showcaseControls(app) }
        #endif
    }

    #if os(iOS)
    @MainActor private func showcaseControls(_ app: XCUIApplication) throws {
        app.navigationBars.buttons.firstMatch.tap()
        page(app, named: "分段选择")
        let today = app.buttons["今天"].firstMatch
        XCTAssertTrue(today.waitForExistence(timeout: 10), app.debugDescription)
        today.tap()
        let second = NSPredicate(format: "value == %@", "stream:2")
        XCTAssertTrue(app.descendants(matching: .any).matching(second).firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        app.navigationBars.buttons.firstMatch.tap()
        page(app, named: "列表")
        let more = app.buttons["plugin-ui-more"]
        for _ in 0..<15 where !more.isHittable { app.swipeUp() }
        XCTAssertTrue(more.isHittable, app.debugDescription)
        more.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(second).firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        let forty = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "显示 40 条")).firstMatch
        for _ in 0..<15 where !forty.isHittable { app.swipeDown() }
        XCTAssertTrue(forty.waitForExistence(timeout: 10), app.debugDescription)
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.isHittable, app.debugDescription)
        search.tap(); search.typeText("02")
        let third = NSPredicate(format: "value == %@", "stream:3")
        XCTAssertTrue(app.descendants(matching: .any).matching(third).firstMatch.waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "示例条目 02")).firstMatch.waitForExistence(timeout: 10))
    }
    #endif

    @MainActor private func reviewBar(_ appearance: String) throws {
        let app = try launch(appearance, ui: "code-review-v5"); defer { app.terminate() }
        let main = app.buttons["code-open-main"]
        if main.exists { main.tap() }
        XCTAssertTrue(app.staticTexts["草稿"].waitForExistence(timeout: 10), app.debugDescription)
        let toggle = app.buttons["extension-review-toggle"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), app.debugDescription)
        let summary = app.descendants(matching: .any)["extension-review-summary"].firstMatch
        if !summary.exists { toggle.tap() }
        XCTAssertTrue(summary.waitForExistence(timeout: 5), app.debugDescription)
        summary.tap(); summary.typeText("Synthetic review summary")
        toggle.tap()
        toggle.tap()
        XCTAssertEqual(summary.value as? String, "Synthetic review summary")
        let comment = app.buttons.matching(NSPredicate(format: "label == %@", "评论")).firstMatch
        XCTAssertTrue(comment.isHittable, app.debugDescription)
        comment.tap()
        let second = NSPredicate(format: "value == %@", "stream:2")
        XCTAssertTrue(app.descendants(matching: .any).matching(second).firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        let discard = app.buttons.matching(NSPredicate(format: "label IN %@", ["放弃草稿", "Discard Drafts"])).firstMatch
        discard.tap()
        let cancel = app.buttons.matching(NSPredicate(format: "label IN %@", ["取消", "Cancel"])).firstMatch
        XCTAssertTrue(cancel.waitForExistence(timeout: 5), app.debugDescription)
        cancel.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(second).firstMatch.exists)
        discard.tap()
        let confirm = app.buttons["plugin-ui-confirm"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), app.debugDescription)
        confirm.tap()
        let third = NSPredicate(format: "value == %@", "stream:3")
        XCTAssertTrue(app.descendants(matching: .any).matching(third).firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        try shot(app, name: "ios-review-bar-" + appearance)
    }

    @MainActor func testReviewBarLightNativeActionsAndCancellation() throws { try reviewBar("light") }
    @MainActor func testReviewBarDarkNativeActionsAndCancellation() throws { try reviewBar("dark") }

    @MainActor func testShowcaseLightStreamFenceAndCancellation() throws { try buttons("light") }
    @MainActor func testShowcaseDarkStreamFenceAndCancellation() throws { try buttons("dark") }

    @MainActor func testSyntheticNativeFocusLinesAndReview() throws {
        let app = try launch("light", ui: "native-contract"); defer { app.terminate() }
        let field = app.textFields["plugin-ui-field-draft"]
        XCTAssertTrue(field.waitForExistence(timeout: 15), app.debugDescription)
        field.tap(); field.typeText("Synthetic draft")
        let tick = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "无视觉变化回调")).firstMatch
        XCTAssertTrue(tick.isHittable, app.debugDescription)
        tick.tap()
        let second = NSPredicate(format: "value == %@", "stream:2")
        XCTAssertTrue(app.descendants(matching: .any).matching(second).firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        #if os(iOS)
        XCTAssertTrue(app.keyboards.firstMatch.exists, "An unrelated fence must not dismiss the focused native field")
        #endif
        field.typeText(" retained")
        XCTAssertEqual(field.value as? String, "Synthetic draft retained")
        let load = app.buttons["code-load-more"]
        #if os(iOS)
        for _ in 0..<6 where !load.isHittable { app.swipeUp() }
        #endif
        XCTAssertTrue(load.isHittable, app.debugDescription)
        load.tap()
        let third = NSPredicate(format: "value == %@", "stream:3")
        XCTAssertTrue(app.descendants(matching: .any).matching(third).firstMatch.waitForExistence(timeout: 15))
        expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: load)
        waitForExpectations(timeout: 10)
        let submit = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "提交审阅")).firstMatch
        #if os(iOS)
        for _ in 0..<6 where !submit.isHittable { app.swipeUp() }
        #endif
        XCTAssertTrue(submit.isHittable, app.debugDescription)
        submit.tap()
        let cancel = app.buttons.matching(NSPredicate(format: "label IN %@", ["取消", "Cancel"])).firstMatch
        XCTAssertTrue(cancel.waitForExistence(timeout: 5))
        cancel.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(third).firstMatch.exists)
    }

    @MainActor func testShowcaseFieldDraftSurvivesUnrelatedFence() throws {
        let app = try launch("light"); defer { app.terminate() }
        page(app, named: "输入")
        let field = app.textFields["plugin-ui-field-empty"]
        XCTAssertTrue(field.waitForExistence(timeout: 20), app.debugDescription)
        field.tap(); field.typeText("Synthetic draft")
        let submit = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "提交")).firstMatch
        #if os(iOS)
        for _ in 0..<6 where !submit.isHittable { app.swipeUp() }
        #endif
        XCTAssertTrue(submit.isHittable, app.debugDescription)
        submit.tap()
        let advanced = NSPredicate(format: "value == %@", "stream:2")
        XCTAssertTrue(app.descendants(matching: .any).matching(advanced).firstMatch.waitForExistence(timeout: 15))
        XCTAssertEqual(field.value as? String, "Synthetic draft")
    }
}
