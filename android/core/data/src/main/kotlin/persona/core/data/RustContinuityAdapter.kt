package persona.core.data

import android.os.Handler
import android.os.Looper
import android.view.Choreographer
import java.util.concurrent.atomic.AtomicLong
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.domain.ContinuitySync.Action
import persona.core.rustcore.CoreHost
import persona.core.rustcore.RustCore
import persona.core.rustcore.wire.*
import persona.core.rustcore.wire.Action as CoreAction
import persona.core.rustcore.wire.ChatSnapshot as CoreSnapshot

/** A callback represents the next frame, not a timer or a wait for a worker. */
interface ContinuityFrameScheduler {
    fun schedule(frame: () -> Unit)
    /** Creation must start after the frame's traversal, not inside its animation callback. */
    fun afterFirstFrame(start: () -> Unit) = schedule(start)
    /** JVM tests may finish worker work and run their manual frame here. The app never waits. */
    suspend fun submitted(core: RustCore) {}
    val synchronous: Boolean get() = false
}

class AndroidContinuityFrameScheduler : ContinuityFrameScheduler {
    override fun afterFirstFrame(start: () -> Unit) = schedule {
        Handler(Looper.getMainLooper()).post { start() }
    }
    override fun schedule(frame: () -> Unit) {
        Handler(Looper.getMainLooper()).post {
            Choreographer.getInstance().postFrameCallback { frame() }
        }
    }
}

