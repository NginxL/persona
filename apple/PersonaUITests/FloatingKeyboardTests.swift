import XCTest

/// The iPad's floating keyboard (#2565) floats over the page, which keeps its height: the composer stays where it is
/// with the keyboard down, whether the keyboard is undocked while the composer has it or the composer is focused while
/// it floats. Docked, the composer stands on the keyboard as before. An Agents chat and Persona's new conversation (the
/// two kinds of page with a composer at the foot), against the isolated composer mock (`scripts/mock/composer_hub.py`).
/// iPad only: an iPhone's keyboard does not float. The simulator floats its keyboard on a pinch and docks it again when
/// its grabber is dragged to the bottom of the screen; the keyboard stays floating for whatever runs next, so the test
/// docks it again however it ends.
final class FloatingKeyboardTests: XCTestCase {
    private var app: XCUIApplication!
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs the isolated composer mock hub (PERSONA_MOCK_HUB)")
        }
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "floating keyboard tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        super.tearDown()
    }

    @MainActor private func open(_ route: [String]) throws {
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("only an iPad's keyboard floats") }
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        // Pair separately: a first pairing resets the route arguments, and must never point at a real hub.
        app.launchArguments = ["-pairURL", pair]
        app.launchPinned()
        XCTAssertTrue(app.buttons["Persona"].waitForExistence(timeout: 15) || field.waitForExistence(timeout: 15))
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-holdChromeOpen", "YES", "-sideBars", "NO", "-dev.openChat", ""] + route
        app.launchPinned()
        // However the test ends, the keyboard docks again: it would stay floating for whatever runs next.
        addTeardownBlock { @MainActor [self] in dock() }
        XCTAssertTrue(field.waitForExistence(timeout: 20), "the page did not open")
        sleep(2)
    }

    @MainActor private var field: XCUIElement { app.descendants(matching: .any)["shell-composer-field"].firstMatch }
    @MainActor private var add: XCUIElement { app.buttons["shell-composer-add"].firstMatch }
    @MainActor private var keyboard: XCUIElement { app.keyboards.firstMatch }
    @MainActor private var window: CGRect { app.windows.firstMatch.frame }

    /// The keyboard is up and narrower than the window: it floats.
    @MainActor private var floating: Bool { keyboard.exists && keyboard.frame.width < window.width - 1 }

    @MainActor private func focus() {
        for _ in 0..<3 where !keyboard.exists {
            field.tap()
            _ = keyboard.waitForExistence(timeout: 5)
        }
        XCTAssertTrue(keyboard.exists, "no software keyboard")
        sleep(1)
    }

    @MainActor private func float() {
        keyboard.pinch(withScale: 0.3, velocity: -3)
        for _ in 0..<5 where !floating { sleep(1) }
        XCTAssertTrue(floating, "the pinch did not float the keyboard")
        sleep(1)
    }

    /// Drags the floating keyboard by its grabber, just under its keys, to the bottom of the screen.
    @MainActor private func dock() {
        for below in [18.0, 30.0, 8.0, 45.0] where floating {
            let keys = keyboard.frame, origin = app.coordinate(withNormalizedOffset: .zero)
            origin.withOffset(CGVector(dx: keys.midX, dy: keys.maxY + below))
                .press(forDuration: 0.4, thenDragTo: origin.withOffset(CGVector(dx: window.midX, dy: window.maxY - 2)),
                       withVelocity: .slow, thenHoldForDuration: 0.3)
            sleep(2)
        }
    }

    @MainActor private func shot(_ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    /// Docked, then undocked while focused, then focused again while it floats, then docked again.
    @MainActor private func check(_ page: String) {
        focus()
        if floating { dock() } // a run that stopped half way left it floating
        XCTAssertFalse(floating, "\(page): the keyboard did not dock")
        // Where the composer's foot is with the keyboard down: its card opened (focused) with nothing in its way, as the
        // floating keyboard leaves it. Docked, it stands on the keyboard.
        XCTAssertLessThanOrEqual(add.frame.maxY, keyboard.frame.minY + 1, "\(page): the composer is under the docked keyboard")
        shot("\(page) docked")
        let docked = add.frame.maxY

        float()
        shot("\(page) undocked while focused")
        let undocked = add.frame.maxY
        XCTAssertGreaterThan(undocked, keyboard.frame.maxY, "\(page): the composer did not go back down under the floating keyboard")
        XCTAssertGreaterThan(undocked, window.maxY - 80, "\(page): the composer is \(window.maxY - undocked) pt off the bottom")

        // Put away with the composer's Hide Keyboard (clear of the floating keyboard now), and focused again while it floats.
        let hide = app.buttons["shell-composer-dismiss-keyboard"].firstMatch
        XCTAssertTrue(hide.exists && hide.isHittable, "\(page): Hide Keyboard is under the floating keyboard")
        hide.tap()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5), "\(page): the keyboard did not go down")
        focus()
        XCTAssertTrue(floating, "\(page): the keyboard came back docked")
        shot("\(page) focused while floating")
        XCTAssertEqual(add.frame.maxY, undocked, accuracy: 1, "\(page): focused while it floats, the composer stood elsewhere")

        dock()
        XCTAssertFalse(floating, "\(page): the keyboard did not dock again")
        XCTAssertEqual(add.frame.maxY, docked, accuracy: 1, "\(page): docked again, the composer is not on the keyboard")
    }

    @MainActor func testAgentsChat() throws {
        try open(["-app.tab", "chats", "-tab", "dev", "-openSession", agent])
        check("agents chat")
    }

    @MainActor func testPersonasNewConversation() throws {
        try open(["-app.tab", "assistant", "-tab", "assistant", "-newAssistant", "YES"])
        check("new conversation")
    }
}
