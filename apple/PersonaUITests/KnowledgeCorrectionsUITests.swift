import XCTest

/// Against a fresh scripts/mock/mock_hub.py with MOCK_KB_CORRECTIONS=1 (PERSONA_MOCK_HUB).
/// PERSONA_SHOTS=<dir> keeps screenshots. The unlink tests use distinct relations in the shared fixture.
final class KnowledgeCorrectionsUITests: XCTestCase {
    private var hub = ""
    private let facts = "负责天气 App 的图标和视觉。" // i18n-ignore

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_KB_CORRECTIONS=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    @MainActor func testDeletingConfirmedFactsOffersUndo() throws {
        put("/api/v1/kb/people/lilei", ["pinned": facts])
        let app = open("people/lilei")
        let text = app.staticTexts["kb.facts"]
        XCTAssertTrue(text.waitForExistence(timeout: 15), "no confirmed facts:\n\(app.debugDescription)")
        XCTAssertTrue(text.label.contains("图标和视觉"), text.label) // i18n-ignore
        shot(app, "facts")

        app.buttons["kb.facts.edit"].tap()
        XCTAssertTrue(app.textViews["kb.facts.editor"].waitForExistence(timeout: 5), "the editor did not open")
        shot(app, "facts-editor")
        app.buttons["kb.facts.delete"].tap()

        XCTAssertTrue(app.buttons["kb.facts.add"].waitForExistence(timeout: 5), "the facts did not go:\n\(app.debugDescription)")
        let undo = app.buttons["撤销"] // i18n-ignore
        XCTAssertTrue(undo.waitForExistence(timeout: 5), "no Undo:\n\(app.debugDescription)")
        shot(app, "facts-deleted")
        wait("the hub did not get the empty facts") { pinnedSent().last == "" }
        undo.tap()

        XCTAssertTrue(app.staticTexts["kb.facts"].waitForExistence(timeout: 10), "Undo did not bring the facts back")
        wait("Undo did not write the old facts back") { pinnedSent().last == facts }
    }

