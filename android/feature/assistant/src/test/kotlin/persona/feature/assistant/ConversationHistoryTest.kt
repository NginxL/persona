package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHomeDrawer
import persona.core.designsystem.PersonaTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.swipeLeft
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
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * C12 (#4339): the assistant's conversation history as on iOS (`AssistantSidebar`, `ArchivedConversationsView`): Rename…
 * sends the title, a swipe left archives, the menu previews the last message, and "Archived (N)" opens the archived
 * conversations, which open or come back.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
class ConversationHistoryTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", status = "idle")
    private val first = Session(machineId = "hub", sessionId = "a", title = "第一个任务", status = "idle",
        lastText = "**行程**已经排好了：周五出发。", updatedAt = "2026-11-12T09:30:00Z")
    private val second = Session(machineId = "hub", sessionId = "b", title = "第二个任务", status = "working")
    private val old = Session(machineId = "hub", sessionId = "old", title = "旧主对话 · 至 11月10日", archived = true,
        updatedAt = "2026-11-10T09:00:00Z")
    private val older = Session(machineId = "hub", sessionId = "older", title = "上周的周报", archived = true,
        updatedAt = "2026-11-05T09:00:00Z")
    private val notices = mutableListOf<Pair<String, FeedbackStyle>>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun show(hub: HistoryHub = HistoryHub(main, first, second)): HistoryHub {
        rule.setContent {
            CompositionLocalProvider(LocalAppFeedback provides object : AppFeedback {
                override fun failure(failure: Throwable, operation: Boolean): Boolean {
                    notices += failure.message.orEmpty() to FeedbackStyle.Error
                    return true
                }
                override fun notice(message: String) = false
                override fun notice(message: String, style: FeedbackStyle): Boolean {
                    notices += message to style
                    return true
                }
            }) {
                AssistantScreen(navigator, hub, hub, startConversation = { error("Unexpected creation") },
                    requestedSession = main.key, management = hub, composerService = null,
                    chatContent = { Text("chat:${it.sessionId}") })
            }
        }
        rule.onNodeWithContentDescription("对话列表").performClick()
        return hub
    }

    /** The rows with a menu, top to bottom: Persona, then the two recent chats. */
    private fun menuOf(row: Int) = rule.onAllNodesWithContentDescription("对话操作")[row].performClick()

    @Test fun aLongPressPreviewsTheLastMessageOverIOSsItems() {
        show()
        rule.onNode(hasText(first.title) and hasClickAction()).performTouchInput { longClick() }
        // what it last said, as prose, and when, above the items
        rule.onNodeWithText("行程已经排好了：周五出发。").assertExists()
        val labels = listOf("设为常驻", "重命名…", "归档", "拷贝会话 ID", "拷贝诊断", "结束…")
        val tops = labels.map { rule.onNode(hasText(it) and hasAnyAncestor(isDialog())).fetchSemanticsNode().boundsInRoot.top }
        assertEquals("iOS's order", tops.sorted(), tops)
        assertTrue("the preview is above the items",
            rule.onNodeWithText("行程已经排好了：周五出发。").fetchSemanticsNode().boundsInRoot.bottom <= tops.first())
    }

    @Test fun copyDiagnosticsCopiesTheStructure() {
        show()
        rule.onAllNodesWithContentDescription("对话操作")[1].performClick()
        rule.onNodeWithText("拷贝诊断").performClick()
        rule.waitForIdle()
        val clipboard = rule.activity.getSystemService(android.content.ClipboardManager::class.java)
        val text = clipboard.primaryClip?.getItemAt(0)?.text?.toString().orEmpty()
        assertTrue(text, "session=hub/a" in text)
        assertTrue(text, first.title !in text)
    }

    @Test fun theMainConversationCopiesButIsNotRenamedOrArchived() {
        show()
        menuOf(0)
        rule.onNodeWithText("拷贝会话 ID").assertExists()
        rule.onNodeWithText("拷贝诊断").assertExists()
        rule.onAllNodesWithText("重命名…").assertCountEquals(0)
        rule.onAllNodesWithText("归档").assertCountEquals(0)
    }

    @Test fun aSwipeLeftArchivesAChatButNotTheMainOne() {
        val hub = show()
        rule.onNode(hasText(first.title) and hasClickAction()).performTouchInput { swipeLeft() }
        rule.waitForIdle()
        assertEquals(listOf(first.key), hub.archived)
        rule.onAllNodesWithText(first.title).assertCountEquals(0)
        assertTrue(notices.toString(), "已归档" to FeedbackStyle.Success in notices)
        // The main conversation has no swipe: nothing more is archived.
        rule.onNode(hasText("主对话") and hasClickAction()).performTouchInput { swipeLeft() }
        rule.waitForIdle()
        assertEquals(listOf(first.key), hub.archived)
    }

    @Test fun archivedOpensTheArchivedConversationsWhichComeBackOrOpen() {
        val hub = HistoryHub(main, first, second).apply { archivedList = listOf(old, older) }
        show(hub)
        rule.onNodeWithText("已归档（2）").performScrollTo().performClick()
        rule.onNodeWithText(old.title).assertExists()
        rule.onNodeWithText(older.title).assertExists()
        val tops = listOf(old.title, older.title).map { rule.onNodeWithText(it).fetchSemanticsNode().boundsInRoot.top }
        assertEquals("newest first", tops.sorted(), tops)
        // Unarchive: off this list at once, back in the history.
        rule.onAllNodesWithText("取消归档")[1].performClick()
        rule.waitForIdle()
        assertEquals(listOf(older.key), hub.unarchived)
        rule.onAllNodesWithText(older.title).assertCountEquals(1) // its row in the history, behind the sheet
        assertTrue(notices.toString(), "已取消归档" to FeedbackStyle.Success in notices)
        // A tap opens one there: the sheet and the list close on its chat.
        rule.onNode(hasText(old.title) and hasAnyAncestor(isDialog()) and hasClickAction()).performClick()
        rule.onNodeWithText("chat:old").assertExists()
        rule.onAllNodesWithText("已归档（1）").assertCountEquals(0)
    }

    @Test fun archivingCountsTheChatUnderArchived() {
        val hub = show()
        rule.onAllNodesWithText("已归档", substring = true).assertCountEquals(0)
        menuOf(1)
        rule.onNodeWithText("归档").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("已归档（1）").performScrollTo().assertExists()
        assertEquals(listOf(first.key), hub.archived)
    }

    // A hub title's day and kind are quiet before its topic on Glass (iOS `ConversationTitle`, #4339 C12): in the list and in
    // Archived; Brief and Board draw the title whole.
    private val dated = Session(machineId = "hub", sessionId = "dated", title = "1101 | 修复 | 搜索框和顶栏重叠", status = "idle",
        updatedAt = java.time.Instant.now().minusSeconds(7 * 60).toString(), lastText = "已经修好了。")

    private fun spansOf(title: String): List<Pair<Int, Int>> =
        rule.onNodeWithText(title, useUnmergedTree = true).fetchSemanticsNode().config
            .getOrNull(SemanticsProperties.Text).orEmpty().first().spanStyles.map { it.start to it.end }

    @Test fun aHubTitlesDayAndKindAreQuietInTheListOnGlass() {
        show(HistoryHub(main, dated))
        rule.onNodeWithText(dated.title, useUnmergedTree = true).assertExists()
        assertEquals(listOf(0 to "1101 | 修复 | ".length), spansOf(dated.title))
        rule.onAllNodesWithText("搜索框和顶栏重叠", useUnmergedTree = true).assertCountEquals(0) // one text, not two
    }

    /** The spans of a hub title's row and of another title's in Archived, in [style]. */
    private fun archivedSpans(style: AppStyle): Pair<List<Pair<Int, Int>>, List<Pair<Int, Int>>> {
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = style) {
                ArchivedConversationsSheet(listOf(dated.copy(archived = true), old), onDismiss = {}, onOpen = {}, onUnarchive = {})
            }
        }
        rule.waitForIdle()
        return spansOf(dated.title) to spansOf(old.title)
    }

    @Test fun archivedRowsQuietTheDayAndKindOnGlass() {
        val (hub, other) = archivedSpans(AppStyle.Glass)
        assertEquals(listOf(0 to "1101 | 修复 | ".length), hub)
        assertEquals("any other title is drawn as it is", emptyList<Pair<Int, Int>>(), other)
    }

    @Test fun archivedRowsDrawTheTitleWholeInBrief() = assertEquals(emptyList<Pair<Int, Int>>(), archivedSpans(AppStyle.Brief).first)

    @Test fun archivedRowsDrawTheTitleWholeInBoard() = assertEquals(emptyList<Pair<Int, Int>>(), archivedSpans(AppStyle.Board).first)

    // The history says when as iOS does (`Fmt.relative`): minutes ago, not the date and time (#4339 C12).
    @Test fun theHistorySaysWhenAsIOSDoes() {
        show(HistoryHub(main, dated))
        // The row's time line says it, and then the menu's preview does too.
        rule.onAllNodesWithText("7 分钟前").assertCountEquals(1)
        rule.onNode(hasText(dated.title) and hasClickAction()).performTouchInput { longClick() }
        rule.onAllNodesWithText("7 分钟前").assertCountEquals(2)
        rule.onAllNodesWithText("11月", substring = true).assertCountEquals(0)
    }

    @Test fun archivedRowsSayWhenAsIOSDoes() {
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ArchivedConversationsSheet(listOf(dated.copy(archived = true)), onDismiss = {}, onOpen = {}, onUnarchive = {})
        } }
        rule.onNodeWithText("7 分钟前", useUnmergedTree = true).assertExists()
    }

    @Test fun theLastOneBroughtBackLeavesTheEmptyPage() {
        val hub = HistoryHub(main, first, second).apply { archivedList = listOf(old) }
        show(hub)
        rule.onNodeWithText("已归档（1）").performScrollTo().performClick()
        rule.onNode(hasText(old.title) and hasAnyAncestor(isDialog()) and hasClickAction()).performTouchInput { swipeLeft() }
        rule.waitForIdle()
        assertEquals(listOf(old.key), hub.unarchived)
        rule.onNodeWithText("没有归档的对话").assertExists()
        rule.onNodeWithText("在对话列表里左滑一个对话就能归档，它会收在这里。").assertExists()
    }
}

