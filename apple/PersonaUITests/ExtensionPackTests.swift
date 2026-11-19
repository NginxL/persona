import XCTest

/// Extension packs (#4072, #4122), against scripts/mock/mock_hub.py with MOCK_EXTENSIONS=1 MOCK_EXTENSION_PACK=1
/// (`TEST_RUNNER_PERSONA_EXTENSION_PACK_HUB=http://127.0.0.1:<port>`): the pack's page names its members, the uninstall
/// confirmation names them, an uninstall with members that stopped part way says what was uninstalled, what wasn't and
/// why, and what wasn't tried, and a pack's install preview names its members. Each in light, dark and at the largest
/// text size; `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots (`pack-<step>-<look>.png`).
/// `TEST_RUNNER_PERSONA_PHASE=before` takes the same steps on an older build (its xctestrun pointed at it), for before /
/// after screenshots, and checks nothing that build doesn't do. The switch tests take a second hub with
/// `MOCK_EXTENSION_PACK=legacy` in `TEST_RUNNER_PERSONA_EXTENSION_PACK_OLD_HUB` for the older-hub wording and no prompt.
final class ExtensionPackTests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var before: Bool { env["PERSONA_PHASE"] == "before" }
    /// The mock's first market, where both packs come from.
    private let market = "3f2a9c1d0b"
    private let looks: [(name: String, args: [String])] = [
        ("light", ["-ui.scheme", "light"]),
        ("dark", ["-ui.scheme", "dark"]),
        ("large", ["-ui.scheme", "light", "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]),
    ]

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor private func launch(_ args: [String], hub hubOverride: String? = nil) throws -> XCUIApplication {
        guard let hub = hubOverride ?? env["PERSONA_EXTENSION_PACK_HUB"] else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 MOCK_EXTENSION_PACK=1 (PERSONA_EXTENSION_PACK_HUB)")
        }
        addUIInterruptionMonitor(withDescription: "notifications, the icon's note") { alert in
            for label in ["允许", "Allow", "OK", "好"] where alert.buttons[label].exists { alert.buttons[label].tap(); return true } // i18n-ignore
            return false
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings"] + args
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where springboard.buttons[label].waitForExistence(timeout: 3) { springboard.buttons[label].tap() }
        if springboard.alerts.firstMatch.waitForExistence(timeout: 2) {
            HomeScreen.dismissIconNote()
            app.terminate()
            app.launchPinned()
            XCTAssertFalse(springboard.alerts.firstMatch.waitForExistence(timeout: 1),
                           "the same appearance must not ask about its icon again")
        }
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ step: String, _ look: String) {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        HomeScreen.dismissIconNote()
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "pack-\(step)-\(look).png"))
    }

    /// Any element whose label has `text` (a row's combined label has its name and its line).
    @MainActor private func labeled(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    /// Scrolls up a part of a screen at a time until `el` can be tapped (a whole-screen swipe can carry it past the top).
    @MainActor private func reveal(_ el: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<14 where !(el.exists && el.isHittable) {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.72))
                .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)))
        }
    }

    @MainActor func testPackMembersByName() throws {
        for look in looks {
            // Names are checked where every row is on the screen; the largest text pushes some off.
            let check = !before && look.name != "large"
            // The pack's page: Pack Members by name, the id under it (#4122); the one not installed keeps its id.
            var app = try launch(["-openExtension", "example-pack"] + look.args)
            XCTAssertTrue(labeled(app, "Example 合集").waitForExistence(timeout: 15), "the pack's page")
            let mail = labeled(app, "example-mail")
            reveal(mail, in: app)
            XCTAssertTrue(mail.exists, "the member not installed, by its id")
            if check {
                let notes = labeled(app, "Example Notes")
                XCTAssertTrue(notes.exists && notes.label.contains("example-notes"), notes.label)
                XCTAssertTrue(labeled(app, "Example 助手").label.contains("github:example/team-plugins"))
            }
            shot(app, "page", look.name)

            // The market page's Uninstall: the confirmation names the members (#4072).
            app = try launch(["-openMarketExtension", "\(market)/example-pack"] + look.args)
            let remove = app.buttons["market-uninstall"].firstMatch
            // The actions are in a lazy list: at the largest text Uninstall is only there once scrolled to.
            XCTAssertTrue(labeled(app, "Example 合集").waitForExistence(timeout: 15), "the pack's market page")
            reveal(remove, in: app)
            XCTAssertTrue(remove.waitForExistence(timeout: 5))
            remove.tap()
            let withMembers = app.buttons["market-uninstall-members"].firstMatch // a dialog lists its buttons twice
            XCTAssertTrue(withMembers.waitForExistence(timeout: 5))
            if check { XCTAssertTrue(labeled(app, "Example Notes").label.contains("example-mail")) }
            shot(app, "confirm", look.name)

            // With members: the hub stops part way. The card lists each group by name (example-gateway, installed, by
            // its name), the hub's reason with the one it couldn't uninstall, and keeps Retry.
            withMembers.tap()
            let card = app.descendants(matching: .any)["extension-operation-error"]
            XCTAssertTrue(card.waitForExistence(timeout: 15), "the failed uninstall's card")
            for _ in 0..<6 where !card.isHittable { app.swipeDown() }
            if !before {
                for part in ["Example Notes", "Example 助手", "扩展还在运行，停不下来", "Example 网关面板"] {
                    XCTAssertTrue(card.label.contains(part), "\(part) in \(card.label)")
                }
            }
            shot(app, "partial", look.name)
            let retry = app.buttons["extension-operation-retry"].firstMatch
            if !retry.isHittable {
                reveal(retry, in: app)
                shot(app, "partial-retry", look.name)
            }

            // A pack's install preview names its members: one installed, one installed with it (#4122).
            app = try launch(["-openMarketExtension", "\(market)/example-suite"] + look.args)
            let install = app.buttons["market-install"].firstMatch
            XCTAssertTrue(install.waitForExistence(timeout: 15))
            reveal(install, in: app)
            install.tap()
            XCTAssertTrue(app.buttons["market-confirm"].firstMatch.waitForExistence(timeout: 15), "the preview")
            let calendar = labeled(app, "example-calendar")
            XCTAssertTrue(calendar.waitForExistence(timeout: 5))
            reveal(calendar, in: app)
            if check {
                XCTAssertTrue(calendar.label.contains("Example 日历"), calendar.label)
                XCTAssertTrue(labeled(app, "Example Notes").label.contains("example-notes"))
            }
            shot(app, "preview", look.name)
        }
    }

    @MainActor private func setPackEnabled(_ enabled: Bool, hub: String) async throws {
        var request = URLRequest(url: URL(string: hub + "/api/v1/extensions/example-pack/-/enabled")!)
        request.httpMethod = "PUT"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["enabled": enabled])
        let (_, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
    }

    @MainActor private func toggle(_ element: XCUIElement, in app: XCUIApplication,
                                   changingTo expected: String? = nil, presenting dialogID: String? = nil) {
        // Retry only a synthesized tap with no observable outcome. The caller's state, wording and write assertions stay exact.
        for _ in 0..<3 {
            HomeScreen.dismissIconNote()
            let control = element.switches.firstMatch
            let target = control.exists ? control : element
            let ready = XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == true AND hittable == true"), object: target)
            XCTAssertEqual(XCTWaiter.wait(for: [ready], timeout: 10), .completed,
                           "the switch must finish loading before it is tapped")
            target.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
            if let dialogID, app.buttons[dialogID].waitForExistence(timeout: 2) { return }
            if let expected {
                let changed = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", expected), object: element)
                if XCTWaiter.wait(for: [changed], timeout: 2) == .completed { return }
            }
        }
    }

    @MainActor private func value(_ expected: String, of element: XCUIElement) {
        let wait = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", expected), object: element)
        XCTAssertEqual(XCTWaiter.wait(for: [wait], timeout: 5), .completed,
                       "expected \(expected), got \(String(describing: element.value))")
    }

    @MainActor private func systemShot(_ step: String, _ look: String) {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        HomeScreen.dismissIconNote()
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir)
            .appending(path: "pack-\(step)-\(look).png"))
    }

    @MainActor func testPackSwitchConfirmationAndFootnotes() async throws {
        if before { throw XCTSkip("the before build has no pack-switch confirmation") }
        guard let hub = env["PERSONA_EXTENSION_PACK_HUB"] else {
            throw XCTSkip("needs the current pack mock")
        }
        let switchLooks = [looks[0], looks[1],
                           (name: "large-dark", args: ["-ui.scheme", "dark", "-UIPreferredContentSizeCategoryName",
                                                       "UICTContentSizeCategoryAccessibilityXXXL"]),
                           looks[2]]
        let languages = [
            ("en", "en_US", "Turning the pack off turns off its members too", "Turning the pack back on turns on"),
            ("zh-Hans", "zh_CN", "停用合集会一起停用它的成员", "重新启用合集会重新启用"),
        ]
        for (language, locale, onText, offText) in languages {
            for look in switchLooks {
                try await setPackEnabled(true, hub: hub)
                let app = try launch(["-openExtension", "example-pack", "-AppleLanguages", "(\(language))",
                                      "-AppleLocale", locale, "-probes", "YES"] + look.args)
                let enabled = app.switches["plugin-enabled"]
                XCTAssertTrue(enabled.waitForExistence(timeout: 15), app.debugDescription)
                value("1", of: enabled)
                let reason = app.descendants(matching: .any)["extension-enabled-reason"].firstMatch
                XCTAssertTrue(reason.waitForExistence(timeout: 10) && reason.label.contains(onText), app.debugDescription)
                shot(app, "switch-on-\(language)", look.name)

                toggle(enabled, in: app, presenting: "extension-pack-disable-confirm")
                let confirm = app.buttons["extension-pack-disable-confirm"].firstMatch
                let cancel = app.buttons["extension-pack-disable-cancel"].firstMatch
                XCTAssertTrue(confirm.waitForExistence(timeout: 5), app.debugDescription)
                let confirmReady = XCTNSPredicateExpectation(predicate: NSPredicate(format: "hittable == true"), object: confirm)
                XCTAssertEqual(XCTWaiter.wait(for: [confirmReady], timeout: 5), .completed,
                               "the destructive action must settle before it is measured")
                value("1", of: enabled)
                let confirmHeight = confirm.frame.height
                XCTAssertGreaterThan(confirmHeight, 0)
                systemShot("switch-confirm-\(language)", look.name)
                let confirmContext = "\(confirm.label) at \(look.name) in \(language)"
                let confirmLabel = try labelFrame(for: confirm, in: app)
                assertLabel(confirmLabel, inside: confirm, context: confirmContext)

                // The compact iOS 27 dialog exposes cancellation by outside tap; older layouts also render a Cancel button.
                if cancel.exists {
                    let cancelReady = XCTNSPredicateExpectation(predicate: NSPredicate(format: "hittable == true"), object: cancel)
                    XCTAssertEqual(XCTWaiter.wait(for: [cancelReady], timeout: 5), .completed,
                                   "Cancel must settle before it is measured")
                    XCTAssertGreaterThan(cancel.frame.height, 0)
                    XCTAssertEqual(confirmHeight, cancel.frame.height, accuracy: 2,
                                   "the dialog buttons must stay on one line")
                    let cancelContext = "\(cancel.label) at \(look.name) in \(language)"
                    let cancelLabel = try labelFrame(for: cancel, in: app)
                    assertLabel(cancelLabel, inside: cancel, context: cancelContext)
                }

                if language == "en" && look.name == "light" {
                    if cancel.exists && cancel.isHittable { cancel.tap() }
                    else { app.coordinate(withNormalizedOffset: CGVector(dx: 0.05, dy: 0.25)).tap() }
                    XCTAssertTrue(confirm.waitForNonExistence(timeout: 5))
                    value("1", of: enabled)
                    toggle(enabled, in: app, presenting: "extension-pack-disable-confirm")
                    XCTAssertTrue(confirm.waitForExistence(timeout: 5))
                }
                let confirmAgain = XCTNSPredicateExpectation(predicate: NSPredicate(format: "hittable == true"), object: confirm)
                XCTAssertEqual(XCTWaiter.wait(for: [confirmAgain], timeout: 5), .completed,
                               "the destructive action must be visible before it is tapped")
                confirm.tap()
                value("0", of: enabled)
                let restored = app.descendants(matching: .any)["extension-enabled-reason"].firstMatch
                XCTAssertTrue(restored.waitForExistence(timeout: 10) && restored.label.contains(offText), app.debugDescription)
                for name in ["Example Notes", "Example 助手", "Example 网关面板"] {
                    XCTAssertTrue(restored.label.contains(name), "missing restored member \(name) in \(restored.label)")
                }
                shot(app, "switch-off-\(language)", look.name)

                toggle(enabled, in: app, changingTo: "1")
                value("1", of: enabled)
                XCTAssertFalse(confirm.exists, "switching a pack on must not ask")
                // End each look cleanly; relaunching or tearing down while the previous app is still active can be
                // reported as a crash by XCTest even after every action and state assertion has passed.
                app.terminate()
            }
        }
    }

    private struct DialogLabel: Decodable {
        let text: String
        let x, y, width, height: Double
        var frame: CGRect { CGRect(x: x, y: y, width: width, height: height) }
    }

    @MainActor private func labelFrame(for button: XCUIElement, in app: XCUIApplication) throws -> CGRect {
        let probe = app.descendants(matching: .any)["probe.confirmation-dialog-labels"].firstMatch
        let value = try XCTUnwrap(probe.value as? String, "missing dialog-label probe")
        let labels = try JSONDecoder().decode([DialogLabel].self, from: Data(value.utf8))
        return try XCTUnwrap(labels.first(where: { $0.text == button.label })?.frame,
                             "no UIKit label for \(button.label) in \(value)")
    }

    @MainActor private func assertLabel(_ labelFrame: CGRect, inside button: XCUIElement, context: String) {
        let buttonFrame = button.frame
        // A shared edge can round differently between accessibility frames; half a point covers only that rounding.
        let contained = buttonFrame.insetBy(dx: -0.5, dy: -0.5).contains(labelFrame)
        if !contained {
            let evidence = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
            evidence.name = "Confirmation label overflow: \(context)"
            evidence.lifetime = .keepAlways
            add(evidence)
        }
        XCTAssertTrue(contained,
                      "\(context): label \(labelFrame) is outside button \(buttonFrame)")
    }

    @MainActor func testPackSwitchOnLegacyHub() async throws {
        if before { throw XCTSkip("the before build uses the old mock fields") }
        guard let hub = env["PERSONA_EXTENSION_PACK_OLD_HUB"] else {
            throw XCTSkip("needs MOCK_EXTENSION_PACK=legacy (PERSONA_EXTENSION_PACK_OLD_HUB)")
        }
        let languages = [
            ("en", "en_US", "Turning the pack off turns off only the pack itself"),
            ("zh-Hans", "zh_CN", "停用合集只会停用合集本身"),
        ]
        for (language, locale, footnote) in languages {
            for look in looks.prefix(2) {
                try await setPackEnabled(true, hub: hub)
                let app = try launch(["-openExtension", "example-pack", "-AppleLanguages", "(\(language))",
                                      "-AppleLocale", locale] + look.args, hub: hub)
                let enabled = app.switches["plugin-enabled"]
                XCTAssertTrue(enabled.waitForExistence(timeout: 15), app.debugDescription)
                value("1", of: enabled)
                let reason = app.descendants(matching: .any)["extension-enabled-reason"].firstMatch
                XCTAssertTrue(reason.waitForExistence(timeout: 10) && reason.label.contains(footnote), app.debugDescription)
                shot(app, "switch-legacy-\(language)", look.name)

                toggle(enabled, in: app, changingTo: "0")
                value("0", of: enabled)
                XCTAssertFalse(app.buttons["extension-pack-disable-confirm"].exists,
                               "an older hub must not show the pack-member confirmation")
            }
        }
        try await setPackEnabled(true, hub: hub)
    }
}
