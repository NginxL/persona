package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.filter
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.CoreText
import kotlin.math.abs

/**
 * A compact session row has no state line and its mark is colour only, so TalkBack hears the state from the row's
 * state description (#3902); a detailed row already says it in its text and does not say it twice.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class AgentRowSemanticsTest {
    @get:Rule val rule = createComposeRule()

    private fun textLayout(text: String): TextLayoutResult {
        val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun assertWithinRow(style: AppStyle, text: String) {
        val row = rule.onNodeWithTag("row").fetchSemanticsNode().boundsInRoot
        val bounds = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("$style: $text must be placed inside the row: row=$row, text=$bounds",
            bounds.left >= row.left && bounds.right <= row.right && bounds.top >= row.top && bounds.bottom <= row.bottom)
    }

    @Test fun compactRowSaysItsState() {
        rule.setContent {
            Column {
                for (style in AppStyle.entries) PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                    AgentRow("补齐网络层的单元测试", Status.NeedsYou, onClick = {}, Modifier.testTag("$style"), time = "2 分钟前",
                        statusText = "需要你", compact = true, note = RowNote(listOf(CoreText.plain("不应显示的工作流")), WorkflowIcon),
                        facts = listOf(RowFact("不应显示的事实")))
                }
            }
        }
        for (style in AppStyle.entries) rule.onNodeWithTag("$style")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "需要你"))
        rule.onNodeWithText("不应显示的工作流", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("不应显示的事实", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun detailedRowSaysItInItsText() {
        rule.setContent {
            Column {
                for (style in AppStyle.entries) PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                    AgentRow("补齐网络层的单元测试", Status.NeedsYou, onClick = {}, Modifier.testTag("$style"), statusText = "需要你")
                }
            }
        }
        for (style in AppStyle.entries) rule.onNodeWithTag("$style")
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.StateDescription))
    }

    /** A compact tab row says its lead agent's state; its fold toggle keeps only its own. */
    @Test fun compactTabRowSaysItsLeadsState() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                AgentTabRow("#2", "补齐网络层的单元测试", Status.NeedsYou, agents = 3, folded = true, onToggle = {}, onClick = {},
                    Modifier.testTag("tab"), statusText = "需要你", compact = true)
            }
        }
        rule.onNodeWithTag("tab").assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "需要你"))
    }

    /** Workflow and facts share one metadata line. Glass/Brief have three lines; Board has two and still speaks its state. */
    @Test fun detailedRowKeepsCriticalFactsBesideALongWorkflow() {
        val workflowName = "Background workflow with a very long name that consumes the default row width"
        val workflow = "$workflowName   Verify 2/2"
        val statusText = "工作中"
        val preview = "最近回复"
        val facts = listOf("仅查看", "结果待确认", "待发 2", "发送中", "草稿", "后台 3")
        val factsText = facts.joinToString("   ")
        var note by mutableStateOf(listOf("Background workflow", "Scan 1/2"))
        var style by mutableStateOf(AppStyle.Glass)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = style) {
                Column(Modifier.testTag("metadata-shot").width(400.dp).background(PersonaTheme.palette.background)) {
                    AgentRow("Workflow", Status.Working, onClick = {}, Modifier.testTag("row"), statusText = statusText, preview = preview,
                        note = RowNote(note.map { CoreText.plain(it) }, WorkflowIcon),
                        facts = listOf(RowFact(facts[0], caution = true), RowFact(facts[1], caution = true),
                            RowFact(facts[2], caution = true), RowFact(facts[3]), RowFact(facts[4], caution = true), RowFact(facts[5])))
                }
            }
        }
        rule.mainClock.advanceTimeByFrame()
        note = listOf(workflowName, "Verify 2/2")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()

        fun renderedPixels(text: String): Int {
            val shot = rule.onNodeWithTag("metadata-shot")
            val origin = shot.fetchSemanticsNode().boundsInRoot.topLeft
            val bounds = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val bitmap = shot.captureToImage().asAndroidBitmap()
            val background = bitmap.getPixel(0, 0)
            val left = (bounds.left - origin.x).toInt().coerceIn(0, bitmap.width)
            val right = (bounds.right - origin.x).toInt().coerceIn(left, bitmap.width)
            val top = (bounds.top - origin.y).toInt().coerceIn(0, bitmap.height)
            val bottom = (bounds.bottom - origin.y).toInt().coerceIn(top, bitmap.height)
            var drawn = 0
            for (y in top until bottom) for (x in left until right) {
                if (bitmap.getPixel(x, y) != background) drawn++
            }
            return drawn
        }

        for (nextStyle in AppStyle.entries) {
            rule.runOnIdle { style = nextStyle }
            rule.mainClock.advanceTimeByFrame()
            assertTrue("$nextStyle: the replacement Workflow must not start transparent", renderedPixels(workflow) > 20)
            assertTrue("$nextStyle: the long Workflow ellipsizes", textLayout(workflow).hasVisualOverflow)
            val noteNode = rule.onNodeWithText(workflow, useUnmergedTree = true).fetchSemanticsNode()
            val rowBounds = rule.onNodeWithTag("row").fetchSemanticsNode().boundsInRoot
            val factsNode = rule.onNodeWithText(factsText, useUnmergedTree = true).fetchSemanticsNode()
            assertFalse("$nextStyle: critical facts must not ellipsize", textLayout(factsText).hasVisualOverflow)
            // Latin and CJK fonts report different text bounds; their vertical centers must share the metadata line.
            assertTrue("$nextStyle: facts share the Workflow's metadata line: note=${noteNode.boundsInRoot}, facts=${factsNode.boundsInRoot}",
                abs(factsNode.boundsInRoot.center.y - noteNode.boundsInRoot.center.y) <= 1f)
            assertTrue("$nextStyle: facts stay inside the row", factsNode.boundsInRoot.left >= rowBounds.left &&
                factsNode.boundsInRoot.right <= rowBounds.right && factsNode.boundsInRoot.bottom <= rowBounds.bottom)
            assertTrue("$nextStyle: critical facts must be rendered, not only present in semantics", renderedPixels(factsText) > 20)
            if (nextStyle == AppStyle.Board) {
                rule.onAllNodesWithText(preview, useUnmergedTree = true)
                    .filter(SemanticsMatcher.keyIsDefined(SemanticsActions.GetTextLayoutResult)).assertCountEquals(0)
                val row = rule.onNodeWithTag("row")
                val descriptions = row.fetchSemanticsNode().config.getOrNull(SemanticsProperties.ContentDescription).orEmpty()
                assertFalse("Board preview must not override the row's text: $descriptions", preview in descriptions)
                val spokenText = row.fetchSemanticsNode().config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text }
                for (expected in listOf("Workflow", workflow, factsText, preview)) {
                    assertTrue("Board merged text must keep $expected, but was $spokenText", expected in spokenText)
                }
                row.assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, statusText))
            } else {
                val statusLine = rule.onNodeWithText("$statusText  $preview", useUnmergedTree = true).fetchSemanticsNode()
                assertTrue("$nextStyle: metadata is the third line", noteNode.boundsInRoot.top >= statusLine.boundsInRoot.bottom - 1f)
            }
        }
    }

    /** A narrow row still has one metadata line, and neither measured text can extend past its right edge. */
    @Test fun narrowRowBoundsMetadataToItsActualWidth() {
        val workflow = "Background workflow with a very long dynamic name   Verify 2/2"
        val statusText = "Working"
        val preview = "Latest reply"
        val facts = listOf("Read only", "Needs confirmation", "Queued 2", "Sending", "Draft", "Background 3")
        val factsText = facts.joinToString("   ")
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Column(Modifier.width(200.dp)) {
                    AgentRow("Session", Status.Working, onClick = {}, Modifier.testTag("row"), statusText = statusText, preview = preview,
                        note = RowNote(listOf(CoreText.plain(workflow)), WorkflowIcon), facts = facts.map(::RowFact))
                }
            }
        }

        for (nextStyle in AppStyle.entries) {
            rule.runOnIdle { style = nextStyle }
            val rowBounds = rule.onNodeWithTag("row").fetchSemanticsNode().boundsInRoot
            val visibleNoteBounds = rule.onNodeWithText(workflow, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val visibleFactsBounds = rule.onNodeWithText(factsText, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            assertTrue("$nextStyle: Workflow must have visible bounds: row=$rowBounds, note=$visibleNoteBounds, facts=$visibleFactsBounds",
                visibleNoteBounds.width > 0f && visibleNoteBounds.height > 0f)
            rule.onNodeWithText(workflow, useUnmergedTree = true).assertIsDisplayed()
            assertTrue("$nextStyle: Workflow must ellipsize", textLayout(workflow).hasVisualOverflow)
            assertTrue("$nextStyle: facts must ellipsize", textLayout(factsText).hasVisualOverflow)
            assertTrue("$nextStyle: Workflow stays on one line", textLayout(workflow).lineCount == 1)
            assertTrue("$nextStyle: facts stay on one line", textLayout(factsText).lineCount == 1)
            assertWithinRow(nextStyle, workflow)
            assertWithinRow(nextStyle, factsText)
            val noteBounds = rule.onNodeWithText(workflow, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val factsBounds = rule.onNodeWithText(factsText, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            assertTrue("$nextStyle: metadata fields share one line", abs(noteBounds.center.y - factsBounds.center.y) <= 1f)
            if (nextStyle == AppStyle.Board) {
                rule.onAllNodesWithText(preview, useUnmergedTree = true)
                    .filter(SemanticsMatcher.keyIsDefined(SemanticsActions.GetTextLayoutResult)).assertCountEquals(0)
                rule.onNodeWithTag("row").assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, statusText))
                val titleBounds = rule.onNodeWithText("Session", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                assertTrue("$nextStyle: Board metadata is its second line", noteBounds.top >= titleBounds.bottom - 1f)
            } else {
                val statusBounds = rule.onNodeWithText("$statusText  $preview", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                assertTrue("$nextStyle: metadata is the third line", noteBounds.top >= statusBounds.bottom - 1f)
            }
        }
    }

    /** Facts can leave less than the minimum Workflow width without being long enough to trigger the old slot check. */
    @Test fun workflowKeepsItsSlotAtIntermediateWidthsAndLargeText() {
        val workflow = "Background workflow"
        val factsText = "Needs confirmation"
        var style by mutableStateOf(AppStyle.Glass)
        var scale by mutableStateOf(1f)
        var rowWidth by mutableStateOf(400.dp)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                    Column(Modifier.width(rowWidth)) {
                        AgentRow("Session", Status.Working, onClick = {}, Modifier.testTag("row"),
                            note = RowNote(listOf(CoreText.plain(workflow)), WorkflowIcon), facts = listOf(RowFact(factsText)))
                    }
                }
            }
        }

        for (nextStyle in AppStyle.entries) for (nextScale in listOf(1f, 2f)) {
            rule.runOnIdle {
                style = nextStyle
                scale = nextScale
                rowWidth = 400.dp
            }
            assertFalse("$nextStyle at ${nextScale}x: facts fixture must start unellipsized", textLayout(factsText).hasVisualOverflow)
            val factsWidth = rule.onNodeWithText(factsText, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot.width
            for (extraDp in listOf(9, 19, 29, 39)) {
                rule.runOnIdle { rowWidth = with(rule.density) { factsWidth.toDp() + extraDp.dp } }
                val noteBounds = rule.onNodeWithText(workflow, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                val minimumTextWidth = with(rule.density) { Tokens.Space.l.toPx() }
                assertTrue("$nextStyle at ${nextScale}x, extra $extraDp dp: Workflow text needs 16 dp, not $noteBounds",
                    noteBounds.width + 0.5f >= minimumTextWidth)
                assertTrue("$nextStyle at ${nextScale}x, extra $extraDp dp: Workflow must ellipsize",
                    textLayout(workflow).hasVisualOverflow)
                assertWithinRow(nextStyle, workflow)
                assertWithinRow(nextStyle, factsText)
            }
        }
    }

    /** Ellipsized dynamic fields retain their complete value in semantics, including the critical facts after them. */
    @Test fun longDynamicFactsStayBoundedAndAccessible() {
        val machine = "machine-with-a-long-dynamic-name / repository-with-a-long-dynamic-name"
        val todo = "To-do: finish the accessibility audit for the dynamically named project"
        val critical = "Needs confirmation"
        val factsText = listOf(machine, todo, critical).joinToString("   ")
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Column(Modifier.width(200.dp)) {
                    AgentRow("Session", Status.NeedsYou, onClick = {}, Modifier.testTag("row"), statusText = "Needs you",
                        note = RowNote(listOf(CoreText.plain("Background workflow")), WorkflowIcon),
                        facts = listOf(RowFact(machine), RowFact(todo), RowFact(critical, caution = true)))
                }
            }
        }

        for (nextStyle in AppStyle.entries) {
            rule.runOnIdle { style = nextStyle }
            assertTrue("$nextStyle: long dynamic facts must ellipsize", textLayout(factsText).hasVisualOverflow)
            assertWithinRow(nextStyle, factsText)
            val node = rule.onNodeWithText(factsText, useUnmergedTree = true).fetchSemanticsNode()
            val accessibleText = node.config[SemanticsProperties.Text].single().text
            assertTrue("$nextStyle: the complete machine fact remains accessible", accessibleText.contains(machine))
            assertTrue("$nextStyle: the complete to-do fact remains accessible", accessibleText.contains(todo))
            assertTrue("$nextStyle: the critical fact remains accessible", accessibleText.contains(critical))
        }
    }
}
