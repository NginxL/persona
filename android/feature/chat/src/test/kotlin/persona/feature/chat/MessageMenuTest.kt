package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.api.DeliveryState
import persona.core.designsystem.ErrorTag
import persona.core.designsystem.ErrorTagTone
import persona.core.domain.lines.ApiErrorInfo
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.chatLines
import persona.feature.chat.MessageAction.*
import kotlinx.coroutines.ExperimentalCoroutinesApi
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant
import java.time.ZoneOffset

/** The long-press menu (R4) with Forward (#3901), the API error card's rules (D6), recaps and Claude's suggestion (X1). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
@OptIn(ExperimentalCoroutinesApi::class)
class MessageMenuTest {
    private val res = ApplicationProvider.getApplicationContext<Context>().resources
    @Test fun theMenuOffersWhatTheConversationAllows() {
        val mine = MessageTarget("m", "你好", user = true)
        // Forward right after Quote (#3901); it took Hand to Persona's place.
        assertEquals(listOf(Copy, Select, Quote, Forward, Share, Context, Edit), messageActions(mine, writes = true, forward = true))
        assertEquals(listOf(Copy, Select, Quote, Share, Context, Edit), messageActions(mine, writes = true, forward = false))
        // Read-only (a sub-agent, history): nothing that changes the composer; Forward changes another chat's, so it stays.
        assertEquals(listOf(Copy, Select, Share), messageActions(mine, writes = false, forward = false))
        assertEquals(listOf(Copy, Select, Forward, Share), messageActions(mine, writes = false, forward = true))
        // An agent's reply cannot be edited.
        assertEquals(listOf(Copy, Select, Quote, Share, Context), messageActions(MessageTarget("a", "回复", user = false), true, false))
        // "Branch to new chat" on a reply that is a transcript line; "Rewind to here" on yours, only where the composer is the user's.
        val extras = MessageExtras(fork = true, rewind = true)
        assertEquals(listOf(Copy, Select, Quote, Share, Context, Fork), messageActions(MessageTarget("u#1", "回复", user = false), true, false, extras))
        assertEquals(listOf(Copy, Select, Quote, Share, Context), messageActions(MessageTarget("line3", "回复", user = false), true, false, extras))
        assertEquals(listOf(Copy, Select, Quote, Share, Context, Edit, Rewind), messageActions(mine, true, false, extras))
        assertEquals(listOf(Copy, Select, Share), messageActions(mine, writes = false, forward = false, extras))
        // Only on this phone (#4645): Copy, Select, Share, then Delete ("Cancel send" while it waits) where the page deletes;
        // nothing that treats it as a transcript line (Quote, Context, Edit, Rewind).
        val deletes = MessageExtras(fork = true, rewind = true, delete = true)
        for (state in listOf(DeliveryState.Queued, DeliveryState.WaitingForNetwork, DeliveryState.Refused, DeliveryState.Rejected)) {
            val pending = mine.copy(pending = state)
            assertEquals("$state", listOf(Copy, Select, Share, Delete), messageActions(pending, true, true, deletes))
            assertEquals("$state", listOf(Copy, Select, Share), messageActions(pending, true, true, extras))
            assertEquals("$state", listOf(Copy, Select, Share), messageActions(pending, writes = false, forward = true, extras = deletes))
        }
        // The hub may have it: no Delete.
        for (state in listOf(DeliveryState.Sending, DeliveryState.Sent, DeliveryState.Unknown, DeliveryState.Verifying, DeliveryState.NeedsConfirmation))
            assertEquals("$state", listOf(Copy, Select, Share), messageActions(mine.copy(pending = state), true, true, deletes))
        assertTrue(messageMenuWrites(readOnly = false, session = null))
        assertTrue(!messageMenuWrites(readOnly = true, session = null))
        // A Codex history session takes no messages: no quote, edit or context either.
        val history = Session(machineId = "m", sessionId = "h", sourceKind = "codex_history",
            capabilities = persona.core.contracts.SessionCapabilities(read = true))
        assertTrue(!messageMenuWrites(readOnly = false, session = history))
        assertTrue(messageMenuWrites(readOnly = false, session = Session(machineId = "m", sessionId = "s")))
    }

    @Test fun aQuoteGoesOverTheBoxAndLeavesTheDraft() {
        // "Quote" is a context over the composer now (iOS #986, ComposerParityTest): the words are not typed into the draft.
        val quotes = withQuote(emptyList(), ChatLinkContext.quote("第一行\n第二行", "我", "m"))
        assertEquals("<persona-context kind=\"quote\" title=\"我\" url=\"\" msg=\"m\">\n第一行\n第二行\n</persona-context>\n\nmine",
            linkMessage("mine", quotes, res))
    }

    @Test fun useAsContextTakesTheMessageTitledByItsChat() {
        val context = messageContext("修复登录", "原文", res)
        assertEquals(listOf("context", "修复登录 · 消息", "", "原文"), listOf(context.kind, context.title, context.url, context.text))
    }

    private var n = 0
    private fun item(role: String, text: String, kind: String = "text") =
        LineItem(Item(id = "i${n++}#0", seq = n, role = role, kind = kind, text = text))

    @Test fun onlyTheNewestCardOffersRetryWhileIdle() {
        val lines = chatLines(listOf(item("user", "帮我看看"), item("assistant", "API Error: 502 no available GPU"))).lines
        val card = lines.last() as ChatLine.ApiError
        assertEquals(card.id, retryLine(lines, working = false, canSend = true))
        assertNull(retryLine(lines, working = true, canSend = true))
        assertNull(retryLine(lines, working = false, canSend = false))
        val login = chatLines(listOf(item("user", "继续改"), item("assistant", "API Error: 401 · Please run /login"))).lines
        assertNull(retryLine(login, working = false, canSend = true))
        val answered = chatLines(listOf(item("assistant", "API Error: 502 Bad Gateway"), item("user", "换个问题"))).lines
        assertNull(retryLine(answered, working = false, canSend = true))
    }

    @Test fun errorTagsSayWhatKindOfError() {
        val gpu = ApiErrorInfo.parse("API Error: 502 no available GPU")!!
        assertEquals(listOf(ErrorTag("502", ErrorTagTone.Danger), ErrorTag("临时故障"), ErrorTag("连续 3 次")), apiErrorTags(gpu, 3, res))
        val login = ApiErrorInfo.parse("API Error: 401 /login")!!
        assertEquals(listOf(ErrorTag("401", ErrorTagTone.Danger), ErrorTag("要处理", ErrorTagTone.Caution)), apiErrorTags(login, 1, res))
        assertEquals(listOf<ErrorTag>(), apiErrorTags(ApiErrorInfo.parse("API Error: odd")!!, 1, res))
        assertEquals("11月2日 08:01:05", apiErrorTime("2026-11-02T08:01:05+08:00", res, ZoneOffset.ofHours(8)))
        assertNull(apiErrorTime(null, res))
    }

    @Test fun recapsAndTimesReadLikeTheOldApp() {
        val now = Instant.parse("2026-11-02T12:00:00Z")
        assertEquals("刚刚", relativeTime(now.minusSeconds(30), now, res, ZoneOffset.UTC))
        assertEquals("21 分钟前", relativeTime(now.minusSeconds(21 * 60), now, res, ZoneOffset.UTC))
        assertEquals("3 小时前", relativeTime(now.minusSeconds(3 * 3600), now, res, ZoneOffset.UTC))
        assertEquals("昨天 09:30", relativeTime(Instant.parse("2026-11-01T09:30:00Z"), now, res, ZoneOffset.UTC))
        assertEquals("10月3日", relativeTime(Instant.parse("2026-10-03T08:00:00Z"), now, res, ZoneOffset.UTC))
        assertEquals("离开期间回顾 · 21 分钟前", recapLabel(1, now.minusSeconds(21 * 60), now, res))
        assertEquals("离开期间回顾 3 条 · 刚刚", recapLabel(3, now, now, res))
        assertEquals("离开期间回顾", recapLabel(1, null, now, res))
    }

    @Test fun claudesSuggestionWaitsForAnIdleEmptyComposer() {
        val idle = Session(sessionId = "s", status = "idle", suggestion = "  把命名改完再跑一遍测试 ")
        assertEquals("把命名改完再跑一遍测试", suggestionFor(idle, typed = false))
        assertNull(suggestionFor(idle, typed = true))
        assertNull(suggestionFor(idle.copy(status = "working"), typed = false))
        assertNull(suggestionFor(idle.copy(suggestion = " "), typed = false))
        assertNull(suggestionFor(null, typed = false))

        val send: (String) -> Unit = {}
        assertSame("a safe suggestion keeps the caller's sender", send, suggestionDirectSend(idle, send))
        assertSame(send, suggestionDirectSend(idle.copy(suggestionRisky = false), send))
        assertNull("a risky suggestion never gets a direct sender", suggestionDirectSend(idle.copy(suggestionRisky = true), send))
        assertNull("no sender stays fill-only", suggestionDirectSend(idle, null))
        assertNull(suggestionDirectSend(null, send))
    }
}
