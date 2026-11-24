package persona.feature.inbox

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import kotlinx.coroutines.CompletableDeferred
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
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.util.concurrent.atomic.AtomicInteger
import persona.core.inbox.*

/**
 * The Inbox host stays composed, so a one-shot effect would not run when it returns to the front.
 * iOS reloads every time the list appears (InboxView.swift:219–220) and shows a spinner only from
 * pull-to-refresh or an empty first load (InboxView.swift:916–917).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxAppearRefreshTest {
    @get:Rule val rule = createComposeRule()

    @Test
    fun returningToResumedRefreshesWithoutThePullIndicator() {
        val inbox = CountingInbox()
        val owner = rule.runOnUiThread {
            Owner().also { it.registry.currentState = Lifecycle.State.RESUMED }
        }
        rule.setContent {
            CompositionLocalProvider(LocalLifecycleOwner provides owner) {
                PersonaTheme(reduceMotion = true) { InboxScreen(inbox, navigator) }
            }
        }
        rule.waitForIdle()
        assertEquals(1, inbox.refreshes.get())
        assertEquals(0, progressBars())
        rule.runOnUiThread { owner.registry.currentState = Lifecycle.State.CREATED }
        rule.waitForIdle()
        assertEquals("leaving the resumed state does not refresh again", 1, inbox.refreshes.get())
        rule.runOnUiThread { owner.registry.currentState = Lifecycle.State.RESUMED }
        rule.waitForIdle()
        assertEquals(2, inbox.refreshes.get())
        assertEquals(0, progressBars())
    }

    @Test
    fun aBackgroundRefreshInFlightSkipsTheNextOne() {
        val hold = CompletableDeferred<Unit>()
        val first = CountingInbox().also { it.hold = hold }
        val skipped = CountingInbox()
        val after = CountingInbox()
        val repository = mutableStateOf<InboxRepository>(first)
        val owner = rule.runOnUiThread {
            Owner().also { it.registry.currentState = Lifecycle.State.RESUMED }
        }
        rule.setContent {
            val current by repository
            CompositionLocalProvider(LocalLifecycleOwner provides owner) {
                PersonaTheme(reduceMotion = true) { InboxScreen(current, navigator) }
            }
        }
        rule.waitUntil(5_000) { first.refreshes.get() >= 1 }
        skipped.snapshot.value = inboxSnapshot("SkippedListToken")
        rule.runOnIdle { repository.value = skipped }
        rule.waitForIdle()
        rule.onNodeWithText("SkippedListToken").assertExists()
        assertEquals(1, first.refreshes.get())
        assertEquals(0, skipped.refreshes.get())
        rule.runOnIdle { hold.complete(Unit) }
        rule.waitForIdle()
        after.snapshot.value = inboxSnapshot("AfterListToken")
        rule.runOnIdle { repository.value = after }
        rule.waitForIdle()
        rule.onNodeWithText("AfterListToken").assertExists()
        assertEquals(1, after.refreshes.get())
        assertEquals(0, skipped.refreshes.get())
    }

    private fun progressBars() = rule.onAllNodes(
        SemanticsMatcher.keyIsDefined(SemanticsProperties.ProgressBarRangeInfo),
    ).fetchSemanticsNodes().size

    private class Owner : LifecycleOwner {
        val registry = LifecycleRegistry(this)
        override val lifecycle: Lifecycle get() = registry
    }

    private class CountingInbox : InboxRepository {
        val refreshes = AtomicInteger(0)
        var hold: CompletableDeferred<Unit>? = null
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(inboxSnapshot("项目群"))
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override suspend fun refresh() {
            refreshes.incrementAndGet()
            hold?.await()
        }
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
}

// The section bar opens on Needs you (InboxBar.entered). An FYI row is not in that section, so the title
// the test waits for has to be an action item.
private fun inboxSnapshot(title: String) = InboxSnapshot(records = listOf(
    InboxRecord(InboxItem("lark:f2:120", kind = "group", title = title, sender = "示例同事", preview = "周四",
        tier = "action", updatedAt = "2026-11-04T08:10:00Z"), sourceLabel = "飞书"),
))
