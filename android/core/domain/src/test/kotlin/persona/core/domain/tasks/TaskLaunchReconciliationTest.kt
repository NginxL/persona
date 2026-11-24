package persona.core.domain.tasks

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class TaskLaunchReconciliationTest {
    private val key = SessionKey("box", "original")
    private val starting = Todo("task", origin = "main")
    private val linked = starting.copy(agents = listOf(TodoAgent("box", "original", title = "Original agent", cwd = "/code/app")))
    private val running = Session("box", "original", state = "working", rev = 12)

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Api : TasksApi {
        var list = emptyList<Todo>()
        var read: (suspend () -> List<Todo>)? = null
        var agent: Session? = null
        var lookup: (suspend () -> Session?)? = null
        var retries = 0
        val retryManagers = mutableListOf<String?>()
        var retryGate: CompletableDeferred<Unit>? = null
        override val features = MutableStateFlow(setOf(TASK_RETRY_LAUNCHER_FEATURE))
        val launcherRetries = mutableListOf<Triple<String, String, String?>>()
        var launcherRetryGate: CompletableDeferred<Unit>? = null
        var launcherRetryResult: Todo? = null
        val answers = mutableListOf<Triple<String, String, String?>>()
        var answerGate: CompletableDeferred<Unit>? = null
        var answerResult: Todo? = null
        var answerFailure: Throwable? = null
        override suspend fun todos() = read?.invoke() ?: list
        override suspend fun taskAgent(key: SessionKey) = lookup?.invoke() ?: agent
        override suspend fun retryTask(todoId: String): Todo {
            retries++
            retryManagers += currentCoroutineContext()[RequestManager]?.id
            retryGate?.await()
            return list.single()
        }
        override suspend fun retryTaskOnLauncher(todoId: String, launcher: String): Todo {
            launcherRetries += Triple(todoId, launcher, currentCoroutineContext()[RequestManager]?.id)
            launcherRetryGate?.await()
            return launcherRetryResult ?: list.single { it.id == todoId }
        }
        override suspend fun answerTaskStart(todoId: String, choice: String): Todo {
            answers += Triple(todoId, choice, currentCoroutineContext()[RequestManager]?.id)
            answerGate?.await()
            answerFailure?.let { throw it }
            return answerResult ?: list.single { it.id == todoId }
        }
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    @Test fun aDurableBindingOpensTheOriginalAgentOutsideTheFilteredWindow() = runTest {
        val store = TasksStore(Api(), Connection(), backgroundScope)
        val unrelated = listOf(Session("other", "row", state = "working"))
        assertEquals(key, store.lead(linked, unrelated)?.key)
        assertEquals(TaskState.Followup, store.stateOf(linked, unrelated))
        assertEquals("offline", store.lead(linked, unrelated)?.state)
        assertEquals(TaskState.NotStarted, store.stateOf(starting, unrelated))
        assertTrue(store.agentsOf(linked.copy(agents = linked.agents.map { it.copy(dismissedAt = "2026-01-01T00:00:00Z") }), unrelated).isEmpty())
    }

    @Test fun aWaitingReplacementKeepsAnOldInterruptionOutOfTaskState() = runTest {
        val store = TasksStore(Api(), Connection(), backgroundScope)
        val old = Session("box", "original", state = "unread", turnError = "opaque account transition 7A99",
            turnErrorKind = "unrecoverable", turnErrorCode = "compaction_sealed")
        val task = starting.copy(agents = listOf(
            TodoAgent("box", "original"),
            TodoAgent("box", "replacement"),
        ))
        for (on in listOf("agent", "external", "transient")) {
            val replacement = Session("box", "replacement", state = "idle", work = WorkState(phase = "blocked", on = on))
            assertTrue(on, replacement.waitsWithoutBackground())
            assertNotEquals(on, TaskState.Interrupted, store.stateOf(task, listOf(old, replacement)))
        }
    }

    @Test fun aTransientWaitingReplacementWithTurnErrorKeepsAnOldInterruptionOutOfTaskState() = runTest {
        val store = TasksStore(Api(), Connection(), backgroundScope)
        val old = Session("box", "original", state = "idle", turnError = "opaque account transition 7A99",
            turnErrorKind = "unrecoverable", turnErrorCode = "compaction_sealed")
        val replacement = Session("box", "replacement", state = "idle",
            turnError = "API Error: 503 Service Unavailable", turnErrorKind = "transient",
            work = WorkState(phase = "blocked", on = "transient"))
        val task = starting.copy(agents = listOf(
            TodoAgent("box", "original"),
            TodoAgent("box", "replacement"),
        ))
        assertTrue(replacement.waitsWithoutBackground())
        assertEquals(TaskState.Followup, store.stateOf(task, listOf(old, replacement)))
    }

    @Test fun anInvalidAgentWithStaleTransientWorkRemainsInterruptedInTaskState() = runTest {
        val store = TasksStore(Api(), Connection(), backgroundScope)
        val invalid = Session("box", "original", state = "idle", turnError = "opaque account transition 7A99",
            turnErrorKind = "unrecoverable", turnErrorCode = "compaction_sealed",
            work = WorkState(phase = "blocked", on = "transient"))
        assertEquals(TaskState.Interrupted, store.stateOf(linked, listOf(invalid)))
    }

    @Test fun delayedAcknowledgementAndReconnectRecoverTheExistingBinding() = runTest {
        val api = Api().apply { list = listOf(starting) }
        val connection = Connection()
        val store = TasksStore(api, connection, backgroundScope)
        store.reloadTodos(); runCurrent()
        assertEquals(TaskState.NotStarted, store.stateOf(store.todos.value.single(), emptyList()))
        api.list = listOf(linked); api.agent = running
        connection.events.emit(TodosEvent()); runCurrent()
        store.watchAgents(setOf(key)); runCurrent()
        assertEquals(key, store.lead(store.todos.value.single(), emptyList())?.key)
        assertEquals(TaskState.Working, store.stateOf(store.todos.value.single(), emptyList()))
        // The event while disconnected is missed; Online must fetch the reopened row and current metadata.
        api.list = listOf(linked.copy(doneAt = "2026-01-01T00:00:00Z"))
        store.reloadTodos(); runCurrent()
        assertEquals(TaskState.Done, store.stateOf(store.todos.value.single(), emptyList()))
        api.list = listOf(linked)
        connection.state.value = ConnectionState.Online; runCurrent()
        assertEquals(TaskState.Working, store.stateOf(store.todos.value.single(), emptyList()))
        assertEquals(0, api.retries)
    }

    @Test fun concurrentInvalidationsCannotPersistAnOldUnboundOrCompletedSnapshotLast() = runTest {
        for (old in listOf(starting, linked.copy(doneAt = "2026-01-01T00:00:00Z"))) {
            val gate = CompletableDeferred<Unit>()
            var calls = 0
            var active = 0
            var maximum = 0
            var persisted = emptyList<Todo>()
            val cache = object : TodoCache {
                override suspend fun read(): List<Todo>? = null
                override suspend fun write(todos: List<Todo>) { persisted = todos }
            }
            val api = Api().apply { read = {
                val call = ++calls
                maximum = maxOf(maximum, ++active)
                if (call == 1) gate.await()
                active--
                if (call == 1) listOf(old) else listOf(linked)
            } }
            val store = TasksStore(api, Connection(), backgroundScope, cache)
            store.reloadTodos(); runCurrent()
            store.reloadTodos(); runCurrent()
            assertEquals(1, calls)
            gate.complete(Unit); runCurrent()
            assertEquals(1, maximum)
            assertEquals(linked, store.todos.value.single())
            assertEquals(linked, persisted.single())
        }
    }

    @Test fun failedOrTimedOutStartsAreRetryableAndRepeatedTapsSendOneRequest() = runTest {
        for (reason in listOf("launcher missing", "context deadline exceeded")) {
            val failed = starting.copy(doneAt = "2026-01-01T00:00:00Z", startError = reason)
            val gate = CompletableDeferred<Unit>()
            val api = Api().apply { list = listOf(starting); retryGate = gate }
            val store = TasksStore(api, Connection(), backgroundScope)
            assertEquals(TaskState.Failed, store.stateOf(failed, emptyList()))
            store.retryTask(failed) { throw it }; store.retryTask(failed) { throw it }; runCurrent()
            assertEquals(1, api.retries)
            gate.complete(Unit); runCurrent()
        }
    }

    @Test fun plainLauncherAndAnswerActionsShareRetryBusyKey() = runTest {
        val failed = starting.copy(doneAt = "2026-01-01T00:00:00Z", startError = "launcher missing")
        for (first in listOf("plain", "launcher", "answer")) {
            val gate = CompletableDeferred<Unit>()
            val api = Api().apply {
                list = listOf(failed)
                when (first) {
                    "plain" -> retryGate = gate
                    "launcher" -> launcherRetryGate = gate
                    else -> answerGate = gate
                }
            }
            val store = TasksStore(api, Connection(), backgroundScope)
            val errors = mutableListOf<Throwable>()
            val firstAnswer = when (first) {
                "plain" -> {
                    store.retryTask(failed) { errors += it }
                    null
                }
                "launcher" -> {
                    store.retryTaskOnLauncher(failed, "claude") { errors += it }
                    null
                }
                else -> async { store.answerTaskStart(failed, "n") }
            }
            runCurrent()
            assertEquals("the $first action holds Retry", setOf(TasksStore.retryKey(failed.id)), store.busy.value)

            store.retryTask(failed) { errors += it }
            store.retryTaskOnLauncher(failed, "claude") { errors += it }
            val competingAnswer = async { store.answerTaskStart(failed, "n") }
            runCurrent()
            assertFalse("a competing answer is refused while $first is running", competingAnswer.await())
            val expected = Triple(if (first == "plain") 1 else 0, if (first == "launcher") 1 else 0, if (first == "answer") 1 else 0)
            assertEquals("only $first is sent", expected, Triple(api.retries, api.launcherRetries.size, api.answers.size))
            assertTrue(errors.isEmpty())

            gate.complete(Unit); runCurrent()
            if (firstAnswer != null) assertTrue(firstAnswer.await())
            assertEquals("completion does not resend $first", expected, Triple(api.retries, api.launcherRetries.size, api.answers.size))
            assertTrue(errors.isEmpty())
            assertTrue(store.busy.value.isEmpty())
        }
    }

    @Test fun launcherRetryWithoutPlanIdUsesTakeoverSuggestionManagerAndImmediateTodo() = runTest {
        val failed = starting.copy(doneAt = "2026-01-01T00:00:00Z", startError = "terminal launcher missing", manager = "m-work")
        val reopened = failed.copy(doneAt = null, startError = null)
        val plan = TaskPlan("takeover", conversation = "main", state = "started", tasks = listOf(
            PlanTask(title = "Taken over", todoId = failed.id, retryWith = listOf("claude", "work"))))
        val gate = CompletableDeferred<Unit>()
        val api = Api().apply {
            list = listOf(failed)
            launcherRetryGate = gate
            launcherRetryResult = reopened
        }
        val connection = Connection()
        val store = TasksStore(api, connection, backgroundScope)
        store.reloadTodos(); runCurrent()
        connection.events.emit(TaskPlanEvent(plan)); runCurrent()
        assertNull(failed.planId)
        val launcher = store.launcherForRetry(failed) ?: error("the takeover plan's suggestion was not found")
        assertEquals("claude", launcher)

        val refresh = CompletableDeferred<List<Todo>>()
        api.read = { refresh.await() }
        store.retryTaskOnLauncher(failed, launcher) { throw it }
        runCurrent()
        assertEquals(listOf(Triple(failed.id, "claude", "m-work")), api.launcherRetries)
        assertEquals(0, api.retries)
        assertEquals(setOf(TasksStore.retryKey(failed.id)), store.busy.value)

        gate.complete(Unit); runCurrent()
        assertFalse("the follow-up read is still held", refresh.isCompleted)
        assertEquals("the retry response is visible before that read", reopened, store.todos.value.single())
        refresh.complete(listOf(reopened)); runCurrent()
        assertEquals(reopened, store.todos.value.single())
        assertTrue(store.busy.value.isEmpty())
    }

    @Test fun plainRetryWithoutPlanIdUsesTheTodosManager() = runTest {
        val failed = starting.copy(doneAt = "2026-01-01T00:00:00Z", startError = "terminal launcher missing", manager = "m-work")
        val reopened = failed.copy(doneAt = null, startError = null)
        val api = Api().apply { list = listOf(reopened) }
        val store = TasksStore(api, Connection(), backgroundScope)

        store.retryTask(failed) { throw it }
        runCurrent()

        assertNull(failed.planId)
        assertEquals(listOf("m-work"), api.retryManagers)
        assertEquals(reopened, store.todos.value.single())
        assertTrue(store.busy.value.isEmpty())
    }

    @Test fun launcherRetryCapabilityLossRefusesWithoutRequestOrBusyLeak() = runTest {
        val api = Api().apply { features.value = emptySet() }
        val store = TasksStore(api, Connection(), backgroundScope)
        val errors = mutableListOf<Throwable>()

        store.retryTaskOnLauncher(starting, "claude") { errors += it }
        runCurrent()

        assertTrue(errors.single() is LauncherRetryUnsupportedException)
        assertTrue(api.launcherRetries.isEmpty())
        assertEquals(0, api.retries)
        assertTrue(store.busy.value.isEmpty())
    }

    @Test fun answerRefusalCleansUpAndOnlyAnExplicitSecondAnswerResends() = runTest {
        val failed = starting.copy(doneAt = "2026-01-01T00:00:00Z", startError = "terminal question", manager = "m-work")
        val reopened = failed.copy(doneAt = null, startError = null)
        val plan = TaskPlan("question", conversation = "main", state = "started", tasks = listOf(
            PlanTask(title = "Question", todoId = failed.id, errorCode = "shell_waiting",
                errorArgs = mapOf("line" to "Continue?", "choices" to "y,n"))))
        val refusal = IllegalStateException("answer refused")
        val api = Api().apply { list = listOf(failed); answerFailure = refusal }
        val connection = Connection()
        val store = TasksStore(api, connection, backgroundScope)
        store.reloadTodos(); runCurrent()
        connection.events.emit(TaskPlanEvent(plan)); runCurrent()

        val answerError = runCatching { store.answerTaskStart(failed, "n") }.exceptionOrNull()
        assertTrue(answerError is IllegalStateException)
        // Coroutine stack recovery may copy the same refusal across the manager context boundary.
        assertEquals("answer refused", answerError?.message)
        assertEquals(failed, store.todos.value.single())
        assertEquals("the unanswered question stays available", plan, store.plans.value[plan.id])
        assertEquals(listOf(Triple(failed.id, "n", "m-work")), api.answers)
        assertTrue(store.busy.value.isEmpty())
        runCurrent()
        assertEquals("a refusal never resends by itself", 1, api.answers.size)
        assertEquals(0, api.retries)
        assertTrue(api.launcherRetries.isEmpty())

        api.answerFailure = null
        api.answerResult = reopened
        val refresh = CompletableDeferred<List<Todo>>()
        api.read = { refresh.await() }
        assertTrue(store.answerTaskStart(failed, "n"))
        assertEquals("the answer response is visible before the follow-up read", reopened, store.todos.value.single())
        assertEquals(2, api.answers.size)
        assertEquals(listOf("m-work", "m-work"), api.answers.map { it.third })
        assertTrue(store.busy.value.isEmpty())
        runCurrent()
        assertFalse(refresh.isCompleted)
        refresh.complete(listOf(reopened)); runCurrent()
        assertEquals(reopened, store.todos.value.single())
        assertEquals(0, api.retries)
        assertTrue(api.launcherRetries.isEmpty())
    }

    @Test fun aLookupFailureKeepsTheOriginalOpenTargetWithoutPretendingItIsStillWorking() = runTest {
        val api = Api().apply { agent = running }
        val connection = Connection()
        val store = TasksStore(api, connection, backgroundScope)
        store.watchAgents(setOf(key)); runCurrent()
        api.lookup = { throw IllegalStateException("offline") }
        connection.events.emit(SessionsListEvent("r2")); runCurrent()
        assertEquals(TaskState.Followup, store.stateOf(linked, emptyList()))
        assertEquals(key, store.lead(linked, emptyList())?.key)
        assertEquals("offline", store.agentLookupFailures.value[key]?.message)
        assertEquals(0, api.retries)
    }

    @Test fun lookupInvalidationsRefreshWithoutChangingBoundIdentityAndRejectLateUnwatchedRows() = runTest {
        val api = Api().apply { agent = running }
        val connection = Connection()
        val store = TasksStore(api, connection, backgroundScope)
        store.watchAgents(setOf(key)); runCurrent()
        assertEquals(TaskState.Working, store.stateOf(linked, emptyList()))
        api.agent = running.copy(state = "needs_you", rev = 13)
        connection.events.emit(SessionsListEvent("r13")); runCurrent()
        assertEquals(TaskState.NeedsYou, store.stateOf(linked, emptyList()))
        val gate = CompletableDeferred<Session?>()
        api.lookup = { gate.await() }
        connection.events.emit(SessionsListEvent("r14")); runCurrent()
        store.watchAgents(emptySet())
        gate.complete(running); runCurrent()
        assertTrue(store.agentSessions.value.isEmpty())
    }
}
