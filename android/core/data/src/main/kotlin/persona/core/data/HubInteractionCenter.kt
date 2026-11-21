package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.domain.answer.Delegated
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.encodeToString
import java.time.Instant

class HubInteractionCenter(
    private val transport: ChatTransport,
    connection: HubConnection,
    private val scope: CoroutineScope,
    private val snapshots: ListSnapshotStore? = null,
    /** The hub's sessions: where an agent stood when its card was answered here ([answered]). */
    private val sessions: StateFlow<List<Session>>? = null,
    /** When a card is answered here ([AnsweredMark.at]). */
    private val clock: () -> Instant = Instant::now,
) : InteractionCenter {
    private val mutablePending = MutableStateFlow<List<Interaction>>(emptyList())
    override val pending = mutablePending.asStateFlow()
    private val mutableArrivals = MutableSharedFlow<Interaction>(extraBufferCapacity = 16, onBufferOverflow = BufferOverflow.DROP_OLDEST)
    override val arrivals = mutableArrivals.asSharedFlow()
    private val mutableAnswered = MutableStateFlow<Map<SessionKey, AnsweredMark>>(emptyMap())
    /** The agents a card was answered for here in the last [ANSWERED_MILLIS] (#4525, [answeredSessions]). */
    val answered = mutableAnswered.asStateFlow()
    private val mutableDelegated = MutableStateFlow<List<DelegatedAnswer>>(emptyList())
    override val delegated = mutableDelegated.asStateFlow()
    private val mutableReanswered = MutableStateFlow<Map<String, Map<String, String>>>(emptyMap())
    override val reanswered = mutableReanswered.asStateFlow()
    private val lock = Mutex()
    private val refreshing = Mutex()
    private var revision = 0L
    private val changes = mutableMapOf<String, Pair<Long, Interaction?>>()
    /**
     * Requests that ended here (answered, gone, resolved by an event): a snapshot that still lists one, fetched or
     * served before the hub caught up, does not bring its card back (#1688). Only a live event for the id does.
     */
    private val ended = LinkedHashSet<String>()
    /**
     * Of those, the ones this device ended (answered, found over): the hub still lists one until the node ends it, and
     * the pending-card snapshot after a hello that resumes sends it again as an `interaction` frame (#2767). A frame for
     * one never brings it back; one the hub withdrew may be raised again under the same id (as the web's `endedHere`).
     */
    private val endedHere = LinkedHashSet<String>()
    /** Cards the hub withdrew (interaction_resolved), newest last: the event often comes before the answer's own reply. */
    private val withdrawn = LinkedHashSet<String>()
    private var discarded = emptySet<String>()

    private val restored = scope.async {
        lock.withLock {
            // Unlike a cache, dismissal must not silently forget a failed read or write.
            discarded = snapshots?.read("discarded-interactions")?.let { JsonCodec.decodeFromString<Set<String>>(it) }.orEmpty()
            snapshots.restore<List<Interaction>>("interactions")?.let { list ->
                mutablePending.value = list.filterNot { it.id in discarded }
            }
        }
    }

    init {
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.events.collect { event -> restored.await(); lock.withLock {
                when (event) {
                    is InteractionEvent -> if (event.interaction.id !in discarded && event.interaction.id !in endedHere) {
                        val previous = mutablePending.value.firstOrNull { it.id == event.interaction.id }
                        // Looking is quiet. Its first handover is an arrival; any later edit of that copy is not.
                        val announces = !event.interaction.isDelegating && (previous == null || previous.isDelegating)
                        ended.remove(event.interaction.id); change(event.interaction.id, event.interaction)
                        if (announces) mutableArrivals.tryEmit(event.interaction)
                    }
                    is InteractionResolvedEvent -> {
                        // The assistant's answer to a question whose card this device had: it stays in its chat.
                        if (event.byAssistant) mutablePending.value.firstOrNull { it.id == event.id }
                            ?.let { keepDelegated(it, event.answers, event.reason.orEmpty()) }
                        withdrew(event.id); end(event.id)
                    }
                    is InteractionReansweredEvent -> noteReanswer(event.id, event.answers)
                    else -> Unit
                }
            } }
        }
        scope.launch {
            // A resumed socket already had the pending snapshot and every answer missed (#1574).
            connection.state.collect { if (it == ConnectionState.Online && !connection.resumed) quietly { refresh() } }
        }
    }

    override suspend fun refresh() = refreshing.withLock {
        restored.await()
        val before = lock.withLock { revision }
        val fetched = transport.interactions()
        lock.withLock {
            val merged = fetched.filterNot { it.id in ended || it.id in discarded }.associateBy { it.id }.toMutableMap()
            changes.filterValues { it.first > before }.forEach { (id, change) ->
                if (change.second == null) merged.remove(id) else merged[id] = change.second!!
            }
            mutablePending.value = merged.values.toList()
            snapshots.save("interactions", mutablePending.value)
        }
    }

    override suspend fun resolve(id: String, resolution: Resolution) { resolveOutcome(id, resolution) }

    override suspend fun discardLocal(id: String) {
        restored.await()
        lock.withLock {
            val disk = checkNotNull(snapshots) { "Local dismissal storage is unavailable" }
            val next = discarded + id
            disk.write("discarded-interactions", JsonCodec.encodeToString(next))
            discarded = next
            end(id)
        }
    }

    override suspend fun resolveOutcome(id: String, resolution: Resolution): ResolveOutcome =
        resolveOutcome(id, resolution, handoffCard = false)

    override suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean): ResolveOutcome {
        restored.await()
        // Whose card it is and the hub's state for them now, before an interaction_resolved can take the card away.
        val card = pending.value.firstOrNull { it.id == id }
        val asked = card?.sessionKey
        val since = asked?.let { key -> sessions?.value?.firstOrNull { it.key == key }?.stateSince }
        val marked = handoffCard && resolution.allow == true && card?.isSecretHandoff == true
        val outcome = try {
            // Every ordinary answer keeps the old overload: delegated transports must not bypass their existing path.
            if (marked) transport.resolveOutcome(id, resolution, handoffCard = true)
            else transport.resolveOutcome(id, resolution)
        } catch (gone: InteractionGoneException) {
            lock.withLock { end(id, here = true) }
            throw gone
        }
        val seen = lock.withLock {
            end(id, here = true)
            // The assistant answered first: its answer shows in the chat, where the user may change it (iOS `resolve`).
            val effective = outcome.resolution
            if (outcome.already && card != null && effective?.answeredBy == DelegatedAnswer.ASSISTANT) {
                keepDelegated(card, effective.answers.orEmpty(), effective.reason.orEmpty())
            }
            id in withdrawn
        }
        // Answered (now, or already): its agent goes on. An expired or withdrawn card (gone) says nothing of that, and a
        // card handed back to the computer, now or already (a second tap), leaves the agent waiting there (#709, as the
        // web's card-answer.ts).
        val handedBack = if (outcome.already) outcome.status == "dismissed" else resolution.dismiss == true
        if (!handedBack) asked?.let { answered(it, AnsweredMark(since, id, pending = !outcome.already && !seen, at = clock())) }
        return outcome
    }

    override suspend fun replyOwnerApproval(interaction: Interaction, phrase: String) {
        restored.await()
        transport.replyOwnerApproval(interaction, phrase)
        lock.withLock { end(interaction.id, here = true) }
    }

    override fun dropDelegated(id: String) = mutableDelegated.update { list -> list.filterNot { it.id == id } }

    override suspend fun reanswer(id: String, answers: Map<String, String>, manager: String?) {
        transport.reanswer(id, answers, manager)
        noteReanswer(id, answers)
    }

    /** A question the assistant answered, once, the oldest put away past [Delegated.KEPT] (iOS `keepDelegated`). */
    private fun keepDelegated(card: Interaction, answers: Map<String, String>, reason: String) {
        if (card.kind != "question") return
        mutableDelegated.update { list ->
            if (list.any { it.id == card.id }) list else (list + DelegatedAnswer(card, answers, reason, clock())).takeLast(Delegated.KEPT)
        }
    }

    /** The hub names only the questions that changed: an earlier change to another question of the card stays. */
    private fun noteReanswer(id: String, answers: Map<String, String>) {
        if (answers.isEmpty()) return
        mutableReanswered.update { it + (id to it[id].orEmpty() + answers) }
    }

    private fun answered(key: SessionKey, mark: AnsweredMark) {
        mutableAnswered.update { it + (key to mark) }
        scope.launch {
            delay(ANSWERED_MILLIS)
            // A later answer for the same agent keeps its own time.
            mutableAnswered.update { if (it[key]?.answer === mark.answer) it - key else it }
        }
    }

    /** The hub withdrew a card: an answer here for it is no longer waiting on that (under [lock]). */
    private fun withdrew(id: String) {
        withdrawn.remove(id)
        withdrawn.add(id)
        while (withdrawn.size > ENDED_LIMIT) withdrawn.remove(withdrawn.first())
        mutableAnswered.update { marks ->
            if (marks.values.none { it.card == id && it.pending }) marks
            else marks.mapValues { (_, mark) -> if (mark.card == id && mark.pending) mark.withdrawn() else mark }
        }
    }

    private suspend fun end(id: String, here: Boolean = false) {
        ended.remove(id)
        ended.add(id)
        // The bound only keeps a long-running app from holding every answer it ever gave.
        while (ended.size > ENDED_LIMIT) ended.remove(ended.first())
        if (here) {
            endedHere.remove(id)
            endedHere.add(id)
            while (endedHere.size > ENDED_LIMIT) endedHere.remove(endedHere.first())
        }
        change(id, null)
    }

    private suspend fun change(id: String, interaction: Interaction?) {
        changes[id] = ++revision to interaction
        mutablePending.update { list -> list.filterNot { it.id == id } + listOfNotNull(interaction) }
        snapshots.save("interactions", mutablePending.value)
    }

    internal companion object {
        const val ENDED_LIMIT = 512
        /** As the web's answered cards (store.ts ANSWERED_MS): long enough for the node's next sessions push. */
        const val ANSWERED_MILLIS = 20_000L
    }
}
