package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** Markdown table captures at a phone's 360 dp width. PNGs are written to `build/chat-table-shots/`. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h2200dp-xhdpi")
class ChatTableShotsTest {
    @get:Rule val rule = createComposeRule()

    private data class Scene(val style: AppStyle, val dark: Boolean, val fontScale: Float) {
        val name = "${style.name.lowercase()}-${if (dark) "dark" else "light"}-f${fontScale.toString().replace('.', '-') }"
    }

    private data class NumberedScene(val language: String, val markdown: String, val scene: Scene) {
        val name = "$language-${scene.name}"
    }

    private fun out() = File(System.getProperty("persona.shots") ?: "build/chat-table-shots").also { it.mkdirs() }

    @Test fun incomingTablesInEveryStyleAndAtLargeTextSizes() {
        var scene by mutableStateOf(Scene(AppStyle.Glass, dark = false, fontScale = 1f))
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, scene.fontScale)) {
                PersonaTheme(style = scene.style, darkTheme = scene.dark, reduceMotion = true) {
                    Column(Modifier.width(360.dp).background(PersonaTheme.palette.background)) {
                        Box(Modifier.testTag("wide")) {
                            MessageContent(Item("wide", role = "assistant", kind = "text", text = wideTable), user = false)
                        }
                        Box(Modifier.testTag("small")) {
                            MessageContent(Item("small", role = "assistant", kind = "text", text = smallTable), user = false)
                        }
                    }
                }
            }
        }
        rule.waitUntil(10_000) {
            rule.onAllNodes(horizontalScroll).fetchSemanticsNodes().singleOrNull()?.let { node ->
                node.config.getOrNull(SemanticsProperties.HorizontalScrollAxisRange)?.maxValue()?.let { it > 0f }
            } == true
        }
        val scenes = AppStyle.entries.flatMap { style ->
            listOf(false, true).map { dark -> Scene(style, dark, 1f) }
        } + listOf(1.3f, 2f).flatMap { scale ->
            listOf(false, true).map { dark -> Scene(AppStyle.Glass, dark, scale) }
        }
        for (current in scenes) {
            scene = current
            rule.waitForIdle()
            scrollWideToStart()
            capture("wide", "wide-${current.name}-start.png")
            if (current.fontScale == 1f) capture("small", "small-${current.name}.png")
            scrollWideToEnd()
            capture("wide", "wide-${current.name}-end.png")
        }
    }

    @Test fun numberedTablesKeepTheIndexAndItemColumnsCompact() {
        val scenes = listOf("en" to numberedEnglish, "zh" to numberedChinese).flatMap { (language, markdown) ->
            (listOf(false, true).flatMap { dark ->
                listOf(1f, 2f).map { scale -> Scene(AppStyle.Glass, dark, scale) }
            } + listOf(Scene(AppStyle.Brief, dark = false, fontScale = 1f),
                Scene(AppStyle.Board, dark = true, fontScale = 1f)))
                .map { NumberedScene(language, markdown, it) }
        }
        var current by mutableStateOf(scenes.first())
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, current.scene.fontScale)) {
                PersonaTheme(style = current.scene.style, darkTheme = current.scene.dark, reduceMotion = true) {
                    Box(Modifier.testTag("numbered").width(360.dp).background(PersonaTheme.palette.background)) {
                        MessageContent(Item("numbered-${current.language}", role = "assistant", kind = "text",
                            text = current.markdown), user = false)
                    }
                }
            }
        }
        for (scene in scenes) {
            current = scene
            waitForNumbered(scene)
            if (scene.scene.fontScale == 1f) {
                rule.onAllNodes(horizontalScroll).assertCountEquals(0)
                capture("numbered", "numbered-${scene.name}.png")
            } else {
                rule.waitUntil(10_000) {
                    rule.onAllNodes(horizontalScroll).fetchSemanticsNodes().singleOrNull()?.let { node ->
                        node.config.getOrNull(SemanticsProperties.HorizontalScrollAxisRange)?.maxValue()?.let { it > 0f }
                    } == true
                }
                scrollWideToStart()
                capture("numbered", "numbered-${scene.name}-start.png")
                scrollWideToEnd()
                capture("numbered", "numbered-${scene.name}-end.png")
            }
        }
    }

    @Test fun ownBubbleKeepsItsCurrentPlainTextMarkdownControl() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("own").width(360.dp).background(PersonaTheme.palette.background)) {
                    MergedUserBubble(listOf(Item("own", role = "user", kind = "text", text = wideTable, clientMsgId = "own")))
                }
            }
        }
        for (current in AppStyle.entries) for (isDark in listOf(false, true)) {
            style = current
            dark = isDark
            rule.waitForIdle()
            capture("own", "own-${current.name.lowercase()}-${if (isDark) "dark" else "light"}.png")
        }
    }

    private fun waitForNumbered(scene: NumberedScene) {
        val firstItem = if (scene.language == "en") "Alpha" else "阿尔法"
        rule.waitUntil(10_000) {
            rule.onAllNodesWithText(firstItem, substring = true).fetchSemanticsNodes().isNotEmpty() &&
                rule.onAllNodes(collection, useUnmergedTree = true).fetchSemanticsNodes().any { node ->
                    node.config.getOrNull(SemanticsProperties.CollectionInfo)?.let { it.rowCount == 4 && it.columnCount == 3 } == true
                }
        }
        rule.waitForIdle()
    }

    private fun scrollWideToStart() {
        if (rule.onAllNodes(horizontalScroll).fetchSemanticsNodes().isEmpty()) return
        rule.onAllNodes(horizontalScroll)[0].performSemanticsAction(SemanticsActions.ScrollBy) { val _ = it(-100_000f, 0f) }
        rule.waitForIdle()
    }

    private fun scrollWideToEnd() {
        rule.onAllNodes(horizontalScroll)[0].performSemanticsAction(SemanticsActions.ScrollBy) { val _ = it(100_000f, 0f) }
        rule.waitForIdle()
    }

    private fun capture(tag: String, name: String) {
        val bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()
        File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private companion object {
        val horizontalScroll = SemanticsMatcher.keyIsDefined(SemanticsProperties.HorizontalScrollAxisRange)
        val collection = SemanticsMatcher.keyIsDefined(SemanticsProperties.CollectionInfo)

        val wideTable = """
            | Area | Finding | 中文说明 | Next step |
            | --- | --- | --- | --- |
            | Network retry | The request waits for a connection timeout before trying the next available route. | 网络请求会先等待连接超时，然后再尝试下一条可用路线，因此整个回复需要更长时间。 | Keep the successful route visible and explain each retry. |
            | Rendering | Explanatory sentences need readable columns instead of squeezing every word into a narrow strip. | 表格中的完整句子需要合适的列宽，中文内容也不能被挤压成每行一个字。 | Scroll only the table when every column cannot fit. |
        """.trimIndent()

        val numberedEnglish = """
            | # | Item | Notes |
            | --- | --- | --- |
            | 1 | Alpha | The first result includes a complete sentence that wraps while the number stays compact. |
            | 2 | Beta | A second sentence explains the next step without making the item column wide. |
            | 3 | Gamma | The final row keeps every short cell aligned with the top of its wrapped note. |
        """.trimIndent()

        val numberedChinese = """
            | # | 项目 | 说明 |
            | --- | --- | --- |
            | 1 | 阿尔法 | 第一条结果包含一句完整说明，长内容会换行，而序号列保持自己的窄宽度。 |
            | 2 | 贝塔 | 第二条说明解释下一步，项目名称不会因为旁边的长句子而被撑宽。 |
            | 3 | 伽马 | 最后一行的短内容会与旁边换行说明的顶部对齐，方便按行阅读。 |
        """.trimIndent()

        val smallTable = """
            | Name | State |
            | --- | --- |
            | Build | Passed |
            | Tests | Ready |
        """.trimIndent()
    }
}
