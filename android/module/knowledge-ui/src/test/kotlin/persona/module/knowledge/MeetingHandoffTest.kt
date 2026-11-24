package persona.module.knowledge

import java.time.Instant
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.api.KnowledgeDetail
import persona.core.contracts.api.KnowledgeSegment
import persona.core.contracts.api.MeetingHandoff
import persona.core.contracts.api.MinutesItem
import persona.core.contracts.api.RecordingMinutes

/** Meeting entry, target and history rules (#10092); no Android UI is needed for these decisions. */
class MeetingHandoffTest {
    private val now = Instant.parse("2026-11-19T12:00:00Z")

    private fun agent(id: String, at: String, machine: String = "mac", extra: Session.() -> Session = { this }) =
        Session(machineId = machine, sessionId = id, title = id, status = "idle", updatedAt = at).extra()

    @Test fun targetsAreInboxHandToAILessEveryMeetingExclusionLatestFirst() {
        val sessions = listOf(
            agent("older", "2026-11-19T10:00:00Z"),
            agent("latest", "2026-11-19T11:00:00Z").copy(status = "working"), // A busy Agent still takes messages.
            agent("persona", "2026-11-19T11:30:00Z").copy(assistant = true),
            agent("offline", "2026-11-19T11:29:00Z").copy(status = "offline"),
            agent("read-only", "2026-11-19T11:28:00Z").copy(
                sourceKind = "codex_app_server", capabilities = SessionCapabilities(read = true, send = false)),
            agent("subagent", "2026-11-19T11:27:00Z").copy(isSubagent = true),
            agent("interrupted", "2026-11-19T11:26:00Z").copy(status = "interrupted"),
            agent("allow-all", "2026-11-19T11:25:00Z").copy(permissionMode = "bypassPermissions"),
            agent("away", "2026-11-19T11:24:00Z", machine = "away"),
            agent("missing-computer", "2026-11-19T11:23:00Z", machine = "missing"),
            agent("sends", "2026-11-19T09:00:00Z").copy(
                sourceKind = "codex_app_server", capabilities = SessionCapabilities(read = true, send = true)),
        )
        val machines = listOf(Machine("mac", online = true), Machine("away", online = false))

        assertEquals(listOf("latest", "older", "sends"), meetingHandoffTargets(sessions, machines).map { it.sessionId })
        assertTrue(meetingHandoffTargets(emptyList(), machines).isEmpty())
    }

    @Test fun entryRequiresAnEndedMeetingMaterialAndAnEligibleTarget() {
        val target = agent("latest", "2026-11-19T11:00:00Z")
        val targets = listOf(target)
        val ready = recording()
        assertTrue(meetingHandoffEligible(ready, targets, now))
        assertFalse(meetingHandoffEligible(ready, emptyList(), now))
        assertFalse(meetingHandoffEligible(recording(minutes = null, chars = 0), targets, now))
        assertFalse(meetingHandoffEligible(recording(endedAt = null, updated = now.toString()), targets, now))
        assertFalse(meetingHandoffEligible(recording(endedAt = null, updated = null), targets, now))
        assertFalse(meetingHandoffEligible(recording(room = "ready"), targets, now))
        assertFalse(meetingHandoffEligible(recording(room = "starting"), targets, now))
        assertFalse(meetingHandoffEligible(recording(room = "unavailable"), targets, now))
        assertTrue(meetingHandoffEligible(recording(room = "ended"), targets, now))
        assertTrue(meetingHandoffEligible(recording(endedAt = null, updated = "2026-11-18T11:59:59Z"), targets, now))
        assertFalse(meetingHandoffEligible(recording(kind = "other"), targets, now))
        assertFalse(meetingHandoffEligible(recording(allDay = true), targets, now))
    }

    @Test fun secondaryGroupUsesOnlyItsOwnQualifiedTargetsAndReceipts() {
        val primary = agent("primary", "2026-11-19T11:00:00Z")
        val secondary = agent("secondary", "2026-11-19T11:30:00Z", machine = "mac~g1234abcd")
        val sessions = listOf(primary, secondary)
        val machines = listOf(Machine("mac", online = true), Machine("mac~g1234abcd", online = true))
        assertEquals(listOf("primary"), meetingHandoffTargets(sessions, machines).map { it.sessionId })
        assertEquals(listOf("secondary"), meetingHandoffTargets(sessions, machines, "g1234abcd").map { it.sessionId })

        val meta = JsonCodec.parseToJsonElement("""{"handoffs":[
            {"id":"h1","at":"2026-11-19T12:00:00Z","machine_id":"mac","session_id":"secondary","state":"sent"}
            ]}""").jsonObject
        assertEquals("mac~g1234abcd", latestMeetingHandoff(meta, "g1234abcd")?.machineId)
    }

