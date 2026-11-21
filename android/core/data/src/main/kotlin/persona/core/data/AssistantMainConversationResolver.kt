package persona.core.data

import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.MainConversationResolver
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.decodeFromJsonElement

/**
 * Use the immutable transport for the same pairing as the outbox, never a mutable connection. This phone's main queue
 * is the default manager's (docs/assistant/managers.md step 4): its lookups name the default whatever the assistant page shows,
 * so a message waiting in it never goes to another manager's main conversation.
 */
class AssistantMainConversationResolver(private val transport: InboxAssistantTransport) : MainConversationResolver {
    private val lock = Mutex()

    override suspend fun findMainConversation(): SessionKey? = lock.withLock { lookup() }

    override suspend fun createMainConversation(): SessionKey = createMainConversation(null)

    override suspend fun createMainConversation(via: String?): SessionKey = lock.withLock {
        // Another entry point or device may have installed the main pointer since the first lookup.
        lookup() ?: JsonCodec.decodeFromJsonElement<Session>(transport.createConversationOf(null, true, null, via).getValue("session")).key
    }

    private suspend fun lookup(): SessionKey? {
        val config = transport.assistantOf(null)
        val machine = config.string("machine_id")
        val main = config.string("main")
        return if (machine.isNotBlank() && main.isNotBlank()) SessionKey(machine, main) else null
    }
}
