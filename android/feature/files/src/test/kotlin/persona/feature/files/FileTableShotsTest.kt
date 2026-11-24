package persona.feature.files

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** The file preview's Markdown table at 360 dp, in every style and appearance. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h1400dp-xhdpi")
class FileTableShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun wideTableInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("file").width(360.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    FileMarkdown(table, base = "", contentPadding = PaddingValues(), onOpenPath = {}, loadImage = { null })
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Network retry", substring = true).fetchSemanticsNodes().isNotEmpty() }
        val scroll = SemanticsMatcher.keyIsDefined(SemanticsProperties.HorizontalScrollAxisRange)
        val out = File(System.getProperty("persona.shots") ?: "build/file-table-shots").also { it.mkdirs() }
        for (current in AppStyle.entries) for (isDark in listOf(false, true)) {
            style = current
            dark = isDark
            rule.waitForIdle()
            rule.onAllNodes(scroll)[0].performSemanticsAction(SemanticsActions.ScrollBy) { val _ = it(-100_000f, 0f) }
            rule.waitForIdle()
            capture(out, "file-${current.name.lowercase()}-${if (isDark) "dark" else "light"}-start.png")
            rule.onAllNodes(scroll)[0].performSemanticsAction(SemanticsActions.ScrollBy) { val _ = it(100_000f, 0f) }
            rule.waitForIdle()
            capture(out, "file-${current.name.lowercase()}-${if (isDark) "dark" else "light"}-end.png")
        }
    }

    private fun capture(out: File, name: String) {
        val bitmap = rule.onNodeWithTag("file").captureToImage().asAndroidBitmap()
        File(out, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private companion object {
        val table = """
            | Area | Finding | 中文说明 | Next step |
            | --- | --- | --- | --- |
            | Network retry | The request waits for a connection timeout before trying the next available route. | 网络请求会先等待连接超时，然后再尝试下一条可用路线，因此整个回复需要更长时间。 | Keep the successful route visible and explain each retry. |
            | Rendering | Explanatory sentences need readable columns instead of squeezing every word into a narrow strip. | 表格中的完整句子需要合适的列宽，中文内容也不能被挤压成每行一个字。 | Scroll only the table when every column cannot fit. |
        """.trimIndent()
    }
}
