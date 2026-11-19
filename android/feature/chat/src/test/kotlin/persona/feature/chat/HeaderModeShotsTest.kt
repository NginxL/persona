package persona.feature.chat

import android.graphics.Bitmap
import android.graphics.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.StatusLineState
import java.io.File

/**
 * The Agent chat's header with its permission mode (#3957, iOS #3719): a working Claude agent's header, status line and
 * composer button ("after", to set beside the same scene on main, "before"); the header in every mode, unknown and Codex;
 * the mode menu without and with Allow Everything on offer; going into Allow Everything, and leaving it with no way back.
 * The line above the composer no longer says the state: gone without facts, only the queued count with them ("after-facts").
 * Every style, light and dark. PNGs `chat-mode-*` in `build/i18n-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class HeaderModeShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val machine = Machine(id = "box", name = "workstation", online = true)
    private fun agent(mode: String?, bypass: Boolean = false, agent: String = "claude") = Session(machineId = "box", sessionId = "s-$mode-$agent",
        title = "Refactor the sync layer", agent = agent, status = "working", paneId = "p1", cwd = "/srv/app", permissionMode = mode,
        effort = "high", bypassAvailable = bypass)

    /** Takes every change; records what it was asked. */
    private class Actions : ChatActions {
        val asked = mutableListOf<ChatAction>()
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done.also { asked += action }
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
    }

    private val actions = Actions()
    private val working = StatusLineState(working = true)

    private fun show(style: AppStyle, dark: Boolean, height: Int, content: @androidx.compose.runtime.Composable (ChatChromeState) -> Unit) {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val scope = rememberCoroutineScope()
                val resources = LocalResources.current
                val chrome = remember { ChatChromeState(resources, actions, scope, {}, {}) }
                Box(Modifier.testTag("shot").width(400.dp).height(height.dp).background(PersonaTheme.palette.background)) { content(chrome) }
            }
        }
        rule.waitForIdle()
    }

    private fun save(name: String, style: AppStyle, dark: Boolean, bitmap: Bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()) {
        File(out(), "chat-mode-$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    /** The page with its popup drawn where it opened. */
    private fun withPopup(): Bitmap {
        val page = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val menu = rule.onNode(isPopup()).captureToImage().asAndroidBitmap()
        val p = rule.onNodeWithTag("shot").fetchSemanticsNode().positionOnScreen
        val m = rule.onNode(isPopup()).fetchSemanticsNode().positionOnScreen
        return page.copy(Bitmap.Config.ARGB_8888, true).also { Canvas(it).drawBitmap(menu, m.x - p.x, m.y - p.y, null) }
    }

    /** The header and, on Glass, the mode as the composer has it (Q11, #5055: Glass's header has no mode button). */
    @androidx.compose.runtime.Composable
    private fun Moded(s: Session, chrome: ChatChromeState) {
        ChatHeader(s, machine, listOf(s), chrome, null, false, onBack = {}, onOpenSession = {}, onConnectionDetails = {},
            onOverlay = {}, statusState = working)
        if (PersonaTheme.style == AppStyle.Glass && SessionModes.showsInComposer(s)) Row { HeaderModeButton(s, chrome, online = true) {} }
    }

    private fun has(text: String) = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
    private fun described(text: String) = rule.onAllNodesWithContentDescription(text, substring = true).fetchSemanticsNodes().isNotEmpty()

    private fun shot(scene: String, style: AppStyle, dark: Boolean) {
        when (scene) {
            // The page's top and bottom: the header says the state once; the line above the composer keeps its words
            "after", "after-bypass", "after-facts" -> {
                val s = agent(if (scene == "after-bypass") "bypassPermissions" else "acceptEdits")
                    .let { if (scene == "after-facts") it.copy(queued = listOf("Also update the changelog", "Then run the tests")) else it }
                show(style, dark, 420) { chrome ->
                    Column(Modifier.fillMaxWidth()) {
                        ChatHeader(s, machine, listOf(s), chrome, null, false, onBack = {}, onOpenSession = {}, onConnectionDetails = {},
                            onOverlay = {}, statusState = working)
                        Spacer(Modifier.weight(1f))
                        ChatSessionStatusLine(working, s, null, 0, onDetails = {}, terminal = null)
                        // the composer's control: Glass's is the mode button beside the model pill (Q11, #5055)
                        Row { if (style == AppStyle.Glass) HeaderModeButton(s, chrome, online = true) {} else ChatModeBar(s, chrome, online = true) }
                    }
                }
                assertTrue("$scene: mode button", described(if (scene == "after-bypass") "权限模式：完全访问" else "权限模式：允许编辑"))
                // the state is said once, in the header's title line; the line above the composer keeps only its facts
                assertTrue(has("工作中"))
                assertFalse("$scene: state said twice", has("正在工作"))
                assertEquals(scene == "after-facts", has("排队 2"))
                save(scene, style, dark)
            }
            // Every mode, not reported yet, and Codex (none)
            "modes" -> {
                val list = listOf(agent("default"), agent("plan"), agent("acceptEdits"), agent("auto"), agent("bypassPermissions"),
                    agent(null), agent(null, agent = "codex"))
                show(style, dark, 640) { chrome ->
                    Column(Modifier.fillMaxWidth()) {
                        for (s in list) Moded(s, chrome)
                    }
                }
                for (w in listOf("默认权限", "计划模式", "允许编辑", "自动审批", "完全访问", "未知")) assertTrue("modes: $w", described("权限模式：$w"))
                assertEquals(6, rule.onAllNodesWithContentDescription("权限模式：", substring = true).fetchSemanticsNodes().size)
                save("modes", style, dark)
            }
            "menu", "menu-bypass" -> {
                // not in a terminal pane: the node can't restart it into Allow Everything either (#6513, PermissionsCommandShotsTest)
                val s = agent("default", bypass = scene == "menu-bypass").let { if (scene == "menu") it.copy(paneId = null) else it }
                show(style, dark, 640) { chrome -> Column { Moded(s, chrome) } }
                rule.onNodeWithTag("chat-mode").performClick()
                rule.waitForIdle()
                assertTrue(has("权限模式：默认权限") && has("计划模式") && has("完全访问"))
                // without bypass_available it is there, greyed, saying it is a start's choice
                assertEquals(scene == "menu", has("只能在新开时选"))
                save(scene, style, dark, withPopup())
            }
            "confirm-bypass", "confirm-leave" -> {
                val into = scene == "confirm-bypass"
                val s = if (into) agent("default", bypass = true) else agent("bypassPermissions")
                show(style, dark, 640) { chrome -> Column { Moded(s, chrome) } }
                rule.onNodeWithTag("chat-mode").performClick()
                rule.waitForIdle()
                rule.onNode(hasText(if (into) "完全访问" else "计划模式") and hasAnyAncestor(isPopup())).performClick()
                rule.waitForIdle()
                assertTrue(has(if (into) "完全访问？" else "退出完全访问？"))
                assertTrue("nothing sent before the answer", actions.asked.isEmpty())
                save(scene, style, dark, rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
                rule.onNode(hasText(if (into) "完全访问" else "切到 计划模式") and hasAnyAncestor(isDialog())).performClick()
                rule.waitForIdle()
                assertEquals(listOf(ChatAction.Modes(permissionMode = if (into) "bypassPermissions" else "plan")), actions.asked)
            }
        }
    }

    @Test fun afterGlass() = shot("after", AppStyle.Glass, false)
    @Test fun afterGlassDark() = shot("after", AppStyle.Glass, true)
    @Test fun afterBrief() = shot("after", AppStyle.Brief, false)
    @Test fun afterBriefDark() = shot("after", AppStyle.Brief, true)
    @Test fun afterBoard() = shot("after", AppStyle.Board, false)
    @Test fun afterBoardDark() = shot("after", AppStyle.Board, true)
    @Test fun afterBypassGlass() = shot("after-bypass", AppStyle.Glass, false)
    @Test fun afterBypassGlassDark() = shot("after-bypass", AppStyle.Glass, true)
    @Test fun afterBypassBrief() = shot("after-bypass", AppStyle.Brief, false)
    @Test fun afterBypassBriefDark() = shot("after-bypass", AppStyle.Brief, true)
    @Test fun afterBypassBoard() = shot("after-bypass", AppStyle.Board, false)
    @Test fun afterBypassBoardDark() = shot("after-bypass", AppStyle.Board, true)
    @Test fun afterFactsGlass() = shot("after-facts", AppStyle.Glass, false)
    @Test fun afterFactsGlassDark() = shot("after-facts", AppStyle.Glass, true)
    @Test fun afterFactsBrief() = shot("after-facts", AppStyle.Brief, false)
    @Test fun afterFactsBriefDark() = shot("after-facts", AppStyle.Brief, true)
    @Test fun afterFactsBoard() = shot("after-facts", AppStyle.Board, false)
    @Test fun afterFactsBoardDark() = shot("after-facts", AppStyle.Board, true)
    @Test fun modesGlass() = shot("modes", AppStyle.Glass, false)
    @Test fun modesGlassDark() = shot("modes", AppStyle.Glass, true)
    @Test fun modesBrief() = shot("modes", AppStyle.Brief, false)
    @Test fun modesBriefDark() = shot("modes", AppStyle.Brief, true)
    @Test fun modesBoard() = shot("modes", AppStyle.Board, false)
    @Test fun modesBoardDark() = shot("modes", AppStyle.Board, true)
    @Test fun menuGlass() = shot("menu", AppStyle.Glass, false)
    @Test fun menuGlassDark() = shot("menu", AppStyle.Glass, true)
    @Test fun menuBrief() = shot("menu", AppStyle.Brief, false)
    @Test fun menuBriefDark() = shot("menu", AppStyle.Brief, true)
    @Test fun menuBoard() = shot("menu", AppStyle.Board, false)
    @Test fun menuBoardDark() = shot("menu", AppStyle.Board, true)
    @Test fun menuBypassGlass() = shot("menu-bypass", AppStyle.Glass, false)
    @Test fun menuBypassGlassDark() = shot("menu-bypass", AppStyle.Glass, true)
    @Test fun menuBypassBrief() = shot("menu-bypass", AppStyle.Brief, false)
    @Test fun menuBypassBriefDark() = shot("menu-bypass", AppStyle.Brief, true)
    @Test fun menuBypassBoard() = shot("menu-bypass", AppStyle.Board, false)
    @Test fun menuBypassBoardDark() = shot("menu-bypass", AppStyle.Board, true)
    @Test fun confirmBypassGlass() = shot("confirm-bypass", AppStyle.Glass, false)
    @Test fun confirmBypassGlassDark() = shot("confirm-bypass", AppStyle.Glass, true)
    @Test fun confirmBypassBrief() = shot("confirm-bypass", AppStyle.Brief, false)
    @Test fun confirmBypassBriefDark() = shot("confirm-bypass", AppStyle.Brief, true)
    @Test fun confirmBypassBoard() = shot("confirm-bypass", AppStyle.Board, false)
    @Test fun confirmBypassBoardDark() = shot("confirm-bypass", AppStyle.Board, true)
    @Test fun confirmLeaveGlass() = shot("confirm-leave", AppStyle.Glass, false)
    @Test fun confirmLeaveGlassDark() = shot("confirm-leave", AppStyle.Glass, true)
    @Test fun confirmLeaveBrief() = shot("confirm-leave", AppStyle.Brief, false)
    @Test fun confirmLeaveBriefDark() = shot("confirm-leave", AppStyle.Brief, true)
    @Test fun confirmLeaveBoard() = shot("confirm-leave", AppStyle.Board, false)
    @Test fun confirmLeaveBoardDark() = shot("confirm-leave", AppStyle.Board, true)
}
