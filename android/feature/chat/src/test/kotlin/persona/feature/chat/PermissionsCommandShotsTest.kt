package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import android.graphics.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performKeyInput
import androidx.compose.ui.test.pressKey
import androidx.compose.ui.test.requestFocus
import androidx.compose.ui.test.withKeyDown
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
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
import persona.core.contracts.Session
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalLargeWindowInput
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * `/permissions` and Allow Everything by a restart (#6513, the web's #6476): the "/" list offering /permissions, picking it
 * opening the mode menu with all five modes (nothing sent), Allow Everything offered with its restart note for Claude Code in
 * a pane started without a bypass flag, its confirmation repeating the note, and a restart that waits for the turn
 * (`bypass_pending`): the button keeps the mode it runs in and the menu says it restarts. Glass, light and dark (Brief and
 * Board's "Mode" sheet in the unit tests). PNGs `permissions-*` in `build/reports/tests/permissions-shots/`, under the
 * reports hosted CI keeps (`android-reports-*`), so a run without a local Android SDK still yields them.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class PermissionsCommandShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/reports/tests/permissions-shots").also { it.mkdirs() }

    private fun agent(mode: String, pending: Boolean = false) = Session(machineId = "box", sessionId = "s1", title = "Add a home screen widget",
        agent = "claude", status = "working", paneId = "w1:p1", cwd = "/srv/app", permissionMode = mode, effort = "high", bypassPending = pending)

    private class Actions : ChatActions {
        val asked = mutableListOf<ChatAction>()
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done.also { asked += action }
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
    }

    private val actions = Actions()
    private val sent = mutableListOf<String>()

    /** The composer as the chat page wires it on Glass: the mode button leading, /permissions opening its menu. */
    private fun show(s: Session, dark: Boolean, draft: String = "") {
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    val scope = rememberCoroutineScope()
                    val resources = LocalResources.current
                    val chrome = remember { ChatChromeState(resources, actions, scope, {}, {}) }
                    var openPermissions by remember { mutableStateOf(false) }
                    val state = remember { ChatComposerState().apply { value = TextFieldValue(draft, TextRange(draft.length)) } }
                    Box(Modifier.testTag("shot").width(400.dp).height(640.dp).background(PersonaTheme.palette.background)) {
                        Column(Modifier.fillMaxSize()) {
                            Spacer(Modifier.weight(1f))
                            ChatComposer(s.key, state, working = true, sending = false, canClear = true, service = null,
                                onSend = { sent += state.value.text }, onCommand = { sent += it }, onNotice = {},
                                leading = {
                                    HeaderModeButton(s, chrome, online = true, openRequested = openPermissions,
                                        onOpenHandled = { openPermissions = false }) {}
                                },
                                onModel = {}, onPermissions = { openPermissions = true }, loop = true)
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun save(name: String, dark: Boolean, bitmap: Bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()) {
        File(out(), "permissions-$name-${if (dark) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun withPopup(): Bitmap {
        val page = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val menu = rule.onNode(isPopup()).captureToImage().asAndroidBitmap()
        val p = rule.onNodeWithTag("shot").fetchSemanticsNode().positionOnScreen
        val m = rule.onNode(isPopup()).fetchSemanticsNode().positionOnScreen
        return page.copy(Bitmap.Config.ARGB_8888, true).also { Canvas(it).drawBitmap(menu, m.x - p.x, m.y - p.y, null) }
    }

    private fun has(text: String) = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
    private fun described(text: String) = rule.onAllNodesWithContentDescription(text, substring = true).fetchSemanticsNodes().isNotEmpty()

    private val restart = "Restarts the session to apply it once this turn ends"
    private val pendingNote = "The session restarts in Full access once this turn ends"

    private fun slash(dark: Boolean) {
        show(agent("auto"), dark, draft = "/")
        rule.onNodeWithText("/permissions").assertExists()
        save("slash", dark)
    }

    /** The box focused, as when the user types: the mode button sits in the composer only while it is open (Glass). */
    private fun focusBox() {
        rule.onNode(hasSetTextAction()).requestFocus()
        rule.waitForIdle()
    }

    private fun menu(dark: Boolean) {
        show(agent("auto"), dark, draft = "/perm")
        focusBox()
        rule.onNodeWithText("/permissions").performClick()
        rule.waitForIdle()
        // the five modes, the current one checked, Full access offered with its restart note; nothing sent
        for (w in listOf("Default permissions", "Auto accept edits", "Plan mode", "Auto-review", "Full access")) assertTrue("menu: $w", has(w))
        assertTrue(has(restart))
        assertFalse(has("only when starting a new one"))
        assertTrue(sent.isEmpty() && actions.asked.isEmpty())
        save("menu", dark, withPopup())
        rule.onNode(hasText("Full access") and hasAnyAncestor(isPopup())).performClick()
        rule.waitForIdle()
        assertTrue(has("Full access?") && has(restart))
        assertTrue("nothing sent before the answer", actions.asked.isEmpty())
        save("confirm", dark, rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
        rule.onNode(hasText("Full access") and hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals(listOf(ChatAction.Modes(permissionMode = SessionModes.BYPASS)), actions.asked)
    }

    private fun pending(dark: Boolean) {
        show(agent("acceptEdits", pending = true), dark)
        focusBox()
        // the button keeps the mode it runs in, and says the restart waits
        assertTrue(described("Permission mode: Auto accept edits"))
        assertTrue(described(pendingNote))
        rule.onNodeWithTag("chat-mode").performClick()
        rule.waitForIdle()
        assertTrue(rule.onAllNodesWithText(pendingNote, useUnmergedTree = true).fetchSemanticsNodes().size >= 2) // header line and the row
        save("pending", dark, withPopup())
        // Picking the checked mode sends it without a confirmation so the node calls off the queued restart (#7996 review).
        rule.onNode(hasText("Auto accept edits") and hasAnyAncestor(isPopup())).performClick()
        rule.waitForIdle()
        assertEquals(listOf(ChatAction.Modes(permissionMode = "acceptEdits")), actions.asked)
    }

    @Test fun slashLight() = slash(false)
    @Test fun slashDark() = slash(true)
    @Test fun menuLight() = menu(false)
    @Test fun menuDark() = menu(true)
    @Test fun pendingLight() = pending(false)
    @Test fun pendingDark() = pending(true)

    /** #11405: the expanded editor's hardware send must open the picker before its leading control leaves composition. */
    private fun expandedHardwarePermissions(style: AppStyle, key: Key) {
        val s = agent("auto")
        val long = (1..9).joinToString("\n") { "line $it with enough words to wrap in the editor" }
        val state = ChatComposerState().apply { value = TextFieldValue(long, TextRange(long.length)) }
        var openRequests = 0
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                    val scope = rememberCoroutineScope()
                    val resources = LocalResources.current
                    val chrome = remember { ChatChromeState(resources, actions, scope, {}, {}) }
                    var openPermissions by remember { mutableStateOf(false) }
                    Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) {
                        Column(Modifier.fillMaxSize()) {
                            Spacer(Modifier.weight(1f))
                            LargeWindowComposer(s.key, state, working = true, sending = false, canClear = true, service = null,
                                onSend = { sent += state.value.text }, onCommand = { sent += it }, onNotice = {},
                                leading = {
                                    if (style == AppStyle.Glass) {
                                        HeaderModeButton(s, chrome, online = true, openRequested = openPermissions,
                                            onOpenHandled = { openPermissions = false }) {}
                                    } else {
                                        ChatModeBar(s, chrome, online = true, onPickModel = {}, openRequested = openPermissions,
                                            onOpenHandled = { openPermissions = false })
                                    }
                                },
                                onModel = {}, onPermissions = { openPermissions = true; openRequests++ }, loop = true)
                        }
                    }
                }
            }
        }
        rule.runOnIdle { state.value = TextFieldValue(long, TextRange(long.length)) }
        rule.onNode(hasSetTextAction()).performClick()
        rule.onNodeWithTag("composer-expand").performClick()
        val input = rule.onNodeWithTag("composer-expanded-input").assertIsDisplayed()
        input.performClick()
        // A committed value, as after typing: an active IME composition deliberately gates hardware send.
        rule.runOnIdle { state.value = TextFieldValue("/permissions", TextRange(12)) }
        assertEquals("/permissions", state.value.text)
        input.performKeyInput { withKeyDown(key) { pressKey(Key.Enter) } }

        rule.onNodeWithTag("composer-expanded").assertDoesNotExist()
        assertEquals("$style $key clears the draft", "", state.value.text)
        assertTrue("$style $key sends nothing", sent.isEmpty() && actions.asked.isEmpty())
        assertEquals("$style $key requests the picker once", 1, openRequests)
        val picker = if (style == AppStyle.Glass) isPopup() else isDialog()
        rule.onNode(picker).assertIsDisplayed()
        rule.onNode(hasText("Default permissions") and hasAnyAncestor(picker), useUnmergedTree = true).assertIsDisplayed()
        if (style != AppStyle.Glass) {
            rule.onNode(hasText("Chat mode") and hasAnyAncestor(picker), useUnmergedTree = true).assertIsDisplayed()
        }

        // Dismiss on the already-selected mode: no mode action, and the one-off request must not reopen with the composer.
        rule.onNode(hasText("Auto-review") and hasAnyAncestor(picker), useUnmergedTree = true).performClick()
        rule.waitForIdle()
        rule.onNode(picker).assertDoesNotExist()
        rule.onNode(hasSetTextAction()).performClick()
        rule.waitForIdle()
        rule.onNode(picker).assertDoesNotExist()
        assertTrue("$style $key dismissal sends nothing", sent.isEmpty() && actions.asked.isEmpty())
    }

    @Config(sdk = [35], qualifiers = "en-w640dp-h800dp-xhdpi")
    @Test fun expandedPermissionsCtrlEnterOpensGlassMenu() = expandedHardwarePermissions(AppStyle.Glass, Key.CtrlLeft)

    @Config(sdk = [35], qualifiers = "en-w640dp-h800dp-xhdpi")
    @Test fun expandedPermissionsMetaEnterOpensGlassMenu() = expandedHardwarePermissions(AppStyle.Glass, Key.MetaLeft)

    @Config(sdk = [35], qualifiers = "en-w640dp-h800dp-xhdpi")
    @Test fun expandedPermissionsCtrlEnterOpensBriefModeSheet() = expandedHardwarePermissions(AppStyle.Brief, Key.CtrlLeft)

    @Config(sdk = [35], qualifiers = "en-w640dp-h800dp-xhdpi")
    @Test fun expandedPermissionsMetaEnterOpensBriefModeSheet() = expandedHardwarePermissions(AppStyle.Brief, Key.MetaLeft)

    @Config(sdk = [35], qualifiers = "en-w640dp-h800dp-xhdpi")
    @Test fun expandedPermissionsCtrlEnterOpensBoardModeSheet() = expandedHardwarePermissions(AppStyle.Board, Key.CtrlLeft)

    @Config(sdk = [35], qualifiers = "en-w640dp-h800dp-xhdpi")
    @Test fun expandedPermissionsMetaEnterOpensBoardModeSheet() = expandedHardwarePermissions(AppStyle.Board, Key.MetaLeft)
}
