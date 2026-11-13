package persona.feature.chat

import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.junit4.StateRestorationTester
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository

/**
 * The chat page's saved state survives the activity being made again: a message quoted over the composer
 * (`linkContexts`, a `rememberSaveable` in ChatPage) is still there after a saved-instance-state restore.
 * Guards moving ChatPage's set-up code into helpers: the saved values are found by their place in the composition.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class ChatPageStateRestorationTest {
    @get:Rule val rule = createComposeRule()

    private val key = SessionKey("macbook", "weather")
    private val items = listOf(
        Item("u1", 1, role = "user", kind = "text", text = "Add a home screen widget", ts = "2026-11-10T10:00:00Z"),
        Item("a2", 2, role = "assistant", kind = "text", text = "The widget shows the next three hours.", ts = "2026-11-10T10:00:05Z"),
    )
    private val done = Session(machineId = key.machineId, sessionId = key.sessionId, title = "Weather widget", status = "done")
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(emptyList<Machine>())
        override val sessions = MutableStateFlow(listOf(done))
        override suspend fun refresh() = Unit
    }
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = items, loading = false))
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
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

    private val quotable = SemanticsMatcher("has Quote") { node ->
        node.config.getOrNull(SemanticsActions.CustomActions)?.any { it.label == "Quote" } == true
    }

    private fun texts(): List<String> = rule.onAllNodes(SemanticsMatcher("any node") { true }, useUnmergedTree = true)
        .fetchSemanticsNodes().flatMap { node -> node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } }

    private fun quoting(): Boolean = texts().any { it.startsWith("Quoting ") }

    @Test fun aQuoteOverTheComposerSurvivesTheActivityBeingMadeAgain() {
        val restoration = StateRestorationTester(rule)
        restoration.setContent { ChatScreen(key, chats, sender, interactions, sessions, onBack = {}) }

        // The lines are built off the main thread: wait for the reply's row and its "Quote" action.
        rule.waitUntil(30_000) { rule.onAllNodes(quotable).fetchSemanticsNodes().isNotEmpty() }
        val reply = rule.onAllNodes(quotable).fetchSemanticsNodes().first { node ->
            node.config.getOrNull(SemanticsProperties.Text).orEmpty().any { "next three hours" in it.text }
        }
        val action = reply.config[SemanticsActions.CustomActions].single { it.label == "Quote" }
        rule.runOnIdle { action.action() }
        rule.waitUntil(10_000) { quoting() }

        restoration.emulateSavedInstanceStateRestore()
        rule.waitUntil(30_000) { rule.onAllNodes(quotable).fetchSemanticsNodes().isNotEmpty() }
        rule.waitForIdle()
        assertTrue(texts().toString(), quoting())
    }
}
