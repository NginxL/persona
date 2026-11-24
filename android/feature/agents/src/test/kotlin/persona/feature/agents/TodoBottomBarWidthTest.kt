package persona.feature.agents

import android.content.Context
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Todo
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.time.Instant

/**
 * The to-do page's bottom bar across window widths (#9009, iOS #9008): Complete (Reopen once done) and Hand to agent stop at
 * `Tokens.Size.readable`, centered, where they had been about 370dp wide each across a tablet or an unfolded screen; on a phone,
 * and on any window narrower than the cap, they keep the width of their bar. The cap grows with the text size, so a label the
 * uncapped bar showed whole is never cut by it (Japanese "Hand to agent" is the widest). The test reads where the buttons stand,
 * the row they make, and whether a label is ellipsized, in every style, with an open to-do (two buttons) and a done one (Reopen
 * alone). Native graphics: the text is measured as drawn.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class TodoBottomBarWidthTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-16T09:30:00Z")
    private val todos = listOf(
        Todo("open", title = "Plan the release checklist", createdAt = "2026-11-16T07:30:00Z", spaces = emptyList()),
        Todo("done", title = "Write the release notes", createdAt = "2026-11-16T04:30:00Z", doneAt = "2026-11-16T09:00:00Z",
            doneBy = "user", spaces = emptyList()),
    )

    /** The most a bar's own padding takes from each side of the window it fills: Glass's floating capsule, 10 + 12 + 4 = 26dp. */
    private val barPadding = 32.dp

    /** Text sizes from the default to the largest the system offers. */
    private val textSizes = listOf(1f, 1.15f, 1.3f, 1.5f, 2f)

    /** The row the buttons make, from the window's left edge to its right edge, and the labels in it that end in an ellipsis. */
    private class ButtonRow(val left: Dp, val right: Dp, val cut: List<String>) { val width get() = right - left }

    /** Runs [check] for every style, both states of the page and each of [scales], on the window this test's qualifiers give. */
    private fun forEachBar(scales: List<Float> = listOf(1f), check: (where: String, row: ButtonRow, window: Dp, scale: Float) -> Unit) {
        var style by mutableStateOf(AppStyle.Glass)
        var id by mutableStateOf("open")
        var scale by mutableStateOf(1f)
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                    TodoDetailScreen(id, todos, emptyList(), emptyList(), null, onBack = {}, onOpenSession = {}, onHandOff = {}, now = now)
                }
            }
        }
        val context = ApplicationProvider.getApplicationContext<Context>()
        val window = rule.onRoot().getUnclippedBoundsInRoot().let { it.right - it.left }
        for (s in AppStyle.entries) for (state in listOf("open", "done")) for (size in scales) {
            rule.runOnIdle { style = s; id = state; scale = size }
            rule.waitForIdle()
            val labels = (if (state == "done") listOf(R.string.agents_todo_reopen)
            else listOf(R.string.agents_todo_complete, R.string.agents_todo_hand_to_agent)).map(context::getString)
            val buttons = labels.map { rule.onNode(hasText(it) and hasClickAction()).getUnclippedBoundsInRoot() }
            val cut = labels.filter { label ->
                val layouts = mutableListOf<TextLayoutResult>()
                val _ = rule.onNodeWithText(label, useUnmergedTree = true).fetchSemanticsNode()
                    .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
                layouts.first().isLineEllipsized(0)
            }
            check("${s.name} $state", ButtonRow(buttons.minOf { it.left }, buttons.maxOf { it.right }, cut), window, size)
        }
    }

    /** No cap in play: the row reaches the bar's padding on both sides, as it did before the cap. */
    private fun assertFillsItsBar(where: String, row: ButtonRow, window: Dp) {
        assertTrue("$where: the row starts ${row.left} from the left, more than a bar's padding", row.left <= barPadding)
        assertTrue("$where: the row ends ${window - row.right} from the right, more than a bar's padding", window - row.right <= barPadding)
        assertEquals("$where: centered", row.left.value, (window - row.right).value, 0.5f)
    }

    /** Capped: no wider than a readable page, the bar's padding inside it, in the middle of the window, and the labels whole. */
    private fun assertStopsAtTheReadableWidth(where: String, row: ButtonRow, window: Dp) {
        assertTrue("$where: the window is $window, not wider than the cap", window > Tokens.Size.readable + barPadding * 2)
        assertTrue("$where: the row is ${row.width} wide, more than ${Tokens.Size.readable}", row.width <= Tokens.Size.readable)
        assertTrue("$where: the row is ${row.width} wide, much less than the cap", row.width >= Tokens.Size.readable - barPadding * 2)
        assertEquals("$where: centered", row.left.value, (window - row.right).value, 0.5f)
        assertTrue("$where: cut short: ${row.cut}", row.cut.isEmpty())
    }

    /** Larger text widens the cap with it, to the page frame's own limit; no label that fits the uncapped bar is cut. */
    private fun assertLabelsStayWhole(where: String, row: ButtonRow, scale: Float) {
        val cap = Tokens.Size.readable * scale.coerceAtLeast(1f)
        assertTrue("$where at $scale: the row is ${row.width} wide, more than the cap $cap", row.width <= cap)
        assertTrue("$where at $scale: cut short: ${row.cut}", row.cut.isEmpty())
    }

    @Test fun aPhoneKeepsTheWidthOfItsBar() = forEachBar { where, row, window, _ -> assertFillsItsBar(where, row, window) }

    @Test @Config(qualifiers = "en-w520dp-h900dp-xhdpi")
    fun aWindowNarrowerThanTheCapIsNotCapped() = forEachBar { where, row, window, _ -> assertFillsItsBar(where, row, window) }

    @Test @Config(qualifiers = "en-w840dp-h700dp-xhdpi")
    fun anUnfoldedScreenStopsAtTheReadableWidth() = forEachBar { where, row, window, _ -> assertStopsAtTheReadableWidth(where, row, window) }

    @Test @Config(qualifiers = "en-w1280dp-h800dp-xhdpi")
    fun aTabletStopsAtTheReadableWidth() = forEachBar { where, row, window, _ -> assertStopsAtTheReadableWidth(where, row, window) }

    /** As iOS's row on an iPhone turned on its side: the cap is the window's width, not the device. */
    @Test @Config(qualifiers = "en-w915dp-h412dp-xhdpi")
    fun aPhoneOnItsSideStopsAtTheReadableWidth() = forEachBar { where, row, window, _ -> assertStopsAtTheReadableWidth(where, row, window) }

    // On an unfolded screen, from the default text size to the largest, in each language the app ships.
    @Test @Config(qualifiers = "en-w840dp-h700dp-xhdpi")
    fun englishLabelsStayWholeAsTheTextGrows() = forEachBar(textSizes) { where, row, _, scale -> assertLabelsStayWhole(where, row, scale) }

    @Test @Config(qualifiers = "zh-w840dp-h700dp-xhdpi")
    fun chineseLabelsStayWholeAsTheTextGrows() = forEachBar(textSizes) { where, row, _, scale -> assertLabelsStayWhole(where, row, scale) }

    @Test @Config(qualifiers = "ja-w840dp-h700dp-xhdpi")
    fun japaneseLabelsStayWholeAsTheTextGrows() = forEachBar(textSizes) { where, row, _, scale -> assertLabelsStayWhole(where, row, scale) }
}
