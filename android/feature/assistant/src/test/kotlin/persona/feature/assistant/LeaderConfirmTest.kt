package persona.feature.assistant

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.longClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.HubScope
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantNotes
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.MemoryFile
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/**
 * #6214: the drawer's 设为常驻 / 取消常驻 menu asks a second time (JDialog) before the hub call; cancelling calls
 * nothing, confirming calls make/end once. The hub stays the only state source (assistant.leaders).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1500dp")
class LeaderConfirmTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() { scope.cancel() }

    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", status = "idle", assistant = true)
    private val lead = Session(machineId = "hub", sessionId = "lead-a", title = "增长方向", status = "idle", assistant = true)
    private val chat = Session(machineId = "hub", sessionId = "chat-b", title = "周末安排", status = "idle", assistant = true)

    private val made = mutableListOf<SessionKey>()
    private val ended = mutableListOf<SessionKey>()

    private val management = object : AssistantManagement {
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = error("Unexpected end")
        override suspend fun archiveConversation(session: SessionKey) = error("Unexpected archive")
        override suspend fun unarchiveConversation(session: SessionKey) = error("Unexpected unarchive")
        override suspend fun archivedConversations(): List<Session> = emptyList()
        override suspend fun renameConversation(session: SessionKey, title: String) = error("Unexpected rename")
        override suspend fun makeDirection(session: SessionKey) { made += session }
        override suspend fun endDirection(session: SessionKey) { ended += session }
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask): ScheduledTask = error("Unexpected save")
        override suspend fun deleteTask(id: String) = error("Unexpected delete")
        override suspend fun runTask(id: String): SessionKey = error("Unexpected run")
        override suspend fun freshTask(id: String): ScheduledTask = error("Unexpected fresh")
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = error("Unexpected notes")
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) = error("Unexpected memory")
        override suspend fun deleteMemory(name: String) = error("Unexpected memory")
    }

    private val drafts = object : DraftRepository {
        override fun observe(key: DraftKey): Flow<DraftSnapshot> =
            flowOf(DraftSnapshot(HubScope("hub", "gen"), key))
        override suspend fun read(key: DraftKey): DraftSnapshot =
            DraftSnapshot(HubScope("hub", "gen"), key)
        override suspend fun save(key: DraftKey, text: String, expectedVersion: Long): DraftSnapshot =
            DraftSnapshot(HubScope("hub", "gen"), key, text, expectedVersion + 1)
    }

    private val mainSender = object : MainConversationSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) =
            error("Unexpected send")
    }

    private fun show(leaders: List<String>) {
        val snapshot = AssistantSnapshot(main.key, listOf(main, lead, chat),
            manager = AssistantManagerChoice.current.value.manager,
            managerId = AssistantManagerChoice.current.value.shown, leaders = leaders)
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        val bindings = AssistantHostBindings(
            identity = "test", management = management, drafts = drafts, coroutineScope = scope,
            phoneToolsAvailable = false, machines = emptyList(), conversations = repository, mainSender = mainSender,
            chatContent = { _, _ -> },
        )
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                CompositionLocalProvider(LocalAssistantHost provides bindings) {
                    Box(Modifier.width(400.dp).height(1200.dp)) {
                        AssistantSidebarContent(snapshot, repository, null, scope, demo = false,
                            onOpen = {}, onAction = {}, onSettings = {},
                            onInbox = {}, onKnowledge = {}, onTodos = {}, todoCount = 0, showTodos = true,
                            inboxCount = 0, settingsCount = 0, settingsDot = false, settingsBadge = null,
                            pages = emptyList(), onPage = {}, onRetired = {})
                    }
                }
            }
        }
    }

    private fun openMenu(title: String) {
        rule.onNode(hasText(title) and hasClickAction()).performTouchInput { longClick() }
        rule.waitForIdle()
    }

    @Test fun unpinAsksFirstAndOnlyEndsOnTheSecondChoice() {
        show(listOf("lead-a"))
        openMenu("增长方向")
        rule.onNodeWithText("取消常驻").performClick()
        // The dialog says what moves: back to 最近, open tasks return to the main chat, nothing deleted.
        rule.onNode(isDialog()).assertExists()
        rule.onNode(hasText("取消常驻？") and hasAnyAncestor(isDialog())).assertExists()
        // The explanation lives in the dialog's body (the sidebar's own 最近 heading is outside the dialog).
        rule.onNode(hasText("最近", substring = true) and hasAnyAncestor(isDialog())).assertExists()
        rule.onNode(hasText("主对话", substring = true) and hasAnyAncestor(isDialog())).assertExists()
        // Cancel calls nothing.
        rule.onNode(hasText("取消") and hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals(emptyList<SessionKey>(), ended)
        // Reopen and confirm: the end call reaches the hub exactly once.
        openMenu("增长方向")
        rule.onNodeWithText("取消常驻").performClick()
        rule.onNode(hasText("取消常驻") and hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals(listOf(lead.key), ended)
        assertTrue(made.isEmpty())
    }

    @Test fun makeLeaderAsksFirstAndOnlyMakesOnTheSecondChoice() {
        show(emptyList())
        openMenu("周末安排")
        rule.onNodeWithText("设为常驻").performClick()
        rule.onNode(hasText("设为常驻？") and hasAnyAncestor(isDialog())).assertExists()
        rule.onNode(hasText("取消") and hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals(emptyList<SessionKey>(), made)
        openMenu("周末安排")
        rule.onNodeWithText("设为常驻").performClick()
        rule.onNode(hasText("设为常驻") and hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals(listOf(chat.key), made)
        assertTrue(ended.isEmpty())
    }
}
