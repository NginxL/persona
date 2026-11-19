package persona.feature.settings

import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewSection
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.InboxItemUiRoute

/** An extension-drawn inbox item that changes (a new message) is drawn again, and the reply being typed in it stays. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxItemBodyRedrawTest {
    @get:Rule val rule = createComposeRule()

    private class Thread : ExtensionApiStub() {
        @Volatile var renders = 0
        override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?): ExtensionView {
            renders += 1
            return ExtensionView(title = "Thread", sections = listOf(ExtensionViewSection(items = listOf(
                ExtensionViewItem("row", title = "Message $renders"),
                ExtensionViewItem("field", name = "reply", kind = "text", label = "Reply", value = ""),
            ))))
        }
    }

    @Test fun aNewMessageKeepsTheReplyBeingTyped() {
        val api = Thread()
        val slots = ExtensionSlots(api, flowOf(true), MutableSharedFlow())
        var revision by mutableIntStateOf(1)
        rule.setContent { PersonaTheme(reduceMotion = true) {
            val body = rememberInboxItemBody(slots, InboxItemUiRoute("lark:F1:1051", "lark-events", "event"), revision = revision, onSource = {})
            LazyColumn { body.items(this) }
        } }
        rule.waitUntil { rule.onAllNodes(hasText("Message 1")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNode(hasSetTextAction()).performTextInput("On my way")
        rule.runOnIdle { revision = 2 }
        rule.waitUntil { rule.onAllNodes(hasText("Message 2")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNode(hasSetTextAction()).assert(hasText("On my way"))
    }
}
