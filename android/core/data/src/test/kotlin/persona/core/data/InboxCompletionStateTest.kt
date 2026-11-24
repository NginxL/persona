package persona.core.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.domain.inbox.InboxItem
import persona.core.inbox.InboxRecord
import persona.core.inbox.InboxSnapshot

class InboxCompletionStateTest {
    private fun record(version: String = "v1", done: Boolean = false) = InboxRecord(
        InboxItem("a", title = "Synthetic item", version = version, doneAt = if (done) "2026-11-09T00:00:00Z" else null))
    private fun raw(record: InboxRecord) = buildJsonObject {
        put("id", record.item.id); put("version", record.item.version)
        record.item.doneAt?.let { put("done_at", it) }
    }
    private fun InboxCompletionState.publish(record: InboxRecord, epoch: Long) =
        publish(listOf(record), listOf(raw(record)), epoch) { old, records -> old.copy(records = records) }

    @Test fun newMessageInReadStartedBeforeSwipeIsNotHiddenByTheOverlay() {
        for (finishFirst in listOf(false, true)) {
            val current = MutableStateFlow(InboxSnapshot(records = listOf(record())))
            val state = InboxCompletionState(current)
            state.publish(record(), state.epoch())
            val epoch = state.epoch()
            val change = state.begin(setOf("a"), true)
            if (finishFirst) state.finish(change, listOf(record(done = true).let { it to raw(it) }), emptySet())
            state.publish(record("v2"), epoch)
            if (!finishFirst) state.finish(change, listOf(record(done = true).let { it to raw(it) }), emptySet())
            assertEquals("v2", current.value.records.single().item.version)
            assertFalse(current.value.records.single().item.isDone)
            assertEquals("v2", state.cachedItems().single().jsonObject["version"]?.jsonPrimitive?.content)
        }
    }
    @Test fun speculativeStateIsNeverWrittenToTheCacheAndSameVersionReadCannotUndoIt() {
        val current = MutableStateFlow(InboxSnapshot(records = listOf(record())))
        val state = InboxCompletionState(current)
        state.publish(record(), state.epoch())
        val change = state.begin(setOf("a"), true)
        state.publish(record(), state.epoch())
        assertTrue(current.value.records.single().item.isDone)
        assertNull(state.cachedItems().single().jsonObject["done_at"])
        state.finish(change)
        assertFalse(current.value.records.single().item.isDone)
    }
}
