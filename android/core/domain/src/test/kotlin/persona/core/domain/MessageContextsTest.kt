package persona.core.domain

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class MessageContextsTest {
    @Test fun attributesInAnyOrderOrMissing() {
        val (contexts, rest) = parseContexts(
            "<persona-context url=\"https://example.com/a\" title=\"页面\" kind=\"edit\">\n选中的字\n</persona-context>\n\n改一下",
        )
        assertEquals(listOf(MessageContext("edit", "页面", "https://example.com/a", "选中的字")), contexts)
        assertEquals("改一下", (rest).zhAny())

        val bare = parseContexts("<persona-context title='只有标题'>正文</persona-context>看看").first.single()
        assertEquals("context", (bare.kind).zhAny())
        assertEquals("只有标题", (bare.title).zhAny())
        assertNull(bare.url)
        assertNull(parseContexts("<persona-context kind=\"context\" title=\"t\" url=\"\">x</persona-context>").first.single().url)
    }

    @Test fun escapedAttributesAndBodyAreDecoded() {
        val context = parseContexts(
            "<persona-context kind=\"context\" title=\"A &quot;b&quot; &lt;c&gt; &amp;amp;\" url=\"https://example.com/?a=1&amp;b=2\">" +
                "&lt;b&gt; &amp; c</persona-context>",
        ).first.single()
        assertEquals("A \"b\" <c> &amp;", (context.title).zhAny())
        assertEquals("https://example.com/?a=1&b=2", (context.url).zhAny())
        assertEquals("<b> & c", (context.body).zhAny())
    }

    @Test fun emptyBodyAndLabels() {
        val (contexts, rest) = parseContexts("<persona-context kind=\"context\" title=\"项目 · 发布\">\n</persona-context>")
        assertEquals("", (contexts.single().body).zhAny())
        assertEquals("", (rest).zhAny())
        assertEquals("引用", (MessageContext("context", "", null, "").label).zhAny())
        assertEquals("https://example.com", (MessageContext("context", " ", "https://example.com", "").label).zhAny())
    }

    @Test fun plainTextHasNoContexts() {
        assertEquals(emptyList<MessageContext>() to "就一句话", (parseContexts("  就一句话\n")).zhAny())
        // An unclosed tag (a message still streaming) is left as it is.
        assertTrue(parseContexts("<persona-context kind=\"context\">没写完").first.isEmpty())
    }

    /** Canonical context blocks retain their order, whether bare or attributed. */
    @Test fun personaContextsRetainTheirOrder() {
        val (contexts, rest) = parseContexts(
            "<persona-context kind=\"edit\" title=\"页面\" url=\"https://example.com/a\">\n选中的字\n</persona-context>\n" +
                "<persona-context kind=\"context\" title=\"旧的\">旧块</persona-context>\n\n改一下",
        )
        assertEquals(listOf(MessageContext("edit", "页面", "https://example.com/a", "选中的字"), MessageContext("context", "旧的", null, "旧块")), contexts)
        assertEquals("改一下", (rest).zhAny())
        val bare = parseContexts("<persona-context>快照</persona-context>看看")
        assertEquals("context", (bare.first.single().kind).zhAny())
        assertEquals("看看", (bare.second).zhAny())
        assertTrue(parseContexts("<persona-context kind=\"context\">没写完").first.isEmpty())
        assertTrue(hasContextTag("x <persona-context"))
        assertEquals(false, hasContextTag("x <context"))
    }

    @Test fun summaries() {
        val one = MessageContext("context", "文档 · 手册", null, "")
        assertEquals("", (contextSummary(emptyList())).zhAny())
        assertEquals("上下文 · 文档 · 手册", (contextSummary(listOf(one))).zhAny())
        assertEquals("要改的内容 · 文档 · 手册", (contextSummary(listOf(one.copy(kind = "edit")))).zhAny())
        assertEquals("上下文 · 2 项", (contextSummary(listOf(one, one.copy(kind = "edit")))).zhAny())
    }

    @Test fun feishuAgentRequest() {
        val text = """
            <persona-context kind="instructions" title="给 Agent 的说明">
            你在飞书里被 /agent 叫到。
            ## 怎么做
            先读上下文，再回答。
            </persona-context>
            <persona-context kind="context" title="请求的位置">
            位置：群聊「发布群」
            </persona-context>
            <persona-context kind="context" title="前面的聊天">
            10-31 09:00 张三：构建挂了
            10-31 09:01 李四：我看看
            …（已截断，共 20480 字节）
            </persona-context>
            <persona-context kind="doc" title="文档 · 手册" url="https://example.com/d">
            手册正文
            </persona-context>
            <persona-context kind="context" title="项目 · 发布">
            </persona-context>
            看下构建为什么挂了
        """.trimIndent()
        val (contexts, rest) = parseContexts(text)
        assertEquals("看下构建为什么挂了", (rest).zhAny())
        assertEquals(listOf("instructions", "context", "context", "doc", "context"), (contexts.map { it.kind }).zhAny())
        assertEquals(listOf("给 Agent 的说明", "请求的位置", "前面的聊天", "文档 · 手册", "项目 · 发布"), (contexts.map { it.title }).zhAny())
        assertEquals("位置：群聊「发布群」", (contexts[1].body).zhAny())
        assertTrue(contexts[2].body.endsWith("…（已截断，共 20480 字节）"))
        assertEquals("https://example.com/d", (contexts[3].url).zhAny())
        assertEquals("", (contexts[4].body).zhAny())
        assertEquals("上下文 · 5 项", (contextSummary(contexts)).zhAny())
        assertEquals("看下构建为什么挂了", (cleanText(text, "[图片]")).zhAny())
    }

    @Test fun theInterjectNoteIsNeverAnOwnerBubbleQuote() {
        val interject =
            """<persona-context kind="context" title="插话" url="">
            用户在你这一轮还没做完时发来下面这条消息。
            </persona-context>
            先停一下，帮我查个东西""".trimIndent()
        val (contexts, rest) = parseContexts(interject)
        // the block parses like any context, but the render predicate marks it as the hub's interject note
        assertEquals(1, contexts.size)
        assertTrue(contexts.single().isInterjectContext())
        assertEquals("先停一下，帮我查个东西", rest.zhAny())
        // a normal picked context beside it still renders
        val mixed = interject + "\n<persona-context kind=\"context\" title=\"需求文档\" url=\"https://x\">摘要</persona-context>"
        val renderable = parseContexts(mixed).first.filterNot(MessageContext::isInterjectContext)
        assertEquals(listOf("需求文档"), renderable.map { it.title }.zhAny())
        // only the exact title is dropped; a similarly named context stays a quote
        val similar = parseContexts("<persona-context kind=\"context\" title=\"插话记录\" url=\"\">x</persona-context>问").first
        assertEquals(1, similar.size)
        assertTrue(similar.none { it.isInterjectContext() })
    }
}
