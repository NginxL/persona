import XCTest

/// The composer's + (#3225), in a conversation and in a new one (#3805). On the iPhone a tap opens the photo / file panel
/// and touch and hold opens a menu of what the panel's More tab lists, in the same order: Voice Chat, Take Photo, Scan
/// Document (#3720), (Persona's main conversation) Meeting Recording (#3723), Mention a File or Session, an Agent's Quick
/// Commands in a submenu, then extension prompts. A new conversation has the same, less what needs a conversation (Voice
/// Chat), and so does Health's (#3825). The iPad's + is that menu, with Photos and Files first. Against
/// scripts/mock/mock_hub.py (PERSONA_MOCK_HUB); `-showCameraRows YES` lists Take Photo and Scan Document, since the
/// simulator can't take or scan.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> keeps screenshots, in light and dark (`-ui.scheme`) and in the BeautifulUI style too.
/// Voice Chat is in the menu on the iPad too, as it was before (`VoicePlatform.phone`: iPhone, iPad and Vision Pro, #4896).
final class AttachMenuTests: XCTestCase {
    private var hub = ""
    private static let tab = ["-tab", "assistant", "-app.tab", "assistant"]
    /// The main conversation, named: the Persona tab otherwise opens on whatever the last launch left (a new one).
    private static let persona = tab + ["-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]
    private static let new = tab + ["-newAssistant", "YES"]
    /// Health's new conversation page (#3825).
    private static let health = tab + ["-openShellPage", "health"]
    private static let agent = ["-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000002"] // weather › tests: working, Claude in herdr
    private var isPad: Bool { UIDevice.current.userInterfaceIdiom == .pad }
    private var shots: Bool { ProcessInfo.processInfo.environment["PERSONA_SHOTS"] != nil }
    /// Both looks when screenshots are kept; light alone otherwise.
    private var schemes: [String] { shots ? ["light", "dark"] : ["light"] }
    /// The default style and BeautifulUI's when screenshots are kept (its components, the same composer in the shell);
    /// otherwise the run's (`UITestStyle`).
    private var styles: [String] { shots ? ["glass", "beautiful"] : [UITestStyle.name] }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        hub = h
    }

    /// Persona's conversation: the camera and Mention, no Quick Commands, no voice row (#9887: the composer's phone icon is
    /// the one voice entry); Mention types "@" and opens the list.
    @MainActor func testPersonaMenu() {
        for style in styles {
            for scheme in schemes {
                let app = launch(Self.persona, scheme: scheme, style: style)
                openMenu(app)
                for id in ["composer-menu-camera", "composer-menu-scan", "composer-menu-mention", "composer-menu-meeting"] {
                    XCTAssertTrue(item(app, id).waitForExistence(timeout: 5), "no \(id) in the menu:\n\(app.debugDescription)")
                }
                XCTAssertFalse(item(app, "composer-menu-voice").exists, "Voice Chat in the menu")
                XCTAssertFalse(commands(app).exists, "Quick Commands in Persona's conversation")
                XCTAssertTrue(addContext(app).exists, "no Add as Context in Persona's menu:\n\(app.debugDescription)")
                photosAndFiles(app)
                shot("attach-menu-persona", scheme, style)
                mention(app)
                app.terminate()
            }
        }
    }

    /// A new conversation (#3805): a conversation's menu, less Voice Chat; Mention types "@" and opens the list.
    @MainActor func testNewConversationMenu() {
        for style in styles {
            for scheme in schemes {
                let app = launch(Self.new, scheme: scheme, style: style)
                openMenu(app)
                for id in ["composer-menu-camera", "composer-menu-scan", "composer-menu-mention"] {
                    XCTAssertTrue(item(app, id).waitForExistence(timeout: 5), "no \(id) in the menu:\n\(app.debugDescription)")
                }
                XCTAssertFalse(item(app, "composer-menu-voice").exists, "Voice Chat before the conversation exists")
                XCTAssertFalse(item(app, "composer-menu-meeting").exists, "Meeting Recording outside the main conversation")
                XCTAssertFalse(commands(app).exists, "Quick Commands in a new conversation")
                photosAndFiles(app)
                shot("attach-menu-new", scheme, style)
                mention(app)
                app.terminate()
            }
        }
    }

    /// Health's new conversation (#3825): Persona's new conversation's composer and menu, less Voice Chat.
    @MainActor func testHealthMenu() {
        for style in styles {
            for scheme in schemes {
                let app = launch(Self.health, scheme: scheme, style: style)
                openMenu(app)
                for id in ["composer-menu-camera", "composer-menu-scan", "composer-menu-mention"] {
                    XCTAssertTrue(item(app, id).waitForExistence(timeout: 5), "no \(id) in Health's menu:\n\(app.debugDescription)")
                }
                XCTAssertFalse(item(app, "composer-menu-voice").exists, "Voice Chat before the health conversation exists")
                XCTAssertFalse(item(app, "composer-menu-meeting").exists, "Meeting Recording outside the main conversation")
                XCTAssertFalse(commands(app).exists, "Quick Commands in Health's new conversation")
                photosAndFiles(app)
                shot("attach-menu-health", scheme, style)
                app.terminate()
            }
        }
    }

