package persona.feature.chat

import android.graphics.Bitmap
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
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.width
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.designsystem.AppStyle
import persona.core.designsystem.InterfaceFontScaleCap
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

/**
 * A diff's sign column as on iOS (#4209): `Tokens.Size.dotXL` wide, growing to the interface cap, then `Tokens.Space.sm` before the
 * code. Glass, light and dark, at the default and the 2.0 system maximum. PNGs in `build/diff-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class DiffViewShotsTest {
    @get:Rule val rule = createComposeRule()

    private val diff = ToolDiff(listOf(
        DiffLine("@@ -1,4 +1,5 @@", '@', null, null),
        DiffLine("fun greet(name: String) {", ' ', 1, 1),
        DiffLine("    println(\"Hi \$name\")", '-', 2, null),
        DiffLine("    println(\"Hello, \$name\")", '+', null, 2),
        DiffLine("    log(name)", '+', null, 3),
        DiffLine("}", ' ', 3, 4),
    ))

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/diff-shots").also { it.mkdirs() }

    @Test fun signColumnFollowsTheInterfaceCap() {
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        DiffView(diff, "d1")
                    }
                }
            }
        }
        for (scale in listOf(1f, 2f)) for (d in listOf(false, true)) {
            fontScale = scale; dark = d
            rule.waitForIdle()
            // The sign's interface column grows only to the cap; the sm after it is padding outside the text's bounds.
            val column = rule.onAllNodesWithText("+")[0].getBoundsInRoot().width
            assertEquals((Tokens.Size.dotXL * minOf(scale, InterfaceFontScaleCap)).value, column.value, 0.5f)
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "diff-glass-${if (d) "dark" else "light"}-x${scale.toInt()}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    /**
     * An edit with a replaced word and a long unchanged stretch around it: the tint of the added and removed lines, the
     * deeper tint of the words, and the fold row, in each style, light and dark, at the default and the largest font.
     */
    @Test fun changedWordsAndFoldedContextInEveryStyle() {
        val old = (1..24).joinToString("\n") { "    val item$it = load(\"part-$it\")" }
        val edited = toolDiff(Item("call", kind = "tool_use", toolName = "Edit",
            toolInput = buildJsonObject {
                put("old_string", old)
                put("new_string", old.replace("load(\"part-12\")", "fetch(\"part-12\", retries = 2)"))
            }))!!
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        DiffView(edited, "words")
                    }
                }
            }
        }
        for (st in AppStyle.entries) for (scale in listOf(1f, 2f)) for (d in listOf(false, true)) {
            style = st; fontScale = scale; dark = d
            rule.waitForIdle()
            rule.onAllNodesWithText("unchanged", substring = true)[0].assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "diff-words-${st.key}-${if (d) "dark" else "light"}-x${scale.toInt()}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
