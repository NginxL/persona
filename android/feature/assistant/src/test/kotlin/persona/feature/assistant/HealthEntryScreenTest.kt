package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.CompletableDeferred
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * The sidebar's Health (#13273) opens the home with its history, without waiting for the list.
 * The first message starts a health chat (never the main one). New on a health chat returns to the home.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class HealthEntryScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Before fun setup() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun cleanup() = AssistantManagerChoice.reset()

    private inner class Hub(val health: List<Session>) : AssistantConversations, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = main))
        override val pinned = MutableStateFlow(emptySet<SessionKey>())
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val listed = mutableListOf<String>()
        val spaceStarts = mutableListOf<String>()
        val starts = mutableListOf<SessionKey>()
        val targets = mutableListOf<SessionKey>()
        val toMain = mutableListOf<String>()
        val embeddedHealth = mutableMapOf<SessionKey, Boolean>()
        var historyReady: CompletableDeferred<Unit>? = null
        override suspend fun refresh() = Unit
        override suspend fun startConversation(): SessionKey = SessionKey("mac", "new-${starts.size + 1}").also { starts += it }
        override suspend fun spaceConversations(space: String): List<Session> {
            listed += space
            historyReady?.await()
            return health
        }
        override suspend fun startConversationIn(space: String): SessionKey {
            spaceStarts += space
            return SessionKey("mac", "h-${spaceStarts.size}")
        }
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) { toMain += text }
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) { targets += session }
    }

    private fun mount(hub: Hub): AssistantSidebarHost {
        val sidebar = AssistantSidebarHost()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalAssistantSidebarHost provides sidebar) {
                    AssistantScreen(navigator, hub, hub, hub::startConversation, composerService = null, mainConversationSender = hub,
                        assistantChatContent = { key, options ->
                            hub.embeddedHealth[key] = options.healthChat
                            Text("chat:${key.sessionId}")
                        })
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("chat:main").assertExists()
        return sidebar
    }
    private fun health(sidebar: AssistantSidebarHost) {
        rule.runOnIdle { sidebar.action = AssistantSidebarAction.Health }
        rule.waitForIdle()
    }
    private fun waitFor(text: String) = rule.waitUntil(5_000) { rule.onAllNodesWithText(text).fetchSemanticsNodes().isNotEmpty() }
    private fun send(text: String) {
        rule.onNode(hasSetTextAction()).performTextReplacement(text)
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitForIdle()
    }
    private fun chat(id: String, updated: String, archived: Boolean = false) = Session(machineId = "mac", sessionId = id,
        title = "Health $id", updatedAt = updated, assistant = true, archived = archived, space = AssistantSpace.HEALTH)

    @Test fun healthOpensTheHomeWithHistoryAndNewReturnsToIt() {
        val hub = Hub(listOf(chat("older", "2026-11-01T08:00:00Z"), chat("newest", "2026-11-10T08:00:00Z"),
            chat("put-away", "2026-11-11T08:00:00Z", archived = true)))
        val sidebar = mount(hub)
        health(sidebar)
        waitFor("Ask about your health")
        waitFor("Health older")
        rule.onAllNodesWithText("chat:newest").assertCountEquals(0)
        rule.onAllNodesWithText("Health put-away").assertCountEquals(0)
        assertEquals(emptyList<String>(), hub.spaceStarts)
        rule.onNodeWithText("Health newest").performClick()
        rule.waitForIdle()
        waitFor("chat:newest")
        assertEquals(listOf(AssistantSpace.HEALTH), hub.listed)
        assertEquals(false, hub.embeddedHealth[main])
        assertEquals(true, hub.embeddedHealth[SessionKey("mac", "newest")])

        // New on a health chat: the health space's page, with its history; nothing is started yet.
        rule.onNodeWithContentDescription("New health chat").performClick()
        rule.waitForIdle()
        waitFor("Ask about your health")
        rule.onAllNodesWithText("chat:newest").assertCountEquals(0)
        waitFor("Health older")
        assertEquals(emptyList<String>(), hub.spaceStarts)

        send("How did I sleep?")
        waitFor("chat:h-1")
        assertEquals(true, hub.embeddedHealth[SessionKey("mac", "h-1")])
        assertEquals(listOf(AssistantSpace.HEALTH), hub.spaceStarts)
        assertEquals(listOf(SessionKey("mac", "h-1")), hub.targets)
        assertEquals("never the main chat or a plain new one", emptyList<String>(), hub.toMain)
        assertEquals(emptyList<SessionKey>(), hub.starts)
    }

    @Test fun healthOpensTheHomeWhileItsHistoryIsStillLoading() {
        val ready = CompletableDeferred<Unit>()
        val hub = Hub(listOf(chat("newest", "2026-11-10T08:00:00Z"))).apply { historyReady = ready }
        val sidebar = mount(hub)
        health(sidebar)
        waitFor("Ask about your health")
        rule.onAllNodesWithText("chat:main").assertCountEquals(0)
        rule.onAllNodesWithText("chat:newest").assertCountEquals(0)
        assertEquals(emptyList<String>(), hub.spaceStarts)
        rule.runOnIdle { ready.complete(Unit) }
        waitFor("Health newest")
        rule.onAllNodesWithText("chat:newest").assertCountEquals(0)
    }

    @Test fun noHealthChatOpensTheHealthNewPage() {
        val hub = Hub(emptyList())
        val sidebar = mount(hub)
        health(sidebar)
        waitFor("Ask about your health")
        rule.onAllNodesWithText("chat:main").assertCountEquals(0)
        assertEquals(emptyList<String>(), hub.spaceStarts)
        send("Log my weight")
        waitFor("chat:h-1")
        assertEquals(true, hub.embeddedHealth[SessionKey("mac", "h-1")])
        assertEquals(listOf(AssistantSpace.HEALTH), hub.spaceStarts)
        assertEquals(emptyList<String>(), hub.toMain)
    }
}
