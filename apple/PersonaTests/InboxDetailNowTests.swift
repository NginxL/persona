import Foundation
import Testing
@testable import Persona

/// The second detail batch: the shared header line, now-only event pages and the one status-symbol mapping used by row and detail.
@MainActor
struct InboxDetailNowTests {
    private func item(now: String? = nil) -> InboxItem {
        var item = InboxItem(id: "matter", source: "chat", kind: "group", account: "chat", accountName: "Team chat",
                             title: "A matter", preview: "Latest line", count: 1)
        item.now = now
        return item
    }

    @Test func statusSymbolsFollowTheSharedRule() {
        #expect(InboxNow.symbol(for: item(now: "waiting")) == .time)
        for key in ["queued", "looking", "agent_working"] {
            #expect(InboxNow.symbol(for: item(now: key)) == .spinner, "\(key)")
        }
        for key in ["confirm_reply", "confirm_agent", "decide", "follow_up_ask", "check_due", "needs_you"] {
            #expect(InboxNow.symbol(for: item(now: key)) == .needsYou, "\(key)")
        }
        #expect(InboxNow.symbol(for: item(now: "failed")) == .failed)
        for key in ["fyi", "folded", "done_replied", "done_manager", "done_user", "done_read", "done_dismissed",
                    "done_muted", "done_superseded", "a_future_state"] {
            #expect(InboxNow.symbol(for: item(now: key)) == nil, "\(key)")
        }
        #expect(InboxNow.symbol(for: item()) == nil)
    }

    @Test func aSharedStateKeyAloneMakesAnEvent() {
        var it = item(now: "done_replied")
        #expect(it.isEvent)
        #expect(InboxNow.text(for: it) == "你回过了")
        it.now = nil
        #expect(!it.isEvent)
    }

    @Test func detailHeaderUsesSharedMetaAndKeepsTheOldFallback() {
        var it = item()
        it.where = "Launch group"
        it.sender = "Alex"
        let expected = InboxNow.meta(it, lang: interfaceTextLocale.identifier)
        #expect(expected == "Launch group · Alex：Latest line")
        #expect(InboxEventPage.from(it) == expected)

        it.where = nil
        #expect(InboxNow.meta(it, lang: interfaceTextLocale.identifier) == nil)
        #expect(InboxEventPage.from(it) == "Team chat · A matter")

        it.where = ""
        #expect(InboxEventPage.from(it) == "Team chat · A matter")
    }

    @Test func mailNamesItsSourceFolderInTheRowAndDetail() throws {
        var it = item()
        it.source = "mail"
        it.kind = "mail"
        it.sender = "Alex"
        it.mail = .init(accountID: "demo", mailbox: "Projects/通知", uids: [7])
        #expect(InboxNow.meta(it, lang: "en") == "Projects/通知 · Alex: Latest line")
        #expect(InboxNow.meta(it, lang: "en", translatedPreview: "Translated") == "Projects/通知 · Alex: Translated")
        #expect(InboxEventPage.from(it).contains("Projects/通知"))
        it.mail?.mailbox = " INBOX "
        #expect(InboxNow.meta(it, lang: "en") == " INBOX  · Alex: Latest line")
        it.mail = try JSONDecoder().decode(InboxItem.Mail.self, from: Data(#"{"account_id":"demo","uids":[7]}"#.utf8))
        #expect(InboxNow.meta(it, lang: "en") == nil)
        for folder in ["INBOX", "InBoX", ""] {
            it.mail?.mailbox = folder
            #expect(InboxNow.meta(it, lang: "en") == nil)
        }
    }
}
