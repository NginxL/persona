package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class ConversationTagFilterBarTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class Fixture {
        var entries by mutableStateOf(listOf("second", "first"))
        var active by mutableStateOf<String?>(null)
        var customized by mutableStateOf(true)
        var editing by mutableStateOf(false)
        val changes = mutableListOf<List<String>?>()
        var changeResult = true
    }

    private fun draft() = rule.onNode(hasSetTextAction() and
        hasAnyAncestor(hasTestTag("conversation-tag-filter-draft")))

    private fun show(fixture: Fixture) {
        rule.setContent {
            PersonaTheme {
                ConversationTagFilterBar(
                    entries = fixture.entries,
                    activeTag = fixture.active,
                    customized = fixture.customized,
                    editing = fixture.editing,
                    busy = false,
                    onSelect = { fixture.active = if (fixture.active == it) null else it },
                    onChange = { next ->
                        fixture.changes.add(next)
                        if (fixture.changeResult) {
                            fixture.entries = next ?: listOf("automatic")
                            fixture.customized = next != null
                        }
                        fixture.changeResult
                    },
                    onEdit = { fixture.editing = true },
                    onDone = { fixture.editing = false },
                )
            }
        }
    }

    @Test fun defaultEmptySetAddsNoRowOrSettingsOnlyControl() {
        val fixture = Fixture().apply {
            entries = emptyList()
            customized = false
        }
        show(fixture)

        rule.onNodeWithTag("conversation-tag-filter").assertDoesNotExist()
        rule.onNodeWithContentDescription("Customize tag filters").assertDoesNotExist()
    }

    @Test fun automaticPresentationOnlyRevealsEditingOnRequestAndSelectionStaysControlled() {
        val fixture = Fixture().apply {
            entries = listOf("Planning", "Travel")
            customized = false
        }
        show(fixture)

        rule.onNodeWithText("Add").assertDoesNotExist()
        rule.onNodeWithText("Done").assertDoesNotExist()
        rule.onNodeWithText("Reset to automatic").assertDoesNotExist()
        rule.onNodeWithText("Planning").performClick()
        rule.onNodeWithText("Planning").assertIsSelected()
        rule.onNodeWithContentDescription("Customize tag filters").performClick()
        rule.onNodeWithTag("conversation-tag-filter-draft").assertExists()
        rule.onNodeWithText("Done").assertExists()
        // Automatic mode offers Done and Add, but Reset is meaningful only for an already customized set.
        rule.onNodeWithText("Reset to automatic").assertDoesNotExist()
    }

    @Test fun editingAddsInOrderRemovesOneEntryResetsNullAndDoneClosesControls() {
        val fixture = Fixture().apply { editing = true }
        show(fixture)

        draft().performTextInput(" new ")
        rule.onNodeWithText("Add").performClick()
        rule.waitForIdle()
        assertEquals(listOf("second", "first", "new"), fixture.changes.last())

        rule.onNodeWithContentDescription("Remove first").performClick()
        rule.waitForIdle()
        assertEquals(listOf("second", "new"), fixture.changes.last())

        rule.onNodeWithText("Reset to automatic").performClick()
        rule.waitForIdle()
        assertEquals(null, fixture.changes.last())
        assertFalse(fixture.customized)

        rule.onNodeWithText("Done").performClick()
        rule.onNodeWithTag("conversation-tag-filter-draft").assertDoesNotExist()
        rule.onNodeWithContentDescription("Customize tag filters").assertExists()
    }

    @Test fun customizedEmptySetHasAnExplicitEmptyStateAndNoAutomaticChip() {
        val fixture = Fixture().apply {
            entries = emptyList()
            customized = true
        }
        show(fixture)

        rule.onNodeWithText("No tag filters").assertExists()
        rule.onAllNodesWithText("automatic").assertCountEquals(0)
        rule.onNodeWithContentDescription("Customize tag filters").performClick()
        rule.onNodeWithText("Reset to automatic").assertExists()
    }

    @Test fun duplicateAddDoesNotWrite() {
        val fixture = Fixture().apply { editing = true }
        show(fixture)

        draft().performTextInput(" first ")
        rule.onNodeWithText("Add").performClick()
        rule.waitForIdle()

        assertEquals(0, fixture.changes.size)
        rule.onNodeWithText("first").assertExists() // The existing chip remains; no duplicate is added.
    }

    @Test fun failedAddKeepsTheDraft() {
        val fixture = Fixture().apply {
            editing = true
            changeResult = false
        }
        show(fixture)

        draft().performTextInput("third")
        rule.onNodeWithText("Add").performClick()
        rule.waitForIdle()

        assertEquals(listOf(listOf("second", "first", "third")), fixture.changes)
        assertEquals(listOf("second", "first"), fixture.entries)
        rule.onNodeWithText("third").assertExists()
    }
}
