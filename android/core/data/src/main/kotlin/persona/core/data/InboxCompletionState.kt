package persona.core.data

import java.time.Instant
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.serialization.json.*
import persona.core.inbox.InboxRecord
import persona.core.inbox.InboxSnapshot

/** Writes do not hold the full-list network lock. Generations keep an older list from undoing them. */
internal class InboxCompletionState(private val current: MutableStateFlow<InboxSnapshot>) {
    class Change(val before: List<InboxRecord>, val ids: Set<String>)
    private var generation = 0L
    private val revisions = mutableMapOf<String, Long>()
    private val originalVersions = mutableMapOf<String, String?>()
    private val active = mutableMapOf<String, Change>()
    private val committed = linkedMapOf<String, JsonObject>()
    private class ItemLock(val mutex: Mutex = Mutex(), var users: Int = 0)
    private val itemLocks = mutableMapOf<String, ItemLock>()
    private val requests = Semaphore(MAX_CONCURRENT_COMPLETIONS)

    suspend fun <T> withItems(ids: List<String>, block: suspend () -> T): T {
        val locks = synchronized(this) { ids.distinct().sorted().map { key ->
            key to itemLocks.getOrPut(key) { ItemLock() }.also { it.users++ }
        } }
        var acquired = 0
        try {
            for ((_, lock) in locks) { lock.mutex.lock(); acquired++ }
            return block()
        } finally {
            locks.take(acquired).asReversed().forEach { it.second.mutex.unlock() }
            synchronized(this) { locks.forEach { (key, lock) -> if (--lock.users == 0) itemLocks.remove(key) } }
        }
    }
    suspend fun <T> request(block: suspend () -> T): T = requests.withPermit { block() }

    @Synchronized fun epoch() = generation
    @Synchronized fun reset() {
        generation++
        revisions.clear(); originalVersions.clear(); active.clear(); committed.clear()
    }
    @Synchronized fun begin(ids: Set<String>, done: Boolean): Change {
        val change = Change(current.value.records.filter { it.item.id in ids }, ids)
        val doneAt = if (done) Instant.now().toString() else null
        ids.forEach { id ->
            active[id] = change
            originalVersions[id] = change.before.firstOrNull { it.item.id == id }?.item?.version
            revisions[id] = ++generation
        }
        current.update { state -> state.copy(records = state.records.map { record ->
            if (record.item.id in ids) record.copy(item = record.item.copy(doneAt = doneAt)) else record
        }) }
        return change
    }

    /** A newer version observed during a write wins over both its response and its rollback. */
    @Synchronized fun finish(
        change: Change, replacements: List<Pair<InboxRecord, JsonObject>> = emptyList(),
        restore: Set<String> = change.before.map { it.item.id }.toSet(), missing: Set<String> = emptySet(),
    ) {
        val before = change.before.associateBy { it.item.id }
        val changed = replacements.associateBy { it.first.item.id }
        current.update { state -> state.copy(records = state.records.mapNotNull { record ->
            val id = record.item.id
            val old = before[id]
            if (active[id] !== change || old == null || record.item.version != old.item.version) return@mapNotNull record
            when {
                id in missing -> { committed.remove(id); null }
                id in changed -> changed.getValue(id).let { (next, raw) -> committed[id] = raw; next }
                id in restore -> old
                else -> record.also {
                    // "Unchanged" means already at the requested state, and supplies no item body.
                    committed[id]?.let { raw -> committed[id] = JsonObject(raw + ("done_at" to (record.item.doneAt?.let(::JsonPrimitive) ?: JsonNull))) }
                }
            }
        }) }
        change.ids.forEach { id -> if (active[id] === change) { active.remove(id); revisions[id] = ++generation } }
    }

    @Synchronized fun publish(
        records: List<InboxRecord>, raw: List<JsonObject>, started: Long,
        update: (InboxSnapshot, List<InboxRecord>) -> InboxSnapshot,
    ) {
        val now = current.value.records.associateBy { it.item.id }
        fun protected(id: String, fetched: InboxRecord?): Boolean {
            if ((revisions[id] ?: 0) <= started && id !in active) return false
            // A different message version is authoritative even when this read began before the swipe.
            val original = originalVersions[id]
            if (fetched != null && fetched.item.version != original && now[id]?.item?.version == original) return false
            return true
        }
        val seen = records.map { it.item.id }.toSet()
        val merged = records.zip(raw).mapNotNull { (record, body) ->
            val id = record.item.id
            if (protected(id, record)) now[id]
            else record.also { committed[id] = body }
        } + now.values.filter { it.item.id !in seen && protected(it.item.id, null) }
        committed.keys.retainAll(merged.map { it.item.id }.toSet())
        revisions.entries.removeAll { it.value <= started && it.key !in active }
        originalVersions.keys.retainAll(revisions.keys)
        current.update { update(it, merged) }
    }
    private companion object { const val MAX_CONCURRENT_COMPLETIONS = 4 }

    @Synchronized fun cachedItems() = JsonArray(committed.values.toList())
}
