@file:OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)

package persona.module.knowledge

import androidx.lifecycle.ViewModelStore
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import java.io.File
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.HubScope
import persona.core.contracts.api.*
import persona.core.designsystem.UiText

/** A recording's minutes on its page (#2738): which state shows, where a time points, and the writes' read-back. */
class KnowledgeMinutesTest {
    private fun detail(meta: JsonObject, minutes: RecordingMinutes? = null) =
        KnowledgeDetail("recordings", "r1", "data/recordings/r1", meta = meta, minutes = minutes)

    private fun meta(state: String? = null, complete: Boolean = true, kind: String? = null, trimmed: Boolean = false) = buildJsonObject {
        put("started_at", "2026-11-05T14:00:00Z"); put("complete", complete)
        state?.let { put("minutes_state", it) }
        kind?.let { put("kind", it) }
        if (trimmed) put("trimmed_at", "2026-11-09T00:00:00Z")
    }

    @Test fun minutesViewFollowsTheWebsRules() {
        val minutes = RecordingMinutes(version = 2, points = listOf(MinutesItem("Ship on Friday", 62.0)))
        fun view(meta: JsonObject, minutes: RecordingMinutes? = null) = minutesView(detail(meta, minutes), RecordingFacts.of(meta))

        // Minutes: shown; newer ones on their way or failed say so, and Regenerate waits for a complete transcript.
        assertEquals(MinutesView(MinutesPhase.Ready, minutes, canMake = true), view(meta("ready"), minutes))
        assertEquals(MinutesView(MinutesPhase.Ready, minutes, updating = true), view(meta("pending"), minutes))
        assertEquals(MinutesView(MinutesPhase.Ready, minutes, failed = true, canMake = true), view(meta("failed"), minutes))
        assertEquals(MinutesView(MinutesPhase.Ready, minutes), view(meta("ready", complete = false), minutes))
        // An expired transcript keeps its minutes and can't make new ones; without minutes there is nothing to show.
        assertEquals(MinutesView(MinutesPhase.Ready, minutes, trimmed = true), view(meta("ready", trimmed = true), minutes))
        assertNull(view(meta(trimmed = true)))

        // None yet.
        assertEquals(MinutesView(MinutesPhase.Writing), view(meta("pending")))
        assertEquals(MinutesView(MinutesPhase.Failed, canMake = true), view(meta("failed")))
        assertEquals(MinutesView(MinutesPhase.Failed), view(meta("failed", complete = false)))
        assertEquals(MinutesView(MinutesPhase.Waiting), view(meta(complete = false, kind = "meeting")))
        assertNull(view(meta(complete = false)))
        assertEquals(MinutesView(MinutesPhase.Offer, canMake = true), view(meta()))
    }

    @Test fun waitsForTheFinalPassLikeTheOtherClients() {
        val fixture = File(System.getProperty("persona.repo.root"), "spec/fixtures/minutes-wait/cases.json")
        val cases = Json.parseToJsonElement(fixture.readText()).jsonObject.getValue("cases").jsonArray
        for (entry in cases) {
            val case = entry.jsonObject
            val name = case.string("name")
            val meta = buildJsonObject {
                put("complete", true)
                put("minutes_state", case.getValue("minutes_state"))
                put("transcription", case["transcription"] ?: JsonNull)
            }
            val shown = RecordingMinutes(version = 2).takeIf { case.string("has_minutes") == "true" }
            val view = minutesView(detail(meta, shown), RecordingFacts.of(meta))
            val expected = case.getValue("expected").jsonObject
            assertEquals(name, expected.string("line"), when (view?.phase) {
                MinutesPhase.Checking -> "checking"
                MinutesPhase.Writing -> "writing"
                else -> "none"
            })
            assertEquals(name, expected.string("done_minutes").toIntOrNull(), view?.progress?.done)
            assertEquals(name, expected.string("total_minutes").toIntOrNull(), view?.progress?.total)
            if (shown != null) assertEquals(name, shown, view?.minutes)
        }
    }

    @Test fun anItemsTimePointsAtTheLineThatWasSpeaking() {
        val lines = listOf(KnowledgeSegment(1.0, 4.0, "a"), KnowledgeSegment(95.5, 99.0, "b"), KnowledgeSegment(612.0, 618.0, "c"))
        assertEquals(0, segmentAt(lines, 0.0))
        assertEquals(0, segmentAt(lines, 60.0))
        // Half a second early still counts: the minutes round times the transcript keeps to the tenth.
        assertEquals(1, segmentAt(lines, 95.0))
        assertEquals(1, segmentAt(lines, 300.0))
        assertEquals(2, segmentAt(lines, 9_000.0))
        assertNull(segmentAt(emptyList(), 10.0))
        assertEquals("segment/2", segmentKey(2))
    }

