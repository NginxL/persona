package persona.feature.chat

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.InterfaceFontScaleCap
import persona.core.designsystem.PersonaTheme

/** Natural code width is measured at the same reading density that draws the shared message. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w800dp-h1200dp-mdpi")
class MessageShareReadingSizeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun shortCodeWidensAboveTheCapWithoutChangingTheBaseCard() {
        var scale by mutableStateOf(1f)
        val message = MessagePicture("```\nabcdefghijklmnopqrstuvwxyz\n```", user = false)
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(reduceMotion = true) {
                    ShareCardPicture(message, dark = false, layer = null, modifier = Modifier.testTag(CardTag))
                }
            }
        }
        var baseWidth = -1f
        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            val bounds = rule.onNodeWithTag(CardTag).getUnclippedBoundsInRoot()
            val width = (bounds.right - bounds.left).value
            if (chosenScale <= InterfaceFontScaleCap) {
                assertEquals("base card at $chosenScale", ShareCardWidth.BASE, width, 0f)
                if (baseWidth < 0) baseWidth = width else assertEquals(baseWidth, width, 0f)
            } else {
                assertTrue("2.0 code width $width must exceed the capped base $baseWidth", width > baseWidth)
                assertTrue("card width $width must remain bounded", width <= ShareCardWidth.CAP)
            }
        }
    }

    private companion object {
        const val CardTag = "share-card-reading-measure"
    }
}
