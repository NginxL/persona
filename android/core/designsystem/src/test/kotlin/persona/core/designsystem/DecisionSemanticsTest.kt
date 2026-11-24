package persona.core.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertContentDescriptionEquals
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * A "needs you" card read by TalkBack (#4326): a multi-select option is a checkbox with its tick, a single
 * choice a radio button; each option says which of how many it is; the dock's headline is a heading, its header
 * folds it, and the stack says how many wait once they scroll.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class DecisionSemanticsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun multiSelectOptionIsACheckboxWithItsTick() {
        rule.setContent {
            Column {
                for (style in AppStyle.entries) PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                    DecisionOption("单元测试", {}, Modifier.testTag("on $style"), selected = true, multiSelect = true, position = 1 to 3)
                    DecisionOption("集成测试", {}, Modifier.testTag("off $style"), multiSelect = true, position = 2 to 3)
                }
            }
        }
        for (style in AppStyle.entries) {
            rule.onNodeWithTag("on $style")
                .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Checkbox))
                .assert(SemanticsMatcher.expectValue(SemanticsProperties.ToggleableState, ToggleableState.On))
                .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
            rule.onNodeWithTag("off $style")
                .assert(SemanticsMatcher.expectValue(SemanticsProperties.ToggleableState, ToggleableState.Off))
        }
    }

    @Test fun singleChoiceSaysWhichOfHowMany() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column {
                    DecisionOption("1. Swift Testing", {}, Modifier.testTag("first"), detail = "新代码统一用", recommended = true,
                        selected = true, position = 1 to 2)
                    DecisionOption("2. XCTest", {}, Modifier.testTag("second"), position = 2 to 2)
                }
            }
        }
        rule.onNodeWithTag("first")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.RadioButton)).assertIsSelected()
            .assertContentDescriptionEquals("1. Swift Testing，新代码统一用，建议，第 1 个，共 2 个")
        rule.onNodeWithTag("second").assertContentDescriptionEquals("2. XCTest，第 2 个，共 2 个")
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.ToggleableState))
    }

    /** A card's headline is a heading; a tap on its header folds it to that line with what it asks (iOS), and opens it again. */
    @Test fun dockHeadlineIsAHeadingAndItsHeaderFoldsIt() {
        var folded by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                DecisionDock(DecisionKind.Command, "要运行 npm test", collapsed = folded, onCollapsedChange = { folded = it },
                    preview = "npm test --watch") { Text("正文") }
            }
        }
        rule.onNodeWithText("要运行 npm test").assert(SemanticsMatcher.keyIsDefined(SemanticsProperties.Heading))
        rule.onNodeWithText("正文").assertExists()
        rule.onNodeWithText("npm test --watch", substring = true).assertDoesNotExist()
        rule.onNodeWithText("要运行 npm test").performClick()
        assertTrue(folded)
        rule.onNodeWithText("正文").assertDoesNotExist()
        // one line: the title, then what it asks
        rule.onNodeWithText("要运行 npm test  npm test --watch").assert(SemanticsMatcher.keyIsDefined(SemanticsProperties.Heading))
        rule.onNodeWithContentDescription("展开").performClick()
        assertFalse(folded)
        rule.onNodeWithText("正文").assertExists()
    }

    /** Past its height the stack says how many wait, as one heading, and scrolls; while they fit it says nothing. */
    @Test fun theStackSaysHowManyOnlyOnceTheyScroll() {
        var tall by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                DecisionStack(3, 200.dp) { scrolling ->
                    Text(if (scrolling) "滚动" else "放得下", Modifier.testTag("mode"))
                    repeat(3) { Box(Modifier.fillMaxWidth().height(if (tall) 120.dp else 40.dp)) }
                }
            }
        }
        rule.onNodeWithText("3 个待处理").assertDoesNotExist()
        rule.onNodeWithText("放得下").assertExists()
        tall = true
        rule.onNodeWithText("3 个待处理").assert(SemanticsMatcher.keyIsDefined(SemanticsProperties.Heading))
        rule.onNodeWithText("滚动").assertExists()
    }

    /** A place that says its count another way (Persona's task question, "N more") passes counted = false: no "N pending" there. */
    @Test fun aPlaceThatIsNotCountedNeverSaysNPending() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                DecisionStack(1, 100.dp, counted = false) { scrolling ->
                    Text(if (scrolling) "滚动" else "放得下")
                    Box(Modifier.fillMaxWidth().height(300.dp))
                }
            }
        }
        rule.onNodeWithText("滚动").assertExists() // taller than its place: it scrolls
        rule.onNodeWithText("1 个待处理").assertDoesNotExist()
    }

    // A long option is cut to 2 lines (label) and 3 (detail) in the card above the composer, and readable in full on the
    // full page, where it has to be read before it is picked (#2707). Lines are measured with real fonts (NATIVE).
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    @Test fun aLongOptionIsCutInTheCardAndWholeOnTheFullPage() {
        val label = "Keep the existing migration and add a backfill job that rewrites every stored row in batches overnight"
        val detail = "This keeps the service online the whole time, but the job needs a lock on each batch, a retry budget, " +
            "a way to resume after a restart and a report of the rows it could not rewrite, so it takes longer to ship. " +
            "Each batch also has to be checked against the old copy before the next one starts, and the report goes to the owner."
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column {
                    for (full in listOf(false, true)) DecisionOption("$full $label", {}, Modifier.width(240.dp), detail = "$full $detail", fullText = full)
                }
            }
        }
        for (full in listOf(false, true)) {
            val cut = listOf("$full $label", "$full $detail").map { overflows(it) }
            if (full) assertFalse("cut on the full page: $cut", cut.any { it }) else assertTrue("not cut in the card: $cut", cut.all { it })
        }
    }

    private fun overflows(text: String): Boolean {
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single().hasVisualOverflow
    }
}
