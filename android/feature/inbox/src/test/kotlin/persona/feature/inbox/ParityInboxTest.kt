package persona.feature.inbox

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.domain.inbox.InboxItem
import persona.core.inbox.*

class ParityInboxTest {
    private fun json(text: String) = Json.parseToJsonElement(text).jsonObject

    private val transport = object : InboxAssistantTransport {
        override val scope = "s"
        private fun no(): Nothing = error("not used")
        override suspend fun inbox() = no()
        override suspend fun setDone(itemId: String, done: Boolean) = no()
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = no()
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = no()
        override suspend fun assistant() = no()
        override suspend fun createConversation(main: Boolean) = no()
        override suspend fun pins() = no()
        override suspend fun mutes() = no()
        override suspend fun media(ref: String) = no()
    }

    @Test fun muteOptionsAndMutesComeFromTheHub() {
        val options = inboxMuteOptions(json("""{"mutes":[{"tag":"repo","value":"o/r","name":"o/r"},{"tag":"sender","value":"x"},{"tag":"","value":"y"}]}"""))
        assertEquals(listOf(InboxMuteOption("repo", "o/r", "o/r"), InboxMuteOption("sender", "x", "x")), options)
        assertEquals("r", options[0].shortName)
        assertEquals(persona.core.inbox.R.string.inbox_mute_repo, options[0].title)
        assertEquals(persona.core.inbox.R.string.inbox_mute_sender, options[1].title)
        val mutes = inboxMutes(json("""{"mutes":[{"id":"a","source":"lark","tag":"chat","value":"c","name":"Chat","created_at":"2026-01-01T00:00:00Z"},
            {"id":"b","source":"github","source_name":"GitHub","tag":"repo","value":"o/r","created_at":"2026-02-01T00:00:00Z"},{"source":"x"}]}"""))
        assertEquals(listOf("b", "a"), mutes.map { it.id })
        assertEquals("o/r", mutes[0].name)
        assertEquals(persona.core.inbox.R.string.inbox_mute_tag_chat, mutes[1].tagName)
    }

    @Test fun aMuteCoversTheOpenItemsOfItsSourceWithTheSameValue() {
        val repo = InboxMuteOption("repo", "o/r", "o/r")
        fun rec(id: String, source: String, done: Boolean = false, mutes: List<InboxMuteOption> = listOf(repo)) =
            InboxRecord(InboxItem(id, doneAt = if (done) "2026-01-01T00:00:00Z" else null), source = source, mutes = mutes)
        val records = listOf(rec("1", "github"), rec("2", "github"), rec("3", "github", done = true), rec("4", "lark"), rec("5", "github", mutes = emptyList()))
        assertEquals(setOf("1", "2"), mutedBy(records, records[0], repo))
    }

    @Test fun displayShowsKeptWordsOnlyWithItsSwitch() {
        val kept = LarkMessage("m", "u", "A", parts = listOf(LarkPart.Text("hi")), recalled = true, retained = true, system = true,
            edited = true, editHistory = listOf(LarkEditVersion("t", "old")))
        val plain = kept.copy(recalled = false, retained = false, system = false)
        assertFalse(LarkMessageDisplay.Hidden.showsBody(kept))
        assertTrue(LarkMessageDisplay("a", antiRecall = true).showsBody(kept))
        assertFalse(LarkMessageDisplay("a", antiRecall = true).showsBody(kept.copy(retained = false)))
        assertTrue(LarkMessageDisplay.Hidden.showsBody(plain))
        assertFalse(LarkMessageDisplay.Hidden.showsHistory(plain))
        assertTrue(LarkMessageDisplay("a", showEditHistory = true).showsHistory(plain))
        assertFalse(LarkMessageDisplay("a", showEditHistory = true).showsHistory(kept))
    }

    @Test fun mappingKeepsRecalledWordsOnlyWhenRetained() {
        val kept = larkMessage(json("""{"id":"1","recalled":true,"retained":true,"parts":[{"kind":"text","text":"kept"}],
            "edit_history":[{"edited_at":"t","text":"v1"}],"can_pin":true,"can_unpin":true}"""), transport, "f")
        assertTrue(kept.recalled && kept.retained && kept.system)
        assertEquals(listOf("kept"), kept.parts.filterIsInstance<LarkPart.Text>().map { it.text })
        assertEquals(listOf(LarkEditVersion("t", "v1")), kept.editHistory)
        assertTrue(kept.canPin && kept.canUnpin)
        val gone = larkMessage(json("""{"id":"2","recalled":true,"parts":[{"kind":"text","text":"x"}]}"""), transport, "f")
        assertTrue(gone.parts.isEmpty() && !gone.retained)
    }

