package persona.core.data

import persona.core.contracts.CoreText.Key
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.data.ContinuityAdapter.Input
import persona.core.data.ContinuityAdapter.Batch
import persona.core.domain.ContinuitySync.Action
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.sync.withPermit
import kotlinx.serialization.json.*
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Owns continuity for one pairing. Message bodies synchronize only for observed chats, chats closed within
 * [releaseAfterMillis] (still subscribed, so switching back is warm) and chats whose sent messages
 * [awaitingTranscript] still waits to see. Four other idle entries stay for warm navigation, the most recently closed
 * kept; at most 64 entries exist and a 65th simultaneous observer is rejected. These are policy limits, not measurements.
 */
class HubChatRepository(
    private val transport: ChatTransport,
    private val connection: HubConnection,
    private val cache: ChatItemsCache,
    private val sessions: SessionsRepository,
    private val scope: CoroutineScope,
    private val foreground: StateFlow<Boolean> = MutableStateFlow(true),
    private val seen: (suspend (SessionKey) -> Unit)? = null,
    /** Told the session's transcript whenever it gains items (the outbox finds the messages sent from here in it). */
    private val transcript: (suspend (SessionKey, List<Item>) -> Unit)? = null,
    /** Sessions with a sent message not yet seen in their transcript: they keep syncing while closed. */
    private val awaitingTranscript: Flow<Set<SessionKey>> = emptyFlow(),
    /** How long a closed chat stays subscribed; 0 unsubscribes at once. */
    private val releaseAfterMillis: Long = 0,
    /** Streamed text publishes its first chunk at once, then at most once per this window; the turn's end publishes at once. */
    private val deltaPublishWindowMillis: Long = 50,
    private val continuityFactory: (String, (ContinuityAdapter.Applied) -> Unit) -> ContinuityAdapter = ContinuityAdapters::create,
) : ChatRepository, StructuralChatRepository {
    private class Entry(val key: SessionKey, val scope: CoroutineScope) {
        lateinit var continuity: ContinuityAdapter
        var appliedContinuity = ContinuityAdapter.State()
        val reducer get() = if (continuity.asynchronous) appliedContinuity else continuity.state
        val patches by lazy { kotlinx.coroutines.channels.Channel<ContinuityAdapter.Applied>(kotlinx.coroutines.channels.Channel.UNLIMITED) }
        val realtime = ChatRealtimeState(key)
        val operations = mutableMapOf<Long, CompletableDeferred<List<Action>>>()
        var nextOperation = 0L
        val activeFolds = mutableSetOf<String>()
        val inputs = Mutex()
        /** Transport epoch follows submitted history, not the possibly older applied frame. Guarded by [inputs]. */
        var submittedHistory: String? = null
        var historyGeneration = 0L
        val older = Mutex()
        val lock = Mutex()
        val requests = Mutex()
        val saving = Mutex()
        /** An items event's save is waiting for [saving]; later events ride on it (it reads the state once it starts). */
        val persistPending = AtomicBoolean(false)
        val reconciliations = mutableMapOf<CompletableDeferred<Unit>, Long>()
        var followupCoverage: Int? = null
        var followupPages = 0
        val ready = CompletableDeferred<Unit>()
        val state = MutableStateFlow(ChatSnapshot(verified = false))
        var subscribed = false
        var subscriptionReady = CompletableDeferred<Unit>()
        var seenHistory: String? = null
        var references = 0
        /** Closed, but still subscribed until [release] runs. */
        var lingering = false
        var release: Job? = null
        /** Order of the last close; the most recently closed idle entries are kept. */
        var closedAt = 0L
        var awaited = false
        var opening: Job? = null
        var timer: Job? = null
        var lastSeen: Item? = null
        var previousSession: Session? = null
        var currentSessions: List<Session>? = null
        var machineOnline: Boolean? = null
        var closeConfirmed = false
        var listedGone = false
        var stopRequested = false
        var turnDurationSeconds: Long? = null
        var durationAfterSeq: Int = 0
        var lastUserId: String? = null
        var eventError: String? = null
        var errorAfterOperation = 0L
        var observedTranscript: List<Item>? = null
        var durationItems: List<Item>? = null
        var durations: Map<String, RecordedDuration> = emptyMap()
        var durationSeconds: Map<String, Long> = emptyMap()
        /** Back online or reopened and no live delta since: the empty delta that ends a turn may have been missed. */
        var reconnected = false
        /** The open streaming window (see [scheduleDeltaPublish]); null when none is open. Guarded by [lock]. */
        var deltaWindow: Job? = null
        /** A delta arrived since the window's last publish. Guarded by [lock]. */
        var deltaDirty = false
        /**
         * Nothing has come from the hub for this chat yet: its first page is on its way, or was refused as not_loaded
         * and waits for the watch ([notLoadedWait]). With nothing cached the chat shows its loading state meanwhile, not
         * an empty chat (#8321).
         */
        var firstPagePending = true
        /** The open's first read was refused as not_loaded ([awaitWatch]): ends the wait if no items come first. */
        var notLoadedWait: Job? = null
    }

    private val entries = LinkedHashMap<SessionKey, Entry>(64, 0.75f, true)
    private val images = ImageBytesCache()
    private val backgroundSlots = Semaphore(3)
    private var closes = 0L

    init {
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.events.collect { event ->
                when (event) {
                    is ItemsEvent -> existing(SessionKey(event.machineId, event.sessionId))?.let { entry ->
                        entry.ready.await()
                        synchronized(entries) { if (entry.subscribed) entry.subscriptionReady.complete(Unit) }
                        val input = Input.Push(Batch(event.items, event.history,
                            event.fromSeq, event.toSeq, event.backlog, event.hasMore, event.moreAfter))
                        val actions = if (entry.continuity.asynchronous) acceptAsync(entry, input)?.requests.orEmpty()
                        else pushSynchronous(entry, input)
                        dispatch(entry, actions)
                        if (!entry.continuity.asynchronous && entry.persistPending.compareAndSet(false, true))
                            entry.scope.launch { persist(entry) }
                    }
                    is DeltaEvent -> existing(SessionKey(event.machineId, event.sessionId))?.let { entry ->
                        entry.ready.await()
                        entry.lock.withLock {
                            entry.realtime.delta(event, entry.reducer.items)
                            entry.reconnected = false
                            if (!event.turnFinished) {
                                entry.turnDurationSeconds = null
                                entry.durationAfterSeq = entry.reducer.items.lastOrNull()?.seq ?: 0
                                scheduleDeltaPublish(entry)
                            }
                            else {
                                entry.stopRequested = false
                                // The turn's end publishes now; a window left open would hold the next turn's first chunk.
                                entry.deltaWindow?.cancel()
                                entry.deltaWindow = null
                                requestSync(entry.key)
                                publish(entry)
                            }
                        }
                    }
                    is ScreenEvent -> update(SessionKey(event.machineId, event.sessionId)) { entry ->
                        entry.realtime.screen(event, entry.reducer.items)
                        if (event.status in listOf("idle", "done", "offline", "blocked")) entry.stopRequested = false
                    }
                    // Every open session records the call, but only the one showing its row is rebuilt (#5341).
                    is ToolCallEvent -> retainedEntries().forEach { entry -> entry.lock.withLock {
                        if (entry.realtime.tool(event, entry.reducer.items)) publish(entry)
                    } }
                    is HookEvent -> update(SessionKey(event.machineId, event.sessionId)) { entry ->
                        if (event.event == "Stop") {
                            entry.stopRequested = false
                            entry.realtime.finish()
                        }
                        requestSync(entry.key)
                    }
                    // The hub's own failure for the chat (a backlog it could not read): said, and a first page no longer awaited.
                    is ErrorEvent -> event.sessionKey?.let { key -> update(key) {
                        if (it.continuity.asynchronous) recordAsyncError(it, event.error.ifBlank { DataText.of(Key.UnknownError) })
                        else it.eventError = event.error.ifBlank { DataText.of(Key.UnknownError) }
                        firstPageIn(it)
                    } }
                    is SessionsEvent -> retainedEntries().filter { it.key.machineId == event.machineId }.forEach { entry ->
                        entry.lock.withLock {
                            if (sessions !is HubSessionsRepository && connection.state.value == ConnectionState.Online)
                                entry.currentSessions = event.sessions.map { it.copy(machineId = event.machineId) }
                            publish(entry)
                        }
                    }
                    is MachinesEvent -> retainedEntries().forEach { entry -> entry.lock.withLock {
                        if (sessions !is HubSessionsRepository && connection.state.value == ConnectionState.Online)
                            entry.machineOnline = event.machines.firstOrNull { it.id == entry.key.machineId }?.online
                        publish(entry)
                    } }
                    is RevsEvent -> event.revs.keys.forEach { sid -> requestSync(SessionKey(event.machineId, sid)) }
                    else -> Unit
                }
            }
        }
        scope.launch {
            connection.state.collect { state ->
                val active = synchronized(entries) { entries.values.filter { it.references > 0 || it.lingering } }
                if (state == ConnectionState.Online) {
                    active.forEach { entry -> entry.scope.launch {
                        entry.ready.await()
                        if (entry.continuity.asynchronous) {
                            entry.lock.withLock { entry.reconnected = true }
                            dispatch(entry, acceptAsync(entry, Input.Reconnect)?.requests.orEmpty())
                        } else dispatch(entry, entry.lock.withLock {
                            entry.reconnected = true
                            entry.continuity.accept(Input.Reconnect)!!.requests
                        })
                        catchUp(entry)
                    } }
                } else retainedEntries().forEach { entry ->
                    synchronized(entries) { entry.subscribed = false; entry.subscriptionReady = CompletableDeferred() }
                    entry.lock.withLock {
                        entry.currentSessions = null
                        entry.machineOnline = null
                        publish(entry, verified = false)
                    }
                }
            }
        }
        scope.launch {
            var previous = emptyMap<SessionKey, Long?>()
            val verified = (sessions as? HubSessionsRepository)?.verifiedLists ?: MutableStateFlow(VerifiedSessionLists())
            combine(sessions.sessions, verified) { list, _ -> list }.collect { list ->
                list.forEach { session ->
                    if (session.rev != previous[session.key]) requestSync(session.key)
                }
                retainedEntries().forEach { entry -> entry.lock.withLock {
                    publish(entry)
                } }
                previous = list.associate { it.key to it.rev }
            }
        }
        scope.launch {
            foreground.collect { if (it && connection.state.value == ConnectionState.Online) {
                retainedEntries().forEach { requestSync(it.key) }
            } }
        }
        scope.launch {
            awaitingTranscript.distinctUntilChanged().collect { keys ->
                synchronized(entries) {
                    entries.values.forEach { it.awaited = it.key in keys }
                    keys.filter { it !in entries && (entries.size < 64 || entries.values.any { e -> e.references == 0 }) }
                        .forEach { entry(it).awaited = true }
                    trimIdleEntries()
                }
                keys.forEach(::requestSync)
            }
        }
    }

    /** Reconcile only observed sessions; HTTP catch-up drains every forward page. */
    suspend fun reconcileOpened() = coroutineScope {
        val active = synchronized(entries) { entries.values.filter { it.references > 0 } }
        active.map { entry -> async {
            entry.ready.await()
            entry.opening?.join()
            entry.timer?.cancelAndJoin()
            synchronized(entries) { entry.subscribed = false }
            if (entry.continuity.asynchronous) reconcileAsync(entry)
            else {
                entry.lock.withLock {
                    entry.continuity.accept(Input.FillFailed)
                    publish(entry, verified = false)
                    dispatch(entry, entry.continuity.accept(Input.Reconnect)!!.requests)
                }
                catchUp(entry)
                entry.state.first { it.verified }
            }
        } }.awaitAll()
        Unit
    }

    /** Wrap the paired hub's action service so confirmed operations update the same snapshot. */
    fun trackActions(delegate: ChatActions): ChatActions = object : ChatActions {
        override suspend fun isMuted(session: Session) = delegate.isMuted(session)
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult {
            val result = delegate.execute(session, action)
            recordAction(session, action, result)
            return result
        }
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult {
            val result = delegate.verify(session, action)
            recordAction(session, action, result, verified = true)
            return result
        }
        override suspend fun editQueue(session: Session, keep: List<Int>) = delegate.editQueue(session, keep)
        override suspend fun sendQueuedNow(session: Session) = delegate.sendQueuedNow(session)
        override suspend fun retryUndelivered(session: Session, messageId: String) = delegate.retryUndelivered(session, messageId)
        override suspend fun deleteUndelivered(session: Session, messageId: String) = delegate.deleteUndelivered(session, messageId)
        override suspend fun info(session: Session) = delegate.info(session)
        override suspend fun completeRow(session: Session) = delegate.completeRow(session)
        override suspend fun childRows(session: Session) = delegate.childRows(session)
        override suspend fun childPage(session: Session, since: String?) = delegate.childPage(session, since)
        override suspend fun interactionHistory(session: Session) = delegate.interactionHistory(session)
        override suspend fun restoreModel(session: Session) = delegate.restoreModel(session)
        override suspend fun catalogPrices(ids: List<String>) = delegate.catalogPrices(ids)
        override suspend fun fork(session: Session, atItemId: String): String {
            // A finished streamed reply keeps showing its draft id; the node only knows the transcript one (#5305).
            val entry = existing(session.key)
            return delegate.fork(session, entry?.lock?.withLock { entry.realtime.canonicalId(atItemId) } ?: atItemId)
        }
        override suspend fun rewind(session: Session, target: RewindTarget, restore: RewindRestore) = delegate.rewind(session, target, restore)
    }

    /** The composer shares interruption state with menu actions. Failed requests leave it unchanged. */
    fun trackComposer(delegate: ChatComposerService): ChatComposerService = object : ChatComposerService by delegate {
        override suspend fun upload(session: SessionKey, name: String, file: java.io.File, destination: String): String =
            delegate.upload(session, name, file, destination)

        override suspend fun interrupt(session: SessionKey) {
            delegate.interrupt(session)
            update(session) { it.stopRequested = true }
        }
    }

    /** Only recorded durations may be supplied; never derive them from message timestamps. */
    suspend fun recordTurnDuration(session: SessionKey, seconds: Long?) {
        require(seconds == null || seconds >= 0)
        update(session) { it.turnDurationSeconds = seconds }
    }

    private suspend fun recordAction(session: Session, action: ChatAction, result: ChatActionResult, verified: Boolean = false) {
        update(session.key) { entry ->
            if (entry.previousSession == null) entry.previousSession = session
            if (result == ChatActionResult.Done) when (action) {
                ChatAction.Interrupt -> entry.stopRequested = !verified
                ChatAction.Close -> entry.closeConfirmed = true
                else -> Unit
            }
        }
    }

    /** Existing consumers keep seeing each delta as a complete, immutable snapshot. */
    @OptIn(ExperimentalCoroutinesApi::class)
    override fun observe(session: SessionKey): Flow<ChatSnapshot> = observeStructure(session).flatMapLatest { snapshot ->
        val live = snapshot.liveText
        if (live.isEmpty()) flowOf(snapshot)
        else combine(live.map { (id, text) -> text.map { value -> id to value } }) { values ->
            val updates = values.toMap()
            snapshot.copy(items = snapshot.items.map { item ->
                updates[item.id]?.takeIf { it != item.text.orEmpty() }?.let { item.copy(text = it) } ?: item
            })
        }
    }

    override fun observeStructure(session: SessionKey): Flow<ChatSnapshot> = flow {
        cache.retain(session)
        try {
            val entry = synchronized(entries) {
                val entry = entry(session)
                entry.references++
                if (entry.references == 1) {
                    // Reopened while still subscribed: live events kept it current, no cold open needed.
                    val warm = entry.lingering && entry.subscribed
                    entry.release?.cancel()
                    entry.release = null
                    entry.lingering = false
                    if (!warm) entry.opening = entry.scope.launch {
                        entry.ready.await()
                        // Cold opens always verify against a folded latest page, including when cache exists.
                        entry.lock.withLock { entry.reconnected = true }
                        latest(entry, initial = true)
                        dispatch(entry, if (entry.continuity.asynchronous) acceptAsync(entry, Input.Reconnect)?.requests.orEmpty()
                            else entry.lock.withLock { entry.continuity.accept(Input.Reconnect)!!.requests })
                    }
                }
                entry
            }
            try { emitAll(entry.state) }
            finally {
                synchronized(entries) {
                    entry.references--
                    if (entry.references == 0) {
                        entry.closedAt = ++closes
                        if (releaseAfterMillis > 0) {
                            entry.lingering = true
                            entry.release = entry.scope.launch {
                                delay(releaseAfterMillis)
                                synchronized(entries) { if (entry.references == 0) release(entry) }
                            }
                        } else release(entry)
                    }
                }
            }
        } finally { withContext(NonCancellable) { cache.release(session) } }
    }

    /** Latest arrival, independent of the merged UI publish window. */
    internal fun liveText(session: SessionKey): String? =
        existing(session)?.let { entry -> entry.realtime.currentText(entry.reducer.items) }

    /** Keep this send subscribed before POST, including while the main queue is binding its first conversation. */
    suspend fun withSendSubscription(session: SessionKey, send: suspend () -> persona.core.contracts.api.MessageTransportResult): persona.core.contracts.api.MessageTransportResult = coroutineScope {
        val mounted = CompletableDeferred<Unit>()
        val observer = launch(start = CoroutineStart.UNDISPATCHED) { observe(session).collect { mounted.complete(Unit) } }
        try {
            // Older hubs may send no initial items event. Bound this barrier and retain the legacy HTTP/outbox fallback.
            withTimeoutOrNull(3_000) {
                mounted.await()
                val ready = synchronized(entries) {
                    val entry = entries[session] ?: return@synchronized null
                    if (!entry.subscribed) entry.subscribed = connection.send(buildJsonObject {
                        envelope("subscribe", session).forEach { (key, value) -> put(key, value) }
                        put("lazy_images", true); put("fold_work", true)
                    })
                    entry.subscriptionReady
                }
                ready?.await()
            }
            send()
        } finally { observer.cancelAndJoin() }
    }

    override suspend fun modeChanges(session: SessionKey): List<ModeChange> = transport.modeChanges(session)

    override suspend fun loadOlder(session: SessionKey) {
        val entry = existing(session) ?: return
        entry.ready.await()
        if (entry.continuity.asynchronous) { loadOlderAsync(entry); return }
        entry.requests.withLock {
            val (action, history) = entry.lock.withLock {
                val action = entry.continuity.accept(Input.LoadOlder)!!.requests.firstOrNull()?.also {
                    entry.state.update { it.copy(loadingOlder = true, error = null) }
                }
                action to entry.reducer.history
            }
            val fetch = action as? Action.FetchOlder ?: return
            try {
                val page = transport.items(session, ItemsQuery(beforeSeq = fetch.beforeSeq))
                entry.lock.withLock {
                    if (history != entry.reducer.history || page.history != null && history != null && page.history != history) {
                        entry.continuity.accept(Input.OlderFailed)
                    } else entry.continuity.accept(Input.Older(page.items, page.hasMore))
                    publish(entry)
                }
                persist(entry)
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                val missing = missingHistory(error) != null
                entry.lock.withLock {
                    if (missing && history == entry.reducer.history) {
                        entry.continuity.accept(Input.Older(emptyList(), hasMore = false))
                        publish(entry)
                    }
                    entry.state.update { it.copy(error = DataText.of(Key.ChatHistoryFailed)) }
                }
                if (missing) persist(entry)
            }
            finally { withContext(NonCancellable) { entry.lock.withLock {
                entry.continuity.accept(Input.OlderFailed)
                entry.state.update { it.copy(loadingOlder = false) }
            } } }
        }
    }

    private suspend fun loadOlderAsync(entry: Entry) {
        entry.older.withLock {
            var generation: Long? = null
            try {
                val initial = entry.inputs.withLock { entry.historyGeneration to entry.submittedHistory }
                generation = initial.first
                val history = initial.second
                val action = awaitActionsAsync(entry, Input.LoadOlder, generation)?.singleOrNull() as? Action.FetchOlder ?: return@withLock
                entry.lock.withLock { entry.state.update { it.copy(loadingOlder = true, error = null) } }
                val page = transport.items(entry.key, ItemsQuery(beforeSeq = action.beforeSeq))
                currentCoroutineContext().ensureActive()
                val stale = page.history != null && history != null && page.history != history
                if (!stale) awaitActionsAsync(entry, Input.Older(page.items, page.hasMore), generation)
                entry.lock.withLock { publish(entry) }
                persist(entry)
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                if (missingHistory(error) != null && generation != null) {
                    awaitActionsAsync(entry, Input.Older(emptyList(), hasMore = false), generation)
                    entry.lock.withLock { publish(entry) }
                    persist(entry)
                }
                entry.lock.withLock {
                    recordAsyncError(entry, DataText.of(Key.ChatHistoryFailed))
                    entry.state.update { it.copy(error = entry.eventError) }
                }
            }
            finally { withContext(NonCancellable) {
                acceptAsync(entry, Input.OlderFailed)
                entry.lock.withLock { entry.state.update { it.copy(loadingOlder = false) } }
            } }
        }
    }

    override suspend fun loadRange(session: SessionKey, fromSeq: Int, throughSeq: Int): List<Item> {
        require(fromSeq >= 0 && throughSeq >= fromSeq)
        val out = mutableListOf<Item>()
        var from = fromSeq
        repeat(20) {
            val page = transport.items(session, ItemsQuery(fromSeq = from, throughSeq = throughSeq))
            out += page.items
            val last = page.items.lastOrNull()?.seq
            if (!page.more || last == null || last >= throughSeq) return out.distinctBy { it.id }
            val next = last + 1
            if (next <= from) return out.distinctBy { it.id }
            from = next
        }
        return out.distinctBy { it.id }
    }

    override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> {
        require(fromSeq >= 0 && toSeq >= fromSeq)
        val entry = existing(session) ?: return emptyList()
        entry.ready.await()
        if (entry.continuity.asynchronous) return expandFoldAsync(entry, fromSeq, toSeq)
        return entry.requests.withLock {
            val fold = entry.lock.withLock { entry.reducer.items.firstOrNull {
                it.kind == "fold" && it.seq == fromSeq && it.fold?.throughSeq == toSeq
            } } ?: return@withLock emptyList()
            val history = entry.lock.withLock { entry.reducer.history }
            var actions = entry.lock.withLock { entry.continuity.accept(Input.OpenFold(fold.id))!!.requests }
            val work = mutableListOf<Item>()
            try {
                while (actions.isNotEmpty()) {
                    val action = actions.single() as Action.FetchRange
                    val page = transport.items(session, ItemsQuery(fromSeq = action.fromSeq, throughSeq = action.throughSeq))
                    check(page.items.none { it.kind == "fold" } && (!page.more ||
                        page.items.lastOrNull()?.seq?.let { it >= action.fromSeq && it < action.throughSeq } == true))
                    actions = entry.lock.withLock {
                        check(history == entry.reducer.history)
                        check(page.history == null || history == null || page.history == history)
                        work += page.items
                        entry.continuity.accept(Input.Range(page.items, page.more))!!.requests
                    }
                }
                entry.lock.withLock { publish(entry) }
                persist(entry)
                work.distinctBy { it.id }
            } finally { withContext(NonCancellable) { entry.lock.withLock { entry.continuity.accept(Input.RangeFailed()) } } }
        }
    }

    private suspend fun expandFoldAsync(entry: Entry, fromSeq: Int, toSeq: Int): List<Item> {
        val fold = entry.lock.withLock { entry.reducer.items.firstOrNull {
            it.kind == "fold" && it.seq == fromSeq && it.fold?.throughSeq == toSeq
        } } ?: return emptyList()
        if (!synchronized(entry.activeFolds) { entry.activeFolds.add(fold.id) }) return emptyList()
        // Downloaded items by id, latest page wins: a changed fold's refetch replaces its discarded first page.
        val work = LinkedHashMap<String, Item>()
        try {
            val (generation, history) = entry.inputs.withLock { entry.historyGeneration to entry.submittedHistory }
            var actions = awaitActionsAsync(entry, Input.OpenFold(fold.id), generation) ?: return emptyList()
            while (actions.isNotEmpty()) {
                val action = actions.single() as Action.FetchRange
                val page = transport.items(entry.key, ItemsQuery(fromSeq = action.fromSeq, throughSeq = action.throughSeq))
                currentCoroutineContext().ensureActive()
                check(page.items.none { it.kind == "fold" } && (!page.more ||
                    page.items.lastOrNull()?.seq?.let { it >= action.fromSeq && it < action.throughSeq } == true))
                entry.lock.withLock {
                    check(history == entry.reducer.history)
                    check(page.history == null || history == null || page.history == history)
                }
                page.items.forEach { work[it.id] = it }
                // A changed fold can request another page even when HTTP more=false. Only the applied Ack ends it.
                actions = awaitActionsAsync(entry, Input.Range(page.items, page.more, fold.id), generation) ?: return emptyList()
            }
            // Like the synchronous path, return what was downloaded even if a push removed the fold meanwhile.
            entry.lock.withLock { publish(entry) }
            persist(entry)
            return work.values.toList()
        } finally { withContext(NonCancellable) {
            try { acceptAsync(entry, Input.RangeFailed(fold.id)) }
            finally { synchronized(entry.activeFolds) { entry.activeFolds.remove(fold.id) } }
        } }
    }

    override suspend fun loadImage(session: SessionKey, itemId: String) =
        images.get("${session.path}:$itemId") { transport.image(session, itemId) }
    override suspend fun fullText(session: SessionKey, itemId: String): String {
        val entry = existing(session)
        val canonical = entry?.lock?.withLock { entry.realtime.canonicalId(itemId) } ?: itemId
        return transport.fullItem(session, canonical).text.orEmpty()
    }

    override suspend fun markSeen(session: SessionKey, itemId: String) {
        val entry = existing(session) ?: return
        entry.lock.withLock {
            if (!entry.state.value.verified || !foreground.value || connection.state.value != ConnectionState.Online) return
            val canonical = entry.realtime.canonicalId(itemId)
            val item = entry.reducer.items.firstOrNull { it.id == canonical } ?: return
            val last = entry.lastSeen
            if (last != null && compareValuesBy(item, last, { it.seq }, { part(it) }) <= 0) return
            val prior = entry.state.value
            entry.lastSeen = item
            entry.state.update { it.copy(lastSeenMessageId = entry.realtime.displayId(item.id)) }
            try {
                if (seen != null) seen.invoke(session)
                else check(connection.send(envelope("seen", session))) { "Read receipt was not sent" }
            } catch (failure: Exception) {
                entry.lastSeen = last
                entry.state.value = prior
                throw failure
            }
        }
    }

    private fun entry(key: SessionKey): Entry = synchronized(entries) {
        entries.getOrPut(key) {
            if (entries.size >= 64) {
                val victim = entries.values.firstOrNull { it.references == 0 }
                    ?: error("Too many active chat sessions")
                entries.remove(victim.key)
                victim.ready.complete(Unit)
                victim.scope.cancel()
            }
            Entry(key, CoroutineScope(scope.coroutineContext + SupervisorJob(scope.coroutineContext[Job]))).also { entry ->
                entry.continuity = continuityFactory(key.path) { result -> entry.patches.trySend(result) }
                if (entry.continuity.asynchronous) entry.scope.launch { consumeAsync(entry) }
                entry.scope.coroutineContext[Job]?.invokeOnCompletion { entry.continuity.close() }
                entry.scope.launch {
                try {
                    val cached = cache.load(key)
                    val input = Input.Restore(ContinuityAdapter.State(cached.items, cached.history, cached.covered, cached.hasMore))
                    if (entry.continuity.asynchronous) {
                        acceptAsync(entry, input)
                        entry.lock.withLock { publish(entry, verified = false) }
                    } else entry.lock.withLock {
                        entry.continuity.accept(input)
                        publish(entry, verified = false)
                    }
                } catch (cancelled: CancellationException) { throw cancelled }
                catch (_: Exception) { /* The latest page can recover an unavailable disk cache. */ }
                finally { entry.ready.complete(Unit) }
            } }
        }
    }

    private fun existing(key: SessionKey): Entry? = synchronized(entries) { entries[key] }

    private fun retainedEntries(): List<Entry> = synchronized(entries) { entries.values.toList() }

    /** Called holding [entries]: stops a closed chat's live updates. */
    private fun release(entry: Entry) {
        entry.lingering = false
        entry.release = null
        entry.opening?.cancel()
        entry.timer?.cancel()
        entry.notLoadedWait?.cancel() // the next open reads again, and waits again if it has to
        entry.subscribed = false
        connection.send(envelope("unsubscribe", entry.key))
        trimIdleEntries()
    }

    /** Metadata and unread counts come from the session list; closed chats need no message-body prefetch. */
    private fun trimIdleEntries() = synchronized(entries) {
        val idle = entries.values.filter { it.references == 0 && !it.lingering && !it.awaited }.sortedBy { it.closedAt }
        idle.dropLast(4).forEach { victim ->
            entries.remove(victim.key)
            victim.ready.complete(Unit)
            victim.scope.cancel()
        }
    }

    private suspend fun update(key: SessionKey, change: (Entry) -> Unit) {
        val entry = existing(key) ?: return
        entry.ready.await()
        entry.lock.withLock { change(entry); publish(entry) }
    }

    /**
     * Called holding [Entry.lock] after a streamed chunk was applied. The first chunk of a burst publishes at once; chunks
     * that arrive while the window is open only mark it dirty, and its end publishes the latest state once. The window
     * repeats while chunks keep arriving and closes after a quiet window, so a later chunk publishes at once again.
     */
    private fun scheduleDeltaPublish(entry: Entry) {
        entry.deltaDirty = true
        if (entry.deltaWindow?.isActive == true) return
        entry.deltaDirty = false
        publish(entry)
        entry.deltaWindow = entry.scope.launch {
            while (true) {
                delay(deltaPublishWindowMillis)
                val quiet = entry.lock.withLock {
                    if (!entry.deltaDirty) { entry.deltaWindow = null; true }
                    else { entry.deltaDirty = false; publish(entry); false }
                }
                if (quiet) return@launch
            }
        }
    }

    private fun requestSync(key: SessionKey) {
        if (connection.state.value != ConnectionState.Online) return
        val entry = synchronized(entries) {
            entries[key]?.takeIf { it.references > 0 || it.lingering || it.awaited } ?: return
        }
        entry.scope.launch {
            entry.ready.await()
            val actions = if (entry.continuity.asynchronous) {
                entry.lock.withLock { entry.followupCoverage = null; entry.followupPages = 0 }
                acceptAsync(entry, Input.HubChanged)?.requests.orEmpty()
            } else entry.lock.withLock { entry.continuity.accept(Input.HubChanged)!!.requests }
            dispatch(entry, actions)
        }
    }

    private suspend fun pushSynchronous(entry: Entry, input: Input.Push): List<Action> = entry.lock.withLock {
        val result = entry.continuity.accept(input)!!
        entry.eventError = null
        firstPageIn(entry)
        publish(entry, verified = result.verified && (input.batch.backlog || entry.state.value.verified))
        result.requests
    }

    private suspend fun consumeAsync(entry: Entry) {
        for (result in entry.patches) {
            if (result.diagnosticOnly) continue
            val continueFill = entry.lock.withLock {
                entry.appliedContinuity = result.state
                if (result.contentApplied) {
                    if (result.operations.any { it.content && it.id > entry.errorAfterOperation }) entry.eventError = null
                    firstPageIn(entry)
                }
                if (result.stateChanged || result.contentApplied)
                    publish(entry, verified = result.verified && entry.eventError == null)
                val settled = result.afterApplied || result.reloadApplied
                if (!settled) return@withLock false
                val overtaken = result.state.items.any { it.seq > result.state.covered }
                val hasFill = result.requests.any { it is Action.FetchAfter || it is Action.FetchLatest }
                val follow = !hasFill && (overtaken || entry.continuity.behind) &&
                    entry.followupPages < 8 && (entry.followupCoverage == null || result.state.covered > entry.followupCoverage!!)
                if (follow) { entry.followupPages++; entry.followupCoverage = result.state.covered }
                if (result.verified && entry.eventError == null && !overtaken && !hasFill) {
                    entry.followupPages = 0; entry.followupCoverage = null
                    val receipt = result.operations.filter { it.fillResponse }.maxOfOrNull { it.id }
                    if (receipt != null) entry.reconciliations.entries.removeAll { (ready, startedAfter) ->
                        (receipt > startedAfter).also { if (it) ready.complete(Unit) }
                    }
                }
                follow
            }
            result.operations.forEach { operation ->
                synchronized(entry.operations) { entry.operations[operation.id] }?.complete(operation.requests)
            }
            dispatch(entry, result.requests)
            if (continueFill) dispatch(entry, acceptAsync(entry, Input.ContinueFill)?.requests.orEmpty())
            if (result.stateChanged && entry.persistPending.compareAndSet(false, true)) entry.scope.launch { persist(entry) }
        }
    }

    private fun recordAsyncError(entry: Entry, error: String) {
        entry.eventError = error
        entry.errorAfterOperation = synchronized(entry.operations) { entry.nextOperation }
    }

    private suspend fun reconcileAsync(entry: Entry) {
        val completion = CompletableDeferred<Unit>()
        entry.lock.withLock {
            entry.reconciliations[completion] = synchronized(entry.operations) { entry.nextOperation }
            entry.followupCoverage = null; entry.followupPages = 0
            publish(entry, verified = false)
        }
        try {
            dispatch(entry, acceptAsync(entry, Input.Reconnect)?.requests.orEmpty())
            catchUpAsync(entry)
            completion.await()
        } finally { withContext(NonCancellable) {
            entry.lock.withLock { entry.reconciliations.remove(completion) }
        } }
    }

    private suspend fun acceptAsync(entry: Entry, input: Input, operation: Long? = null) =
        entry.inputs.withLock { acceptAsyncLocked(entry, input, operation) }

    /** Holding inputs. Commit metadata only after queue success. */
    private suspend fun acceptAsyncLocked(entry: Entry, input: Input, operation: Long?, onAccepted: () -> Unit = {}): ContinuityAdapter.Applied? {
        val id = operation ?: synchronized(entry.operations) { ++entry.nextOperation }
        val result = entry.continuity.accept(input, id)
        onAccepted()
        val history = when (input) {
            is Input.Restore -> input.state.history
            is Input.Push -> input.batch.history
            is Input.Latest -> input.batch.history
            is Input.After -> input.batch.history
            is Input.Reload -> input.history
            else -> null
        }
        if (input is Input.Restore || history != null && history != entry.submittedHistory) {
            entry.historyGeneration++
            entry.submittedHistory = history
        }
        return result
    }

    private suspend fun awaitActionsAsync(entry: Entry, input: Input, generation: Long? = null): List<Action>? {
        val ready = CompletableDeferred<List<Action>>()
        val id = synchronized(entry.operations) { (++entry.nextOperation).also { entry.operations[it] = ready } }
        return try {
            withTimeoutOrNull(3_000) {
                var stale = false
                val result = entry.inputs.withLock {
                    if (generation != null && generation != entry.historyGeneration) { stale = true; null }
                    else acceptAsyncLocked(entry, input, id)
                }
                if (stale) null else result?.requests ?: ready.await()
            }
        } finally {
            synchronized(entry.operations) { entry.operations.remove(id) }
        }
    }

    private fun dispatch(entry: Entry, actions: List<Action>) {
        actions.forEach { action -> when (action) {
            is Action.Subscribe -> synchronized(entries) {
                if (entry.references > 0 && !entry.subscribed) entry.subscribed = connection.send(buildJsonObject {
                    envelope("subscribe", entry.key).forEach { (key, value) -> put(key, value) }
                    put("lazy_images", true); put("fold_work", action.foldWork)
                    action.sinceSeq?.let { put("since_seq", it) }
                })
            }
            is Action.FetchAfter -> entry.scope.launch {
                // Match iOS: no settling delay on a real gap. `requests` and the in-flight gate collapse duplicates.
                // The continuity already opened this read (its gate is closed): run it rather than ask again.
                var first: List<Action>? = listOf(action)
                do {
                    catchUp(entry, first)
                    first = null
                } while (!entry.continuity.asynchronous && entry.lock.withLock { entry.continuity.behind })
            }
            is Action.FetchLatest -> entry.scope.launch { latest(entry) }
            is Action.FetchRange, is Action.FetchOlder -> if (!entry.continuity.asynchronous)
                error("Unexpected standalone continuity action: $action")
        } }
    }

    private data class FillStamp(val history: String?, val generation: Long)
    private data class PageApplication(val accepted: Boolean, val actions: List<Action>?)

    private suspend fun fillStampAsync(entry: Entry) = entry.inputs.withLock {
        FillStamp(entry.submittedHistory, entry.historyGeneration)
    }

    private suspend fun queuePageAsync(entry: Entry, stamp: FillStamp, page: ItemsResponse, input: Input,
        handedOff: () -> Unit): PageApplication = entry.inputs.withLock {
        val history = entry.submittedHistory
        if ((stamp.generation != entry.historyGeneration || stamp.history != history) && page.history != history)
            return@withLock PageApplication(false, null)
        val result = acceptAsyncLocked(entry, input, null, handedOff)
        PageApplication(true, result?.requests)
    }

    private suspend fun latest(entry: Entry, initial: Boolean = false) {
        if (entry.continuity.asynchronous) { latestAsync(entry, initial); return }
        entry.requests.withLock {
            try {
                val history = entry.lock.withLock { entry.reducer.history }
                val page = transport.items(entry.key, ItemsQuery())
                entry.lock.withLock {
                    if (history != entry.reducer.history && page.history != entry.reducer.history) {
                        dispatch(entry, listOf(Action.FetchLatest()))
                        return@withLock
                    }
                    val result = if (initial) entry.continuity.accept(Input.Latest(Batch(page.items, page.history,
                        toSeq = page.toSeq, hasMore = page.hasMore)))!!
                    else entry.continuity.accept(Input.Reload(page.items, page.history, page.toSeq, page.hasMore))!!
                    val overtaken = result.state.items.any { it.seq > result.state.covered }
                    entry.eventError = null
                    firstPageIn(entry)
                    publish(entry, verified = !overtaken)
                    if (overtaken || entry.continuity.behind) dispatch(entry, entry.continuity.accept(Input.HubChanged)!!.requests)
                }
                persist(entry)
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { entry.lock.withLock {
                if (initial && notLoaded(error)) awaitWatch(entry)
                else {
                    firstPageIn(entry)
                    val missing = missingHistory(error)
                    entry.eventError = missing
                    entry.state.update { it.copy(loading = false, error = missing ?: DataText.of(Key.ChatSyncFailed), verified = false) }
                }
            } }
            finally { withContext(NonCancellable) { entry.lock.withLock { entry.continuity.accept(Input.FillFailed) } } }
        }
    }

    private suspend fun latestAsync(entry: Entry, initial: Boolean = false) = entry.requests.withLock {
        var ownsTransfer = true
        try {
            val stamp = fillStampAsync(entry)
            val page = transport.items(entry.key, ItemsQuery())
            currentCoroutineContext().ensureActive()
            val input = if (initial) Input.Latest(Batch(page.items, page.history, toSeq = page.toSeq, hasMore = page.hasMore))
                else Input.Reload(page.items, page.history, page.toSeq, page.hasMore)
            val result = queuePageAsync(entry, stamp, page, input) { ownsTransfer = false }
            if (!result.accepted) {
                dispatch(entry, listOf(Action.FetchLatest()))
                ownsTransfer = false // Ownership moved to the replacement transfer.
            } else if (result.actions != null) {
                dispatch(entry, result.actions)
                persist(entry)
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { entry.lock.withLock {
            if (initial && notLoaded(error)) awaitWatch(entry)
            else {
                firstPageIn(entry)
                recordAsyncError(entry, missingHistory(error) ?: DataText.of(Key.ChatSyncFailed))
                entry.state.update { it.copy(loading = false, error = entry.eventError, verified = false) }
            }
        } } finally { withContext(NonCancellable) {
            if (ownsTransfer) acceptAsync(entry, Input.FillFailed)
        } }
    }

    /**
     * The open's first read was refused as not_loaded: the agent's engine let it go, and its runtime reads it only while
     * its chat is watched (Paseo, docs/integrations/paseo.md "Content", #8132). The subscribe that follows this read loads
     * it, and the hub's backlog brings the page a second or three later. Until then the chat keeps its loading state, not
     * the refusal (#8321): any items event ends the wait, the hub's error for the chat says why it can't, and after
     * [NOT_LOADED_WAIT_MILLIS] with neither the chat says it could not sync. Called holding [Entry.lock].
     */
    private fun awaitWatch(entry: Entry) {
        entry.firstPagePending = true
        entry.eventError = null // a wait of an earlier open that ran out
        entry.notLoadedWait?.cancel()
        entry.notLoadedWait = entry.scope.launch {
            delay(NOT_LOADED_WAIT_MILLIS)
            entry.lock.withLock {
                if (!entry.firstPagePending) return@withLock
                entry.firstPagePending = false
                if (entry.continuity.asynchronous) recordAsyncError(entry, DataText.of(Key.ChatSyncFailed))
                else entry.eventError = DataText.of(Key.ChatSyncFailed)
                publish(entry, verified = false)
            }
        }
        publish(entry, verified = false)
    }

    /** Something came from the hub for the chat (a page, items, its error): no first page pends. Holding [Entry.lock]. */
    private fun firstPageIn(entry: Entry) {
        entry.firstPagePending = false
        entry.notLoadedWait?.cancel()
        entry.notLoadedWait = null
    }

    private fun notLoaded(error: Throwable) = hubCodedFailure(error)?.code == NOT_LOADED

    /** A history read the node answered as gone: terminal for paging, with the hub's own sentence for the chat. */
    private fun missingHistory(error: Throwable): String? {
        val coded = hubCodedFailure(error) ?: return null
        if (coded.status != 404 || coded.code != NOT_FOUND) return null
        return (coded as? Throwable)?.message?.takeIf { it.isNotBlank() } ?: DataText.of(Key.ChatSyncFailed)
    }

    /** [first]: a read the continuity already issued; without it, [ContinuityAdapter.Input.ContinueFill] opens one. */
    private suspend fun catchUp(entry: Entry, first: List<Action>? = null) {
        if (entry.continuity.asynchronous) { catchUpAsync(entry, first); return }
        entry.requests.withLock {
            var actions = first ?: entry.lock.withLock { entry.continuity.accept(Input.ContinueFill)!!.requests }
            try {
                while (actions.isNotEmpty()) {
                    actions = when (val action = actions.single()) {
                        is Action.FetchAfter -> {
                            val history = entry.lock.withLock { entry.reducer.history }
                            val page = transport.items(entry.key, ItemsQuery(afterSeq = action.afterSeq))
                            entry.lock.withLock {
                                if (history != entry.reducer.history && page.history != entry.reducer.history) {
                                    return@withLock listOf(Action.FetchLatest())
                                }
                                val result = entry.continuity.accept(Input.After(Batch(page.items, page.history,
                                    page.fromSeq, page.toSeq, moreAfter = page.moreAfter)))!!
                                val overtaken = result.state.items.any { it.seq > result.state.covered }
                                entry.eventError = null
                                firstPageIn(entry)
                                publish(entry, verified = result.verified && !overtaken)
                                if (result.requests.isEmpty() && overtaken) entry.continuity.accept(Input.ContinueFill)!!.requests else result.requests
                            }
                        }
                        is Action.FetchLatest -> {
                            val history = entry.lock.withLock { entry.reducer.history }
                            val page = transport.items(entry.key, ItemsQuery())
                            entry.lock.withLock {
                                if (history != entry.reducer.history && page.history != entry.reducer.history) {
                                    return@withLock listOf(Action.FetchLatest())
                                }
                                val result = entry.continuity.accept(Input.Reload(page.items, page.history, page.toSeq, page.hasMore))!!
                                val overtaken = result.state.items.any { it.seq > result.state.covered }
                                entry.eventError = null
                                firstPageIn(entry)
                                publish(entry, verified = !overtaken)
                                if (overtaken || entry.continuity.behind) entry.continuity.accept(Input.ContinueFill)!!.requests else emptyList()
                            }
                        }
                        else -> error("Unexpected catch-up action: $action")
                    }
                }
                persist(entry)
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { entry.lock.withLock {
                // While the open waits for the watch (awaitWatch), a catch-up its runtime refuses alike says nothing new.
                if (entry.notLoadedWait?.isActive != true || !notLoaded(error)) {
                    val missing = missingHistory(error)
                    entry.eventError = missing
                    entry.state.update { it.copy(error = missing ?: DataText.of(Key.ChatSyncFailed), verified = false) }
                }
            } }
            finally { withContext(NonCancellable) { entry.lock.withLock { entry.continuity.accept(Input.FillFailed) } } }
        }
    }

    private suspend fun catchUpAsync(entry: Entry, first: List<Action>? = null) = entry.requests.withLock {
        var ownsTransfer = first?.isNotEmpty() == true
        try {
            var actions = first ?: acceptAsync(entry, Input.ContinueFill)?.requests.orEmpty()
            while (actions.isNotEmpty()) {
                ownsTransfer = true
                val action = actions.single()
                val stamp = fillStampAsync(entry)
                val page = when (action) {
                    is Action.FetchAfter -> transport.items(entry.key, ItemsQuery(afterSeq = action.afterSeq))
                    is Action.FetchLatest -> transport.items(entry.key, ItemsQuery())
                    else -> error("Unexpected catch-up action: $action")
                }
                currentCoroutineContext().ensureActive()
                val input = when (action) {
                    is Action.FetchAfter -> Input.After(Batch(page.items, page.history, page.fromSeq, page.toSeq, moreAfter = page.moreAfter))
                    else -> Input.Reload(page.items, page.history, page.toSeq, page.hasMore)
                }
                val result = queuePageAsync(entry, stamp, page, input) { ownsTransfer = false }
                if (!result.accepted) { actions = listOf(Action.FetchLatest()); continue }
                // A queued response belongs to the applied callback, including any subsequent transport.
                actions = result.actions ?: break
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { entry.lock.withLock {
            if (entry.notLoadedWait?.isActive != true || !notLoaded(error)) {
                recordAsyncError(entry, missingHistory(error) ?: DataText.of(Key.ChatSyncFailed))
                entry.state.update { it.copy(error = entry.eventError, verified = false) }
            }
        } } finally { withContext(NonCancellable) {
            if (ownsTransfer) acceptAsync(entry, Input.FillFailed)
        } }
    }

    private suspend fun persist(entry: Entry) = entry.saving.withLock {
        // From here an event's change may be missing from the snapshot below, so the next event starts another save.
        entry.persistPending.set(false)
        quietly {
            val snapshot = entry.lock.withLock {
                val state = entry.reducer
                CachedChat(state.items, state.history, state.covered, state.hasMore)
            }
            cache.save(entry.key, snapshot)
        }
    }

    private fun publish(entry: Entry, verified: Boolean = entry.state.value.verified) {
        (sessions as? HubSessionsRepository)?.verifiedLists?.value?.let { authority ->
            entry.currentSessions = authority.sessions[entry.key.machineId]
            entry.machineOnline = authority.machines?.firstOrNull { it.id == entry.key.machineId }?.online
        }
        if (entry.seenHistory != entry.reducer.history) {
            if (entry.seenHistory != null) {
                entry.realtime.reset()
                entry.turnDurationSeconds = null
                entry.durationAfterSeq = 0
            }
            entry.lastSeen = null
            entry.seenHistory = entry.reducer.history
        }
        val metadata = entry.currentSessions?.firstOrNull { it.key == entry.key }
            ?: sessions.sessions.value.firstOrNull { it.key == entry.key }
        val previous = metadata ?: entry.previousSession
        val recorded = entry.reducer.items
        val userId = recorded.lastOrNull { it.startsRecordedTurn }?.id
        if (entry.lastUserId != userId) {
            if (entry.lastUserId != null) entry.turnDurationSeconds = null
            entry.lastUserId = userId
        }
        if (entry.durationItems !== recorded) {
            entry.durationItems = recorded
            entry.durations = recordedTurnDurations(recorded)
            entry.durationSeconds = entry.durations.mapValues { it.value.seconds }
        }
        val durations = entry.durations
        durations[userId]?.takeIf { it.seq > entry.durationAfterSeq }?.let { entry.turnDurationSeconds = it.seconds }
        if (metadata != null) entry.previousSession = metadata
        val machineOnline = entry.machineOnline.takeIf { connection.state.value == ConnectionState.Online }
        // Ended and replaced stay latched while lists are unknown; a verified list that drops the session and later
        // lists it again (resume reuses the id, #4986) undoes them, a confirmed close included.
        var relisted = false
        entry.currentSessions?.takeIf { machineOnline == true }?.let { current ->
            val listed = current.firstOrNull { it.key == entry.key }
            if (listed == null || listed.status == "offline") entry.listedGone = true
            else if (entry.listedGone) {
                entry.listedGone = false
                entry.closeConfirmed = false
                relisted = true
            }
        }
        val runtime = previous?.let {
            SessionRuntimeState.map(it, entry.currentSessions, machineOnline,
                closeConfirmed = entry.closeConfirmed, stopRequested = entry.stopRequested,
                recordedTurnDurationSeconds = entry.turnDurationSeconds)
        } ?: SessionRuntimeState(turnDurationSeconds = entry.turnDurationSeconds)
        if (runtime.ended || metadata?.status in listOf("idle", "done", "offline", "blocked")) entry.stopRequested = false
        val seenAt = parseTime(metadata?.seenAt)
        val lastSeen = entry.lastSeen?.id ?: recorded.lastOrNull {
            seenAt != null && parseTime(it.ts)?.let { time -> time <= seenAt } == true
        }?.id
        // Only the hub's fresh list counts (cleared while offline); a session missing from it is left alone. While it
        // still works the hub resends its live text, so the draft stays and is overwritten in place.
        if (entry.reconnected && entry.currentSessions?.firstOrNull { it.key == entry.key }?.status
                ?.let { it != "working" && it != "blocked" } == true) {
            entry.realtime.finish()
            entry.reconnected = false
        }
        val items = entry.realtime.structuralItems(recorded)
        entry.realtime.publishLiveText(recorded)
        // Loading only while nothing is shown and the hub has not answered for the chat yet: an empty chat is one it said is empty.
        entry.state.update { it.copy(items = items, loading = entry.firstPagePending && items.isEmpty(),
            history = entry.reducer.history,
            streamingItemIds = entry.realtime.streamingItemIds(),
            liveText = entry.realtime.liveText(),
            hasOlder = entry.reducer.hasMore == true, olderBoundaryKnown = entry.reducer.hasMore != null,
            error = entry.eventError, verified = verified,
            lastSeenMessageId = lastSeen?.let(entry.realtime::displayId),
            ended = runtime.ended || (it.ended && !relisted), replaced = runtime.replaced || (it.replaced && !relisted),
            stopping = runtime.stopping, turnDurationSeconds = runtime.turnDurationSeconds,
            recordedTurnDurations = entry.durationSeconds) }
        // However the items came (an event, a page, a catch-up), a message sent from here that is in them is shown.
        val observed = recorded
        if (transcript != null && observed.isNotEmpty() && observed != entry.observedTranscript) {
            entry.observedTranscript = observed
            entry.scope.launch {
                try { transcript.invoke(entry.key, observed) }
                catch (cancelled: CancellationException) { throw cancelled }
                catch (_: Exception) { entry.observedTranscript = null }
            }
        }
    }

    private fun envelope(type: String, session: SessionKey) = buildJsonObject {
        put("type", type); put("machine_id", session.machineId); put("session_id", session.sessionId)
    }
    private fun part(item: Item) = item.id.substringAfterLast('#').toIntOrNull() ?: 0

    companion object {
        /** The code of a read that would start the agent: one its engine let go, read only while watched (#8132). */
        private const val NOT_LOADED = "not_loaded"
        private const val NOT_FOUND = "not_found"
        /**
         * How long an open whose first read was refused as not_loaded waits for the watch ([awaitWatch]): the web's read
         * time (`READ_TIMEOUT_MS`), past the hub's own limits for a backlog (10 s for the watch, then 30 s for the read),
         * so the hub's error for the chat comes first.
         */
        internal const val NOT_LOADED_WAIT_MILLIS = 45_000L
    }
}
