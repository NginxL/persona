package persona.feature.chat

import android.content.Context
import android.content.res.Configuration
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.domain.AttachedFile
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

/** Sent file bubbles: 1, 4, 5 and 10 files, folded and opened, in the full JVM appearance matrix. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ChatFileBubbleShotsTest {
    @get:Rule val rule = createComposeRule()
    private val context = ApplicationProvider.getApplicationContext<Context>()

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
    @Test fun english() = matrix("en", 1f)
    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun englishLargeText() = matrix("en", 2f)
    @Config(sdk = [35], qualifiers = "zh-w400dp-h2400dp-xhdpi")
    @Test fun chinese() = matrix("zh-Hans", 1f)
    @Config(sdk = [35], qualifiers = "zh-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun chineseLargeText() = matrix("zh-Hans", 2f)

    private enum class Scene(val count: Int, val open: Boolean) {
        One(1, false), Four(4, false), FiveFolded(5, false), FiveOpen(5, true), TenFolded(10, false), TenOpen(10, true),
    }

    private fun matrix(language: String, fontScale: Float) {
        val resources = context.resources
        var scene by mutableStateOf(Scene.One)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            val files = remember(scene.count) {
                List(scene.count) { index -> AttachedFile("file", "/up/report-$index.txt", "report-$index.txt", 1000L + index) }
            }
            val raw = "Review the attached files\n\n" + files.joinToString("\n") { it.toXml() }
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration,
                LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        MergedUserBubble(listOf(Item("user", role = "user", kind = "text", text = raw, ts = "2026-11-15T10:00:00Z")))
                    }
                }
            }
        }
        val out = File("build/file-bubble-shots-8527").apply { mkdirs() }
        for (sc in Scene.entries) {
            rule.runOnIdle { scene = sc }
            rule.waitForIdle()
            if (sc.open) {
                rule.onNodeWithTag("attached-files-fold").performClick()
                rule.waitForIdle()
            }
            if (sc.count <= 4) {
                rule.onNodeWithTag("attached-files-fold").assertDoesNotExist()
                rule.onNodeWithText("report-${sc.count - 1}.txt").assertExists()
            } else {
                val hidden = sc.count - 3
                val label = if (sc.open) resources.getString(R.string.chat_files_show_less)
                    else resources.getQuantityString(R.plurals.chat_files_more, hidden, hidden)
                rule.onNodeWithText(label).assertExists()
                if (sc.open) rule.onNodeWithText("report-${sc.count - 1}.txt").assertExists()
                else rule.onNodeWithText("report-3.txt").assertDoesNotExist()
                val layout = textLayout(label)
                assertEquals("$language $sc: fold row must stay one line", 1, layout.lineCount)
                assertFalse(layout.isLineEllipsized(0))
                val shot = rule.onNodeWithTag("shot").getBoundsInRoot()
                val row = rule.onNodeWithTag("attached-files-fold").getBoundsInRoot()
                assertTrue("$language $sc: fold row clipped horizontally", row.left >= shot.left && row.right <= shot.right)
            }
            for (s in AppStyle.entries) for (night in listOf(false, true)) {
                rule.runOnIdle { style = s; dark = night }
                rule.waitForIdle()
                if (sc.open) {
                    // Compose idleness does not finish the native click ripple; give its fade-out wall-clock time.
                    Thread.sleep(500)
                    rule.waitForIdle()
                }
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val name = "bubble-${sc.name.lowercase()}-$language-${s.name.lowercase()}-${if (night) "dark" else "light"}-${fontScale.text()}.png"
                File(out, name).outputStream().use { assertTrue(bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)) }
            }
        }
        if (language == "en") {
            assertEquals("1 more file", resources.getQuantityString(R.plurals.chat_files_more, 1, 1))
            assertEquals("2 more files", resources.getQuantityString(R.plurals.chat_files_more, 2, 2))
        }
    }

    private fun textLayout(text: String): TextLayoutResult {
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
            .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun Float.text() = if (this == 1f) "1x" else "2x"
}
