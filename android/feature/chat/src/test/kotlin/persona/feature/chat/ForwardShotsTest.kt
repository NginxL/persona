package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import android.graphics.Canvas
import android.view.View
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.Density
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSnapshot
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File
import java.time.Instant

/**
 * Forward (#3901) as it draws, in every style, light and dark: the message menu with Forward after Quote, Forward to with
 * Persona, New agent…, Recent and each computer's Agents, the target's composer with the forward and its route line, and the
 * sent bubble with the forward's card (and the same message while it waits to send). Mock data only. PNGs in
 * `build/forward-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h860dp-xhdpi")
@OptIn(ExperimentalMaterial3Api::class)
class ForwardShotsTest {
    @get:Rule val rule = createComposeRule()

    private val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val now = Instant.now()
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()

    private val blog = Session(machineId = "macbook", sessionId = "blog", title = "博客迁移到 Astro", cwd = "/Users/me/code/blog",
        status = "idle", state = "unread", agent = "claude", spaceName = "blog", updatedAt = ago(2))
    private val photos = Session(machineId = "studio", sessionId = "photos", title = "照片同步支持 HEIC", cwd = "/Users/me/code/photos",
        status = "working", state = "working", agent = "codex", spaceName = "photos", updatedAt = ago(5))
    private val agents = listOf(blog, photos,
        Session(machineId = "macbook", sessionId = "notes", title = "整理发布说明", cwd = "/Users/me/code/site", status = "blocked",
            state = "needs_you", agent = "claude", spaceName = "site", updatedAt = ago(9)),
        Session(machineId = "macbook", sessionId = "weekly", title = "写周报", cwd = "/Users/me/docs", status = "idle", state = "idle",
            agent = "claude", updatedAt = ago(40)),
        Session(machineId = "studio", sessionId = "login", title = "修复登录超时", cwd = "/Users/me/code/app", status = "idle", state = "idle",
            agent = "claude", spaceName = "app", updatedAt = ago(30)),
    )
    private val main = SessionKey("macbook", "main")
    private val trip = Session(machineId = "macbook", sessionId = "trip", title = "周末出行安排", status = "idle", state = "idle",
        assistant = true, updatedAt = ago(60))
    private val machines = listOf(Machine("macbook", "MacBook", online = true), Machine("studio", "Mac Studio", online = true))
    private val reply = Item("r#4", role = "assistant", kind = "text", text = "PR #42 看完了：逻辑没问题，留了两条关于命名的小建议。", ts = ago(3))

    private class Memory(var list: List<String>) : ForwardMemory {
        override suspend fun targets() = list
        override suspend fun remember(target: String) { list = ForwardMemory.next(list, target) }
    }

    private val memory = Memory(listOf("session:studio/photos", "persona", "session:macbook/trip"))
    private val opened = mutableListOf<SessionKey>()
    private val forwarding = ChatForwarding(
        agents = MutableStateFlow(agents), machines = MutableStateFlow(machines),
        assistant = MutableStateFlow(AssistantSnapshot(main, listOf(trip))), personaGroupOf = { true },
        conversationTitle = { it.title }, memory = { memory },
        openChat = { key, _ -> opened += key }, openPersonaHome = {}, openNewAgent = {})

    /** The reply above as a forward, as Forward to makes it: from Claude on the MacBook. */
    private val draft = ForwardDraft(ForwardMessage.Source(blog.key, reply.id, ForwardMessage.Role.Agent, "Claude", blog.title,
        "blog", "MacBook", Instant.parse(reply.ts)), reply.text.orEmpty())
    private val forward = ForwardMessage.ref(draft.raw, draft.source, res)
    private val routed = forward.copy(route = ForwardMessage.route("MacBook", ForwardMessage.RouteTarget("Codex", "Mac Studio"), res))
    private val note = "按这两条建议把命名也改一下"
    private val sent = Item("u9", role = "user", kind = "text", text = forward.xml() + "\n\n" + note, ts = ago(1))

    private enum class Scene { Menu, Sheet, Composer, Bubble }

    @After fun clear() { ForwardPlacements.home = emptyList() }

    private fun shots(style: AppStyle, dark: Boolean, large: Boolean = false) {
        var scene by mutableStateOf(Scene.Menu)
        val menu = MessageMenuState()
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                fontScale = if (large) 2f else 1f
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            val density = LocalDensity.current
            val scaledDensity = Density(density.density, if (large) 2f else density.fontScale)
            // A tapped row keeps a frozen, half-drawn ripple in native shots: none here (tests only).
            CompositionLocalProvider(LocalConfiguration provides configuration, LocalDensity provides scaledDensity,
                LocalRippleConfiguration provides null) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val actions = remember(menu) {
                        ChatRowActions(blog.key, { "" }, null, photos = null, pictures = { emptyList() }, openImages = { _, _ -> },
                            menu = menu, retryLineId = null, onRetry = {})
                    }
                    CompositionLocalProvider(LocalChatForwarding provides forwarding, LocalChatRowActions provides actions) {
                        Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                            when (scene) {
                                Scene.Menu, Scene.Sheet -> {
                                    Column(Modifier.fillMaxWidth().padding(Tokens.Space.l)) {
                                        MergedUserBubble(listOf(Item("u1", role = "user", kind = "text", text = "帮我看一下 PR #42", ts = ago(4))))
                                        MessageContent(reply, user = false)
                                    }
                                    if (scene == Scene.Menu) MessageMenuSheet(menu, writes = true, onQuote = {}, onEdit = {}, onContext = {},
                                        onForward = {}, onNotice = {})
                                    else ForwardSheet(draft, forwarding, complete = { forward }, onDismiss = {}, onPicked = { _, _ -> })
                                }
                                Scene.Composer -> Column(Modifier.testTag("shot").fillMaxSize()) {
                                    Column(Modifier.fillMaxWidth().padding(Tokens.Space.l)) {
                                        MessageContent(Item("p#1", role = "assistant", kind = "text", text = "HEIC 的缩略图已经能生成了，正在补测试。"), user = false)
                                    }
                                    Spacer(Modifier.weight(1f))
                                    val state = remember { ChatComposerState().apply { value = TextFieldValue(note, TextRange(note.length)) } }
                                    ChatComposer(photos.key, state, working = false, sending = false, canClear = false, service = null,
                                        onSend = {}, onCommand = {}, onNotice = {}, extraContent = true,
                                        accessory = { ChatContextCard(routed, lines = 2) {} })
                                }
                                Scene.Bubble -> Column(Modifier.testTag("shot").fillMaxSize().padding(Tokens.Space.l)) {
                                    MergedUserBubble(listOf(sent))
                                    MessageContent(Item("p#2", role = "assistant", kind = "text", text = "好的，按建议把 `photoSync` 改成 `syncPhotos`。"), user = false)
                                    // The same message while it waits to send: the card, not the block.
                                    OutboxBubble(forward.xml() + "\n\n" + "这条也看一下", "c1")
                                }
                            }
                        }
                    }
                }
            }
        }
        val name = "${style.name.lowercase()}-${if (dark) "dark" else "light"}${if (large) "-large" else ""}"
        menu.open(MessageTarget(reply.id, reply.text.orEmpty(), user = false))
        settle()
        val words = sheetTexts()
        assertTrue(words.toString(), words.containsAll(listOf("复制", "引用", "转发", "分享")))
        assertFalse(words.toString(), words.any { it.contains("交给 Persona") })
        assertEquals("Forward comes right after Quote", words.indexOf("引用") + 1, words.indexOf("转发"))
        capture("menu-$name")
        menu.close()
        scene = Scene.Sheet
        settle()
        listOf("转发到", "主对话", "开 Agent…", "最近", "照片同步支持 HEIC", "Mac Studio", "整理发布说明", "转发自 Claude · 博客迁移到 Astro")
            .forEach { assertTrue(it, rule.onAllNodesWithText(it, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()) }
        // Never the chat it comes from; a Persona conversation in Recent says so under its title.
        assertFalse(sheetTexts().contains("博客迁移到 Astro"))
        assertTrue(sheetTexts().contains("周末出行安排"))
        capture("sheet-$name")
        scene = Scene.Composer
        settle()
        rule.onNodeWithTag(FORWARD_ROUTE_TAG, useUnmergedTree = true).assertExistsWithText("从 MacBook 转发 · 发到 Mac Studio 上的 Codex")
        capture("composer-$name")
        scene = Scene.Bubble
        settle()
        assertEquals(2, rule.onAllNodesWithText("转发自 Claude · 博客迁移到 Astro", useUnmergedTree = true).fetchSemanticsNodes().size)
        rule.onNodeWithText(note, useUnmergedTree = true).assertExists()
        capture("bubble-$name")
    }

    private fun androidx.compose.ui.test.SemanticsNodeInteraction.assertExistsWithText(text: String) {
        val shown = fetchSemanticsNode().config.getOrNull(SemanticsProperties.Text).orEmpty().joinToString { it.text }
        assertEquals(text, shown)
    }

    private fun settle() {
        repeat(6) { rule.mainClock.advanceTimeBy(200); rule.waitForIdle() }
    }

    private fun sheetTexts(): List<String> = rule.onAllNodes(hasAnyAncestor(isDialog()), useUnmergedTree = true).fetchSemanticsNodes()
        .flatMap { node -> node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } }

    /** Every window, sheets and their scrim included (captureToImage can crop the Activity for a dialog). */
    @Suppress("UNCHECKED_CAST")
    private fun capture(name: String) {
        rule.waitForIdle()
        // Replies parse their Markdown off the main thread (ChatMarkdownCache) and show plain text until then: give it real
        // time, as ReplyBlocksShotsTest does, or `code` is captured with its backticks.
        Thread.sleep(200)
        rule.waitForIdle()
        val bitmap = rule.runOnIdle {
            val global = Class.forName("android.view.WindowManagerGlobal")
            val instance = global.getMethod("getInstance").invoke(null)
            val views = global.getDeclaredField("mViews").apply { isAccessible = true }.get(instance) as List<View>
            Bitmap.createBitmap(views.first().width, views.first().height, Bitmap.Config.ARGB_8888).also { image ->
                val canvas = Canvas(image)
                views.forEach { view ->
                    val position = IntArray(2).also { view.getLocationOnScreen(it) }
                    canvas.save()
                    canvas.translate(position[0].toFloat(), position[1].toFloat())
                    view.draw(canvas)
                    canvas.restore()
                }
            }
        }
        val directory = File(System.getProperty("persona.shots") ?: "build/forward-shots").also { it.mkdirs() }
        File(directory, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    @Test fun glassLight() = shots(AppStyle.Glass, false)
    @Test fun glassDark() = shots(AppStyle.Glass, true)
    @Test fun briefLight() = shots(AppStyle.Brief, false)
    @Test fun briefDark() = shots(AppStyle.Brief, true)
    @Test fun boardLight() = shots(AppStyle.Board, false)
    @Test fun boardDark() = shots(AppStyle.Board, true)
    @Test fun glassLightLarge() = shots(AppStyle.Glass, false, large = true)
    @Test fun glassDarkLarge() = shots(AppStyle.Glass, true, large = true)
    @Test fun briefLightLarge() = shots(AppStyle.Brief, false, large = true)
    @Test fun briefDarkLarge() = shots(AppStyle.Brief, true, large = true)
    @Test fun boardLightLarge() = shots(AppStyle.Board, false, large = true)
    @Test fun boardDarkLarge() = shots(AppStyle.Board, true, large = true)

    /** Persona is offered for a chat of the hub group Persona's page shows; a linked group's chat cannot reach it (same hub only). */
    @Test fun personaIsOfferedOnItsOwnHubOnly() {
        var sameHub by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val shown = remember(sameHub) { ChatForwarding(forwarding.agents, forwarding.machines, forwarding.assistant, { sameHub },
                    forwarding.conversationTitle, forwarding.memory, forwarding.openChat, forwarding.openPersonaHome, forwarding.openNewAgent) }
                androidx.compose.runtime.key(sameHub) {
                    ForwardSheet(draft, shown, complete = { forward }, onDismiss = {}, onPicked = { _, _ -> })
                }
            }
        }
        settle()
        assertEquals(1, rule.onAllNodes(androidx.compose.ui.test.hasTestTag("forward-persona"), useUnmergedTree = true).fetchSemanticsNodes().size)
        sameHub = false
        settle()
        assertEquals(0, rule.onAllNodes(androidx.compose.ui.test.hasTestTag("forward-persona"), useUnmergedTree = true).fetchSemanticsNodes().size)
        assertEquals(1, rule.onAllNodes(androidx.compose.ui.test.hasTestTag("forward-new-agent"), useUnmergedTree = true).fetchSemanticsNodes().size)
    }

    /** A tap on a target reads the forward, remembers the target and hands both on; the chat it came from is never offered. */
    @Test fun pickingATargetRemembersItAndHandsItOn() {
        var picked: Pair<ForwardTarget, ChatLinkContext>? = null
        var gone = false
        rule.setContent {
            CompositionLocalProvider(LocalRippleConfiguration provides null) {
                PersonaTheme(reduceMotion = true) {
                    if (!gone) ForwardSheet(draft, forwarding, complete = { forward }, onDismiss = { gone = true }, onPicked = { t, r -> picked = t to r })
                }
            }
        }
        settle()
        rule.onNodeWithTag("forward-session-login", useUnmergedTree = true).performClick()
        settle()
        assertEquals(ForwardTarget.Chat(SessionKey("studio", "login")) to forward, picked)
        assertTrue(gone)
        assertEquals("session:studio/login", memory.list.first())
        assertTrue(rule.onAllNodesWithText("博客迁移到 Astro", useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
    }
}