    @Test fun pinsPagesReadTheHubsShape() {
        val page = larkPinsPage(json("""{"account_id":"acct","total":1,"next_cursor":"","items":[
            {"id":"p1","message_id":"m1","message":{"id":"m1","text":"hi","can_unpin":true},"created_at":"2026-01-01T00:00:00Z"},
            {"id":"p1","message_id":"m1"},{"id":"p2","message_id":"m2","message":{"id":"m2","deleted":true}}]}""")) { larkMessage(it, transport, "f") }
        assertEquals("acct", page.accountId)
        assertEquals(listOf("p1", "p2"), page.items.map { it.id })
        assertEquals(null, page.nextCursor)
        assertEquals(2, page.total)
        assertTrue(page.items[0].canRemove)
        assertTrue(page.items[1].deleted)
    }

    @Test fun readMarksFollowTheChatsKindAndTheCounts() {
        assertEquals(null, LarkReadMark.of(null, false))
        assertEquals(null, LarkReadMark.of(LarkReadState("m", 0, 0), true))
        // One-to-one: "Read" / "Unread".
        assertEquals(LarkReadMark.Read, LarkReadMark.of(LarkReadState("m", 1, 0), false))
        assertEquals(LarkReadMark.Unread, LarkReadMark.of(LarkReadState("m", 0, 1), false))
        // A group, even of two: "Read by all" / how many have not, as iOS.
        assertEquals(LarkReadMark.AllRead, LarkReadMark.of(LarkReadState("m", 1, 0), true))
        assertEquals(LarkReadMark.Progress(0, 1), LarkReadMark.of(LarkReadState("m", 0, 1), true))
        assertEquals(LarkReadMark.AllRead, LarkReadMark.of(LarkReadState("m", 4, 0), true))
        assertEquals(LarkReadMark.Progress(1, 4), LarkReadMark.of(LarkReadState("m", 1, 3), true))
        // A chat whose kind is unknown goes by the count.
        assertEquals(LarkReadMark.Read, LarkReadMark.of(LarkReadState("m", 1, 0), null))
        assertEquals(LarkReadMark.Progress(1, 4), LarkReadMark.of(LarkReadState("m", 1, 3), null))
        assertEquals(listOf(LarkReadState("a", 2, 1)),
            larkReadStates(json("""{"states":[{"message_id":"a","read_count":2,"unread_count":1},{"read_count":1}]}""")))
    }

    @Test fun pinsUseOnlyTheirOwnAccountsDisplay() {
        val shown = LarkMessageDisplay("a", antiRecall = true)
        assertEquals(shown, larkPinPolicy(shown, "a"))
        assertEquals(LarkMessageDisplay.Hidden, larkPinPolicy(shown, "b"))
        assertEquals(LarkMessageDisplay.Hidden, larkPinPolicy(LarkMessageDisplay("", antiRecall = true), ""))
    }

    @Test fun receiptsAskForMineOnceInAWhileAndNotForTheFinalOnes() {
        val r = LarkReadReceipts()
        fun m(id: String, mine: Boolean = true, position: Long? = 1) = LarkMessage(id, "u", "A", position = position, mine = mine, parts = emptyList())
        val list = listOf(m("a"), m("b"), m("c", mine = false), m("d", position = null))
        assertEquals(listOf("a", "b"), r.due(list, now = 10_000))
        assertEquals(emptyList<String>(), r.due(list, now = 11_000))
        r.record(listOf(LarkReadState("a", 3, 0), LarkReadState("b", 1, 2)))
        assertEquals(listOf("b"), r.due(list, now = 20_000))
        assertEquals(0L, r.wait(now = 23_000))
        assertEquals(2_000L, r.wait(now = 21_000))
        r.failed(404)
        assertEquals(emptyList<String>(), r.due(list, now = 40_000))
        val many = (1..150).map { m("x$it") }
        assertEquals(LarkReadReceipts.BATCH, LarkReadReceipts().due(many, now = 10_000).size)
    }

}
