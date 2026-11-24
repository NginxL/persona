package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.LarkMomentsComment
import persona.core.contracts.LarkMomentsContent
import persona.core.contracts.LarkMomentsPost
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.time.Instant
import persona.core.inbox.*

/**
 * The conversation link card's native Feishu views (G184 G185 G187), on the JVM with Robolectric's native graphics, in
 * every style, light and dark: a document with who edited it and when, its counts, the AI summary, "Contents" and "Open in Lark";
 * a Moments post with its comments. Written as PNGs to `build/lark-link-shots/` (or `-Dpersona.shots=<dir>`).
 */
@OptIn(ExperimentalTestApi::class)
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class LarkLinkReaderShotsTest {
    @get:Rule val rule = createComposeRule()

    private val url = "https://example.feishu.cn/docx/D1"
    private val postUrl = "https://applink.feishu.cn/client/moments/detail?postId=p1"

    private class Docs : InboxAssistantTransport {
        override val scope = "pairing"
        private fun json(text: String) = JsonCodec.parseToJsonElement(text).jsonObject
        override suspend fun readDocument(url: String): JsonObject = json("""{"title":"Q4 项目排期","content":{"type":"doc","content":[
            {"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"背景"}]},
            {"type":"paragraph","content":[{"type":"text","text":"这一季要把灰度方案和压测报告都落地，下面按周列出每件事和跟进人。"}]},
            {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"风险"}]},
            {"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"压测环境要提前一周申请"}]}]},
              {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"灰度期间值班表还没排"}]}]}]}
        ]}}""")
        override suspend fun documentExtra(path: String, url: String): JsonObject = when (path) {
            "reader/info" -> json("""{"owner":{"name":"示例同事"},"views":128,"visitors":36,"likes":4,"comments":3}""")
            "header" -> json("""{"editedAt":"${Instant.now().minusSeconds(3600)}","editorName":"另一位同事"}""")
            else -> json("""{"state":"ready","summary":"这篇文档列出了第四季度的排期：灰度方案在第二周开始，压测报告在第三周交付。主要风险是压测环境申请周期长、灰度期间的值班还没排。"}""")
        }
        override suspend fun inbox() = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun assistant() = error("unused")
        override suspend fun createConversation(main: Boolean) = error("unused")
        override suspend fun pins() = error("unused")
        override suspend fun mutes() = error("unused")
        override suspend fun media(ref: String) = error("unused")
    }

    private class Feed(private val failing: Boolean = false) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot())
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
        override suspend fun momentsPost(postId: String, manager: String?): LarkMomentsPost =
            if (failing) error("连不上飞书") else LarkMomentsPost(postId, userName = "示例同事",
                content = LarkMomentsContent("周五下午的分享会改到三楼大会议室，欢迎大家来听压测踩过的坑。"),
                reactionCount = 12, commentCount = 2, createTime = 1_762_300_000_000.0)
        override suspend fun momentsComments(postId: String, manager: String?) = listOf(
            LarkMomentsComment("c1", userName = "另一位同事", content = LarkMomentsContent("会录屏吗？"), createTime = 1_762_301_000_000.0),
            LarkMomentsComment("c2", userName = "示例同事", content = LarkMomentsContent("会，结束后发群里。"), createTime = 1_762_302_000_000.0,
                replyUserName = "另一位同事"),
        )
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/lark-link-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun drawsTheDocumentReaderWithItsDetailsInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val source = LarkContentSource(Docs())
        val cache = LarkDocumentCache(scope = CoroutineScope(Dispatchers.Default))
        val loaded = mutableListOf<String>()
        val picks = LarkLinkPicks(listOf(LarkTextPick("作为上下文") { })) { title, _ -> loaded += title }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        LarkDocumentReader(url, source, Feed(), showConnectionTitle = false, picks = picks, cache = cache)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitUntilAtLeastOneExists(hasText("AI 摘要"), 5_000)
            rule.onNodeWithText("示例同事 · 另一位同事 更新于", substring = true).assertExists()
            rule.onNodeWithText("128 次阅读 · 36 人看过 · 4 个赞 · 3 条评论").assertExists()
            rule.onNodeWithText("目录").assertExists()
            capture("lark-doc-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        assertEquals("Q4 项目排期", loaded.last())
    }

    @Test fun drawsAMomentsPostInTheLinkCardInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val loaded = mutableListOf<Pair<String, String>>()
        val picks = LarkLinkPicks(emptyList()) { title, whole -> loaded += title to whole }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        LarkMomentsLinkContent(Feed(), "p1", postUrl, picks = picks) { Text("网页") }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitUntilAtLeastOneExists(hasText("会录屏吗？"), 5_000)
            rule.onNodeWithText("在飞书中打开").assertExists()
            capture("lark-moments-link-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        assertEquals("示例同事 的动态" to "周五下午的分享会改到三楼大会议室，欢迎大家来听压测踩过的坑。", loaded.last())
    }

    @Test fun aPostThatCannotBeReadOffersRetryAndTheWebPage() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    LarkMomentsLinkContent(Feed(failing = true), "p1", postUrl) { Text("网页内容") }
                }
            }
        }
        rule.waitUntilAtLeastOneExists(hasText("连不上飞书"), 5_000)
        capture("lark-moments-link-failed")
        rule.onNodeWithText("切换网页").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("网页内容").assertExists()
    }

    /** A pick runs the menu's own "Copy" into [PickingClipboard]: that one copy goes to the pick, every other to the system. */
    @Test fun aPickTakesTheOneCopyItAskedForAndLeavesTheSystemClipboardAlone() = kotlinx.coroutines.runBlocking {
        val written = mutableListOf<String>()
        val system = object : androidx.compose.ui.platform.Clipboard {
            override suspend fun getClipEntry(): androidx.compose.ui.platform.ClipEntry? = null
            override suspend fun setClipEntry(clipEntry: androidx.compose.ui.platform.ClipEntry?) {
                written += clipEntry?.clipData?.getItemAt(0)?.text.toString()
            }
            override val nativeClipboard get() = error("unused")
        }
        val clipboard = PickingClipboard(system)
        fun entry(text: String) = androidx.compose.ui.platform.ClipEntry(android.content.ClipData.newPlainText("", text))
        val picked = mutableListOf<String>()
        clipboard.next = { picked += it }
        clipboard.setClipEntry(entry("  选中的一段  "))
        clipboard.setClipEntry(entry("复制的"))
        assertEquals(listOf("选中的一段"), picked)
        assertEquals(listOf("复制的"), written)
    }
}
