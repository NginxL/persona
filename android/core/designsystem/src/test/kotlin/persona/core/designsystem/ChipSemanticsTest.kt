package persona.core.designsystem

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/** Toggle chips retain checkbox semantics; expansion entries are buttons with an expanded / collapsed state. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class ChipSemanticsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun toggleChipsKeepTheirExistingRoleAndSelection() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column {
                    JChip("Filter", Modifier.testTag("toggle"), selected = true, onClick = {})
                    JChip("Open menu", Modifier.testTag("action"), selected = true, onClick = {}, onLongClick = {}, role = Role.Button)
                }
            }
        }
        rule.onNodeWithTag("toggle")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Checkbox)).assertIsSelected()
        rule.onNodeWithTag("action")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
    }

    @Test fun glassTrayEntryIsACompactExpandableCapsuleWithFullTouchTarget() {
        var expanded by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column {
                    AssistantTrayEntry("Needs you", 3, AssistantTrayTone.Pending, expanded,
                        if (expanded) "Expanded" else "Collapsed", { expanded = !expanded }, Modifier.testTag("tray"))
                    JChip("Needs you", Modifier.testTag("chip"), selected = expanded, onClick = {}, count = 3,
                        dot = assistantTrayColor(AssistantTrayTone.Pending), role = Role.Button)
                    Row(Modifier.width(360.dp).padding(horizontal = Tokens.Space.m).testTag("bar"),
                        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        AssistantTrayEntry("Needs you", 3, AssistantTrayTone.Pending, false, "Collapsed", {}, Modifier.testTag("bar-needs"))
                        AssistantTrayEntry("In progress", 4, AssistantTrayTone.Progress, false, "Collapsed", {}, Modifier.testTag("bar-progress"))
                        AssistantTrayEntry("Finished", 1, AssistantTrayTone.Finished, false, "Collapsed", {}, Modifier.testTag("bar-finished"))
                    }
                }
            }
        }
        val tray = rule.onNodeWithTag("tray")
        tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Collapsed"))
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
        val trayNode = tray.fetchSemanticsNode()
        val chipWidth = rule.onNodeWithTag("chip").fetchSemanticsNode().boundsInRoot.width
        assertTrue("Glass capsule omits the leading dot and caret, so it is narrower than JChip", trayNode.boundsInRoot.width < chipWidth)
        val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }
        assertTrue("Glass capsule stays visually compact", trayNode.boundsInRoot.height < minTouchPx)
        assertTrue("Glass capsule keeps Android's 48dp touch target", trayNode.touchBoundsInRoot.height >= minTouchPx)
        val barRight = rule.onNodeWithTag("bar").fetchSemanticsNode().boundsInRoot.right
        val finishedRight = rule.onNodeWithTag("bar-finished").fetchSemanticsNode().boundsInRoot.right
        assertTrue("All three default Glass entries fit within a 360dp phone width without clipping", finishedRight <= barRight)
        tray.performClick()
        tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Expanded"))
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
    }

    @Test fun briefAndBoardTrayEntriesKeepTheirChipAndChevron() {
        var style by mutableStateOf(AppStyle.Brief)
        var expanded by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Column {
                    AssistantTrayEntry("Pending", 2, AssistantTrayTone.Pending, expanded,
                        if (expanded) "Expanded" else "Collapsed", { expanded = !expanded }, Modifier.testTag("tray"))
                    JChip("Pending", Modifier.testTag("plain"), selected = expanded, onClick = {}, count = 2,
                        dot = assistantTrayColor(AssistantTrayTone.Pending), role = Role.Button)
                }
            }
        }
        for (s in listOf(AppStyle.Brief, AppStyle.Board)) {
            style = s
            expanded = false
            rule.waitForIdle()
            val tray = rule.onNodeWithTag("tray")
            tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
                .assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Collapsed"))
                .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
            assertChevronWidth()
            tray.performClick()
            tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Expanded"))
                .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
            assertChevronWidth()
        }
    }

    @Test fun assistantTrayColorFollowsStyleStatusPalette() {
        val observed = mutableMapOf<Triple<AppStyle, Boolean, AssistantTrayTone>, Pair<Color, Color>>()
        rule.setContent {
            for (style in AppStyle.entries) for (dark in listOf(false, true)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val palette = PersonaTheme.palette
                    observed[Triple(style, dark, AssistantTrayTone.Pending)] =
                        assistantTrayColor(AssistantTrayTone.Pending) to palette.needsYou.dot
                    observed[Triple(style, dark, AssistantTrayTone.Progress)] =
                        assistantTrayColor(AssistantTrayTone.Progress) to palette.working.dot
                    observed[Triple(style, dark, AssistantTrayTone.Finished)] =
                        assistantTrayColor(AssistantTrayTone.Finished) to palette.positive
                    observed[Triple(style, dark, AssistantTrayTone.Agents)] =
                        assistantTrayColor(AssistantTrayTone.Agents) to palette.positive
                }
            }
        }
        rule.waitForIdle()
        for ((key, pair) in observed) {
            assertEquals("Tone color mismatch for $key", pair.second, pair.first)
        }
    }

    private fun assertChevronWidth() {
        val trayWidth = rule.onNodeWithTag("tray").fetchSemanticsNode().boundsInRoot.width
        val plainWidth = rule.onNodeWithTag("plain").fetchSemanticsNode().boundsInRoot.width
        val chevronWidth = with(rule.density) { (JChipTokens.iconSize + JChipTokens.iconGap).toPx() }
        assertEquals("The trailing chevron reserves its icon and gap", chevronWidth, trayWidth - plainWidth, 0.5f)
    }
}
