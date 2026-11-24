import XCTest

/// The inbox, Feishu chats and topic groups, the Feishu messages page and mail (#5730).
/// Mock (`scripts/app/perf-test.sh`, suite `inbox`): MOCK_LARK=1 MOCK_LARK_RICH=1 MOCK_LARK_TOPIC=1 MOCK_INBOX_EXTRA=150
/// MOCK_MAIL=1 MOCK_AGENTS=1 MOCK_MANAGERS=0 (one assistant, so 「周末徒步群」 is in the list; on the default roster it
/// belongs to 「工作」).
final class PerfInboxTests: PerfTestCase {
    private let lark = ["-demo.on", "NO", "-integrations.lark.enabled", "YES"]

    @MainActor private func openInbox(_ extra: [String] = []) {
        // the whole stacked page, as measured before the section bar (#9053), the group chat (FYI) in it
        launch(["-dev.openChat", "", "-tab", "mail", "-inbox.kind", "", "-inbox.source", "", "-showInboxSection", "all"] + lark + extra)
        sleep(2)
    }

    @MainActor private func openItem(_ id: String) {
        launch(["-dev.openChat", "", "-tab", "mail", "-inbox.kind", "", "-inbox.source", "", "-openInboxItem", id] + lark)
        expandSource()
    }

    @MainActor private func expandSource() {
        see(app.buttons["inbox.detail.source"].firstMatch, "the original source control", timeout: 30).tapCenter()
    }

    /// The list is lazy: rows below Needs You's waiting Agents are not in the tree until they come on screen.
    @MainActor private func inboxRow(_ title: String, _ what: String) -> XCUIElement {
        see(app.buttons["inbox.more"], "the inbox toolbar", timeout: 20)
        let row = text(title)
        for _ in 0..<16 where !row.waitForExistence(timeout: 1) {
            app.swipeUp()
        }
        return see(row, what, timeout: 5)
    }

    // MARK: Inbox

    /// ~160 items (Feishu and mail merged, sections) scrolled.
    @MainActor func testInboxScroll() throws {
        openInbox()
        see(app.buttons["inbox.more"], "the inbox toolbar", timeout: 20)
        XCTAssertGreaterThan(app.collectionViews.firstMatch.cells.count, 0, "the inbox has no loaded rows")
        measureVerifiedGlide()
    }

    /// The inbox at rest while Feishu pushes new messages and inbox changes.
    @MainActor func testInboxIdleUnderPushes() throws {
        openInbox()
        see(app.buttons["inbox.more"], "the inbox toolbar", timeout: 20)
        measureIdle(push: "lark_churn?feed=f3&n=8&ms=500&new=1&inbox=1")
    }

    /// Typing into the inbox's search (the list filtered per keystroke).
    @MainActor func testInboxSearchTyping() throws {
        openInbox()
        see(app.buttons["inbox.more"], "the inbox toolbar", timeout: 20)
        let search = app.searchFields.firstMatch
        if !search.waitForExistence(timeout: 3) { mainScroller().swipeDown() }
        measureTyping(see(search, "the inbox's search"), text: "周末徒步群的照片")
    }

    /// Inbox → the Feishu group chat → back.
    @MainActor func testOpenFeishuChatFromInbox() throws {
        openInbox(["-inbox.kind", "group"])
        let row = inboxRow("周末徒步群", "the group's row")
        measureTransition {
            row.tap()
            expandSource()
            wait(app.textFields["lark.box"], timeout: 15)
            sleep(2)
            back()
            wait(row)
            sleep(1)
        }
    }

    // MARK: Feishu chat

    /// The group's history (cards, images, rich posts every third message) scrolled.
    @MainActor func testFeishuChatScroll() throws {
        openItem("lark:f3:300")
        see(app.textFields["lark.box"], "the Feishu chat", timeout: 30)
        sleep(2)
        measureScroll(mainScroller(), first: .down)
    }

    /// The chat at rest while new messages arrive (one each 500 ms).
    @MainActor func testFeishuChatIdleUnderPushes() throws {
        openItem("lark:f3:300")
        see(app.textFields["lark.box"], "the Feishu chat", timeout: 30)
        measureIdle(push: "lark_churn?feed=f3&n=8&ms=500&new=1")
    }

