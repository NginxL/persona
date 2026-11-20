package persona.feature.assistant.settings

import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.SavedPrompt
import persona.core.contracts.api.SavedPromptsApi
import persona.core.designsystem.PersonaTheme
import persona.core.hub.HubError
import persona.core.hub.HubFailure

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SavedPromptsPageTest {
    @get:Rule val compose = createComposeRule()

    private class Hub : SavedPromptsApi {
        var prompts: List<SavedPrompt>? = listOf(SavedPrompt("a", "review", "Read the draft."))
        var saveError: Exception? = null
        var deletes = 0
        override suspend fun list() = prompts
        override suspend fun save(id: String?, name: String, text: String): SavedPrompt {
            saveError?.let { throw it }
            val prompt = SavedPrompt(id ?: "b", name, text)
            val before = prompts.orEmpty()
            prompts = if (id == null) before + prompt else before.map { if (it.id == id) prompt else it }
            return prompt
        }
        override suspend fun reorder(ids: List<String>) = ids.map { id -> prompts.orEmpty().first { it.id == id } }
            .also { prompts = it }
        override suspend fun delete(id: String) { deletes++; prompts = prompts.orEmpty().filterNot { it.id == id } }
    }

    private fun open(hub: Hub) {
        val store = SavedPromptsStore(hub)
        compose.setContent {
            PersonaTheme(reduceMotion = true) {
                LaunchedEffect(store) { store.refresh() }
                SavedPromptsPage(store, onBack = {})
            }
        }
    }

    @Test fun editorKeepsARefusedDraftAndDeleteRequiresConfirmation() {
        val hub = Hub().apply { saveError = HubFailure(HubError.Server, "Try again later", 503) }
        open(hub)
        compose.onNodeWithContentDescription("New Prompt").performClick()
        fun field(word: String) =
            compose.onNode(hasSetTextAction() and hasAnyAncestor(hasTestTag("saved-prompt-$word")), useUnmergedTree = true)
        field("name").performTextInput("/notes")
        field("text").performTextInput("Write notes.")
        compose.onNodeWithContentDescription("Save").performClick()
        compose.onNodeWithText("Try again later", substring = true).assertExists()
        field("name").assertTextEquals("/notes")
        field("text").assertTextEquals("Write notes.")
        compose.runOnIdle { hub.saveError = null }
        compose.onNodeWithContentDescription("Save").performClick()
        compose.onNodeWithTag("saved-prompt-b").assertExists().performClick()
        field("text").performTextReplacement("Write new notes.")
        compose.onNodeWithContentDescription("Save").performClick()
        compose.onNodeWithText("Write new notes.").assertExists()

        compose.onNodeWithContentDescription("Actions for /notes", useUnmergedTree = true).performClick()
        compose.onNodeWithText("Move Up").performClick()
        compose.runOnIdle { assertEquals(listOf("b", "a"), hub.prompts?.map { it.id }) }
        fun askDelete() {
            compose.onNodeWithContentDescription("Actions for /notes", useUnmergedTree = true).performClick()
            compose.onNodeWithText("Delete").performClick()
            compose.onNodeWithText("Delete “/notes”?").assertExists()
        }
        askDelete()
        compose.onNodeWithText("Cancel").performClick()
        compose.runOnIdle { assertEquals(0, hub.deletes) }
        askDelete()
        compose.onNodeWithText("Delete").performClick()
        compose.onNodeWithTag("saved-prompt-b").assertDoesNotExist()
        compose.runOnIdle { assertEquals(1, hub.deletes) }
    }

    @Test fun anOldHubShowsUnavailableWithoutANewPromptAction() {
        open(Hub().apply { prompts = null })
        compose.onNodeWithText("Saved Prompts Unavailable").assertExists()
        compose.onNodeWithContentDescription("New Prompt").assertDoesNotExist()
        compose.onNodeWithText("New Prompt").assertDoesNotExist()
    }
}
