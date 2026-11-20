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
import androidx.compose.runtime.Composable
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
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
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
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.SessionInfo
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * A running chat of Paseo's provider that lists its permission modes (#8261, Grok): the mode control (the header's on Brief
 * and Board, the composer's on Glass) with its two rows, its own approval under the plain shield, never Claude Code's
 * modes; going into Full access asks first, leaving it does not; Brief and Board's Mode sheet with the same rows
 * and its model's efforts; Session info without the counts nobody gives. Every style, light and dark. PNGs
 * `paseo-modes-*` in `build/paseo-modes-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class PaseoPermissionModesShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/paseo-modes-shots").also { it.mkdirs() }

    private val machine = Machine(id = "macbook", name = "MacBook", online = true)
    private fun grok(mode: String?) = Session(machineId = "macbook", sessionId = "paseo-agent-1", title = "Sort the photo albums",
        cwd = "/Users/me/code/albums", status = "idle", state = "idle", agent = "paseo", agentLabelHint = "Grok", launcher = "grok",
        model = "grok-5", effort = "high", efforts = listOf("low", "high"), permissionMode = mode, sourceKind = "runtime", spaceKind = "paseo",
        capabilities = SessionCapabilities(read = true, send = true, interrupt = true, approve = true, close = true, rename = true, modes = false,
            model = true, permissionModes = listOf("default", "bypassPermissions")))

    /** Takes every change and records it; Session info as a runtime's session gives it, with no counts. */
    private class Actions : ChatActions {
        val asked = mutableListOf<ChatAction>()
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done.also { asked += action }
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun info(session: Session) = SessionInfo(sessionId = session.sessionId, cwd = session.cwd, model = "grok-5",
            launcher = "grok", usageUnknown = true, startedAt = "2026-11-14T02:00:00Z", lastActivity = "2026-11-14T02:30:00Z")
    }

    private val actions = Actions()

    private fun show(style: AppStyle, dark: Boolean, height: Int, content: @Composable (ChatChromeState) -> Unit) {
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
        File(out(), "paseo-modes-$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
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

    /** The header and its mode control where the style has it: the composer's on Glass (Q11, #5055). */
    @Composable
    private fun Moded(s: Session, chrome: ChatChromeState) {
        Column(Modifier.fillMaxWidth()) {
            ChatHeader(s, machine, listOf(s), chrome, null, false, onBack = {}, onOpenSession = {}, onConnectionDetails = {}, onOverlay = {})
            Spacer(Modifier.height(8.dp))
            if (PersonaTheme.style == AppStyle.Glass) Row { HeaderModeButton(s, chrome, online = true) {} }
        }
    }

    private fun has(text: String) = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()

    /** None of Claude Code's own modes: those are not this agent's. */
    private fun noClaudeModes() {
        for (w in listOf("Default permissions", "Plan mode", "Auto accept edits")) assertFalse("$w is Claude Code's", has(w))
    }

    private fun scene(name: String, style: AppStyle, dark: Boolean) {
        when (name) {
            "menu" -> {
                show(style, dark, 520) { chrome -> Moded(grok(null), chrome) }
                rule.onNodeWithTag("chat-mode").performClick()
                rule.waitForIdle()
                assertTrue(has("Permission mode: Follow launcher") && has("Full access"))
                assertTrue(has("may already allow everything") && has("can be switched back"))
                noClaudeModes()
                save(name, style, dark, withPopup())
            }
            "confirm" -> {
                show(style, dark, 520) { chrome -> Moded(grok(null), chrome) }
                rule.onNodeWithTag("chat-mode").performClick()
                rule.waitForIdle()
                rule.onNode(hasText("Full access") and hasAnyAncestor(isPopup())).performClick()
                rule.waitForIdle()
                assertTrue(has("Full access?"))
                assertTrue("nothing sent before the answer", actions.asked.isEmpty())
                save(name, style, dark, rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
                rule.onNode(hasText("Full access") and hasAnyAncestor(isDialog())).performClick()
                rule.waitForIdle()
                assertEquals(listOf(ChatAction.Modes(permissionMode = "bypassPermissions")), actions.asked)
            }
            "leave" -> {
                // In Full access: the danger shield; back to its own approval without a question, since it can come back.
                show(style, dark, 520) { chrome -> Moded(grok("bypassPermissions"), chrome) }
                rule.onNodeWithTag("chat-mode").performClick()
                rule.waitForIdle()
                assertTrue(has("Permission mode: Full access"))
                save(name, style, dark, withPopup())
                rule.onNode(hasText("Follow launcher") and hasAnyAncestor(isPopup())).performClick()
                rule.waitForIdle()
                assertFalse(has("Leave Full access?"))
                rule.waitUntil(5_000) { actions.asked.isNotEmpty() }
                assertEquals(listOf(ChatAction.Modes(permissionMode = "default")), actions.asked)
            }
            "sheet" -> {
                show(style, dark, 900) { chrome -> ChatModeBar(grok(null), chrome, online = true) }
                rule.onNodeWithContentDescription("Mode").performClick()
                rule.waitForIdle()
                for (text in listOf("File permissions", "Follow launcher", "Full access", "Thinking effort", "Low", "High")) {
                    assertTrue("$text in the sheet", rule.onAllNodes(hasText(text) and hasAnyAncestor(isDialog()), useUnmergedTree = true)
                        .fetchSemanticsNodes().isNotEmpty())
                }
                noClaudeModes()
                save(name, style, dark, rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
            }
            "info" -> {
                show(style, dark, 900) { chrome -> ChatSessionInfoSheet(grok(null), machine, chrome, onDismiss = {}) }
                rule.waitUntil(5_000) { has("Follow launcher") }
                assertTrue(has("Started"))
                for (w in listOf("Context", "Generated", "Compactions")) assertFalse("$w: nobody gives it", has(w))
                save(name, style, dark, rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
            }
        }
    }

    @Test fun menuGlass() = scene("menu", AppStyle.Glass, false)
    @Test fun menuGlassDark() = scene("menu", AppStyle.Glass, true)
    @Test fun menuBrief() = scene("menu", AppStyle.Brief, false)
    @Test fun menuBriefDark() = scene("menu", AppStyle.Brief, true)
    @Test fun menuBoard() = scene("menu", AppStyle.Board, false)
    @Test fun menuBoardDark() = scene("menu", AppStyle.Board, true)
    @Test fun confirmBrief() = scene("confirm", AppStyle.Brief, false)
    @Test fun confirmGlassDark() = scene("confirm", AppStyle.Glass, true)
    @Test fun leaveBoard() = scene("leave", AppStyle.Board, false)
    @Test fun leaveGlassDark() = scene("leave", AppStyle.Glass, true)
    @Test fun sheetBrief() = scene("sheet", AppStyle.Brief, false)
    @Test fun sheetBriefDark() = scene("sheet", AppStyle.Brief, true)
    @Test fun sheetBoard() = scene("sheet", AppStyle.Board, false)
    @Test fun sheetBoardDark() = scene("sheet", AppStyle.Board, true)
    @Test fun infoGlass() = scene("info", AppStyle.Glass, false)
    @Test fun infoGlassDark() = scene("info", AppStyle.Glass, true)
    @Test fun infoBrief() = scene("info", AppStyle.Brief, false)
    @Test fun infoBriefDark() = scene("info", AppStyle.Brief, true)
}