    /// Typing into the Feishu composer over the chat.
    @MainActor func testFeishuChatTyping() throws {
        openItem("lark:f3:300")
        measureTyping(see(app.textFields["lark.box"], "the Feishu composer", timeout: 30), text: "周六早上八点在地铁站集合，记得带水")
    }

    /// A picked photo's thumbnail beside the composer, redrawn with every typed character.
    @MainActor func testLarkAttachmentTyping() throws {
        launch(["-dev.openChat", "", "-tab", "mail", "-inbox.kind", "", "-inbox.source", "",
                "-openInboxItem", "lark:f2:200", "-showLarkAttachmentDraft", "photo", "-showLarkSendFresh", "YES"] + lark)
        expandSource()
        see(app.buttons["移除 照片.jpg"], "the picked photo", timeout: 30)
        measureTyping(see(app.textFields["lark.box"], "the Feishu composer", timeout: 30), text: "周六早上八点在地铁站集合，记得带水")
    }

    /// A topic group's cards (a non-lazy stack) scrolled.
    @MainActor func testFeishuTopicGroupScroll() throws {
        openItem("lark:f4:405")
        see(any("lark.topic"), "the topic cards", timeout: 30)
        sleep(2)
        measureScroll(mainScroller(), first: .down)
    }

    /// A topic's replies (the thread sheet) presented and dismissed.
    @MainActor func testFeishuTopicThreadSheet() throws {
        openItem("lark:f4:405")
        let replies = see(app.buttons["lark.topic.replies"].firstMatch, "a topic's replies", timeout: 30)
        measureTransition(navigation: false) {
            replies.tap()
            let box = app.textFields["lark.thread.box"]
            wait(box)
            sleep(1)
            let w = app.windows.firstMatch
            w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.12))
                .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.95)))
            wait(box, shown: false)
            sleep(1)
        }
    }

    // MARK: Feishu messages page

    @MainActor private func openMessages() -> XCUIElement {
        launch(["-dev.openChat", "", "-tab", "feishu", "-app.tab", "feishu", "-larkTop", "NO"] + lark)
        let list = app.buttons["feishu-chat-f3"]
        if list.waitForExistence(timeout: 5) { return list }
        let entry = app.buttons["shell-feishu"].firstMatch
        if entry.waitForExistence(timeout: 20) {
            entry.tapCenter()
        } else if app.buttons["sidebar-messages"].firstMatch.waitForExistence(timeout: 5) {
            app.buttons["sidebar-messages"].firstMatch.tapCenter()
        }
        return see(app.buttons["feishu-chat-f3"], "the conversation list", timeout: 20)
    }

    /// The conversation list scrolled (pulling at the top expands the pinned shelf).
    @MainActor func testFeishuMessagesListScroll() throws {
        _ = openMessages()
        sleep(2)
        measureScroll(mainScroller(), flicks: 2)
    }

    /// The messages page → a chat → back.
    @MainActor func testFeishuMessagesOpenChat() throws {
        let row = openMessages()
        sleep(2)
        measureTransition {
            row.tapCenter()
            wait(app.textFields["lark.box"], timeout: 15)
            sleep(2)
            back()
            wait(row)
            sleep(1)
        }
    }

    // MARK: Mail

    /// A mail thread (HTML in a web view, a two-mail thread) scrolled.
    @MainActor func testMailDetailScroll() throws {
        openItem("mail:acc1:t1:501")
        see(text("下周的分享"), "the mail", timeout: 30)
        sleep(3)
        measureScroll(mainScroller(), flicks: 2)
    }

    /// Inbox → a mail → back (a new WKWebView each time).
    @MainActor func testOpenMailFromInbox() throws {
        openInbox(["-inbox.kind", "mail"])
        let row = inboxRow("下周的分享", "the mail's row")
        measureTransition {
            row.tap()
            expandSource()
            sleep(3)
            back()
            wait(row)
            sleep(1)
        }
    }
}
