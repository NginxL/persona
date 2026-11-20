package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.*
import java.io.File

/**
 * A to-do's stage bar (#4636) open on Develop, and once its work closed with nothing left to deploy (#11590) ending with
 * Done after Develop and after Merge, every step ticked; in every style, light and dark, with what TalkBack reads.
 * Written as PNGs to `build/todo-stage-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class TodoStageBarShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/todo-stage-shots").also { it.mkdirs() }

    @Test fun openAndFinishedBars() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xl)) {
                    Box { TodoStageBar(TodoStage.Develop) }
                    Box { TodoStageBar(TodoStage.Develop, done = true) }
                    Box { TodoStageBar(TodoStage.Merge, done = true) }
                }
            }
        }
        rule.waitForIdle()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            val shot = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "stage-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { assertTrue(shot.compress(Bitmap.CompressFormat.PNG, 100, it)) }
            val said = rule.onAllNodesWithTag("todo-stage").fetchSemanticsNodes()
                .map { it.config[SemanticsProperties.ContentDescription].single() }
            assertEquals("$s ${if (d) "dark" else "light"}",
                listOf("Stage: Develop (step 2 of 6)", "Stage: Done (step 3 of 3)", "Stage: Done (step 5 of 5)"), said)
        }
    }
}
