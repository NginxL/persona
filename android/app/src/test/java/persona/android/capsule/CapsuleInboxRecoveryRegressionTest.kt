package persona.android.capsule

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import java.lang.reflect.Proxy
import kotlinx.coroutines.CoroutineExceptionHandler
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.After
import org.junit.Before
import org.junit.Test
import persona.android.AppText
import persona.android.ui.ShellSession
import persona.core.contracts.api.AssistantConversations
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.HubPreferences
import persona.core.contracts.api.HubPreferencesSnapshot
import persona.core.domain.inbox.InboxItem
import persona.feature.agents.AgentsPreferences
import persona.feature.inbox.*
import persona.core.inbox.*
import persona.feature.inbox.InboxRepository

/** A selected inbox conversation can disappear while the capsule has no message observer. */
@OptIn(ExperimentalCoroutinesApi::class)
class CapsuleInboxRecoveryRegressionTest {
    // The regression tests routing and ownership; translated action labels are a separate boundary.
    @Before fun labels() = AppText.install { id, _ -> "label:$id" }

    @After fun restoreLabels() = persona.android.ZhText.install()

    @Test fun removingTheCollapsedSelectionDoesNotKillObservationAndAnotherConversationCanOpen() = runTest {
        val errors = mutableListOf<Throwable>()
        val owner = CoroutineScope(SupervisorJob() + StandardTestDispatcher(testScheduler) +
            CoroutineExceptionHandler { _, error -> errors += error })
        try {
            val repository = Repository("old", "new")
            val controller = InboxConversations(repository, "paired", owner, ReplyComposers(repository, owner))
            runCurrent()
            assertEquals(setOf("old", "new"), controller.state.value.rows.map { it.key.thread }.toSet())
            repository.conversationInbox.value = controller
            val source = CapsuleInboxSource(shell(repository), owner, {})
            runCurrent()
            assertTrue(errors.joinToString("\n") { it.stackTraceToString() }, errors.isEmpty())
            assertEquals(2, source.content.value.entries.size)
            val oldId = source.content.value.entries.first { it.title == "old" }.id
            val newId = source.content.value.entries.first { it.title == "new" }.id
            source.select(oldId)
            source.shown(true)
            runCurrent()
            assertEquals("Body old", source.content.value.messages.single().text)

            source.shown(false)
            runCurrent()
            repository.snapshot.value = InboxSnapshot(records = repository.snapshot.value.records.filter { it.feedId != "old" })
            runCurrent()
            assertEquals(listOf("new"), controller.state.value.rows.map { it.key.thread })
            source.shown(true)
            runCurrent()

            assertTrue("Reopening a removed selection must not throw from the stateIn observer:\n" +
                errors.joinToString("\n") { it.stackTraceToString() }, errors.isEmpty())
            source.select(newId)
            runCurrent()
            assertEquals(newId, source.content.value.selectedId)
            assertEquals("Body new", source.content.value.messages.single().text)
            assertTrue(source.content.value.canSend)
        } finally { owner.cancel() }
    }

    @Test fun replacingTheControllerWhileCollapsedDoesNotSendTheOldScopedKeyToTheNewOwner() = runTest {
        val errors = mutableListOf<Throwable>()
        val owner = CoroutineScope(SupervisorJob() + StandardTestDispatcher(testScheduler) +
            CoroutineExceptionHandler { _, error -> errors += error })
        try {
            val repository = Repository("room")
            val original = InboxConversations(repository, "old-pairing", owner, ReplyComposers(repository, owner))
            runCurrent()
            assertEquals(listOf("room"), original.state.value.rows.map { it.key.thread })
            repository.conversationInbox.value = original
            val source = CapsuleInboxSource(shell(repository), owner, {})
            runCurrent()
            assertTrue(errors.joinToString("\n") { it.stackTraceToString() }, errors.isEmpty())
            assertEquals(1, source.content.value.entries.size)
            val id = source.content.value.entries.single().id
            source.select(id)
            source.shown(true)
            runCurrent()
            assertEquals("Body room", source.content.value.messages.single().text)
            source.shown(false)
            runCurrent()

            val replacementRepository = Repository("room", bodyPrefix = "Replacement")
            val replacement = InboxConversations(replacementRepository, "new-pairing", owner, ReplyComposers(replacementRepository, owner))
            runCurrent()
            assertEquals("new-pairing", replacement.state.value.rows.single().key.scope)
            repository.conversationInbox.value = replacement
            runCurrent()
            source.shown(true)
            runCurrent()

            assertTrue("Replacing the controller must not pass a stale scoped key to an uncaught coroutine:\n" +
                errors.joinToString("\n") { it.stackTraceToString() }, errors.isEmpty())
            source.select(source.content.value.entries.single().id)
            runCurrent()
            assertEquals("Replacement room", source.content.value.messages.single().text)
            assertTrue(source.content.value.canSend)
        } finally { owner.cancel() }
    }

    private class Repository(vararg feeds: String, private val bodyPrefix: String = "Body") : InboxRepository by unused() {
        override val snapshot = MutableStateFlow(InboxSnapshot(records = feeds.map { feed ->
            InboxRecord(InboxItem("item-$feed", "account", "group", title = feed, tier = "action", version = "v1"),
                source = "lark", feedId = feed)
        }))
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
        override val messageReads = MutableStateFlow<Set<InboxMessageRead>>(emptySet())
        override fun conversation(feedId: String, startPosition: Long?) = MutableStateFlow(LarkConversation(title = feedId,
            messages = listOf(LarkMessage("message-$feedId", "sender", "Sender", 1, parts = listOf(LarkPart.Text("$bodyPrefix $feedId")))),
            verified = true, loading = false))
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun replyStatus(target: ReplyTarget, clientId: String) = ReplyDelivery.Ready
        override fun replyUpdates(target: ReplyTarget) = emptyFlow<Unit>()
    }

    private fun shell(repository: InboxRepository): ShellSession {
        val assistant = object : AssistantConversations by unused() {
            override val assistant = MutableStateFlow(AssistantSnapshot())
        }
        val preferences = AgentsPreferences(object : DataStore<Preferences> {
            override val data = MutableStateFlow(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
                transform(data.value).also { data.value = it }
        }, object : HubPreferences by unused() {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
        })
        return ShellSession("paired", unused(), unused(), unused(), unused(), unused(), assistant, repository, preferences)
    }

    companion object {
        private inline fun <reified T> unused(): T = Proxy.newProxyInstance(T::class.java.classLoader, arrayOf(T::class.java)) { proxy, method, args ->
            when (method.name) {
                "toString" -> "Unused ${T::class.java.simpleName}"
                "hashCode" -> System.identityHashCode(proxy)
                "equals" -> proxy === args?.firstOrNull()
                else -> error("Unexpected ${T::class.java.simpleName}.${method.name}")
            }
        } as T
    }
}
