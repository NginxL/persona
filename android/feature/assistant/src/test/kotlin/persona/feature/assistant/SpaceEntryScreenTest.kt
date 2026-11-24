package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
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
 * A domain space from the sidebar (#13500): the newest of its chats opens; with none, its home, whose first message starts
 * a conversation in the space (never the main one, never a health chat). New on a space's chat opens its home again.
 * Fake spaces only: qa-plain and qa-finance.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SpaceEntryScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Before fun setup() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun cleanup() = AssistantManagerChoice.reset()

    private fun space(slug: String, title: String, order: Int, categories: List<String> = emptyList()) = Track(slug = slug, kind = "space",
        title = title, defaultCategories = categories, revision = 1,
        config = TrackKindConfig(sidebar = TrackSidebar(group = Tracks.SPACES_GROUP, order = order)))

    private inner class Hub(val chats: Map<String, List<Session>>) : AssistantConversations, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = main))
        override val pinned = MutableStateFlow(emptySet<SessionKey>())
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val spaceStarts = mutableListOf<String>()
        val starts = mutableListOf<SessionKey>()
        val toMain = mutableListOf<String>()
        val embeddedHealth = mutableMapOf<SessionKey, Boolean>()
        override suspend fun refresh() = Unit
        override suspend fun startConversation(): SessionKey = SessionKey("mac", "new-${starts.size + 1}").also { starts += it }
        override suspend fun spaceConversations(space: String): List<Session> = chats[space].orEmpty()
        override suspend fun startConversationIn(space: String): SessionKey {
            spaceStarts += space
            return SessionKey("mac", "s-${spaceStarts.size}")
        }
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) { toMain += text }
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }

    private inner class Management : AssistantManagement {
        val tracks = mutableListOf(space("qa-finance", "Money", 2, listOf("finance")), space("qa-plain", "Plain", 1),
            Track(slug = "health", title = "Health", builtin = true, config = TrackKindConfig(sidebar = TrackSidebar(group = Tracks.SPACES_GROUP))))
        val changes = mutableListOf<Pair<String, TrackChange>>()
        override suspend fun tracks(): List<Track> = tracks.toList()
        override suspend fun spaceMd(slug: String) = SpaceMd("# Plain\n\nFor plain things.\n", "d1", "")
        override suspend fun changeTrack(slug: String, change: TrackChange): Track {
            changes += slug to change
            return tracks.first { it.slug == slug }.copy(archived = change.archived == true, revision = change.revision + 1)
        }
        override suspend fun conversationCategories(session: SessionKey) =
            ConversationCategories(categories = listOf("finance"), floor = listOf("finance"))
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask): ScheduledTask = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String): SessionKey = error("Unexpected")
        override suspend fun freshTask(id: String): ScheduledTask = error("Unexpected")
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes(machineId = "mac")
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    private fun mount(hub: Hub, management: Management = Management()): AssistantSidebarHost {
        val sidebar = AssistantSidebarHost()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalAssistantSidebarHost provides sidebar) {
                    AssistantScreen(navigator, hub, hub, hub::startConversation, composerService = null, mainConversationSender = hub,
                        management = management,
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
    private fun open(sidebar: AssistantSidebarHost, slug: String) {
        rule.runOnIdle { sidebar.openSpace = slug; sidebar.action = AssistantSidebarAction.Space }
        rule.waitForIdle()
    }
    private fun waitFor(text: String) = rule.waitUntil(5_000) { rule.onAllNodesWithText(text).fetchSemanticsNodes().isNotEmpty() }
    private fun waitForTag(tag: String) = rule.waitUntil(5_000) { rule.onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty() }
    private fun send(text: String) {
        rule.onNode(hasSetTextAction()).performTextReplacement(text)
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitForIdle()
    }

    @Test fun aSpaceWithoutChatsOpensItsHomeWhoseFirstMessageStartsOneThere() {
        val hub = Hub(emptyMap())
        val sidebar = mount(hub)
        open(sidebar, "qa-plain")
        waitForTag("space-home")
        waitFor("Plain")
        rule.onNodeWithTag("space-privacy").assertExists()
        // SPACE.md's leading title repeats the space's name, so only what is under it shows.
        waitFor("For plain things.")
        assertEquals("qa-plain", sidebar.spaceInFront)
        send("What is on this week?")
        waitFor("chat:s-1")
        assertEquals(listOf("qa-plain"), hub.spaceStarts)
        assertEquals("a space's chat is never a health chat", false, hub.embeddedHealth[SessionKey("mac", "s-1")])
        assertEquals(emptyList<String>(), hub.toMain)
        assertEquals(emptyList<SessionKey>(), hub.starts)
    }

    @Test fun aSpaceOpensItsNewestChatAndNewOpensItsHome() {
        val hub = Hub(mapOf("qa-finance" to listOf(
            Session(machineId = "mac", sessionId = "fin-old", updatedAt = "2026-11-01T08:00:00Z", assistant = true, space = "qa-finance"),
            Session(machineId = "mac", sessionId = "fin-new", updatedAt = "2026-11-20T08:00:00Z", assistant = true, space = "qa-finance"))))
        val sidebar = mount(hub)
        open(sidebar, "qa-finance")
        waitFor("chat:fin-new")
        assertEquals(false, hub.embeddedHealth[SessionKey("mac", "fin-new")])
        // Its categories as one chip in the bar.
        waitForTag("conversation-categories")
        rule.onNodeWithContentDescription("New conversation").performClick()
        rule.waitForIdle()
        waitForTag("space-home")
        waitFor("Money")
        assertEquals(emptyList<String>(), hub.spaceStarts)
    }

    @Test fun archivingASpaceSendsItsRevisionAndLeavesForTheMainChat() {
        val management = Management()
        val sidebar = mount(Hub(emptyMap()), management)
        open(sidebar, "qa-plain")
        waitForTag("space-home")
        rule.onNodeWithContentDescription("Space actions").performClick()
        rule.onNodeWithText("Archive").performClick()
        rule.waitForIdle()
        waitFor("Archive Plain?")
        rule.onAllNodesWithText("Archive").onLast().performClick()
        rule.waitUntil(5_000) { management.changes.isNotEmpty() }
        assertEquals("qa-plain" to TrackChange(revision = 1, archived = true), management.changes.single())
        waitFor("chat:main")
        assertNull(sidebar.spaceInFront)
    }
}
