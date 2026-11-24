package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.LarkFeatures
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalLarkFeatures
import persona.core.designsystem.PersonaTheme
import java.io.File
import persona.core.inbox.*

/**
 * "Comment on this passage" (#2964, iOS #1729): a long press on a paragraph of a document whose text cannot be selected opens the
 * comments with the box on that passage, and the comment carries its quote and block. Also the box on a passage in
 * every style, light and dark, as PNGs in `build/doc-comments-shots/` (or `-Dpersona.shots=<dir>`).
 */
@OptIn(ExperimentalTestApi::class)
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class LarkDocPassageCommentTest {
    @get:Rule val rule = createComposeRule()

    private val url = "https://example.feishu.cn/docx/D1"
    private val paragraph = "这一季要把灰度方案和压测报告都落地。"

    private inner class Docs : InboxAssistantTransport {
        val writes = mutableListOf<Pair<String?, JsonObject>>()
        override val scope = "pairing"
        override suspend fun readDocument(url: String): JsonObject = JsonCodec.parseToJsonElement("""{"title":"Q4 项目排期",
            "authority":{"copy":false},"content":{"type":"doc","content":[
            {"type":"heading","attrs":{"level":1,"id":"blk0"},"content":[{"type":"text","text":"背景"}]},
            {"type":"paragraph","attrs":{"id":"blk1"},"content":[{"type":"text","text":"$paragraph"}]}
        ]}}""").jsonObject
        override suspend fun documentExtra(path: String, url: String): JsonObject = JsonObject(emptyMap())
        override suspend fun documentComments(url: String): JsonObject = buildJsonObject { put("threads", kotlinx.serialization.json.JsonArray(emptyList())) }
        override suspend fun writeDocumentComment(thread: String?, body: JsonObject): JsonObject { writes += thread to body; return buildJsonObject { put("ok", true) } }
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

    private class Feed : InboxRepository {
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
    }

    private fun reader(docs: Docs, features: LarkFeatures) {
        val cache = LarkDocumentCache(scope = CoroutineScope(Dispatchers.Default))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalLarkFeatures provides features) {
                    Box(Modifier.width(400.dp).height(900.dp)) {
                        LarkDocumentReader(url, LarkContentSource(docs), Feed(), showConnectionTitle = false, cache = cache)
                    }
                }
            }
        }
        rule.waitUntilAtLeastOneExists(hasText(paragraph), 5_000)
    }

    @Test fun aLongPressOnAParagraphCommentsOnIt() {
        val docs = Docs()
        reader(docs, LarkFeatures(chats = true, moments = true, docComments = true))
        rule.onNodeWithTag("docComments.passage.blk1").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText("评论这段").performClick()
        rule.waitUntilAtLeastOneExists(hasTestTag("docComments.box"), 5_000)
        // The box says where it goes: "Comment on this passage" over the passage.
        assertEquals(1, rule.onAllNodesWithText(paragraph, useUnmergedTree = true).fetchSemanticsNodes().size - 1)
        rule.onNodeWithTag("docComments.box").performTextInput("这里要写清楚负责人")
        rule.onNodeWithTag("docComments.send").performClick()
        rule.waitUntil(5_000) { docs.writes.isNotEmpty() }
        val (thread, body) = docs.writes.single()
        assertNull(thread)
        assertEquals(paragraph, body["quote"]!!.jsonPrimitive.content)
        assertEquals("blk1", body["block_id"]!!.jsonPrimitive.content)
        assertEquals("这里要写清楚负责人", body["content"]!!.jsonPrimitive.content)
    }

    @Test fun withoutTheExtensionsCommentsALongPressDoesNothing() {
        reader(Docs(), LarkFeatures(chats = true, moments = true))
        rule.onNodeWithTag("docComments.passage.blk1").assertDoesNotExist()
    }

    @Test fun drawsTheBoxOnAPassageInEveryStyle() {
        val model = LarkDocCommentsModel(url, LarkDocCommentsTest.FakeApi(), LarkDocCommentTarget.Passage(paragraph, "blk1"), unnamed = "飞书用户")
        kotlinx.coroutines.runBlocking { model.load() }
        model.draft = "这里要写清楚负责人"
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    val scope = rememberCoroutineScope()
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(
                        if (style == AppStyle.Brief) PersonaTheme.palette.background else PersonaTheme.palette.surfaceOpaque)) {
                        Column { LarkDocCommentsContent(model, true, false, {}, Modifier.weight(1f)) { scope.launch { it() } } }
                    }
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/doc-comments-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            rule.onNodeWithText("评论这段").assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out, "doc-comments-passage-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