    /// Health's model button (#3825): Persona's new conversation's menu, the effort and, a level down, the permission mode.
    /// As there, the button is in the box's open row: a short draft opens it, with the keyboard put away so the menu has room.
    @MainActor func testHealthModelMenu() {
        for style in styles {
            for scheme in schemes {
                let app = launch(Self.health, scheme: scheme, style: style)
                let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
                XCTAssertTrue(field.waitForExistence(timeout: 40), "no composer on Health's page:\n\(app.debugDescription)")
                sleep(2) // the page settles
                for _ in 0..<3 where !app.keyboards.firstMatch.exists {
                    field.tap()
                    _ = app.keyboards.firstMatch.waitForExistence(timeout: 4)
                }
                field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 12) + "ok")
                let hide = app.buttons["shell-composer-dismiss-keyboard"].firstMatch
                if hide.waitForExistence(timeout: 5) { hide.tap() }
                _ = app.keyboards.firstMatch.waitForNonExistence(timeout: 5)
                let modes = app.buttons["shell-composer-modes"].firstMatch
                XCTAssertTrue(modes.waitForExistence(timeout: 10), "no model button on Health's page:\n\(app.debugDescription)")
                modes.tap()
                // The mock hub's launchers, an effort level (its detail may follow in the label, as in `MenuSectionTitleTests`),
                // and the permission mode's submenu.
                for (what, format, title) in [("the launchers", "label BEGINSWITH %@", "claude"),
                                              ("the effort", "label MATCHES %@", "^中([,，\\n][\\s\\S]*)?$"),
                                              ("the permission mode", "label BEGINSWITH %@", "权限模式")] {
                    let row = app.descendants(matching: .any).matching(NSPredicate(format: format, title)).firstMatch
                    XCTAssertTrue(row.waitForExistence(timeout: 5), "no \(what) in Health's model menu:\n\(app.debugDescription)")
                }
                shot("health-model-menu", scheme, style)
                // The page keeps its draft: it goes again, or the next screenshots show it.
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap()
                for _ in 0..<3 where !app.keyboards.firstMatch.exists {
                    field.tap()
                    _ = app.keyboards.firstMatch.waitForExistence(timeout: 4)
                }
                field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 12))
                sleep(2) // drafts are saved a moment later
                app.terminate()
            }
        }
    }

    /// An Agent's conversation: Quick Commands fold into a submenu, with /clear dimmed while the Agent works.
    @MainActor func testAgentMenu() {
        for scheme in schemes {
            let app = launch(Self.agent, scheme: scheme)
            openMenu(app)
            let quick = commands(app)
            XCTAssertTrue(quick.waitForExistence(timeout: 5), "no Quick Commands:\n\(app.debugDescription)")
            XCTAssertTrue(item(app, "composer-menu-mention").exists, "no Mention:\n\(app.debugDescription)")
            XCTAssertTrue(item(app, "composer-menu-scan").exists, "no Scan Document:\n\(app.debugDescription)")
            XCTAssertTrue(addContext(app).exists, "no Add as Context in an Agent's menu:\n\(app.debugDescription)")
            XCTAssertFalse(item(app, "composer-menu-voice").exists, "Voice Chat in an Agent's menu: the phone icon is the one entry")
            XCTAssertFalse(item(app, "composer-menu-meeting").exists, "Meeting Recording in an Agent's menu")
            XCTAssertFalse(item(app, "attach-clear").exists, "the commands are not folded")
            shot("attach-menu-agent", scheme)
            quick.tap()
            let clear = item(app, "attach-clear")
            XCTAssertTrue(clear.waitForExistence(timeout: 5), "no /clear in Quick Commands:\n\(app.debugDescription)")
            XCTAssertFalse(clear.isEnabled, "/clear while the Agent works")
            XCTAssertTrue(item(app, "attach-command-汇报进展").exists, "no Report Progress")
            shot("attach-menu-agent-commands", scheme)
            app.terminate()
        }
    }

    /// The iPhone's tap is unchanged in a conversation and now the same in a new one: the panel, whose More tab lists the
    /// menu's rows (an Agent's commands under them).
    @MainActor func testATapOpensThePanel() throws {
        guard !isPad else { throw XCTSkip("the iPad's + is the menu") }
        var pages = [("persona", Self.persona, "glass"), ("agent", Self.agent, "glass"), ("new", Self.new, "glass"), ("health", Self.health, "glass")]
        if shots { pages += [("persona", Self.persona, "beautiful"), ("new", Self.new, "beautiful"), ("health", Self.health, "beautiful")] }
        for scheme in schemes {
            for (name, args, style) in pages {
                let app = launch(args, scheme: scheme, style: style)
                let plus = plusButton(app)
                sleep(2)
                plus.tap()
                let more = app.buttons["attach-tab-more"]
                XCTAssertTrue(more.waitForExistence(timeout: 5), "a tap did not open the panel (\(name)):\n\(app.debugDescription)")
                more.tap()
                for id in ["composer-menu-camera", "composer-menu-scan", "composer-menu-mention"] {
                    XCTAssertTrue(app.buttons[id].waitForExistence(timeout: 5), "no \(id) in More (\(name)):\n\(app.debugDescription)")
                }
                XCTAssertFalse(app.buttons["composer-menu-voice"].exists || app.buttons["composer-menu-call"].exists, "a voice row in More (\(name))")
                XCTAssertEqual(app.buttons["composer-menu-meeting"].exists, name == "persona", "Meeting Recording in More (\(name))")
                // The list draws what is on screen: the first command, not /clear at the bottom. An Agent's list is
                // longer than the resting sheet: tapping the tab shown opens it all the way.
                let command = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'attach-command-'")).firstMatch
                if name == "agent", !command.waitForExistence(timeout: 2) {
                    more.tap()
                    _ = command.waitForExistence(timeout: 5)
                }
                XCTAssertEqual(command.exists, name == "agent", "the commands in More (\(name))")
                shot("attach-panel-more-\(name)", scheme, style)
                app.terminate()
            }
        }
    }

    /// Add as Context › To-do… (#3721): the picker lists the hub's to-dos; a pick goes over the box as a quote.
    @MainActor func testAddAsContextPutsAToDoOverTheBox() throws {
        guard !isPad else { throw XCTSkip("the iPhone's panel; the iPad's menu lists the same rows") }
        for scheme in schemes {
            let app = launch(Self.persona, scheme: scheme)
            let plus = plusButton(app)
            sleep(2)
            plus.tap()
            let more = app.buttons["attach-tab-more"]
            XCTAssertTrue(more.waitForExistence(timeout: 5), "no panel:\n\(app.debugDescription)")
            more.tap()
            let todo = app.buttons["attach-context-todo"]
            // The sheet opens at half height (#4312): the row may be further down the list.
            if !todo.waitForExistence(timeout: 2) { app.descendants(matching: .any)["attach-sheet"].firstMatch.swipeUp() }
            XCTAssertTrue(todo.waitForExistence(timeout: 5), "no To-do… in More:\n\(app.debugDescription)")
            shot("attach-panel-more-context", scheme)
            todo.tap()
            let row = app.buttons["context-pick-row"].firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 10), "the picker lists no to-dos:\n\(app.debugDescription)")
            shot("context-picker-todo", scheme)
            row.tap()
            let quote = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "待办 · ")).firstMatch
            XCTAssertTrue(quote.waitForExistence(timeout: 10), "the pick is not over the box:\n\(app.debugDescription)")
            XCTAssertFalse(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "引用选中的文字")).firstMatch.exists,
                           "a to-do is quoted as a whole, not as selected text")
            shot("context-picked-todo", scheme)
            // The conversation keeps its quotes: the pick goes again, or the next tests' screenshots show it.
            app.buttons["取消引用"].firstMatch.tap()
            sleep(2) // drafts are saved a moment later
            app.terminate()
        }
    }

    /// The largest accessibility text size: the menu and the Quick Commands submenu still read (screenshots only). The
    /// menu scrolls at this size, so Mention and Quick Commands, the rows furthest from the +, are scrolled to (#6112).
    @MainActor func testTheMenuAtTheLargestTextSize() throws {
        guard shots else { throw XCTSkip("screenshots only") }
        let largest = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        let app = launch(Self.persona + largest, scheme: "light")
        openMenu(app)
        XCTAssertTrue(item(app, "composer-menu-camera").waitForExistence(timeout: 5), "no menu:\n\(app.debugDescription)")
        shot("attach-menu-persona-largest-text", "light")
        XCTAssertTrue(scrolledTo(app, item(app, "composer-menu-mention")), "Mention can't be scrolled to:\n\(app.debugDescription)")
        shot("attach-menu-persona-largest-text-top", "light")
        app.terminate()
        let agent = launch(Self.agent + largest, scheme: "light")
        openMenu(agent)
        XCTAssertTrue(item(agent, "composer-menu-camera").waitForExistence(timeout: 5), "no menu:\n\(agent.debugDescription)")
        let quick = commands(agent)
        XCTAssertTrue(scrolledTo(agent, quick), "no Quick Commands:\n\(agent.debugDescription)")
        quick.tap()
        shot("attach-menu-agent-commands-largest-text", "light")
        agent.terminate()
    }

    /// Photos and Files are the panel's on the iPhone, the menu's first on the iPad.
    @MainActor private func photosAndFiles(_ app: XCUIApplication) {
        if isPad {
            XCTAssertTrue(app.buttons["照片"].exists && app.buttons["文件"].exists, "the iPad's menu lost Photos or Files")
        } else {
            XCTAssertFalse(app.buttons["照片"].exists, "Photos in the touch-and-hold menu: the panel's job")
        }
    }

    /// The menu's Mention: "@" typed at the caret and the @ list open.
    @MainActor private func mention(_ app: XCUIApplication) {
        item(app, "composer-menu-mention").tap()
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(app.descendants(matching: .any)["mention-menu"].firstMatch.waitForExistence(timeout: 5),
                      "Mention did not open the @ list:\n\(app.debugDescription)")
        XCTAssertTrue((field.value as? String)?.hasSuffix("@") == true, "Mention did not type @: \(String(describing: field.value))")
        // The conversation keeps its draft: the "@" goes again, or the next screenshots show it. The app saves a draft
        // a second after its last change (`AppModel.drafts`), so the empty one is saved before the app is ended (#6112).
        let typed = (field.value as? String)?.count ?? 0
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: typed))
        XCTAssertFalse((field.value as? String ?? "").contains("@"), "the @ stayed: \(String(describing: field.value))")
        sleep(2)
    }

    /// Swipes the menu down until `row` is on screen. At the largest text sizes the menu scrolls: it opens upward with
    /// its first row next to the +, and a row it hasn't scrolled to is not in the accessibility tree (#6112).
    @MainActor private func scrolledTo(_ app: XCUIApplication, _ row: XCUIElement) -> Bool {
        let rows = ["语音对话", "拍照", "扫描文稿", "开始会议", "提及文件或会话", "作为上下文", "快捷指令"]
        let menu = app.collectionViews.containing(NSPredicate(format: "label IN %@", rows)).firstMatch
        for _ in 0..<5 where !(row.exists && row.isHittable) {
            menu.swipeDown()
            sleep(1)
        }
        return row.exists && row.isHittable
    }

    @MainActor private func plusButton(_ app: XCUIApplication) -> XCUIElement {
        let plus = app.descendants(matching: .any).matching(identifier: "shell-composer-add").firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 40), "no + button:\n\(app.debugDescription)")
        return plus
    }

    /// The iPad's + opens its menu on a tap, the iPhone's on touch and hold.
    @MainActor private func openMenu(_ app: XCUIApplication) {
        let plus = plusButton(app)
        sleep(2) // the page settles
        if isPad { plus.tap() } else { plus.press(forDuration: 1.0) }
    }

    /// A menu row by its identifier, or by its title where UIKit's menu drops the identifier (zh-Hans, as the tests run).
    /// The meeting row says Record, as the drawer's row does since #5243 (#6067).
    @MainActor private func item(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        let titles = ["composer-menu-voice": "语音对话", "composer-menu-camera": "拍照", "composer-menu-scan": "扫描文稿",
                      "composer-menu-mention": "提及文件或会话", "composer-menu-meeting": "开始会议", "attach-clear": "清空上下文"]
        let title = titles[id] ?? (id.hasPrefix("attach-command-") ? String(id.dropFirst("attach-command-".count)) : id)
        return app.descendants(matching: .any).matching(NSPredicate(format: "identifier == %@ OR label BEGINSWITH %@", id, title)).firstMatch
    }

    /// The Add as Context submenu (#3721), by its title.
    @MainActor private func addContext(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "作为上下文")).firstMatch
    }

    /// The Quick Commands submenu: by its title too, as a submenu may not carry its identifier through UIKit's menu.
    @MainActor private func commands(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "identifier == %@ OR label == %@", "attach-menu-commands", "快捷指令")).firstMatch
    }

    /// Always with a style: the app writes the style it launched with back to its settings on a fresh install. This test
    /// names its styles itself, so not `launchPinned()`: its `-ui.style` comes later and wins, and the BeautifulUI
    /// screenshots came out in Liquid Glass.
    @MainActor private func launch(_ extra: [String], scheme: String, style: String = UITestStyle.name) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-showCameraRows", "YES", "-ui.scheme", scheme, "-ui.style", style] + extra
        app.launchPersona()
        return app
    }

    /// The whole screen: a menu is drawn in a window of its own.
    @MainActor private func shot(_ name: String, _ scheme: String, _ style: String = UITestStyle.name) {
        sleep(1)
        let s = XCUIScreen.main.screenshot()
        let file = name + (isPad ? "-ipad" : "") + (style == "glass" ? "" : "-" + style) + "-" + scheme
        let a = XCTAttachment(screenshot: s)
        a.name = file
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file + ".png"))
    }
}
