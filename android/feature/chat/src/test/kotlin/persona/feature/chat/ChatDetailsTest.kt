package persona.feature.chat

import android.content.Context
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.chatlist.ChatAnchorStore
import persona.core.chatlist.ChatReadingKey
import persona.core.chatlist.ChatReadingPosition
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.UndeliveredMessage
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.MessageSendReceipt
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.PersonaTheme
import java.time.Instant

/**
 * The smaller details of an Agent's chat that follow iOS (docs/plans/android-ios-alignment/chats.md, C9): the composer
 * names the agent, "Claude is getting ready…" under a message whose turn has not begun, the newest recap open as the last
 * line until tapped or typing starts (Glass), and the plan pill's sheet (Glass).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h700dp")
class ChatDetailsTest {
    @get:Rule val rule = createComposeRule()
    private val context = ApplicationProvider.getApplicationContext<Context>()
    private val res = context.resources

    private val key = SessionKey("box", "s1")
    private fun session(agent: String? = null, status: String = "idle", starting: Boolean = false, startError: String? = null,
                        hint: String? = null, sourceKind: String? = null) =
        Session(machineId = key.machineId, sessionId = key.sessionId, title = "导入重试", agent = agent, status = status,
            starting = starting, startError = startError, agentLabelHint = hint, sourceKind = sourceKind)
    private fun outbox(state: DeliveryState) = OutboxMessage("m-$state", key, "再加一个重启的测试", state)

    @Test fun theComposerNamesTheAgent() {
        assertEquals("给 Claude 发消息", agentPlaceholder(res, session()))
        assertEquals("给 Claude 发消息", agentPlaceholder(res, null))
        assertEquals("给 Codex 发消息", agentPlaceholder(res, session(agent = "codex")))
        assertEquals("给 Codex 发消息", agentPlaceholder(res, session(sourceKind = "codex_app_server")))
        assertEquals("给 TRAEX 发消息", agentPlaceholder(res, session(agent = "traex")))
        // A runtime's agent by the hub's name for it, else its own.
        assertEquals("给 DSH 发消息", agentPlaceholder(res, session(agent = "dsh", hint = "DSH")))
        assertEquals("给 pi 发消息", agentPlaceholder(res, session(agent = "pi")))
        assertEquals("给 opencode 发消息", agentPlaceholder(res, session(agent = "opencode", hint = " ")))
    }

    @Test fun gettingReadyWhileYourMessageWaitsForItsTurn() {
        val sent = listOf(outbox(DeliveryState.Sent))
        // Sent, the hub still says idle: it may not have caught up.
        assertEquals("Claude 正在准备…", gettingReady(res, session(), sent, liveWork = false))
        assertEquals("Codex 正在准备…", gettingReady(res, session(agent = "codex"), listOf(outbox(DeliveryState.Sending)), liveWork = false))
        // At work or starting up, also while it only waits for the network.
        val waiting = listOf(outbox(DeliveryState.WaitingForNetwork))
        assertEquals("Claude 正在准备…", gettingReady(res, session(status = "working"), waiting, liveWork = false))
        assertEquals("Claude 正在准备…", gettingReady(res, session(starting = true), waiting, liveWork = false))
        // Waiting for the network while idle: not on its way yet.
        assertNull(gettingReady(res, session(), waiting, liveWork = false))
        // The turn's work line runs: it takes over.
        assertNull(gettingReady(res, session(status = "working"), sent, liveWork = true))
        // Nothing going in: no message, or only refused ones and ones whose delivery is unknown.
        assertNull(gettingReady(res, session(status = "working"), emptyList(), liveWork = false))
        for (state in listOf(DeliveryState.Refused, DeliveryState.Rejected, DeliveryState.Unknown, DeliveryState.NeedsConfirmation))
            assertNull("$state", gettingReady(res, session(status = "working"), listOf(outbox(state)), liveWork = false))
        // Offline, or the start failed: nothing is getting ready.
        assertNull(gettingReady(res, session(status = "offline"), sent, liveWork = false))
        assertNull(gettingReady(res, session(startError = "claude did not become ready"), sent, liveWork = false))
    }

    private val start = Instant.now().minusSeconds(3600)
    private fun at(seq: Int) = start.plusSeconds(seq * 60L).toString()
    private fun user(seq: Int, text: String) = Item("u$seq", seq, role = "user", kind = "text", text = text, ts = at(seq))
    private fun reply(seq: Int, text: String) = Item("a$seq", seq, role = "assistant", kind = "text", text = text, ts = at(seq))
    private fun recap(seq: Int, text: String) = Item("r$seq", seq, role = "system", kind = "recap", text = text, ts = at(seq))
    private fun lines(items: List<Item>) = ChatTimeline().lines(items, live = false, open = emptySet()).lines.asReversed()

    @Test fun theNewestRecapIsOpenUntilClosed() {
        val exchange = listOf(user(1, "修好导入重试"), reply(2, "修好了。"))
        assertEquals("r3", openRecapId(lines(exchange + recap(3, "这一轮：修了导入重试。")), closed = null))
        // A newer permission-mode line does not hide the recap it follows.
        val mode = persona.core.domain.lines.ChatLine.ModeChange("mode", persona.core.contracts.ModeChange(
            kind = "set", to = "acceptEdits", at = at(4)))
        assertEquals("r3", openRecapId(listOf(mode) + lines(exchange + recap(3, "这一轮：修了导入重试。")), closed = null))
        // Several in a row are one line: its newest is open.
        assertEquals("r4", openRecapId(lines(exchange + recap(3, "第一段") + recap(4, "第二段")), closed = null))
        // Closed, until a newer one comes.
        assertNull(openRecapId(lines(exchange + recap(3, "第一段") + recap(4, "第二段")), closed = "r4"))
        assertEquals("r4", openRecapId(lines(exchange + recap(3, "第一段") + recap(4, "第二段")), closed = "r3"))
        // Not the newest line any more.
        assertNull(openRecapId(lines(exchange + recap(3, "这一轮") + user(4, "继续")), closed = null))
        assertNull(openRecapId(lines(exchange), closed = null))
    }

    private fun resources(locale: java.util.Locale) =
        context.createConfigurationContext(android.content.res.Configuration(context.resources.configuration).apply { setLocale(locale) }).resources

    @Test fun theOpenRecapSaysOneFullStop() {
        // A recap that ends a sentence keeps its own mark; the template adds none.
        assertEquals("离开期间回顾：测试都过了。点按关闭", recapSpoken(res, "测试都过了。"))
        assertEquals("离开期间回顾：真的过了吗？点按关闭", recapSpoken(res, "真的过了吗？"))
        assertEquals("离开期间回顾：过了！点按关闭", recapSpoken(res, "过了！ "))
        assertEquals("离开期间回顾：测试都过了。点按关闭", recapSpoken(res, "测试都过了"))
        val ja = resources(java.util.Locale.JAPANESE)
        assertEquals("不在中のまとめ：テストは通りました。タップして閉じる", recapSpoken(ja, "テストは通りました。"))
        assertEquals("不在中のまとめ：テストは通りました．タップして閉じる", recapSpoken(ja, "テストは通りました．"))
        assertEquals("不在中のまとめ：テストは通りました。タップして閉じる", recapSpoken(ja, "テストは通りました"))
        val en = resources(java.util.Locale.ENGLISH)
        assertEquals("Recap while you were away: The tests pass. Tap to close", recapSpoken(en, "The tests pass."))
        assertEquals("Recap while you were away: Did they pass? Tap to close", recapSpoken(en, "Did they pass?"))
        assertEquals("Recap while you were away: The tests pass. Tap to close", recapSpoken(en, "The tests pass"))
        // Other closing marks still get the template's full stop.
        assertEquals("Recap while you were away: Done (mostly). Tap to close", recapSpoken(en, "Done (mostly)"))
    }

    @Test fun closedRecapsAreKeptOnThisDeviceAndPruned() {
        val store = ClosedRecaps(context)
        assertNull(store.closed(key))
        store.close(key, "r3", now = 1_000)
        assertEquals("r3", ClosedRecaps(context).closed(key))
        store.close(key, "r9", now = 2_000)
        assertEquals("r9", store.closed(key))
        // Past 300 chats the 200 closed most recently stay.
        val many = (1..301).associate { "box/s$it" to "$it r$it" }
        val kept = keptClosedRecaps(many)
        assertEquals(200, kept.size)
        assertTrue("box/s301" in kept && "box/s102" in kept && "box/s101" !in kept)
        assertEquals(many.minus("box/s1"), keptClosedRecaps(many.minus("box/s1")))
    }

    @Test fun thePlanSheetSaysHowFarAlong() {
        val plan = listOf(TodoEntry("读代码", "completed", null), TodoEntry("改重试", "in_progress", "正在改重试"),
            TodoEntry("跑测试", "pending", null))
        assertEquals("第 2 步，共 3 步", planStepText(res, plan))
        assertEquals("第 2 步，共 3 步 · 改重试", planSummary(res, plan))
        // Nothing in progress: the next step to do.
        assertEquals("第 3 步，共 3 步 · 跑测试", planSummary(res, plan.map { if (it.status == "in_progress") it.copy(status = "completed") else it }))
        val done = plan.map { it.copy(status = "completed") }
        assertEquals("3 步全部完成", planStepText(res, done))
        assertEquals("3 步全部完成", planSummary(res, done))
    }

    // The real chat page from here on.

    private fun texts(): List<String> = rule.onAllNodes(SemanticsMatcher("any node") { true }, useUnmergedTree = true).fetchSemanticsNodes().flatMap { node ->
        node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } +
            node.config.getOrNull(SemanticsProperties.ContentDescription).orEmpty()
    }

    /** Lines are built off the main thread: frames by hand, real time until [condition] holds. */
    private fun waitFor(what: String, condition: (List<String>) -> Boolean): List<String> {
        val deadline = System.currentTimeMillis() + 30_000
        while (true) {
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeByFrame()
            val shown = texts()
            if (condition(shown)) return shown
            check(System.currentTimeMillis() < deadline) { "$what never showed; shown: $shown" }
            Thread.sleep(20)
        }
    }

    private fun page(style: AppStyle, items: List<Item>, session: Session, outbox: List<OutboxMessage> = emptyList(),
                     composer: ChatComposerState = ChatComposerState(), expanded: Set<String> = emptySet(),
                     actions: ChatActions? = null, renderStyle: ChatRenderStyle = ChatRenderStyle.Agent,
                     features: Set<String> = emptySet(), sessionFlow: MutableStateFlow<List<Session>>? = null,
                     receipts: MutableStateFlow<List<MessageSendReceipt>>? = null) {
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Box", online = true)))
            override val sessions = sessionFlow ?: MutableStateFlow(listOf(session))
            override suspend fun refresh() = Unit
        }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = items, loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(outbox)
            override val sendReceipts = receipts ?: MutableStateFlow(emptyList())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        val anchors = object : ChatAnchorStore {
            override fun read(key: ChatReadingKey) = ChatReadingPosition(expanded = expanded)
            override fun write(key: ChatReadingKey, position: ChatReadingPosition) = Unit
        }
        rule.mainClock.autoAdvance = false // the spinner and the typing dots never settle
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides features) {
                PersonaTheme(reduceMotion = true, style = style) {
                    ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, composerState = composer,
                        chrome = ChatChromeBindings(actions = actions), renderStyle = renderStyle,
                        readingKey = ChatReadingKey("test", key.path), anchorStore = anchors)
                }
            }
        }
    }

    @Test fun thePageSaysWhoGetsReadyAndWhoYouWriteTo() {
        page(AppStyle.Brief, listOf(user(1, "修好导入重试"), reply(2, "修好了。")), session(agent = "codex"), listOf(outbox(DeliveryState.Sent)))
        val shown = waitFor("getting ready") { "Codex 正在准备…" in it }
        assertTrue(shown.toString(), "给 Codex 发消息" in shown)
    }

    @Config(qualifiers = "en-w400dp-h700dp")
    @Test fun assistantChatDrawsKeptMessagesWithTheirOwnActions() {
        val calls = mutableListOf<String>()
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun retryUndelivered(session: Session, messageId: String) {
                calls += "retry:${session.sessionId}:$messageId"
            }
            override suspend fun deleteUndelivered(session: Session, messageId: String) {
                calls += "delete:${session.sessionId}:$messageId"
            }
        }
        val waiting = UndeliveredMessage("kept-waiting", "Keep this waiting", images = 1,
            at = "2026-11-17T10:00:00Z", state = "waiting", attempts = 2,
            nextAt = "2026-11-17T10:01:00Z", error = "The assistant could not start")
        val failed = UndeliveredMessage("kept-failed", "Retry this one", at = "2026-11-17T10:02:00Z",
            state = "failed", error = "The computer is away")
        val row = session().copy(assistant = true, undelivered = listOf(waiting, failed))
        page(AppStyle.Glass, emptyList(), row, actions = actions, renderStyle = ChatRenderStyle.Assistant)

        val shown = waitFor("both kept messages") { texts ->
            "Keep this waiting" in texts && "Retry this one" in texts &&
                "Not delivered: The assistant could not start. It will be sent automatically." in texts &&
                "Not delivered: The computer is away" in texts
        }
        assertTrue(shown.toString(), shown.none { it == "Sends when connected" })

        val waitingMark = rule.onNodeWithContentDescription("Not delivered yet. It will be sent automatically.")
        waitingMark.performClick()
        waitFor("the waiting message's actions") { "Delete" in it }
        rule.onNodeWithText("Retry").assertDoesNotExist()
        rule.onNodeWithText("Delete").performClick()
        waitFor("the waiting message's Delete") { "delete:s1:kept-waiting" in calls }
        waitFor("the waiting row and menu to leave") { "Keep this waiting" !in it && "Delete" !in it }

        val failedMark = rule.onNodeWithContentDescription("Not delivered. Retry or delete.")
        failedMark.performClick()
        waitFor("the failed message's actions") { "Retry" in it && "Delete" in it }
        rule.onNodeWithText("Retry").performClick()
        waitFor("the failed message's Retry") { "retry:s1:kept-failed" in calls }
        waitFor("Retry to show the waiting row") { "Not delivered: The assistant could not start. It will be sent automatically." in it }
        failedMark.assertDoesNotExist()
        val retriedMark = rule.onNodeWithContentDescription("Not delivered yet. It will be sent automatically.")
        retriedMark.performClick()
        waitFor("the failed message's Delete action") { "Delete" in it }
        rule.onNodeWithText("Delete").performClick()
        waitFor("the failed message's Delete") { "delete:s1:kept-failed" in calls }
        waitFor("the deleted row to leave") { "Retry this one" !in it }
        assertEquals(listOf("delete:s1:kept-waiting", "retry:s1:kept-failed", "delete:s1:kept-failed"), calls)
    }

    @Config(qualifiers = "en-w400dp-h700dp")
    @Test fun assistantChatReloadsTheCompleteRowWhenKeptReceiptArrives() {
        var reads = 0
        val kept = UndeliveredMessage("kept-detail", "Loaded from the detail row",
            at = "2026-11-17T10:00:00Z", state = "waiting", error = "The assistant could not start")
        val summary = session().copy(assistant = true, waiting = false)
        var complete = summary.copy(waiting = null)
        val receipts = MutableStateFlow<List<MessageSendReceipt>>(emptyList())
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun completeRow(session: Session): Session {
                reads++
                assertEquals(summary.sessionId, session.sessionId)
                return complete
            }
        }
        page(AppStyle.Glass, emptyList(), summary, actions = actions,
            renderStyle = ChatRenderStyle.Assistant, features = setOf("sessions_lookup"), receipts = receipts)
        waitFor("the initial empty detail read") { reads == 1 }
        complete = complete.copy(undelivered = listOf(kept))
        // The summary and transcript rev stay unchanged; only the terminal kept receipt triggers another read.
        receipts.value = listOf(MessageSendReceipt(key, "kept-detail", "kept", Instant.now()))
        waitFor("the complete row's kept message") { "Loaded from the detail row" in it }
        assertEquals(2, reads)
    }

    @Config(qualifiers = "en-w400dp-h700dp")
    @Test fun authoritativeRowsReplaceOptimisticRetryIncludingANewFailure() {
        val failed = UndeliveredMessage("kept-failed", "Retry this one", at = "2026-11-17T10:02:00Z",
            state = "failed", error = "The computer is away")
        val row = session().copy(assistant = true, undelivered = listOf(failed))
        val rows = MutableStateFlow(listOf(row))
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun retryUndelivered(session: Session, messageId: String) = Unit
        }
        page(AppStyle.Glass, emptyList(), row, actions = actions,
            renderStyle = ChatRenderStyle.Assistant, sessionFlow = rows)
        val failedMark = rule.onNodeWithContentDescription("Not delivered. Retry or delete.")
        failedMark.performClick()
        waitFor("the failed message's actions") { "Retry" in it }
        rule.onNodeWithText("Retry").performClick()
        waitFor("the optimistic waiting row") {
            "Not delivered: The assistant could not start. It will be sent automatically." in it
        }

        // The hub's Retry changes state but keeps the old reason. That authoritative row replaces the synthetic default.
        rule.runOnIdle { rows.value = listOf(row.copy(undelivered = listOf(failed.copy(state = "waiting")))) }
        waitFor("the hub's waiting reason") {
            "Not delivered: The computer is away. It will be sent automatically." in it
        }

        // A later failure is newer still: it must never stay masked as optimistic waiting.
        rule.runOnIdle {
            rows.value = listOf(row.copy(undelivered = listOf(failed.copy(error = "It may already have gone in"))))
        }
        waitFor("the authoritative failure") { "Not delivered: It may already have gone in" in it }
        failedMark.assertExists()
    }

    @Config(qualifiers = "en-w400dp-h700dp")
    @Test fun retryAndDeleteReloadTheCompleteRowWithoutAReceiptOrRevChange() {
        val failed = UndeliveredMessage("kept-failed", "Retry this one", at = "2026-11-17T10:02:00Z",
            state = "failed", error = "The computer is away")
        val summary = session().copy(assistant = true, waiting = false)
        var complete = summary.copy(waiting = null, undelivered = listOf(failed))
        var reads = 0
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun completeRow(session: Session): Session {
                reads++
                return complete
            }
            override suspend fun retryUndelivered(session: Session, messageId: String) {
                complete = complete.copy(undelivered = listOf(failed.copy(state = "waiting")))
            }
            override suspend fun deleteUndelivered(session: Session, messageId: String) {
                complete = complete.copy(undelivered = null)
            }
        }
        page(AppStyle.Glass, emptyList(), summary, actions = actions,
            renderStyle = ChatRenderStyle.Assistant, features = setOf("sessions_lookup"))
        waitFor("the initial failed row") { "Retry this one" in it }
        val failedMark = rule.onNodeWithContentDescription("Not delivered. Retry or delete.")
        failedMark.performClick()
        waitFor("the failed message's actions") { "Retry" in it }
        rule.onNodeWithText("Retry").performClick()
        waitFor("Retry's authoritative waiting row") {
            "Not delivered: The computer is away. It will be sent automatically." in it
        }
        assertEquals(2, reads)

        val waitingMark = rule.onNodeWithContentDescription("Not delivered yet. It will be sent automatically.")
        waitingMark.performClick()
        waitFor("the waiting message's actions") { "Delete" in it }
        rule.onNodeWithText("Delete").performClick()
        waitFor("Delete's authoritative empty row") { "Retry this one" !in it }
        assertEquals(3, reads)
    }

    private val recapped = listOf(user(1, "修好导入重试"), reply(2, "修好了。"), recap(3, "**这一轮**：修了导入重试，测试都过了。"))
    private val openSpoken = "离开期间回顾：这一轮：修了导入重试，测试都过了。点按关闭"

    @Test fun glassOpensTheNewestRecapUntilTapped() {
        context.getSharedPreferences("closed_recaps", Context.MODE_PRIVATE).edit().clear().commit()
        page(AppStyle.Glass, recapped, session())
        waitFor("the open recap") { openSpoken in it }
        rule.onNodeWithContentDescription(openSpoken).performClick()
        val after = waitFor("the folded recap") { openSpoken !in it }
        assertTrue(after.toString(), after.any { it.startsWith("离开期间回顾 · ") })
        assertEquals("r3", ClosedRecaps(context).closed(key))
    }

    @Test fun typingClosesTheOpenRecap() {
        context.getSharedPreferences("closed_recaps", Context.MODE_PRIVATE).edit().clear().commit()
        val composer = ChatComposerState()
        page(AppStyle.Glass, recapped, session(), composer = composer)
        waitFor("the open recap") { openSpoken in it }
        composer.value = TextFieldValue("再", TextRange(1))
        waitFor("the folded recap") { openSpoken !in it }
        assertEquals("r3", ClosedRecaps(context).closed(key))
    }

    @Test fun briefAndBoardKeepTheRecapFolded() {
        context.getSharedPreferences("closed_recaps", Context.MODE_PRIVATE).edit().clear().commit()
        page(AppStyle.Board, recapped, session())
        val shown = waitFor("the folded recap") { texts -> texts.any { it.startsWith("离开期间回顾 · ") } }
        assertFalse(shown.toString(), openSpoken in shown)
    }

    private fun todo(content: String, status: String) = buildJsonObject { put("content", content); put("status", status) }
    /** A finished turn whose plan is followed by enough steps to push it out of view once its work line is open. */
    private val planned = buildList {
        add(user(1, "修好导入重试"))
        add(Item("t2", 2, role = "assistant", kind = "tool_use", toolUseId = "t2", toolName = "TodoWrite", ts = at(2),
            toolInput = buildJsonObject { put("todos", buildJsonArray {
                add(todo("读代码", "completed")); add(todo("改重试", "in_progress")); add(todo("跑测试", "pending"))
            }) }))
        for (seq in 3..30) {
            add(Item("t$seq", seq, role = "assistant", kind = "tool_use", toolUseId = "t$seq", toolName = "Bash", ts = at(seq),
                toolInput = buildJsonObject { put("command", "./gradlew test --tests Step$seq") }))
            add(Item("t$seq-r", seq, role = "user", kind = "tool_result", toolUseId = "t$seq", text = "ok", ts = at(seq)))
        }
        add(reply(31, "改好了。"))
    }

    @Test fun glassPlanPillOpensTheWholePlan() {
        page(AppStyle.Glass, planned, session(), expanded = setOf("w-u1"))
        waitFor("the plan pill") { "第 2 步，共 3 步" in it }
        rule.onNodeWithContentDescription("第 2 步，共 3 步").performClick()
        val sheet = waitFor("the plan sheet") { "计划" in it }
        assertTrue(sheet.toString(), "第 2 步，共 3 步 · 改重试" in sheet)
    }

    @Test fun briefPlanPillStillScrollsBackToTheCard() {
        page(AppStyle.Brief, planned, session(), expanded = setOf("w-u1"))
        waitFor("the plan pill") { "回到计划：第 2 步，共 3 步" in it }
        rule.onNodeWithContentDescription("回到计划：第 2 步，共 3 步").performClick()
        val after = waitFor("the plan card") { "回到计划：第 2 步，共 3 步" !in it }
        assertFalse(after.toString(), "第 2 步，共 3 步 · 改重试" in after)
    }
}
