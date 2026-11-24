package persona.core.data

import android.content.Context
import android.content.ContextWrapper
import androidx.datastore.core.DataStore
import androidx.room.Room
import java.io.File
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.EndpointOverrides
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import persona.core.inbox.InboxHandOffRequest

/** A group merge carries an unsent hand-off's durable identity and item route with its outbox row. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxHandOffGroupMergeTest {
    @Test fun pendingHandOffMetadataSurvivesAdoptionBeforeAccepted() = runBlocking {
        val base = RuntimeEnvironment.getApplication()
        val root = File(base.cacheDir, "handoff-group-merge-${java.util.UUID.randomUUID()}").apply { mkdirs() }
        val context = object : ContextWrapper(base) {
            override fun getApplicationContext(): Context = this
            override fun getFilesDir() = File(root, "files").apply { mkdirs() }
            override fun getCacheDir() = File(root, "cache").apply { mkdirs() }
            override fun getNoBackupFilesDir() = File(root, "no-backup").apply { mkdirs() }
        }
        val parent = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val store = HubDataStore(context, parent) { _, _ -> MergeHub() }
        val originScope = HubScope("origin", "generation")
        val targetScope = HubScope("target", "generation")
        var source: PersistentDatabase? = null
        var target: ScopedHubData? = null
        val targetDatabase = Room.inMemoryDatabaseBuilder(base, PersistentDatabase::class.java).allowMainThreadQueries().build()
        val cacheDatabase = Room.inMemoryDatabaseBuilder(base, ItemsDatabase::class.java).allowMainThreadQueries().build()
        try {
            val request = InboxHandOffRequest("item", "Handle this", "laptop", "agent",
                "merge-client", targetTitle = "Example agent")
            val pending = InboxAgentHandOff(originScope.path, request, "hub-item", manager = "manager-2")
            val originJournal = InboxReplyJournal(
                File(context.noBackupFilesDir, "hub-data/${originScope.path}/inbox-replies"), originScope.path)
            val sourceFile = File(context.filesDir, "hub-data/${originScope.path}/data.db").apply { parentFile!!.mkdirs() }
            source = Room.databaseBuilder(base, PersistentDatabase::class.java, sourceFile.absolutePath)
                .allowMainThreadQueries().build()
            RoomOutboxStore(checkNotNull(source), originScope, originJournal).enqueue(OutboxEntry(
                request.operationId, pending.session, request.text, createdAt = 1).withAgentHandOff(pending))
            source.close()
            source = null

            // This is the production closed-group read, including its journal, not a hand-built UnsentData fixture.
            val unsent = store.unsentAt(originScope)
            assertEquals(1, unsent.messageCount)

            val targetJournal = InboxReplyJournal(
                File(context.noBackupFilesDir, "hub-data/${targetScope.path}/inbox-replies"), targetScope.path)
            val hub = MergeHub()
            val owner = CoroutineScope(parent.coroutineContext + SupervisorJob(parent.coroutineContext[kotlinx.coroutines.Job]))
            val targetStore = RoomOutboxStore(targetDatabase, targetScope, targetJournal)
            target = ScopedHubData(targetScope, credentials("target"),
                OutboxMessageSender(targetScope, targetStore, hub, owner), DraftStore(targetDatabase, targetScope),
                SessionItemsCache(cacheDatabase, targetScope), owner, targetDatabase, cacheDatabase,
                File(root, "target-files").apply { mkdirs() }, File(root, "target-cache").apply { mkdirs() },
                File(root, "target-no-backup").apply { mkdirs() },
                EndpointOverridesStore(EmptyEndpointStore(), EndpointOverrides()), inboxReplies = targetJournal)
            assertEquals(1, checkNotNull(target).adopt(unsent))

            val moved = targetJournal.read(targetScope.path).agentHandOffs.single()
            assertEquals(targetScope.path, moved.scope)
            assertEquals(request, moved.request)
            assertEquals("hub-item", moved.hubItemId)
            assertEquals("manager-2", moved.manager)
            assertFalse(moved.delivered)
            assertFalse(moved.recorded)

            // The adopted row can still publish genuine Accepted evidence under its original client and item route.
            hub.readyToSend.value = true
            withTimeout(5_000) {
                while (targetJournal.read(targetScope.path).agentHandOffs.single().delivered.not()) delay(10)
            }
            assertEquals(listOf(request.operationId), hub.requests.map { it.clientMessageId })
            assertTrue(targetJournal.read(targetScope.path).agentHandOffs.single().delivered)
        } finally {
            source?.close()
            target?.close() ?: run {
                targetDatabase.close()
                cacheDatabase.close()
            }
            store.close()
            parent.cancel()
        }
    }

    private fun credentials(hub: String) = HubCredentials("https://$hub.example.com", hub, null, emptyList(), "token-$hub")

    private class EmptyEndpointStore : DataStore<EndpointOverridesDocument> {
        override val data: Flow<EndpointOverridesDocument> = flowOf(EndpointOverridesDocument())
        override suspend fun updateData(transform: suspend (EndpointOverridesDocument) -> EndpointOverridesDocument) =
            transform(EndpointOverridesDocument())
    }

    private class MergeHub : MessageTransport {
        override val readyToSend = MutableStateFlow(false)
        override val supportsClientOps = false
        val requests = mutableListOf<MessageRequest>()
        override suspend fun send(request: MessageRequest): MessageTransportResult {
            requests += request
            return MessageTransportResult.Accepted
        }
    }
}
