package persona.core.domain.tasks

import java.time.Clock
import java.time.Instant
import java.time.ZoneOffset
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi

/** #5707: Got it and its Undo change only seen_at, optimistically, with a refused write put back. */
@OptIn(ExperimentalCoroutinesApi::class)
class TodoSeenTest {
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Api(var rows: List<Todo>) : TasksApi {
        val sent = mutableListOf<Pair<String, Boolean>>()
        var gate: CompletableDeferred<Unit>? = null
        var answerGate: CompletableDeferred<Unit>? = null
        var failure: Throwable? = null
        override suspend fun todos(): List<Todo> = rows
        override suspend fun setTodoSeen(id: String, seen: Boolean): Todo {
            sent += id to seen
            gate?.await()
            failure?.let { throw it }
            val updated = rows.first { it.id == id }.copy(seenAt = if (seen) "2026-11-05T09:31:00Z" else null)
            rows = rows.map { if (it.id == id) updated else it }
            answerGate?.await()
            return updated
        }
        override suspend fun feed(conversation: String, since: String?) = FeedResponse()
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private val now = Instant.parse("2026-11-05T09:30:00Z")
    private val notes = Todo("notes", title = "Write the release notes", origin = "conv", createdAt = "2026-11-05T07:30:00Z",
        updatedAt = "2026-11-05T09:00:00Z", doneAt = "2026-11-05T09:00:00Z")
    private val other = Todo("other", title = "Back up the NAS")

    private fun store(api: Api, scope: kotlinx.coroutines.CoroutineScope) = TasksStore(api, Connection(), scope,
        clock = Clock.fixed(now, ZoneOffset.UTC)).also { it.reloadTodos() }

    @Test fun gotItMovesBeforeTheHubAnswersAndKeepsItsAnswer() = runTest {
        val api = Api(listOf(notes, other)).apply { gate = CompletableDeferred() }
        val store = store(api, backgroundScope); runCurrent()
        val after = now.plusSeconds(3600).toEpochMilli()
        val saving = async { store.setTodoSeen(notes.id, true, after) }
        runCurrent()
        val optimistic = store.todos.value.first { it.id == notes.id }
        assertEquals(Instant.ofEpochMilli(after).toString(), optimistic.seenAt)
        assertEquals(TriageSection.Done, optimistic.triage({ null }, now.toEpochMilli()).section)
        assertEquals(other, store.todos.value.first { it.id == other.id })
        api.gate!!.complete(Unit); runCurrent()
        assertEquals("2026-11-05T09:31:00Z", saving.await().seenAt)
        assertEquals("2026-11-05T09:31:00Z", store.todos.value.first { it.id == notes.id }.seenAt)
        assertEquals(listOf(notes.id to true), api.sent)
    }

    @Test fun aRefusedGotItRestoresTheOriginalRow() = runTest {
        val api = Api(listOf(notes, other)).apply { failure = IllegalStateException("hub unavailable") }
        val store = store(api, backgroundScope); runCurrent()
        val saving = async { runCatching { store.setTodoSeen(notes.id, true) } }
        runCurrent()
        assertEquals("hub unavailable", saving.await().exceptionOrNull()?.message)
        assertEquals(listOf(notes, other), store.todos.value)
    }

    @Test fun aSlowGotItAnswerCannotOverwriteAFinishedUndo() = runTest {
        val answer = CompletableDeferred<Unit>()
        val api = Api(listOf(notes, other)).apply { answerGate = answer }
        val store = store(api, backgroundScope); runCurrent()
        val saving = async { store.setTodoSeen(notes.id, true) }
        runCurrent()
        api.answerGate = null
        val undo = async { store.setTodoSeen(notes.id, false) }
        runCurrent()
        assertNull(undo.await().seenAt)
        answer.complete(Unit); runCurrent()
        assertEquals("2026-11-05T09:31:00Z", saving.await().seenAt)
        assertNull(store.todos.value.first { it.id == notes.id }.seenAt)
        assertEquals(listOf(notes.id to true, notes.id to false), api.sent)
    }

    @Test fun undoClearsOnlyTheMarkAndARefusalKeepsItSeen() = runTest {
        val seen = notes.copy(seenAt = "2026-11-05T09:10:00Z")
        val api = Api(listOf(seen, other)).apply { gate = CompletableDeferred() }
        val store = store(api, backgroundScope); runCurrent()
        val undo = async { runCatching { store.setTodoSeen(seen.id, false) } }
        runCurrent()
        val optimistic = store.todos.value.first { it.id == seen.id }
        assertNull(optimistic.seenAt)
        assertEquals(TriageSection.Done, optimistic.triage({ null }, now.toEpochMilli()).section)
        assertEquals(seen.doneAt, optimistic.doneAt)
        api.failure = IllegalStateException("delete refused")
        api.gate!!.complete(Unit); runCurrent()
        assertEquals("delete refused", undo.await().exceptionOrNull()?.message)
        assertEquals(seen, store.todos.value.first { it.id == seen.id })
        assertEquals(listOf(seen.id to false), api.sent)
    }
}
