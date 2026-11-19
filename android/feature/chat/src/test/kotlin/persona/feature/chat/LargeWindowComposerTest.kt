package persona.feature.chat

import android.Manifest
import android.app.Application
import android.os.Bundle
import android.speech.SpeechRecognizer
import androidx.test.core.app.ApplicationProvider
import androidx.compose.ui.test.onNodeWithContentDescription
import org.robolectric.Shadows.shadowOf
import org.robolectric.shadows.ShadowSpeechRecognizer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.isFocused
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performKeyInput
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.pressKey
import androidx.compose.ui.test.withKeyDown
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.designsystem.HardwareShortcutHost
import persona.core.designsystem.LocalLargeWindowInput
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w640dp-h800dp-xhdpi")
class LargeWindowComposerTest {
    @get:Rule val rule = createComposeRule()

    private fun show(
        state: ChatComposerState,
        large: Boolean = true,
        sending: Boolean = false,
        enabled: Boolean = true,
        extraContent: Boolean = false,
        blockedLabel: String? = null,
        onPermissions: (() -> Unit)? = null,
        onSend: () -> Unit,
    ) {
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    LargeWindowComposer(SessionKey("box", "s1"), state, working = false, sending = sending,
                        canClear = false, service = null, onSend = onSend, onCommand = {}, onNotice = {},
                        enabled = enabled, extraContent = extraContent, blockedLabel = blockedLabel,
                        onPermissions = onPermissions)
                }
            }
        }
        if (enabled) {
            rule.onNode(hasSetTextAction()).performClick()
            rule.waitForIdle()
        }
    }

    @Test fun returnAndShiftReturnInsertLinesWithoutSending() {
        val state = ChatComposerState().apply { value = TextFieldValue("hello", TextRange(5)) }
        var sent = 0
        show(state) { sent++ }
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            pressKey(Key.Enter)
            withKeyDown(Key.ShiftLeft) { pressKey(Key.Enter) }
        }
        assertEquals(0, sent)
        assertEquals("hello\n\n", state.value.text)
    }

    @Test fun ctrlAndMetaReturnEachSendOnceAndExtraContentUsesTheSameGate() {
        val state = ChatComposerState().apply { value = TextFieldValue("hello", TextRange(5)) }
        var sent = 0
        show(state, extraContent = true) { sent++ }
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(1, sent)
        sent = 0
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.MetaLeft) { pressKey(Key.Enter) }
        }
        assertEquals(1, sent)
        state.value = TextFieldValue()
        rule.waitForIdle()
        sent = 0
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals("extra content alone remains sendable", 1, sent)
    }

    @Test fun hardwareSendWaitsForFinalOnlyDictationAndThenUsesTheCompletedDraft() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(ApplicationProvider.getApplicationContext<Application>()).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val state = ChatComposerState().apply { value = TextFieldValue("Original", TextRange(8)) }
        val sent = mutableListOf<String>()
        show(state) { sent += state.value.text }
        rule.onNodeWithContentDescription("Dictate").performClick()
        val recognition = shadowOf(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        fun shortcut(key: Key) = rule.onNode(hasSetTextAction() and isFocused()).performKeyInput { withKeyDown(key) { pressKey(Key.Enter) } }
        shortcut(Key.CtrlLeft)
        rule.onNodeWithContentDescription("Stop dictation").performClick()
        shortcut(Key.MetaLeft)
        assertTrue("Neither listening nor finishing may send", sent.isEmpty())
        val words = Bundle().apply { putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf("final words")) }
        rule.runOnIdle { recognition.triggerOnResults(words) }
        shortcut(Key.CtrlLeft)
        assertEquals(listOf("Original final words"), sent)
    }

    @Test fun emptySendingAndBlockedDraftsDoNotSend() {
        val state = ChatComposerState()
        var sent = 0
        show(state, sending = true, blockedLabel = "Connect a model first") { sent++ }
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(0, sent)
    }

    @Test fun pendingAttachmentsUseTheOrdinarySendReadinessGate() {
        val state = ChatComposerState().apply {
            attachments = listOf(ComposerAttachment("pending", "photo.jpg", "", image = true))
        }
        var sent = 0
        show(state) { sent++ }
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(0, sent)
    }

    @Test fun readOnlyComposerDoesNotSend() {
        var sent = 0
        show(ChatComposerState(), enabled = false) { sent++ }
        rule.onRoot().performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(0, sent)
    }

    @Test fun composingTextIsNotSentUntilTheCompositionCommits() {
        val state = ChatComposerState().apply {
            value = TextFieldValue("中文", TextRange(2), TextRange(0, 2))
        }
        var sent = 0
        show(state) { sent++ }
        // Clicking to focus commits the composition; restore it before the shortcut under test.
        state.value = TextFieldValue("中文", TextRange(2), TextRange(0, 2))
        rule.waitForIdle()
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(0, sent)
        state.value = TextFieldValue("中文", TextRange(2))
        rule.waitForIdle()
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(1, sent)
        assertEquals(TextRange(2), state.value.selection)
    }

    @Test fun smallComposerHelpLeavesTextCtrlChordsAndCompositionAlone() {
        val state = ChatComposerState()
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize()) {
                        LargeWindowComposer(SessionKey("box", "s1"), state, working = false, sending = false,
                            canClear = false, service = null, onSend = {}, onCommand = {}, onNotice = {})
                    }
                }
            }
        }
        val field = rule.onNode(hasSetTextAction())
        field.performClick()
        field.performTextInput("a")
        assertEquals("a", state.value.text)
        field.performKeyInput { withKeyDown(Key.CtrlLeft) { pressKey(Key.C) } }
        rule.onNodeWithTag("keyboard-shortcuts").assertDoesNotExist()
        state.value = TextFieldValue("中文", TextRange(2), TextRange(0, 2))
        rule.waitForIdle()
        field.performKeyInput { withKeyDown(Key.CtrlLeft) { pressKey(Key.Slash) } }
        rule.onNodeWithTag("keyboard-shortcuts").assertDoesNotExist()
        assertEquals("中文/", state.value.text)
        state.value = TextFieldValue("中文", TextRange(2))
        rule.waitForIdle()
        field.performKeyInput { withKeyDown(Key.CtrlLeft) { pressKey(Key.Slash) } }
        rule.onNodeWithTag("keyboard-shortcuts").assertExists()
    }

    @Test fun openingTheFullEditorStopsCaptureAndSendsOnlyAfterTheRecognizerFinal() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(ApplicationProvider.getApplicationContext<Application>()).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val text = (1..9).joinToString("\n") { "Typed line $it" }
        val state = ChatComposerState().apply { value = TextFieldValue(text) }
        val sent = mutableListOf<String>()
        show(state) { sent += state.value.text }
        rule.onNodeWithContentDescription("Dictate").performClick()
        val recognition = shadowOf(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        rule.onNodeWithTag("composer-expand").performClick()
        fun send() = rule.onNodeWithTag("composer-expanded-input").performKeyInput { withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) } }
        send()
        assertTrue(sent.isEmpty())
        val words = Bundle().apply { putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf("finished")) }
        rule.runOnIdle { recognition.triggerOnResults(words) }
        send()
        assertEquals(listOf("$text finished"), sent)
    }

    @Test fun expandedEditorSendsOnceAndEscapeOnlyCollapses() {
        val long = (1..9).joinToString("\n") { "line $it with enough words to wrap in the editor" }
        val state = ChatComposerState().apply { value = TextFieldValue(long, TextRange(12)) }
        var sent = 0
        show(state) { sent++ }
        state.value = state.value.copy(selection = TextRange(12))
        rule.waitForIdle()
        val focusBeforeSend = state.focusRequest
        rule.onNodeWithTag("composer-expand").performClick()
        rule.onNodeWithTag("composer-expanded").assertIsDisplayed()
        rule.onNodeWithTag("composer-expanded-input").performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        rule.onNodeWithTag("composer-expanded").assertDoesNotExist()
        assertEquals(1, sent)
        assertEquals(long, state.value.text)
        assertEquals(TextRange(12), state.value.selection)
        assertEquals("hardware send must not request small-field focus", focusBeforeSend, state.focusRequest)
        assertFalse(state.focused)

        val focusBeforeEscape = state.focusRequest
        rule.onNodeWithTag("composer-expand").performClick()
        rule.onNodeWithTag("composer-expanded-input").performKeyInput { pressKey(Key.Escape) }
        rule.onNodeWithTag("composer-expanded").assertDoesNotExist()
        assertEquals(1, sent)
        assertEquals(long, state.value.text)
        assertEquals(TextRange(12), state.value.selection)
        assertEquals("Escape restores small-field focus", focusBeforeEscape + 1, state.focusRequest)
        assertTrue(state.focused)
    }

    @Test fun compactDoesNotInstallTheNewSendShortcut() {
        val state = ChatComposerState().apply { value = TextFieldValue("hello", TextRange(5)) }
        var sent = 0
        show(state, large = false) { sent++ }
        rule.onNodeWithTag("composer-drop-target").assertDoesNotExist()
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(0, sent)
    }

    @Test fun hardwareSendInterceptsBarePermissionsInsteadOfSending() {
        val state = ChatComposerState().apply { value = TextFieldValue(" /permissions ", TextRange(14)) }
        var sent = 0
        var opened = 0
        show(state, onPermissions = { opened++ }) { sent++ }
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(1, opened)
        assertEquals(0, sent)
        assertEquals("", state.value.text)
    }

    @Test fun hardwareSendWithExtraContentKeepsPermissionsAsMessage() {
        val state = ChatComposerState().apply { value = TextFieldValue("/permissions", TextRange(12)) }
        var sent = 0
        var opened = 0
        show(state, extraContent = true, onPermissions = { opened++ }) { sent++ }
        rule.onNode(hasSetTextAction() and isFocused()).performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Enter) }
        }
        assertEquals(0, opened)
        assertEquals(1, sent)
    }
}
