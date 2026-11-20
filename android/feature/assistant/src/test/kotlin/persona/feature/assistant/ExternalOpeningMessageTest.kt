package persona.feature.assistant

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.ImagePayload
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.HubChannel
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.LocalChannels
import persona.core.designsystem.PersonaTheme
import persona.feature.chat.ChatRenderStyle
import persona.feature.chat.ChatScreen
import persona.feature.chat.LocalOpeningMessageSource

/** The actual Assistant chat reader, including projection, pagination, grouping photos, and the drawer's source renderer. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp")
class ExternalOpeningMessageTest {
    @get:Rule val rule = createComposeRule()

    private val row = Session("computer", "external", title = "Weekly report", assistant = true, origin = " extension ")
    private val sessions = object : SessionsRepository {
        override val sessions = MutableStateFlow(listOf(row))
        override val machines = MutableStateFlow(emptyList<Machine>())
        override suspend fun refresh() = Unit
    }
    private val snapshot = MutableStateFlow(ChatSnapshot(loading = false))
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = snapshot
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private var style by mutableStateOf(ChatRenderStyle.Assistant)
    private val first = Item("first", 1, role = "user", kind = "text", text = "Prepare the report")
    private val next = Item("next", 3, role = "user", kind = "text", text = "Include the totals")
    private val reply = Item("reply", 4, role = "assistant", kind = "text", text = "Report ready")

    private fun show(initial: ChatSnapshot) {
        snapshot.value = initial
        val channels = listOf(HubChannel("wechat", "WeChat", "im", "plugin"))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalOpeningMessageSource provides ::conversationOpeningSource, LocalChannels provides channels) {
                    ChatScreen(row.key, chats, sender, interactions, sessions, onBack = {}, renderStyle = style)
                }
            }
        }
    }

    private fun shows(text: String) = rule.waitUntil(10_000) { rule.onNodeWithText(text).isDisplayed() }
    private fun marks(name: String) = rule.onAllNodesWithContentDescription(name)
    private fun one(name: String) = rule.waitUntil(10_000) { marks(name).fetchSemanticsNodes().size == 1 }

    @Test fun waitsForTheKnownStartAndMarksOnlyTheOpeningBubble() {
        show(ChatSnapshot(listOf(next, reply), loading = false, hasOlder = true))
        shows("Include the totals")
        marks("From an extension").assertCountEquals(0)

        snapshot.value = ChatSnapshot(listOf(first, next, reply), loading = false, verified = false)
        shows("Prepare the report")
        marks("From an extension").assertCountEquals(0)
        snapshot.value = snapshot.value.copy(olderBoundaryKnown = true) // A complete cache needs no network round trip.
        one("From an extension")
        val source = hasContentDescription("From an extension")
        rule.onNode(hasText("Prepare the report") and source).assertExists()
        rule.onNode(hasText("Include the totals") and source).assertDoesNotExist()
    }

    @Test fun usesTheMessagesOwnChannelAndIgnoresOtherOriginsAndAgentPages() {
        sessions.sessions.value = listOf(row.copy(origin = " lark_agent "))
        show(ChatSnapshot(listOf(first.copy(clientMsgId = "wechat-first"), reply), loading = false))
        shows("Prepare the report")
        one("From WeChat")
        marks("From Lark /agent").assertCountEquals(0)

        snapshot.value = snapshot.value.copy(items = listOf(first, reply))
        one("From Lark /agent")
        marks("From WeChat").assertCountEquals(0)
        for (origin in listOf(null, "user", "channel", "inbox", "task", "spawned", "new-origin")) {
            sessions.sessions.value = listOf(row.copy(origin = origin))
            rule.waitForIdle()
            marks("From Lark /agent").assertCountEquals(0)
            marks("From an extension").assertCountEquals(0)
        }
        sessions.sessions.value = listOf(row)
        one("From an extension")
        style = ChatRenderStyle.Agent
        rule.waitForIdle()
        marks("From an extension").assertCountEquals(0)
    }

    @Test fun openingPhotoStaysMarkedWhenGroupedWithItsTextAndCannotBeAToolImage() {
        val picture = Item("picture", 1, role = "user", kind = "image", image = ImagePayload(width = 24, height = 24))
        val toolPicture = picture.copy(id = "tool-result", seq = 0, toolUseId = "read")
        show(ChatSnapshot(listOf(toolPicture, picture, reply), loading = false))
        shows("Report ready")
        one("From an extension")

        snapshot.value = snapshot.value.copy(items = listOf(toolPicture, picture, next, reply))
        shows("Include the totals")
        one("From an extension")
        snapshot.value = snapshot.value.copy(items = listOf(toolPicture, picture.copy(clientMsgId = "wechat-photo"), next, reply))
        one("From WeChat")
        marks("From an extension").assertCountEquals(0)
        snapshot.value = snapshot.value.copy(items = listOf(toolPicture, reply))
        rule.waitForIdle()
        marks("From WeChat").assertCountEquals(0)
        marks("From an extension").assertCountEquals(0)
    }
}
