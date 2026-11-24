package persona.feature.inbox

import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.ManagerPins
import persona.core.domain.inbox.InboxItem
import persona.core.inbox.*

/** An inbox item's page names the item's manager on the requests it makes, and only on those. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxItemPinsTest {
    @get:Rule val rule = createComposeRule()

    @After fun reset() = AssistantManagerChoice.reset()

    private class Recording : InboxRepository {
        private val calls = mutableListOf<Pair<String, ManagerPins?>>()
        val seen: List<Pair<String, ManagerPins?>> get() = synchronized(calls) { calls.toList() }
        private suspend fun note(call: String) { val pins = currentCoroutineContext()[ManagerPins]; synchronized(calls) { calls += call to pins } }
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(
            InboxRecord(InboxItem("lark:other", title = "From another manager"), feedId = "chat", manager = "m-3"))))
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flow { note("conversation"); emit(LarkConversation()) }
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = note("refresh")
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    @Test fun anItemsPageNamesItsManagerOnItsOwnRequestsOnly() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val feed = Recording()
        rule.setContent {
            val page = pinnedForItem(feed, "lark:other")
            LaunchedEffect(page) { page.refreshConversation("chat") }
            page.conversation("chat", null).collectAsState(LarkConversation())
        }
        rule.waitUntil(5_000) { feed.seen.map { it.first }.containsAll(listOf("conversation", "refresh")) }
        rule.runOnIdle {
            val chat = listOf("lark", "conversations", "chat")
            feed.seen.forEach { (call, pins) -> assertEquals(call, "m-3", pins?.manager(chat)) }
            // The same repository used elsewhere while the page is open names nothing.
            runBlocking { feed.refreshConversation("chat") }
            assertNull(feed.seen.last().second)
        }
    }
}
