package persona.core.domain.lines

import persona.core.domain.zhAny

import persona.core.contracts.Item
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** API error cards (iOS APIErrorInfo / ChatLines.foldAPIErrors): the same wording and folding rules. */
class ApiErrorsTest {
    @Test fun sealedCompactionCannotBeRetried() {
        val sealed = "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account and can't go to it now"
        for (text in listOf(sealed, "API Error: 503 upstream: $sealed", sealed.uppercase())) {
            val error = ApiErrorInfo.parse(text)!!
            assertEquals(false, error.transient)
            assertTrue(error.needsAction)
        }
        assertEquals(true, ApiErrorInfo.parse("API Error: 503 encrypted connection failed")?.transient)
        assertNull(ApiErrorInfo.parse("A reply explaining: $sealed"))
    }

    @Test fun readsClaudeCodesAndTheHubsErrors() {
        val a = ApiErrorInfo.parse("API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}} request_id: req_011CUxyzABCD1234")!!
        assertEquals(529, a.status); assertEquals("模型服务太忙，过载了", (a.reason).zhAny()); assertEquals(true, a.transient)
        assertEquals("req_011CUxyzABCD1234", (a.requestId).zhAny())
        val b = ApiErrorInfo.parse("API 错误：429 The Tokens Per Minute (TPM) limit was reached（正在重试）")!!
        assertEquals("请求太多，被限流了", (b.reason).zhAny()); assertEquals(true, b.transient)
        val c = ApiErrorInfo.parse("API Error: 401 authentication_error · Please run /login")!!
        assertEquals("登录过期了，要在电脑上重新 /login", (c.reason).zhAny()); assertEquals(false, c.transient)
        assertTrue(c.needsAction)
        assertEquals("请求被拒绝：参数或上下文有问题", (ApiErrorInfo.parse("API Error: 400 bad request")!!.reason).zhAny())
        assertEquals("模型服务暂时不可用", (ApiErrorInfo.parse("API Error: 502 Bad Gateway")!!.reason).zhAny())
        // Claude Code's auth failure leads with a sentence.
        val d = ApiErrorInfo.parse("Failed to authenticate. API Error: 401 Authentication credentials are required; sign in or provide a valid API key")!!
        assertEquals(401, d.status); assertEquals(false, d.transient)
        assertNull(ApiErrorInfo.parse("The API Error: field is documented here"))
        assertNull(ApiErrorInfo.parse("I looked at it. The log says API Error: 500 somewhere in the middle of a long answer"))
        assertNull(ApiErrorInfo.parse(null))
    }

    @Test fun readsTheGatewaysIds() {
        val e = ApiErrorInfo.parse("API Error: 502 upstream stream error: rpc error: code = 13 desc = Req 202610302115A1B2C3D4E5F6 " +
            "Session 1ebd0000:7e470000 streamingcall loadbalance failed: no available GPU pod [biz error]. try again in a moment.")!!
        assertEquals("推理服务暂时没有可用的 GPU", (e.reason).zhAny())
        assertEquals("202610302115A1B2C3D4E5F6", (e.requestId).zhAny())
        assertEquals("1ebd0000:7e470000", (e.session).zhAny())
        assertEquals(true, e.transient)
        assertEquals("模型请求失败", (ApiErrorInfo.parse("API Error: something odd")!!.reason).zhAny())
        assertNull(ApiErrorInfo.parse("API Error: something odd")!!.transient)
        assertEquals(true, ApiErrorInfo.parse("API Error: please try again later")!!.transient)
    }

    private var n = 0
    private fun item(role: String, text: String, kind: String = "text") =
        LineItem(Item(id = "i${n++}#0", seq = n, role = role, kind = kind, text = text))

    private fun lines(vararg items: LineItem) = chatLines(items.toList()).lines

    @Test fun theSameErrorAfterARetryFoldsWithACount() {
        val e = "API Error: 529 overloaded"
        // Items are made in transcript order: their seq is what orders the lines.
        val start = item("user", "做点事")
        val first = item("assistant", e)
        val out = lines(start, first, item("user", "继续"), item("assistant", e), item("user", "继续"), item("assistant", e))
        assertEquals(2, out.size)
        val card = out[1] as ChatLine.ApiError
        assertEquals(3, card.count)
        // The card keeps the first error's place in the list.
        assertEquals(first.id, card.id)
    }

    @Test fun somethingElseInBetweenKeepsThemApart() {
        val e = "API Error: 529 overloaded"
        val out = lines(item("assistant", e), item("user", "换个问题"), item("assistant", e))
        assertEquals(listOf(1, 1), out.filterIsInstance<ChatLine.ApiError>().map { it.count })
        // A different error after a retry is a new card.
        val other = lines(item("assistant", e), item("user", "继续"), item("assistant", "API Error: 401 /login"))
        assertEquals(2, other.filterIsInstance<ChatLine.ApiError>().size)
        assertTrue(other[1] is ChatLine.Row)
    }

    @Test fun anAnswerThatOnlyMentionsAnErrorStaysAnAnswer() {
        val out = lines(item("user", "日志里是什么"), item("assistant", "日志里写着 API Error: 500，是上游的问题。"))
        assertTrue(out.none { it is ChatLine.ApiError })
    }

    @Test fun recapsInARowAreOneLine() {
        val ask = item("user", "看一下 PR")
        val answer = item("assistant", "看完了")
        val r1 = item("system", "第一次回顾", kind = "recap")
        val out = lines(ask, answer, r1, item("system", "第二次回顾", kind = "recap"), item("system", "第三次回顾", kind = "recap"))
        val recaps = out.last() as ChatLine.Recaps
        assertEquals(3, recaps.items.size)
        assertEquals("rc-" + r1.id, (recaps.id).zhAny())
        val single = lines(item("user", "看一下"), item("system", "只有一次", kind = "recap"))
        assertTrue(single.last() is ChatLine.Row)
    }
}
