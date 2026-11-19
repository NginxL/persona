import XCTest

/// A group title's Mark All Seen (#10422, docs/assistant/inbox.md "Group titles"): on FYI's group titles a pull-down
/// files the group's items or the whole section's, in one request with each item's version, and the toast's Undo brings
/// them back. Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB), which keeps the requests at /mock/inbox_bulk.
/// `MOCK_INBOX_EXTRA=<n>` on the mock (a dozen more FYI mails) also shows the mail group's title stuck at the top;
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` writes the screenshots there.
final class InboxGroupClearUITests: XCTestCase {
    @MainActor private func launch(_ app: XCUIApplication, pair: String, args: [String]) {
        let scheme = ProcessInfo.processInfo.environment["PERSONA_SHOTS_SCHEME"] ?? "light"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-ui.scheme", scheme]
        app.launchPinned()
        acceptIconAlert()
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", scheme] + args
        app.launchPinned()
        acceptIconAlert()
    }

    @MainActor func testMarkAGroupThenTheWholeSectionSeen() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // FYI alone on the section bar, by source: Lark first, then the mail account
        launch(app, pair: pair, args: ["-tab", "mail", "-inbox.kind", "", "-inbox.source", "", "-inbox.grouping", "", "-showInboxSection", "fyi"])
        func shot(_ name: String) {
            let image = app.screenshot()
            let a = XCTAttachment(screenshot: image)
            a.name = "\(name) \(UITestStyle.name)"
            a.lifetime = .keepAlways
            add(a)
            if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
                let scheme = ProcessInfo.processInfo.environment["PERSONA_STYLE"] ?? "light"
                let file = name.replacingOccurrences(of: " ", with: "-") + "-" + scheme + "-" + UITestStyle.name + ".png"
                try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
        }
        func inbox() throws -> [[String: Any]] {
            let data = try Data(contentsOf: URL(string: hub + "/api/v1/inbox?sources=all")!)
            return (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["items"] as? [[String: Any]] ?? []
        }
        func requests() throws -> [[String: Any]] {
            let bulk = try JSONSerialization.jsonObject(with: Data(contentsOf: URL(string: hub + "/mock/inbox_bulk")!)) as? [String: Any]
            return bulk?["requests"] as? [[String: Any]] ?? []
        }
        func ids(_ request: [String: Any]?) -> Set<String> {
            Set((request?["items"] as? [[String: Any]] ?? []).compactMap { $0["id"] as? String })
        }
        // InboxGroups.sourceKey, on the mock's JSON
        func sourceKey(_ x: [String: Any]) -> String {
            let source = x["source"] as? String ?? "", account = x["account"] as? String ?? ""
            if x["connector"] != nil { return "c:" + account }
            return source == "mail" ? "mail:" + account : source
        }
        let byID = Dictionary(try inbox().compactMap { x in (x["id"] as? String).map { ($0, x) } }, uniquingKeysWith: { a, _ in a })
        let fyiOpen = Set(byID.filter { _, x in
            x["done_at"] == nil && ((x["status"] as? String) ?? (x["tier"] as? String)) == "fyi"
        }.keys)
        XCTAssertGreaterThan(Set(fyiOpen.compactMap { byID[$0].map(sourceKey) }).count, 1, "the mock's FYI has one source")
        let earlier = try requests().count

        let menu = app.buttons.matching(identifier: "inbox.groupClear").firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 20), "no Mark All Seen on a group title:\n\(app.debugDescription)")
        XCTAssertEqual(menu.label, "全部看过", "the section's own words")
        sleep(1)
        shot("inbox group seen title")
        if ProcessInfo.processInfo.environment["PERSONA_SHOTS"] != nil {
            // the mail group's title stuck at the top, as it is once its rows scroll by (with MOCK_INBOX_EXTRA)
            app.swipeUp(velocity: .slow)
            sleep(1)
            shot("inbox group seen sticky")
            let stuck = app.buttons.matching(identifier: "inbox.groupClear").allElementsBoundByIndex.filter(\.isHittable).first
            stuck?.tap()
            if app.buttons["inbox.groupClear.group"].firstMatch.waitForExistence(timeout: 5) {
                sleep(1)
                shot("inbox group seen sticky menu")
                // close the menu where nothing is (above the list)
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.08)).tap()
            }
            app.swipeDown(velocity: .slow)
            app.swipeDown(velocity: .slow)
            sleep(1)
        }

        // The group: its items alone, one request with their versions.
        menu.tap()
        let group = app.buttons["inbox.groupClear.group"].firstMatch
        XCTAssertTrue(group.waitForExistence(timeout: 5), "no group choice in the menu:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["inbox.groupClear.section"].firstMatch.exists, "no whole-section choice in the menu")
        XCTAssertTrue(app.buttons["inbox.groupClear.section"].firstMatch.label.contains("知悉即可"), "the section choice does not name FYI")
        // "Mark all N items in Feishu as seen": the count shown in the group title
        let shown = Int(group.label.components(separatedBy: "全部 ").last?.components(separatedBy: " ").first ?? "") ?? -1
        sleep(1)
        shot("inbox group seen menu")
        group.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "已将 ")).firstMatch.waitForExistence(timeout: 5),
                      "no toast:\n\(app.debugDescription)")
        let undo = app.buttons["撤销"].firstMatch
        XCTAssertTrue(undo.waitForExistence(timeout: 2), "no 撤销 on the toast")
        shot("inbox group seen toast")
        // Undo while the toast is up; the requests are checked after
        undo.tap()
        let first = Array(try requests().dropFirst(earlier))
        XCTAssertEqual(first.first?["done"] as? Bool, true, "not POST /api/v1/inbox/done with done: \(first)")
        let sent = ids(first.first)
        let keys = Set(sent.compactMap { byID[$0].map(sourceKey) })
        XCTAssertEqual(keys.count, 1, "items of more than one group filed: \(sent)")
        XCTAssertEqual(sent.count, shown, "not the number the menu said: \(sent)")
        XCTAssertTrue(sent.isSubset(of: fyiOpen), "an item outside FYI filed: \(sent.subtracting(fyiOpen))")
        XCTAssertTrue((first.first?["items"] as? [[String: Any]] ?? []).allSatisfy { ($0["version"] as? String)?.isEmpty == false },
                      "items sent without their version")

        // Undo: the same ones back.
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "已移回 \(sent.count) 件")).firstMatch
            .waitForExistence(timeout: 5), "no undo toast:\n\(app.debugDescription)")
        let both = Array(try requests().dropFirst(earlier))
        XCTAssertEqual(both.count, 2, "not one request and its undo: \(both)")
        let back = Array(both.dropFirst())
        XCTAssertEqual(back.first?["done"] as? Bool, false, "撤销 did not reopen: \(back)")
        XCTAssertEqual(ids(back.first), sent)

        // The whole section, from the same menu: more than the group, all of it FYI.
        XCTAssertTrue(menu.waitForExistence(timeout: 5))
        sleep(2) // the undo toast's own time
        menu.tap()
        let section = app.buttons["inbox.groupClear.section"].firstMatch
        XCTAssertTrue(section.waitForExistence(timeout: 5), "no whole-section choice:\n\(app.debugDescription)")
        section.tap()
        XCTAssertTrue(app.buttons["撤销"].firstMatch.waitForExistence(timeout: 5), "no 撤销 after the section")
        let all = ids(Array(try requests().dropFirst(earlier + 2)).first)
        XCTAssertTrue(all.isStrictSuperset(of: sent), "the section did not cover more than the group: \(all)")
        XCTAssertTrue(all.isSubset(of: fyiOpen), "an item outside FYI filed: \(all.subtracting(fyiOpen))")
        app.buttons["撤销"].firstMatch.tap()
    }
}
