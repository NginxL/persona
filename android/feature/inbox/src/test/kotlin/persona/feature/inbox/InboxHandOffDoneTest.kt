package persona.feature.inbox

import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.StateRestorationTester
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentSession
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import persona.core.navigation.RouteSource

/**
 * "Hand to AI" files an item as done only when the user's own switch says so, and the conversation it opens knows the
 * item it came from (docs/android/android.md "Hand to AI"; docs/design proposals for inbox, design 9).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class InboxHandOffDoneTest {
    @get:Rule val rule = createComposeRule()
    private val navigator = DoneTestNavigator()

    private fun show(repository: DoneTestRepository, dismissed: () -> Unit = {}) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxHandOffSheet(repository, repository.record, navigator, onDismiss = dismissed)
            }
        }
    }

    private fun toggle() = rule.onNode(isToggleable())

    /**
     * The remembered Agent is only preselected: without touching the switch, sending to it files nothing as done. The
     * automatic "on for an Agent" this replaces marked every such hand-off done.
     */
    @Test fun aRememberedAgentIsNotMarkedDoneUnlessTheSwitchIsTurnedOn() {
        val repository = DoneTestRepository(mutes = listOf(InboxMuteOption("sender", "me@example.com", "Me")))
        repository.handOffMemory.value = listOf(HandOffMemoryEntry("mail|sender:me@example.com", "agent:laptop/agent"))
        show(repository)
        rule.waitForIdle()
        rule.onNodeWithText("Last time").assertExists()
        toggle().assertIsOff()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(0, repository.doneCalls)
        assertEquals(1, repository.requests.size)
        assertEquals(listOf(ChatRoute("laptop", "agent", ConversationKind.Agent,
            source = RouteSource.InboxItem("mail:example"))), navigator.routes)
    }

    /** An explicit off stays off across target changes, and the item is not filed done. */
    @Test fun anExplicitlyChosenOffSurvivesATargetChange() {
        val repository = DoneTestRepository()
        show(repository)
        toggle().performClick().assertIsOn()
        toggle().performClick().assertIsOff()
        rule.onNodeWithText("Example agent").performClick()
        toggle().assertIsOff()
        rule.onNodeWithText("Persona").performClick()
        toggle().assertIsOff()
        rule.onNodeWithText("Example agent").performClick()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(0, repository.doneCalls)
    }

    /** An explicit on stays on across target changes and files the item once, whichever target finally gets it. */
    @Test fun anExplicitlyChosenOnSurvivesATargetChangeAndFilesTheItemOnce() {
        val repository = DoneTestRepository()
        show(repository)
        toggle().performClick().assertIsOn()
        rule.onNodeWithText("Example agent").performClick()
        toggle().assertIsOn()
        rule.onNodeWithText("Persona").performClick()
        toggle().assertIsOn()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(1, repository.doneCalls)
        assertEquals(1, repository.requests.size)
    }

    /** The user's choice is saved state, not plain state: a sheet rebuilt after a rotation keeps it. */
    @Test fun theUsersChoiceSurvivesARecreationOfTheSheet() {
        val repository = DoneTestRepository()
        val restoration = StateRestorationTester(rule)
        restoration.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxHandOffSheet(repository, repository.record, navigator, onDismiss = {})
            }
        }
        toggle().performClick().assertIsOn()
        restoration.emulateSavedInstanceStateRestore()
        toggle().assertIsOn()
        rule.onNodeWithText("Example agent").performClick()
        toggle().assertIsOn()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(1, repository.doneCalls)
    }

    /** An item already filed as done has no switch and keeps it that way. */
    @Test fun anAlreadyDoneItemHasNoSwitch() {
        val repository = DoneTestRepository(done = true)
        show(repository)
        rule.onAllNodes(isToggleable()).assertCountEquals(0)
        rule.onNodeWithText("Example agent").performClick()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(0, repository.doneCalls)
        assertEquals(listOf(ChatRoute("laptop", "agent", ConversationKind.Agent,
            source = RouteSource.InboxItem("mail:example"))), navigator.routes)
    }

    private class DoneTestRepository(
        mutes: List<InboxMuteOption> = emptyList(),
        done: Boolean = false,
    ) : InboxRepository {
        val record = InboxRecord(
            InboxItem("mail:example", title = "Project update", preview = "Please review the plan.",
                doneAt = if (done) "2026-11-12T08:00:00Z" else null),
            source = "mail", mutes = mutes)
        override val handOffMemory = MutableStateFlow(emptyList<HandOffMemoryEntry>())
        override suspend fun saveHandOffMemory(value: List<HandOffMemoryEntry>) { handOffMemory.value = value }
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(record), sessions = listOf(
            AgentSession(Session(machineId = "laptop", sessionId = "agent", title = "Example agent", status = "idle")),
        )))
        override val connectionState = flowOf(ConnectionState.Online)
        val requests = mutableListOf<InboxHandOffRequest>()
        var doneCalls = 0
        override suspend fun handOff(request: InboxHandOffRequest): SessionKey? {
            requests += request
            return null
        }
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) { doneCalls++ }
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private class DoneTestNavigator : Navigator {
        val routes = mutableListOf<NavKey>()
        val tabs = mutableListOf<AppTab>()
        override fun navigate(key: NavKey) { routes += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) { tabs += tab }
    }
}
