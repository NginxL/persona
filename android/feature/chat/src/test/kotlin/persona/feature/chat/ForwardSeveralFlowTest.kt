package persona.feature.chat

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.compose.ui.text.input.TextFieldValue
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.R as DesignR
import persona.core.navigation.MessageJumps

/** The real chat at both ends: select, deliver one draft, send once, expand and jump back to one original (#5184). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h920dp-xhdpi")
class ForwardSeveralFlowTest {
    @get:Rule val rule = createComposeRule()
    private val context = ApplicationProvider.getApplicationContext<Context>()
    private val source = Session("laptop", "planning", title = "Park cleanup", cwd = "/planning", agent = "claude", status = "idle", state = "idle")
    private val target = Session("studio", "volunteers", title = "Volunteer briefing", cwd = "/volunteers", agent = "codex", status = "idle", state = "idle")
    private val rows = listOf("Meet at the park?", "The east gate opens at nine.", "Bring the map.", "The park office has gloves.").mapIndexed { i, body ->
        Item("m${i + 1}", i + 1, "2026-06-09T09:0${i}:00Z", if (i % 2 == 0) "user" else "assistant", "text", body,
            fullSize = if (i == 1) 60 else null)
    }
    private val full = "The east gate opens at nine. The office opens at ten."
    private val note = "Please share these details at the briefing."
    private val sent = mutableListOf<Pair<SessionKey, String>>()
    private val requests = mutableListOf<Pair<SessionKey, String>>()
    private var failNextFullText = false
    private val machines = listOf(Machine("laptop", "Field laptop", online = true), Machine("studio", "Studio", online = true))
    private val sourceChat = MutableStateFlow(ChatSnapshot(items = rows, loading = false))
    private val targetChat = MutableStateFlow(ChatSnapshot(loading = false))
    private val repository = object : ChatRepository {
        override fun observe(session: SessionKey) = if (session == source.key) sourceChat else targetChat
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
        override suspend fun fullText(session: SessionKey, itemId: String): String {
            requests += session to itemId
            if (failNextFullText) {
                failNextFullText = false
                throw java.io.IOException("fixture temporarily unavailable")
            }
            return full
        }
    }
    private val sessions = object : SessionsRepository {
        override val sessions = MutableStateFlow(listOf(source, target))
        override val machines = MutableStateFlow(this@ForwardSeveralFlowTest.machines)
        override suspend fun refresh() = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += session to text
            targetChat.value = ChatSnapshot(items = listOf(Item("delivered", 1, "2026-06-09T10:00:00Z", "user", "text", text,
                clientMsgId = clientMessageId)), loading = false)
        }
    }

    @After fun clear() {
        ForwardPlacements.take(target.key)
        MessageJumps.take(source.machineId, source.sessionId)
    }

    private fun run(style: AppStyle, dark: Boolean, scale: Float = 1f) {
        RuntimeEnvironment.setFontScale(scale)
        var page by mutableStateOf(source.key)
        var jump: String? = null
        val composer = ChatComposerState().apply { value = TextFieldValue(note) }
        val forwarding = ChatForwarding(sessions.sessions, sessions.machines, MutableStateFlow(AssistantSnapshot()), { true },
            { it.title }, { null }, { destination, _ ->
                if (destination == source.key) jump = MessageJumps.pending?.itemId
                page = destination
            }, {}, {})
        rule.setContent {
            CompositionLocalProvider(LocalChatForwarding provides forwarding) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.testTag("forward-page").fillMaxSize().background(PersonaTheme.palette.background)) {
                        key(page) {
                            ChatScreen(page, repository, sender, interactions, sessions, onBack = {},
                                composerState = composer.takeIf { page == target.key })
                        }
                    }
                }
            }
        }
        fun tag(name: String) = rule.onNodeWithTag(name, useUnmergedTree = true)
        fun pick(id: String) = tag("forward-pick-" + id)
        fun capture(stage: String) {
            rule.mainClock.advanceTimeBy(500)
            rule.waitForIdle()
            val image = if (stage == "entry" || stage == "sheet") rule.runOnIdle { forwardWindowBitmap() }
                else tag("forward-page").captureToImage().asAndroidBitmap()
            val out = File(System.getProperty("persona.shots") ?: "build/forward-several-shots").also { it.mkdirs() }
            val name = "$stage-${style.name.lowercase()}-${if (dark) "dark" else "light"}${if (scale > 1) "-large" else ""}"
            File(out, "$name.png").outputStream().use { assertTrue(image.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
        fun begin() {
            rule.waitUntil(10_000) { rule.onAllNodesWithText(rows.last().text!!, substring = true, useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty() }
            rule.onNodeWithText(rows.last().text!!, substring = true, useUnmergedTree = true).performTouchInput { longClick() }
            rule.onNodeWithText(context.getString(R.string.chat_menu_forward)).performClick()
            capture("entry")
            tag("forward-select-messages").performClick()
            pick("m4").assertIsOn()
        }
        begin()
        rule.onNodeWithText(context.getString(R.string.chat_dialog_cancel)).performClick()
        tag("forward-picked").assertDoesNotExist()
        begin()
        pick("m4").performClick()
        tag("forward-picked").assertIsNotEnabled()
        rows.reversed().forEach { pick(it.id).performClick() }
        rule.onNodeWithText(context.getString(R.string.chat_forward_selected, rows.size)).assertExists()
        val shortTargetHeight = with(rule.density) { pick("m4").fetchSemanticsNode().boundsInRoot.height.toDp() }
        capture("select")
        tag("forward-picked").performClick()
        tag("forward-group-bar").assertExists()
        capture("sheet")
        rule.onNode(hasScrollToNodeAction() and hasAnyAncestor(isDialog()), useUnmergedTree = true)
            .performScrollToNode(hasTestTag("forward-session-volunteers"))
        val retry = style == AppStyle.Glass && !dark && scale == 1f
        failNextFullText = retry
        tag("forward-session-volunteers").performClick()
        if (retry) {
            rule.waitUntil(10_000) { requests.size == 1 }
            rule.waitForIdle()
            assertEquals(source.key, page)
            assertTrue(ForwardPlacements.waiting(target.key).isNullOrEmpty())
            assertTrue(sent.isEmpty())
            tag("forward-session-volunteers").performClick()
        }
        rule.waitUntil(10_000) { page == target.key }
        assertEquals(List(if (retry) 2 else 1) { source.key to "m2" }, requests)
        tag("forward-group-bar").assertExists()
        rule.onAllNodesWithContentDescription(context.getString(R.string.chat_forward_remove)).assertCountEquals(1)
        assertTrue(sent.isEmpty())
        capture("composer")
        rule.onNodeWithContentDescription(context.getString(DesignR.string.designsystem_send)).performClick()
        rule.waitUntil(10_000) { sent.isNotEmpty() }
        val (destination, text) = sent.single()
        val (refs, rest) = ForwardMessage.splitAll(text)
        assertEquals(target.key, destination)
        assertEquals(note, rest)
        assertEquals(rows.map { it.id }, refs.map { it.msg })
        assertEquals(full, refs[1].text)
        assertEquals(rows.map { java.time.Instant.parse(it.ts) }, refs.map { java.time.OffsetDateTime.parse(it.extra["time"]).toInstant() })
        assertEquals(listOf("user", "agent", "user", "agent"), refs.map { it.extra["role"] })
        tag("forward-group").assertExists()
        rule.onNodeWithText(rows.last().text!!, useUnmergedTree = true).assertDoesNotExist()
        capture("received")
        rule.onNodeWithText(context.getString(R.string.chat_forward_group_more, 1)).performClick()
        rule.onNodeWithText(rows.last().text!!, useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { page == source.key }
        assertEquals(rows.last().id, jump)
        assertTrue("short message checkbox hit height: $shortTargetHeight", shortTargetHeight >= 48.dp)
    }

    @Test fun glassLight() = run(AppStyle.Glass, false)
    @Test fun glassDark() = run(AppStyle.Glass, true)
    @Test fun briefLight() = run(AppStyle.Brief, false)
    @Test fun briefDark() = run(AppStyle.Brief, true)
    @Test fun boardLight() = run(AppStyle.Board, false)
    @Test fun boardDark() = run(AppStyle.Board, true)
    @Test fun glassLightLargeText() = run(AppStyle.Glass, false, 2f)
    @Test fun briefLightLargeText() = run(AppStyle.Brief, false, 2f)
    @Test fun briefDarkLargeText() = run(AppStyle.Brief, true, 2f)
    @Test fun boardLightLargeText() = run(AppStyle.Board, false, 2f)
    @Test fun boardDarkLargeText() = run(AppStyle.Board, true, 2f)
    @Test fun glassDarkLargeText() = run(AppStyle.Glass, true, 2f)
}