    @Test fun materialIsMinutesOrRetainedTranscriptNeverOnlyAnEchoOrTrimmedText() {
        assertTrue(meetingHandoffEligible(recording(minutes = null, chars = 12), listOf(agent("a", "2026-11-19T11:00:00Z")), now))
        assertTrue(meetingHandoffEligible(recording(minutes = null,
            segments = listOf(KnowledgeSegment(text = "Decision")), chars = null), listOf(agent("a", "2026-11-19T11:00:00Z")), now))
        assertFalse(meetingHandoffEligible(recording(minutes = null, chars = null,
            segments = listOf(KnowledgeSegment(text = "Persona answer", echo = true))), listOf(agent("a", "2026-11-19T11:00:00Z")), now))
        assertFalse(meetingHandoffEligible(recording(minutes = null, chars = 100, trimmed = true,
            segments = listOf(KnowledgeSegment(text = "Old transcript"))), listOf(agent("a", "2026-11-19T11:00:00Z")), now))
        // A trimmed meeting ended and keeps its minutes, but has no transcript to offer.
        val trimmed = recording(trimmed = true, endedAt = null, updated = now.toString())
        assertTrue(meetingHandoffEligible(trimmed, listOf(agent("a", "2026-11-19T11:00:00Z")), now))
        assertFalse(meetingHandoffTranscriptOffered(trimmed))
        assertTrue(meetingHandoffTranscriptOffered(recording(minutes = null, chars = 1)))
        assertFalse(meetingHandoffTranscriptOffered(recording(minutes = null, chars = 0)))
    }

    @Test fun latestReadableHandoffSkipsMalformedAndUnknownHistoryWithoutLosingOrder() {
        val meta = JsonCodec.parseToJsonElement("""{"handoffs":[
            {"id":"h1","at":"2026-11-19T10:00:00Z","machine_id":"mac","session_id":"site","title":"Website redesign","state":"sent"},
            "bad",
            {"id":"h2","at":"2026-11-19T11:00:00Z","machine_id":"mac","session_id":"board","transcript":true,"state":"failed","problem":"unavailable"},
            {"id":"missing-required-fields","state":"sent"},
            {"id":"h4","at":"2026-11-19T12:00:00Z","machine_id":"mac","session_id":"other","state":"queued"}
            ]}""").jsonObject

        val last = latestMeetingHandoff(meta)!!

        assertEquals("h2", last.id)
        assertEquals("board", last.sessionId)
        assertEquals(true, last.transcript)
        assertEquals("failed", last.state)
        assertEquals("unavailable", last.problem)
        assertNull(latestMeetingHandoff(buildJsonObject { put("handoffs", "not a list") }))
        assertNull(latestMeetingHandoff(null))
    }

    @Test fun aResultMadeOnThePageStaysUntilANewerDetailResultArrives() {
        val kept = MeetingHandoff("h1", "2026-11-19T10:00:00Z", "mac", "site", state = "sent")
        val made = MeetingHandoff("h2", "2026-11-19T11:00:00Z", "mac", "board",
            title = "Data board", transcript = true, state = "failed", problem = "send_failed")

        assertEquals(made, newerMeetingHandoff(made, kept))
        assertEquals(made, newerMeetingHandoff(made, null))
        assertEquals(kept, newerMeetingHandoff(null, kept))
        assertEquals(made, newerMeetingHandoff(kept, made))
        assertNull(newerMeetingHandoff(null, null))
    }

    private fun recording(
        minutes: RecordingMinutes? = RecordingMinutes(points = listOf(MinutesItem("Ship it"))),
        segments: List<KnowledgeSegment> = emptyList(),
        chars: Int? = null,
        endedAt: String? = "2026-11-19T11:00:00Z",
        updated: String? = null,
        trimmed: Boolean = false,
        room: String? = null,
        kind: String = "recordings",
        allDay: Boolean = false,
    ) = KnowledgeDetail(kind, "r1", "data/recordings/r1", meta = buildJsonObject {
        put("started_at", "2026-11-19T10:00:00Z")
        if (allDay) put("kind", "day")
        endedAt?.let { put("ended_at", it) }
        updated?.let { put("updated", it) }
        chars?.let { put("chars", it) }
        if (trimmed) put("trimmed_at", "2026-11-19T11:30:00Z")
        room?.let { put("room", buildJsonObject { put("state", it) }) }
    }, segments = segments, minutes = minutes)
}
