package persona.feature.knowledge

import androidx.activity.ComponentActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubScope
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator

/** The recording page's hand-off behavior; screenshots can be added once its UI exists. */
@OptIn(ExperimentalTestApi::class)
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
class MeetingHandoffScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val entry: SemanticsMatcher = hasText("Hand to Agent…") or hasContentDescription("Hand to Agent")

    @Test fun runningMeetingKeepsTheExistingPageWithoutTheEntry() {
        show(HandoffKnowledge(running = true))
        rule.waitUntilExactlyOneExists(hasText("Ask Persona"), 10_000)
        rule.onAllNodes(entry).assertCountEquals(0)
        rule.onNodeWithText("Ask Persona").assertExists()
    }

    @Test fun missingMaterialKeepsTheExistingPageWithoutTheEntry() {
        show(HandoffKnowledge(minutes = null, segments = emptyList(), chars = 0))
        rule.waitUntilExactlyOneExists(hasText("Ask Persona"), 10_000)
        rule.onAllNodes(entry).assertCountEquals(0)
    }

    @Test fun noEligibleAgentKeepsTheExistingPageWithoutTheEntry() {
        show(sessions = HandoffSessions(emptyList()))
        rule.waitUntilExactlyOneExists(hasText("Ask Persona"), 10_000)
        rule.onAllNodes(entry).assertCountEquals(0)
    }

    @Test fun defaultsChoicesSubmissionImmediateSentRowAndOpenAgentChat() {
        val repository = HandoffKnowledge()
        val navigator = show(repository)
        rule.waitUntilExactlyOneExists(entry, 10_000)
        rule.onNode(entry).performClick()
        rule.waitUntilExactlyOneExists(hasText("Pick an Agent"), 10_000)
        rule.onNodeWithText("It gets this meeting's minutes: the summary, decisions, to-dos and open questions.").assertExists()
        rule.onNode(isToggleable()).assertIsOff()
        rule.onNodeWithText("What should it do? (optional)").performTextInput("Fix the export")
        rule.onNode(isToggleable()).performClick().assertIsOn()
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Website redesign\""), 10_000)

        // Latest is picked without a target click; the line and transcript choice go in the same request.
        assertEquals(MeetingHandoffRequest("mac", "site", "Website redesign", "Fix the export", true), repository.requests[0])
        // The hub answer is shown even though this fake detail never gains a handoffs row.
        rule.onNodeWithText("Handed to \"Website redesign\"").assertExists()

        // Reopening has the privacy-safe default again; changing the Agent changes exactly that target.
        rule.onNode(entry).performClick()
        rule.waitUntilExactlyOneExists(hasText("Pick an Agent"), 10_000)
        rule.onNode(isToggleable()).assertIsOff()
        rule.onNodeWithText("Data board").performClick()
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Data board\""), 10_000)
        assertEquals(MeetingHandoffRequest("mac", "board", "Data board", "", false), repository.requests[1])

        rule.onNodeWithText("Open").performClick()
        assertEquals(listOf(ChatRoute("mac", "board", ConversationKind.Agent)), navigator.routes)
    }

    @Test fun aTrimmedMeetingOffersMinutesButNoTranscriptChoice() {
        val repository = HandoffKnowledge(trimmed = true)
        show(repository)
        rule.waitUntilExactlyOneExists(entry, 10_000)
        rule.onNode(entry).performClick()
        rule.waitUntilExactlyOneExists(hasText("Pick an Agent"), 10_000)
        rule.onAllNodes(isToggleable()).assertCountEquals(0)
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Website redesign\""), 10_000)
        assertEquals(false, repository.requests.single().transcript)
    }

    @Test fun aHubConfirmedFailureStaysInTheSheetWithItsReason() {
        val repository = HandoffKnowledge().apply {
            result = MeetingHandoff("h1", "2026-11-19T12:00:00Z", "mac", "site",
                title = "Website redesign", state = "failed", problem = "unavailable")
        }
        show(repository)
        rule.waitUntilExactlyOneExists(entry, 10_000)
        rule.onNode(entry).performClick()
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Its computer isn't reachable, or the chat is closed."), 10_000)
        rule.onNodeWithText("Send").assertExists() // The sheet stays open so another Agent can be picked.
    }

    @Test fun meetingRefusalsAreExplainedAndCanBeRetried() {
        val repository = HandoffKnowledge()
        show(repository)
        rule.waitUntilExactlyOneExists(entry, 10_000)
        rule.onNode(entry).performClick()
        repository.failure = HandoffRefusal("meeting_running")
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("The meeting is still going. Hand it over once it ends."), 10_000)
        repository.failure = HandoffRefusal("no_material")
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("This meeting has nothing to hand over yet: no minutes or transcript."), 10_000)
        repository.failure = null
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Website redesign\""), 10_000)
    }

    @Test fun retryRestoresTheFailedAgentAndTranscriptButNotItsLine() {
        val repository = HandoffKnowledge(history = """[{
            "id":"h1","at":"2026-11-19T11:00:00Z","machine_id":"mac","session_id":"board",
            "title":"Data board","transcript":true,"state":"failed","problem":"send_failed"}]""")
        show(repository)
        rule.waitUntilExactlyOneExists(hasText("Couldn't hand to \"Data board\""), 10_000)
        rule.onNodeWithText("The message didn't go through.").assertExists()
        rule.onNodeWithText("Retry").performClick()
        rule.waitUntilExactlyOneExists(hasText("Pick an Agent"), 10_000)
        rule.onNode(isToggleable()).assertIsOn()
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Data board\""), 10_000)
        assertEquals(MeetingHandoffRequest("mac", "board", "Data board", "", true), repository.requests.single())
    }

    @Test fun recreationRestoresAnOpenSheetWithoutReplayingAnEarlierSuccess() {
        val repository = HandoffKnowledge()
        val host = handoffHost(repository)
        host.mount()
        rule.waitUntilExactlyOneExists(entry, 10_000)
        rule.onNode(entry).performClick()
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Website redesign\""), 10_000)
        rule.onNode(entry).performClick()
        rule.onNodeWithText("Data board").performClick()
        rule.onNodeWithText("What should it do? (optional)").performTextInput("Keep this line")
        rule.onNode(isToggleable()).performClick()

        rule.activityRule.scenario.recreate()
        host.mount()

        rule.waitUntilExactlyOneExists(hasText("Pick an Agent"), 10_000)
        rule.onNodeWithText("Keep this line").assertExists()
        rule.onNode(isToggleable()).assertIsOn()
        rule.onNodeWithText("Send").assertIsEnabled().performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Data board\""), 10_000)
        assertEquals(MeetingHandoffRequest("mac", "board", "Data board", "Keep this line", true),
            repository.requests[1])
    }

    @Test fun recreationRestoresRetryAgentAndTranscriptChoice() {
        val repository = HandoffKnowledge(history = """[{
            "id":"h1","at":"2026-11-19T11:00:00Z","machine_id":"mac","session_id":"board",
            "title":"Data board","transcript":true,"state":"failed","problem":"send_failed"}]""")
        val host = handoffHost(repository)
        host.mount()
        rule.onNodeWithText("Retry").performClick()
        rule.waitUntilExactlyOneExists(hasText("Pick an Agent"), 10_000)

        rule.activityRule.scenario.recreate()
        host.mount()

        rule.waitUntilExactlyOneExists(hasText("Pick an Agent"), 10_000)
        rule.onNode(isToggleable()).assertIsOn()
        rule.onNodeWithText("Send").assertIsEnabled().performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Data board\""), 10_000)
        assertEquals(MeetingHandoffRequest("mac", "board", "Data board", "", true), repository.requests.single())
    }

    @Test fun secondaryGroupSendsRawIdsAndOpensTheQualifiedAgentChat() {
        val repository = HandoffKnowledge()
        val qualified = "mac~g1234abcd"
        val sessions = HandoffSessions(
            agents = listOf(Session(machineId = qualified, sessionId = "site", title = "Website redesign",
                status = "idle", updatedAt = "2026-11-19T11:00:00Z")),
            machineList = listOf(Machine(qualified, name = "Work Mac", online = true)),
        )
        val navigator = show(repository, sessions, namespace = "g1234abcd")
        rule.waitUntilExactlyOneExists(entry, 10_000)
        rule.onNode(entry).performClick()
        rule.onNodeWithText("Send").performClick()
        rule.waitUntilExactlyOneExists(hasText("Handed to \"Website redesign\""), 10_000)
        assertEquals("mac", repository.requests.single().machineId)
        rule.onNodeWithText("Open").performClick()
        assertEquals(listOf(ChatRoute(qualified, "site", ConversationKind.Agent)), navigator.routes)
    }

    @Test fun everyFailureReasonAndRetryAppearOnTheLatestRow() {
        var handoff by mutableStateOf(MeetingHandoff("h1", "2026-11-19T12:00:00Z", "mac", "site",
            title = "Website redesign", state = "failed", problem = "not_agent"))
        var retries = 0
        val navigator = HandoffNavigator()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                MeetingHandoffRow(handoff, navigator, onRetry = { retries++ })
            }
        }
        for ((problem, reason) in listOf(
            "not_agent" to "That's a Persona chat, not an Agent.",
            "not_allowed" to "This Agent can't take meetings.",
            "unavailable" to "Its computer isn't reachable, or the chat is closed.",
            "send_failed" to "The message didn't go through.",
            "future_problem" to "The message didn't go through.",
        )) {
            rule.runOnIdle { handoff = handoff.copy(problem = problem) }
            rule.onNodeWithText(reason).assertExists()
            rule.onNodeWithText("Retry").performClick()
        }
        assertEquals(5, retries)
        assertTrue(navigator.routes.isEmpty())
    }

    private fun show(
        repository: HandoffKnowledge = HandoffKnowledge(),
        sessions: HandoffSessions = HandoffSessions(),
        namespace: String? = null,
    ): HandoffNavigator = handoffHost(repository, sessions, namespace).also { it.mount() }.navigator

    private fun handoffHost(
        repository: HandoffKnowledge,
        sessions: HandoffSessions = HandoffSessions(),
        namespace: String? = null,
    ) = HandoffHost(repository, sessions, namespace)

    private inner class HandoffHost(
        repository: HandoffKnowledge,
        sessions: HandoffSessions,
        namespace: String?,
    ) {
        private val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
        private val model = KnowledgeDetailViewModel(KnowledgeKind.Recordings, "r1", connection, NoHandoffSender,
            sessions = sessions, namespace = namespace)
        val navigator = HandoffNavigator()
        fun mount() {
            rule.setContent { PersonaTheme(reduceMotion = true) { KnowledgeDetailScreen(model, navigator, openLinkInApp = {}) } }
        }
    }
}

