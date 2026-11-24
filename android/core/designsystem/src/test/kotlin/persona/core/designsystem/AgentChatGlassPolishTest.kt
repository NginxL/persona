package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * Glass agent chat polish aligned with iOS: monochrome composer Stop fill on Glass (Brief/Board keep danger red),
 * and neutral glass surface on [DecisionDock] without a saturated blue border.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AgentChatGlassPolishTest {
    @get:Rule val rule = createComposeRule()

    @Test fun glassStopButtonUsesActionFillWhileBriefAndBoardKeepDanger() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var expectedFill = Color.Unspecified
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                expectedFill = if (style == AppStyle.Glass) PersonaTheme.palette.action else PersonaTheme.palette.danger.dot
                Box(Modifier.background(PersonaTheme.palette.background).padding(8.dp)) {
                    SendButton({}, Modifier.testTag("stop"), kind = SendKind.Stop)
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s
            dark = d
            rule.waitForIdle()
            val bmp = rule.onNodeWithTag("stop").captureToImage().asAndroidBitmap()
            // Sample inside the circular/square fill ring (clear of the 16dp center stop glyph, inside the 38dp/40dp fill).
            val actual = Color(bmp.getPixel(bmp.width / 2, bmp.height / 4))
            for ((a, b) in listOf(actual.red to expectedFill.red, actual.green to expectedFill.green, actual.blue to expectedFill.blue)) {
                assertEquals("Stop fill for ${s.key} dark=$d", b, a, 2f / 255f)
            }
        }
    }

    @Test fun glassDecisionDockRendersWithNeutralSurfaceAndSemanticIcon() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("dock").background(PersonaTheme.palette.background).padding(12.dp)) {
                    DecisionDock(DecisionKind.Command, "Waiting for your approval") {
                        Text("Run swift test")
                    }
                }
            }
        }
        val bmp = rule.onNodeWithTag("dock").captureToImage().asAndroidBitmap()
        // Top border center pixel must stay neutral (not saturated needsYou blue).
        val topEdge = Color(bmp.getPixel(bmp.width / 2, 24))
        assertEquals("Neutral Glass dock border should not have strong blue shift", topEdge.red, topEdge.blue, 0.12f)
    }
}
