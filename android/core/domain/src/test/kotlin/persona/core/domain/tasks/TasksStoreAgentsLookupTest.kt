package persona.core.domain.tasks

import java.time.Clock
import java.time.Instant
import java.time.ZoneOffset
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.LnModeResponse
import persona.core.contracts.PushInterceptsResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi

class TasksStoreAgentsLookupTest {
    private val now = Instant.parse("2026-11-14T04:00:00Z")

    private class StubApi : TasksApi {
        override suspend fun lnMode() = LnModeResponse(false)
        override suspend fun pushIntercepts() = PushInterceptsResponse(emptyList())
        override suspend fun todos() = emptyList<Todo>()
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private class StubConnection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    @Test
    fun keyedAgentLookupMatchesListOverloads() {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        try {
            val store = TasksStore(StubApi(), StubConnection(), scope, clock = Clock.fixed(now, ZoneOffset.UTC))
            val working = Session("m", "a1", state = "working", status = "working", updatedAt = "2026-11-14T03:00:00Z")
            val needsYou = Session("m", "a2", state = "needs_you", status = "blocked", updatedAt = "2026-11-14T02:00:00Z")
            val idle = Session("m", "a3", state = "idle", status = "idle", updatedAt = "2026-11-14T03:30:00Z")
            val sessions = listOf(working, needsYou, idle)
            val sessionsByKey = sessions.associateBy { it.key }
            val todo = Todo(
                id = "t1",
                title = "Ship fix",
                agents = listOf(
                    TodoAgent("m", "a1"),
                    TodoAgent("m", "a2"),
                    TodoAgent("m", "missing", title = "Fallback"),
                ),
            )

            val agentsFromList = store.agentsOf(todo, sessions)
            val agentsFromMap = store.agentsOf(todo, sessionsByKey)
            assertEquals(agentsFromList, agentsFromMap)
            assertEquals(store.stateOf(todo, sessions), store.stateOfAgents(todo, agentsFromMap))
            assertEquals(store.lead(todo, sessions), store.leadOfAgents(agentsFromMap))
            assertEquals(needsYou, store.leadOfAgents(agentsFromMap))
        } finally {
            scope.cancel()
        }
    }
}
