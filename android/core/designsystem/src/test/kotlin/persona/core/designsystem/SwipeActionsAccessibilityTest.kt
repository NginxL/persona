package persona.core.designsystem

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.icons.PhIcons

/**
 * The swipe component's actions for a screen reader (#4210 T1): a row inside [JSwipeActions] that applies
 * [swipeActionSemantics] takes the swipe's actions as custom accessibility actions on its own node, the one that takes the
 * focus, and runs the same functions the gesture does. A row that does not apply it, or has no swipe, takes none.
 * And the tone: Glass fills a toned action with iOS's tint, every other action and style keeps its neutral backing.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SwipeActionsAccessibilityTest {
    @get:Rule val rule = createComposeRule()

    private fun labels(tag: String): List<String> =
        rule.onNodeWithTag(tag).fetchSemanticsNode().config.getOrNull(SemanticsActions.CustomActions).orEmpty().map { it.label }

    private fun run(tag: String, label: String) {
        rule.onNodeWithTag(tag).fetchSemanticsNode().config.getOrNull(SemanticsActions.CustomActions).orEmpty()
            .single { it.label == label }.action()
        rule.waitForIdle()
    }

    @Test fun aRowTakesBothActionsOnItsOwnNodeInTheOrderTheyAreDrawn() {
        val ran = mutableListOf<String>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                JSwipeActions(start = SwipeAction("Done", PhIcons.Check, Status.Unread, { ran += "done" }),
                    end = SwipeAction("Hand off", PhIcons.Robot, Status.Working, { ran += "hand-off" })) {
                    Text("Row", Modifier.fillMaxWidth().testTag("row").swipeActionSemantics().clickable {})
                }
            }
        }
        assertEquals(listOf("Done", "Hand off"), labels("row"))
        run("row", "Hand off")
        run("row", "Done")
        assertEquals(listOf("hand-off", "done"), ran)
    }

    @Test fun anEndOnlySwipeIsOneAction() {
        var ran = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                JSwipeActions(end = SwipeAction("Done", PhIcons.Check, Status.Unread, { ran++ }, tone = SwipeTone.Positive)) {
                    Text("Row", Modifier.testTag("row").swipeActionSemantics().clickable {})
                }
            }
        }
        assertEquals(listOf("Done"), labels("row"))
        run("row", "Done")
        assertEquals(1, ran)
    }

    @Test fun theActionRunsTheLatestFunctionAndFollowsItsLabel() {
        var revision by mutableIntStateOf(0)
        val ran = mutableListOf<Int>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val now = revision
                JSwipeActions(end = SwipeAction(if (now == 0) "Done" else "Move back", PhIcons.Check, Status.Unread, { ran += now })) {
                    Text("Row", Modifier.testTag("row").swipeActionSemantics().clickable {})
                }
            }
        }
        run("row", "Done")
        revision = 1
        rule.waitForIdle()
        assertEquals(listOf("Move back"), labels("row"))
        run("row", "Move back")
        assertEquals("each runs what the row has now", listOf(0, 1), ran)
    }

    @Test fun aRowWithoutTheModifierOrWithoutASwipeTakesNone() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Column {
                    JSwipeActions(end = SwipeAction("Done", PhIcons.Check, Status.Unread, {})) {
                        Text("Plain", Modifier.testTag("plain").clickable {})
                    }
                    JSwipeActions(start = null, end = null) {
                        Text("Idle", Modifier.testTag("idle").swipeActionSemantics().clickable {})
                    }
                    Text("Outside", Modifier.testTag("outside").swipeActionSemantics().clickable {})
                }
            }
        }
        assertEquals("the row did not ask for them", emptyList<String>(), labels("plain"))
        assertEquals("no swipe, no actions", emptyList<String>(), labels("idle"))
        assertEquals("outside any swipe", emptyList<String>(), labels("outside"))
    }

    @Test fun theActionsGoWhenTheSwipeDoes() {
        var swipe by mutableIntStateOf(1)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                JSwipeActions(end = if (swipe == 1) SwipeAction("Done", PhIcons.Check, Status.Unread, {}) else null) {
                    Text("Row", Modifier.testTag("row").swipeActionSemantics().clickable {})
                }
            }
        }
        assertEquals(listOf("Done"), labels("row"))
        swipe = 0
        rule.waitForIdle()
        assertEquals(emptyList<String>(), labels("row"))
    }

    // ---- the tone ---------------------------------------------------------------------------------------------------

    private var shownStyle by mutableStateOf(AppStyle.Glass)
    private var shownDark by mutableStateOf(false)
    private var mounted = false
    private lateinit var shown: PersonaPalette

    /** The palette a style draws in light or dark, read from the theme itself. */
    private fun palette(style: AppStyle, dark: Boolean): PersonaPalette {
        if (!mounted) {
            mounted = true
            rule.setContent { PersonaTheme(darkTheme = shownDark, reduceMotion = true, style = shownStyle) { shown = PersonaTheme.palette } }
        }
        shownStyle = style
        shownDark = dark
        rule.waitForIdle()
        return shown
    }

    private val toned = SwipeAction("Done", PhIcons.Check, Status.Unread, {}, tone = SwipeTone.Positive)
    private val back = SwipeAction("Move Back", PhIcons.ArrowUUpLeft, Status.Unread, {}, tone = SwipeTone.Accent)

    @Test fun glassFillsAToneWithIosTint() {
        val glass = palette(AppStyle.Glass, dark = false)
        val done = glass.swipeColors(toned, glass = true)
        assertEquals(glass.positive, done.fill)
        assertEquals("white on the green, as iOS, where it reads", glass.onAccent, done.content)
        assertTrue(done.tinted)
        val move = glass.swipeColors(back, glass = true)
        assertEquals(glass.accent, move.fill)
        assertEquals("white on the accent, as iOS", glass.onAccent, move.content)
        assertTrue(move.tinted)
        assertNotEquals(done.fill, move.fill)
    }

    @Test fun glassInTheDarkFillsTheSameWayWithItsOwnTints() {
        val glass = palette(AppStyle.Glass, dark = true)
        val done = glass.swipeColors(toned, glass = true)
        assertEquals(glass.positive, done.fill)
        // White on the bright dark green would be 2:1; the palette's words on a positive fill read there.
        assertTrue(ExtensionThemeColors.contrast(glass.onAccent, glass.positive) < 3.5)
        assertEquals(glass.positiveRole!!.onDot, done.content)
        assertTrue(ExtensionThemeColors.contrast(done.content, done.fill) >= 4.5)
        assertEquals(glass.accent, glass.swipeColors(back, glass = true).fill)
        assertEquals(glass.onAccent, glass.swipeColors(back, glass = true).content)
    }

    @Test fun anActionWithoutAToneADestructiveOneAndEveryOtherStyleKeepTheNeutralBacking() {
        val plain = SwipeAction("Archive", PhIcons.Check, Status.Working, {})
        val close = SwipeAction("Close", PhIcons.X, Status.NeedsYou, {}, destructive = true, tone = SwipeTone.Positive)
        for (style in AppStyle.entries) for (dark in listOf(false, true)) {
            val p = palette(style, dark)
            val glass = style == AppStyle.Glass
            val plainColors = p.swipeColors(plain, glass)
            assertEquals("$style: backing", p.statusSurface, plainColors.fill)
            assertEquals("$style: its status colour", p.status(Status.Working).text, plainColors.content)
            assertTrue(!plainColors.tinted)
            val closeColors = p.swipeColors(close, glass)
            assertEquals("$style: a destructive action is the danger colour whatever it asks", p.danger.text, closeColors.content)
            assertEquals(p.statusSurface, closeColors.fill)
            if (!glass) {
                val colors = p.swipeColors(toned, glass = false)
                assertEquals("$style keeps its look: backing", p.statusSurface, colors.fill)
                assertEquals("$style keeps its look: the status colour", p.status(Status.Unread).text, colors.content)
                assertTrue(!colors.tinted)
            }
        }
    }
}