    @MainActor func testRemovingOnlyThisLinkAndAllowingItAgain() throws {
        let person = "people/lilei", project = "projects/weather-app"
        requireLink("weather-app", in: "projects", on: person)
        let before = updates().count
        let app = open(person)
        let row = findRow("天气 App", in: app) // i18n-ignore
        let toggle = app.switches["kb.unlink.lockLists"]
        let lockExplanation = app.staticTexts["kb.unlink.lockExplanation"]
        let cancel = app.buttons["kb.unlink.cancel"]
        let confirm = app.buttons["kb.unlink.confirm"]
        askToRemove(row, in: app)
        XCTAssertTrue(toggle.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertEqual(toggle.value as? String, "0")
        XCTAssertFalse(lockExplanation.exists)
        toggle.tap()
        XCTAssertTrue(lockExplanation.waitForExistence(timeout: 5))
        XCTAssertTrue(cancel.isHittable && confirm.isHittable)
        cancel.tap()
        XCTAssertTrue(cancel.waitForNonExistence(timeout: 5))
        XCTAssertTrue(row.isHittable)
        XCTAssertEqual(updates().count, before, "Cancel must not write")

        askToRemove(row, in: app)
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.value as? String, "0", "Each confirmation starts with the switch off")
        let message = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "都会保留")).firstMatch // i18n-ignore
        XCTAssertTrue(message.exists, app.debugDescription)
        shot(app, "unlink-default")
        let start = ProcessInfo.processInfo.systemUptime
        confirm.tap()
        XCTAssertTrue(row.waitForNonExistence(timeout: 5), "The link should disappear immediately")
        let hiddenAt = ProcessInfo.processInfo.systemUptime
        wait("the hub did not get remove_projects") { self.updates().count == before + 1 }
        let remove = try XCTUnwrap(updates().last)
        XCTAssertEqual(remove["remove_projects"] as? [String], ["weather-app"])
        XCTAssertEqual(remove["lock_lists"] as? Bool, false)
        assertRemoved("weather-app", from: "projects", on: person, locked: false)
        assertRemoved("lilei", from: "people", on: project, locked: false)
        XCTAssertTrue(members("chats", on: person).contains("design-review"))
        XCTAssertTrue(members("people", on: project).contains("p_han"))

        let allow = app.buttons["kb.link.allow.projects/weather-app"]
        scrollTo(allow, in: app)
        XCTAssertTrue(allow.isHittable, app.debugDescription)
        let reconciledAt = ProcessInfo.processInfo.systemUptime
        print("KB unlink UI observations: tap to hidden \(hiddenAt - start)s, tap to recovery row \(reconciledAt - start)s (includes XCTest and scrolling)")
        shot(app, "unlink-removed")
        allow.tap()
        XCTAssertTrue(allow.waitForNonExistence(timeout: 10), app.debugDescription)
        let restore = try XCTUnwrap(updates().last)
        XCTAssertEqual(updates().count, before + 2)
        XCTAssertEqual(restore["allow_projects"] as? [String], ["weather-app"])
        XCTAssertNil(restore["lock_lists"])
        assertAllowed("weather-app", from: "projects", on: person, locked: false)
        assertAllowed("lilei", from: "people", on: project, locked: false)
    }

    @MainActor func testRemovingWithExplicitLockProtectsBothLists() throws {
        let person = "people/lilei", chat = "chats/design-review"
        requireLink("design-review", in: "chats", on: person)
        requireLink("lilei", in: "people", on: chat)
        let before = updates().count
        let app = open(person)
        let row = findRow("设计评审", in: app) // i18n-ignore
        askToRemove(row, in: app, byHolding: true)
        let toggle = app.switches["kb.unlink.lockLists"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertEqual(toggle.value as? String, "0")
        toggle.tap()
        let explanation = app.staticTexts["kb.unlink.lockExplanation"]
        XCTAssertTrue(explanation.waitForExistence(timeout: 5))
        XCTAssertTrue(explanation.label.contains("李雷") && explanation.label.contains("设计评审")) // i18n-ignore
        let confirm = app.buttons["kb.unlink.confirm"]
        XCTAssertTrue(confirm.isHittable && app.buttons["kb.unlink.cancel"].isHittable)
        shot(app, "unlink-lock")
        confirm.tap()
        wait("the hub did not get remove_chats") { self.updates().count == before + 1 }
        let remove = try XCTUnwrap(updates().last)
        XCTAssertEqual(remove["remove_chats"] as? [String], ["design-review"])
        XCTAssertEqual(remove["lock_lists"] as? Bool, true)
        assertRemoved("design-review", from: "chats", on: person, locked: true)
        assertRemoved("lilei", from: "people", on: chat, locked: true)
        XCTAssertTrue(members("people", on: chat).contains("p_han"))

        let allow = app.buttons["kb.link.allow.chats/design-review"]
        scrollTo(allow, in: app)
        XCTAssertTrue(allow.isHittable, app.debugDescription)
        allow.tap()
        XCTAssertTrue(allow.waitForNonExistence(timeout: 10), app.debugDescription)
        XCTAssertEqual(updates().count, before + 2)
        XCTAssertEqual(updates().last?["allow_chats"] as? [String], ["design-review"])
        assertAllowed("design-review", from: "chats", on: person, locked: true)
        assertAllowed("lilei", from: "people", on: chat, locked: true)
    }

    // MARK: Helpers

    @MainActor private func open(_ entry: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openAssistantPage", "knowledge", "-showKnowledgeEntry", entry]
        app.launchPinned()
        return app
    }

    @MainActor private func scrollTo(_ element: XCUIElement, in app: XCUIApplication) {
        let list = app.collectionViews.firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 15), app.debugDescription)
        for _ in 0..<6 where !element.isHittable { list.swipeUp(velocity: .slow) }
    }

    @MainActor private func findRow(_ label: String, in app: XCUIApplication) -> XCUIElement {
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", label)).firstMatch
        scrollTo(row, in: app)
        XCTAssertTrue(row.isHittable, "No linked entry named \(label) in \(app.debugDescription)")
        return row
    }

    @MainActor private func askToRemove(_ row: XCUIElement, in app: XCUIApplication, byHolding: Bool = false) {
        if byHolding { row.press(forDuration: 1) } else { row.swipeLeft() }
        let remove = app.buttons["移除关联"].firstMatch // i18n-ignore
        XCTAssertTrue(remove.waitForExistence(timeout: 5), app.debugDescription)
        remove.tap()
    }

    private func updates() -> [[String: Any]] {
        (get("/mock/kb_updates")["updates"] as? [[String: Any]] ?? []).filter {
            $0["remove_projects"] != nil || $0["remove_chats"] != nil || $0["allow_projects"] != nil || $0["allow_chats"] != nil
        }
    }

    private func detail(_ entry: String) -> [String: Any]? {
        get("/api/v1/kb/" + entry)["detail"] as? [String: Any]
    }

    private func members(_ list: String, on entry: String) -> [String] {
        let meta = detail(entry)?["meta"] as? [String: Any]
        return meta?[list] as? [String] ?? []
    }

    private func requireLink(_ id: String, in list: String, on entry: String) {
        XCTAssertTrue(members(list, on: entry).contains(id), "Needs a fresh mock with MOCK_KB_CORRECTIONS=1 for \(entry)")
    }

    private func assertRemoved(_ id: String, from list: String, on entry: String, locked: Bool) {
        assertRelationship(id, in: list, on: entry, removed: true, locked: locked)
    }

    private func assertAllowed(_ id: String, from list: String, on entry: String, locked: Bool) {
        assertRelationship(id, in: list, on: entry, removed: false, locked: locked)
    }

    private func assertRelationship(_ id: String, in list: String, on entry: String, removed: Bool, locked: Bool) {
        let response = detail(entry)
        let meta = response?["meta"] as? [String: Any]
        XCTAssertNotNil(meta, "Missing readback for \(entry)")
        let exclusions = meta?["excluded"] as? [String: [String]]
        let removedEntries = response?["removed"] as? [[String: Any]] ?? []
        XCTAssertFalse((meta?[list] as? [String] ?? []).contains(id))
        XCTAssertEqual((exclusions?[list] ?? []).contains(id), removed)
        XCTAssertEqual(removedEntries.contains { $0["kind"] as? String == list && $0["id"] as? String == id }, removed)
        XCTAssertEqual((meta?["locked_fields"] as? [String] ?? []).contains(list), locked)
    }

    private func pinnedSent() -> [String] {
        (get("/mock/kb_updates")["updates"] as? [[String: Any]] ?? []).filter { $0["id"] as? String == "lilei" }.compactMap { $0["pinned"] as? String }
    }

    private func get(_ path: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + path)!) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: data) as? [String: Any]) ?? [:]
    }

    private func put(_ path: String, _ body: [String: Any]) {
        var r = URLRequest(url: URL(string: hub + path)!)
        r.httpMethod = "PUT"
        r.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        r.httpBody = try? JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: "PUT \(path)")
        URLSession.shared.dataTask(with: r) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
    }

    private func wait(_ what: String, _ done: () -> Bool) {
        for _ in 0..<50 where !done() { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(done(), what)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "kb-\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("kb-\(name)-\(look).png"))
        }
    }
}
