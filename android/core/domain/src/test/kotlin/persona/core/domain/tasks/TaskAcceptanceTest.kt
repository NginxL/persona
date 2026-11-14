package persona.core.domain.tasks

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

class TaskAcceptanceTest {
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private class Api(var rows: List<Todo>) : TasksApi {
        var delayed: CompletableDeferred<List<Todo>>? = null
        val accepted = mutableListOf<Pair<String, String>>()
        val patches = mutableListOf<Pair<String, Boolean?>>()
        var patchManager: String? = "not called"
        var patchGate: CompletableDeferred<Unit>? = null
        var patchFailure: Throwable? = null
        var readFailure: Throwable? = null
        override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo {
            patchManager = currentCoroutineContext()[RequestManager]?.id
            patches += id to done
            patchGate?.await()
            patchFailure?.let { throw it }
            val updated = rows.first { it.id == id }.copy(doneAt = "2026-11-01T10:00:00Z", ask = null)
            rows = rows.map { if (it.id == id) updated else it }
            return updated
        }
        override suspend fun todos(): List<Todo> {
            readFailure?.let { throw it }
            return delayed?.let { held -> delayed = null; held.await() } ?: rows
        }
        override suspend fun acceptTask(todoId: String, askedAt: String) { accepted += todoId to askedAt }
        val answers = mutableListOf<Triple<String, String, AskAnswer>>()
        var answerFailure: Throwable? = null
        override suspend fun answerTask(todoId: String, askedAt: String, answer: AskAnswer) {
            answers += Triple(todoId, askedAt, answer)
            answerFailure?.let { throw it }
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

    private val decision = Todo("task", origin = "main", supervision = Supervision(state = "needs_user"),
        ask = TodoAsk("decide", "Choose the release scope", "Defer the optional change", "2026-11-01T09:00:00Z"))

    @Test fun finishOnlyPatchesDoneAndWaitsForConfirmationWhileBlockingBothActions() = runTest {
        val api = Api(listOf(decision)).apply { patchGate = CompletableDeferred() }
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        store.setRoute("main", decision.id)
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }
        store.finishTaskNow(decision, { throw it }) { fail("duplicate") }
        store.acceptTask(decision, { throw it }) { fail("duplicate") }
        runCurrent()
        assertEquals(listOf(decision.id to true), api.patches)
        assertTrue(api.accepted.isEmpty())
        assertEquals(decision, store.todos.value.single())
        assertTrue("accept:${decision.id}" in store.busy.value)
        api.patchGate!!.complete(Unit); runCurrent()
        assertTrue(store.todos.value.single().isDone)
        assertNull(store.todos.value.single().doneBy)
        assertNull(store.todos.value.single().ask)
        assertTrue(store.busy.value.isEmpty())
        assertNull(store.routes.value["main"])
    }

    @Test fun finishNowKeepsTheRestoredTodosManager() = runTest {
        val restored = decision.copy(manager = "m-other")
        val api = Api(listOf(restored))
        val cache = object : TodoCache {
            override suspend fun read() = listOf(restored)
            override suspend fun write(todos: List<Todo>) = Unit
        }
        val store = TasksStore(api, Connection(), backgroundScope, cache)
        store.finishTaskNow(restored, { throw it }) { fail("unchanged") }
        runCurrent()
        assertEquals("m-other", api.patchManager)
    }

    @Test fun failedFinishRetainsCardAndUnrelatedTasksAndCanRetry() = runTest {
        val other = Todo("other", "An independent task")
        val api = Api(listOf(decision, other)).apply { patchFailure = IllegalStateException("synthetic failure") }
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val errors = mutableListOf<Throwable>()
        store.finishTaskNow(decision, { errors += it }) { fail("unchanged") }; runCurrent()
        assertEquals(1, errors.size)
        assertEquals(listOf(decision, other), store.todos.value)
        assertTrue(store.busy.value.isEmpty())
        api.patchFailure = null
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }; runCurrent()
        assertTrue(store.todos.value.first().isDone)
        assertEquals(other, store.todos.value.last())
        assertEquals(2, api.patches.size)
    }

