package persona.feature.chat

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

/** The collapsed historical plan and its full reader, at ordinary and the largest accepted text size. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-mdpi")
class PlanDetailShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @Test fun planAndReaderAcrossStylesLightDarkAndTextSizes() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableStateOf(1f)
        var openReader = ""
        var readerTitle = ""
        val item = Item("plan", kind = "tool_use", toolName = "ExitPlanMode",
            toolInput = buildJsonObject { put("plan", LongPlan) })
        rule.setContent {
            openReader = stringResource(R.string.chat_reader_open)
            readerTitle = stringResource(R.string.chat_tool_plan_title)
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Column(Modifier.width(400.dp).background(PersonaTheme.palette.background)
                        .padding(Tokens.Space.m).testTag("plan-detail-shot")) {
                        ToolInputDetail(item)
                    }
                }
            }
        }
        for (chosenStyle in AppStyle.entries) for (night in listOf(false, true)) for (chosenScale in listOf(1f, 2f)) {
            style = chosenStyle
            dark = night
            scale = chosenScale
            rule.waitForIdle()
            val suffix = "${chosenStyle.name.lowercase()}-${if (night) "dark" else "light"}-${chosenScale.toInt()}x"
            save("plan-$suffix", rule.onNodeWithTag("plan-detail-shot").captureToImage().asAndroidBitmap())
            rule.onNodeWithText(openReader).performClick()
            rule.waitUntil(10_000) { rule.onAllNodesWithText(readerTitle).fetchSemanticsNodes().isNotEmpty() }
            save("reader-$suffix", rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
            rule.onNodeWithContentDescription(rule.activity.getString(persona.core.designsystem.R.string.designsystem_back)).performClick()
            rule.waitForIdle()
        }
    }

    private fun save(name: String, bitmap: Bitmap) {
        val directory = File(System.getProperty("persona.shots") ?: "build/plan-detail-shots").also { it.mkdirs() }
        File(directory, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    private companion object {
        val LongPlan = (1..41).joinToString("\n") { "Step $it: Review the release notes" }
    }
}
