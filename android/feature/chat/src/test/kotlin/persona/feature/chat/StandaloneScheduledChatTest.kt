package persona.feature.chat

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.isDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Injected
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.MessageSource
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class StandaloneScheduledChatTest {
    @get:Rule val rule = createComposeRule()

    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }

    private fun repository(items: List<Item>) = object : ChatRepository {
        override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = items, loading = false, verified = true))
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
    }

    private fun sessionsOf(vararg list: Session) = object : SessionsRepository {
        override val machines = MutableStateFlow(emptyList<Machine>())
        override val sessions = MutableStateFlow(list.toList())
        override suspend fun refresh() = Unit
    }

    @Test fun standaloneScheduledNotificationChatShowsRepliesAcrossAllStyles() {
        val scheduledKey = SessionKey("computer", "sched-chat")
        val transcript = listOf(
            Item("u1", 1, ts = "2026-11-18T09:00:00Z", role = "user", kind = "text",
                text = "Run morning check",
                source = MessageSource("patrol", title = "Morning check"),
                injected = Injected("followup", "daily", "Morning check", line = "Scheduled task started")),
            Item("use2", 2, ts = "2026-11-18T09:00:02Z", role = "assistant", kind = "tool_use",
                toolUseId = "call2", toolName = "mcp_persona_0c671287_notify_user"),
            Item("res3", 3, ts = "2026-11-18T09:00:03Z", role = "user", kind = "tool_result",
                toolUseId = "call2", text = "Notification sent."),
            Item("a4", 4, ts = "2026-11-18T09:00:04Z", role = "assistant", kind = "text",
                text = "Daily briefing pushed to your phone."),
        )
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                ChatScreen(
                    sessionKey = scheduledKey,
                    repository = repository(transcript),
                    sender = sender,
                    interactions = interactions,
                    sessions = sessionsOf(Session("computer", "sched-chat", title = "Morning check", status = "idle", assistant = true)),
                    onBack = {},
                    chrome = ChatChromeBindings(mainConversation = "main", leaderConversations = setOf("lead-1")),
                    renderStyle = ChatRenderStyle.Assistant,
                )
            }
        }
        for (candidate in AppStyle.entries) {
            style = candidate
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            // Projection and list placement finish off the main thread; Compose idle does not cover that work (#10747).
            rule.waitUntil(20_000) { rule.onNodeWithText("Daily briefing pushed to your phone.").isDisplayed() }
            rule.onNodeWithText("Daily briefing pushed to your phone.").assertIsDisplayed()
        }
    }

    @Test fun mainConversationShowsNotifiedAntigravitySupervisionRoundsWhileKeepingQuietOnesHidden() {
        val mainKey = SessionKey("computer", "main")
        val transcript = listOf(
            Item("u1", 1, ts = "2026-11-18T09:00:00Z", role = "user", kind = "text",
                text = "Supervision: 1 task running",
                injected = Injected("system", "supervise", "Supervision")),
            Item("a2", 2, ts = "2026-11-18T09:00:02Z", role = "assistant", kind = "text",
                text = "Nothing needs the owner right now."),
            Item("u3", 3, ts = "2026-11-18T09:05:00Z", role = "user", kind = "text",
                text = "Supervision: deployment finished",
                injected = Injected("system", "supervise", "Supervision")),
            Item("use4", 4, ts = "2026-11-18T09:05:01Z", role = "assistant", kind = "tool_use",
                toolUseId = "call4", toolName = "mcp_persona_0c671287_notify_user"),
            Item("res5", 5, ts = "2026-11-18T09:05:02Z", role = "user", kind = "tool_result",
                toolUseId = "call4", text = "Notification sent."),
            Item("a6", 6, ts = "2026-11-18T09:05:03Z", role = "assistant", kind = "text",
                text = "Deployment finished and pushed."),
        )
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                ChatScreen(
                    sessionKey = mainKey,
                    repository = repository(transcript),
                    sender = sender,
                    interactions = interactions,
                    sessions = sessionsOf(Session("computer", "main", title = "Persona", status = "idle", assistant = true)),
                    onBack = {},
                    chrome = ChatChromeBindings(mainConversation = "main"),
                    renderStyle = ChatRenderStyle.Assistant,
                    embedded = true,
                    showTopBar = false,
                )
            }
        }
        for (candidate in AppStyle.entries) {
            style = candidate
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            // Projection and list placement finish off the main thread; Compose idle does not cover that work.
            rule.waitUntil(20_000) { rule.onNodeWithText("Deployment finished and pushed.").isDisplayed() }
            rule.onNodeWithText("Deployment finished and pushed.").assertIsDisplayed()
            rule.onNodeWithText("Nothing needs the owner right now.").assertDoesNotExist()
        }
    }
}
