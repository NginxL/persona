package persona.feature.inbox

import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performTouchInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.LarkReaction
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.inbox.*

/**
 * #5306: a recalled message Keep recalled messages kept is shown in full, yet it takes no reactions, replies or card writes
 * (docs/protocol/protocol.md, `retained`), as on iOS. A live message beside it keeps them, so the checks can fail.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class LarkRecalledActionsTest {
    @get:Rule val rule = createComposeRule()

    private object Transport : InboxAssistantTransport {
        override val scope = "pairing"
        override suspend fun inbox() = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun assistant() = error("unused")
        override suspend fun createConversation(main: Boolean) = error("unused")
        override suspend fun pins() = error("unused")
        override suspend fun mutes() = error("unused")
        override suspend fun media(ref: String) = error("unused")
    }

    private object Fake : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot())
        override val connectionState = flowOf(ConnectionState.Online)
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

    /** Words, a reaction, a reply and a card with a callback button: every write a bubble offers. */
    private fun message(id: String, text: String, recalled: Boolean) = LarkMessage(id, "u2", "示例同事", position = 1,
        time = "2026-11-04T09:30:00Z", parts = listOf(LarkPart.Text(text),
            LarkPart.Card(LarkCardContent("", listOf(LarkCardBlock.Button("确认$id", actionId = "a1", actionMethod = 2)), ""),
                LarkContentSource(Transport), messageId = id)),
        reactions = listOf(LarkReaction("THUMBSUP", 2, mine = false)), replyCount = 1,
        recalled = recalled, retained = recalled, system = recalled)

    private fun show(message: LarkMessage) = rule.setContent {
        PersonaTheme(reduceMotion = true) {
            CompositionLocalProvider(LocalLarkMessageDisplay provides LarkMessageDisplay("acct", antiRecall = true)) {
                Column {
                    LarkBubble(message, true, Fake, null, retrying = false, onRetry = {}, onError = {}, onThread = {},
                        onReact = { _, _, _ -> }, onOpenReactionPicker = {})
                }
            }
        }
    }

    // The bubble's middle can be the card's button: the long press goes to the corner of its words.
    private fun longPress(text: String) {
        rule.onNodeWithText(text).performTouchInput { longClick(topLeft + Offset(4f, 4f)) }
        rule.waitForIdle()
    }

    @Test fun aRecalledMessageKeptTakesNoReactionReplyOrCardWrite() {
        show(message("d", "我先撤回这句", recalled = true))
        longPress("我先撤回这句")
        rule.onNode(hasText("拷贝")).assertExists()
        rule.onNode(hasContentDescription("更多表情")).assertDoesNotExist()
        rule.onNode(hasText("👍")).assertDoesNotExist()
        rule.onNode(hasContentDescription("表情回复")).assertDoesNotExist()
        rule.onNodeWithText("1 条回复").assertDoesNotExist()
        rule.onNodeWithText("确认d").assertIsNotEnabled()
    }

    @Test fun aLiveMessageKeepsThem() {
        show(message("a", "下午三点的会挪到四点", recalled = false))
        rule.onNodeWithText("确认a").assertIsEnabled()
        rule.onNodeWithText("1 条回复").assertExists()
        rule.onNode(hasContentDescription("表情回复")).assertExists()
        longPress("下午三点的会挪到四点")
        rule.onNode(hasContentDescription("更多表情")).assertExists()
    }
}
