package persona.core.designsystem

import androidx.activity.ComponentActivity
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertIsFocused
import androidx.compose.ui.test.assertIsNotFocused
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Real window, real focus and the real input method: the Robolectric tests cannot show the soft keyboard. */
@RunWith(AndroidJUnit4::class)
class SettingsFocusInstrumentedTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val automation get() = InstrumentationRegistry.getInstrumentation().uiAutomation
    private var lastTouch = ""
    private var previousSetting: String? = null

    @Before fun showTheKeyboardWithoutAPhysicalOne() {
        previousSetting = shell("settings get secure show_ime_with_hard_keyboard").trim()
        shell("settings put secure show_ime_with_hard_keyboard 1")
    }

    @After fun restoreTheSetting() {
        val value = previousSetting?.takeIf { it == "0" || it == "1" } ?: "0"
        shell("settings put secure show_ime_with_hard_keyboard $value")
    }

    @Test fun theSecondFieldKeepsFocusAcrossPresentationChangesInARealWindow() {
        var large by mutableStateOf(true)
        setContent { large }
        rule.onNodeWithTag("first-settings-field").assertIsNotFocused()
        rule.onNodeWithTag("second-settings-field").assertIsFocused()
        large = false
        rule.waitForIdle()
        rule.onNodeWithTag("second-settings-field").assertIsFocused()
        rule.onNodeWithTag("first-settings-field").assertIsNotFocused()
        large = true
        rule.waitForIdle()
        Thread.sleep(2000)
        assertTrue("after compact to large first=${focusedOf("first-settings-field")} second=${focusedOf("second-settings-field")}",
            focusedOf("second-settings-field") == true)
        rule.onNodeWithTag("first-settings-field").assertIsNotFocused()
    }

    @Test fun anExplicitClearThenAResizeDoesNotBringTheKeyboardBack() {
        var large by mutableStateOf(true)
        setContent { large }
        assertTrue("keyboard shows for the focused field: ${inputState()}", waitForKeyboard(true))
        realTouch("settings-blank-label")
        rule.waitForIdle()
        try { rule.onNodeWithTag("second-settings-field").assertIsNotFocused() } catch (e: AssertionError) {
            throw AssertionError("tap at $lastTouch left focus; card=${rule.onNodeWithTag("settings-page-card").fetchSemanticsNode().let { "${it.positionOnScreen} ${it.size}" }} first=${focusedOf("first-settings-field")} second=${focusedOf("second-settings-field")} ime=${inputState()}", e)
        }
        assertTrue("keyboard hides after the blank tap: ${inputState()}", waitForKeyboard(false))
        large = false
        rule.waitForIdle()
        large = true
        rule.waitForIdle()
        Thread.sleep(1500)
        rule.onNodeWithTag("first-settings-field").assertIsNotFocused()
        rule.onNodeWithTag("second-settings-field").assertIsNotFocused()
        assertFalse("a resize must not raise the keyboard again: ${inputState()}", inputShown())
    }

    private fun setContent(large: () -> Boolean) {
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large()) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    JSettingsSheet(onDismiss = {}) {
                        val second = remember { FocusRequester() }
                        LaunchedEffect(Unit) { val _ = runCatching { second.requestFocus() } }
                        Column(Modifier.verticalScroll(rememberScrollState())) {
                            Text("Blank area", Modifier.testTag("settings-blank-label"))
                            BasicTextField("first", {}, Modifier.testTag("first-settings-field"))
                            BasicTextField("second", {}, Modifier.testTag("second-settings-field").focusRequester(second))
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    /** A finger on the touchscreen, so the window leaves keyboard focus mode the way a real tap does. */
    private fun realTouch(tag: String) {
        val node = rule.onNodeWithTag(tag).fetchSemanticsNode()
        val x = node.positionOnScreen.x + node.size.width / 2f
        val y = node.positionOnScreen.y + node.size.height / 2f
        lastTouch = "x=$x y=$y size=${node.size} pos=${node.positionOnScreen}"
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val downTime = android.os.SystemClock.uptimeMillis()
        val down = android.view.MotionEvent.obtain(downTime, downTime, android.view.MotionEvent.ACTION_DOWN, x, y, 0)
        val up = android.view.MotionEvent.obtain(downTime, downTime + 60, android.view.MotionEvent.ACTION_UP, x, y, 0)
        down.source = android.view.InputDevice.SOURCE_TOUCHSCREEN
        up.source = android.view.InputDevice.SOURCE_TOUCHSCREEN
        try {
            instrumentation.sendPointerSync(down)
            instrumentation.sendPointerSync(up)
            instrumentation.waitForIdleSync()
        } finally {
            down.recycle()
            up.recycle()
        }
    }

    private fun focusedOf(tag: String) = rule.onNodeWithTag(tag).fetchSemanticsNode().config
        .getOrNull(androidx.compose.ui.semantics.SemanticsProperties.Focused)

    private fun waitForKeyboard(shown: Boolean): Boolean {
        val deadline = System.currentTimeMillis() + 6000
        while (System.currentTimeMillis() < deadline) {
            if (inputShown() == shown) return true
            Thread.sleep(200)
        }
        return false
    }

    private fun inputShown(): Boolean = inputState().contains("mInputShown=true")

    private fun inputState(): String = shell("dumpsys input_method").lineSequence()
        .filter { it.contains("mInputShown") || it.contains("mImeWindowVis") }.joinToString(" | ")

    private fun shell(command: String): String =
        android.os.ParcelFileDescriptor.AutoCloseInputStream(automation.executeShellCommand(command)).bufferedReader().use { it.readText() }
}
