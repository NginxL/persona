package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.IOException

/**
 * A new conversation's first message, as iOS shows it (#4339 C18, iOS `FirstMessagePending` and `FailedStart`): your
 * bubble and "Sending…" while the conversation starts, the composer empty; a start that failed keeps the message as a
 * failed bubble with why, Resend and Delete (with Undo), on the device, and the page takes no other message meanwhile.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class FirstMessageTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Before fun setup() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun cleanup() {
        AssistantManagerChoice.reset()
        persona.feature.chat.ForwardPlacements.home = emptyList()
    }

    private inner class Hub : AssistantRepository, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = main, conversations = listOf(
            Session(machineId = "mac", sessionId = "main", title = "Main", assistant = true),
        )))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val starts = mutableListOf<SessionKey>()
        val sent = mutableListOf<Pair<SessionKey, String>>()
        var gate: CompletableDeferred<Unit>? = null
        var refuse = false
        var failSend = false
        override suspend fun refresh() = Unit
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            if (failSend) { failSend = false; throw IOException("outbox closed") }
            sent += session to text
        }
        suspend fun start(): SessionKey {
            gate?.await()
            if (refuse) { refuse = false; throw IOException("offline") }
            return SessionKey("mac", "new-${starts.size + 1}").also { starts += it }
        }
    }

    private fun mount(hub: Hub = Hub(), style: () -> AppStyle = { AppStyle.Glass }): Hub {
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = style()) {
                CompositionLocalProvider(LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "Helper", online = true, isDefault = true),
                ), "assistant")) {
                    AssistantScreen(navigator, hub, hub, hub::start, composerService = null, mainConversationSender = hub,
                        chatContent = { Text("chat:${it.sessionId}") })
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("chat:main").assertExists()
        rule.onNodeWithContentDescription("New chat").performClick()
        rule.waitForIdle()
        return hub
    }
    private fun field() = rule.onNode(hasSetTextAction())
    private fun send(text: String) {
        field().performTextReplacement(text)
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitForIdle()
    }
    private fun kept(): String? = rule.activity.getSharedPreferences("assistant-failed-start", android.content.Context.MODE_PRIVATE)
        .all.entries.firstOrNull { it.key.endsWith(".text") }?.value as String?

    @Test fun theBubbleAndSendingStandOnThePageWhileTheConversationStarts() {
        val hub = Hub().apply { gate = CompletableDeferred() }
        mount(hub)
        send("Plan a weekend in the mountains")
        rule.onNodeWithTag("assistant-first-sending").assertExists()
        rule.onNodeWithText("Sending…").assertExists()
        // The words left the composer for the bubble: they show once, and nothing more can be typed meanwhile.
        rule.onAllNodesWithText("Plan a weekend in the mountains").assertCountEquals(1)
        rule.onAllNodes(hasSetTextAction()).assertCountEquals(0)
        rule.runOnIdle { hub.gate?.complete(Unit) }
        rule.waitUntil(5_000) { hub.sent.isNotEmpty() }
        rule.waitForIdle()
        assertEquals(listOf(SessionKey("mac", "new-1") to "Plan a weekend in the mountains"), hub.sent)
        rule.onNodeWithText("chat:new-1").assertExists()
        rule.onAllNodesWithTag("assistant-first-sending").assertCountEquals(0)
        assertNull(kept())
    }

    @Test fun aFailedStartKeepsTheMessageWithResendAndDelete() {
        val hub = mount(Hub().apply { refuse = true })
        send("Plan a weekend in the mountains")
        rule.onNodeWithTag("assistant-first-failed").assertExists()
        rule.onNodeWithText("Not sent", substring = true).assertExists()
        assertEquals("Kept on the device", "Plan a weekend in the mountains", kept())
        // The page takes no other first message while this one is there; the composer gave its words to the bubble.
        rule.onAllNodesWithText("Plan a weekend in the mountains").assertCountEquals(1)
        rule.onAllNodes(hasSetTextAction()).assertCountEquals(0)

        // Delete: gone, Undo puts it back.
        rule.onNodeWithText("Delete").performClick()
        rule.waitForIdle()
        rule.onAllNodesWithTag("assistant-first-failed").assertCountEquals(0)
        assertNull(kept())
        rule.onNodeWithText("Undo").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-first-failed").assertExists()
        assertEquals("Plan a weekend in the mountains", kept())

        // Resend starts the conversation and sends it there.
        rule.onNodeWithText("Resend").performClick()
        rule.waitUntil(5_000) { hub.sent.isNotEmpty() }
        rule.waitForIdle()
        assertEquals(listOf(SessionKey("mac", "new-1")), hub.starts)
        assertEquals(listOf(SessionKey("mac", "new-1") to "Plan a weekend in the mountains"), hub.sent)
        rule.onNodeWithText("chat:new-1").assertExists()
        assertNull(kept())
    }

    // The conversation starts, then the message never reaches the outbox: the bubble already took the forward off the
    // composer, so putting it back would send it twice — once from Resend, once with the next message.
    @Test fun aForwardWhoseFirstSendFailsStaysOnItsBubbleOnly() {
        val resources = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val ref = persona.feature.chat.ForwardMessage.ref("Original reply",
            persona.feature.chat.ForwardMessage.Source(SessionKey("mac", "source"), "a#1",
                persona.feature.chat.ForwardMessage.Role.Agent, "Claude", "Project"), resources)
        persona.feature.chat.ForwardPlacements.home = listOf(ref)
        val hub = mount(Hub().apply { failSend = true })
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-first-failed").assertExists()
        rule.onNodeWithText("Forwarded from Claude · Project").assertExists()
        assertTrue("The failed bubble owns the only forward", persona.feature.chat.ForwardPlacements.home.isEmpty())

        rule.onNodeWithText("Resend").performClick()
        rule.waitUntil(5_000) { hub.sent.isNotEmpty() }
        rule.waitForIdle()
        assertTrue("Retry leaves no forward for a later chat", persona.feature.chat.ForwardPlacements.home.isEmpty())
    }

    @Test fun aForwardedFailureKeepsItsCardWithoutPlacingItAgain() {
        val resources = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val ref = persona.feature.chat.ForwardMessage.ref("Original reply",
            persona.feature.chat.ForwardMessage.Source(SessionKey("mac", "source"), "a#1",
                persona.feature.chat.ForwardMessage.Role.Agent, "Claude", "Project"), resources)
        persona.feature.chat.ForwardPlacements.home = listOf(ref)
        val hub = mount(Hub().apply { refuse = true })
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-first-failed").assertExists()
        rule.onNodeWithText("Forwarded from Claude · Project").assertExists()
        rule.onNodeWithText("Original reply").assertExists()
        assertTrue("The failed bubble owns the only forward", persona.feature.chat.ForwardPlacements.home.isEmpty())

        rule.onNodeWithText("Resend").performClick()
        rule.waitUntil(5_000) { hub.sent.isNotEmpty() }
        rule.waitForIdle()
        assertEquals(listOf(SessionKey("mac", "new-1") to
            persona.feature.chat.forwardMessage("", listOf(ref), resources).trim()), hub.sent)
        assertTrue("Retry leaves no forward for a later chat", persona.feature.chat.ForwardPlacements.home.isEmpty())
    }

    @Test fun aKeptFailureComesBackWithThePage() {
        val prefs = FailedStarts(androidx.test.core.app.ApplicationProvider.getApplicationContext())
        val failed = FirstMessage("Book a table for Friday", failed = true, reason = "offline", at = 1_762_000_000_000L)
        prefs.write("hub/assistant", failed)
        // When it was sent is kept with it: the day over its bubble is that day.
        assertEquals(failed, prefs.read("hub/assistant"))
        mount()
        rule.onNodeWithTag("assistant-first-failed").assertExists()
        rule.onNodeWithText("Book a table for Friday").assertExists()
        rule.onNodeWithText("Not sent: offline").assertExists()
        // Sending is not a failure: nothing of it is kept.
        prefs.write("hub/assistant", FirstMessage("Book a table for Friday"))
        assertNull(prefs.read("hub/assistant"))
    }

    // iOS's DayLine over the bubble (#1911): the day stands there from the start, as the conversation's own will have it, so it
    // does not appear above the message later. Glass only: Brief and Board keep the page as it was.
    @Test fun theDayStandsOverTheBubbleOnGlassOnly() {
        var style by mutableStateOf(AppStyle.Glass)
        val hub = Hub().apply { gate = CompletableDeferred() }
        mount(hub) { style }
        send("Plan a weekend in the mountains")
        rule.onNodeWithTag("assistant-first-sending").assertExists()
        rule.onNodeWithTag("assistant-first-day").assertExists().assert(hasText("Today · ", substring = true))
        for (s in listOf(AppStyle.Brief, AppStyle.Board)) {
            style = s
            rule.waitForIdle()
            rule.onNodeWithTag("assistant-first-sending").assertExists()
            rule.onAllNodesWithTag("assistant-first-day").assertCountEquals(0)
        }
        style = AppStyle.Glass
        rule.waitForIdle()
        rule.onAllNodesWithTag("assistant-first-day").assertCountEquals(1)
        rule.runOnIdle { hub.gate?.complete(Unit) }
        rule.waitUntil(5_000) { hub.sent.isNotEmpty() }
    }

    // A failed start kept on the device says the day it was sent, not the day it is shown (iOS `FailedStart.Message.at`).
    @Test fun aFailedStartSaysTheDayItWasSent() {
        val prefs = FailedStarts(androidx.test.core.app.ApplicationProvider.getApplicationContext())
        prefs.write("hub/assistant", FirstMessage("Book a table for Friday", failed = true, reason = "offline",
            at = java.time.LocalDate.now().minusDays(1).atTime(12, 0).atZone(java.time.ZoneId.systemDefault()).toInstant().toEpochMilli()))
        mount()
        rule.onNodeWithTag("assistant-first-day").assertExists().assert(hasText("Yesterday · ", substring = true))
    }

    // The page that starts the main conversation when there is none yet draws the messages it sent to its queue, not iOS's
    // first-message page: on Glass the day stands over them too, the day the first was written.
    @Test fun theMainQueuePageHasTheDayOnGlassOnly() {
        val sentAt = java.time.LocalDate.now().minusDays(1).atTime(12, 0).atZone(java.time.ZoneId.systemDefault()).toInstant().toEpochMilli()
        val queued = listOf(OutboxMessage("c1", main, "Plan a weekend", DeliveryState.Sent, mainTarget = true, createdAt = sentAt))
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent { PersonaTheme(reduceMotion = true, style = style) { AssistantPendingConversation(queued, null) } }
        rule.onNodeWithTag("assistant-pending-day").assertExists().assert(hasText("Yesterday · ", substring = true))
        for (s in listOf(AppStyle.Brief, AppStyle.Board)) {
            style = s
            rule.waitForIdle()
            rule.onAllNodesWithTag("assistant-pending-day").assertCountEquals(0)
        }
    }

    @Test fun noDayStandsOverNothing() {
        rule.setContent { PersonaTheme(reduceMotion = true, style = AppStyle.Glass) { AssistantPendingConversation(emptyList(), null) } }
        rule.onAllNodesWithTag("assistant-pending-day").assertCountEquals(0)
    }
}
