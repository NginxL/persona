package persona.feature.inbox

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import java.time.temporal.ChronoUnit
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/** Done open while the current assistant changes: the new assistant's Done (its days and history) is read too (#3374 review). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1500dp-xhdpi")
class InboxSwitchHistoryTest {
    @get:Rule val rule = createComposeRule()
    @After fun reset() = AssistantManagerChoice.reset()

    private val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    private fun ago(minutes: Long) = now.minus(minutes, ChronoUnit.MINUTES).toString()

    private class Inbox(snapshot: InboxSnapshot, prefs: InboxPreferences) : InboxRepository {
        val daysAsked = mutableListOf<String?>()
        val historyAsked = mutableListOf<String?>()
        val prefs = MutableStateFlow(prefs)
        override val preferences = this.prefs
        override suspend fun savePreferences(value: InboxPreferences) { prefs.value = value }
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun doneHistory(manager: String?, cursor: InboxDoneCursor?): InboxDonePage {
            historyAsked += manager
            return InboxDonePage()
        }
        override suspend fun doneDays(manager: String?, zone: java.time.ZoneId): List<InboxDoneDay>? {
            daysAsked += manager
            return emptyList()
        }
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction) = InboxBulkResult(changed = items.map { it.id })
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

    @Test fun switchingWithDoneOpenReadsTheNewAssistantsDone() {
        val roster = ManagerRoster(listOf(
            HubManager("assistant", "助手", online = true, isDefault = true, builtin = true),
            HubManager("m-2", "工作", online = true),
        ), "assistant")
        fun done(id: String, manager: String) = InboxRecord(InboxItem("mail:$id", account = "acc", kind = "mail", title = "T$id",
            sender = "S", preview = "p", tier = "fyi", updatedAt = ago(30), doneAt = ago(10), version = "v-$id"),
            sourceLabel = "mail", source = "mail", manager = manager)
        val inbox = Inbox(InboxSnapshot(records = listOf(done("a", "assistant"), done("b", "m-2"))),
            InboxPreferences(section = "done", expanded = listOf("action", "fyi", "done")))
        AssistantManagerChoice.choose("hub", null, "assistant")
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.width(400.dp).height(1500.dp)) { OnAll { InboxScreen(inbox, navigator) } }
                }
            }
        }
        rule.waitForIdle()
        val before = inbox.daysAsked.toList()
        rule.runOnIdle { AssistantManagerChoice.pick("m-2") }
        rule.waitForIdle()
        assertEquals(listOf<String?>("assistant"), before)
        // Work's Done is read once it is the current assistant, Done still open.
        assertEquals(listOf<String?>("assistant", "m-2"), inbox.daysAsked)
    }
}
