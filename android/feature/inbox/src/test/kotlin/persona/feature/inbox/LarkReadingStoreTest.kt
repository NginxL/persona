package persona.feature.inbox

import persona.core.chatlist.ChatReadingKey
import persona.core.chatlist.ChatReadingPosition
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.inbox.*

@OptIn(ExperimentalCoroutinesApi::class)
class LarkReadingStoreTest {
    @Test fun pairingCancellationDropsQueuedAndLateWritesButPageDisposalKeepsAnchors() = runTest {
        var document: String? = null
        val storage = object : InboxStateStorage {
            override suspend fun read() = document
            override suspend fun write(value: String) { document = value }
        }
        val job = SupervisorJob(backgroundScope.coroutineContext[Job])
        val owner = CoroutineScope(backgroundScope.coroutineContext + job)
        val store = LarkReadingStore.open(storage, "hub/generation", owner)
        val key = ChatReadingKey("hub/generation", "lark:feed")
        store.write(key, ChatReadingPosition("row", 7, false, setOf("expanded")))
        runCurrent()
        assertNotNull(document)
        val restored = LarkReadingStore.open(storage, "hub/generation", owner)
        assertEquals(store.read(key), restored.read(key))
        store.write(key, ChatReadingPosition("queued", 9, false, emptySet()))
        job.cancelAndJoin()
        document = null
        store.write(key, ChatReadingPosition("late", 10, false, emptySet()))
        runCurrent()
        assertNull(document)
    }
}
