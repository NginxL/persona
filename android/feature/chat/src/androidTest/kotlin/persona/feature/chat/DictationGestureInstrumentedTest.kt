package persona.feature.chat

import android.graphics.Rect
import android.os.SystemClock
import android.view.InputDevice
import android.view.MotionEvent
import android.view.accessibility.AccessibilityNodeInfo
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
class DictationGestureInstrumentedTest {
    @get:Rule val rule = createAndroidComposeRule<ComposerInstrumentationActivity>()

    @Test fun onlyAReleaseThatIsStillFlickingDropsAShortLeftDrag() {
        val kept = mutableListOf<String>()
        var starts = 0
        rule.setContent {
            val scope = rememberCoroutineScope()
            val control = remember {
                DictationControl(DictationSession(scope, open = { listener -> object : DictationSession.Input {
                    override fun start() { starts++; listener.partial("Spoken words") }
                    override fun stop() { listener.result("Spoken words") }
                    override fun close() {}
                } }, hasPermission = { true }, askPermission = {}, keep = kept::add, error = { error("Recognition failed") })) {}
            }
            PersonaTheme(reduceMotion = true) { Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { DictationMic(control, "Dictate") } }
        }
        val automation = InstrumentationRegistry.getInstrumentation().uiAutomation
        val density = rule.activity.resources.displayMetrics.density
        fun button(node: AccessibilityNodeInfo): AccessibilityNodeInfo? {
            if (node.contentDescription == "Dictate") return node
            return (0 until node.childCount).firstNotNullOfOrNull { node.getChild(it)?.let(::button) }
        }
        rule.onNodeWithContentDescription("Dictate").assertExists()
        val bounds = Rect().also { requireNotNull(button(automation.rootInActiveWindow)).getBoundsInScreen(it) }
        fun drag(duration: Int, pause: Long = 0, end: Int = MotionEvent.ACTION_UP) {
            val expectedStarts = starts + 1
            val start = SystemClock.uptimeMillis()
            fun pointer(action: Int, fraction: Float) {
                val event = MotionEvent.obtain(start, SystemClock.uptimeMillis(), action,
                    bounds.exactCenterX() - 62 * density * fraction, bounds.exactCenterY(), 0)
                event.source = InputDevice.SOURCE_TOUCHSCREEN
                try { require(automation.injectInputEvent(event, false)) } finally { event.recycle() }
            }
            pointer(MotionEvent.ACTION_DOWN, 0f)
            rule.waitUntil { starts == expectedStarts }
            for (step in 1..8) {
                SystemClock.sleep((duration / 8).toLong())
                pointer(MotionEvent.ACTION_MOVE, step / 8f)
            }
            if (pause > 0) SystemClock.sleep(pause)
            pointer(end, 1f)
            automation.waitForIdle(100, 3000)
            rule.waitForIdle()
        }

        drag(50)
        assertEquals(emptyList<String>(), kept)
        drag(440)
        assertEquals(listOf("Spoken words"), kept)
        drag(50, pause = 180)
        assertEquals(listOf("Spoken words", "Spoken words"), kept)
        drag(50, end = MotionEvent.ACTION_CANCEL)
        assertEquals(3, kept.size)
    }
}
