package persona.core.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
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
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsFocused
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import kotlinx.coroutines.awaitCancellation

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w840dp-h900dp-xhdpi")
class PageSizedSettingsSheetTest {
    @get:Rule val rule = createComposeRule()

    @Test fun expandedSettingsIsACenteredPageWithWindowMargins() {
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    JSettingsSheet(onDismiss = {}) { Box(Modifier.testTag("settings-content")) }
                }
            }
        }
        rule.onNodeWithTag("settings-content").assertExists()
        val bounds = rule.onNodeWithTag("settings-page-card").getUnclippedBoundsInRoot()
        assertTrue(bounds.right - bounds.left <= SettingsPageSizing.maximumWidth)
        assertTrue(bounds.bottom - bounds.top <= SettingsPageSizing.maximumHeight)
        assertTrue(bounds.left >= SettingsPageSizing.minimumWindowMargin)
        assertTrue(bounds.top >= SettingsPageSizing.minimumWindowMargin)
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
    @Test fun compactSettingsKeepsTheOriginalFullWindowSheet() {
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides false) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    JSettingsSheet(onDismiss = {}) { Box(Modifier.testTag("settings-content")) }
                }
            }
        }
        rule.onNodeWithTag("settings-content").assertExists()
        rule.onNodeWithTag("settings-page-card").assertDoesNotExist()
    }

    @Test fun presentationChangesKeepStackNonSaveableStateAnchorAndEffect() {
        var large by mutableStateOf(true)
        val stack = mutableListOf("root", "agent", "editor")
        val originalStack = stack.toList()
        var effectStarts = 0
        var disposals = 0
        lateinit var marker: Any
        lateinit var list: LazyListState
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    JSettingsSheet(onDismiss = {}) {
                        val remembered = remember { Any() }
                        val scroll = rememberLazyListState()
                        var draft by remember { mutableStateOf("draft") }
                        val field = remember { FocusRequester() }
                        SideEffect { marker = remembered; list = scroll }
                        DisposableEffect(Unit) { onDispose { disposals++ } }
                        LaunchedEffect(Unit) { effectStarts++; awaitCancellation() }
                        LaunchedEffect(Unit) { val _ = runCatching { field.requestFocus() } }
                        Column {
                            BasicTextField(draft, { draft = it }, Modifier.testTag("settings-draft").focusRequester(field))
                            LazyColumn(Modifier.height(Tokens.Size.box280), state = scroll) {
                                items(20) { index -> Text("Row $index") }
                            }
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("settings-draft").performTextReplacement("edited")
        rule.onNodeWithTag("settings-draft").assertIsFocused()
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
        rule.onNodeWithTag("settings-draft").assertTextEquals("edited")
        rule.onNodeWithTag("settings-draft").assertIsFocused()
        large = true
        rule.waitForIdle()
        assertSame(originalMarker, marker)
        assertSame(originalList, list)
        assertEquals(first, list.firstVisibleItemIndex)
        assertEquals(offset, list.firstVisibleItemScrollOffset)
        assertEquals(1, effectStarts)
        assertEquals(0, disposals)
        rule.onNodeWithTag("settings-draft").assertTextEquals("edited")
        rule.onNodeWithTag("settings-draft").assertIsFocused()
        assertTrue(stack == originalStack)
    }

    @Test fun clearedOrAlreadyFocusedSettingsDoNotRequestFocus() {
        val restoration = SettingsFocusRestoration().apply {
            restoreChildFocus = true
            hasFocus = false
            clearFocus()
        }
        assertFalse(restoration.shouldRestoreFocus())
        restoration.restoreChildFocus = true
        restoration.hasFocus = true
        assertFalse(restoration.shouldRestoreFocus())
    }

    @Test fun presentationChangesReturnToThePreviouslyFocusedSecondField() {
        var large by mutableStateOf(true)
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides large) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    JSettingsSheet(onDismiss = {}) {
                        val second = remember { FocusRequester() }
                        LaunchedEffect(Unit) { val _ = runCatching { second.requestFocus() } }
                        Column {
                            BasicTextField("first", {}, Modifier.testTag("first-settings-field"))
                            BasicTextField("second", {}, Modifier.testTag("second-settings-field").focusRequester(second))
                        }
                    }
                }
            }
        }
        rule.onNodeWithTag("second-settings-field").assertIsFocused()
        large = false
        rule.waitForIdle()
        rule.onNodeWithTag("second-settings-field").assertIsFocused()
        large = true
        rule.waitForIdle()
        rule.onNodeWithTag("second-settings-field").assertIsFocused()
    }
}
