package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.toPixelMap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import kotlin.math.abs

/**
 * Send shrinks under a finger on Glass, slowly, and comes back quicker when let go, as iOS's does for the shell composer's
 * send (`ComposerRoundButton(pressScales:)`, `Theme.Motion.composerPress`, 0.75): measured on the pixels of the button, with
 * the clock in the test's hand. It does not with the system's animations off, in Brief and Board, for stop, or for a send
 * that did not ask for it (the Inbox's reply, as on iOS).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SendButtonPressTest {
    @get:Rule val rule = createComposeRule()

    /** The ways a composer draws its round button. */
    private enum class Draws { PlainSend, ScalingSend, SlotSend, SlotStop }

    @Composable
    private fun Button(draws: Draws, modifier: Modifier) = when (draws) {
        Draws.PlainSend -> SendButton({}, modifier)
        Draws.ScalingSend -> SendButton({}, modifier, pressScales = true)
        Draws.SlotSend -> ComposerActionSlot(ComposerAction(SendKind.Send), {}, {}, modifier)
        Draws.SlotStop -> ComposerActionSlot(ComposerAction(SendKind.Stop), {}, {}, modifier)
    }

    private fun show(draws: Draws, style: AppStyle, reduceMotion: Boolean) = rule.setContent {
        PersonaTheme(darkTheme = false, reduceMotion = reduceMotion, style = style) {
            Box(Modifier.background(PersonaTheme.palette.background).padding(8.dp)) {
                Button(draws, Modifier.testTag("button"))
            }
        }
    }

    private fun distance(a: Color, b: Color) = abs(a.red - b.red) + abs(a.green - b.green) + abs(a.blue - b.blue)

    /** The width in pixels of the button's disc along its middle line: the pixels nearer its fill than the page. */
    private fun disc(): Int {
        val map = rule.onNodeWithTag("button").captureToImage().toPixelMap()
        val row = map.height / 2
        val fill = map[(map.width * 0.3f).toInt(), row]
        val page = map[1, 1]
        val across = (0 until map.width).filter { distance(map[it, row], fill) < distance(map[it, row], page) }
        return across.last() - across.first() + 1
    }

    private fun press() = rule.onNodeWithTag("button").performTouchInput { down(center) }
    private fun release() = rule.onNodeWithTag("button").performTouchInput { up() }

    /** Shows the button with the clock in the test's hand, and gives its width at rest. */
    private fun rest(draws: Draws, style: AppStyle = AppStyle.Glass, reduceMotion: Boolean = false): Int {
        rule.mainClock.autoAdvance = false
        show(draws, style, reduceMotion)
        rule.mainClock.advanceTimeBy(500)
        return disc()
    }

    @Test fun sendShrinksSlowlyToThreeQuartersWhilePressedAndComesBackQuickerOnGlass() = shrinks(Draws.ScalingSend)

    /** The composer's own path, through the slot, shrinks the same. */
    @Test fun theComposersSlotShrinksSendToo() = shrinks(Draws.SlotSend)

    private fun shrinks(draws: Draws) {
        val rest = rest(draws)
        assertEquals("a disc of 38 dp on a 2x screen", 76, rest)
        press()
        rule.mainClock.advanceTimeBy(100)
        val early = disc()
        // Slowly: 0.4 s, easing out, so after a quarter of it the disc has done well under its half-way.
        assertTrue("$early px after 100 ms of $rest", early < rest && early > 0.80f * rest)
        rule.mainClock.advanceTimeBy(500)
        val pressed = disc()
        assertEquals("0.75 of $rest", 0.75f * rest, pressed.toFloat(), 3f)
        release()
        rule.mainClock.advanceTimeBy(100)
        val back = disc()
        // Quicker: 0.25 s, so after 100 ms of it more than half of the way is back.
        assertTrue("$back px 100 ms after the release, from $pressed to $rest", (back - pressed).toFloat() / (rest - pressed) > 0.6f)
        rule.mainClock.advanceTimeBy(300)
        assertEquals(rest.toFloat(), disc().toFloat(), 1f)
    }

    @Test fun nothingShrinksWithTheSystemsAnimationsOff() = staysTheSame(Draws.ScalingSend, reduceMotion = true)
    @Test fun nothingShrinksInTheSlotWithTheSystemsAnimationsOff() = staysTheSame(Draws.SlotSend, reduceMotion = true)
    @Test fun briefDoesNotShrink() = staysTheSame(Draws.ScalingSend, AppStyle.Brief)
    @Test fun boardDoesNotShrink() = staysTheSame(Draws.ScalingSend, AppStyle.Board)
    @Test fun stopDoesNotShrink() = staysTheSame(Draws.SlotStop)
    @Test fun aSendThatDidNotAskForItKeepsThePlainPress() = staysTheSame(Draws.PlainSend)

    private fun staysTheSame(draws: Draws, style: AppStyle = AppStyle.Glass, reduceMotion: Boolean = false) {
        val rest = rest(draws, style, reduceMotion)
        press()
        rule.mainClock.advanceTimeBy(600)
        assertEquals("$draws in $style pressed", rest.toFloat(), disc().toFloat(), 1f)
    }
}