/** One lazy process-wide core; tests can own a separate driver and manual frames. */
class RustContinuityDriver(
    private val scheduler: ContinuityFrameScheduler,
    private val onRejected: (() -> Unit)? = null,
    private val create: (CoreHost) -> RustCore = { RustCore(Config(), it) },
) : AutoCloseable {
    data class Rejections(val count: Long = 0, val lastReason: String? = null)
    @Volatile var rejections = Rejections(); private set
    internal data class Delivery(val request: Request, val sequence: ULong?)
    private data class Submission(val chat: String, val sequence: ULong)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val owners = linkedMapOf<String, (ChatPatch, List<Delivery>, Boolean) -> Unit>()
    private val requests = mutableMapOf<String, MutableList<Delivery>>()
    // Each submitted input and its Ack are adjacent in the worker FIFO. Fetch callbacks precede that Ack's
    // global wake; state changes use changedChat. This tags effects before a later UI operation can start.
    private val submissions = ArrayDeque<Submission>()
    private val rejected = mutableSetOf<String>()
    private val dirty = linkedSetOf<String>()
    private var scheduled = false
    private var closed = false
    @Volatile private var liveCore: RustCore? = null
    private val coreDelegate = lazy {
        CompletableDeferred<RustCore>(scope.coroutineContext[Job]).also { ready ->
            scheduler.afterFirstFrame {
                if (ready.isCancelled) return@afterFirstFrame
                if (scheduler.synchronous) {
                    try { ready.complete(createCore()) }
                    catch (failure: Throwable) { ready.completeExceptionally(failure) }
                } else scope.launch {
                    try { ready.complete(createCore()) }
                    catch (failure: Throwable) { ready.completeExceptionally(failure) }
                }
            }
        }
    }
    private fun createCore(): RustCore {
        val created = create(host)
        val accepted = synchronized(owners) {
            if (closed) false else { liveCore = created; true }
        }
        if (!accepted) {
            created.close()
            throw CancellationException("Continuity driver was closed")
        }
        return created
    }
    private val core by coreDelegate
    internal val synchronous get() = scheduler.synchronous

    private val host = object : CoreHost {
        override fun changed(version: Long) {
            // publish_ack is the core's global wake; it follows the input's effects, including an applied no-op.
            val chat = synchronized(owners) { submissions.removeFirstOrNull()?.chat }
            if (chat != null) changedChat(chat, version) else this@RustContinuityDriver.changed(version)
        }
        override fun changedChat(chat: String, version: Long) = this@RustContinuityDriver.changedChat(chat, version)
        override fun run(effect: Effect) {
            when (effect) {
                is Effect.Fetch -> {
                    synchronized(owners) {
                        val sequence = submissions.firstOrNull()?.takeIf { it.chat == effect.chat }?.sequence
                        if (effect.chat in owners)
                            requests.getOrPut(effect.chat) { mutableListOf() }.add(Delivery(effect.request, sequence))
                    }
                    changedChat(effect.chat, 0)
                }
                is Effect.Rejected -> {
                    synchronized(owners) {
                        rejections = Rejections(rejections.count + 1, effect.reason)
                        rejected.addAll(owners.keys)
                        dirty.addAll(owners.keys)
                    }
                    // Rejected has no owner. Diagnostics must not fail another chat or release its core gates.
                    // A shadow observer gets only the signal, never a potentially content-bearing reason.
                    val _ = runCatching {
                        if (onRejected != null) onRejected.invoke()
                        else android.util.Log.w("ContinuityCore", effect.reason)
                    }
                    scheduleDrain()
                }
            }
        }
    }

    internal fun register(chat: String, apply: (ChatPatch, List<Request>) -> Unit) =
        registerDetailed(chat) { patch, deliveries, _ -> apply(patch, deliveries.map { it.request }) }

    internal fun registerDetailed(chat: String, apply: (ChatPatch, List<Delivery>, Boolean) -> Unit) = synchronized(owners) {
        check(chat !in owners)
        owners[chat] = apply
    }

    /** Compatibility fallback: a global wake must not guess which chat changed. */
    fun changed(version: Long) {
        synchronized(owners) { dirty.addAll(owners.keys) }
        scheduleDrain()
    }

    fun changedChat(chat: String, version: Long) {
        synchronized(owners) { if (chat in owners) dirty.add(chat) }
        scheduleDrain()
    }

    private fun scheduleDrain() {
        val start = synchronized(owners) {
            if (closed || scheduled || dirty.isEmpty()) false else { scheduled = true; true }
        }
        if (start) scheduler.schedule(::drainFrame)
    }

    private fun drainFrame() {
        val chats = synchronized(owners) {
            scheduled = false
            dirty.toList().also { dirty.clear() }
        }
        val live = liveCore ?: return
        for (chat in chats) {
            val apply = synchronized(owners) { owners[chat] } ?: continue
            val (patch, fetches, wasRejected) = synchronized(owners) {
                // An Ack cannot be observed without collecting all earlier Fetch callbacks in the same section.
                Triple(live.drain(chat), requests.remove(chat)?.toList().orEmpty(), rejected.remove(chat))
            }
            apply(patch, fetches, wasRejected)
        }
    }

    internal suspend fun dispatch(action: CoreAction) = queue(action, null)
    internal suspend fun submit(action: CoreAction, sequence: ULong) = queue(action, sequence)

    private suspend fun queue(action: CoreAction, sequence: ULong?) {
        val live = core.await()
        val chat = when (action) {
            is CoreAction.OpenChat -> action.chat
            is CoreAction.CloseChat -> action.chat
            is CoreAction.Chat -> action.chat
            is CoreAction.ContinueFill -> action.chat
            is CoreAction.HubChanged -> action.chat
            is CoreAction.WirePayload -> action.chat
            is CoreAction.Ack -> action.chat
        }
        synchronized(owners) {
            if (closed || chat !in owners) throw CancellationException("Continuity owner was released")
            if (sequence != null) submissions.addLast(Submission(chat, sequence))
            check(live.dispatch(action)) { "Continuity action was not queued" }
            if (sequence != null) check(live.dispatch(CoreAction.Ack(chat, sequence))) { "Continuity acknowledgement was not queued" }
        }
        if (scheduler.synchronous) changedChat(chat, 0)
        scheduler.submitted(live)
    }

    internal fun remove(chat: String) {
        synchronized(owners) {
            owners.remove(chat); requests.remove(chat); dirty.remove(chat); rejected.remove(chat)
            // Already queued Acks stay in FIFO ahead of CloseChat; removing their records would retag later effects.
            if (!closed) liveCore?.dispatch(CoreAction.CloseChat(chat))
        }
    }

    override fun close() {
        val live = synchronized(owners) {
            if (closed) return
            closed = true
            owners.clear(); requests.clear(); dirty.clear(); rejected.clear(); submissions.clear()
            liveCore.also { liveCore = null }
        }
        scope.cancel()
        live?.close()
    }
}

