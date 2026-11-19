package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.update
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.*
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.hub.HttpHeldAgentActions
import persona.core.hub.HeldAgentRetryReceipts

/** The production chat against a second phone's session updates. Only the manager may replace or remove a held row. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class HeldAgentChatTest {
    @get:Rule val rule = createComposeRule()
    private val key = SessionKey("studio", "release")
    private val list = MutableStateFlow(listOf(Session(machineId = key.machineId, sessionId = key.sessionId,
        title = "Release notes", status = "idle", agent = "claude", cwd = "/home/me/release")))
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Studio Mac", online = false)))
        override val sessions = list
        override suspend fun refresh() = Unit
    }
    private val view = MutableStateFlow(ChatSnapshot(loading = false))
    private val attempts = mutableListOf<Pair<String, String>>()
    private var answer: CompletableDeferred<Unit>? = null
    private val pairingScope = MainScope()
    @After fun closePairing() = pairingScope.cancel()
    private val actions = object : HeldAgentActions {
        override val features = MutableStateFlow(setOf(AgentMessageHoldFeature))
        override fun canRetry(row: HeldAgentMessage) = flowOf(true)
        override suspend fun retry(row: HeldAgentMessage) { attempts += "retry" to row.id; answer?.await() }
        override suspend fun cancel(id: String) { attempts += "cancel" to id; answer?.await() }
    }
    private var sessionActions: (SessionKey) -> HeldAgentActions? = { session -> actions.takeIf { session == key } }
    private val chats = object : ChatRepository, ChatLocalStorage {
        override val localStorage = MutableStateFlow<ChatStorageSnapshot?>(null)
        override fun heldMessages(session: SessionKey) = sessionActions(session)
        override suspend fun imageFile(session: SessionKey, item: Item, width: Int, height: Int): File = error("no image bytes")
        override fun observe(session: SessionKey) = view
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
    }
    private val sends = mutableListOf<Pair<String, String>>()
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            assertEquals(key, session)
            sends += clientMessageId to text
            outbox.value = listOf(OutboxMessage(clientMessageId, key, text, DeliveryState.Sending))
        }
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = error("no pending interaction")
    }
    private val waiting = HeldAgentMessage("held-next", "Summarize the release notes.", computer = "Studio Mac")
    private val expired = HeldAgentMessage("held-expired", "Run the release checks.", computer = "Studio Mac", state = "failed",
        error = "Not sent: Studio Mac was away for 24 hours")
    private val uncertain = HeldAgentMessage("held-uncertain", "Review the screenshots.", images = 2, computer = "Studio Mac",
        state = "failed", error = "Might not have arrived. Retry checks the chat first.")
    private var appStyle by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var history by mutableStateOf(false)
    private var chatOpen by mutableStateOf(true)

    private fun push(vararg rows: HeldAgentMessage) { list.value = list.value.map { it.copy(undelivered = rows.toList()) } }
    private fun show() {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = appStyle) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                    if (chatOpen) ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, historyReadOnly = history)
                }
            }
        }
    }
    private fun action(row: HeldAgentMessage, label: String) =
        rule.onNode(hasText(label) and hasAnyAncestor(hasTestTag("held-${row.id}")))

    @Test fun secondPhoneShowsEveryDeliveryStateInEveryStyleAndAppearance() = captureQueue(large = false)

    // Applies the system font scale to the host Activity and to the separate confirmation dialog window.
    @Test @Config(fontScale = 2f)
    fun secondPhoneQueueAndRemovalAtMaximumFontScale() = captureQueue(large = true)

    private fun captureQueue(large: Boolean) {
        push(waiting, expired, uncertain)
        show()
        val out = File(System.getProperty("persona.shots") ?: "build/held-agent-shots").also { it.mkdirs() }
        val clipped = mutableListOf<String>()
        val size = if (large) "-large" else ""
        for (style in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { appStyle = style; dark = night }
            for (row in listOf(waiting, expired, uncertain)) rule.onAllNodesWithText(row.text).assertCountEquals(1)
            rule.onNodeWithText("Will send when Studio Mac is back").assertIsDisplayed()
            rule.onNodeWithText(expired.error!!).assertIsDisplayed()
            rule.onNodeWithText(uncertain.error!!).assertIsDisplayed()
            rule.onNodeWithText("2 images").assertIsDisplayed()
            val hint = rule.onNodeWithText("Studio Mac is offline. Sends when it’s back.", useUnmergedTree = true)
            val layouts = mutableListOf<TextLayoutResult>()
            hint.performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(layouts) }
            val textLayout = layouts.single()
            val last = textLayout.lineCount - 1
            if (textLayout.isLineEllipsized(last) || textLayout.getLineEnd(last, visibleEnd = true) != textLayout.layoutInput.text.length)
                clipped += "$style/$night/$size (${textLayout.lineCount} lines, end ${textLayout.getLineEnd(last, true)} of ${textLayout.layoutInput.text.length})"
            action(waiting, "Retry").assertDoesNotExist()
            action(waiting, "Don't send").assertIsEnabled()
            action(uncertain, "Retry").assertIsEnabled()
            File(out, "held-${style.name.lowercase()}-${if (night) "dark" else "light"}$size.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
        for (style in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { appStyle = style; dark = night }
            action(uncertain, "Delete").performClick()
            rule.onNodeWithText("Remove this message from the queue?").assertIsDisplayed()
            File(out, "remove-${style.name.lowercase()}-${if (night) "dark" else "light"}$size.png").outputStream().use {
                assertTrue(rule.onNode(isDialog()).captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
            rule.onNodeWithText("Cancel").performClick()
        }
        assertTrue("The full offline hint must be readable: $clipped", clipped.isEmpty())
        assertTrue(sends.isEmpty())
        assertTrue(attempts.isEmpty())
    }

    @Test fun composerHandsOffToManagerAndTheSecondPhoneCanCancel() {
        show()
        rule.onNode(hasSetTextAction()).performTextInput(waiting.text)
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitUntil(10_000) { sends.size == 1 }
        val (op, text) = sends.single()
        val row = waiting.copy(id = op)
        rule.runOnIdle { push(row) } // The session broadcast can beat the send's HTTP response.
        rule.onAllNodesWithText(text).assertCountEquals(1)
        action(row, "Don't send").performClick()
        rule.onNodeWithText("Cancel").performClick()
        assertTrue(attempts.isEmpty())
        action(row, "Don't send").performClick()
        rule.onNode(hasText("Don't send") and hasAnyAncestor(isDialog())).performClick()
        assertEquals(listOf("cancel" to op), attempts)
        rule.onNodeWithTag("held-$op").assertExists()
        rule.runOnIdle {
            sender.outbox.value = listOf(OutboxMessage(op, key, text, DeliveryState.Held))
            push()
        }
        rule.onNodeWithText(text).assertDoesNotExist()
        assertEquals(1, sends.size)
    }

    @Test fun aLateTranscriptEchoReplacesTheRemoteBubbleWhileAnOfflineComputerStillShowsItsSendPolicy() {
        list.value = list.value.map { it.copy(status = "working") }
        push(uncertain)
        show()
        rule.onNodeWithText("Studio Mac is offline. Sends when it’s back.").assertExists()
        val arrival = Item("echo", 1, role = "user", kind = "text", text = uncertain.text, clientMsgId = uncertain.id)
        rule.runOnIdle { view.value = ChatSnapshot(loading = false, items = listOf(arrival)) }
        rule.waitUntil(10_000) { rule.onAllNodesWithText(uncertain.text).fetchSemanticsNodes().size == 1 &&
            rule.onAllNodesWithTag("held-${uncertain.id}").fetchSemanticsNodes().isEmpty() }
        rule.onNodeWithText(uncertain.error!!).assertDoesNotExist()
        assertTrue(sends.isEmpty())
        assertTrue(attempts.isEmpty())
    }

    @Test fun deniedOrUnrecognizedRowsStayOnScreenUntilTheManagerChangesThem() {
        push(expired)
        show()
        rule.runOnIdle { answer = CompletableDeferred<Unit>().apply { completeExceptionally(IllegalStateException("unavailable")) } }
        action(expired, "Retry").performClick()
        action(expired, "Retry").assertIsEnabled()
        rule.onNodeWithText(expired.error!!).assertIsDisplayed()
        assertEquals(listOf("retry" to expired.id), attempts)
        rule.runOnIdle { push(expired.copy(state = "new-state", error = null, images = 1, text = "")) }
        rule.onNodeWithText("1 image").assertExists()
        rule.onNodeWithText("Delivery wasn’t confirmed").assertExists()
        action(expired, "Retry").assertDoesNotExist()
        action(expired, "Delete").assertDoesNotExist()
        assertTrue(sends.isEmpty())
    }

    @Test fun uncertainRetryAndDeletionFollowOnlyTheManagersUpdates() {
        push(uncertain)
        show()
        rule.runOnIdle { answer = CompletableDeferred() }
        action(uncertain, "Retry").performClick()
        action(uncertain, "Retry").assertIsNotEnabled()
        rule.onNodeWithText(uncertain.error!!).assertExists()
        rule.runOnIdle {
            answer!!.complete(Unit)
            val checking = uncertain.copy(state = "waiting", error = "Will check the chat when Studio Mac is back")
            push(checking)
        }
        rule.onNodeWithText("Will check the chat when Studio Mac is back").assertIsDisplayed()
        action(uncertain, "Retry").assertDoesNotExist()
        rule.runOnIdle { push(uncertain.copy(error = "Couldn't confirm it arrived. Retry to send it anyway, or delete it.")) }
        rule.onNodeWithText("Couldn't confirm it arrived. Retry to send it anyway, or delete it.").assertExists()
        assertEquals(listOf("retry" to uncertain.id), attempts)
        action(uncertain, "Retry").performClick()
        rule.runOnIdle { push(uncertain.copy(deleteOnly = true, error = "Not sent: the device that sent it was signed out")) }
        action(uncertain, "Retry").assertDoesNotExist()
        action(uncertain, "Delete").performClick()
        rule.onNode(hasText("Delete") and hasAnyAncestor(isDialog())).performClick()
        assertEquals(listOf("retry" to uncertain.id, "retry" to uncertain.id, "cancel" to uncertain.id), attempts)
        rule.onNodeWithTag("held-${uncertain.id}").assertExists()
        rule.runOnIdle { push() }
        rule.onNodeWithTag("held-${uncertain.id}").assertDoesNotExist()
        assertTrue(sends.isEmpty())
    }

    @Test fun aCachedCheckOnlyRowCannotAuthorizeResendAfterReopeningChat() = cachedCheckOnlyRow(large = false)

    @Test @Config(fontScale = 2f)
    fun aCachedCheckOnlyRowKeepsDeleteAvailableAtMaximumFontScale() = cachedCheckOnlyRow(large = true)

    private fun cachedCheckOnlyRow(large: Boolean) {
        val effects = mutableListOf<String>()
        val stored = MutableStateFlow(emptySet<String>())
        val receipts = HeldAgentRetryReceipts(stored) { stored.update(it) }
        sessionActions = { session ->
            HttpHeldAgentActions(session, actions.features, receipts, pairingScope, { path, method, _ ->
                assertEquals("/api/v1/sessions/studio/release/held/held-uncertain/retry", path)
                assertEquals("POST", method)
                // The manager completed its check. Another POST now sends even if the first row is still on screen.
                effects += if (effects.isEmpty()) "checked" else "sent"
                Response.Builder().request(Request.Builder().url("https://example.com/").build())
                    .protocol(Protocol.HTTP_1_1).code(200).message("OK").body("{}".toResponseBody()).build()
            }, { _, _ -> })
        }
        fun publish(status: String, rev: Int) {
            list.value = listOf(JsonCodec.decodeFromString<Session>(
                """{"machine_id":"studio","session_id":"release","title":"Release notes","rev":$rev,
                    "undelivered":[{"id":"held-uncertain","text":"Review the screenshots.","images":2,
                    "at":"2026-06-06T12:00:00Z","state":"failed","error":"$status","computer":"Studio Mac"}]}"""))
        }
        publish(uncertain.error!!, 1)
        show()
        action(uncertain, "Retry").performClick()
        rule.runOnIdle { assertEquals(listOf("checked"), effects); chatOpen = false }
        rule.onNodeWithTag("held-${uncertain.id}").assertDoesNotExist()
        rule.runOnIdle { publish(uncertain.error!!, 2); chatOpen = true }
        val retry = action(uncertain, "Retry")
        if (SemanticsProperties.Disabled !in retry.fetchSemanticsNode().config) retry.performClick()
        rule.runOnIdle { assertEquals("The same check-only row must never authorize a send", listOf("checked"), effects) }
        retry.assertIsNotEnabled()
        action(uncertain, "Delete").assertIsEnabled()
        val out = File(System.getProperty("persona.shots") ?: "build/held-agent-shots").also { it.mkdirs() }
        for (style in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { appStyle = style; dark = night }
            rule.onNodeWithText(uncertain.error!!).assertIsDisplayed()
            retry.assertIsNotEnabled()
            action(uncertain, "Delete").assertIsEnabled().assertIsDisplayed()
            val name = "checked-${style.name.lowercase()}-${if (night) "dark" else "light"}${if (large) "-large" else ""}.png"
            File(out, name).outputStream().use {
                assertTrue(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
        val unconfirmed = "Couldn't confirm it arrived. Retry to send it anyway, or delete it."
        rule.runOnIdle { publish(unconfirmed, 3) }
        rule.onNodeWithText(unconfirmed).assertIsDisplayed()
        retry.assertIsEnabled().performClick()
        rule.runOnIdle { assertEquals(listOf("checked", "sent"), effects) }
    }

    @Test fun oldHubReadOnlySourceStoppedAgentAndHistoryKeepTheirOwnControls() {
        push(expired)
        actions.features.value = emptySet()
        show()
        rule.onNodeWithText("Studio Mac is offline.").assertExists()
        rule.onNodeWithText(expired.error!!).assertExists()
        action(expired, "Retry").assertDoesNotExist()
        action(expired, "Delete").assertDoesNotExist()
        rule.runOnIdle {
            actions.features.value = setOf(AgentMessageHoldFeature)
            list.value = list.value.map { it.copy(sourceKind = "codex_history", capabilities = SessionCapabilities(read = true)) }
        }
        rule.onNode(SemanticsMatcher.keyIsDefined(SemanticsProperties.EditableText), useUnmergedTree = true).assertIsNotEnabled()
        rule.onNodeWithText("Studio Mac is offline. Sends when it’s back.").assertDoesNotExist()
        action(expired, "Retry").assertDoesNotExist()
        rule.runOnIdle {
            sessions.machines.value = sessions.machines.value.map { it.copy(online = true) }
            list.value = list.value.map { it.copy(sourceKind = null, status = "offline", capabilities = SessionCapabilities(read = true, terminal = true)) }
        }
        rule.onNodeWithTag("chat-stopped").assertExists()
        action(expired, "Retry").assertDoesNotExist()
        rule.runOnIdle { history = true }
        action(expired, "Delete").assertDoesNotExist()
        assertTrue(attempts.isEmpty())
    }

    @Test fun taggedTranscriptWinsOverStaleRowsAndRetriedAttachmentsKeepTheirOperationId() {
        val text = "same text"
        val item = Item("remote-id", 1, role = "user", kind = "text", text = text, clientMsgId = "new-op")
        val held = listOf(waiting.copy(id = "new-op", text = text), waiting, waiting, waiting.copy(id = "assistant", computer = null))
        val outbox = listOf(OutboxMessage("old-bubble", key, text, DeliveryState.Sending, opId = "new-op"),
            OutboxMessage("confirmed-202", key, text, DeliveryState.Held), OutboxMessage("different", key, text, DeliveryState.Sending))
        val before = heldChatMessages(held, emptyList(), outbox)
        assertEquals(listOf("new-op", waiting.id), before.rows.map { it.id })
        assertEquals(listOf("different"), before.outbox.map { it.clientMessageId })
        val after = heldChatMessages(held, listOf(item), outbox)
        assertEquals(listOf(waiting.id), after.rows.map { it.id })
        assertEquals(listOf("different"), after.outbox.map { it.clientMessageId })
        val race = heldChatMessages(emptyList(), listOf(item), outbox)
        assertEquals(listOf("different"), race.outbox.map { it.clientMessageId })
    }
}
