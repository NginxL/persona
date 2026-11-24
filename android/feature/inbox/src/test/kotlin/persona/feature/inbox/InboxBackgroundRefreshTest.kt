package persona.feature.inbox

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxInteraction
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/**
 * A background refresh of a shown inbox leaves its layout alone (#1947): no loading row over the list, and the brief
 * Connecting / Updating of a reconnect does not put a line under the title unless it lasts.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxBackgroundRefreshTest {
    @get:Rule val rule = createComposeRule()

    private val records = listOf(
        InboxRecord(InboxItem("lark:f2:120", kind = "group", title = "项目群", sender = "示例同事", preview = "评审改到周四下午了",
            tier = "fyi", updatedAt = "2026-11-04T08:10:00Z"), sourceLabel = "飞书", feedId = "f2", count = 2, source = "lark"),
    )

    private class FakeInbox(snapshot: InboxSnapshot, connection: ConnectionState = ConnectionState.Online) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        val connection = MutableStateFlow(connection)
        override val connectionState = this.connection
        var refreshFailure: Exception? = null
        override suspend fun refresh() { refreshFailure?.let { throw it } }
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun count(text: String) = rule.onAllNodes(androidx.compose.ui.test.hasText(text)).fetchSemanticsNodes().size

    // A refresh that fails, or a reconnect, over a shown list: an error may show, the loading row does not, and a list of
    // only Agent requests (no inbox items yet) counts as shown too.
    @Test fun failedRefreshAndReconnectOverAShownListShowNoLoadingRow() {
        val inbox = FakeInbox(InboxSnapshot(records = records))
        rule.setContent { PersonaTheme(reduceMotion = true) { OnAll { InboxScreen(inbox, navigator) } } }
        rule.waitForIdle()
        inbox.connection.value = ConnectionState.Connecting
        inbox.snapshot.value = InboxSnapshot(records = records, loading = true)
        rule.waitForIdle()
        inbox.connection.value = ConnectionState.Online
        inbox.snapshot.value = InboxSnapshot(records = records, error = "收件箱刷新失败，请重试")
        rule.waitForIdle()
        assertEquals(0, count("正在读取收件箱"))
        rule.onNodeWithText("项目群").assertExists()
        val waiting = listOf(InboxInteraction("i1", "server", "s1", "2026-11-04T08:00:00Z", "示例会话", "计划等你批准", "照片同步脚本"))
        inbox.snapshot.value = InboxSnapshot(interactions = waiting, loading = true)
        rule.waitForIdle()
        assertEquals(0, count("正在读取收件箱"))
    }

    @Test fun backgroundRecoveryClearsAnEarlierRefreshErrorAndRetryIsAvailable() {
        val inbox = FakeInbox(InboxSnapshot(records = records))
        inbox.refreshFailure = java.io.IOException("Synthetic refresh failure")
        rule.setContent { PersonaTheme(reduceMotion = true) { OnAll { InboxScreen(inbox, navigator) } } }
        rule.waitForIdle()
        rule.onNodeWithText("收件箱刷新失败，请重试").assertExists()
        rule.onNodeWithText("重试").assertExists()
        inbox.refreshFailure = null
        inbox.snapshot.value = inbox.snapshot.value.copy(refreshes = 1)
        rule.waitForIdle()
        rule.onNodeWithText("收件箱刷新失败，请重试").assertDoesNotExist()
        rule.onNodeWithText("项目群").assertExists()
    }

    @Test fun loadingRowOnlyWhileTheListIsEmpty() {
        val inbox = FakeInbox(InboxSnapshot(loading = true))
        rule.setContent { PersonaTheme(reduceMotion = true) { OnAll { InboxScreen(inbox, navigator) } } }
        rule.waitForIdle()
        rule.onNodeWithText("正在读取收件箱").assertExists()
        inbox.snapshot.value = InboxSnapshot(records = records)
        rule.waitForIdle()
        rule.onNodeWithText("项目群").assertExists()
        // A background refresh of the shown list: the list stays where it is.
        inbox.snapshot.value = InboxSnapshot(records = records, loading = true)
        rule.waitForIdle()
        assertEquals(0, count("正在读取收件箱"))
        rule.onNodeWithText("项目群").assertExists()
    }

    @Test fun briefReconnectLeavesTheTitleAloneAndALongOneShows() {
        val inbox = FakeInbox(InboxSnapshot(records = records))
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) { OnAll { InboxScreen(inbox, navigator) } } }
        rule.mainClock.advanceTimeBy(100)
        assertEquals(0, count("更新中…"))
        // Updating for half a second, as after every reconnect: nothing under the title.
        inbox.connection.value = ConnectionState.Updating
        rule.mainClock.advanceTimeBy(500)
        assertEquals(0, count("更新中…"))
        inbox.connection.value = ConnectionState.Online
        rule.mainClock.advanceTimeBy(1_500)
        assertEquals(0, count("更新中…"))
        // Lasting longer than a second: it shows.
        inbox.connection.value = ConnectionState.Connecting
        rule.mainClock.advanceTimeBy(1_500)
        assertEquals(1, count("连接中…"))
        // A state that needs the user shows at once.
        inbox.connection.value = ConnectionState.CertificateMismatch
        rule.mainClock.advanceTimeBy(50)
        assertEquals(1, count("hub 的证书变了"))
    }
}
