package persona.feature.chat

import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.compose.ui.test.swipeUp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
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
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.time.Instant

/**
 * An Agent chat's folding header (#4339 C4, iOS `ChatChrome`): the rules, then the page. Glass folds it on the list's
 * scrolling and after 3 s untouched (not with animations off); the slim bar, a new request and the title's tap as iOS.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class ChatHeaderFoldTest {
    @get:Rule val rule = createComposeRule()

    @Test fun onlyGlassFoldsAnAgentsChatHeader() {
        assertTrue(HeaderFoldRules.folds(AppStyle.Glass, topBar = true, ChatRenderStyle.Agent))
        assertFalse(HeaderFoldRules.folds(AppStyle.Brief, topBar = true, ChatRenderStyle.Agent))
        assertFalse(HeaderFoldRules.folds(AppStyle.Board, topBar = true, ChatRenderStyle.Agent))
        assertFalse(HeaderFoldRules.folds(AppStyle.Glass, topBar = true, ChatRenderStyle.Assistant))
        assertFalse(HeaderFoldRules.folds(AppStyle.Glass, topBar = false, ChatRenderStyle.Agent))
    }

    @Test fun theHeaderFollowsTheListAsFarAsItMoves() {
        // The list is upside down: moving up the screen (negative) goes toward the newest and folds.
        assertEquals(0.25f, HeaderFoldRules.follow(0f, -25f, 100), 0.001f)
        assertEquals(1f, HeaderFoldRules.follow(0.5f, -300f, 100), 0.001f)
        assertEquals(0.3f, HeaderFoldRules.follow(0.5f, 20f, 100), 0.001f) // back into the history opens it
        assertEquals(0f, HeaderFoldRules.follow(0.1f, 50f, 100), 0.001f)
        assertEquals(0.4f, HeaderFoldRules.follow(0.4f, -50f, 0), 0.001f) // not measured yet
    }

    @Test fun aLiftedFingerNeverLeavesItHalfWay() {
        assertEquals(1f, HeaderFoldRules.settle(0.2f, atNewest = true), 0f)
        assertEquals(1f, HeaderFoldRules.settle(0f, atNewest = true), 0f)
        assertEquals(1f, HeaderFoldRules.settle(0.6f, atNewest = false), 0f)
        assertEquals(0f, HeaderFoldRules.settle(0.4f, atNewest = false), 0f)
        assertEquals(0f, HeaderFoldRules.settle(0f, atNewest = false), 0f)
        assertEquals(1f, HeaderFoldRules.settle(1f, atNewest = false), 0f)
    }

    @Test fun leftAloneThreeSecondsItFoldsUnlessAnimationsAreOff() {
        assertTrue(HeaderFoldRules.idleFolds(0f, 3_000, dragging = false, busy = false, reduceMotion = false))
        assertFalse(HeaderFoldRules.idleFolds(0f, 2_500, dragging = false, busy = false, reduceMotion = false))
        assertFalse(HeaderFoldRules.idleFolds(0f, 3_000, dragging = true, busy = false, reduceMotion = false))
        assertFalse(HeaderFoldRules.idleFolds(0f, 3_000, dragging = false, busy = true, reduceMotion = false)) // a sheet is up
        assertFalse(HeaderFoldRules.idleFolds(0f, 9_000, dragging = false, busy = false, reduceMotion = true))
        assertFalse(HeaderFoldRules.idleFolds(1f, 9_000, dragging = false, busy = false, reduceMotion = false)) // already folded
    }

    @Test fun aRequestItHadNotSeenOpensIt() {
        assertTrue(HeaderFoldRules.reopens(setOf("a"), setOf("a", "b")))
        assertFalse(HeaderFoldRules.reopens(setOf("a", "b"), setOf("a")))
        assertFalse(HeaderFoldRules.reopens(setOf("a"), setOf("a")))
    }

    @Test fun theTitleOpensSessionInfoOnGlass() {
        assertEquals(ChatTitleTap.SessionInfo, ChatTitleTap.of(AppStyle.Glass, online = true, hasSession = true))
        assertEquals(ChatTitleTap.Connection, ChatTitleTap.of(AppStyle.Glass, online = false, hasSession = true))
        assertEquals(ChatTitleTap.Status, ChatTitleTap.of(AppStyle.Glass, online = true, hasSession = false))
        assertEquals(ChatTitleTap.Status, ChatTitleTap.of(AppStyle.Brief, online = true, hasSession = true))
        assertEquals(ChatTitleTap.Status, ChatTitleTap.of(AppStyle.Board, online = false, hasSession = true))
    }

    private val key = SessionKey("macbook", "weather")
    private val start = Instant.now().minusSeconds(3_600)
    private val items = (1..30).map { n ->
        Item("i$n", n, role = if (n % 2 == 1) "user" else "assistant", kind = "text",
            text = "Message $n: the widget shows today's forecast, the hourly chart and the rain warning.",
            ts = start.plusSeconds(n * 20L).toString())
    }
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("macbook", "MacBook", online = true)))
        override val sessions = MutableStateFlow(listOf(Session(machineId = key.machineId, sessionId = key.sessionId,
            title = "Weather widget", status = "idle", state = "idle", agent = "claude", paneId = "p1",
            cwd = "/Users/me/weather", spaceName = "weather", slotName = "widget")))
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
    private val actions = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun info(session: Session) = SessionInfo(sessionId = session.sessionId, model = "claude-opus-4", cwd = session.cwd)
    }

    private val renamer = object : persona.core.domain.sort.SessionRenamer {
        override fun project(source: kotlinx.coroutines.flow.Flow<List<Session>>) = source
        override suspend fun rename(session: Session, title: String?, tabName: String?) = true
    }

    private fun page(style: AppStyle = AppStyle.Glass, reduceMotion: Boolean = true) = rule.setContent {
        PersonaTheme(darkTheme = false, reduceMotion = reduceMotion, style = style) {
            ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, chrome = ChatChromeBindings(actions = actions, renamer = renamer))
        }
    }.also {
        // The lines are built off the main thread: the list scrolls once they are there.
        if (rule.mainClock.autoAdvance) rule.waitUntil(20_000) {
            rule.onAllNodes(hasText("Message 30", substring = true)).fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** The chat reads the system's setting itself (its own PersonaTheme): animations off, as Android turns them off. */
    private fun animationsOff() {
        val context = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>()
        android.provider.Settings.Global.putFloat(context.contentResolver, android.provider.Settings.Global.ANIMATOR_DURATION_SCALE, 0f)
    }

    private val slim = "Weather widget, show title and tags"
    private fun folded(): Boolean = rule.onAllNodes(androidx.compose.ui.test.hasContentDescription(slim)).fetchSemanticsNodes().isNotEmpty()

    /** Back into the history, then toward the newest again: the list moves both ways. */
    private fun scrollTowardTheNewest() {
        val list = rule.onNode(hasScrollToIndexAction() and androidx.compose.ui.test.SemanticsMatcher.keyIsDefined(androidx.compose.ui.semantics.SemanticsProperties.VerticalScrollAxisRange))
        list.performTouchInput { swipeDown() }
        rule.waitForIdle()
        list.performTouchInput { swipeUp() }
        rule.waitForIdle()
    }

    @Test fun scrollingTowardTheNewestFoldsItAndTheSlimBarOpensIt() {
        animationsOff() // no idle fold: only the scrolling moves it
        page()
        rule.waitForIdle()
        assertFalse(folded())
        scrollTowardTheNewest()
        assertTrue("the header folded to its slim bar", folded())
        rule.onNodeWithContentDescription(slim).performClick()
        rule.waitForIdle()
        assertFalse("a tap on the capsule opens it", folded())
    }

    @Test fun leftAloneItFoldsAfterThreeSeconds() {
        rule.mainClock.autoAdvance = false
        page(reduceMotion = false)
        rule.mainClock.advanceTimeBy(1_000)
        assertFalse(folded())
        rule.mainClock.advanceTimeBy(3_000)
        assertTrue("folded by itself", folded())
    }

    @Test fun withAnimationsOffItNeverFoldsByItself() {
        animationsOff()
        rule.mainClock.autoAdvance = false
        page(reduceMotion = true)
        rule.mainClock.advanceTimeBy(10_000)
        assertFalse(folded())
    }

    @Config(application = QuestionTestApplication::class) // the request's card reads the app's editor services
    @Test fun aRequestArrivingOpensItAgain() {
        animationsOff()
        page()
        rule.waitForIdle()
        scrollTowardTheNewest()
        assertTrue(folded())
        interactions.pending.value = listOf(Interaction("ask", machineId = key.machineId, sessionId = key.sessionId,
            kind = "permission", toolName = "Bash", summary = "Run the tests"))
        rule.waitForIdle()
        assertFalse("the header says it needs you again", folded())
    }

    @Test fun briefNeverFolds() {
        page(style = AppStyle.Brief)
        rule.waitForIdle()
        scrollTowardTheNewest()
        rule.mainClock.advanceTimeBy(5_000)
        assertFalse(folded())
    }

    @Test fun boardNeverFolds() {
        page(style = AppStyle.Board)
        rule.waitForIdle()
        scrollTowardTheNewest()
        rule.mainClock.advanceTimeBy(5_000)
        assertFalse(folded())
    }

    @Test fun glassTitleOpensSessionInfo() {
        page()
        rule.waitForIdle()
        rule.onNode(hasClickAction() and hasText("Weather widget", substring = true)).performClick()
        rule.waitForIdle()
        rule.onNode(isDialog() and androidx.compose.ui.test.hasAnyDescendant(hasText("Session overview"))).assertExists()
    }

    @Test fun briefTitleKeepsTheStatusSheet() {
        page(style = AppStyle.Brief)
        rule.waitForIdle()
        rule.onNode(hasClickAction() and hasText("Weather widget", substring = true)).performClick()
        rule.waitForIdle()
        rule.onNode(isDialog() and androidx.compose.ui.test.hasAnyDescendant(hasText("Session status"))).assertExists()
    }
}
