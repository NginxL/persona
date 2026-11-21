package persona.feature.inbox

import java.io.IOException
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.InboxAssistantTransport

class LarkMessagesRepositoryTest {
    private class Transport : InboxAssistantTransport {
        val requests = mutableListOf<String?>()
        val messageReads = mutableListOf<String>()
        val handlers = ArrayDeque<suspend (String?) -> JsonObject>()

        override val scope = "pairing/a"
        override suspend fun larkConversations(cursor: String?): JsonObject {
            requests += cursor
            check(handlers.isNotEmpty()) { "Unexpected conversation read: $cursor" }
            return handlers.removeFirst()(cursor)
        }
        override suspend fun inbox() = JsonObject(emptyMap())
        override suspend fun setDone(itemId: String, done: Boolean) = JsonObject(emptyMap())
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = JsonObject(emptyMap())
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?): JsonObject {
            messageReads += feedId
            return JsonObject(emptyMap())
        }
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = JsonObject(emptyMap())
        override suspend fun media(ref: String) = byteArrayOf()
    }

    @Test fun firstLoadParsesRowsWithoutPrefetchingChatBodies() = runTest {
        val transport = Transport().apply { handlers += { page(next = null, direct(), group()) } }
        val repository = RealLarkMessagesRepository("pairing/a", transport)
        assertTrue(repository.state.value.loading)
        assertFalse(repository.state.value.loaded)

        repository.refresh()

        val state = repository.state.value
        assertTrue(state.loaded)
        assertFalse(state.loading)
        assertNull(state.failure)
        assertEquals(listOf<String?>(null), transport.requests)
        assertEquals(emptyList<String>(), transport.messageReads)
        assertEquals(listOf("direct", "group"), state.rows.map { it.id })
        val direct = state.rows[0]
        assertEquals("Alex", direct.name)
        assertEquals("/avatar/direct", direct.avatarRef)
        assertFalse(direct.isGroup)
        assertTrue(direct.previewMine)
        assertEquals(41L, direct.readPosition)
        val group = state.rows[1]
        assertTrue(group.isGroup)
        assertTrue(group.isTopic)
        assertTrue(group.muted)
        assertTrue(group.markedUnread)
        assertEquals(3, group.unread)
        assertEquals("Sam", group.previewSender)
        assertEquals(52L, group.mentionPosition)
    }

    @Test fun anEmptySuccessfulPageIsNotAnError() = runTest {
        val transport = Transport().apply { handlers += { page(next = null) } }
        val repository = RealLarkMessagesRepository("pairing/a", transport)
        repository.refresh()
        assertTrue(repository.state.value.loaded)
        assertTrue(repository.state.value.rows.isEmpty())
        assertNull(repository.state.value.failure)
    }

    @Test fun initialFailureIsNotEmptyAndRetryLoads() = runTest {
        val transport = Transport().apply {
            handlers += { throw IOException("offline") }
            handlers += { page(next = null, direct()) }
        }
        val repository = RealLarkMessagesRepository("pairing/a", transport)

        repository.refresh()
        assertFalse(repository.state.value.loaded)
        assertNotNull(repository.state.value.failure)
        assertTrue(repository.state.value.rows.isEmpty())

        repository.refresh()
        assertTrue(repository.state.value.loaded)
        assertNull(repository.state.value.failure)
        assertEquals(listOf("direct"), repository.state.value.rows.map { it.id })
        assertEquals(listOf<String?>(null, null), transport.requests)
    }

    @Test fun refreshReplacesTheWholeLoadedWindowOnlyAfterEveryPageSucceeds() = runTest {
        val transport = Transport().apply {
            handlers += { page(next = "two", direct()) }
            handlers += { cursor ->
                assertEquals("two", cursor)
                page(next = null, group())
            }
            // The refresh gets a changed head but a failed tail: neither may be published.
            handlers += { page(next = "two", direct(name = "Changed")) }
            handlers += { throw IOException("tail failed") }
            // Retry reads both pages again and publishes them together.
            handlers += { page(next = "two", direct(name = "Fresh head")) }
            handlers += { page(next = null, group(name = "Fresh group")) }
        }
        val repository = RealLarkMessagesRepository("pairing/a", transport)
        repository.refresh()
        repository.loadMore()
        assertEquals(listOf("direct", "group"), repository.state.value.rows.map { it.id })

        repository.refresh()
        assertEquals(listOf("Alex", "Project"), repository.state.value.rows.map { it.name })
        assertNotNull(repository.state.value.failure)
        assertTrue(repository.state.value.loaded)

        repository.refresh()
        assertEquals(listOf("Fresh head", "Fresh group"), repository.state.value.rows.map { it.name })
        assertNull(repository.state.value.failure)
        assertNull(repository.state.value.nextCursor)
    }

    @Test fun loadMoreDeduplicatesInServerOrderAndEndsARepeatedCursor() = runTest {
        val transport = Transport().apply {
            handlers += { page(next = "same", direct()) }
            handlers += { page(next = "same", direct(name = "Duplicate"), group()) }
        }
        val repository = RealLarkMessagesRepository("pairing/a", transport)
        repository.refresh()
        repository.loadMore()

        assertEquals(listOf("direct", "group"), repository.state.value.rows.map { it.id })
        assertEquals("Alex", repository.state.value.rows.first().name)
        assertNull(repository.state.value.nextCursor)
        val requests = transport.requests.toList()
        repository.loadMore()
        assertEquals(requests, transport.requests)
        assertEquals(listOf<String?>(null, "same"), requests)
    }

    @Test fun failedTailKeepsRowsAndItsCursorForRetry() = runTest {
        val transport = Transport().apply {
            handlers += { page(next = "two", direct()) }
            handlers += { throw IOException("tail failed") }
            handlers += { page(next = null, group()) }
        }
        val repository = RealLarkMessagesRepository("pairing/a", transport)
        repository.refresh()

        repository.loadMore()
        assertEquals(listOf("direct"), repository.state.value.rows.map { it.id })
        assertEquals("two", repository.state.value.nextCursor)
        assertNotNull(repository.state.value.moreFailure)
        assertNull(repository.state.value.failure)

        repository.loadMore()
        assertEquals(listOf("direct", "group"), repository.state.value.rows.map { it.id })
        assertNull(repository.state.value.moreFailure)
        assertNull(repository.state.value.nextCursor)
    }

    private fun direct(name: String = "Alex") = buildJsonObject {
        put("id", "direct")
        put("name", name)
        put("avatar_url", "/avatar/direct")
        put("is_group", false)
        put("preview", "See you soon")
        put("is_mine", true)
        put("updated_at", "2026-01-02T03:04:05Z")
        put("read_position", "41")
    }

    private fun group(name: String = "Project") = buildJsonObject {
        put("id", "group")
        put("name", name)
        put("is_group", true)
        put("is_topic", true)
        put("unread", 3)
        put("marked_unread", true)
        put("muted", true)
        put("preview", "Latest update")
        put("preview_sender", "Sam")
        put("mention_position", "52")
    }

    private fun page(next: String?, vararg rows: JsonObject) = buildJsonObject {
        put("conversations", buildJsonArray { rows.forEach(::add) })
        next?.let { put("next_cursor", it) }
    }
}