private class HandoffKnowledge(
    var minutes: RecordingMinutes? = RecordingMinutes(points = listOf(MinutesItem("Ship it"))),
    var segments: List<KnowledgeSegment> = listOf(KnowledgeSegment(text = "A retained transcript")),
    var chars: Int? = 20,
    var running: Boolean = false,
    var trimmed: Boolean = false,
    var history: String? = null,
) : KnowledgeRepository {
    override val scope = HubScope("demo", "handoff-screen")
    val requests = mutableListOf<MeetingHandoffRequest>()
    var result: MeetingHandoff? = null
    var failure: Exception? = null

    override suspend fun list(kind: KnowledgeKind, tag: String?) = KnowledgeList()
    override suspend fun search(query: String) = emptyList<KnowledgeHit>()
    override suspend fun detail(kind: KnowledgeKind, id: String) = KnowledgeDetail("recordings", id, "data/recordings/$id",
        meta = buildJsonObject {
            put("title", "Weekly sync")
            put("started_at", "2026-11-19T10:00:00Z")
            // A fixed timestamp eventually leaves the twelve-hour running window.
            if (running) put("updated", java.time.Instant.now().toString()) else put("ended_at", "2026-11-19T11:00:00Z")
            chars?.let { put("chars", it) }
            if (trimmed) put("trimmed_at", "2026-11-19T11:30:00Z")
            history?.let { put("handoffs", Json.parseToJsonElement(it)) }
        }, segments = segments, minutes = minutes)

    override suspend fun handOff(id: String, request: MeetingHandoffRequest): MeetingHandoff {
        requests += request
        failure?.let { throw it }
        return result ?: MeetingHandoff("made-${requests.size}", "2026-11-19T12:0${requests.size}:00Z",
            request.machineId, request.sessionId, request.title, request.transcript, "sent")
    }
}

private class HandoffSessions(
    agents: List<Session> = listOf(
        Session(machineId = "mac", sessionId = "board", title = "Data board", status = "idle", updatedAt = "2026-11-19T10:00:00Z"),
        Session(machineId = "mac", sessionId = "site", title = "Website redesign", status = "idle", updatedAt = "2026-11-19T11:00:00Z"),
    ),
    machineList: List<Machine> = listOf(Machine("mac", name = "Work Mac", online = true)),
) : SessionsRepository {
    override val machines = MutableStateFlow(machineList)
    override val sessions = MutableStateFlow(agents)
    override suspend fun refresh() = Unit
}

private class HandoffNavigator : Navigator {
    val routes = mutableListOf<NavKey>()
    override fun navigate(key: NavKey) { routes += key }
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}

private object NoHandoffSender : MainConversationSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
}

private class HandoffRefusal(override val code: String) : Exception(code), HubCodedFailure {
    override val status = 409
}
