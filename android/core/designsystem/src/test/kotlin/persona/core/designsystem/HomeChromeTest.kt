package persona.core.designsystem

import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusManager
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertLeftPositionInRootIsEqualTo
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.icons.PhIcons

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class HomeChromeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun openedDrawerPreventsKeyboardFocusFromEnteringTheHomeBehindIt() {
        var opened by mutableStateOf(false)
        var homeFocused = false
        val home = FocusRequester()
        val drawer = FocusRequester()
        lateinit var focus: FocusManager
        rule.setContent {
            focus = LocalFocusManager.current
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                JShellDrawer(opened, onDismiss = { opened = false }, drawer = {
                    Box(Modifier.fillMaxSize().focusRequester(drawer).focusable())
                }) {
                    Box(Modifier.fillMaxSize().focusRequester(home)
                        .onFocusChanged { homeFocused = it.isFocused }.focusable())
                }
            }
        }
        rule.runOnIdle { assertTrue(home.requestFocus()) }
        rule.runOnIdle {
            assertTrue(homeFocused)
            focus.clearFocus(force = true)
            opened = true
        }
        rule.waitForIdle()
        rule.runOnIdle {
            assertFalse(home.requestFocus())
            assertFalse(homeFocused)
            assertTrue(drawer.requestFocus())
            focus.clearFocus(force = true)
            opened = false
        }
        rule.waitForIdle()
        rule.runOnIdle { assertTrue(home.requestFocus()) }
        rule.runOnIdle { assertTrue(homeFocused) }
    }

    @Test fun theSidebarColumnComesAndGoesWithoutRebuildingThePage() {
        var regular by mutableStateOf(false)
        var shown by mutableStateOf(true)
        var built = 0
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                JSidebarColumn(if (regular) ({ Box(Modifier.fillMaxSize().testTag("sidebar")) }) else null,
                    width = 120.dp, pageStart = 140.dp, shown = shown) {
                    remember { built++ }
                    Box(Modifier.fillMaxSize().testTag("page"))
                }
            }
        }
        // A phone: the page fills the box.
        rule.onNodeWithTag("sidebar").assertDoesNotExist()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(0.dp)
        // Unfolded: the column, and the page past it (here past a fold 20dp wide).
        regular = true
        rule.onNodeWithTag("sidebar").assertExists()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(140.dp)
        // Hidden with the avatar, then shown again.
        shown = false
        rule.onNodeWithTag("sidebar").assertDoesNotExist()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(0.dp)
        shown = true
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(140.dp)
        // Folded again.
        regular = false
        rule.onNodeWithTag("sidebar").assertDoesNotExist()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(0.dp)
        rule.runOnIdle { assertEquals("the page was composed once, through every change", 1, built) }
    }

    @Test fun narrowSwitcherLeavesRoomForTheTrailingAction() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                Box(Modifier.width(300.dp).height(100.dp)) {
                    HomeNavigation(
                        items = listOf(
                            NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                            NavItem("agents", "Agents", PhIcons.Robot),
                        ),
                        selected = "agents", onSelect = {}, leading = {},
                        trailing = { JIconButton(PhIcons.Plus, "New chat", {}) },
                    )
                }
            }
        }
        val label = rule.onNodeWithText("Agents").getBoundsInRoot()
        val action = rule.onNodeWithContentDescription("New chat").getBoundsInRoot()
        assertTrue("the trailing action covers the destination label: $label / $action", label.right <= action.left)
    }

    @Test fun briefRetappingAssistantOpensItsMenu() = retappingAssistantOpensItsMenu(AppStyle.Brief)

    @Test fun boardRetappingAssistantOpensItsMenu() = retappingAssistantOpensItsMenu(AppStyle.Board)

    private fun retappingAssistantOpensItsMenu(style: AppStyle) {
        var selected by mutableStateOf("assistant")
        val selections = mutableListOf<String>()
        var menuOpens = 0
        rule.setContent {
            PersonaTheme(style = style, reduceMotion = true) {
                HomeNavigation(
                    items = listOf(
                        NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                        NavItem("agents", "Agents", PhIcons.Robot),
                    ),
                    selected = selected,
                    onSelect = { selected = it; selections += it },
                    leading = {},
                    onSelectedMenu = { menuOpens++ },
                )
            }
        }

        rule.onNodeWithText("Assistant").performClick()
        assertEquals(1, menuOpens)
        assertEquals(emptyList<String>(), selections)

        rule.onNodeWithText("Agents").performClick()
        assertEquals("agents", selected)
        assertEquals(listOf("agents"), selections)
        assertEquals("switching pages must not open the menu", 1, menuOpens)
    }
}
