package persona.feature.inbox

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/** The filter button's dot is color only; TalkBack has to hear that a filter is on (#3729). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxFilterA11yTest {
    @get:Rule val rule = createComposeRule()

    private class FakeInbox(prefs: InboxPreferences) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot())
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override val preferences = flowOf(prefs)
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

    private val navigator = object : Navigator {
        override fun navigate(key: androidx.navigation3.runtime.NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun show(prefs: InboxPreferences) {
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxScreen(FakeInbox(prefs), navigator) } }
        rule.waitForIdle()
    }

    @Test fun anActiveFilterIsSpokenNotOnlyDotted() {
        show(InboxPreferences(source = "lark"))
        rule.onNodeWithContentDescription("Filter (on)").assertExists()
    }

    @Test fun noFilterReadsPlainFilter() {
        show(InboxPreferences())
        rule.onNodeWithContentDescription("Filter").assertExists()
    }
}
