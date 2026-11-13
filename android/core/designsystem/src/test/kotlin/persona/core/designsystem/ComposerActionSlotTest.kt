package persona.core.designsystem

import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.click
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import kotlin.math.abs

/**
 * The composer's action slot (#4210 T3, iOS `ShellComposerBar`'s send and stop and `Theme.Motion.composerSwap`): which button
 * shows in each state; on Glass a swap grows and shrinks on iOS's curve; Brief, Board and system animations off swap at
 * once; the button on its way out takes no touch; and a swap goes on when `ComposerFrame` moves the slot to its other row.
 * The clock is the test's, so a frame is a frame.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ComposerActionSlotTest {
    @get:Rule val rule = createComposeRule()

    private var action by mutableStateOf<ComposerAction?>(null)
    private var expanded by mutableStateOf(false)
    private val taps = mutableListOf<String>()

    /** iOS's `timingCurve(0.38, 0.7, 0.125, 1)`, written here on its own so a wrong token cannot agree with itself. */
    private val ios = CubicBezierEasing(0.38f, 0.7f, 0.125f, 1f)

    /** A button is 48 dp across, in every style. */
    private val full = 48f

    private fun show(style: AppStyle = AppStyle.Glass, reduceMotion: Boolean = false, direction: LayoutDirection = LayoutDirection.Ltr) = rule.setContent {
        CompositionLocalProvider(LocalLayoutDirection provides direction) {
            PersonaTheme(darkTheme = false, reduceMotion = reduceMotion, style = style) {
                Row(Modifier.background(PersonaTheme.palette.background).width(300.dp), verticalAlignment = Alignment.CenterVertically) {
                    Spacer(Modifier.weight(1f))
                    ComposerActionSlot(action, onSend = { taps += "send" }, onStop = { taps += "stop" }, modifier = Modifier.testTag("slot"))
                }
            }
        }
    }

    private fun slotWidth(): Float = rule.onNodeWithTag("slot").getBoundsInRoot().let { (it.right - it.left).value }
    private fun width(description: String): Float = rule.onNodeWithContentDescription(description).getBoundsInRoot().let { (it.right - it.left).value }
    private fun shown(description: String) = rule.onAllNodesWithContentDescription(description).fetchSemanticsNodes().isNotEmpty()
    private fun frames(ms: Long) = rule.mainClock.advanceTimeBy(ms)
    private fun clock() { rule.mainClock.autoAdvance = false }

    /** A write from the test thread reaches the composition only once the snapshot is applied (docs/lessons/android.md). */
    private fun change(action: ComposerAction? = this.action, expanded: Boolean = this.expanded) {
        this.action = action
        this.expanded = expanded
        Snapshot.sendApplyNotifications()
    }

    /**
     * How far the swap has gone [ms] after the state changed, 0 to 1, on iOS's curve: an entering button's animation starts a
     * frame after the change, a leaving one's up to two, hence the wider [leaving] margin.
     */
    private fun grown(ms: Long) = ios.transform(((ms - 16).coerceAtLeast(0) / 400f).coerceAtMost(1f))
    private val leaving = 0.12f

    // --- Which button shows -----------------------------------------------------------------------------------------------

    @Test fun whichButtonShowsInEachState() {
        show(reduceMotion = true)
        rule.waitForIdle()
        assertFalse(shown("Send")); assertFalse(shown("Stop"))
        change(action = ComposerAction(SendKind.Send)); rule.waitForIdle()
        assertTrue(shown("Send")); assertFalse(shown("Stop"))
        rule.onNodeWithContentDescription("Send").assertIsEnabled()
        change(action = ComposerAction(SendKind.Stop)); rule.waitForIdle()
        assertFalse(shown("Send")); assertTrue(shown("Stop"))
        change(action = ComposerAction(SendKind.Send, enabled = false)); rule.waitForIdle()
        rule.onNodeWithContentDescription("Send").assertIsNotEnabled()
        change(action = ComposerAction(SendKind.Send, loading = true)); rule.waitForIdle()
        rule.onNodeWithContentDescription("Send").assertIsNotEnabled()
        change(action = null); rule.waitForIdle()
        assertFalse(shown("Send")); assertFalse(shown("Stop"))
    }

    @Test fun eachButtonAnswersItsOwnTap() {
        show(reduceMotion = true)
        change(action = ComposerAction(SendKind.Send)); rule.waitForIdle()
        rule.onNodeWithContentDescription("Send").performClick()
        change(action = ComposerAction(SendKind.Stop)); rule.waitForIdle()
        rule.onNodeWithContentDescription("Stop").performClick()
        assertEquals(listOf("send", "stop"), taps)
    }

    // --- Glass moves ------------------------------------------------------------------------------------------------------

    @Test fun sendGrowsOutOfTheTrailingEdgeOnIosCurve() {
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send))
        var elapsed = 0L
        var last = 0f
        for (at in listOf(16L, 40L, 80L, 120L, 200L, 400L)) {
            frames(at - elapsed); elapsed = at
            val now = width("Send")
            assertTrue("$at ms: $now dp after $last dp", now >= last - 0.01f)
            assertEquals("$at ms", grown(at), now / full, 0.08f)
            // Out of the right edge: it stays against it as it grows.
            assertEquals("$at ms", 300f, rule.onNodeWithContentDescription("Send").getBoundsInRoot().right.value, 0.1f)
            last = now
        }
        assertEquals(full, width("Send"), 0.01f)
    }

    @Test fun whereTheTrailingEdgeIsTheLeftOneSendGrowsOutOfThat() {
        clock(); show(direction = LayoutDirection.Rtl)
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send))
        frames(80)
        val bounds = rule.onNodeWithContentDescription("Send").getBoundsInRoot()
        assertEquals("it stays against the left edge", 0f, bounds.left.value, 0.1f)
        assertTrue("and is partway: ${bounds.right.value} dp", bounds.right.value > 1f && bounds.right.value < 0.9f * full)
        frames(500)
        assertEquals(full, width("Send"), 0.01f)
    }

    @Test fun sendStartsTinyNotAtZero() {
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send))
        frames(16)
        val first = width("Send")
        assertTrue("send's first frame is $first dp", first > 0f && first < 0.05f * full)
    }

    @Test fun stopShrinksToAFifthInItsMiddleAndSendGrowsInItsPlace() {
        change(action = ComposerAction(SendKind.Stop))
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send))
        frames(16)
        // The stop that is leaving is still there, at about its size.
        assertEquals(full, width("Stop"), 1f)
        val stop = rule.onNodeWithContentDescription("Stop").getBoundsInRoot()
        val middle = (stop.left.value + stop.right.value) / 2
        var elapsed = 16L
        for (at in listOf(80L, 120L, 200L)) {
            frames(at - elapsed); elapsed = at
            val bounds = rule.onNodeWithContentDescription("Stop").getBoundsInRoot()
            assertEquals("$at ms", 1f - 0.8f * grown(at), (bounds.right.value - bounds.left.value) / full, leaving)
            assertEquals("$at ms: stop stays in its middle", middle, (bounds.left.value + bounds.right.value) / 2, 0.5f)
        }
        frames(400)
        assertFalse("the stop is gone once it has left", shown("Stop"))
        assertEquals(full, width("Send"), 0.01f)
    }

    @Test fun sendLeavesTheOtherWayAndStopGrowsFromAFifth() {
        change(action = ComposerAction(SendKind.Send))
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Stop))
        frames(16)
        assertEquals(0.2f, width("Stop") / full, 0.03f)
        frames(64)
        assertEquals(1f - grown(80), width("Send") / full, leaving)
        assertEquals(300f, rule.onNodeWithContentDescription("Send").getBoundsInRoot().right.value, 0.1f)
        frames(400)
        assertFalse(shown("Send"))
        assertEquals(full, width("Stop"), 0.01f)
    }

    @Test fun theSlotsWidthGoesWithTheSwapSoWhatStandsBesideItGlides() {
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        assertEquals(0f, slotWidth(), 0.01f)
        change(action = ComposerAction(SendKind.Send))
        frames(48)
        val mid = slotWidth()
        assertTrue("the slot is $mid dp wide, between none and its 48", mid > 0f && mid < full)
        frames(500)
        assertEquals(full, slotWidth(), 0.01f)
        change(action = null)
        frames(48)
        val back = slotWidth()
        assertTrue("the slot is $back dp wide on its way back", back > 0f && back < full)
        frames(500)
        assertEquals(0f, slotWidth(), 0.01f)
    }

    @Test fun aSwapTurnedBackContinuesFromWhereItWasRatherThanJumping() {
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send))
        frames(80)
        val before = width("Send")
        change(action = null)
        frames(16)
        val after = width("Send")
        assertTrue("$before dp, then $after dp a frame after the turn", before > 0.1f * full && after > 0.1f * full && abs(after - before) < 0.3f * full)
        frames(500)
        assertFalse(shown("Send"))
    }

    @Test fun aChangeInHowItLooksHappensInPlace() {
        change(action = ComposerAction(SendKind.Send, enabled = false))
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send, enabled = true))
        for (frame in 1..4) {
            rule.mainClock.advanceTimeByFrame()
            assertEquals("frame $frame", 1, rule.onAllNodesWithContentDescription("Send").fetchSemanticsNodes().size)
            assertEquals(full, width("Send"), 0.01f)
        }
        rule.onNodeWithContentDescription("Send").assertIsEnabled()
    }

    // --- The button on its way out ----------------------------------------------------------------------------------------

    @Test fun theLeavingButtonTakesNoTouchAndIsNotReadOut() {
        change(action = ComposerAction(SendKind.Stop))
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        rule.onNodeWithContentDescription("Stop").assertIsEnabled()
        change(action = ComposerAction(SendKind.Send))
        frames(48)
        // Still there to see, but not pressable and not for the screen reader.
        rule.onNodeWithContentDescription("Stop").assertIsNotEnabled()
            .assert(SemanticsMatcher.keyIsDefined(SemanticsProperties.HideFromAccessibility))
        rule.onNodeWithContentDescription("Stop").performTouchInput { click(center) }
        assertEquals("a tap on the stop that is leaving did nothing", emptyList<String>(), taps)
        // The one coming is for the screen reader from the start, and takes a tap as soon as it is under the finger.
        rule.onNodeWithContentDescription("Send").assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.HideFromAccessibility))
        frames(100)
        rule.onNodeWithContentDescription("Send").performClick()
        assertEquals(listOf("send"), taps)
        frames(500)
        assertFalse(shown("Stop"))
    }

    @Test fun aButtonThatIsStayingKeepsTakingTouch() {
        change(action = ComposerAction(SendKind.Send))
        clock(); show()
        rule.mainClock.advanceTimeByFrame()
        // Its looks change in place; it is never inert.
        change(action = ComposerAction(SendKind.Send, loading = true))
        frames(48)
        change(action = ComposerAction(SendKind.Send))
        frames(48)
        rule.onNodeWithContentDescription("Send").assertIsEnabled().assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.HideFromAccessibility))
        rule.onNodeWithContentDescription("Send").performClick()
        assertEquals(listOf("send"), taps)
    }

    // --- Nothing moves where nothing should ------------------------------------------------------------------------------

    @Test fun withAnimationsOffGlassSwapsAtOnce() = swapsAtOnce(AppStyle.Glass, reduceMotion = true)
    @Test fun briefSwapsAtOnce() = swapsAtOnce(AppStyle.Brief, reduceMotion = false)
    @Test fun boardSwapsAtOnce() = swapsAtOnce(AppStyle.Board, reduceMotion = false)

    private fun swapsAtOnce(style: AppStyle, reduceMotion: Boolean) {
        change(action = ComposerAction(SendKind.Stop))
        clock(); show(style, reduceMotion)
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send))
        rule.mainClock.advanceTimeByFrame()
        assertFalse("$style: the old button is gone in the first frame", shown("Stop"))
        assertEquals(full, width("Send"), 0.01f)
        assertEquals(full, slotWidth(), 0.01f)
        change(action = null)
        rule.mainClock.advanceTimeByFrame()
        assertFalse("$style: and so is the one that went", shown("Send"))
        change(action = ComposerAction(SendKind.Send))
        rule.mainClock.advanceTimeByFrame()
        assertEquals(full, width("Send"), 0.01f)
        rule.onNodeWithContentDescription("Send").performClick()
        assertEquals(listOf("send"), taps)
    }

    // --- The frame moving the slot between rows ----------------------------------------------------------------------------

    private fun showFrame() = rule.setContent {
        PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
            Box(Modifier.background(PersonaTheme.palette.background).width(360.dp)) {
                ComposerFrame(
                    expanded = expanded,
                    leading = { Spacer(Modifier.width(48.dp)) },
                    trailing = { ComposerActionSlot(action, onSend = { taps += "send" }, onStop = { taps += "stop" }) },
                    field = { Text("Message") },
                )
            }
        }
    }

    @Test fun aSwapGoesOnWhenTheComposerGrowsAndMovesTheSlotToItsOtherRow() {
        clock(); showFrame()
        rule.mainClock.advanceTimeByFrame()
        // Focus and a first character arrive together: the composer opens (the slot moves to the second row) and send comes.
        change(action = ComposerAction(SendKind.Send), expanded = true)
        frames(16)
        val first = width("Send")
        frames(48)
        val later = width("Send")
        assertTrue("send is $first dp in its first frame and $later dp 48 ms on: still growing", first < 0.1f * full && later > first && later < 0.9f * full)
        frames(500)
        assertEquals(full, width("Send"), 0.01f)
    }

    @Test fun aSwapUnderWayKeepsGoingWhenTheSlotMoves() {
        clock(); showFrame()
        rule.mainClock.advanceTimeByFrame()
        change(action = ComposerAction(SendKind.Send))
        frames(80)
        val before = width("Send")
        assertTrue("send is $before dp, partway", before > 0.1f * full && before < 0.9f * full)
        // The composer opens in the middle of it: the slot moves rows.
        change(expanded = true)
        frames(16)
        val after = width("Send")
        assertTrue("$before dp, then $after dp after the move: it did not start over or jump to the end", after >= before - 0.01f && after < 0.99f * full)
        frames(500)
        assertEquals(full, width("Send"), 0.01f)
        // Closing it moves the slot back, with send leaving: that goes on in the first row too.
        change(action = null, expanded = false)
        frames(32)
        assertTrue("send is still leaving in the first row", shown("Send"))
        frames(500)
        assertFalse(shown("Send"))
    }

    // --- Tokens ------------------------------------------------------------------------------------------------------------

    @Test fun blurIsForAndroid12AndLater() {
        assertEquals(Dp(0f), ComposerMotion.blur(sdk = 30))
        assertEquals(Tokens.Motion.ComposerSwap.blur, ComposerMotion.blur(sdk = 31))
        assertEquals(Tokens.Motion.ComposerSwap.blur, ComposerMotion.blur(sdk = 37))
    }

    @Test fun theCurvesTheTokensNameAreTheOnesTheDesignSystemKnows() {
        for (name in listOf(Tokens.Motion.ComposerSwap.fadeCurve, Tokens.Motion.ComposerPress.curve)) ComposerMotion.curve(name)
        assertTrue(runCatching { ComposerMotion.curve("elastic") }.isFailure)
    }
}
