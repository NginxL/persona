package persona.core.designsystem

import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusManager
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertLeftPositionInRootIsEqualTo
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.filter
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.DpRect
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
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

    @Test fun glassOneTrailingFitsEveryWidthAndFont() = homeControlsFit(AppStyle.Glass, trailingActions = 1)
    @Test fun glassTwoTrailingFitEveryWidthAndFont() = homeControlsFit(AppStyle.Glass, trailingActions = 2)
    @Test fun briefOneTrailingFitsEveryWidthAndFont() = homeControlsFit(AppStyle.Brief, trailingActions = 1)
    @Test fun briefTwoTrailingFitEveryWidthAndFont() = homeControlsFit(AppStyle.Brief, trailingActions = 2)
    @Test fun boardOneTrailingFitsEveryWidthAndFont() = homeControlsFit(AppStyle.Board, trailingActions = 1)
    @Test fun boardTwoTrailingFitEveryWidthAndFont() = homeControlsFit(AppStyle.Board, trailingActions = 2)

    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-mdpi")
    @Test fun glassOneTrailingTextScalesMatchMain() = homeControlsFit(AppStyle.Glass, 1, checkMainTextScale = true)
    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-mdpi")
    @Test fun glassTwoTrailingTextScalesMatchMain() = homeControlsFit(AppStyle.Glass, 2, checkMainTextScale = true)
    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-mdpi")
    @Test fun briefOneTrailingTextScalesMatchMain() = homeControlsFit(AppStyle.Brief, 1, checkMainTextScale = true)
    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-mdpi")
    @Test fun briefTwoTrailingTextScalesMatchMain() = homeControlsFit(AppStyle.Brief, 2, checkMainTextScale = true)
    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-mdpi")
    @Test fun boardOneTrailingTextScalesMatchMain() = homeControlsFit(AppStyle.Board, 1, checkMainTextScale = true)
    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-mdpi")
    @Test fun boardTwoTrailingTextScalesMatchMain() = homeControlsFit(AppStyle.Board, 2, checkMainTextScale = true)

    private fun homeControlsFit(style: AppStyle, trailingActions: Int, checkMainTextScale: Boolean = false) {
        var width by mutableStateOf(200.dp)
        var fontScale by mutableStateOf(2f)
        var faceClicks = 0
        var newClicks = 0
        var optionClicks = 0
        val selections = mutableListOf<String>()
        val items = listOf(
            NavItem("assistant", "Assistant", PhIcons.ChatCircle),
            NavItem("agents", "Agents", PhIcons.Robot, badge = 2),
        )
        val expectedTextSizes = mutableMapOf<String, Float>()
        val expectedDefaultTextSizes = mutableMapOf<String, Float>()
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = fontScale)) {
                PersonaTheme(style = style, reduceMotion = true) {
                    if (checkMainTextScale) {
                        val density = LocalDensity.current
                        val textMeasurer = androidx.compose.ui.text.rememberTextMeasurer()
                        val scale = (density.fontScale * HomeTokens.shellTextStep).coerceAtMost(HomeTokens.shellScaleMaximum)
                        if (style == AppStyle.Glass) {
                            items.forEach { item ->
                                val chosen = item.key == "agents"
                                val base = PersonaTheme.type.body
                                val labelStyle = base.copy(fontSize = base.fontSize * HomeTokens.shellTextStep,
                                    fontWeight = if (chosen) androidx.compose.ui.text.font.FontWeight.SemiBold
                                    else androidx.compose.ui.text.font.FontWeight.Normal)
                                val measured = textMeasurer.measure(item.label, style = labelStyle, maxLines = 1)
                                val available = with(density) { (HomeTokens.segmentWidth * scale).toPx() }
                                val fit = (available / measured.size.width.coerceAtLeast(1)).coerceAtMost(1f)
                                expectedTextSizes[item.label] =
                                    maxOf(Tokens.Type.Label.size.value, labelStyle.fontSize.value * fit) * density.fontScale
                            }
                        } else {
                            val fontSize = (if (style == AppStyle.Brief) PersonaTheme.type.label else PersonaTheme.type.labelSmall).fontSize
                            expectedTextSizes["Assistant"] = fontSize.value * density.fontScale
                            expectedTextSizes["Agents 2"] = fontSize.value * density.fontScale
                        }
                        if (density.fontScale == 1f) {
                            expectedDefaultTextSizes.clear()
                            expectedDefaultTextSizes.putAll(expectedTextSizes)
                        }
                    }
                    Box(Modifier.width(width).height(220.dp).testTag("pane")) {
                        HomeNavigation(
                            items = items,
                            selected = "agents", onSelect = { selections += it },
                            leading = { JPersonaButton(PersonaState.Idle, "Sidebar", { faceClicks++ }) },
                            trailing = {
                                JIconButton(PhIcons.NotePencil, "New session", { newClicks++ })
                                if (trailingActions == 2) JIconButton(PersonaIcons.moreMenu, "List options", { optionClicks++ })
                            },
                        )
                    }
                }
            }
        }
        val widths = if (checkMainTextScale) listOf(320.dp, 360.dp, 411.dp) else listOf(200.dp, 280.dp, 320.dp, 360.dp, 400.dp)
        val scales = if (checkMainTextScale) listOf(0.85f, 1f, 1.3f, 1.6f, 2f) else listOf(1f, 2f)
        for (w in widths) for (scale in scales) {
            rule.runOnIdle { width = w; fontScale = scale }
            rule.waitForIdle()
            val case = "$style width=$w fontScale=$scale trailing=$trailingActions"
            val agentsLabel = if (style == AppStyle.Glass) "Agents" else "Agents 2"
            if (checkMainTextScale || w >= 320.dp || scale == 1f) {
                assertTextNotEllipsized("Assistant", case, expectedTextSizes["Assistant"])
                assertTextNotEllipsized(agentsLabel, case, expectedTextSizes[agentsLabel])
            }
            if (checkMainTextScale && scale > 1f) {
                assertTrue("main text must grow at $case",
                    expectedTextSizes.getValue("Assistant") > expectedDefaultTextSizes.getValue("Assistant"))
                assertTrue("main text must grow at $case",
                    expectedTextSizes.getValue(agentsLabel) > expectedDefaultTextSizes.getValue(agentsLabel))
            }
            val pane = rule.onNodeWithTag("pane").getBoundsInRoot()
            val face = rule.onNodeWithContentDescription("Sidebar").getBoundsInRoot()
            val switcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            val new = rule.onNodeWithContentDescription("New session").getBoundsInRoot()
            val controls = mutableListOf("face" to face, "switcher" to switcher, "new" to new)
            if (trailingActions == 2) controls += "options" to rule.onNodeWithContentDescription("List options").getBoundsInRoot()
            assertTrue("switcher has no width: $case / $switcher", switcher.right > switcher.left)
            for ((name, bounds) in controls) {
                assertTrue("$name is horizontally clipped: $case / $pane / $bounds",
                    bounds.left >= pane.left && bounds.right <= pane.right)
                assertTrue("$name is vertically clipped: $case / $pane / $bounds",
                    bounds.top >= pane.top && bounds.bottom <= pane.bottom)
            }
            for (i in controls.indices) for (j in i + 1 until controls.size) {
                assertDoesNotOverlap(controls[i].second, controls[j].second,
                    "${controls[i].first} / ${controls[j].first}: $case")
            }
            if (style == AppStyle.Glass) {
                val badge = rule.onNodeWithTag("home-badge").getBoundsInRoot()
                assertTrue("badge is horizontally clipped: $case / $pane / $badge",
                    badge.left >= pane.left && badge.right <= pane.right)
                for ((name, bounds) in controls.filter { it.first != "switcher" }) {
                    assertDoesNotOverlap(badge, bounds, "badge / $name: $case")
                }
            }
            for ((name, bounds) in controls.filter { it.first != "switcher" }) {
                assertEquals("$name lost its 48dp target: $case", 48.dp, bounds.right - bounds.left)
            }
            if (w == 200.dp) {
                assertTrue("200dp must stack below the face: $case / $face / $switcher", switcher.top >= face.bottom)
                assertTrue("200dp must stack below the action: $case / $new / $switcher", switcher.top >= new.bottom)
            }
            if (w == 360.dp && scale == 1f && trailingActions == 1) {
                assertTrue("360dp must stay inline: $case / $face / $switcher",
                    switcher.top < face.bottom && switcher.bottom > face.top)
            }
            if (style == AppStyle.Glass && scale == 1f && trailingActions == 2 && (w == 360.dp || w == 384.dp)) {
                assertTrue("Glass must use a one-row gap layout: $case / $face / $switcher",
                    switcher.top < face.bottom && switcher.bottom > face.top)
            }
            val beforeFace = faceClicks
            val beforeNew = newClicks
            val beforeOptions = optionClicks
            rule.onNodeWithContentDescription("Sidebar").performClick()
            rule.onNodeWithContentDescription("New session").performClick()
            if (trailingActions == 2) rule.onNodeWithContentDescription("List options").performClick()
            placedText("Assistant").performClick()
            placedText(agentsLabel).performClick()
            assertEquals("face click: $case", beforeFace + 1, faceClicks)
            assertEquals("new click: $case", beforeNew + 1, newClicks)
            if (trailingActions == 2) assertEquals("options click: $case", beforeOptions + 1, optionClicks)
            assertEquals("segment clicks: $case", listOf("assistant", "agents"), selections.takeLast(2))
        }
    }

    private fun placedText(text: String, useUnmergedTree: Boolean = false) =
        rule.onAllNodesWithText(text, useUnmergedTree = useUnmergedTree)
            .filter(SemanticsMatcher("placed in the top bar") { it.boundsInRoot.top > 0f }).onFirst()

    private fun assertTextNotEllipsized(text: String, case: String, expectedSize: Float? = null) {
        val layouts = mutableListOf<TextLayoutResult>()
        placedText(text, useUnmergedTree = true)
            .performSemanticsAction(SemanticsActions.GetTextLayoutResult) { val _ = it(layouts) }
        val result = layouts.single()
        val actualSize = result.layoutInput.style.fontSize.value * result.layoutInput.density.fontScale
        assertTrue("$text is ellipsized: $case; expectedSize=$expectedSize actualSize=$actualSize layoutWidth=${result.size.width}", !result.isLineEllipsized(0))
        expectedSize?.let { assertEquals("$text effective font size differs from main: $case", it, actualSize, 0.001f) }
    }

    @Test fun glassWideSwitcherIsCenteredWithOneAndTwoTrailingActions() {
        var trailingActions by mutableStateOf(1)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                Box(Modifier.width(400.dp).height(120.dp).testTag("pane")) {
                    HomeNavigation(
                        items = listOf(
                            NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                            NavItem("agents", "Agents", PhIcons.Robot, badge = 2),
                        ),
                        selected = "agents", onSelect = {},
                        leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) },
                        trailing = {
                            JIconButton(PhIcons.NotePencil, "New session", {})
                            if (trailingActions == 2) JIconButton(PersonaIcons.moreMenu, "List options", {})
                        },
                    )
                }
            }
        }
        for (count in 1..2) {
            rule.runOnIdle { trailingActions = count }
            rule.waitForIdle()
            val pane = rule.onNodeWithTag("pane").getBoundsInRoot()
            val face = rule.onNodeWithContentDescription("Sidebar").getBoundsInRoot()
            val switcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            assertTrue("switcher is not inline with $count trailing actions: $face / $switcher",
                switcher.top < face.bottom && switcher.bottom > face.top)
            assertEquals("switcher is not centered with $count trailing actions",
                (pane.left.value + pane.right.value) / 2,
                (switcher.left.value + switcher.right.value) / 2, 0.51f)
        }
    }

    @Test fun glassExactFitCompactBadgeClearsOneTrailingAction() = glassExactFitCompactBadgeClearsTrailingActions(1)

    @Test fun glassExactFitCompactBadgeClearsTwoTrailingActions() = glassExactFitCompactBadgeClearsTrailingActions(2)

    private fun glassExactFitCompactBadgeClearsTrailingActions(trailingActions: Int) {
        var width by mutableStateOf(200.dp)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                Box(Modifier.width(width).height(140.dp).testTag("pane")) {
                    HomeNavigation(
                        items = listOf(
                            NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                            NavItem("agents", "Agents", PhIcons.Robot, badge = 2),
                        ),
                        selected = "agents", onSelect = {},
                        leading = { Box(Modifier.width(24.dp).height(48.dp).testTag("compact-leading")) },
                        trailing = {
                            JIconButton(PhIcons.NotePencil, "New session", {})
                            if (trailingActions == 2) JIconButton(PersonaIcons.moreMenu, "List options", {})
                        },
                    )
                }
            }
        }
        var verified = false
        for (w in 200..500) {
            rule.runOnIdle { width = w.dp }
            rule.waitForIdle()
            if (rule.onAllNodesWithTag("home-level-gap-compact").fetchSemanticsNodes().isEmpty()) continue
            verified = true
            val case = "exact-fit compact width=$w trailing=$trailingActions"
            val pane = rule.onNodeWithTag("pane").getBoundsInRoot()
            val leading = rule.onNodeWithTag("compact-leading").getBoundsInRoot()
            val switcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            val badge = rule.onNodeWithTag("home-badge").getBoundsInRoot()
            val trailing = rule.onNodeWithContentDescription("New session").getBoundsInRoot()
            assertEquals("the first gap-compact width must be an exact fit: $case / $switcher / $trailing",
                0f, trailing.left.value - switcher.right.value, 0.51f)
            assertTrue("badge is clipped by the pane: $case / $pane / $badge",
                badge.left >= pane.left && badge.right <= pane.right)
            assertDoesNotOverlap(badge, leading, "badge / leading: $case")
            assertDoesNotOverlap(badge, trailing, "badge / trailing: $case")
            assertTrue("capsule reaches the trailing control: $case / $switcher / $trailing",
                switcher.right <= trailing.left)
            if (trailingActions == 2) {
                val options = rule.onNodeWithContentDescription("List options").getBoundsInRoot()
                assertDoesNotOverlap(badge, options, "badge / options: $case")
            }
            break
        }
        assertTrue("no exact-fit gap-compact case found for $trailingActions trailing actions", verified)
    }

    @Test fun homeSwitcherOccupiedBoundsUseTheMeasuredBadge() {
        assertEquals("full badge outside the capsule",
            HomeSwitcherOccupiedBounds(left = 0, right = 108),
            homeSwitcherOccupiedBounds(capsuleWidth = 100, badgeWidth = 40, badgeOffset = 8))
        assertEquals("compact badge inside the capsule",
            HomeSwitcherOccupiedBounds(left = 0, right = 100),
            homeSwitcherOccupiedBounds(capsuleWidth = 100, badgeWidth = 40, badgeOffset = 0))
        assertEquals("a wide badge can occupy space before the capsule",
            HomeSwitcherOccupiedBounds(left = -20, right = 100),
            homeSwitcherOccupiedBounds(capsuleWidth = 100, badgeWidth = 120, badgeOffset = 0))
        assertEquals("measured badge bounds can extend on both sides",
            HomeSwitcherOccupiedBounds(left = -32, right = 108),
            homeSwitcherOccupiedBounds(capsuleWidth = 100, badgeWidth = 140, badgeOffset = 8))
    }

    @Test fun glassStackedSwitcherStaysCenteredWithBadge() {
        var trailingActions by mutableStateOf(1)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                Box(Modifier.width(200.dp).height(160.dp).testTag("pane")) {
                    HomeNavigation(
                        items = listOf(
                            NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                            NavItem("agents", "Agents", PhIcons.Robot, badge = 2),
                        ),
                        selected = "agents", onSelect = {},
                        leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) },
                        trailing = {
                            JIconButton(PhIcons.NotePencil, "New session", {})
                            if (trailingActions == 2) JIconButton(PersonaIcons.moreMenu, "List options", {})
                        },
                    )
                }
            }
        }
        for (count in 1..2) {
            rule.runOnIdle { trailingActions = count }
            rule.waitForIdle()
            val pane = rule.onNodeWithTag("pane").getBoundsInRoot()
            val switcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            rule.onNodeWithTag("home-level-stacked").assertExists()
            assertEquals("stacked switcher shifted left with $count trailing actions",
                (pane.left.value + pane.right.value) / 2,
                (switcher.left.value + switcher.right.value) / 2, 0.51f)
        }
    }

    @Test fun stackedSwitchersAreCenteredWithShortAndLongLabels() {
        var style by mutableStateOf(AppStyle.Glass)
        var assistant by mutableStateOf("Assistant")
        var width by mutableStateOf(200.dp)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = 1.45f)) {
                PersonaTheme(style = style, reduceMotion = true) {
                    Box(Modifier.width(width).height(200.dp).testTag("pane")) {
                        HomeNavigation(
                            items = listOf(
                                NavItem("assistant", assistant, PhIcons.ChatCircle),
                                NavItem("agents", "Agents", PhIcons.Robot, badge = 2),
                            ),
                            selected = "agents", onSelect = {},
                            leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) },
                            trailing = {
                                JIconButton(PhIcons.NotePencil, "New session", {})
                                JIconButton(PersonaIcons.moreMenu, "List options", {})
                            },
                        )
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (label in listOf("Assistant", "助手")) {
            for (w in listOf(200.dp, 280.dp, 320.dp, 360.dp)) {
                rule.runOnIdle { style = s; assistant = label; width = w }
                rule.waitForIdle()
                val expectedStacked = when {
                    w == 200.dp -> true
                    s == AppStyle.Brief && label == "助手" && w >= 320.dp -> false
                    s == AppStyle.Board && label == "助手" && w == 360.dp -> false
                    else -> true
                }
                val stacked = rule.onAllNodesWithTag("home-level-stacked").fetchSemanticsNodes().isNotEmpty()
                assertEquals("layout at the capped scale: $s $label $w", expectedStacked, stacked)
                if (!expectedStacked) continue
                val pane = rule.onNodeWithTag("pane").getBoundsInRoot()
                val switcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
                val paneCenter = (pane.left.value + pane.right.value) / 2
                val switcherCenter = (switcher.left.value + switcher.right.value) / 2
                assertTrue("$s $label $w stacked center is off by ${switcherCenter - paneCenter}dp",
                    kotlin.math.abs(switcherCenter - paneCenter) <= 0.51f)
            }
        }
    }

    @Test fun glassStackedCapsuleDoesNotExceedMainFullWidth() {
        var width by mutableStateOf(200.dp)
        var fontScale by mutableStateOf(1.3f)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = fontScale)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    Box(Modifier.width(width).height(200.dp).testTag("pane")) {
                        HomeNavigation(
                            items = listOf(
                                NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                                NavItem("agents", "Agents", PhIcons.Robot, badge = 2),
                            ),
                            selected = "agents", onSelect = {},
                            leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) },
                            trailing = {
                                JIconButton(PhIcons.NotePencil, "New session", {})
                                JIconButton(PersonaIcons.moreMenu, "List options", {})
                            },
                        )
                    }
                }
            }
        }
        for (w in listOf(200.dp, 240.dp, 280.dp, 320.dp, 360.dp, 411.dp)) for (scale in listOf(1.3f, 1.45f, 2f)) {
            rule.runOnIdle { width = w; fontScale = scale }
            rule.waitForIdle()
            rule.onNodeWithTag("home-level-stacked").assertExists()
            val switcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            val scale = (minOf(fontScale, InterfaceFontScaleCap) * HomeTokens.shellTextStep).coerceAtMost(HomeTokens.shellScaleMaximum)
            val mainSegment = HomeTokens.segmentWidth * scale + if (fontScale > 1f) Tokens.Space.s else 0.dp
            val fullWidth = mainSegment * 2 + Tokens.Space.xs * 2
            assertTrue("stacked capsule exceeds main full width: $w / $fontScale / $switcher / $fullWidth",
                switcher.right - switcher.left <= fullWidth + 1.dp)
            if (w >= 320.dp) assertEquals("stacked capsule must equal full width when it fits: $w / $fontScale",
                fullWidth.value, (switcher.right - switcher.left).value, 1f)
        }
    }

    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-xhdpi")
    @Test fun glass240Dp145UsesBaseSizeWhenItFits() {
        var expectedSize = 0f
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = 1.45f)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    val density = LocalDensity.current
                    val scale = (density.fontScale * HomeTokens.shellTextStep).coerceAtMost(HomeTokens.shellScaleMaximum)
                    val base = PersonaTheme.type.body
                    val labelStyle = base.copy(fontSize = base.fontSize * HomeTokens.shellTextStep,
                        fontWeight = androidx.compose.ui.text.font.FontWeight.SemiBold)
                    val measured = androidx.compose.ui.text.rememberTextMeasurer().measure("Agents", style = labelStyle, maxLines = 1)
                    val available = with(density) { (HomeTokens.segmentWidth * scale).toPx() }
                    val fit = (available / measured.size.width.coerceAtLeast(1)).coerceAtMost(1f)
                    expectedSize = maxOf(Tokens.Type.Label.size.value, labelStyle.fontSize.value * fit) * density.fontScale
                    Box(Modifier.width(240.dp).height(200.dp)) {
                        HomeNavigation(
                            items = listOf(
                                NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                                NavItem("agents", "Agents", PhIcons.Robot, badge = 2),
                            ),
                            selected = "agents", onSelect = {},
                            leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) },
                            trailing = {
                                JIconButton(PhIcons.NotePencil, "New session", {})
                                JIconButton(PersonaIcons.moreMenu, "List options", {})
                            },
                        )
                    }
                }
            }
        }
        val layouts = mutableListOf<TextLayoutResult>()
        placedText("Agents", useUnmergedTree = true)
            .performSemanticsAction(SemanticsActions.GetTextLayoutResult) { val _ = it(layouts) }
        val result = layouts.single()
        assertTrue("Agents is ellipsized at 240dp / 1.45", !result.isLineEllipsized(0))
        assertEquals("240dp / 1.45 shrank below main", expectedSize,
            result.layoutInput.style.fontSize.value * result.layoutInput.density.fontScale, 0.001f)
    }

    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-xhdpi")
    @Test fun glassStackedWithoutBadgeKeepsCellsEqualAndInsideContent() {
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = 1.3f)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    Box(Modifier.width(220.dp).height(160.dp).testTag("pane")) {
                        HomeNavigation(
                            items = listOf(
                                NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                                NavItem("agents", "Agents", PhIcons.Robot),
                            ),
                            selected = "agents", onSelect = {},
                            leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) },
                            trailing = {
                                JIconButton(PhIcons.NotePencil, "New session", {})
                                JIconButton(PersonaIcons.moreMenu, "List options", {})
                            },
                        )
                    }
                }
            }
        }
        val pane = rule.onNodeWithTag("pane").getBoundsInRoot()
        val switcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
        val assistant = rule.onNodeWithText("Assistant").getBoundsInRoot()
        val agents = rule.onNodeWithText("Agents").getBoundsInRoot()
        assertEquals("stacked cells must have equal widths", assistant.right - assistant.left, agents.right - agents.left)
        assertTrue("capsule exceeds content width: $pane / $switcher",
            switcher.left >= pane.left + Tokens.Space.l && switcher.right <= pane.right - Tokens.Space.l)
    }

    @Config(sdk = [35], qualifiers = "en-w500dp-h900dp-xhdpi")
    @Test fun narrowLabelEllipsisMatrix() {
        var style by mutableStateOf(AppStyle.Glass)
        var width by mutableStateOf(200.dp)
        var fontScale by mutableStateOf(1.3f)
        var badge by mutableStateOf(0)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = fontScale)) {
                PersonaTheme(style = style, reduceMotion = true) {
                    Box(Modifier.width(width).height(220.dp)) {
                        HomeNavigation(
                            items = listOf(
                                NavItem("assistant", "Assistant", PhIcons.ChatCircle),
                                NavItem("agents", "Agents", PhIcons.Robot, badge = badge),
                            ),
                            selected = "agents", onSelect = {},
                            leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) },
                            trailing = {
                                JIconButton(PhIcons.NotePencil, "New session", {})
                                JIconButton(PersonaIcons.moreMenu, "List options", {})
                            },
                        )
                    }
                }
            }
        }
        for (badgeValue in listOf(0, 2)) for (s in AppStyle.entries) for (w in listOf(200.dp, 220.dp, 240.dp, 280.dp)) for (scale in listOf(1.3f, 1.45f, 2f)) {
            rule.runOnIdle { badge = badgeValue; style = s; width = w; fontScale = scale }
            rule.waitForIdle()
            val expected = when (s) {
                AppStyle.Glass -> (scale == 2f && w <= 240.dp) to (badgeValue == 2 && w == 200.dp && scale == 2f)
                AppStyle.Brief -> false to (scale == 2f || (badgeValue == 2 && w == 200.dp && scale >= 1.45f))
                AppStyle.Board -> ((w == 200.dp && scale >= 1.45f) || ((w == 220.dp || w == 240.dp) && scale == 2f)) to
                    (scale == 2f || (badgeValue == 2 && w == 200.dp && scale >= 1.45f))
            }
            val agentsLabel = if (s == AppStyle.Glass || badgeValue == 0) "Agents" else "Agents 2"
            if (!expected.first) assertFalse("$s $w $scale badge=$badgeValue Assistant must not be ellipsized", isTextEllipsized("Assistant"))
            if (!expected.second) assertFalse("$s $w $scale badge=$badgeValue $agentsLabel must not be ellipsized", isTextEllipsized(agentsLabel))
        }
    }

    private fun isTextEllipsized(text: String): Boolean {
        val layouts = mutableListOf<TextLayoutResult>()
        placedText(text, useUnmergedTree = true)
            .performSemanticsAction(SemanticsActions.GetTextLayoutResult) { val _ = it(layouts) }
        return layouts.single().isLineEllipsized(0)
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
    @Test fun board400DpKeepsOriginalVerticalPosition() {
        var fontScale by mutableStateOf(1f)
        val items = listOf(
            NavItem("assistant", "Assistant", PhIcons.ChatCircle),
            NavItem("agents", "Agents", PhIcons.Robot),
        )
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = fontScale)) {
                PersonaTheme(style = AppStyle.Board, reduceMotion = true) {
                    Column {
                        Box(Modifier.width(400.dp).testTag("old-pane")) {
                            Row(
                                Modifier.fillMaxWidth().statusBarsPadding().padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                            ) {
                                JPersonaButton(PersonaState.Idle, "Sidebar", {})
                                Box(Modifier.weight(1f)) {
                                    JSegmented(items.map { JSegment(it.label) }, 0, {}, Modifier.testTag("old-switcher"))
                                }
                            }
                        }
                        Box(Modifier.width(400.dp).testTag("new-pane")) {
                            HomeNavigation(items, "assistant", {}, leading = { JPersonaButton(PersonaState.Idle, "Sidebar", {}) })
                        }
                    }
                }
            }
        }
        for (scale in listOf(1f, 1.25f, 1.5f, 1.75f, 2f)) {
            rule.runOnIdle { fontScale = scale }
            rule.waitForIdle()
            val oldPane = rule.onNodeWithTag("old-pane").getBoundsInRoot()
            val newPane = rule.onNodeWithTag("new-pane").getBoundsInRoot()
            val oldSwitcher = rule.onNodeWithTag("old-switcher").getBoundsInRoot()
            val newSwitcher = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            assertEquals("Board's 400dp vertical position changed at fontScale=$scale",
                oldSwitcher.top - oldPane.top, newSwitcher.top - newPane.top)
        }
    }

    private fun assertDoesNotOverlap(a: DpRect, b: DpRect, message: String) {
        val separate = a.right <= b.left || b.right <= a.left || a.bottom <= b.top || b.bottom <= a.top
        assertTrue("$message: $a / $b", separate)
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

    @Test fun personaButtonRoomKeepsSwitcherPositionWhenButtonIsHiddenInEveryStyle() {
        val items = listOf(
            NavItem("assistant", "Assistant", PhIcons.ChatCircle),
            NavItem("agents", "Agents", PhIcons.Robot),
        )
        var style by mutableStateOf(AppStyle.Glass)
        var showButton by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(style = style, reduceMotion = true) {
                Box(Modifier.width(560.dp)) {
                    HomeNavigation(
                        items = items,
                        selected = "assistant",
                        onSelect = {},
                        leading = {
                            if (showButton) JPersonaButton(PersonaState.Idle, "Sidebar", {})
                            else JPersonaButtonRoom()
                        },
                    )
                }
            }
        }
        for (candidate in AppStyle.entries) {
            rule.runOnIdle { style = candidate; showButton = true }
            rule.waitForIdle()
            val shownBounds = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            rule.onNodeWithContentDescription("Sidebar").assertExists()

            rule.runOnIdle { showButton = false }
            rule.waitForIdle()
            val hiddenBounds = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            rule.onAllNodesWithContentDescription("Sidebar").assertCountEquals(0)
            assertEquals("Switcher shifted horizontally in $candidate", shownBounds.left.value, hiddenBounds.left.value, 0.01f)
            assertEquals("Switcher shifted vertically in $candidate", shownBounds.top.value, hiddenBounds.top.value, 0.01f)
        }
    }
}
