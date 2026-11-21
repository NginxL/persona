import XCTest

/// The assistant's face asleep (offline) where it sits in the top bar (#11283), for screenshots in light and dark:
/// the face held offline (`-showPersonaState offline`), the sidebar column folded where there is one so the bar shows
/// the face. Against scripts/mock/mock_hub.py (TEST_RUNNER_PERSONA_MOCK_HUB); TEST_RUNNER_PERSONA_SHOTS=<dir> saves
/// `offline-face-<device>-<appearance>.png` (TEST_RUNNER_PERSONA_SHOTS_DEVICE, …_APPEARANCE). On the open iPhone Duo,
/// whose screenshots come out black from here, TEST_RUNNER_PERSONA_SHOTS_EXTERNAL=1 has the host take it: the test
/// names the file in `<dir>/want` and waits for it.
final class OfflineFaceShotsTests: XCTestCase {
    @MainActor func testOfflineFaceInTheTopBar() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-showPersonaState", "offline"]
        app.launchPinned()
        acceptIconAlert()
        let face = app.buttons["assistant-sidebar-button"].firstMatch
        let fold = app.descendants(matching: .any)["sidebar-fold-column"].firstMatch
        let ready = NSPredicate { _, _ in face.exists || fold.exists }
        XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: ready, object: nil)], timeout: 30), .completed,
                       "neither the bar's face nor the column's fold button:\n\(app.debugDescription)")
        if !face.isHittable, fold.exists, fold.isHittable {
            fold.tap()
            XCTAssertTrue(face.waitForExistence(timeout: 10), "folding the column brought no face to the bar:\n\(app.debugDescription)")
        }
        XCTAssertTrue(face.waitForExistence(timeout: 10))
        sleep(2) // the column's fold animation settles
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let file = "offline-face-\(env["PERSONA_SHOTS_DEVICE"] ?? "device")-\(env["PERSONA_SHOTS_APPEARANCE"] ?? "system").png"
        let folder = URL(fileURLWithPath: dir)
        guard env["PERSONA_SHOTS_EXTERNAL"] != nil else {
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: folder.appendingPathComponent(file))
            return
        }
        try? file.write(to: folder.appendingPathComponent("want"), atomically: true, encoding: .utf8)
        let target = folder.appendingPathComponent(file)
        let deadline = Date.now.addingTimeInterval(15)
        while Date.now < deadline, !FileManager.default.fileExists(atPath: target.path) { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(FileManager.default.fileExists(atPath: target.path), "the host took no screenshot")
    }
}