    @Test fun writesSendTheShownVersionAndReadTheRecordingBack() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            val repository = MinutesKnowledge()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val model = KnowledgeDetailViewModel(KnowledgeKind.Recordings, "r1", connection, NoMinutesSender)
            store.put("detail", model)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect() }
            runCurrent()
            assertEquals(1, repository.reads)
            assertEquals(3, model.page.value.detail?.minutes?.version)

            // Add to to-dos: the version shown goes with it; nothing else is sent while it (and its read-back) is on its way.
            val gate = CompletableDeferred<Unit>().also { repository.gate = it }
            model.addMinutesTodos(listOf("a1"))
            runCurrent()
            assertTrue(model.minutesWork.value.busy)
            model.addMinutesTodos(listOf("a2")); model.makeMinutes()
            gate.complete(Unit); runCurrent()
            assertEquals(listOf("todos r1 3 [a1]"), repository.writes)
            assertEquals(2, repository.reads)
            assertEquals(KnowledgeMinutesWork(), model.minutesWork.value)
            assertTrue(model.page.value.detail!!.minutes!!.actions.single { it.id == "a1" }.added)

            // Rewritten since they were shown: 409 minutes_stale says so and shows the new ones; nothing is sent again.
            repository.version = 4
            model.refresh(); runCurrent()
            repository.version = 5
            model.addMinutesTodos(listOf("a2")); runCurrent()
            assertEquals("todos r1 4 [a2]", repository.writes.last())
            assertEquals(UiText.Res(R.string.knowledge_ui_minutes_stale), model.minutesWork.value.message)
            assertFalse(model.minutesWork.value.busy)
            assertEquals(5, model.page.value.detail?.minutes?.version)
            model.dismissMinutesMessage()
            assertNull(model.minutesWork.value.message)

            // Make minutes: queued, then the page shows them being written.
            model.makeMinutes(); runCurrent()
            assertEquals("make r1", repository.writes.last())
            assertEquals("pending", model.page.value.detail?.meta.string("minutes_state"))
            assertEquals(3, repository.writes.size)

            // Another hub: a write still on its way says nothing on the new one's page, which takes writes at once.
            val other = CompletableDeferred<Unit>().also { repository.gate = it }
            model.addMinutesTodos(listOf("a3")); runCurrent()
            assertTrue(model.minutesWork.value.busy)
            val next = MinutesKnowledge(HubScope("demo", "next"))
            connection.current.value = next; runCurrent()
            assertEquals(KnowledgeMinutesWork(), model.minutesWork.value)
            other.complete(Unit); runCurrent()
            assertEquals(KnowledgeMinutesWork(), model.minutesWork.value)
            model.addMinutesTodos(listOf("a1")); runCurrent()
            assertEquals(listOf("todos r1 3 [a1]"), next.writes)
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }
}

private class MinutesKnowledge(override val scope: HubScope = HubScope("demo", "minutes")) : KnowledgeRepository {
    var version = 3
    var reads = 0
    var gate: CompletableDeferred<Unit>? = null
    val writes = mutableListOf<String>()
    private val added = mutableSetOf<String>()
    private var state = "ready"

    override suspend fun list(kind: KnowledgeKind, tag: String?) = KnowledgeList()
    override suspend fun search(query: String) = emptyList<KnowledgeHit>()
    override suspend fun detail(kind: KnowledgeKind, id: String): KnowledgeDetail {
        reads++
        return KnowledgeDetail("recordings", id, "data/recordings/$id",
            meta = buildJsonObject { put("started_at", "2026-11-05T14:00:00Z"); put("complete", true); put("minutes_state", state) },
            minutes = RecordingMinutes(version = version, actions = listOf("a1", "a2", "a3").map { action ->
                MinutesAction(action, what = "Step $action", todoId = if (action in added) "todo-$action" else null)
            }))
    }
    override suspend fun makeMinutes(id: String) {
        writes += "make $id"
        state = "pending"
    }
    override suspend fun addMinutesTodos(id: String, version: Int, actions: List<String>) {
        gate?.also { gate = null }?.await()
        writes += "todos $id $version $actions"
        if (version != this.version) throw StaleMinutes()
        added += actions
    }
}

private class StaleMinutes : Exception("The minutes changed"), HubCodedFailure {
    override val status = 409
    override val code = "minutes_stale"
}

private object NoMinutesSender : MainConversationSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
}
