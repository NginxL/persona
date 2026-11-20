package persona.feature.chat

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ExtensionQuickPrompt
import persona.core.contracts.api.SavedPrompt
import persona.core.designsystem.LocalExtensionPrompts
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SavedPromptComposerTest {
    @get:Rule val compose = createComposeRule()

    @Test fun assistantAndAgentListTheSavedCollisionBetweenTheirCommandsAndExtensionRows() {
        val assistant = mutableStateOf(true)
        val state = ChatComposerState(ChatComposerDraft("/CLEAR"))
        var clears = 0
        var sends = 0
        var permissionPicks = 0
        val saved = SavedPrompt("clear", "clear", "/permissions")
        val extension = ExtensionQuickPrompt("sample", "Example", "clear", "Clear from an extension", "Extension body")
        compose.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(
                    LocalSavedPrompts provides listOf(saved),
                    LocalExtensionPrompts provides listOf(extension),
                    LocalMainChatClear provides if (assistant.value) MainChatClear { clears++ } else null,
                ) {
                    ChatComposer(SessionKey("box", "chat"), state, working = false, sending = false, canClear = true,
                        service = null, onSend = { sends++ }, onCommand = { clears++ }, onNotice = {},
                        assistantChat = assistant.value, onPermissions = { permissionPicks++ })
                }
            }
        }
        for (isAssistant in listOf(true, false)) {
            compose.runOnIdle {
                assistant.value = isAssistant
                state.value = TextFieldValue("/CLEAR")
            }
            val savedRow = compose.onNodeWithText(saved.text)
            val collisions = compose.onAllNodesWithText("/clear").fetchSemanticsNodes()
            assertEquals(2, collisions.size)
            val builtIn = collisions.first()
            val savedTop = savedRow.fetchSemanticsNode().boundsInRoot.top
            assertTrue(builtIn.boundsInRoot.top < savedTop)
            assertTrue(savedTop < compose.onNodeWithText(extension.title).fetchSemanticsNode().boundsInRoot.top)
            savedRow.performClick()
            compose.onNodeWithText("Saved Prompts").assertDoesNotExist()
            compose.runOnIdle {
                assertEquals(saved.text, state.value.text)
                assertEquals(TextRange(saved.text.length), state.value.selection)
                assertEquals(0, sends)
                assertEquals(0, clears)
                assertEquals(0, permissionPicks)
            }
        }
    }

    @Test fun savedBodiesBypassLocalSendCommandsUntilTextIsEdited() {
        for (word in listOf("/clear", "/permissions")) {
            val state = ChatComposerState(ChatComposerDraft("/"))
            var sends = 0
            var clears = 0
            var permissions = 0
            val mainClear = MainChatClear { clears++ }
            val modes = { permissions++; Unit }
            state.fillSavedPrompt(word)
            state.value = state.value.copy(selection = TextRange.Zero)
            dispatchComposerSend(state, false, mainClear, modes) { sends++ }
            assertEquals(1, sends)
            assertEquals(0, clears + permissions)
            assertEquals(word, state.value.text)

            state.value = TextFieldValue("/")
            state.value = TextFieldValue(word)
            dispatchComposerSend(state, false, mainClear, modes) { sends++ }
            assertEquals(1, sends)
            assertEquals(1, clears + permissions)
        }
    }

    @Test fun matchingUsesTheHubOrderAndWorksWithoutBuiltIns() {
        val prompts = listOf(SavedPrompt("2", "REVIEW-LONG", "First"), SavedPrompt("1", "review", "Second"),
            SavedPrompt("3", "校閲", "Third"))
        assertEquals(prompts, matchingSavedPrompts(prompts, "/"))
        assertEquals(prompts.take(2), matchingSavedPrompts(prompts, "/ReView"))
        assertEquals(prompts.takeLast(1), matchingSavedPrompts(prompts, "/校"))
        assertTrue(matchingSavedPrompts(prompts, "/review\t").isEmpty())
    }
}
