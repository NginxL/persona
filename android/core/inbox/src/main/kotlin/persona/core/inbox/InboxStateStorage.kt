package persona.core.inbox

/** Bind these operations to the shared encrypted store for one HubScope. Writes must be atomic. */
interface InboxStateStorage {
    suspend fun read(): String?
    suspend fun write(value: String)
}
