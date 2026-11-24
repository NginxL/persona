package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.toSize
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import kotlin.math.ceil

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h400dp-xhdpi")
class AgentFilterCapsuleTest {
    @get:Rule val rule = createComposeRule()

    /** The Agents page's seven filters in each language, worded as the page words them (the first is All). */
    private val english = listOf("All", "Needs you", "Unread", "Working", "Waiting in the background", "Lark groups", "Terminal")
    private val chinese = listOf("全部", "需要你", "完成未看", "工作中", "后台等待中", "飞书群", "终端")
    private val japanese = listOf("すべて", "要対応", "未確認", "作業中", "バックグラウンドで待機中", "Lark グループ", "ターミナル")
    private val actions = listOf(JAction("By status", checked = true, onClick = {}))

    private var selected by mutableIntStateOf(0)

    /** Every choice but the first carries a count and a state's dot, as on the page; a count is its position. */
    private fun bays(labels: List<String>) = labels.mapIndexed { index, label ->
        Bay(label, index + 1, status = if (index == 0) null else Status.Working)
    }

    private fun show(
        labels: List<String>, width: Dp = 240.dp, initial: Int = 0, reduceMotion: Boolean = true,
        fontScale: Float = 1f, style: AppStyle = AppStyle.Glass,
    ) {
        selected = initial
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(style = style, reduceMotion = reduceMotion) {
                    Box(Modifier.width(width)) {
                        JAgentFilterCapsule(bays(labels), selected, { selected = it }, "Group by: By status", actions)
                    }
                }
            }
        }
    }

    /** Where a node lies and how large it is laid out, not clipped to the strip that scrolls it. */
    private fun bounds(tag: String): Rect = rule.onNodeWithTag(tag).fetchSemanticsNode().let { Rect(it.positionInRoot, it.size.toSize()) }
    private fun scrollTo(index: Int) {
        rule.onNodeWithTag("agents-status-scroll").performScrollToIndex(index)
        rule.waitForIdle()
    }
    private fun fades(): AgentFilterFades = rule.onNodeWithTag("agents-status-scroll").fetchSemanticsNode().config[AgentFilterFade]

    private fun layoutOf(node: SemanticsNodeInteraction): TextLayoutResult {
        val results = mutableListOf<TextLayoutResult>()
        rule.runOnUiThread { node.fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action!!(results) }
        return results.single()
    }

    /** Scrolls to each choice in a strip too narrow for all of them, and reads the label that was laid out. */
    private fun assertEveryLabelWhole(labels: List<String>, fontScale: Float = 1f) {
        show(labels, fontScale = fontScale)
        labels.forEachIndexed { index, label ->
            scrollTo(index)
            val text = layoutOf(rule.onNode(hasText(label) and hasAnyAncestor(hasTestTag("agents-status-$index")), useUnmergedTree = true))
            assertEquals("\"$label\" is drawn as it is worded", label, text.layoutInput.text.text)
            assertEquals("\"$label\" is on one line", 1, text.lineCount)
            assertFalse("\"$label\" is not cut with an ellipsis", text.isLineEllipsized(0))
            // Laid out as wide as the words want, not squeezed into what is left (a paragraph with no width limit says it overflows).
            assertTrue("\"$label\" is not clipped by its width", text.size.width >= ceil(text.multiParagraph.maxIntrinsicWidth).toInt())
            assertTrue("the chip holds \"$label\" whole", bounds("agents-status-$index").width >= text.size.width)
            // Spoken in full too, with its count (Glass leaves All's count out).
            rule.onNodeWithTag("agents-status-$index").assertContentDescriptionEquals(if (index == 0) label else "$label ${index + 1}")
        }
    }

    @Test fun noEnglishLabelIsShortened() = assertEveryLabelWhole(english)
    @Test fun noSimplifiedChineseLabelIsShortened() = assertEveryLabelWhole(chinese)
    @Test fun noJapaneseLabelIsShortened() = assertEveryLabelWhole(japanese)
    @Test fun noEnglishLabelIsShortenedAtTheLargestTextSize() = assertEveryLabelWhole(english, fontScale = 2f)
    @Test fun noSimplifiedChineseLabelIsShortenedAtTheLargestTextSize() = assertEveryLabelWhole(chinese, fontScale = 2f)
    @Test fun noJapaneseLabelIsShortenedAtTheLargestTextSize() = assertEveryLabelWhole(japanese, fontScale = 2f)
    @Test fun noLabelIsShortenedOnTheOtherStyles() {
        show(chinese, style = AppStyle.Brief)
        scrollTo(chinese.lastIndex)
        rule.onNodeWithTag("agents-status-${chinese.lastIndex}").assertContentDescriptionEquals("终端 ${chinese.size}")
        assertFalse(layoutOf(rule.onNode(hasText("终端") and hasAnyAncestor(hasTestTag("agents-status-${chinese.lastIndex}")),
            useUnmergedTree = true)).isLineEllipsized(0))
    }

    @Test fun scrollingAndChoosingMoveALabelsWidthByNoMoreThanItsWeight() {
        show(chinese)
        val before = (0..1).map { bounds("agents-status-$it").width }
        scrollTo(3)
        scrollTo(0)
        assertEquals("scrolling does not change a label's width", before, (0..1).map { bounds("agents-status-$it").width })
        rule.onNodeWithTag("agents-status-1").performClick()
        rule.waitForIdle()
        // Choosing changes the label's weight (semi-bold), a pixel or two: the labels no longer open to their full width.
        val after = listOf(bounds("agents-status-0").width, bounds("agents-status-1").width)
        val tolerance = with(rule.density) { 3.dp.toPx() }
        before.zip(after).forEach { (was, now) -> assertEquals(was, now, tolerance) }
    }

    @Test fun theEdgesFadeWhereMoreIsHidden() {
        show(chinese)
        rule.waitForIdle()
        assertEquals("at its start more lies only past the end", AgentFilterFades(leading = 0f, trailing = 1f), fades())
        scrollTo(3)
        assertEquals("in the middle both edges fade", AgentFilterFades(1f, 1f), fades())
        scrollTo(chinese.lastIndex)
        assertEquals("at its end only the start fades", AgentFilterFades(1f, 0f), fades())
        scrollTo(0)
        assertEquals("back at its start", AgentFilterFades(0f, 1f), fades())
    }

    @Test fun aStripThatFitsDoesNotFade() {
        show(listOf("All", "Working"), width = 400.dp)
        rule.waitForIdle()
        assertEquals(AgentFilterFades(0f, 0f), fades())
    }

    /** The leading fade, frame by frame, while choosing the last filter carries the strip to its end. */
    private fun leadingFadeWhileTheStripMoves(reduceMotion: Boolean): List<Float> {
        rule.mainClock.autoAdvance = false
        show(chinese, reduceMotion = reduceMotion)
        rule.mainClock.advanceTimeBy(1_000)
        assertEquals("nothing has scrolled yet", 0f, fades().leading, 0f)
        rule.runOnIdle { selected = chinese.lastIndex }
        return (0 until 40).map {
            rule.mainClock.advanceTimeByFrame()
            fades().leading
        }
    }

    @Test fun theFadeEasesInWithMotion() {
        val frames = leadingFadeWhileTheStripMoves(reduceMotion = false)
        assertTrue("it passes through values between 0 and 1: $frames", frames.any { it > 0f && it < 1f })
        assertEquals(1f, frames.last(), 0f)
        assertEquals("it only grows", frames.sorted(), frames)
    }

    @Test fun theFadeSnapsWhenAnimationsAreOff() {
        val frames = leadingFadeWhileTheStripMoves(reduceMotion = true)
        assertTrue("it is 0 or 1 in every frame: $frames", frames.all { it == 0f || it == 1f })
        assertEquals(1f, frames.last(), 0f)
    }

    @Test fun theFadeDimsWhatLiesAtAnEdgeThatHasMore() {
        val surface = Color(0xFFFFFFFF)
        val ink = Color(0xFFFF0000)
        var leading by mutableFloatStateOf(1f)
        var trailing by mutableFloatStateOf(0f)
        rule.setContent {
            Box(Modifier.width(200.dp).height(40.dp).background(surface)) {
                Box(Modifier.fillMaxSize().testTag("ink").agentFilterEdgeFade(surface, 28.dp, { leading }, { trailing }).background(ink))
            }
        }
        fun pixel(x: Int): Color {
            val image = rule.onNodeWithTag("ink").captureToImage().asAndroidBitmap()
            return Color(image.getPixel(x, image.height / 2))
        }
        fun width() = rule.onNodeWithTag("ink").fetchSemanticsNode().size.width
        fun assertNear(message: String, expected: Color, actual: Color, tolerance: Float = 0.08f) {
            assertEquals("$message: green $actual", expected.green, actual.green, tolerance)
            assertEquals("$message: blue $actual", expected.blue, actual.blue, tolerance)
        }
        val far = with(rule.density) { 28.dp.toPx() }.toInt()
        // The strip has more before it only: its start dissolves into the surface, the rest is whole.
        assertNear("the very start", surface, pixel(0))
        assertEquals("halfway through the fade the ink is half there", 0.5f, 1f - pixel(far / 2).green, 0.12f)
        assertNear("past the fade", ink, pixel(far + 2))
        assertNear("the end has nothing more", ink, pixel(width() - 1))
        // Then the other way round, and then neither.
        rule.runOnIdle { leading = 0f; trailing = 1f }
        rule.waitForIdle()
        assertNear("the start has nothing more", ink, pixel(0))
        assertNear("the very end", surface, pixel(width() - 1))
        rule.runOnIdle { trailing = 0f }
        rule.waitForIdle()
        assertNear("no fade at all", ink, pixel(0))
        assertNear("no fade at all", ink, pixel(width() - 1))
    }

    @Test fun aChoiceMovesToTheMiddleOfTheStrip() {
        show(english, width = 360.dp)
        rule.runOnIdle { selected = 3 }
        rule.waitForIdle()
        assertEquals(bounds("agents-status-scroll").center.x, bounds("agents-status-3").center.x, 2f)
        rule.onNodeWithTag("agents-status-3").assertIsSelected()
    }

    @Test fun theLastChoiceEndsTheStripAtItsEnd() {
        show(english, width = 360.dp)
        rule.runOnIdle { selected = english.lastIndex }
        rule.waitForIdle()
        val inset = with(rule.density) { AgentFilterTokens.inset.toPx() }
        assertEquals(bounds("agents-status-scroll").right - inset, bounds("agents-status-${english.lastIndex}").right, 2f)
        assertEquals(AgentFilterFades(1f, 0f), fades())
    }

    private fun fadeWidth(textScale: Float) = with(rule.density) { (AgentFilterTokens.fade * textScale).toPx() }

    @Test fun aChoiceWiderThanTheStripStartsWhereTheClearMiddleBeginsSoItsFirstWordIsNotDimmed() {
        show(english, width = 240.dp, fontScale = 2f)
        rule.runOnIdle { selected = 4 }
        rule.waitForIdle()
        val strip = bounds("agents-status-scroll")
        val choice = bounds("agents-status-4")
        assertTrue("\"Waiting in the background\" must still overflow at the cap: $choice / $strip", choice.width > strip.width)
        assertEquals(strip.left + fadeWidth(InterfaceFontScaleCap), choice.left, 2f)
    }

    @Test fun aChoiceWiderThanTheClearMiddleButNotThanTheStripStartsThereToo() {
        // The strip is 240 dp, its clear middle 176 dp, and the longest label's choice about 210 dp.
        show(english, width = 289.dp)
        rule.runOnIdle { selected = 4 }
        rule.waitForIdle()
        val strip = bounds("agents-status-scroll")
        val choice = bounds("agents-status-4")
        assertTrue("$choice does not fit between the fades of $strip", choice.width > strip.width - 2 * fadeWidth(1f))
        assertTrue("$choice fits the strip $strip", choice.width < strip.width)
        assertEquals(strip.left + fadeWidth(1f), choice.left, 2f)
    }

    @Test fun aChoiceMadeBeforeTheStripWasBuiltIsBroughtIntoView() {
        show(english, initial = 5)
        rule.waitForIdle()
        val strip = bounds("agents-status-scroll")
        val choice = bounds("agents-status-5")
        assertTrue("$choice lies within $strip", choice.left >= strip.left - 1f && choice.right <= strip.right + 1f)
        rule.onNodeWithTag("agents-status-5").assertIsSelected()
    }

    @Test fun choosingWithAnimationsOffMovesTheStripAtOnce() {
        show(chinese, reduceMotion = true)
        rule.onNodeWithTag("agents-status-1").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("agents-status-1").assertIsSelected()
        assertEquals(bounds("agents-status-scroll").center.x, bounds("agents-status-1").center.x, 2f)
    }

    @Test fun allCountKeepsEachStylesExistingVisibility() {
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(style = style, reduceMotion = true) {
                Box(Modifier.width(400.dp)) {
                    JAgentFilterCapsule(listOf(Bay("All", 4), Bay("Working", 1)), 0, {},
                        "Group By: By status", listOf(JAction("By status", checked = true, onClick = {})))
                }
            }
        }
        for (option in AppStyle.entries) {
            rule.runOnIdle { style = option }
            rule.onNodeWithTag("agents-status-0").assertContentDescriptionEquals(if (option == AppStyle.Glass) "All" else "All 4")
            rule.onNodeWithTag("agents-status-0").assertIsSelected()
        }
    }
}
