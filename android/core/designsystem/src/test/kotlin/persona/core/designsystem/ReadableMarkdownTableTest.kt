package persona.core.designsystem

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.model.parseMarkdown
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-mdpi")
class ReadableMarkdownTableTest {
    @get:Rule val rule = createComposeRule()

    @Test fun wideTableHasARealScrollRangeAndRowsStayInReadingOrder() {
        show(wide)
        val scroll = rule.onAllNodes(horizontal, useUnmergedTree = true).fetchSemanticsNodes().single()
        val range = scroll.config[SemanticsProperties.HorizontalScrollAxisRange]
        assertTrue("content is wider than the viewport", range.maxValue() > 0)
        rule.onAllNodes(horizontal, useUnmergedTree = true)[0]
            .performSemanticsAction(SemanticsActions.ScrollBy) { val _ = it(120f, 0f) }
        rule.waitForIdle()
        assertTrue("the table scrolls", range.value() > 0)
        assertEquals(listOf("Area", "Finding", "中文说明", "Next step", "Network", "A complete English sentence that wraps.",
            "一句需要合适列宽才能阅读的中文。", "Retry", "Layout", "Another complete sentence that wraps.", "第二句中文内容。", "Read"),
            cellTexts())

        val table = rule.onAllNodes(collection, useUnmergedTree = true).fetchSemanticsNodes().single()
        val collectionInfo = table.config[SemanticsProperties.CollectionInfo]
        assertEquals(3, collectionInfo.rowCount)
        assertEquals(4, collectionInfo.columnCount)
        val cells = rule.onAllNodes(cell, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals(12, cells.size)
        val positions = cells.map { it.config[SemanticsProperties.CollectionItemInfo] }
        assertEquals(List(3) { row -> List(4) { column -> row to column } }.flatten(),
            positions.sortedWith(compareBy({ it.rowIndex }, { it.columnIndex })).map { it.rowIndex to it.columnIndex })
        cells.forEach { node ->
            val item = node.config[SemanticsProperties.CollectionItemInfo]
            assertEquals("heading at row ${item.rowIndex}, column ${item.columnIndex}", item.rowIndex == 0,
                node.config.getOrNull(SemanticsProperties.Heading) != null)
        }
    }

    @Test fun fittingTableHasNoScrollRangeAndKeepsCellSemantics() {
        show(small)
        val ranges = rule.onAllNodes(horizontal, useUnmergedTree = true).fetchSemanticsNodes()
        assertTrue("a fitting table has no horizontal scroll container", ranges.isEmpty())
        assertEquals(listOf("Name", "State", "Build", "Passed", "Tests", "Ready"), cellTexts())
    }

    @Test fun fittingTableAtLargeTextScaleStillUsesTheUnchangedLegacyRenderer() {
        show(small, fontScale = 2f)
        val ranges = rule.onAllNodes(horizontal, useUnmergedTree = true).fetchSemanticsNodes()
        assertTrue("the scaled minimum must not make a fitting table scroll", ranges.isEmpty())
        assertEquals(listOf("Name", "State", "Build", "Passed", "Tests", "Ready"), cellTexts())
    }

    @Test fun anInlineImageDoesNotRestoreOneLineEllipsizedColumns() {
        showComparison(wideWithImage)
        val cells = rule.onAllNodesWithText("A complete English sentence", substring = true, useUnmergedTree = true)
            .fetchSemanticsNodes()
        assertEquals(2, cells.size)
        val readable = cells[0].boundsInRoot.height
        val legacy = cells[1].boundsInRoot.height
        assertTrue("the inline-content table wraps ($readable) instead of staying one line ($legacy)", readable > legacy)
    }

    @Test fun anImageOnlyCellKeepsTheFittingTableOnTheLegacyPath() {
        show(imageOnly)
        val cell = rule.onAllNodesWithText("Ready", substring = true, useUnmergedTree = true).fetchSemanticsNodes().single()
        val width = generateSequence(cell.parent) { it.parent }
            .first { it.config.getOrNull(SemanticsProperties.CollectionItemInfo) != null }.boundsInRoot.width
        assertEquals("upstream's 180 dp equal share minus two 16 dp cell paddings", 148f, width)
    }

    @Test fun shortBodyCellsStartAtTheTopOfWrappedRows() {
        show(wide)
        val short = rule.onAllNodesWithText("Network", substring = true, useUnmergedTree = true).fetchSemanticsNodes().single()
        val wrapped = rule.onAllNodesWithText("A complete English sentence that wraps.", substring = true, useUnmergedTree = true)
            .fetchSemanticsNodes().single()
        assertEquals("a short cell starts with its wrapped neighbor", wrapped.boundsInRoot.top, short.boundsInRoot.top, 1f)
    }

    private fun showComparison(markdown: String) {
        val state = parseMarkdown(markdown)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Column(Modifier.width(360.dp)) {
                    Markdown(state = state, components = markdownComponents(table = { model ->
                        ReadableMarkdownTable(model.content, model.node, model.typography.table)
                    }))
                    Markdown(state = state, components = markdownComponents(table = { model ->
                        ReadableMarkdownTable(model.content, model.node, model.typography.table, enabled = false)
                    }))
                }
            }
        }
        rule.waitForIdle()
    }

    private fun show(markdown: String, fontScale: Float = 1f) {
        val state = parseMarkdown(markdown)
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, fontScale)) {
                PersonaTheme(reduceMotion = true) {
                    Column(Modifier.width(360.dp)) {
                        Markdown(state = state, components = markdownComponents(table = { model ->
                            ReadableMarkdownTable(model.content, model.node, model.typography.table)
                        }))
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun cellTexts(): List<String> = rule.onAllNodes(text, useUnmergedTree = true).fetchSemanticsNodes()
        .flatMap { it.config[SemanticsProperties.Text] }.map { it.text.trim() }

    private companion object {
        val horizontal = SemanticsMatcher.keyIsDefined(SemanticsProperties.HorizontalScrollAxisRange)
        val collection = SemanticsMatcher.keyIsDefined(SemanticsProperties.CollectionInfo)
        val cell = SemanticsMatcher.keyIsDefined(SemanticsProperties.CollectionItemInfo)
        val text = SemanticsMatcher.keyIsDefined(SemanticsProperties.Text)
        val wide = """
            | Area | Finding | 中文说明 | Next step |
            | --- | --- | --- | --- |
            | Network | A complete English sentence that wraps. | 一句需要合适列宽才能阅读的中文。 | Retry |
            | Layout | Another complete sentence that wraps. | 第二句中文内容。 | Read |
        """.trimIndent()
        val small = """
            | Name | State |
            | --- | --- |
            | Build | Passed |
            | Tests | Ready |
        """.trimIndent()
        val wideWithImage = wide.replace("| Network |", "| Network ![status](status.png) |")
        val imageOnly = """
            | Preview | State |
            | --- | --- |
            | ![preview](https://example.com/a/very/long/image/destination/that/must/not/change/the/fitting/table.png) | Ready |
        """.trimIndent()
    }
}
