package persona.core.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsFocused
import androidx.compose.ui.test.assertIsNotFocused
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performKeyInput
import androidx.compose.ui.test.pressKey
import androidx.compose.ui.test.withKeyDown
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import kotlinx.coroutines.awaitCancellation

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w840dp-h900dp-xhdpi")
class HardwareShortcutHostTest {
    @get:Rule val rule = createComposeRule()

    @Test fun helpClosesBeforeTheSheetAndTheSecondEscapeClosesTheSheet() {
        var dismissed = 0
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize()) {
                        JSheet(onDismiss = { dismissed++ }) {
                            BasicTextField("draft", {}, Modifier.testTag("sheet-field").hardwareShortcutTextInput())
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("sheet-field").performClick()
        rule.onNodeWithTag("sheet-field").performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Slash) }
        }
        rule.onNodeWithTag("keyboard-shortcuts").assertExists()
        assertEquals(0, dismissed)
        rule.onNodeWithTag("keyboard-shortcuts").performKeyInput { pressKey(Key.Escape) }
        rule.waitForIdle()
        rule.onNodeWithTag("keyboard-shortcuts").assertDoesNotExist()
        assertEquals(0, dismissed)
        rule.onNodeWithTag("sheet-field").performKeyInput { pressKey(Key.Escape) }
        assertEquals(1, dismissed)
    }

    @Test fun focusedTerminalKeysNeverFallThroughToTheSheetOrHelp() {
        var dismissed = 0
        val consumed = mutableListOf<Key>()
        val terminalKeys = setOf(Key.Escape, Key.Tab, Key.C, Key.DirectionUp, Key.DirectionDown, Key.DirectionLeft, Key.DirectionRight, Key.Slash)
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize()) {
                        JSheet(onDismiss = { dismissed++ }) {
                            BasicTextField("terminal", {}, Modifier.testTag("terminal-field").onPreviewKeyEvent { event ->
                                if (event.type == KeyEventType.KeyDown && event.key in terminalKeys) {
                                    consumed += event.key
                                    true
                                } else false
                            })
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("terminal-field").performClick()
        rule.onNodeWithTag("terminal-field").performKeyInput {
            pressKey(Key.Escape)
            pressKey(Key.Tab)
            withKeyDown(Key.ShiftLeft) { pressKey(Key.Tab) }
            withKeyDown(Key.CtrlLeft) { pressKey(Key.C) }
            pressKey(Key.DirectionUp)
            pressKey(Key.DirectionDown)
            pressKey(Key.DirectionLeft)
            pressKey(Key.DirectionRight)
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Slash) }
        }
        assertTrue(consumed.containsAll(terminalKeys))
        assertEquals(2, consumed.count { it == Key.Tab })
        assertEquals(0, dismissed)
        rule.onNodeWithTag("keyboard-shortcuts").assertDoesNotExist()
    }

    @Test fun compactDoesNotRegisterEscapeOrHelp() {
        var dismissed = 0
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides false) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize()) {
                        Box(Modifier.hardwareShortcutDismiss { dismissed++ }) {
                            BasicTextField("draft", {}, Modifier.testTag("compact-field"))
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("compact-field").performClick()
        rule.onNodeWithTag("compact-field").performKeyInput {
            pressKey(Key.Escape)
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Slash) }
        }
        assertEquals(0, dismissed)
        rule.onNodeWithTag("keyboard-shortcuts").assertDoesNotExist()
    }

    @Test fun closingHelpRestoresTheDraftAndItsFocusedField() {
        var draft by mutableStateOf("draft")
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize()) {
                        JSheet(onDismiss = {}) {
                            BasicTextField(draft, { draft = it }, Modifier.testTag("draft-field").hardwareShortcutTextInput())
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("draft-field").performClick()
        rule.onNodeWithTag("draft-field").performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Slash) }
        }
        rule.onNodeWithTag("keyboard-shortcuts").performKeyInput { pressKey(Key.Escape) }
        rule.waitForIdle()
        rule.onNodeWithTag("draft-field").assertTextEquals("draft")
        rule.onNodeWithTag("draft-field").assertIsFocused()
    }

    @Test fun initialCompactWithoutChildFocusGetsHostFocusForHelp() {
        var large by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize().testTag("empty-host")) {
                        Box(Modifier.fillMaxSize())
                    }
                }
            }
        }
        large = true
        rule.waitForIdle()
        rule.onNodeWithTag("empty-host").assertIsFocused()
        rule.onNodeWithTag("empty-host").performKeyInput {
            withKeyDown(Key.CtrlLeft) { pressKey(Key.Slash) }
        }
        rule.onNodeWithTag("keyboard-shortcuts").assertExists()
    }

    @Test fun enteringLargeDoesNotStealExistingInputFocus() {
        var large by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize().testTag("host-with-field")) {
                        val field = remember { FocusRequester() }
                        LaunchedEffect(Unit) { val _ = runCatching { field.requestFocus() } }
                        BasicTextField("draft", {}, Modifier.testTag("compact-focused-field").focusRequester(field))
                    }
                }
            }
        }
        rule.onNodeWithTag("compact-focused-field").assertIsFocused()
        large = true
        rule.waitForIdle()
        rule.onNodeWithTag("compact-focused-field").assertIsFocused()
        rule.onNodeWithTag("host-with-field").assertIsNotFocused()
    }

    @Test fun aDismissSurfaceKeepsItsFocusedChildWhenTheWindowBecomesLarge() {
        var large by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize(), requestFocusWhenLarge = false) {
                        Column(Modifier.fillMaxSize().hardwareShortcutDismiss({})) {
                            val second = remember { FocusRequester() }
                            LaunchedEffect(Unit) { val _ = runCatching { second.requestFocus() } }
                            BasicTextField("first", {}, Modifier.testTag("first-dismiss-field"))
                            BasicTextField("second", {}, Modifier.testTag("second-dismiss-field").focusRequester(second))
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("second-dismiss-field").assertIsFocused()
        large = true
        rule.waitForIdle()
        rule.onNodeWithTag("second-dismiss-field").assertIsFocused()
        rule.onNodeWithTag("first-dismiss-field").assertIsNotFocused()
    }

    @Test fun mainShellContentSurvivesLargeWindowChanges() {
        var large by mutableStateOf(true)
        var effectStarts = 0
        var disposals = 0
        lateinit var marker: Any
        lateinit var list: LazyListState
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    HardwareShortcutHost(Modifier.fillMaxSize().testTag("continuous-host")) {
                        val remembered = remember { Any() }
                        val scroll = rememberLazyListState()
                        var draft by remember { mutableStateOf("draft") }
                        val field = remember { FocusRequester() }
                        SideEffect { marker = remembered; list = scroll }
                        DisposableEffect(Unit) { onDispose { disposals++ } }
                        LaunchedEffect(Unit) { effectStarts++; awaitCancellation() }
                        LaunchedEffect(Unit) { val _ = runCatching { field.requestFocus() } }
                        Column {
                            BasicTextField(draft, { draft = it }, Modifier.testTag("continuous-draft").focusRequester(field))
                            LazyColumn(Modifier.height(Tokens.Size.box280), state = scroll) {
                                items(20) { index -> Text("Row $index") }
                            }
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("continuous-draft").assertIsFocused()
        rule.runOnIdle { list.requestScrollToItem(4, 13) }
        rule.waitForIdle()
        val originalMarker = marker
        val originalList = list
        val first = list.firstVisibleItemIndex
        val offset = list.firstVisibleItemScrollOffset
        large = false
        rule.waitForIdle()
        assertSame(originalMarker, marker)
        assertSame(originalList, list)
        assertEquals(first, list.firstVisibleItemIndex)
        assertEquals(offset, list.firstVisibleItemScrollOffset)
        rule.onNodeWithTag("continuous-draft").assertTextEquals("draft")
        rule.onNodeWithTag("continuous-draft").assertIsFocused()
        large = true
        rule.waitForIdle()
        assertSame(originalMarker, marker)
        assertSame(originalList, list)
        assertEquals(first, list.firstVisibleItemIndex)
        assertEquals(offset, list.firstVisibleItemScrollOffset)
        assertEquals(1, effectStarts)
        assertEquals(0, disposals)
        rule.onNodeWithTag("continuous-draft").assertTextEquals("draft")
        rule.onNodeWithTag("continuous-draft").assertIsFocused()
        rule.onNodeWithTag("continuous-host").assertIsNotFocused()
    }
}
