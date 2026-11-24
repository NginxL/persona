package persona.feature.inbox

import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.DpRect
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxChainLink
import persona.core.contracts.InboxEventFields
import persona.core.designsystem.AppStyle
import persona.core.designsystem.InterfaceFontScaleCap
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentPreferences
import java.time.Instant
import java.time.ZoneOffset
import kotlin.math.abs
import persona.core.inbox.*

/** Measured geometry for reported originals, including the 200% text new-line contract. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2600dp-xhdpi")
class InboxReportedLargeTextLayoutTest {
    @get:Rule val rule = createComposeRule()

    private val clock = Instant.parse("2026-01-01T12:00:00Z")

    private fun showEntry(
        style: AppStyle,
        fontScale: Float,
        go: (ChainTarget) -> Unit = {},
        entry: () -> InboxChainEntry,
    ) {
        rule.setContent {
            val record = InboxRecord(
                InboxItem("reported-large-text-layout"),
                event = InboxEventFields(chain = listOf(entry())),
            )
            CompositionLocalProvider(
                LocalDensity provides Density(LocalDensity.current.density, fontScale),
                LocalInboxClock provides { clock },
                LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(style = style, darkTheme = false, reduceMotion = true) {
                    LazyColumn(Modifier.width(400.dp).height(2400.dp)) {
                        eventChain(record, emptyList(), AgentPreferences(), go)
                    }
                }
            }
        }
    }

    private fun entry(
        id: String = "reported-large-text",
        text: String = "Unused legacy wording",
        detail: String? = ORIGINAL,
        kind: String = "reported",
        link: InboxChainLink? = null,
    ) = InboxChainEntry(
        id = id,
        at = "2026-01-01T11:02:00Z",
        kind = kind,
        text = text,
        detail = detail,
        link = link,
    )

    private fun row(text: String) = rule.onNode(
        hasClickAction() and hasAnyDescendant(hasText(text)),
        useUnmergedTree = true,
    )

    private fun layout(text: String): TextLayoutResult {
        val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("The original must occur in exactly one exact-text node", 1, nodes.size)
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = nodes.single().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun bounds(text: String): DpRect =
        rule.onNodeWithText(text, useUnmergedTree = true).getUnclippedBoundsInRoot()

    private fun assertCollapsed(text: String = ORIGINAL) {
        val textLayout = layout(text)
        assertEquals("The collapsed Text must retain the exact original", text, textLayout.layoutInput.text.text)
        assertEquals("A collapsed reported original is a two-line preview", 2, textLayout.lineCount)
        rule.onAllNodesWithText(text, useUnmergedTree = true).assertCountEquals(1)
    }

    private fun assertExpanded(text: String = ORIGINAL) {
        val textLayout = layout(text)
        assertEquals("The expanded Text must retain the exact original", text, textLayout.layoutInput.text.text)
        assertTrue(
            "Every explicit source line must be laid out: ${textLayout.lineCount} / ${text.lines().size}",
            textLayout.lineCount >= text.lines().size,
        )
        val finalGlyph = textLayout.getBoundingBox(text.lastIndex)
        assertTrue("The final glyph must be nonzero: $finalGlyph", finalGlyph.width > 0f && finalGlyph.height > 0f)
        assertTrue(
            "The final glyph must lie inside the original Text: $finalGlyph / ${textLayout.size}",
            finalGlyph.left >= 0f && finalGlyph.top >= 0f &&
                finalGlyph.right <= textLayout.size.width && finalGlyph.bottom <= textLayout.size.height,
        )
        rule.onAllNodesWithText(text, useUnmergedTree = true).assertCountEquals(1)
    }

    private fun assertInlineLayout(scale: Float) {
        val body = bounds(ORIGINAL)
        val time = bounds(TIME)
        val tag = bounds(ASSISTANT_SAYS)
        val rowBounds = row(ORIGINAL).getBoundsInRoot()
        val contentRight = rowBounds.right - (time.left - rowBounds.left)

        assertTrue(
            "At $scale the original must remain to the right of the time and Assistant says header: " +
                "body=$body, time=$time, tag=$tag",
            body.left > tag.right,
        )
        assertTrue(
            "At $scale the original and header must overlap vertically on the same row: body=$body, tag=$tag",
            body.top < tag.bottom && body.bottom > tag.top,
        )
        assertTrue(
            "At $scale the original must remain inside the row's content bounds: body=$body, right=$contentRight",
            body.right <= contentRight + TOLERANCE_DP,
        )
    }

    private fun expandedGeometryCheck(): Pair<List<String>, String> {
        val body = bounds(ORIGINAL)
        val time = bounds(TIME)
        val tag = bounds(ASSISTANT_SAYS)
        val rowBounds = row(ORIGINAL).getBoundsInRoot()
        val contentLeft = time.left
        val contentRight = rowBounds.right - (time.left - rowBounds.left)
        val availableWidth = contentRight - contentLeft
        val bodyWidth = body.right - body.left
        val headerBottom = maxOf(time.bottom, tag.bottom)
        val problems = mutableListOf<String>()

        if (body.top + TOLERANCE_DP < headerBottom) {
            problems += "expanded body must start on a new line below the header: " +
                "body.top=${body.top}, header.bottom=$headerBottom"
        }
        if (abs((body.left - contentLeft).value) > TOLERANCE_DP.value) {
            problems += "expanded body must start at the content's left edge, not in the right-hand column: " +
                "body.left=${body.left}, content.left=$contentLeft"
        }
        if (abs((bodyWidth - availableWidth).value) > TOLERANCE_DP.value) {
            problems += "expanded body must occupy the full available content width: " +
                "body.width=$bodyWidth, available.width=$availableWidth"
        }
        if (abs((body.right - contentRight).value) > TOLERANCE_DP.value) {
            problems += "expanded body must reach the content's right edge: " +
                "body.right=${body.right}, content.right=$contentRight"
        }
        val evidence = "measured bounds: body=$body, time=$time, tag=$tag, row=$rowBounds, " +
            "content=[$contentLeft, $contentRight]"
        return problems to evidence
    }

    private fun assertLargeTextLayoutAndRoundTrip(style: AppStyle) {
        showEntry(style, fontScale = 2f) { entry() }

        assertCollapsed()
        row(ORIGINAL).performClick()
        assertExpanded()
        val (geometryProblems, evidence) = expandedGeometryCheck()

        row(ORIGINAL).performClick()
        assertCollapsed()
        assertTrue(
            "$style at 200% text:\n${geometryProblems.joinToString("\n")}\n$evidence",
            geometryProblems.isEmpty(),
        )
    }

    private fun assertDefaultTextLayoutAndRoundTrip(style: AppStyle) {
        showEntry(style, fontScale = 1f) { entry() }

        assertCollapsed()
        assertInlineLayout(1f)
        row(ORIGINAL).performClick()
        assertExpanded()
        assertInlineLayout(1f)
        row(ORIGINAL).performClick()
        assertCollapsed()
        assertInlineLayout(1f)
    }

    @Test
    fun expandedOriginalAt200UsesNewFullWidthLineGlass() =
        assertLargeTextLayoutAndRoundTrip(AppStyle.Glass)

    @Test
    fun expandedOriginalAt200UsesNewFullWidthLineBrief() =
        assertLargeTextLayoutAndRoundTrip(AppStyle.Brief)

    @Test
    fun expandedOriginalAt200UsesNewFullWidthLineBoard() =
        assertLargeTextLayoutAndRoundTrip(AppStyle.Board)

    @Test
    fun expandedOriginalAt100KeepsCurrentInlineLayoutGlass() =
        assertDefaultTextLayoutAndRoundTrip(AppStyle.Glass)

    @Test
    fun expandedOriginalAt100KeepsCurrentInlineLayoutBrief() =
        assertDefaultTextLayoutAndRoundTrip(AppStyle.Brief)

    @Test
    fun expandedOriginalAt100KeepsCurrentInlineLayoutBoard() =
        assertDefaultTextLayoutAndRoundTrip(AppStyle.Board)

    @Test
    fun expandedOriginalAtTheInterfaceCapKeepsCurrentInlineLayout() {
        showEntry(AppStyle.Glass, fontScale = InterfaceFontScaleCap) { entry() }
        assertCollapsed()
        row(ORIGINAL).performClick()
        assertExpanded()
        assertInlineLayout(InterfaceFontScaleCap)
        row(ORIGINAL).performClick()
        assertCollapsed()
    }

    @Test
    fun nonReportedDetailKeepsSeparateLowerExpansionAt200() {
        val summary = "Short conclusion"
        val detail = "Separate lower detail"
        showEntry(AppStyle.Glass, fontScale = 2f) {
            entry(id = "non-reported-large-text", text = summary, detail = detail, kind = "concluded")
        }

        rule.onNodeWithText(detail, useUnmergedTree = true).assertDoesNotExist()
        row(summary).performClick()
        assertEquals("The lower detail must occur once", 1,
            rule.onAllNodesWithText(detail, useUnmergedTree = true).fetchSemanticsNodes().size)
        assertTrue(
            "The non-reported detail must remain below its summary: detail=${bounds(detail)}, summary=${bounds(summary)}",
            bounds(detail).top >= bounds(summary).bottom,
        )

        row(summary).performClick()
        rule.onNodeWithText(detail, useUnmergedTree = true).assertDoesNotExist()
    }

    @Test
    fun linkedReportedRowStillNavigatesWithoutExpandingAt200() {
        val opened = mutableListOf<ChainTarget>()
        showEntry(
            AppStyle.Glass,
            fontScale = 2f,
            entry = {
                entry(
                    id = "linked-reported-large-text",
                    link = InboxChainLink(type = "item", id = "linked-item"),
                )
            },
            go = { opened += it },
        )

        assertCollapsed()
        row(ORIGINAL).performClick()
        assertEquals(listOf(ChainTarget.Item("linked-item")), opened)
        assertCollapsed()
    }

    @Test
    fun sameIdUpdateFromExpandedLongOriginalToShortTextRemovesControlAt200() {
        val original = mutableStateOf(ORIGINAL)
        showEntry(AppStyle.Glass, fontScale = 2f) {
            entry(id = "stable-reported-id", detail = original.value)
        }

        assertCollapsed()
        row(ORIGINAL).performClick()
        assertExpanded()
        rule.runOnIdle { original.value = "OK" }
        rule.waitForIdle()

        assertEquals("The short replacement must fit on one line", 1, layout("OK").lineCount)
        row("OK").assertDoesNotExist()
        rule.onAllNodesWithText(ORIGINAL, useUnmergedTree = true).assertCountEquals(0)
    }

    @Test
    fun shortOriginalWithoutControlStaysNonClickableAt100() {
        showEntry(AppStyle.Glass, fontScale = 1f) { entry(detail = "Short") }

        assertEquals("The short original must fit on one line", 1, layout("Short").lineCount)
        row("Short").assertDoesNotExist()
    }

    private companion object {
        const val TIME = "11:02"
        const val ASSISTANT_SAYS = "Assistant says"
        val TOLERANCE_DP = 1.dp
        const val ORIGINAL =
            "The launch review is complete.\n" +
                "Keep the customer announcement.\n" +
                "Keep the owner checklist.\n" +
                "Keep the final rollout notes together.\n" +
                "Every reviewer can verify the same decisions.\n" +
                "Review the customer wording.\n" +
                "Review the owner checklist.\n" +
                "L1-END"
    }
}
