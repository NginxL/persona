package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.InboxItemUiRoute
import org.junit.Assert.*
import org.junit.Test
import persona.core.inbox.*

// docs/extensions/extensions.md "UI slots" inbox_item (#1683). Native conversations retain their chat and composer;
// non-chat items keep the inbox_item extension contract.
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxItemPagesTest {
    /** The app's strings; Robolectric runs in Simplified Chinese (robolectric.properties). */
    private val res get() = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val lark = InboxRecord(InboxItem("lark:F1:1051", kind = "group", title = "周四的评审"), feedId = "F1", startPosition = 1051, source = "lark")
    private val mail = InboxRecord(InboxItem("mail-1", kind = "mail", title = "订单"), source = "mail")
    private val moment = InboxRecord(InboxItem("moment-1", kind = "moment"), source = "lark", moment = InboxMomentRef(postId = "post-1"))
    private val event = ExtensionUiPlace("larkext", "event", "事件", "inbox_item", connector = "lark")

    @Test fun aKnownConnectorSlotKeepsChatsNativeAndStillDrawsNonChatItems() {
        val pages = InboxItemPages(listOf(event))
        assertEquals(event, pages.place(lark))
        assertNull(pages.route(lark))
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1051), inboxOpenPage(lark, pages))
        // The connector remains available for non-chat items, including a 公司圈 post.
        assertEquals("moment-1", pages.route(moment)?.itemId)
        assertEquals("moment-1", pages.route(moment.copy(feedId = "F1"))?.itemId)
        // Another connector's items keep their own page.
        assertNull(pages.route(mail))
        assertEquals(MailRoute("mail-1"), inboxOpenPage(mail, pages))
    }

    @Test fun disablingNativeChatsRestoresConnectorPagesAndLateSlotReplacement() {
        val pages = InboxItemPages(listOf(event), nativeChats = false)
        val native = LarkRoute("F1", lark.item.id, 1051)
        val plugin = InboxItemUiRoute(lark.item.id, "larkext", "event", lark.item.title)
        assertEquals(plugin, pages.open(lark))
        assertEquals(plugin, pages.route(lark))
        assertEquals(listOf(native to plugin), pages.swaps(listOf(native), setOf(native), listOf(lark)))
        assertTrue(pages.copy(nativeChats = true).swaps(listOf(native), setOf(native), listOf(lark)).isEmpty())
        assertEquals(native, pages.copy(nativeChats = true).open(lark))
        assertEquals(native, InboxItemPages(nativeChats = false).open(lark))
        assertEquals(native, inboxSourcePage(lark.item.id, listOf(lark)))
        assertEquals(pages.open(moment), pages.copy(nativeChats = true).open(moment))
    }

    @Test fun withoutAPageEveryItemOpensAsBefore() {
        // An older hub lists nothing at inbox_item.
        val none = InboxItemPages()
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1051), inboxOpenPage(lark, none))
        assertEquals(MailRoute("mail-1"), inboxOpenPage(mail, none))
        assertEquals(LarkMomentRoute("post-1", "moment-1"), inboxOpenPage(moment, none))
        // Pages that cannot be for this item: another slot, another connector, no connector, no extension or id.
        val others = InboxItemPages(listOf(event.copy(placement = "nav_page"), event.copy(connector = "github"), event.copy(connector = null),
            event.copy(extension = ""), event.copy(id = "")))
        assertNull(others.route(moment))
        // An item that says nothing of its source is nobody's.
        assertNull(InboxItemPages(listOf(event.copy(connector = ""))).route(moment.copy(source = "")))
    }

    @Test fun theFirstListedPageWins() {
        val pages = InboxItemPages(listOf(event.copy(connector = "github"), event, event.copy(extension = "another", id = "thread")))
        assertEquals("larkext" to "event", pages.place(lark)?.let { it.extension to it.id })
    }

    @Test fun theOwnPageIsWhatTheListOpenedBefore() {
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1051), inboxItemPage(lark))
        assertEquals(MailRoute("mail-1"), inboxItemPage(mail))
        assertEquals(LarkMomentRoute("post-1", "moment-1"), inboxItemPage(moment))
    }

    // A hub connector's item (any connector, #2726) opens its thread; one without a thread, its detail page.
    @Test fun aConnectorsItemOpensItsThread() {
        val chat = InboxRecord(InboxItem("example:1", kind = "dm", title = "Example"), sourceLabel = "Example", source = "example",
            connector = InboxConnectorRef("wxid_example_a", label = "转账", canReply = true))
        assertTrue(chat.isConnector)
        assertEquals(ConnectorThreadRoute("example:1"), inboxItemPage(chat))
        assertEquals(InboxItemDetailRoute("example:1"), inboxItemPage(chat.copy(connector = InboxConnectorRef(""))))
        // The row's tag is the connector's own label, never one guessed from the kind.
        assertEquals("转账", kindLabel(res, chat))
        assertEquals("", kindLabel(res, chat.copy(connector = InboxConnectorRef("t1"))))
        // Feishu's and mail's items stay theirs even when the hub adds a connector block.
        assertEquals(MailRoute("mail-1"), inboxItemPage(mail.copy(connector = InboxConnectorRef("t1"))))
        assertFalse(lark.copy(connector = InboxConnectorRef("t1")).isConnector)
    }

    @Test fun anAssistantRowOpensItsOwnDetailRatherThanAnUnreadableConnectorThread() {
        val row = InboxRecord(InboxItem("assistant:1", kind = "assistant_post", title = "Morning briefing"),
            sourceLabel = "Assistant", source = "assistant", connector = InboxConnectorRef("server/session-1", label = "Assistant"))
        assertEquals(InboxItemDetailRoute("assistant:1"), inboxItemPage(row))
    }

    @Test fun anAssistantRowSkipsTheGenericEventPage() {
        val row = InboxRecord(InboxItem("assistant:1", kind = "assistant_post", title = "Morning briefing"),
            sourceLabel = "Assistant", source = "assistant", connector = InboxConnectorRef("server/session-1", label = "Assistant"),
            event = persona.core.contracts.InboxEventFields(status = "fyi"))
        assertEquals(InboxItemDetailRoute("assistant:1"), inboxRowPage(row, InboxItemPages()))
    }

    // persona://inbox/<id>/source from an extension's page: that item's own page, as the list opened it before.
    @Test fun theSourceLinkOpensTheItemsOwnPage() {
        val records = listOf(lark, mail, moment)
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1051), inboxSourcePage("lark:F1:1051", records))
        assertEquals(MailRoute("mail-1"), inboxSourcePage("mail-1", records))
        assertEquals(LarkMomentRoute("post-1", "moment-1"), inboxSourcePage("moment-1", records))
        assertNull(inboxSourcePage("gone", records))
        val later = lark.copy(startPosition = 1088)
        assertNull(InboxItemPages(listOf(event)).route(later))
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1088), inboxSourcePage(later.item.id, listOf(later)))
    }

    @Test fun retryRoutingCannotReplaceANativeConversationWithTheConnectorView() {
        val pages = InboxItemPages(listOf(event), failures = mapOf(lark.item.id to "扩展暂时不可用"))
        assertNull(pages.route(lark))
        assertEquals(LarkRoute("F1", lark.item.id, lark.startPosition), pages.open(lark))
    }

    @Test fun mailAndOtherConnectorsStillUseTheirDeclaredExtensionPages() {
        val github = InboxRecord(InboxItem("github:1", kind = "unknown", title = "Review"), source = "github")
        val pages = InboxItemPages(listOf(event.copy(connector = "mail"), event.copy(extension = "github", connector = "github")))
        assertEquals(InboxItemUiRoute(mail.item.id, "larkext", "event", mail.item.title), pages.open(mail))
        assertEquals(InboxItemUiRoute(github.item.id, "github", "event", github.item.title), pages.open(github))
    }

    // The header carries the tier (the extension's body does not), once triaged.
    @Test fun theHeaderSaysTheTier() {
        assertEquals(persona.core.domain.inbox.InboxTier.Action, inboxHeaderTier(lark.copy(item = lark.item.copy(tier = "action"))))
        assertEquals(persona.core.domain.inbox.InboxTier.Fyi, inboxHeaderTier(lark.copy(item = lark.item.copy(tier = "fyi"))))
        assertNull(inboxHeaderTier(lark))
    }

    // The header's source line says what the item's row says.
    @Test fun theSourceLineIsTheRows() {
        assertEquals(listOf("飞书", "群聊", "3 条消息"), inboxSourceTags(res, lark.copy(sourceLabel = "飞书", count = 3)))
        assertEquals(listOf("工作账号", "群聊"), inboxSourceTags(res, lark.copy(sourceLabel = "工作账号")))
        // A mail is a mail once.
        assertEquals(listOf("邮件"), inboxSourceTags(res, mail))
    }

    // At launch, before the hub's list is read, an item opens its own page and the shell hears of it; once the list
    // comes, those pages whose connector's extension draws them get the extension's page in their place.
    @Test fun lateSlotsLeaveProvisionalChatsNativeAndOnlyReplaceNonChatPages() {
        val heard = mutableListOf<androidx.navigation3.runtime.NavKey>()
        val unknown = InboxItemPages(known = false, provisional = { heard += it })
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1051), unknown.open(lark))
        assertEquals(MailRoute("mail-1"), unknown.open(mail))
        assertEquals(LarkMomentRoute("post-1", "moment-1"), unknown.open(moment))
        assertEquals(listOf(LarkRoute("F1", "lark:F1:1051", 1051), MailRoute("mail-1"), LarkMomentRoute("post-1", "moment-1")), heard)
        val provisionalChat = heard.first()
        // Known: nothing is provisional, whatever opens.
        val known = InboxItemPages(listOf(event), provisional = { heard += it })
        heard.clear()
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1051), known.open(lark))
        known.open(mail)
        assertTrue(heard.isEmpty())

        val chatLink = LarkRoute("F1", "lark:F1:1051", 1051).copy(startPosition = 1060)
        val post = LarkMomentRoute("post-1", "moment-1")
        val stack = listOf(persona.core.navigation.InboxRoute, LarkRoute("F1", "lark:F1:1051", 1051), MailRoute("mail-1"), post, chatLink)
        val provisional = setOf(LarkRoute("F1", "lark:F1:1051", 1051), MailRoute("mail-1"), post)
        // Only the post becomes an extension page; native item chats and explicit chat links keep their positions.
        assertEquals(listOf(post to InboxItemUiRoute("moment-1", "larkext", "event", moment.item.title)),
            known.swaps(stack, provisional, listOf(lark, mail, moment)))
        assertTrue(known.swaps(listOf(provisionalChat), setOf(provisionalChat), listOf(lark)).isEmpty())
        // An older hub lists no pages: nothing changes.
        assertTrue(InboxItemPages().swaps(stack, provisional, listOf(lark, mail)).isEmpty())
        // Pages that have left the stack are not brought back.
        assertTrue(known.swaps(listOf(persona.core.navigation.InboxRoute), provisional, listOf(lark)).isEmpty())
    }

    @Test fun theListMarksTheItemItsExtensionPageShows() {
        val shown = InboxItemUiRoute("lark:F1:1051", "larkext", "event")
        assertTrue(isInboxItemSelected(shown, "lark:F1:1051", "F1"))
        assertFalse(isInboxItemSelected(shown, "lark:F1:1200", "F1"))
        assertFalse(isInboxItemSelected(shown, "mail-1", null))
    }

    // #2323: a Feishu document's notice (the Lark extension's `doc` block, no chat behind it) opened the mail page, which failed
    // with "Couldn't load the mail". Only a mail opens the mail page; a document, and an item of a shape the app does not know,
    // open the item's detail page.
    @Test fun aDocumentsNoticeOpensItsOwnPageAndOnlyMailOpensTheMailPage() {
        val wire = kotlinx.serialization.json.Json.parseToJsonElement("""{"url":"https://example.larkoffice.com/wiki/W1",
            "link":"https://example.larkoffice.com/wiki/W1","type":"sheet","notice":"mention","notice_id":"n1","read_at":0,
            "reply":{"kind":"cell","label":"评论 @你的那个单元格"},"future":{"x":1},
            "rows":[{"ref":"r1","table":"任务","title":"压测报告","fields":[{"name":"跟进人","value":"@示例同事"}],
              "status":[{"field":"f1","name":"状态","value":"进行中","options":[{"name":"进行中"},{"name":"已完成"}]}]}]}""")
        val ref = requireNotNull(inboxDocRef(wire as kotlinx.serialization.json.JsonObject))
        assertEquals("https://example.larkoffice.com/wiki/W1", ref.url)
        assertEquals("sheet", ref.type)
        assertEquals("评论 @你的那个单元格", ref.reply)
        assertEquals(listOf(InboxDocRow("压测报告", "任务", listOf(InboxDocField("跟进人", "@示例同事")), listOf(InboxDocField("状态", "进行中")))), ref.rows)
        assertTrue(isLarkDocumentUrl(ref.url))
        // Nothing to open: no block.
        assertNull(inboxDocRef(kotlinx.serialization.json.Json.parseToJsonElement("""{"type":"docx"}""") as kotlinx.serialization.json.JsonObject))

        val doc = InboxRecord(InboxItem("doc:T1:1730700000000", kind = "doc", title = "Q4 计划"), source = "lark", doc = ref)
        val none = InboxItemPages()
        assertEquals(InboxItemDetailRoute("doc:T1:1730700000000"), inboxItemPage(doc))
        assertEquals(InboxItemDetailRoute("doc:T1:1730700000000"), inboxOpenPage(doc, none))
        assertEquals(InboxItemDetailRoute("doc:T1:1730700000000"), inboxSourcePage("doc:T1:1730700000000", listOf(doc)))
        // Chats, posts and mail open as before; a mail known only by its kind (a record cached without its source) too.
        assertEquals(LarkRoute("F1", "lark:F1:1051", 1051), inboxItemPage(lark))
        assertEquals(LarkMomentRoute("post-1", "moment-1"), inboxItemPage(moment))
        assertEquals(MailRoute("mail-1"), inboxItemPage(mail))
        assertEquals(MailRoute("mail-2"), inboxItemPage(InboxRecord(InboxItem("mail-2", kind = "mail"))))
        // A shape the app does not know (a connector's thread, nothing to say what it is) is not a mail.
        assertEquals(InboxItemDetailRoute("github:1"), inboxItemPage(InboxRecord(InboxItem("github:1", kind = "unknown"), source = "github")))
        assertEquals(InboxItemDetailRoute("x"), inboxItemPage(InboxRecord(InboxItem("x"))))
        // The list marks it, its row says what kind of notice, and a cached copy still opens as a document.
        assertTrue(isInboxItemSelected(InboxItemDetailRoute("doc:T1:1730700000000"), "doc:T1:1730700000000", null))
        assertEquals("@我", kindLabel(res, doc))
        assertEquals("评论", kindLabel(res, doc.copy(doc = ref.copy(notice = "comment"))))
        val cached = doc.cached().record()
        assertEquals(ref, cached.doc)
        assertEquals(InboxItemDetailRoute("doc:T1:1730700000000"), inboxItemPage(cached))
        // An extension's page that was opened before the list was known gives way for a document too.
        val pages = InboxItemPages(listOf(event))
        val key = InboxItemDetailRoute("doc:T1:1730700000000")
        assertEquals(listOf(key to InboxItemUiRoute("doc:T1:1730700000000", "larkext", "event", "Q4 计划")),
            pages.swaps(listOf(key), setOf(key), listOf(doc)))
    }
}
