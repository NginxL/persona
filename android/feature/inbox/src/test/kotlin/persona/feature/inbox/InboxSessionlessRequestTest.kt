package persona.feature.inbox

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
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
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator
import persona.core.navigation.RequestRef
import persona.core.inbox.*

/**
 * A request card outside any session (`personad ask` in a plain terminal, #5610) opens that card on Agents, as its
 * notification does (#4808); a card in a session still opens its conversation.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxSessionlessRequestTest {
    @get:Rule val rule = createComposeRule()

    private class FakeInbox(snapshot: InboxSnapshot) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigated = mutableListOf<NavKey>()
    private val cards = mutableListOf<RequestRef>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { navigated += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Test fun sessionlessRequestOpensItsCardAndASessionOneItsChat() {
        val inbox = FakeInbox(InboxSnapshot(interactions = listOf(
            InboxInteraction("script", "machine", "", "2026-11-04T08:00:00Z", title = "Run the backup script?"),
            InboxInteraction("plan", "machine", "s1", "2026-11-04T08:01:00Z", sessionTitle = "Example session", title = "Approve the plan"),
        )))
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxScreen(inbox, navigator, onOpenCard = { cards += it }) } }
        rule.waitForIdle()
        rule.onNodeWithText("Run the backup script?").performClick()
        rule.waitForIdle()
        assertEquals(listOf(RequestRef("machine", "script")), cards)
        assertEquals(emptyList<NavKey>(), navigated)
        rule.onNodeWithText("Example session").performClick()
        rule.waitForIdle()
        assertEquals(listOf<NavKey>(ChatRoute("machine", "s1")), navigated)
        assertEquals(1, cards.size)
    }
}
