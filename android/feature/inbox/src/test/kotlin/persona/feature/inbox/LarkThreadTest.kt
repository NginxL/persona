package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
import org.junit.Assert.*
import org.junit.Test

class LarkThreadTest {
    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun lostReceiptRestoresPendingRequestAndReconcilesWithOriginalClientId() = runTest {
        val drafts = mutableMapOf<ReplyTarget, ReplyDraft>()
        val repository = object : InboxRepository by FakeInboxRepository() {
            override suspend fun readDraft(target: ReplyTarget) = drafts[target]
            override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) { drafts[target] = draft }
        }
        val requests = mutableListOf<ReplyRequest>()
        var shown = false
        val source = object : LarkThreadSource {
            override val canReply = true
            override suspend fun page(before: String?) = LarkThreadPage(if (shown) listOf(
                message("remote").copy(clientId = requests.single().clientId)) else emptyList(), false, null)
            override suspend fun reply(request: ReplyRequest): Boolean {
                assertEquals(request.clientId, drafts[ReplyTarget.Lark("thread-pending:root")]?.clientId)
                requests += request
                if (requests.size == 1) error("Lost response")
                return requests.size > 2
            }
        }
        val adapter = LarkThreadReplyRepository(repository, "root", source)
        val composer = ReplyComposer(adapter, adapter.target, backgroundScope)
        runCurrent()
        composer.update("话题回复")
        runCurrent()
        composer.send()
        composer.send()
        runCurrent()
        assertEquals(1, requests.size)
        assertEquals(ReplyDelivery.Unknown, composer.state.value.delivery)
        val restored = LarkThreadReplyRepository(repository, "root", source)
        val reopened = ReplyComposer(restored, restored.target, backgroundScope)
        runCurrent()
        assertFalse(reopened.state.value.canEdit)
        reopened.send()
        runCurrent()
        assertFalse(reopened.state.value.sent)
        assertEquals(ReplyDelivery.Unknown, reopened.state.value.delivery)
        shown = true
        reopened.send()
        runCurrent()
        assertTrue(reopened.state.value.sent)
        assertEquals(1, requests.size)
        assertEquals(1, requests.distinct().size)
        assertEquals("", drafts[adapter.target]?.text)
        assertEquals("", drafts[ReplyTarget.Lark("thread-pending:root")]?.text)
        assertNull(LarkThreadReplyRepository(repository, "other", source).readDraft(ReplyTarget.Lark("thread:other")))
    }

    @Test fun pagesDeduplicateKeepServerOrderAndPreserveWindowOnFailure() = runTest {
        val requested = mutableListOf<String?>()
        val pages = ArrayDeque<LarkThreadPage>()
        val loader = LarkThreadLoader("root", LarkThreadSource { before ->
            requested += before
            pages.removeFirst()
        })
        pages += LarkThreadPage(listOf(message("root"), message("recent")), true, "opaque/cursor")
        loader.load()
        assertEquals(listOf("recent"), loader.state.value.messages.map { it.id })
        pages += LarkThreadPage(listOf(message("old"), message("recent", "已编辑")), true, "next")
        loader.load(older = true)
        assertEquals(listOf(null, "opaque/cursor"), requested)
        assertEquals(listOf("old", "recent"), loader.state.value.messages.map { it.id })
        assertEquals(listOf(LarkPart.Text("已编辑")), loader.state.value.messages.last().parts)

        // A failed request and a repeated cursor must leave the loaded history intact.
        loader.load(older = true)
        assertNotNull(loader.state.value.error)
        assertFalse(loader.state.value.loading)
        pages += LarkThreadPage(listOf(message("invalid")), true, "next")
        loader.load(older = true)
        assertEquals(listOf("old", "recent"), loader.state.value.messages.map { it.id })
        pages += LarkThreadPage(listOf(message("first")), false, null)
        loader.load(older = true)
        assertFalse(loader.state.value.hasOlder)
        assertNull(loader.state.value.error)
        assertEquals(listOf("first", "old", "recent"), loader.state.value.messages.map { it.id })

        pages += LarkThreadPage(listOf(message("latest")), true, "tail")
        loader.load()
        assertEquals(listOf("latest"), loader.state.value.messages.map { it.id })
        assertEquals(2, loader.state.value.revision)
        assertNull(requested.last())
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun aRefreshAskedForWhileAnOlderPageIsReadStillRuns() = runTest {
        var newest = listOf(message("r1"), message("r2"))
        val older = CompletableDeferred<Unit>()
        val loader = LarkThreadLoader("root", LarkThreadSource { before ->
            if (before == null) LarkThreadPage(newest, true, "c1") else { older.await(); LarkThreadPage(listOf(message("r0")), false, null) }
        })
        loader.load()
        val reading = launch { loader.load(older = true) }
        runCurrent()
        // The user's reply arrives while the older page is still on its way: the sheet asks for the newest page again.
        newest = listOf(message("r1"), message("r2"), message("r3"))
        val refresh = launch { loader.load() }
        runCurrent()
        older.complete(Unit)
        reading.join()
        refresh.join()
        assertEquals("r3", loader.state.value.messages.last().id)
        assertFalse(loader.state.value.loading)
    }

    private fun message(id: String, text: String = "回复") = LarkMessage(
        id = id, senderId = "sender", senderName = "发送者", position = -1, parts = listOf(LarkPart.Text(text)),
    )
}