/** Applies keyed patches to the native list. The repository still owns HTTP and WebSocket transport. */
class RustContinuityAdapter(
    chat: String,
    private val driver: RustContinuityDriver,
    private val applied: (ContinuityAdapter.Applied) -> Unit = {},
) : ContinuityAdapter {
    private val owner = "$chat:${ids.incrementAndGet()}"
    private val publicChat = chat
    @Volatile override var state = ContinuityAdapter.State(); private set
    override val behind: Boolean get() = false // HubChanged and fill completion are entirely core-owned.
    override val parallelFolds: Boolean get() = true
    override val asynchronous get() = !driver.synchronous
    private val inputs = Mutex()
    private val metadata = Any()
    private var opened = false
    private var version = 0UL
    private var nextSequence = 0UL
    private var verified = false
    private var reset = false
    private data class Pending(val operation: Long?, val content: Boolean, val after: Boolean, val reload: Boolean,
        val foldId: String?, val requests: MutableList<Action> = mutableListOf())
    private val pending = linkedMapOf<ULong, Pending>()
    private var immediate: ContinuityAdapter.Applied? = null

    init { driver.registerDetailed(owner, ::apply) }

    override suspend fun accept(input: ContinuityAdapter.Input, operation: Long?): ContinuityAdapter.Applied? = inputs.withLock {
        immediate = null
        if (!opened && input !is ContinuityAdapter.Input.Restore) {
            driver.dispatch(CoreAction.OpenChat(owner, CoreSnapshot("[]", null, 0, null, false)))
            opened = true
        }
        val action = when (input) {
            is ContinuityAdapter.Input.Restore -> {
                if (opened) driver.dispatch(CoreAction.CloseChat(owner))
                opened = false
                synchronized(metadata) { reset = true }
                CoreAction.OpenChat(owner, CoreSnapshot(json(input.state.items), input.state.history,
                    input.state.covered.toLong(), input.state.hasMore, input.state.loadedOnce))
            }
            is ContinuityAdapter.Input.Push -> event(ChatEvent.Pushed(batch(input.batch)))
            is ContinuityAdapter.Input.Latest -> event(ChatEvent.Latest(batch(input.batch)))
            is ContinuityAdapter.Input.After -> event(ChatEvent.After(batch(input.batch)))
            is ContinuityAdapter.Input.Reload -> event(ChatEvent.Reloaded(json(input.items), input.history, input.toSeq?.toLong(), input.hasMore))
            is ContinuityAdapter.Input.Older -> event(ChatEvent.Older(json(input.items), input.hasMore))
            is ContinuityAdapter.Input.OpenFold -> event(ChatEvent.OpenFold(input.id))
            is ContinuityAdapter.Input.Range -> event(ChatEvent.Range(input.foldId, json(input.items), input.more))
            ContinuityAdapter.Input.HubChanged -> CoreAction.HubChanged(owner)
            ContinuityAdapter.Input.ContinueFill -> CoreAction.ContinueFill(owner)
            ContinuityAdapter.Input.Reconnect -> event(ChatEvent.Reconnected)
            ContinuityAdapter.Input.LoadOlder -> event(ChatEvent.LoadOlder)
            ContinuityAdapter.Input.FillFailed -> event(ChatEvent.FillFailed)
            ContinuityAdapter.Input.OlderFailed -> event(ChatEvent.OlderFailed)
            is ContinuityAdapter.Input.RangeFailed -> event(ChatEvent.RangeFailed(input.foldId))
        }
        val sequence = synchronized(metadata) {
            (++nextSequence).also { pending[it] = Pending(operation, input.isContent, input is ContinuityAdapter.Input.After,
                input is ContinuityAdapter.Input.Reload || input is ContinuityAdapter.Input.Latest,
                when (input) {
                    is ContinuityAdapter.Input.OpenFold -> input.id
                    is ContinuityAdapter.Input.Range -> input.foldId
                    else -> null
                }) }
        }
        try {
            driver.submit(action, sequence)
            if (input is ContinuityAdapter.Input.Restore) opened = true
        } catch (failure: Throwable) {
            synchronized(metadata) { pending.remove(sequence) }
            throw failure
        }
        if (driver.synchronous) immediate ?: ContinuityAdapter.Applied(publicChat, state, verified = verified) else null
    }

    private fun event(event: ChatEvent) = CoreAction.Chat(owner, event)

    private fun apply(patch: ChatPatch, fetches: List<RustContinuityDriver.Delivery>, rejected: Boolean) {
        val result = synchronized(metadata) {
            val changed = patch.version != version && patch.version != 0UL
            val before = state
            val wasVerified = verified
            if (changed) {
                version = patch.version
                val list = if (reset) mutableListOf() else if (patch.ops.isNotEmpty()) state.items.toMutableList() else null
                reset = false
                if (list != null) for (op in patch.ops) when (op) {
                    is ListOp.Remove -> list.removeAt(op.index.toInt())
                    is ListOp.Insert -> list.add(op.index.toInt(), item(op.id, op.json))
                    is ListOp.Update -> {
                        check(list[op.index.toInt()].id == op.id)
                        list[op.index.toInt()] = item(op.id, op.json)
                    }
                }
                if (list != null || state.history != patch.history || state.covered != patch.covered.toInt() ||
                    state.hasMore != patch.has_more || state.loadedOnce != patch.loaded_once)
                    state = ContinuityAdapter.State(list ?: state.items, patch.history, patch.covered.toInt(), patch.has_more, patch.loaded_once)
                verified = patch.verified
            }
            val stateChanged = state !== before || wasVerified != verified
            val requests = fetches.mapNotNull { delivery ->
                val expectedFold = delivery.sequence?.let { pending[it]?.foldId }
                if (delivery.request is Request.FetchRange && expectedFold != null && delivery.request.fold_id != expectedFold)
                    return@mapNotNull null
                val action = when (val request = delivery.request) {
                    is Request.FetchAfter -> Action.FetchAfter(request.after_seq.toInt())
                    is Request.FetchLatest -> Action.FetchLatest(request.fold_work)
                    is Request.Subscribe -> Action.Subscribe(request.since_seq?.toInt(), request.fold_work)
                    is Request.FetchOlder -> Action.FetchOlder(request.before_seq.toInt(), request.fold_work)
                    is Request.FetchRange -> Action.FetchRange(request.from_seq.toInt(), request.through_seq.toInt())
                }
                delivery.sequence?.let { pending[it]?.requests?.add(action) }
                action
            }
            val acknowledged = patch.acked?.let { ack -> pending.keys.takeWhile { it <= ack } }.orEmpty()
            val completed = acknowledged.mapNotNull { pending.remove(it) }
            if (!changed && requests.isEmpty() && completed.isEmpty() && !rejected && !driver.synchronous) return
            ContinuityAdapter.Applied(publicChat, state, requests = requests, verified = verified,
                operations = completed.mapNotNull { p -> p.operation?.let { ContinuityAdapter.Operation(it, p.requests.toList(), p.content, p.after || p.reload) } },
                reloadApplied = completed.any { it.reload }, afterApplied = completed.any { it.after },
                contentApplied = completed.any { it.content }, stateChanged = stateChanged, rejected = rejected,
                diagnosticOnly = rejected && !changed && requests.isEmpty() && completed.isEmpty())
        }
        if (driver.synchronous) {
            val previous = immediate
            immediate = result.copy(requests = previous?.requests.orEmpty() + result.requests,
                operations = previous?.operations.orEmpty() + result.operations,
                reloadApplied = previous?.reloadApplied == true || result.reloadApplied,
                afterApplied = previous?.afterApplied == true || result.afterApplied,
                contentApplied = previous?.contentApplied == true || result.contentApplied,
                stateChanged = previous?.stateChanged == true || result.stateChanged,
                rejected = previous?.rejected == true || result.rejected,
                diagnosticOnly = result.diagnosticOnly && (previous?.diagnosticOnly != false))
        } else applied(result)
    }

    override fun close() = driver.remove(owner)

    private fun item(id: String, json: String) = JsonCodec.decodeFromString(Item.serializer(), json).also { check(it.id == id) }
    private fun json(items: List<Item>) = items.joinToString(",", "[", "]") { JsonCodec.encodeToString(Item.serializer(), it) }
    private fun batch(b: ContinuityAdapter.Batch) = WireBatch(json(b.items), b.history, b.fromSeq?.toLong(), b.toSeq?.toLong(), b.backlog, b.hasMore, b.moreAfter)

    private companion object { val ids = AtomicLong() }
}
