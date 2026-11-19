package persona.feature.inbox

import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTouchInput
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.IOException
import java.time.Instant
import java.time.temporal.ChronoUnit
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackAction
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxGroups
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * Android acceptance for #10425 (docs/assistant/inbox.md "Group titles"). These drive InboxScreen itself: the group
 * pull-down, its group and whole-section choices, and the exact versioned items delivered to the existing bulk callback.
 * The section's older Mark-all-as-read receipt is deliberately a different path and must remain unchanged.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class InboxGroupClearTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    private fun ago(minutes: Long) = now.minus(minutes, ChronoUnit.MINUTES).toString()

    private fun record(
        id: String,
        title: String,
        tier: String = "fyi",
        source: String = "lark",
        account: String = if (source == "lark") "feishu" else "acc1",
        at: String = ago(30),
        status: String? = null,
        doneAt: String? = null,
    ): InboxRecord {
        val mail = source == "mail"
        return InboxRecord(
            InboxItem(
                id,
                account = account,
                kind = if (mail) "mail" else "dm",
                title = title,
                sender = if (mail) "Sender $id" else "Person $id",
                preview = "Preview $id",
                tier = tier,
                status = status,
                updatedAt = at,
                doneAt = doneAt,
                version = "v-$id",
            ),
            sourceLabel = if (mail) "$account@example.com" else "Lark",
            feedId = if (mail) null else "feed-$id",
            source = source,
        )
    }

    private val fyi = listOf(
        record("f1", "Lark report one"),
        record("f2", "Lark report two", at = ago(40)),
        record("f3", "Mail report three", source = "mail", account = "acc1", at = ago(50)),
        record("f4", "Mail other four", source = "mail", account = "acc1", at = ago(60)),
        record("f5", "Mail report five", source = "mail", account = "acc2", at = ago(70)),
        record("f6", "Mail other six", source = "mail", account = "acc2", at = ago(80)),
    )
    private val noise = listOf(
        record("n1", "Noise Lark one", tier = "noise"),
        record("n2", "Noise Lark two", tier = "noise", at = ago(40)),
        record("n3", "Noise mail three", tier = "noise", source = "mail", at = ago(50)),
        record("n4", "Noise mail four", tier = "noise", source = "mail", at = ago(60)),
    )
    private val action = listOf(
        record("a1", "Action Lark", tier = "action"),
        record("a2", "Action mail", tier = "action", source = "mail", at = ago(40)),
    )

    private class RecordingInbox(
        records: List<InboxRecord>,
        preferences: InboxPreferences = InboxPreferences(),
    ) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records, refreshes = 1))
        override val connectionState = flowOf(ConnectionState.Online)
        override val preferences = MutableStateFlow(preferences)
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
        val asked = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        val singleDone = mutableListOf<Pair<String, Boolean>>()
        val receipts = mutableListOf<String>()
        var gate: CompletableDeferred<InboxBulkResult>? = null
        var respond: (List<InboxDoneTarget>, InboxBulkAction) -> InboxBulkResult = { items, _ ->
            InboxBulkResult(changed = items.map { it.id })
        }

        override suspend fun savePreferences(value: InboxPreferences) { preferences.value = value }
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) { singleDone += itemId to done }
        override suspend fun markLatestRead(feedId: String): Boolean {
            receipts += feedId
            return true
        }

        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            asked += items to action
            val result = gate?.await() ?: respond(items, action)
            val changed = result.changed.toSet()
            if (changed.isNotEmpty()) {
                val records = snapshot.value.records.map { record ->
                    if (record.item.id !in changed) record else record.copy(
                        item = record.item.copy(doneAt = if (action == InboxBulkAction.Reopen) null else "2026-11-19T12:00:00Z"),
                    )
                }
                snapshot.value = snapshot.value.copy(records = records)
            }
            return result
        }

        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private class RecordingFeedback : AppFeedback {
        val notices = mutableListOf<FeedbackNotice>()
        override fun failure(error: Throwable, operation: Boolean) = false
        override fun notice(message: String): Boolean {
            notices += FeedbackNotice(message)
            return true
        }
        override fun notice(message: String, style: FeedbackStyle): Boolean {
            notices += FeedbackNotice(message, style = style)
            return true
        }
        override fun notice(message: String, action: FeedbackAction): Boolean {
            notices += FeedbackNotice(message, action = action)
            return true
        }
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private val feedback = RecordingFeedback()

    private fun show(inbox: RecordingInbox) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val owner = rememberCoroutineScope()
                val conversations = remember(inbox, owner) {
                    InboxConversations(inbox, "paired", owner, ReplyComposers(inbox, owner))
                }
                DisposableEffect(inbox, conversations) {
                    inbox.conversationInbox.value = conversations
                    onDispose { inbox.conversationInbox.compareAndSet(conversations, null) }
                }
                CompositionLocalProvider(LocalRippleConfiguration provides null, LocalAppFeedback provides feedback) {
                    InboxScreen(inbox, navigator)
                }
            }
        }
        rule.waitForIdle()
    }

    private fun pick(title: String) {
        rule.onNode(hasText(title) and SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected))
            .performScrollTo().performClick()
        rule.waitForIdle()
    }

    /** The first group's new action. Collapsed's older section button is index zero, so its first group is index one. */
    private fun openGroup(label: String = "Mark all seen", index: Int = 0) {
        val nodes = rule.onAllNodes(hasText(label) and hasClickAction())
        assertTrue("Expected group action $label at index $index", nodes.fetchSemanticsNodes().size > index)
        nodes[index].performScrollTo().performClick()
        rule.waitForIdle()
    }

    private fun choice(text: String) {
        rule.onNodeWithText(text).assertExists().performClick()
        rule.waitForIdle()
    }

    private fun scopeLines(text: String) {
        val inMenu = hasAnyAncestor(isDialog()) or hasAnyAncestor(isPopup())
        assertEquals("Both choices must carry the scope subtitle", 2,
            rule.onAllNodes(hasText(text) and inMenu).fetchSemanticsNodes().size)
    }

    private fun targets(ids: List<String>) = ids.map { InboxDoneTarget(it, "v-$it") }

    @Test fun fyiGroupChoiceFilesExactlyItsVersionedItemsWithoutReadReceipts() {
        val inbox = RecordingInbox(action + fyi)
        show(inbox)
        pick("FYI")
        openGroup()
        rule.onNodeWithText("Mark all 2 in “Lark” seen").assertExists()
        rule.onNodeWithText("Mark all 6 in “FYI” seen").assertExists()
        choice("Mark all 2 in “Lark” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(listOf("f1", "f2")) to InboxBulkAction.Done, inbox.asked.single())
        assertTrue("Group filing must not send source read receipts", inbox.receipts.isEmpty())
        assertTrue("Group filing must not issue per-item done requests", inbox.singleDone.isEmpty())
    }

    @Test fun fyiSectionChoiceFilesEveryShownItemButNoOtherSection() {
        val inbox = RecordingInbox(action + fyi)
        show(inbox)
        pick("FYI")
        openGroup()
        choice("Mark all 6 in “FYI” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(fyi.map { it.item.id }) to InboxBulkAction.Done, inbox.asked.single())
        assertTrue(inbox.receipts.isEmpty())
        assertTrue(inbox.singleDone.isEmpty())
    }

    @Test fun collapsedGroupChoiceUsesClearAllAndOnlyItsItems() {
        val inbox = RecordingInbox(fyi + noise, InboxPreferences(expanded = listOf("action", "fyi", "noise")))
        show(inbox)
        pick("All")
        openGroup("Clear all", index = 1)
        rule.onNodeWithText("Clear all 2 in “Lark”").assertExists()
        rule.onNodeWithText("Clear all 4 in “Collapsed”").assertExists()
        choice("Clear all 2 in “Lark”")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(listOf("n1", "n2")) to InboxBulkAction.Done, inbox.asked.single())
        assertTrue(inbox.receipts.isEmpty())
    }

    @Test fun collapsedSectionChoiceClearsTheWholeShownSection() {
        val inbox = RecordingInbox(fyi + noise, InboxPreferences(expanded = listOf("action", "fyi", "noise")))
        show(inbox)
        pick("All")
        openGroup("Clear all", index = 1)
        choice("Clear all 4 in “Collapsed”")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(noise.map { it.item.id }) to InboxBulkAction.Done, inbox.asked.single())
    }

    @Test fun kindFilterLimitsTheSectionAndNamesTheScopeUnderBothChoices() {
        val inbox = RecordingInbox(fyi, InboxPreferences(kind = "mail"))
        show(inbox)
        pick("FYI")
        openGroup()
        rule.onNodeWithText("Mark all 2 in “Mail · acc1@example.com” seen").assertExists()
        rule.onNodeWithText("Mark all 4 in “FYI” seen").assertExists()
        scopeLines("Mail")
        choice("Mark all 4 in “FYI” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(listOf("f3", "f4", "f5", "f6")) to InboxBulkAction.Done, inbox.asked.single())
    }

    @Test fun searchLimitsTheGroupAndNamesTheScopeUnderBothChoices() {
        val inbox = RecordingInbox(fyi, InboxPreferences(query = "report"))
        show(inbox)
        openGroup()
        rule.onNodeWithText("Mark all 2 in “Lark” seen").assertExists()
        rule.onNodeWithText("Mark all 4 in “FYI” seen").assertExists()
        scopeLines("Search “report”")
        choice("Mark all 2 in “Lark” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(listOf("f1", "f2")) to InboxBulkAction.Done, inbox.asked.single())
    }

    @Config(fontScale = 2f)
    @Test fun largeTextSearchKeepsTheScopeUnderBothChoices() {
        val inbox = RecordingInbox(fyi, InboxPreferences(query = "report"))
        show(inbox)
        openGroup()
        rule.onNodeWithText("Mark all 2 in “Lark” seen").assertExists()
        rule.onNodeWithText("Mark all 4 in “FYI” seen").assertExists()
        scopeLines("Search “report”")
        choice("Mark all 2 in “Lark” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(listOf("f1", "f2")) to InboxBulkAction.Done, inbox.asked.single())
    }

    @Test fun sourceFilteredTimeGroupsHaveTheSameMenuAndExactTargets() {
        val records = listOf(
            record("t1", "Today one", at = ago(5)),
            record("t2", "Today two", at = ago(10)),
            record("t3", "Yesterday", at = ago(24 * 60)),
            record("m1", "Filtered out mail", source = "mail"),
        )
        val inbox = RecordingInbox(records, InboxPreferences(source = "feishu", grouping = InboxGroups.TIME))
        show(inbox)
        pick("FYI")
        openGroup()
        rule.onNodeWithText("Mark all 2 in “Today” seen").assertExists()
        rule.onNodeWithText("Mark all 3 in “FYI” seen").assertExists()
        scopeLines("Lark")
        choice("Mark all 2 in “Today” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        assertEquals(targets(listOf("t1", "t2")) to InboxBulkAction.Done, inbox.asked.single())
    }

    @Test fun needsYouWorkingDoneAndSelectionKeepTheirExistingGroupTitles() {
        val records = action + fyi + listOf(
            record("w1", "Working", tier = "action", status = "working"),
            record("d1", "Done", doneAt = ago(5)),
        )
        val inbox = RecordingInbox(records, InboxPreferences(expanded = listOf("action", "fyi", "done")))
        show(inbox)
        rule.onAllNodesWithText("Mark all seen").assertCountEquals(0)
        pick("In progress")
        rule.onAllNodesWithText("Mark all seen").assertCountEquals(0)
        pick("Done")
        rule.onAllNodesWithText("Mark all seen").assertCountEquals(0)
        rule.onAllNodesWithText("Clear all").assertCountEquals(0)
        pick("FYI")
        rule.onNodeWithText("Lark report one").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onAllNodesWithText("Mark all seen").assertCountEquals(0)
    }

    @Test fun oneGroupKeepsTheDirectSectionReceiptAndDoesNotFileAnything() {
        val inbox = RecordingInbox(listOf(record("only1", "Only one"), record("only2", "Only two", at = ago(40))))
        show(inbox)
        pick("FYI")
        rule.onAllNodesWithText("Mark all seen").assertCountEquals(0)
        rule.onNodeWithText("Mark all as read").performClick()
        rule.waitUntil(timeoutMillis = 5_000) { inbox.receipts.size == 2 }
        assertEquals(setOf("feed-only1", "feed-only2"), inbox.receipts.toSet())
        assertTrue("The existing section receipt must not file items", inbox.asked.isEmpty())
        assertTrue(inbox.singleDone.isEmpty())
        rule.onNodeWithText("Only one").assertExists()
        rule.onNodeWithText("Only two").assertExists()
    }

    @Test fun undoReopensOnlyChangedItemsAndItemsWithNewMessagesStay() {
        val inbox = RecordingInbox(fyi)
        inbox.respond = { items, action ->
            if (action == InboxBulkAction.Done) InboxBulkResult(changed = listOf("f1"), changedSince = listOf("f2"))
            else InboxBulkResult(changed = items.map { it.id })
        }
        show(inbox)
        pick("FYI")
        openGroup()
        choice("Mark all 2 in “Lark” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        rule.onAllNodesWithText("Lark report one").assertCountEquals(0)
        rule.onNodeWithText("Lark report two").assertExists()
        val undo = feedback.notices.last().action
        assertNotNull("The bulk result must offer Undo", undo)
        assertEquals("Undo", undo!!.label)
        rule.runOnIdle { undo.run() }
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 2 }
        assertEquals(listOf(InboxDoneTarget("f1")) to InboxBulkAction.Reopen, inbox.asked[1])
        rule.onNodeWithText("Lark report one").assertExists()
        rule.onNodeWithText("Lark report two").assertExists()
    }

    @Test fun aFailedGroupRequestRestoresTheOptimisticallyRemovedRows() {
        val gate = CompletableDeferred<InboxBulkResult>()
        val inbox = RecordingInbox(fyi).apply { this.gate = gate }
        show(inbox)
        pick("FYI")
        openGroup()
        choice("Mark all 2 in “Lark” seen")
        rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == 1 }
        rule.onAllNodesWithText("Lark report one").assertCountEquals(0)
        rule.onAllNodesWithText("Lark report two").assertCountEquals(0)
        rule.runOnIdle { gate.completeExceptionally(IOException("offline")) }
        rule.waitForIdle()
        rule.onNodeWithText("Lark report one").assertExists()
        rule.onNodeWithText("Lark report two").assertExists()
        rule.onNodeWithText(ApplicationProvider.getApplicationContext<android.content.Context>()
            .getString(R.string.inbox_failed_restored)).assertExists()
    }

    @Config(qualifiers = "zh-w400dp-h1200dp-xhdpi")
    @Test fun simplifiedChineseShipsTheFyiMenuCopy() {
        val inbox = RecordingInbox(fyi)
        show(inbox)
        pick("知悉即可")
        openGroup("全部看过")
        rule.onNodeWithText("看过「飞书」的全部 2 条").assertExists()
        rule.onNodeWithText("看过「知悉即可」的全部 6 条").assertExists()
    }

    @Config(qualifiers = "zh-w400dp-h1200dp-xhdpi")
    @Test fun simplifiedChineseShipsTheCollapsedMenuCopy() {
        val inbox = RecordingInbox(fyi + noise, InboxPreferences(expanded = listOf("action", "fyi", "noise")))
        show(inbox)
        pick("全部")
        openGroup("全部清除", index = 1)
        rule.onNodeWithText("清除「飞书」的全部 2 条").assertExists()
        rule.onNodeWithText("清除「已折叠」的全部 4 条").assertExists()
    }

    @Config(qualifiers = "ja-w400dp-h1200dp-xhdpi")
    @Test fun japaneseShipsTheFyiMenuCopy() {
        val inbox = RecordingInbox(fyi)
        show(inbox)
        pick("参考")
        openGroup("すべて確認")
        rule.onNodeWithText("「Lark」の2件をすべて確認済みにする").assertExists()
        rule.onNodeWithText("「参考」の6件をすべて確認済みにする").assertExists()
    }

    @Config(qualifiers = "ja-w400dp-h1200dp-xhdpi")
    @Test fun japaneseShipsTheCollapsedMenuCopy() {
        val inbox = RecordingInbox(fyi + noise, InboxPreferences(expanded = listOf("action", "fyi", "noise")))
        show(inbox)
        pick("すべて")
        openGroup("すべてクリア", index = 1)
        rule.onNodeWithText("「Lark」の2件をすべてクリア").assertExists()
        rule.onNodeWithText("「折りたたみ済み」の4件をすべてクリア").assertExists()
    }
}