    @Test fun finishedTaskCannotResurrectFromPreWriteReadButCanReopenLater() = runTest {
        val api = Api(listOf(decision))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val held = CompletableDeferred<List<Todo>>()
        api.delayed = held
        store.reloadTodos(); runCurrent()
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }; runCurrent()
        val confirmed = store.todos.value.single()
        assertTrue(confirmed.isDone)
        held.complete(listOf(decision)); runCurrent()
        assertEquals(confirmed, store.todos.value.single())
        val reopened = decision.copy(ask = decision.ask!!.copy(askedAt = "2026-11-02T09:00:00Z"))
        api.rows = listOf(reopened)
        store.reloadTodos(); runCurrent()
        assertEquals(reopened, store.todos.value.single())
    }

    @Test fun aChangedDecisionIsNotCompletedAndMissingRecommendationStillAllowsFinish() = runTest {
        val latest = decision.copy(ask = decision.ask!!.copy(recommend = null, askedAt = "2026-11-02T09:00:00Z"))
        val api = Api(listOf(latest))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        var changed = 0
        store.finishTaskNow(decision, { throw it }) { changed++ }; runCurrent()
        assertEquals(1, changed)
        assertTrue(api.patches.isEmpty())
        store.finishTaskNow(latest, { throw it }) { fail("unchanged") }; runCurrent()
        assertTrue(store.todos.value.single().isDone)
    }

    @Test fun completionPersistsOnlyConfirmedRowsAndAFailedRefreshDoesNotUndoIt() = runTest {
        val other = Todo("other", "Independent task")
        val api = Api(listOf(decision, other))
        val otherGate = CompletableDeferred<Todo>()
        val routed = object : TasksApi by api {
            override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?) =
                if (id == other.id) otherGate.await() else api.patchTodo(id, title, notes, done)
        }
        var cached = listOf(decision, other)
        val cache = object : TodoCache {
            override suspend fun read() = cached
            override suspend fun write(todos: List<Todo>) { cached = todos }
        }
        val store = TasksStore(routed, Connection(), backgroundScope, cache)
        store.reloadTodos(); runCurrent()
        backgroundScope.launch { store.setTodoDone(other, true) }
        runCurrent()
        assertTrue(store.todos.value.last().isDone) // Unrelated completion has not been confirmed yet.
        api.readFailure = IllegalStateException("synthetic refresh failure")
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }; runCurrent()
        assertTrue(store.todos.value.first().isDone)
        assertTrue(cached.first().isDone)
        assertNull(cached.first().ask)
        assertEquals(other, cached.last())
        assertTrue(store.busy.value.isEmpty())
    }

    @Test fun unrelatedAcceptanceAndCompletedOrCancelledTasksCannotUseDirectFinish() = runTest {
        val api = Api(listOf(decision))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        for (todo in listOf(decision.copy(ask = decision.ask!!.copy(outcome = "done")),
            decision.copy(doneAt = "2026-11-01T10:00:00Z"), decision.copy(cancelledAt = "2026-11-01T10:00:00Z"))) {
            store.finishTaskNow(todo, { throw it }) { fail("not submitted") }
        }
        runCurrent()
        assertTrue(api.patches.isEmpty())
    }

    @Test fun briefCompletionUsesTheCurrentRoundAndSurvivesAFailedRefresh() = runTest {
        val api = Api(listOf(decision))
        var cached = listOf(decision)
        val cache = object : TodoCache {
            override suspend fun read() = cached
            override suspend fun write(todos: List<Todo>) { cached = todos }
        }
        val store = TasksStore(api, Connection(), backgroundScope, cache)
        store.reloadTodos(); runCurrent()
        store.setRoute("main", decision.id)

        // A brief for an older round must never complete the newer ask under the same todo id.
        store.finishBrief(decision.id, "2026-10-31T09:00:00Z", { throw it }); runCurrent()
        assertTrue(api.patches.isEmpty())
        assertEquals(BriefOutcome.AlreadyHandled,
            store.briefOutcomes.value[briefAcceptOutcomeKey(decision.id, "2026-10-31T09:00:00Z")])

        api.readFailure = IllegalStateException("synthetic refresh failure")
        store.finishBrief(decision.id, decision.ask!!.askedAt, { throw it }); runCurrent()
        assertEquals(listOf(decision.id to true), api.patches)
        assertTrue(store.todos.value.single().isDone)
        assertNull(store.todos.value.single().ask)
        assertNull(store.routes.value["main"])
        assertTrue(cached.single().isDone)
        assertNull(cached.single().ask)
        assertEquals(BriefOutcome.Completed,
            store.briefOutcomes.value[briefAcceptOutcomeKey(decision.id, decision.ask!!.askedAt)])
    }

    @Test fun briefCompletionBeforeTodosLoadWaitsForTheCurrentRound() = runTest {
        val api = Api(listOf(decision))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.finishBrief(decision.id, decision.ask!!.askedAt, { throw it }); runCurrent()
        assertTrue(api.patches.isEmpty())
        assertNull(store.briefOutcomes.value[briefAcceptOutcomeKey(decision.id, decision.ask!!.askedAt)])
        assertEquals(decision, store.todos.value.single())

        store.finishBrief(decision.id, decision.ask!!.askedAt, { throw it }); runCurrent()
        assertEquals(listOf(decision.id to true), api.patches)
        assertEquals(BriefOutcome.Completed,
            store.briefOutcomes.value[briefAcceptOutcomeKey(decision.id, decision.ask!!.askedAt)])
    }

    private class Coded(override val status: Int?) : Exception("refused"), HubCodedFailure { override val code: String? = null }

    private val choice = Todo("task", origin = "main", supervision = Supervision(state = "needs_user"),
        ask = TodoAsk("decide", "Choose the release scope", "Ship it", "2026-11-01T09:00:00Z",
            options = listOf(AskOption("Ship it", "Goes out today"), AskOption("Hold")), responses = listOf("decide_yourself", "explain_first")))

    @Test fun anOptionIsAnsweredWithTheRoundTheOwnerReadAndEndsTheDecision() = runTest {
        val api = Api(listOf(choice))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val done = mutableListOf<Boolean>()
        store.answerTask(choice, AskAnswer.Option("Hold"), { throw it }, { fail("unchanged") }) { done += it }
        // a second press while the first is on its way sends nothing
        store.answerTask(choice, AskAnswer.Option("Ship it"), { throw it }, { fail("unchanged") }) { done += it }
        runCurrent()
        assertEquals(listOf(Triple(choice.id, choice.ask!!.askedAt, AskAnswer.Option("Hold") as AskAnswer)), api.answers)
        assertEquals(listOf(true), done)
        assertTrue(api.accepted.isEmpty())
        assertNull(store.todos.value.single().ask)
        assertTrue(store.busy.value.isEmpty())
    }

    @Test fun aFixedResponseLeavesTheDecisionToTheHub() = runTest {
        val api = Api(listOf(choice))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val done = mutableListOf<Boolean>()
        store.answerTask(choice, AskAnswer.Response("explain_first"), { throw it }, { fail("unchanged") }) { done += it }
        runCurrent()
        assertEquals(listOf(Triple(choice.id, choice.ask!!.askedAt, AskAnswer.Response("explain_first") as AskAnswer)), api.answers)
        assertEquals(listOf(true), done)
        // the hub decides whether it still waits: the row is what the next read says
        assertEquals(choice.ask, store.todos.value.single().ask)
    }

    @Test fun anAnswerToARoundHandledElsewhereTakesNothingAndIsNoError() = runTest {
        val api = Api(listOf(choice)).apply { answerFailure = Coded(409) }
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val done = mutableListOf<Boolean>()
        store.answerTask(choice, AskAnswer.Option("Ship it"), { throw it }, { fail("unchanged") }) { done += it }
        runCurrent()
        assertEquals(listOf(false), done)
        assertEquals(choice.ask, store.todos.value.single().ask)
    }

    @Test fun aFailedAnswerIsReportedAndCanBeSentAgain() = runTest {
        val api = Api(listOf(choice)).apply { answerFailure = Coded(502) }
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val errors = mutableListOf<Throwable>()
        store.answerTask(choice, AskAnswer.Option("Ship it"), { errors += it }, { fail("unchanged") }) { fail("not taken") }
        runCurrent()
        assertEquals(1, errors.size)
        assertEquals(choice.ask, store.todos.value.single().ask)
        assertTrue(store.busy.value.isEmpty())
        api.answerFailure = null
        val done = mutableListOf<Boolean>()
        store.answerTask(choice, AskAnswer.Option("Ship it"), { throw it }, { fail("unchanged") }) { done += it }
        runCurrent()
        assertEquals(listOf(true), done)
        assertEquals(2, api.answers.size)
    }

    @Test fun anAnswerToARoundThatChangedMeanwhileIsNotSent() = runTest {
        val api = Api(listOf(choice.copy(ask = choice.ask!!.copy(askedAt = "2026-11-02T09:00:00Z"))))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        var changed = 0
        store.answerTask(choice, AskAnswer.Option("Ship it"), { throw it }, { changed++ }) { fail("not taken") }
        runCurrent()
        assertEquals(1, changed)
        assertTrue(api.answers.isEmpty())
    }

    @Test fun acceptedRoundCannotResurrectFromDelayedReadButANewAskSurvives() = runTest {
        val original = Todo("task", origin = "main", ask = TodoAsk(verdict = "Check the deployed page", askedAt = "2026-11-01T09:00:00Z"))
        val api = Api(listOf(original))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val held = CompletableDeferred<List<Todo>>()
        api.delayed = held
        store.reloadTodos(); runCurrent()
        var changed = 0
        store.acceptTask(original, { throw it }) { changed++ }; runCurrent()
        assertEquals(listOf(original.id to original.ask!!.askedAt), api.accepted)
        assertNull(store.todos.value.single().ask)
        held.complete(listOf(original)); runCurrent()
        assertNull(store.todos.value.single().ask)
        val latest = original.copy(ask = original.ask!!.copy(askedAt = "2026-11-02T09:00:00Z", verdict = "Check the new release"))
        api.rows = listOf(latest)
        store.reloadTodos(); runCurrent()
        assertEquals(latest.ask, store.todos.value.single().ask)
        store.acceptTask(original, { throw it }) { changed++ }; runCurrent()
        assertEquals(1, changed)
        assertEquals(1, api.accepted.size)
    }
}
