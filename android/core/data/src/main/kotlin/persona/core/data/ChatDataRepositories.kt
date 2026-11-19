package persona.core.data

import android.content.Context
import androidx.room.Room
import persona.core.contracts.HubScope
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatTransport
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.HubPreferencesTransport
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.stateIn
import java.io.File

/** Pairing-scoped lifetime; callers replace the entire owner when switching hubs. */
class ChatDataRepositories private constructor(
    items: ChatItemsCache,
    snapshotDirectory: File,
    transport: ChatTransport,
    connection: HubConnection,
    parentScope: CoroutineScope,
    foreground: StateFlow<Boolean>,
    preferenceTransport: HubPreferencesTransport?,
    private val ownedDatabase: ItemsDatabase? = null,
    private val legacyDirectory: File? = null,
    transcript: (suspend (SessionKey, List<Item>) -> Unit)? = null,
    awaitingTranscript: Flow<Set<SessionKey>> = emptyFlow(),
    reconcileHeldRetries: (suspend (Session, Session) -> Unit)? = null,
    continuityFactory: (String, (ContinuityAdapter.Applied) -> Unit) -> ContinuityAdapter = ContinuityAdapters::create,
) {
    /** Reuses the pairing's database and lifetime; closing a view never discards its cache. */
    constructor(
        data: ScopedHubData,
        transport: ChatTransport,
        connection: HubConnection,
        foreground: StateFlow<Boolean>,
        preferenceTransport: HubPreferencesTransport? = null,
        reconcileHeldRetries: (suspend (Session, Session) -> Unit)? = null,
        continuityFactory: (String, (ContinuityAdapter.Applied) -> Unit) -> ContinuityAdapter = ContinuityAdapters::create,
    ) : this(data.items, File(data.noBackupDirectory, "lists"), transport, connection, data.coroutineScope,
        foreground, preferenceTransport, transcript = data.messages::observeTranscript,
        awaitingTranscript = data.messages.awaitingTranscript, reconcileHeldRetries = reconcileHeldRetries,
        continuityFactory = continuityFactory) {
        data.checkActive()
        data.openWindowReconciliation.register(scope, chats::reconcileOpened)
        data.messages.connectActivity { key ->
            sessions.sessions.value.firstOrNull { it.key == key }?.status in setOf("working", "blocked")
        }
        data.messages.connectSendReceipts(connection, sessions.sessions, chats::withSendSubscription)
    }

    @Deprecated("Assemble with ScopedHubData so all stores share the pairing lifetime")
    constructor(
        context: Context,
        hubScope: HubScope,
        transport: ChatTransport,
        connection: HubConnection,
        parentScope: CoroutineScope,
        foreground: StateFlow<Boolean>,
        preferenceTransport: HubPreferencesTransport? = null,
    ) : this(openLegacyCache(context, hubScope), legacyDirectory(context, hubScope),
        hubScope, transport, connection, parentScope, foreground, preferenceTransport)

    private constructor(
        database: ItemsDatabase,
        directory: File,
        hubScope: HubScope,
        transport: ChatTransport,
        connection: HubConnection,
        parentScope: CoroutineScope,
        foreground: StateFlow<Boolean>,
        preferenceTransport: HubPreferencesTransport?,
    ) : this(SessionItemsCache(database, hubScope), File(directory, "lists"), transport, connection, parentScope,
        foreground, preferenceTransport, database, directory)

    private val job = SupervisorJob(parentScope.coroutineContext[Job])
    private val scope = CoroutineScope(parentScope.coroutineContext + job + Dispatchers.IO)
    val snapshots = ListSnapshotStore(snapshotDirectory)
    val sessions = HubSessionsRepository(transport, connection, scope, snapshots, reconcileHeldRetries)
    val preferences = preferenceTransport?.let { remote ->
        HubPreferencesRepository(remote, scope, snapshots) { path ->
            sessions.sessions.value.firstOrNull { it.key.path == path }?.let { session ->
                session.spaceName?.let { "${session.machineId}/$it" }
            }
        }
    }
    val interactions = HubInteractionCenter(transport, connection, scope, snapshots, sessions.sessions)
    /** [sessions] as the pages show them: an agent whose card was just answered here is at work (#4525). */
    val shownSessions: StateFlow<List<Session>> = answeredSessions(sessions.sessions, interactions)
        .stateIn(scope, SharingStarted.Eagerly, sessions.sessions.value)
    /** [sessions] with [shownSessions] as its list, for the pages that take a repository (the Persona tab). */
    val shownRepository = sessions.showing(shownSessions)
    val chats = HubChatRepository(transport, connection, items, sessions, scope,
        foreground, seen = preferences?.let { repository -> { key ->
            // Keep the same local read snapshot used by the Agents list, not only the hub transport's receipt.
            repository.markSeen(key, sessions.sessions.value.firstOrNull { it.key == key }
                ?.let { "${it.updatedAt.orEmpty()}|${it.rev ?: ""}" }.orEmpty())
        } }, transcript = transcript,
        awaitingTranscript = awaitingTranscript, releaseAfterMillis = CLOSED_CHAT_RELEASE_MILLIS, continuityFactory = continuityFactory)

    suspend fun close(discard: Boolean = false) = withContext(NonCancellable + Dispatchers.IO) {
        job.cancelAndJoin()
        ownedDatabase?.close()
        if (discard) legacyDirectory?.deleteRecursively()
    }

    companion object {
        /** A chat left for another tab or page and reopened within this stays live instead of reopening cold. */
        private const val CLOSED_CHAT_RELEASE_MILLIS = 60_000L
        // Snapshots must survive cache trimming, so the legacy owner keeps them out of cacheDir.
        private fun legacyDirectory(context: Context, scope: HubScope) =
            File(context.noBackupFilesDir, "chat-data/${scope.path}")

        private fun openLegacyCache(context: Context, scope: HubScope): ItemsDatabase {
            val directory = legacyDirectory(context, scope).apply { mkdirs() }
            return Room.databaseBuilder(context.applicationContext, ItemsDatabase::class.java,
                File(directory, "items.db").absolutePath).addMigrations(ItemsDatabase.MIGRATION_1_2, ItemsDatabase.MIGRATION_3_4, ItemsDatabase.MIGRATION_4_5, ItemsDatabase.MIGRATION_5_6, ItemsDatabase.MIGRATION_6_7).build()
        }
    }
}
