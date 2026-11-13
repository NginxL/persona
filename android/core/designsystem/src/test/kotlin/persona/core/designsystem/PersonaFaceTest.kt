package persona.core.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/** A pane dragged to an edge can constrain the face to zero width without giving it zero height. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w400dp-h900dp-mdpi")
class PersonaFaceTest {
    @get:Rule val rule = createComposeRule()

    @Test fun zeroWidthDoesNotCrashWhenDrawn() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.testTag("face-parent").size(40.dp)) {
                    PersonaFace(PersonaState.Idle, Modifier.width(0.dp).height(40.dp))
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithTag("face-parent").captureToImage()
    }
}
