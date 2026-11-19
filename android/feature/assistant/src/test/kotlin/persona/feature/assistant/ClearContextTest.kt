package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.SessionCompaction
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.LocalHubFeatures
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.feature.chat.LocalMainChatClear

/**
 * Clear context for the main conversation (#5100, the hub's and the web's #5095): in its title menu only while the hub
 * lists `assistant_main_clear`, and never in the demo or on another conversation; no confirmation; the page then stays a
 * conversation page, empty, with the composer at the bottom; a refusal by another device's change shows what the hub has,
 * any other failure says why and keeps the conversation. The composer's /clear reaches the same action.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ClearContextTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val main = Session(machineId = "mac", sessionId = "main", title = "Main", assistant = true)
    private val side = Session(machineId = "mac", sessionId = "side", title = "Trip plan", assistant = true)
    private val next = Session(machineId = "mac", sessionId = "next", title = "Next", assistant = true)
    private val notices = mutableListOf<Pair<String, FeedbackStyle?>>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun show(hub: Hub, features: Set<String> = setOf(ClearMainFeature), open: Session = main, demo: Boolean = false) {
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides features, LocalAppFeedback provides object : AppFeedback {
                override fun failure(error: Throwable, operation: Boolean) = false
                override fun notice(message: String): Boolean { notices += message to null; return true }
                override fun notice(message: String, style: FeedbackStyle): Boolean { notices += message to style; return true }
            }) {
                AssistantScreen(navigator, hub, hub, startConversation = { error("Unexpected creation") }, demo = demo,
                    requestedSession = open.key, management = hub, mainConversationSender = hub, composerService = null,
                    chatContent = { key ->
                        Text("chat:${key.sessionId}")
                        // What the conversation's composer gets for its /clear.
                        LocalMainChatClear.current?.let { clear ->
                            Text("composer-clear")
                            hub.composerClear = clear
                        }
                })
            }
        }
        rule.onNodeWithText("chat:${open.sessionId}").assertExists()
    }

    private fun menu() { rule.onNodeWithText("Persona").performClick(); rule.waitForIdle() }

    @Test fun theMenuOffersItOnTheMainConversationWhenTheHubDoes() {
        show(Hub(), features = setOf(CompactFeature, NewMainFeature, ClearMainFeature))
        menu()
        rule.onNodeWithText("Compact context").assertExists()
        rule.onNodeWithText("Clear context").assertExists()
        rule.onNodeWithText("New main chat").assertExists()
    }

    @Test fun aHubWithoutTheFeatureShowsNoClearContext() {
        val hub = Hub()
        show(hub, features = setOf(CompactFeature, NewMainFeature))
        menu()
        rule.onNodeWithText("Compact context").assertExists()
        rule.onAllNodesWithText("Clear context").assertCountEquals(0)
        rule.onAllNodesWithText("composer-clear").assertCountEquals(0)
    }

    @Test fun anotherConversationHasNone() {
        show(Hub(), open = side)
        menu()
        rule.onAllNodesWithText("Clear context").assertCountEquals(0)
        rule.onAllNodesWithText("composer-clear").assertCountEquals(0)
    }

    @Test fun anotherConversationCanCompactButCannotClearOrReplaceMain() {
        val hub = Hub()
        show(hub, features = setOf(CompactFeature, NewMainFeature, ClearMainFeature), open = side)
        menu()
        rule.onNodeWithText("Compact context").performClick()
        rule.waitForIdle()
        rule.runOnIdle { assertEquals(listOf(side.key), hub.compacted) }
        rule.onNodeWithText("Compacting context…").assertExists()
        menu()
        rule.onAllNodesWithText("Clear context").assertCountEquals(0)
        rule.onAllNodesWithText("New main chat").assertCountEquals(0)
        rule.onNodeWithText("Compact context").assertIsNotEnabled()
    }

    @Test fun sideConversationObservesItsOwnCompactionRatherThanTheMains() {
        val hub = Hub()
        val running = SessionCompaction("running", startedAt = "2026-11-01T10:00:00Z")
        hub.assistant.value = AssistantSnapshot(main.key, listOf(main, side.copy(compaction = running)))
        show(hub, features = setOf(CompactFeature), open = side)
        rule.onNodeWithText("Compacting context…").assertExists()
        rule.runOnIdle {
            hub.assistant.value = AssistantSnapshot(main.key, listOf(main,
                side.copy(compaction = running.copy(state = "failed", error = "Example failure"))))
        }
        rule.waitForIdle()
        rule.onNodeWithText("Compaction failed: Example failure").assertExists()
        menu()
        rule.onNodeWithText("Back to main chat").performClick()
        rule.waitForIdle()
        rule.onAllNodesWithText("Compaction failed: Example failure").assertCountEquals(0)
    }

    @Test fun theDemoHasNone() {
        show(Hub(), demo = true)
        menu()
        rule.onAllNodesWithText("Clear context").assertCountEquals(0)
    }

    @Test fun clearingAsksNothingAndLeavesAnEmptyConversationPage() {
        val hub = Hub()
        show(hub)
        menu()
        rule.onNodeWithText("Clear context").performClick()
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals(listOf("main"), hub.cleared)
            assertEquals(listOf(main.key), hub.replaced)
            assertTrue(notices.toString(), notices.isEmpty())
        }
        rule.onAllNodesWithText("chat:main").assertCountEquals(0)
        rule.onNodeWithTag("main-cleared").assertExists()
        // Not the centered start page: none of its suggestions.
        rule.onAllNodesWithText("Sort inbox").assertCountEquals(0)
        // The first message starts the new main conversation: once the hub has one, the cleared page is forgotten.
        rule.runOnIdle { hub.assistant.value = AssistantSnapshot(next.key, listOf(next, side)) }
        rule.waitForIdle()
        rule.onAllNodesWithTag("main-cleared").assertCountEquals(0)
    }

    @Test fun aFailureSaysWhyAndKeepsTheConversation() {
        val hub = Hub().apply { failure = IllegalStateException("hub unreachable") }
        show(hub)
        var failed = 0
        rule.runOnIdle { hub.composerClear!!.clear { failed++ } }
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals(1, failed)
            assertEquals(FeedbackStyle.Error, notices.single().second)
            assertTrue(notices.single().first, notices.single().first.startsWith("Couldn't clear the context: "))
            assertTrue(hub.replaced.isEmpty())
        }
        rule.onNodeWithText("chat:main").assertExists()
        rule.onAllNodesWithTag("main-cleared").assertCountEquals(0)
    }

    @Test fun anotherDevicesChangeShowsTheMainConversationTheHubHasNow() {
        val hub = Hub().apply {
            failure = AssistantRefused("main_changed", "The main chat was already replaced on another device")
            // What the hub has once it refused: the page's own refresh when it opens must still see "main".
            afterRefusal = AssistantSnapshot(next.key, listOf(next, side))
        }
        show(hub)
        menu()
        rule.onNodeWithText("Clear context").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("chat:next").assertExists()
        rule.runOnIdle {
            assertEquals(listOf("The main chat was already replaced on another device" to null), notices)
            assertEquals(listOf(main.key), hub.replaced)
        }
    }

    @Test fun theComposersClearIsTheSameAction() {
        val hub = Hub()
        show(hub)
        rule.runOnIdle { hub.composerClear!!.clear { error("Unexpected failure") } }
        rule.waitForIdle()
        rule.runOnIdle { assertEquals(listOf("main"), hub.cleared) }
        rule.onNodeWithTag("main-cleared").assertExists()
    }

    inner class Hub : AssistantRepository, MessageSender, MainConversationSender, AssistantManagement {
        override val assistant = MutableStateFlow(AssistantSnapshot(main.key, listOf(main, side)))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val cleared = mutableListOf<String?>()
        val compacted = mutableListOf<SessionKey>()
        val replaced = mutableListOf<SessionKey>()
        var failure: Exception? = null
        var afterRefresh: AssistantSnapshot? = null
        var afterRefusal: AssistantSnapshot? = null
        var composerClear: persona.feature.chat.MainChatClear? = null
        override suspend fun compactConversation(session: SessionKey) { compacted += session }
        override suspend fun clearMainConversation(expect: String?): String {
            failure?.let { afterRefusal?.let { afterRefresh = it }; throw it }
            cleared += expect
            assistant.value = AssistantSnapshot(null, listOf(side))
            return expect.orEmpty()
        }
        override suspend fun mainReplaced(previous: SessionKey) { replaced += previous }
        override suspend fun refresh() { afterRefresh?.let { assistant.value = it } }
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = error("Unexpected end")
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask) = error("Unexpected save")
        override suspend fun deleteTask(id: String) = error("Unexpected delete")
        override suspend fun runTask(id: String): SessionKey = error("Unexpected run")
        override suspend fun freshTask(id: String): ScheduledTask = error("Unexpected fresh")
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = error("Unexpected notes")
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) = error("Unexpected memory")
        override suspend fun deleteMemory(name: String) = error("Unexpected delete")
    }
}
