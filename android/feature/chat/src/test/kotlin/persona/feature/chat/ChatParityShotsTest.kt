package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.api.RewindRestore
import persona.core.designsystem.*
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.ToolCall
import persona.core.domain.lines.WorkItem
import java.io.File

/**
 * The chat page's parity batch with iOS / the web (#2967), in every style, light and dark: Persona's own tools in
 * words, ask_user opened with what was picked, "View sub-agent" under a Task step (Brief and Board; Glass draws the task card), a fenced block coloured by its
 * language, the "Step N/M" pill, a message dragged left to quote, the plan card's three approvals, and the message
 * menu's "Branch to new chat" / "Rewind to here". PNGs in `build/chat-parity-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2000dp-xhdpi")
class ChatParityShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/chat-parity-shots").also { it.mkdirs() }

    private fun use(id: String, name: String, input: kotlinx.serialization.json.JsonObject) =
        Item(id, kind = "tool_use", role = "assistant", toolName = name, toolInput = input, toolUseId = id).asLineItem()
    private fun result(id: String, text: String) = Item("$id-r", kind = "tool_result", role = "user", text = text, toolUseId = id).asLineItem()

    private val askUser = WorkItem.Tool(ToolCall(use("ask", "mcp__persona__ask_user", buildJsonObject {
        put("questions", buildJsonArray {
            add(buildJsonObject {
                put("question", "周末的会挪到哪天？")
                put("options", buildJsonArray { listOf("周五下午", "下周一上午", "取消").forEach { add(buildJsonObject { put("label", it) }) } })
            })
            add(buildJsonObject {
                put("question", "要通知谁？")
                put("multiSelect", true)
                put("options", buildJsonArray { listOf("产品", "设计", "测试").forEach { add(buildJsonObject { put("label", it) }) } })
            })
        })
    }), result("ask", "The user answered:\n- 周末的会挪到哪天？ → 下周一上午\n- 要通知谁？ → 产品, 测试"), emptyList()))

    private val steps = listOf(
        WorkItem.Tool(ToolCall(use("ar", "mcp__persona__agent_read", buildJsonObject { put("session", "box/s1") }), result("ar", "ok"), emptyList())),
        WorkItem.Tool(ToolCall(use("cr", "mcp__persona__computer_run", buildJsonObject {
            put("machine", "MacBook")
            put("steps", buildJsonArray {
                listOf("All Clear", "6", "Multiply", "7", "Equals").forEach { add(buildJsonObject { put("click", buildJsonObject { put("name", it) }) }) }
            })
        }), result("cr", "ok"), emptyList())),
        WorkItem.Tool(ToolCall(use("cv", "mcp__persona__conversation_read", buildJsonObject { put("id", "c0ffee12345") }), result("cv", "ok"), emptyList())),
        WorkItem.Tool(ToolCall(use("fx", "mcp__persona__lark_doc_fetch", buildJsonObject { put("url", "https://example.com/doc") }), result("fx", "ok"), emptyList())),
        WorkItem.Tool(ToolCall(use("task", "Task", buildJsonObject { put("description", "查一下崩溃日志"); put("prompt", "读最近的崩溃日志，找出原因"); put("subagent_type", "general-purpose") }),
            result("task", "done"), emptyList())),
        askUser,
    )
    private val reply = Item("r#0", kind = "text", role = "assistant",
        text = "改好了：\n\n```kotlin\n// 只在有语言时上色\nfun greet(name: String): String {\n    val n = 42\n    return \"hi, \$name\" + n\n}\n```")

    @Test fun parityScreens() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                val menu = remember { MessageMenuState().apply { quote = {} } }
                val actions = remember(menu) {
                    ChatRowActions(SessionKey("box", "s1"), { "" }, null, photos = null, pictures = { emptyList() },
                        openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})
                }
                CompositionLocalProvider(LocalChatRowActions provides actions,
                    LocalChatSubagents provides ChatSubagents(mapOf("task" to SessionKey("box", "sub-1")), {})) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        steps.forEachIndexed { i, step ->
                            ChatLineContent(ChatLine.Step("w", step, i == 0), SessionKey("box", "s1"), loader,
                                recordedDuration = null, onToggle = {}, onFold = {}, folding = emptySet(), foldErrors = emptyMap(), onFullText = {},
                                lastWorkStep = i == steps.lastIndex)
                        }
                        Box(Modifier.testTag("reply")) { MessageContent(reply, user = false) }
                        Box(Modifier.fillMaxWidth().height(64.dp)) {
                            PlanPill(2, 3, 1, onClick = {}, modifier = Modifier.align(Alignment.Center))
                        }
                        val machine = Machine("box", "Box", online = true)
                        val plan = Interaction("plan", machineId = "box", sessionId = "s1", kind = "plan", plan = "1. 读代码\n2. 写测试\n3. 重构")
                        var draft by remember { mutableStateOf(persona.core.domain.answer.AnswerDraft()) }
                        InteractionForm(plan, machine, draft, { draft = it }, busy = false, uncertain = false, full = false,
                            onAnswer = { _, _, _ -> }, onTerminal = null, onExpand = {})
                    }
                }
            }
        }
        rule.waitForIdle()
        // Persona's tools in words; the fallback names the tool, never its raw id.
        rule.onNodeWithText("读 Agent 对话", substring = true, useUnmergedTree = true).assertExists()
        rule.onNodeWithText("MacBook · 点 AC → 6 → × → 7 → =", substring = true, useUnmergedTree = true).assertExists()
        rule.onNodeWithText("调用 Persona · lark doc fetch", substring = true, useUnmergedTree = true).assertExists()
        // Glass draws the Task as its task card, which is never a conversation of its own: no "View sub-agent" (#5045).
        rule.onNodeWithText("查看子 Agent", substring = true, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("批准，每步确认", substring = true, useUnmergedTree = true).assertExists()
        // ask_user opened: the questions, what was picked.
        rule.onNodeWithText("周末的会挪到哪天？", substring = true, useUnmergedTree = true).performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("ask-user-detail", useUnmergedTree = true).assertExists()
        // …and not the result again in the model's words (iOS hides an answered ask_user's result).
        rule.onNodeWithText("The user answered", substring = true, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("Step 2/3", substring = true, useUnmergedTree = true).assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            if (s != AppStyle.Glass) rule.onNodeWithText("查看子 Agent", substring = true, useUnmergedTree = true).assertExists()
            save(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "parity-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun aMessageDraggedLeftShowsTheReplyIcon() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var quoted = ""
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val menu = remember { MessageMenuState().apply { quote = { quoted = it.text } } }
                val actions = remember(menu) {
                    ChatRowActions(SessionKey("box", "s1"), { "" }, null, photos = null, pictures = { emptyList() },
                        openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})
                }
                CompositionLocalProvider(LocalChatRowActions provides actions) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        Box(Modifier.testTag("msg")) {
                            MessageContent(Item("r#1", kind = "text", role = "assistant", text = "往左拖这条消息就能引用它。"), user = false)
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithTag("msg").performTouchInput { down(center); moveBy(androidx.compose.ui.geometry.Offset(-60f, 0f)); moveBy(androidx.compose.ui.geometry.Offset(-200f, 0f)) }
            rule.waitForIdle()
            save(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "swipe-${s.name.lowercase()}-${if (d) "dark" else "light"}")
            rule.onNodeWithTag("msg").performTouchInput { up() }
            rule.waitForIdle()
        }
        assertEquals("往左拖这条消息就能引用它。", quoted)
    }

    /** A screen reader focuses the message as one node, and "Quote" is an action on it that quotes the message. */
    @Test fun aScreenReaderQuotesWithTheMessagesAction() {
        var quoted = ""
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val menu = remember { MessageMenuState().apply { quote = { quoted = it.text } } }
                val actions = remember(menu) {
                    ChatRowActions(SessionKey("box", "s1"), { "" }, null, photos = null, pictures = { emptyList() },
                        openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})
                }
                CompositionLocalProvider(LocalChatRowActions provides actions) {
                    MessageContent(Item("r#2", kind = "text", role = "assistant", text = "听这条消息，再引用它。"), user = false)
                }
            }
        }
        rule.waitForIdle()
        val hasQuote = androidx.compose.ui.test.SemanticsMatcher("has 引用") { node ->
            node.config.getOrNull(androidx.compose.ui.semantics.SemanticsActions.CustomActions)?.any { it.label == "引用" } == true
        }
        // In the merged tree (what TalkBack walks) the node with the action is the one that speaks the message.
        val node = rule.onNode(hasQuote)
        node.assert(androidx.compose.ui.test.hasText("听这条消息，再引用它。", substring = true))
        val action = node.fetchSemanticsNode().config[androidx.compose.ui.semantics.SemanticsActions.CustomActions].single { it.label == "引用" }
        rule.runOnIdle { action.action() }
        assertEquals("听这条消息，再引用它。", quoted)
    }

    /** The menu's extras: "Branch to new chat" on a reply, "Rewind to here" on yours (with the reason while it cannot), then its two choices. */
    @Test fun theMessageMenuOffersForkAndRewind() {
        val menu = MessageMenuState()
        var forked = ""
        var rewound: Pair<String, RewindRestore>? = null
        var block by mutableStateOf<String?>(null)
        var code by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                MessageMenuSheet(menu, writes = true, onQuote = {}, onEdit = {}, onContext = {}, onForward = null, onNotice = {},
                    onFork = { forked = it }, rewind = RewindMenu(block, code) { target, restore -> rewound = target.id to restore })
            }
        }
        menu.open(MessageTarget("abc#2", "回复", user = false))
        rule.waitForIdle()
        assertTrue(sheet().contains("分支到新聊天"))
        rule.onNodeWithText("分支到新聊天").performClick()
        rule.waitForIdle()
        assertEquals("abc#2", forked)
        // A line without a transcript uuid cannot be branched off.
        menu.open(MessageTarget("line12", "回复", user = false))
        rule.waitForIdle()
        assertTrue("分支到新聊天" !in sheet())
        menu.close()
        block = "工作中"
        menu.open(MessageTarget("u1", "跑一下测试", user = true))
        rule.waitForIdle()
        assertTrue(sheet().contains("回退到这里（工作中）"))
        menu.close()
        block = null
        menu.open(MessageTarget("u1", "跑一下测试", user = true))
        rule.waitForIdle()
        rule.onNodeWithText("回退到这里").performClick()
        rule.waitForIdle()
        assertTrue(sheet().containsAll(listOf("只回退对话", "回退代码和对话")))
        rule.onNodeWithText("只回退对话").performClick()
        rule.waitForIdle()
        assertEquals("u1" to RewindRestore.Conversation, rewound)
        // Where the node puts no files back (a runtime's session, a Codex thread; #6294): the conversation only.
        code = false
        menu.open(MessageTarget("u2", "再跑一次", user = true))
        rule.waitForIdle()
        rule.onNodeWithText("回退到这里").performClick()
        rule.waitForIdle()
        val choices = sheet()
        assertTrue(choices.toString(), "只回退对话" in choices && "回退代码和对话" !in choices)
        rule.onNodeWithText("只回退对话").performClick()
        rule.waitForIdle()
        assertEquals("u2" to RewindRestore.Conversation, rewound)
    }

    private fun sheet(): List<String> {
        repeat(5) { rule.mainClock.advanceTimeBy(200); rule.waitForIdle() }
        return rule.onAllNodes(hasAnyAncestor(isDialog()), useUnmergedTree = true).fetchSemanticsNodes().flatMap { node ->
            node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text }
        }
    }

    private fun save(bitmap: Bitmap, name: String) {
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