/** The hub of these tests: Persona's list, its archived conversations, and what was archived, brought back and renamed. */
internal class HistoryHub(main: Session, vararg chats: Session) : AssistantRepository, MessageSender, AssistantManagement {
    override val assistant = MutableStateFlow(AssistantSnapshot(main.key, listOf(main) + chats))
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    val archived = mutableListOf<SessionKey>()
    val unarchived = mutableListOf<SessionKey>()
    val renamed = mutableListOf<Pair<SessionKey, String>>()
    var archivedList: List<Session> = emptyList()
    var refuseRename = false
    override suspend fun archiveConversation(session: SessionKey) {
        archived += session
        archivedList = listOf(assistant.value.conversations.first { it.key == session }.copy(archived = true)) + archivedList
    }
    override suspend fun unarchiveConversation(session: SessionKey) {
        unarchived += session
        val back = archivedList.first { it.key == session }
        archivedList = archivedList - back
        assistant.value = assistant.value.copy(conversations = assistant.value.conversations + back.copy(archived = false))
    }
    override suspend fun archivedConversations(): List<Session> = archivedList
    override suspend fun renameConversation(session: SessionKey, title: String) {
        check(!refuseRename) { "改名失败" }
        renamed += session to title
    }
    override suspend fun refresh() {
        assistant.value = assistant.value.copy(conversations = assistant.value.conversations.filter { it.key !in archived || it.key in unarchived })
    }
    override suspend fun warm() = Unit
    override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = error("Unexpected send")
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

/**
 * C12's history drawn on the JVM (Robolectric native graphics), Glass, light and dark, to look at: a row's menu with its
 * preview, the list ending in Archived (N), and the Archived sheet with rows and empty (`build/conversation-history-shots/`,
 * or `-Dpersona.shots=<dir>`). The rename dialog is `ConversationRenameShotsTest`'s.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class ConversationHistoryShotsTest {
    @get:Rule val rule = createComposeRule()

    private val minutesAgo = { m: Long -> java.time.Instant.now().minusSeconds(m * 60).toString() }
    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", status = "idle", updatedAt = minutesAgo(2))
    private val trip = Session(machineId = "hub", sessionId = "trip", title = "1112 | 探索 | 周末去哪儿", status = "idle", updatedAt = minutesAgo(20),
        lastText = "**两个方案**：\n- 周六去海边，住一晚\n- 周日去山里徒步，当天回来\n\n天气预报周六晴，周日有小雨，我更推荐第一个。要我订酒店吗？")
    private val inbox = Session(machineId = "hub", sessionId = "inbox", title = "整理收件箱", status = "working", updatedAt = minutesAgo(5))
    private val archived = listOf(
        Session(machineId = "hub", sessionId = "old", title = "旧主对话 · 至 11月10日", archived = true, updatedAt = minutesAgo(60 * 48)),
        Session(machineId = "hub", sessionId = "report", title = "1105 | 定时 | 上周的周报", archived = true, updatedAt = minutesAgo(60 * 24 * 7)),
        Session(machineId = "hub", sessionId = "gift", title = "1023 | 探索 | 给妈妈的生日礼物", archived = true, updatedAt = minutesAgo(60 * 24 * 20)),
    )

    private fun drawer(dark: () -> Boolean, archivedCount: Int, style: () -> AppStyle = { AppStyle.Glass }) {
        rule.setContent {
            PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                CompositionLocalProvider(LocalHomeDrawer provides true) {
                    Box(Modifier.testTag("shot").width(320.dp).height(760.dp).background(PersonaTheme.palette.background)) {
                        AssistantDrawer("", {}, ConversationGroups(main, emptyList(), listOf(inbox, trip), recent = listOf(inbox, trip)),
                            active = null, hasMain = true, onEnd = {}, onMain = {}, onOpen = {}, onInfo = {}, onNew = {},
                            onSettings = {}, onArchive = {}, onLeader = {}, onRename = {}, onDiagnostics = {},
                            archivedCount = archivedCount, onArchived = {})
                    }
                }
            }
        }
    }

    @Test fun drawsARowsMenuWithItsPreview() {
        var dark by mutableStateOf(false)
        drawer({ dark }, archivedCount = 0)
        rule.onNode(hasText(trip.title) and hasClickAction()).performTouchInput { longClick() }
        rule.onNodeWithText("两个方案： 周六去海边，住一晚 周日去山里徒步，当天回来 天气预报周六晴，周日有小雨，我更推荐第一个。要我订酒店吗？").assertExists()
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            historyShot(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "menu-glass-${if (d) "dark" else "light"}")
        }
    }

    @Test fun aWorkingConversationSpinsInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        drawer({ dark }, archivedCount = 0, style = { style })
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithContentDescription("加载中").assertExists()
            historyShot(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(),
                "drawer-own-working-spin-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun drawsTheListEndingInArchived() {
        var dark by mutableStateOf(false)
        drawer({ dark }, archivedCount = archived.size)
        rule.onNodeWithText("已归档（3）").assertExists()
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            historyShot(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "drawer-archived-glass-${if (d) "dark" else "light"}")
        }
    }

    @Test fun drawsTheArchivedSheet() {
        var dark by mutableStateOf(false)
        var list by mutableStateOf(archived)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background))
                ArchivedConversationsSheet(list, onDismiss = {}, onOpen = {}, onUnarchive = {})
            }
        }
        rule.onNodeWithText(archived.first().title).assertExists()
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            historyShot(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "archived-glass-${if (d) "dark" else "light"}")
        }
        list = emptyList()
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            rule.onNodeWithText("没有归档的对话").assertExists()
            historyShot(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "archived-empty-glass-${if (d) "dark" else "light"}")
        }
    }
}
