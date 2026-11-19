package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.*

/** The connector thread page's paging, quote jumps and reply outcomes, against a fake hub API. */
class ConnectorThreadModelTest {
    private fun msg(id: String) = ConnectorThreadMessage(id, text = id)
    private fun page(vararg ids: String, before: String? = null, members: Int? = null) =
        ConnectorThreadPage(ConnectorThreadChat(kind = "group", members = members, reply = "chat.reply"), ids.map(::msg), before)

    /** Pages by cursor: "" is the newest. */
    private class FakeHub(val pages: Map<String, ConnectorThreadPage>, val reply: () -> ConnectorReplyOutcome = { ConnectorReplyOutcome.Sent(null) }) :
        ConnectorsManagement {
        val calls = mutableListOf<String>()
        val replies = mutableListOf<Pair<String, String?>>()
        /** Reads of these cursors wait until the test lets them through. */
        val held = mutableMapOf<String, CompletableDeferred<Unit>>()
        override suspend fun connectorThread(id: String, thread: String, before: String?, limit: Int): ConnectorThreadPage {
            calls += before.orEmpty()
            held[before.orEmpty()]?.await()
            return pages[before.orEmpty()] ?: throw java.io.IOException("no page")
        }
        override suspend fun connectorThreadReply(id: String, thread: String, action: String, text: String, replyTo: String?): ConnectorReplyOutcome {
            replies += text to replyTo
            return reply()
        }
        private fun unused(): Nothing = throw AssertionError("not used")
        override suspend fun connectors(all: Boolean): List<HubConnector> = unused()
        override suspend fun setConnectorEnabled(id: String, enabled: Boolean): HubConnector = unused()
        override suspend fun setConnectorFeed(id: String, feed: String, on: Boolean): HubConnector = unused()
        override suspend fun deleteConnectorData(id: String): ConnectorDataDeleted = unused()
        override suspend fun deleteConnectorArchive(id: String): ConnectorDataDeleted = unused()
        override suspend fun mailAccounts(): List<MailAccount> = unused()
        override suspend fun addMailAccount(req: AddMailAccountRequest): MailAccount = unused()
        override suspend fun deleteMailAccount(id: String): Unit = unused()
        override suspend fun testMailAccount(id: String): MailTestResult = unused()
        override suspend fun googleSignInStart(loginHint: String?): GoogleStart = unused()
        override suspend fun googleSignInFinish(finish: GoogleFinish): MailAccount = unused()
        override suspend fun larkStatus(): LarkStatus = unused()
        override suspend fun larkAccounts(): LarkAccounts? = unused()
        override suspend fun larkChatChoices(): List<LarkChatChoice> = unused()
    }

    @Test fun olderPagesGoFirstWithoutRepeats() {
        val merged = mergeOlder(page("m3", "m4", before = "c1", members = 5), page("m1", "m2", "m3", before = "c0"))
        assertEquals(listOf("m1", "m2", "m3", "m4"), merged.messages.map { it.id })
        assertEquals("c0", merged.before)
        assertEquals(5, merged.chat.members)
    }

    @Test fun theNewestPageReplacesTheTailAndKeepsTheCount() {
        val have = page("m1", "m2", "m3", before = "c0", members = 5)
        val again = mergeNewest(have, page("m2", "m3", "m4", before = "c1"))
        assertEquals(listOf("m1", "m2", "m3", "m4"), again.messages.map { it.id })
        assertEquals("c0", again.before)
        assertEquals(5, again.chat.members)
        // A page with nothing in common starts over.
        assertEquals(listOf("m9"), mergeNewest(have, page("m9")).messages.map { it.id })
    }

    @Test fun aQuoteReadsBackAtMostThreePages() = runBlocking {
        val older = mapOf("c2" to page("m4", before = "c1"), "c1" to page("m3", before = "c0"), "c0" to page("m2", before = "cx"), "cx" to page("m1"))
        val (found, ok) = reachMessage(page("m5", before = "c2"), "m3") { older.getValue(it) }
        assertTrue(ok)
        assertEquals(listOf("m3", "m4", "m5"), found.messages.map { it.id })
        val (_, far) = reachMessage(page("m5", before = "c2"), "m1") { older.getValue(it) }
        assertFalse(far)
        // At the thread's start there is nothing more to read.
        assertFalse(reachMessage(page("m5"), "m1") { error("no older page") }.second)
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun aMessageThatCameDuringAQuoteJumpStays() = runTest {
        val pages = mutableMapOf("" to page("m3", "m4", before = "c1"), "c1" to page("m1", "m2", before = "c0"))
        val hub = FakeHub(pages)
        val model = ConnectorThreadModel(hub, "example", "g1")
        model.load()
        val older = CompletableDeferred<Unit>()
        hub.held["c1"] = older
        val jump = async { model.reach("m1") }
        runCurrent()
        // A new message comes in while the jump reads back: the page is read again.
        pages[""] = page("m3", "m4", "m5", before = "c1")
        model.load()
        older.complete(Unit)
        assertTrue(jump.await())
        assertEquals(listOf("m1", "m2", "m3", "m4", "m5"), model.state.value.page?.messages?.map { it.id })
        assertEquals("c0", model.state.value.page?.before)
    }

    @Test fun replyOutcomesAreShownAsTheHubSaidThem() = runBlocking {
        var outcome: ConnectorReplyOutcome = ConnectorReplyOutcome.Sent("queued")
        val hub = FakeHub(mapOf("" to page("m1")), reply = { outcome })
        val model = ConnectorThreadModel(hub, "example", "g1")
        model.load()
        assertEquals(ConnectorReplyOutcome.Sent("queued"), model.reply("hi", msg("m1")))
        assertEquals("hi" to "m1", hub.replies.last())
        assertEquals(listOf("", ""), hub.calls) // a sent reply reloads the page
        outcome = ConnectorReplyOutcome.Declined("not approved")
        assertEquals(outcome, model.reply("hi", null))
        assertEquals(outcome, model.state.value.outcome)
        outcome = ConnectorReplyOutcome.Failed("connector unavailable")
        assertEquals(outcome, model.reply("hi", null))
        assertFalse(model.state.value.sending)
        assertEquals(listOf("", ""), hub.calls) // not sent: not reloaded
        // Blank text, or a read-only page, sends nothing.
        assertNull(model.reply("  ", null))
        assertEquals(3, hub.replies.size)
    }

    @Test fun aReadOnlyOrUnreadableThreadSendsNothing() = runBlocking {
        val readOnly = FakeHub(mapOf("" to page("m1").let { it.copy(chat = it.chat.copy(reply = null)) }))
        val model = ConnectorThreadModel(readOnly, "example", "g1")
        model.load()
        assertNull(model.reply("hi", null))
        assertTrue(readOnly.replies.isEmpty())
        val broken = ConnectorThreadModel(FakeHub(emptyMap()), "example", "g1")
        broken.load()
        assertTrue(broken.state.value.loaded)
        assertTrue(broken.state.value.failure is java.io.IOException)
        assertNull(broken.state.value.page)
    }
}
