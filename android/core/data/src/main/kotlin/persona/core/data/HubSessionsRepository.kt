package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

internal data class VerifiedSessionLists(
    val machines: List<Machine>? = null,
    val sessions: Map<String, List<Session>> = emptyMap(),
)

/** List events refresh the selected HTTP view; listing never subscribes to chat item streams. */
class HubSessionsRepository(
    private val transport: ChatTransport,
    private val connection: HubConnection,
    scope: CoroutineScope,
    private val snapshots: ListSnapshotStore? = null,
    private val reconcileHeldRetries: (suspend (Session, Session) -> Unit)? = null,
) : SessionsRepository, VerifiedSessionsRepository {
    private val mutableMachines = MutableStateFlow<List<Machine>>(emptyList())
    override val machines = mutableMachines.asStateFlow()
    private val mutableSessions = MutableStateFlow<List<Session>>(emptyList())
    override val sessions = mutableSessions.asStateFlow()
    private val mutableAdoptedSessions = MutableStateFlow<List<Session>>(emptyList())
    override val adoptedSessions = mutableAdoptedSessions.asStateFlow()
    private val mutableStartedThreads = MutableStateFlow(emptyMap<SessionKey, Session>())
    override val startedThreads = mutableStartedThreads.asStateFlow()
    private val mutableListStatus = MutableStateFlow(SessionListStatus())
    override val listStatus = mutableListStatus.asStateFlow()
    internal val verifiedLists = MutableStateFlow(VerifiedSessionLists())
    override fun verifiedSessions(): Map<String, List<Session>> = verifiedLists.value.sessions
    private val lock = Mutex()
    private val refreshing = Mutex()
    private var revision = 0L
    private var machinesChanged = 0L
    private val sessionChanges = mutableMapOf<String, Long>()
    private val revisionChanges = mutableMapOf<SessionKey, Long>()
    /**
     * [Complete]: a view needing every row on a `sessions_list` hub. One baseline of complete pages (`summary=0`), then
     * `since` reads of complete rows on each hint; a new baseline only on an expired revision, a changed hub (another
     * repository, or a hello that changes the capability) or an explicit [refresh].
     */
    private enum class Mode { Unknown, Paged, Legacy, Complete }
    private var mode = Mode.Unknown
    /** Held rows are the whole collection, not a bounded window. */
    private val Mode.holdsAll get() = this == Mode.Legacy || this == Mode.Complete
    private var listCapability: Boolean? = null
    private var generation = 0L
    private var activeRequest: Job? = null
    // The cursor belongs to the frozen baseline, NEVER to a delta's newer revision.
    private var baselineRevision: String? = null
    private var checkpoint: String? = null
    // Only opaque cursor history grows while traversing the snapshot, never a global row/change cache.
    private val pageStarts = mutableListOf<String?>(null)
    private data class LoadedPage(val index: Int, val keys: Set<SessionKey>)
    private val loadedPages = ArrayDeque<LoadedPage>()
    /** A hint's revision, or "" for a local invalidation that must read. */
    private val invalidations = Channel<String>(Channel.CONFLATED)

    private val restored = scope.async {
        val machines = snapshots.restore<List<Machine>>("machines")
        var sessions: List<Session>? = null
        quietly { sessions = snapshots?.sessionWindow(WINDOW_SIZE) }
        lock.withLock {
            machines?.let { mutableMachines.value = it }
            sessions?.let { mutableSessions.value = it }
        }
    }

    private suspend fun persist() {
        snapshots.save("machines", mutableMachines.value)
        // A cache is a first-paint hint, not a row checkpoint or a complete verified baseline. Keep folded rows' parents
        // inside the bounded window so first paint never draws a side/child session as an orphan root (#12101).
        snapshots.save("sessions", firstPaintSnapshot(mutableSessions.value, WINDOW_SIZE))
    }

    /**
     * Pushed events write the first-paint snapshot at once and then at most every [PERSIST_MILLIS], outside the lock:
     * serializing every listed session (some titles megabytes long) on each event kept the phone busy while agents ran,
     * once the capsule kept the process alive. Refreshes still commit before they return.
     */
    private val persistRequests = kotlinx.coroutines.channels.Channel<Unit>(kotlinx.coroutines.channels.Channel.CONFLATED)
    private fun persistLater() { persistRequests.trySend(Unit) }

    init {
        scope.launch {
            for (request in persistRequests) {
                val _ = runCatching { lock.withLock { persist() } }
                delay(PERSIST_MILLIS)
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.events.collect { event ->
                if (event is SessionsListEvent) {
                    invalidations.trySend(event.revision)
                    return@collect
                }
                restored.await()
                lock.withLock {
                    when (event) {
                        is Hello -> {
                            val enabled = "sessions_list" in event.features
                            val changed = (listCapability != null && listCapability != enabled) ||
                                (mode == Mode.Legacy && enabled) || (mode == Mode.Paged && !enabled) ||
                                (mode == Mode.Complete && !enabled)
                            listCapability = enabled
                            if (changed) {
                                generation++
                                activeRequest?.cancel()
                                mode = Mode.Unknown
                                resetPages()
                                // A complete view keeps every row on screen until the new baseline replaces them.
                                if (!mutableListStatus.value.filter.requiresCompleteMembership) {
                                    mutableSessions.value = mutableSessions.value.take(WINDOW_SIZE)
                                }
                                verifiedLists.value = VerifiedSessionLists()
                                mutableListStatus.update { SessionListStatus(filter = it.filter) }
                                invalidations.trySend("")
                            }
                            return@withLock
                        }
                        is MachinesEvent -> {
                            machinesChanged = ++revision
                            mutableMachines.value = event.machines
                            if (connection.state.value == ConnectionState.Online) verifiedLists.update { prior ->
                                prior.copy(machines = event.machines, sessions = prior.sessions.filterKeys { machine ->
                                    mode.holdsAll && event.machines.firstOrNull { it.id == machine }?.online == true &&
                                        prior.machines?.firstOrNull { it.id == machine }?.online != false
                                })
                            }
                        }
                        is SessionsEvent -> {
                            // A negotiated paged client must not adopt an accidental full legacy push.
                            if (mode == Mode.Paged || mode == Mode.Complete) return@withLock
                            sessionChanges[event.machineId] = ++revision
                            revisionChanges.keys.removeAll { it.machineId == event.machineId }
                            val remaining = mutableSessions.value.filterNot { it.machineId == event.machineId }
                            val incoming = if (mode == Mode.Legacy) event.sessions else event.sessions.take(WINDOW_SIZE)
                            val machineRows = incoming.map { it.copy(machineId = event.machineId) }
                            for (row in machineRows) if (!row.undelivered.isNullOrEmpty()) reconcileHeldRow(row)
                            mutableSessions.value = (remaining + machineRows)
                                .let { if (mode == Mode.Legacy) it else it.take(WINDOW_SIZE) }
                            if (mode == Mode.Legacy && connection.state.value == ConnectionState.Online) verifiedLists.update {
                                it.copy(sessions = if (it.machines?.firstOrNull { machine -> machine.id == event.machineId }?.online == false)
                                    it.sessions - event.machineId
                                else it.sessions + (event.machineId to machineRows))
                            }
                            if (mode == Mode.Legacy) mutableListStatus.update { it.copy(total = mutableSessions.value.size) }
                        }
                        is SessionRowEvent -> {
                            val row = event.session
                            if (row.waiting != null && row.undelivered == null) return@withLock
                            reconcileHeldRow(row)
                            updateHeld(row.key) { row.undelivered.orEmpty() }
                        }
                        is RevsEvent -> {
                            revision++
                            // Only a legacy read merges them later; HTTP rows of the other modes come with their revs.
                            if (mode == Mode.Legacy || mode == Mode.Unknown) event.revs.forEach { (sessionId, rev) ->
                                revisionChanges[SessionKey(event.machineId, sessionId)] = rev
                            }
                            mutableSessions.update { list -> list.map { session ->
                                event.revs[session.sessionId]?.takeIf { session.machineId == event.machineId }
                                    ?.let { session.copy(rev = it) } ?: session
                            } }
                        }
                        else -> return@withLock
                    }
                    persistLater()
                }
            }
        }
        scope.launch {
            for (first in invalidations) {
                delay(INVALIDATION_DELAY_MS)
                var hint = first
                // Coalesce a burst, retaining hints during HTTP; a local invalidation always reads.
                while (true) { val next = invalidations.tryReceive().getOrNull() ?: break; hint = if (hint.isEmpty()) hint else next }
                try { quietly { recoverDelta(hint) } }
                catch (cancelled: CancellationException) { currentCoroutineContext().ensureActive() }
            }
        }
        scope.launch {
            connection.state.collectLatest {
                if (it == ConnectionState.Online) {
                    val heldMode = lock.withLock { mode }
                    if (heldMode == Mode.Paged || heldMode == Mode.Complete) {
                        if (connection.resumed) lock.withLock { verifyHeld() }
                        quietly { recoverDelta() }
                    } else if (connection.resumed && heldMode == Mode.Legacy) lock.withLock { verifyHeld() }
                    else quietly { refresh() }
                } else lock.withLock { verifiedLists.value = VerifiedSessionLists() }
            }
        }
    }

    /** Serialize HTTP operations, but let a changed filter cancel the operation and reject late results. */
    private suspend fun request(block: suspend (Long, SessionListFilter) -> Unit) = coroutineScope {
        refreshing.withLock {
            restored.await()
            val job = currentCoroutineContext().job
            val (epoch, filter) = lock.withLock {
                activeRequest = job
                mutableListStatus.update { it.copy(loading = true, failed = false) }
                generation to mutableListStatus.value.filter
            }
            var failed = false
            try { block(epoch, filter) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { failed = true; throw error }
            finally { withContext(NonCancellable) { lock.withLock {
                if (generation == epoch) mutableListStatus.update { it.copy(loading = false, failed = failed) }
                if (activeRequest === job) activeRequest = null
            } } }
        }
    }

    override suspend fun listCounts(filter: SessionListFilter): SessionListCounts {
        if (lock.withLock { mode != Mode.Paged }) throw SessionListsUnsupported()
        return transport.sessionCounts(filter)
    }

    override suspend fun refresh() = request { epoch, filter -> firstPage(epoch, filter) }

    private suspend fun firstPage(epoch: Long, filter: SessionListFilter) {
        if (filter.requiresCompleteMembership) {
            // Without hints (`sessions_list`) a `since` checkpoint would never be followed: the full list keeps the
            // hub's whole-list pushes. Before this repository sees a hello, the connection's last one says.
            val hinted = lock.withLock { listCapability } ?: connection.lastHello?.let { "sessions_list" in it.features }
            if (hinted == true) completeBaseline(epoch) else fallbackLegacy(epoch)
            return
        }
        if (lock.withLock { mode == Mode.Legacy }) {
            legacyRefresh(epoch)
            return
        }
        val page = try { transport.sessionPage(filter) }
        catch (_: SessionListsUnsupported) {
            fallbackLegacy(epoch)
            return
        }
        currentCoroutineContext().ensureActive()
        lock.withLock {
            if (epoch != generation) return
            mode = Mode.Paged
            baselineRevision = page.revision
            pageStarts.clear()
            pageStarts.add(null)
            page.nextCursor?.let { pageStarts.add(it) }
            loadedPages.clear()
            loadedPages.add(LoadedPage(0, page.sessions.take(WINDOW_SIZE).mapTo(mutableSetOf()) { it.key }))
            checkpoint = page.revision
            mutableSessions.value = page.sessions.take(WINDOW_SIZE)
            verifiedLists.value = VerifiedSessionLists(machines = verifiedLists.value.machines)
            mutableListStatus.update { it.copy(paged = true, loaded = true, hasMore = page.hasMore, hasPrevious = false, total = page.total) }
            persist()
        }
        try { refreshMetadata(epoch, filter, includeMachines = true) }
        catch (_: SessionListsUnsupported) { fallbackLegacy(epoch) }
    }

    private suspend fun refreshMetadata(epoch: Long, filter: SessionListFilter, includeMachines: Boolean = false) {
        val before = lock.withLock { revision }
        coroutineScope {
            val machines = if (includeMachines) async { transport.machines() } else null
            val counts = async { transport.sessionCounts(filter.copy(state = null)) }
            val machineCounts = if (filter.machineId != null) async {
                transport.sessionCounts(filter.copy(state = null, machineId = null))
            } else counts
            val stateCounts = counts.await()
            val crossMachineCounts = machineCounts.await()
            val machineList = machines?.await()
            lock.withLock {
                if (epoch != generation) return@withLock
                if (machineList != null && machinesChanged <= before) {
                    mutableMachines.value = machineList
                    if (connection.state.value == ConnectionState.Online) verifiedLists.update { it.copy(machines = machineList) }
                }
                mutableListStatus.update { it.copy(counts = stateCounts, machineCounts = crossMachineCounts) }
                persist()
            }
        }
    }

    override suspend fun setListFilter(filter: SessionListFilter) {
        val filterGeneration = lock.withLock {
            val status = mutableListStatus.value
            if (status.filter == filter) return
            // The complete collection is the same for every filter (the view projects it locally): keep the baseline.
            if (mode == Mode.Complete && filter.requiresCompleteMembership && status.filter.requiresCompleteMembership) {
                mutableListStatus.value = status.copy(filter = filter)
                return
            }
            // The first complete view can project the restored rows locally while its authoritative list loads.
            val keepFirstPaint = mode == Mode.Unknown && !status.loaded && status.filter == SessionListFilter() &&
                filter.requiresCompleteMembership
            // A view policy is not a server capability failure. Reprobe pagination when leaving that view.
            if (status.filter.requiresCompleteMembership != filter.requiresCompleteMembership) mode = Mode.Unknown
            generation++
            activeRequest?.cancel()
            resetPages()
            if (mode != Mode.Legacy && !keepFirstPaint) mutableSessions.value = emptyList()
            verifiedLists.value = VerifiedSessionLists()
            mutableListStatus.value = SessionListStatus(paged = mode == Mode.Paged, filter = filter)
            generation
        }
        try {
            refresh()
        } catch (cancelled: CancellationException) {
            // A replacement request in this repository must not cancel the caller's work on other Hubs.
            currentCoroutineContext().ensureActive()
            if (lock.withLock { generation == filterGeneration }) throw cancelled
        }
    }

    override suspend fun loadMore() = loadPage(previous = false)
    override suspend fun loadPrevious() = loadPage(previous = true)

    private suspend fun loadPage(previous: Boolean) = request { epoch, filter ->
        val target = lock.withLock {
            if (mode != Mode.Paged || loadedPages.isEmpty()) return@request
            val index = if (previous) loadedPages.first().index - 1 else loadedPages.last().index + 1
            if (index !in pageStarts.indices) return@request
            index to pageStarts[index]
        }
        // The protocol supplies no cursor for page zero. Going back to it starts a fresh frozen view.
        if (previous && target.first == 0) {
            firstPage(epoch, filter)
            return@request
        }
        val baseline = lock.withLock { baselineRevision } ?: return@request
        try {
            val page = transport.sessionPage(filter, cursor = target.second)
            currentCoroutineContext().ensureActive()
            // Keep <= one page of candidates private while replaying changes from the OLD baseline.
            // No global change map: unloaded identities are ignored on each delta page.
            val candidates = page.sessions.take(WINDOW_SIZE).associateByTo(linkedMapOf()) { it.key }
            val reconciled = streamDelta(epoch, filter, baseline, candidates)
            lock.withLock {
                if (epoch != generation) return@withLock
                val loadedPage = LoadedPage(target.first, candidates.keys.toSet())
                if (previous) loadedPages.addFirst(loadedPage) else loadedPages.addLast(loadedPage)
                if (loadedPages.size > MAX_LOADED_PAGES) {
                    if (previous) loadedPages.removeLast() else loadedPages.removeFirst()
                }
                val allowed = loadedPages.flatMapTo(mutableSetOf()) { it.keys }
                val retained = mutableSessions.value.filter { it.key in allowed }
                val existing = retained.mapTo(mutableSetOf()) { it.key }
                val added = candidates.values.filterNot { it.key in existing }
                mutableSessions.value = if (previous) added + retained else retained + added
                if (page.nextCursor != null && target.first + 1 == pageStarts.size) pageStarts.add(page.nextCursor)
                checkpoint = reconciled.revision
                mutableListStatus.update { it.copy(
                    hasMore = loadedPages.last().index + 1 in pageStarts.indices,
                    hasPrevious = loadedPages.first().index > 0,
                    total = reconciled.total,
                ) }
                persist()
            }
            refreshMetadata(epoch, filter)
        } catch (_: SessionListExpired) { firstPage(epoch, filter) }
        catch (_: SessionListsUnsupported) { fallbackLegacy(epoch) }
    }

    /** [hint] is the `sessions_list` revision that woke this read, or "" for a local invalidation. */
    private suspend fun recoverDelta(hint: String = "") = request { epoch, filter ->
        val (heldMode, since) = lock.withLock {
            // The checkpoint already holds this revision: nothing changed since the last read.
            if (mode == Mode.Complete && hint.isNotEmpty() && hint == checkpoint) return@request
            mode to if (mode == Mode.Paged || mode == Mode.Complete) checkpoint else null
        }
        if (since == null) {
            // A paged WebSocket emits invalidations even when this view uses the complete HTTP fallback.
            if (filter.requiresCompleteMembership || heldMode != Mode.Legacy) firstPage(epoch, filter)
            return@request
        }
        val complete = heldMode == Mode.Complete
        try {
            val last = streamDelta(epoch, if (complete) COMPLETE_QUERY else filter, since)
            lock.withLock {
                if (epoch != generation) return@withLock
                checkpoint = last.revision
                if (complete) {
                    mutableListStatus.update { it.copy(total = mutableSessions.value.size) }
                    verifyHeld()
                    // Serializing every row on each hint kept the phone busy; the snapshot is a first-paint hint.
                    persistLater()
                } else {
                    mutableListStatus.update { it.copy(total = last.total) }
                    persist()
                }
            }
            // The complete view derives its counts locally (counts need the paged API's membership).
            if (!complete) refreshMetadata(epoch, filter)
        } catch (_: SessionListExpired) { firstPage(epoch, filter) }
        catch (_: SessionListsUnsupported) { fallbackLegacy(epoch) }
    }

    /** Apply each page directly to loaded rows/candidates; only return the final bounded page. */
    private suspend fun streamDelta(
        epoch: Long,
        filter: SessionListFilter,
        since: String,
        candidates: MutableMap<SessionKey, Session>? = null,
    ): SessionListPage {
        var cursor: String? = null
        while (true) {
            val page = transport.sessionPage(filter, cursor = cursor, since = since)
            currentCoroutineContext().ensureActive()
            lock.withLock {
                check(epoch == generation) { "Session filter changed" }
                val changes = page.sessions.associateBy { it.key }
                val removals = page.remove.mapNotNull { encoded ->
                    runCatching { JsonCodec.decodeFromString<List<String>>(encoded) }.getOrNull()
                        ?.takeIf { it.size == 2 }?.let { SessionKey(it[0], it[1]) }
                }.toSet()
                val loaded = mutableSessions.value.mapTo(mutableSetOf()) { it.key }
                val kept = mutableSessions.value.mapNotNull { row ->
                    if (row.key in removals) null else changes[row.key]?.also { changed ->
                        if (!changed.undelivered.isNullOrEmpty()) reconcileHeldRow(changed, row)
                    } ?: row
                }
                val unloaded = changes.values.filter { it.key !in loaded }
                // The complete collection takes every new row; a bounded window only those that belong in it.
                mutableSessions.value = (if (mode == Mode.Complete) unloaded else arrivals(unloaded, kept)) + kept
                candidates?.let { window ->
                    removals.forEach { window.remove(it) }
                    changes.forEach { (key, row) -> if (window.containsKey(key)) window[key] = row }
                }
            }
            if (!page.hasMore) return page
            val next = requireNotNull(page.nextCursor) { "Missing session delta cursor" }
            require(next != cursor) { "Repeated session delta cursor" }
            cursor = next
        }
    }

    /**
     * Called under [lock]. An unloaded delta row joins only while page zero is loaded and it sorts above the window's
     * oldest row (or the whole list is loaded); rows that belong to later frozen pages wait for those pages.
     */
    private fun arrivals(rows: List<Session>, kept: List<Session>): List<Session> {
        val top = loadedPages.firstOrNull()?.takeIf { it.index == 0 } ?: return emptyList()
        val complete = loadedPages.last().index + 1 !in pageStarts.indices
        val oldest = kept.mapNotNull { it.updated }.minOrNull()
        val joined = rows.filter { row -> complete || (oldest != null && row.updated?.let { it > oldest } == true) }
        if (joined.isEmpty()) return joined
        // A burst past one extra page rebaselines page one rather than growing the window without bound.
        if (top.keys.size + joined.size > 2 * WINDOW_SIZE) throw SessionListExpired()
        loadedPages[0] = top.copy(keys = top.keys + joined.map { it.key })
        // Each time parsed once (`updated` parses its text on every read).
        return joined.map { it to it.updated }.sortedWith(compareByDescending<Pair<Session, java.time.Instant?>> { it.second }
            .thenBy { it.first.machineId }.thenBy { it.first.sessionId }).map { it.first }
    }

    /** Called under [lock]; never combines page tokens/checkpoints from different hub epochs. */
    private fun resetPages() {
        pageStarts.clear()
        pageStarts.add(null)
        loadedPages.clear()
        baselineRevision = null
        checkpoint = null
    }

    /**
     * The complete collection from complete pages of one frozen snapshot, with machines read once beside it. Hints then
     * follow from its revision; machines change through their own pushes.
     */
    private suspend fun completeBaseline(epoch: Long) {
        val before = lock.withLock { revision }
        val (machines, baseline) = try {
            coroutineScope {
                val machines = async { transport.machines() }
                val rows = async { completeRows() }
                machines.await() to rows.await()
            }
        } catch (_: SessionListsUnsupported) {
            fallbackLegacy(epoch)
            return
        }
        val (rows, baselineAt) = baseline
        lock.withLock {
            if (epoch != generation) return
            mode = Mode.Complete
            resetPages()
            baselineRevision = baselineAt
            checkpoint = baselineAt
            if (machinesChanged <= before) mutableMachines.value = machines
            mutableSessions.value = rows.map { session ->
                revisionChanges[session.key]?.takeIf { it > (session.rev ?: Long.MIN_VALUE) }?.let { session.copy(rev = it) } ?: session
            }
            revisionChanges.clear()
            mutableListStatus.update { it.copy(paged = false, loaded = true, hasMore = false, hasPrevious = false, total = rows.size) }
            verifyHeld()
            persist()
        }
    }

    /** Every page of one snapshot; a snapshot that expires part way (five minutes) is read again from page one. */
    private suspend fun completeRows(): Pair<List<Session>, String> {
        var attempts = 0
        while (true) {
            val rows = ArrayList<Session>()
            var cursor: String? = null
            try {
                while (true) {
                    val page = transport.sessionPage(COMPLETE_QUERY, cursor = cursor)
                    currentCoroutineContext().ensureActive()
                    rows += page.sessions
                    if (!page.hasMore) return rows to page.revision
                    val next = requireNotNull(page.nextCursor) { "Missing session page cursor" }
                    require(next != cursor) { "Repeated session page cursor" }
                    cursor = next
                }
            } catch (expired: SessionListExpired) {
                if (cursor == null || ++attempts >= COMPLETE_ATTEMPTS) throw expired
            }
        }
    }

    private suspend fun fallbackLegacy(epoch: Long) {
        lock.withLock {
            if (epoch != generation) return
            mode = Mode.Legacy
            resetPages()
        }
        legacyRefresh(epoch)
    }

    /** Compatibility path: retain legacy refresh/WS merge semantics, only after explicit Unsupported. */
    private suspend fun legacyRefresh(epoch: Long) {
        val before = lock.withLock { revision }
        val (machines, sessions) = coroutineScope {
            val machines = async { transport.machines() }
            val sessions = async { transport.sessions() }
            machines.await() to sessions.await()
        }
        lock.withLock {
            if (epoch != generation) return
            if (machinesChanged <= before) mutableMachines.value = machines
            val changed = sessionChanges.filterValues { it > before }.keys
            mutableSessions.value = (sessions.filterNot { it.machineId in changed } +
                mutableSessions.value.filter { it.machineId in changed }).map { session ->
                revisionChanges[session.key]?.takeIf { it > (session.rev ?: Long.MIN_VALUE) }?.let { session.copy(rev = it) } ?: session
            }
            revisionChanges.clear()
            mutableListStatus.update { it.copy(paged = false, loaded = true, hasMore = false, hasPrevious = false, total = mutableSessions.value.size) }
            verifyHeld()
            persist()
        }
    }

    /** A partial filtered window is NEVER evidence that a machine's other sessions were removed. */
    private fun verifyHeld() {
        if (connection.state.value != ConnectionState.Online || !mutableListStatus.value.loaded) return
        if (mode == Mode.Paged) {
            verifiedLists.value = VerifiedSessionLists(machines = mutableMachines.value)
            return
        }
        if (mode.holdsAll) verifiedLists.value = VerifiedSessionLists(
            mutableMachines.value,
            (mutableMachines.value.map { it.id } + mutableSessions.value.map { it.machineId })
                .distinct().associateWith { machine -> mutableSessions.value.filter { it.machineId == machine } },
        )
    }

    /** Only transitions that the manager pushed or exposed in a changes-only read can rearm an identical failure. */
    private suspend fun reconcileHeldRow(row: Session, previousRow: Session? = null) {
        val reconcile = reconcileHeldRetries ?: return
        val previous = previousRow ?: mutableSessions.value.firstOrNull { it.key == row.key }
            ?: mutableAdoptedSessions.value.firstOrNull { it.key == row.key } ?: return
        if (!previous.undelivered.isNullOrEmpty() && previous.undelivered != row.undelivered) {
            quietly { reconcile(previous, row) }
        }
    }

    /** Patch only the held queue of an existing row, leaving filtered-list membership to the list protocol. */
    private fun updateHeld(key: SessionKey, transform: (List<HeldAgentMessage>) -> List<HeldAgentMessage>) {
        fun patch(rows: List<Session>) = rows.map { row ->
            if (row.key != key) row else row.copy(undelivered = transform(row.undelivered.orEmpty()).ifEmpty { null })
        }
        sessionChanges[key.machineId] = ++revision
        mutableSessions.value = patch(mutableSessions.value)
        mutableAdoptedSessions.value = patch(mutableAdoptedSessions.value)
        verifiedLists.update { known ->
            val sameMachine = known.sessions[key.machineId] ?: return@update known
            known.copy(sessions = known.sessions + (key.machineId to patch(sameMachine)))
        }
    }

    suspend fun showHeld(key: SessionKey, message: HeldAgentMessage) {
        restored.await()
        lock.withLock {
            updateHeld(key) { list -> if (list.any { it.id == message.id }) list else list + message }
            persistLater()
        }
    }

    suspend fun heldActionCompleted(key: SessionKey, id: String, retry: Boolean) {
        if (retry) {
            // Only the manager knows whether Retry will send, check for an earlier delivery, or need another decision.
            // A new socket row can already have arrived while the action was in flight.
            invalidations.trySend("")
            return
        }
        restored.await()
        lock.withLock {
            updateHeld(key) { it.filterNot { m -> m.id == id } }
            persistLater()
        }
    }

    override suspend fun adopt(session: Session) {
        if (session.machineId.isBlank() || session.sessionId.isBlank()) return
        restored.await()
        lock.withLock {
            mutableAdoptedSessions.value = (mutableAdoptedSessions.value.filterNot { it.key == session.key } + session).takeLast(WINDOW_SIZE)
            if (mutableSessions.value.any { it.key == session.key }) return@withLock
            // A single start response isn't authoritative filtered membership. Reconcile the bounded list instead.
            if (mode == Mode.Paged) { invalidations.trySend(""); return@withLock }
            sessionChanges[session.machineId] = ++revision
            mutableSessions.value = (mutableSessions.value + session).let { if (mode.holdsAll) it else it.take(WINDOW_SIZE) }
            persist()
        }
    }

    override suspend fun adoptThread(previous: SessionKey, session: Session) {
        if (previous.machineId != session.machineId || previous == session.key) return
        restored.await()
        val waiting = lock.withLock {
            (mutableAdoptedSessions.value.lastOrNull { it.key == previous }
                ?: mutableSessions.value.firstOrNull { it.key == previous })?.awaitingFirstMessage == true
        }
        if (!waiting) return
        adopt(session)
        lock.withLock {
            mutableStartedThreads.value = (mutableStartedThreads.value + (previous to session)).toList().takeLast(WINDOW_SIZE).toMap()
        }
    }

    override suspend fun applyMachine(machine: Machine) {
        if (machine.id.isBlank()) return
        restored.await()
        lock.withLock {
            machinesChanged = ++revision
            val list = mutableMachines.value
            mutableMachines.value = if (list.any { it.id == machine.id }) list.map { if (it.id == machine.id) machine else it } else list + machine
            persist()
        }
    }

    private companion object {
        const val WINDOW_SIZE = 50
        const val MAX_LOADED_PAGES = 10
        const val INVALIDATION_DELAY_MS = 40L
        const val COMPLETE_ATTEMPTS = 3
        /** The complete view's server query: the whole collection as complete rows; its filters apply locally. */
        val COMPLETE_QUERY = SessionListFilter(requiresCompleteMembership = true)
    }
}

internal suspend inline fun quietly(block: () -> Unit) {
    try { block() }
    catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) { /* A later refresh or reconnect reconciles state. */ }
}

/**
 * Picks up to [limit] rows for the first-paint cache, pulling in the listed parent of any folded side/child row it keeps
 * so cold open never briefly renders a folded side/child row as Row #1 before the full baseline loads (#12101).
 */
internal fun firstPaintSnapshot(rows: List<Session>, limit: Int): List<Session> {
    if (rows.size <= limit) return rows
    val byKey = rows.associateBy { it.key }
    val picked = LinkedHashMap<SessionKey, Session>(limit)
    for (row in rows) {
        if (picked.size >= limit) break
        if (row.key in picked) continue
        val parent = row.foldedUnderKey?.let(byKey::get)
        if (parent != null && parent.key !in picked) {
            if (picked.size + 2 > limit) continue
            picked[parent.key] = parent
        }
        picked[row.key] = row
    }
    return picked.values.toList()
}

/** How often pushed session events write the first-paint snapshot at most. */
private const val PERSIST_MILLIS = 10_000L
