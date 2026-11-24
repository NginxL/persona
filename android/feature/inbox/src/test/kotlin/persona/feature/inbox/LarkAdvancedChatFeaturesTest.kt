package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.LarkReaction
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class LarkAdvancedChatFeaturesTest {
    /** The app's strings; Robolectric runs in Simplified Chinese (robolectric.properties). */
    private val res get() = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private fun json(value: String) = Json.parseToJsonElement(value).jsonObject

    private val fakeTransport = object : InboxAssistantTransport {
        override val scope: String = "test-scope"
        override suspend fun inbox(): JsonObject = JsonObject(emptyMap())
        override suspend fun setDone(itemId: String, done: Boolean): JsonObject = JsonObject(emptyMap())
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String): JsonObject = JsonObject(emptyMap())
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?): JsonObject = JsonObject(emptyMap())
        override suspend fun assistant(): JsonObject = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean): JsonObject = JsonObject(emptyMap())
        override suspend fun pins(): JsonObject = JsonObject(emptyMap())
        override suspend fun mutes(): JsonObject = JsonObject(emptyMap())
        override suspend fun media(ref: String): ByteArray = ByteArray(0)
    }

    // --- 1. Reactions ---

    @Test
    fun messageParsesReactionsWithCountAndMineFlag() {
        val data = json("""{
            "id": "msg_001",
            "sender_id": "u_tom",
            "sender_name": "Tom",
            "position": 100,
            "created_at": "2026-11-04T10:00:00Z",
            "parts": [{"kind": "text", "text": "早安"}],
            "reactions": [
                {"key": "THUMBSUP", "count": 4, "mine": true, "users": ["小周", "阿杰", "Mia", "我"]},
                {"key": "OK", "count": 1, "isMine": false, "userNames": ["老王"]}
            ]
        }""")

        val message = larkMessage(data, fakeTransport, "feed_1")
        assertEquals(2, message.reactions.size)

        val r1 = message.reactions[0]
        assertEquals("THUMBSUP", r1.key)
        assertEquals(4, r1.count)
        assertEquals(true, r1.mine)
        assertEquals(listOf("小周", "阿杰", "Mia", "我"), r1.users)

        val r2 = message.reactions[1]
        assertEquals("OK", r2.key)
        assertEquals(1, r2.count)
        assertEquals(false, r2.mine)
        assertEquals(listOf("老王"), r2.users)
    }

    @Test
    fun emojiHelperMapsSymbolsAndLabels() {
        assertEquals("👍", LarkEmojiHelper.symbol("THUMBSUP"))
        assertEquals("赞", LarkEmojiHelper.label(res, "THUMBSUP"))

        assertEquals("👌", LarkEmojiHelper.symbol("ok"))
        assertEquals("OK", LarkEmojiHelper.label(res, "ok"))

        assertEquals("❤️", LarkEmojiHelper.symbol("HEART"))
        assertEquals("爱心", LarkEmojiHelper.label(res, "HEART"))

        assertEquals("✅", LarkEmojiHelper.symbol("DONE"))
        assertEquals("+1", LarkEmojiHelper.symbol("JIAYI"))

        // Unknown fallback
        assertEquals("[CUSTOM_EMOJI]", LarkEmojiHelper.symbol("CUSTOM_EMOJI"))
        assertEquals("CUSTOM_EMOJI", LarkEmojiHelper.label(res, "CUSTOM_EMOJI"))
    }

    // --- 2. Rich Text Syntax Rendering ---

    @Test
    fun rawPartsBuildStructuredRichNodesWithHeadingsListsAndQuotes() {
        val parts = listOf(
            LarkRawPart("text", "规范标题\n", metadata = json("""{"blocks":[{"id":"h1","type":"heading-1"}]}""")),
            LarkRawPart("text", "条目一", metadata = json("""{"blocks":[{"id":"ol","type":"ordered-list","listType":0,"start":1},{"id":"li1","type":"list-item"}]}""")),
            LarkRawPart("text", "条目二", metadata = json("""{"blocks":[{"id":"ol","type":"ordered-list","listType":0,"start":1},{"id":"li2","type":"list-item"}]}""")),
            LarkRawPart("text", "引用说明", metadata = json("""{"blocks":[{"id":"q1","type":"quote"}]}""")),
            LarkRawPart("code", "val x = 42", metadata = json("""{"languageName":"kotlin"}"""))
        )

        val tree = LarkRichNode.tree(parts)
        assertEquals(4, tree.size)

        // Block 1: heading-1
        val headingBlock = tree[0] as LarkRichNode.Block
        assertEquals("heading-1", headingBlock.block.type)
        assertEquals("规范标题\n", (headingBlock.children[0] as LarkRichNode.Leaf).part.text)

        // Block 2: ordered-list
        val listBlock = tree[1] as LarkRichNode.Block
        assertEquals("ordered-list", listBlock.block.type)
        assertEquals("1.", LarkRichNode.marker(listBlock.block, 0))
        assertEquals("2.", LarkRichNode.marker(listBlock.block, 1))

        // Block 3: quote
        val quoteBlock = tree[2] as LarkRichNode.Block
        assertEquals("quote", quoteBlock.block.type)

        // Block 4: code
        val codeLeaf = tree[3] as LarkRichNode.Leaf
        assertEquals("code", codeLeaf.part.kind)
        assertEquals("val x = 42", codeLeaf.part.text)
    }

    @Test
    fun syntaxHighlightingTokenizesKeywordsStringsNumbersAndComments() {
        val code = "val count = 100 // total items\nfun execute() { return \"done\" }"
        val tokens = LarkSyntax.tokens(code)
        assertTrue(tokens.any { it.kind == LarkSyntax.Kind.KEYWORD && it.text == "val" })
        assertTrue(tokens.any { it.kind == LarkSyntax.Kind.NUMBER && it.text == "100" })
        assertTrue(tokens.any { it.kind == LarkSyntax.Kind.COMMENT && it.text.contains("// total items") })
        assertTrue(tokens.any { it.kind == LarkSyntax.Kind.STRING && it.text == "\"done\"" })
    }

    @Test
    fun markdownLinesParseHeadingsListsQuotesAndIndents() {
        val md = """
            # 一级标题
            ## 二级标题
            - 无序列表项
            1. 有序列表项
            > 引用段落
            普通文本
        """.trimIndent()

        val lines = LarkMarkdown.lines(md)
        assertEquals(6, lines.size)

        assertEquals(1, lines[0].heading)
        assertEquals("一级标题", lines[0].text)

        assertEquals(2, lines[1].heading)
        assertEquals("二级标题", lines[1].text)

        assertEquals("•", lines[2].marker)
        assertEquals("无序列表项", lines[2].text)

        assertEquals("1.", lines[3].marker)
        assertEquals("有序列表项", lines[3].text)

        assertTrue(lines[4].quote)
        assertEquals("引用段落", lines[4].text)

        assertEquals(0, lines[5].heading)
        assertNull(lines[5].marker)
        assertFalse(lines[5].quote)
        assertEquals("普通文本", lines[5].text)
    }

    // --- 3. Merge Forward, Audio & Media ---

    @Test
    fun messageParsesMergeForwardWithNestedMessages() {
        val data = json("""{
            "id": "msg_fwd",
            "sender_id": "u_forwarder",
            "sender_name": "转发人",
            "position": 200,
            "parts": [{
                "kind": "merge_forward",
                "metadata": {
                    "title": "项目评审记录",
                    "messages": [
                        {"id": "m1", "senderName": "张三", "textPreview": "第一点已完成", "createdAt": "2026-11-04T09:00:00Z"},
                        {"id": "m2", "senderName": "李四", "textPreview": "正在跟进第二点", "createdAt": "2026-11-04T09:05:00Z"}
                    ]
                }
            }]
        }""")

        val message = larkMessage(data, fakeTransport, "feed_1")
        val part = message.parts.single() as LarkPart.MergeForward
        assertEquals("项目评审记录", part.title)
        assertEquals(2, part.messages.size)
        assertEquals("张三", part.messages[0].senderName)
        assertEquals("第一点已完成", part.messages[0].text)
        assertEquals("李四", part.messages[1].senderName)
        assertEquals("正在跟进第二点", part.messages[1].text)
    }

    @Test
    fun messageParsesVoiceAndVideoMediaParts() {
        val voiceData = json("""{
            "id": "msg_voice",
            "sender_id": "u_voice",
            "sender_name": "发送人",
            "parts": [{
                "kind": "media",
                "url": "/api/v1/lark/media/voice_123",
                "metadata": {"mime": "audio/opus", "duration": 9000}
            }]
        }""")
        val voiceMsg = larkMessage(voiceData, fakeTransport, "feed_1")
        val voicePart = voiceMsg.parts.single() as LarkPart.Audio
        assertEquals("/api/v1/lark/media/voice_123", voicePart.ref)
        assertEquals(9000L, voicePart.durationMs)

        val videoData = json("""{
            "id": "msg_video",
            "sender_id": "u_video",
            "sender_name": "发送人",
            "parts": [{
                "kind": "media",
                "url": "/api/v1/lark/media/video_456",
                "metadata": {"mime": "video/mp4", "duration": 45000}
            }]
        }""")
        val videoMsg = larkMessage(videoData, fakeTransport, "feed_1")
        val videoPart = videoMsg.parts.single() as LarkPart.Video
        assertEquals("/api/v1/lark/media/video_456", videoPart.ref)
        assertEquals(45000L, videoPart.durationMs)
    }

    @Test
    fun messageParsesStickersWithDimensions() {
        val stickerData = json("""{
            "id": "msg_sticker",
            "sender_id": "u_sticker",
            "sender_name": "发送人",
            "parts": [{
                "kind": "emotion",
                "url": "/api/v1/lark/media/sticker_001",
                "text": "开心",
                "metadata": {"width": 240, "height": 240}
            }]
        }""")
        val stickerMsg = larkMessage(stickerData, fakeTransport, "feed_1")
        val stickerPart = stickerMsg.parts.single() as LarkPart.Sticker
        assertEquals("开心", stickerPart.description)
        assertEquals(240, stickerPart.width)
        assertEquals(240, stickerPart.height)
    }

    // --- 4. Card Button Callbacks ---

    @Test
    fun cardButtonParsesActionIdActionMethodAndUrl() {
        val cardData = json("""{
            "header": {"tag": "card_header", "children": [{"tag": "plain_text", "text": "审批卡片"}]},
            "body": {
                "tag": "body",
                "children": [
                    {"tag": "button", "actionId": "action_approve", "actionMethod": 2, "children": [{"tag": "plain_text", "text": "通过"}]},
                    {"tag": "button", "url": "https://example.com/details", "actionMethod": 1, "children": [{"tag": "plain_text", "text": "查看详情"}]}
                ]
            }
        }""")

        val card = parseLarkCard(cardData, "备用卡片")
        assertEquals(2, card.blocks.size)

        val btnApprove = card.blocks[0] as LarkCardBlock.Button
        assertEquals("通过", btnApprove.label)
        assertEquals("action_approve", btnApprove.actionId)
        assertEquals(2, btnApprove.actionMethod)
        assertNull(btnApprove.url)

        val btnLink = card.blocks[1] as LarkCardBlock.Button
        assertEquals("查看详情", btnLink.label)
        assertEquals("https://example.com/details", btnLink.url)
        assertEquals(1, btnLink.actionMethod)
    }

    // --- 5. Sender Profile Card ---

    @Test
    fun profileParserExtractsLeaderDepartmentEmployeeTypeAndStatus() {
        val profileJson = json("""{
            "displayName": "李四",
            "alias": "阿四",
            "enName": "Fourth Li",
            "groupNickname": "架构师阿四",
            "avatarUrl": "/api/v1/lark/media/avatar_lisi",
            "coverUrl": "/api/v1/lark/media/cover_lisi",
            "description": "专注移动端架构与系统性能",
            "userType": "user",
            "relation": {"external": false},
            "customStatus": {"title": "专注中"},
            "workStatus": {"status": 2},
            "fields": [
                {"key": "B-DEPARTMENT", "value": "技术平台部"},
                {"key": "B-LEADER", "value": "张三", "url": "lark://client/profile?uid=u_zhangsan"},
                {"key": "B-EMPLOYEETYPE", "value": "全职员工"},
                {"key": "email", "label": "邮箱", "value": "lisi@example.com"}
            ]
        }""")

        val profile = parseProfile(res, profileJson, "默认名字")
        assertEquals("阿四", profile.displayName)
        assertEquals("Fourth Li", profile.enName)
        assertEquals("架构师阿四", profile.groupNickname)
        assertEquals("/api/v1/lark/media/avatar_lisi", profile.avatarUrl)
        assertEquals("/api/v1/lark/media/cover_lisi", profile.coverUrl)
        assertEquals("专注移动端架构与系统性能", profile.description)
        assertEquals("user", profile.userType)
        assertFalse(profile.isExternal)
        assertEquals("会议中", profile.statusText) // workStatus 2 takes precedence

        assertEquals(4, profile.fields.size)

        val dept = profile.fields.first { it.key == "B-DEPARTMENT" }
        assertEquals("部门", dept.label)
        assertEquals("技术平台部", dept.value)

        val leader = profile.fields.first { it.key == "B-LEADER" }
        assertEquals("直属上级", leader.label)
        assertEquals("张三", leader.value)
        assertEquals("u_zhangsan", leader.leaderId)

        val empType = profile.fields.first { it.key == "B-EMPLOYEETYPE" }
        assertEquals("人员类型", empType.label)
        assertEquals("全职员工", empType.value)

        val email = profile.fields.first { it.key == "email" }
        assertEquals("邮箱", email.label)
        assertEquals("lisi@example.com", email.value)
    }
}
