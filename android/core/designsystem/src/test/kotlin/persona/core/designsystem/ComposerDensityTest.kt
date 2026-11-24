package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.icons.PhIcons
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class ComposerDensityTest {
    @get:Rule val rule = createComposeRule()

    @After fun reset() = LayoutDensity.apply(LayoutDensity.Default)

    @Test fun homeGlassFrameUsesBubbleCornersInCompactOnly() = assertBubbleCornerScale(home = true)

    @Test fun fallbackGlassFrameUsesBubbleCornersInCompactOnly() = assertBubbleCornerScale(home = false)

    @Test fun compactAgentComposerPlacesItsControlsWithoutEmptyAccessorySpace() {
        var expanded by mutableStateOf(false)
        rule.setContent {
            LayoutDensity.apply(LayoutDensity.Standard)
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ComposerFrame(
                    modifier = Modifier.width(360.dp).testTag("frame"),
                    expanded = expanded,
                    useShellSurface = true,
                    accessory = {},
                    leading = {
                        JIconButton(PhIcons.Plus, null, {}, Modifier.testTag("attach"))
                        if (expanded) JIconButton(PhIcons.Keyboard, null, {}, Modifier.testTag("keyboard"))
                    },
                ) { Box(Modifier.size(40.dp)) }
            }
        }

        val idle = rule.onNodeWithTag("frame").getUnclippedBoundsInRoot()
        val idleAttach = rule.onNodeWithTag("attach").getUnclippedBoundsInRoot()
        assertEquals(64f, (idle.bottom - idle.top).value, 0.5f)
        assertEquals(38f, ((idleAttach.right + idleAttach.left) / 2 - idle.left).value, 0.5f)
        assertEquals(32f, ((idleAttach.top + idleAttach.bottom) / 2 - idle.top).value, 0.5f)

        rule.runOnIdle { expanded = true }
        val open = rule.onNodeWithTag("frame").getUnclippedBoundsInRoot()
        val openAttach = rule.onNodeWithTag("attach").getUnclippedBoundsInRoot()
        val keyboard = rule.onNodeWithTag("keyboard").getUnclippedBoundsInRoot()
        assertEquals(116f, (open.bottom - open.top).value, 0.5f)
        assertEquals(42f, ((openAttach.right + openAttach.left) / 2 - open.left).value, 0.5f)
        assertEquals(48f, (keyboard.right - keyboard.left).value, 0.5f)
        assertEquals(0f, (keyboard.left - openAttach.right).value, 0.5f)
        assertEquals(36f, (open.bottom - (keyboard.bottom + keyboard.top) / 2).value, 0.5f)
    }

    @Test fun emptyAccessoryAddsNoExtraHeightInAnyStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var home by mutableStateOf(false)
        rule.setContent {
            LayoutDensity.apply(LayoutDensity.Standard)
            CompositionLocalProvider(LocalHomeChrome provides if (home) HomeChrome(emptyList(), "chat", {}, {}) else null) {
                PersonaTheme(style = style, reduceMotion = true) {
                    Column(Modifier.width(360.dp)) {
                        ComposerFrame(Modifier.testTag("null-acc"), accessory = null) { Text("Message") }
                        ComposerFrame(Modifier.testTag("empty-acc"), accessory = {}) { Text("Message") }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (isHome in listOf(false, true)) {
            rule.runOnIdle { style = s; home = isHome }
            val withoutAcc = rule.onNodeWithTag("null-acc").getUnclippedBoundsInRoot().height
            val withEmpty = rule.onNodeWithTag("empty-acc").getUnclippedBoundsInRoot().height
            assertEquals("$s home=$isHome: empty accessory must not add vertical padding", withoutAcc.value, withEmpty.value, 0.5f)
        }
    }

    @Test fun briefAndBoardExpandedComposerKeepCompactVerticalDepthAndTouchTargets() {
        var style by mutableStateOf(AppStyle.Brief)
        var expanded by mutableStateOf(false)
        rule.setContent {
            LayoutDensity.apply(LayoutDensity.Standard)
            PersonaTheme(style = style, reduceMotion = true) {
                ComposerFrame(
                    modifier = Modifier.width(360.dp).testTag("frame"),
                    expanded = expanded,
                    accessory = {},
                    leading = {
                        JIconButton(PhIcons.Plus, "Add", {}, Modifier.testTag("add"))
                        if (expanded) JIconButton(PhIcons.At, "Mention", {}, Modifier.testTag("mention"))
                    },
                    trailing = {
                        JIconButton(PhIcons.Microphone, "Dictate", {}, Modifier.testTag("mic"))
                        SendButton({}, Modifier.testTag("send"))
                    },
                ) {
                    Text("Ask Persona", Modifier.testTag("field"))
                }
            }
        }
        for (s in listOf(AppStyle.Brief, AppStyle.Board)) {
            rule.runOnIdle { style = s; expanded = false }
            val idleFrame = rule.onNodeWithTag("frame").getUnclippedBoundsInRoot()
            val idleSend = rule.onNodeWithTag("send").getUnclippedBoundsInRoot()
            assertTrue("$s idle send touch height >= 48dp", idleSend.height >= Tokens.MinTouch.android)
            // Compact idle height: top(4) + 48dp input row + bottom(6) = 58dp (down from 64–72dp).
            assertTrue("$s idle frame height (${idleFrame.height}) <= 60dp", idleFrame.height <= 60.dp)

            rule.runOnIdle { expanded = true }
            val openFrame = rule.onNodeWithTag("frame").getUnclippedBoundsInRoot()
            val openAdd = rule.onNodeWithTag("add").getUnclippedBoundsInRoot()
            val openMention = rule.onNodeWithTag("mention").getUnclippedBoundsInRoot()
            val openSend = rule.onNodeWithTag("send").getUnclippedBoundsInRoot()
            assertTrue("$s expanded add touch height >= 48dp", openAdd.height >= Tokens.MinTouch.android)
            assertTrue("$s expanded mention touch height >= 48dp", openMention.height >= Tokens.MinTouch.android)
            assertTrue("$s expanded toolbar sits below input row without overlap", openAdd.top >= openSend.bottom - 0.5.dp)
            // Compact expanded height: top(4) + 48dp input + 48dp toolbar + bottom(2) = 102dp (down from 124–136dp).
            assertTrue("$s expanded frame height (${openFrame.height}) <= 106dp", openFrame.height <= 106.dp)
        }
    }

    @Test fun singleItemQueueStripStaysCompactAndInteractiveAcrossStylesAndLargeText() {
        var style by mutableStateOf(AppStyle.Brief)
        var fontScale by mutableStateOf(1f)
        val picked = mutableListOf<Int>()
        rule.setContent {
            LayoutDensity.apply(LayoutDensity.Standard)
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = fontScale)) {
                PersonaTheme(style = style, reduceMotion = true) {
                    QueueStrip(
                        title = "排队中 1 条 · Persona 忙完会依次处理",
                        items = listOf("为什么这条排队消息占了这么高"),
                        onItem = { picked += it },
                        modifier = Modifier.width(360.dp).testTag("queue"),
                        initiallyExpanded = true,
                    )
                }
            }
        }
        for (s in AppStyle.entries) {
            rule.runOnIdle { style = s; fontScale = 1f }
            val strip = rule.onNodeWithTag("queue").getUnclippedBoundsInRoot()
            val row = rule.onNodeWithContentDescription("第 1 条：为什么这条排队消息占了这么高")
            val rowBounds = row.getUnclippedBoundsInRoot()
            val minRow = if (s == AppStyle.Glass) JChipTokens.height else Tokens.Size.slimBar
            assertTrue("$s queue row height (${rowBounds.height}) >= $minRow", rowBounds.height >= minRow)
            if (s != AppStyle.Glass) {
                // Single-line PlainQueueStrip tightened from ~88dp to <= 76dp.
                assertTrue("$s single-item queue height (${strip.height}) <= 76dp", strip.height <= 76.dp)
            }
            picked.clear()
            row.performClick()
            rule.waitForIdle()
            assertEquals("$s row click delivers index", listOf(0), picked)

            // 2.0x system font scale stays readable, unclipped, and clickable.
            rule.runOnIdle { fontScale = 2f }
            row.assertIsDisplayed()
            val largeBounds = row.getUnclippedBoundsInRoot()
            assertTrue("$s 2.0x row fits inside strip bounds", largeBounds.top >= strip.top && largeBounds.bottom <= rule.onNodeWithTag("queue").getUnclippedBoundsInRoot().bottom)
        }
    }

    @Test fun writesHomeGlassFramesForInspection() {
        var compact by mutableStateOf(false)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            LayoutDensity.apply(if (compact) LayoutDensity.Compact else LayoutDensity.Standard)
            CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "chat", {}, {})) {
                CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale = fontScale)) {
                    PersonaTheme(darkTheme = dark, style = AppStyle.Glass, reduceMotion = true) {
                        Column(Modifier.testTag("shot").width(360.dp).background(PersonaTheme.palette.background)) {
                            ComposerFrame { Text("Message") }
                        }
                    }
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/composer-density-shots").apply { mkdirs() }
        for (isCompact in listOf(false, true)) for (isDark in listOf(false, true)) for (scale in listOf(1f, 2f)) {
            compact = isCompact
            dark = isDark
            fontScale = scale
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val name = "composer-${if (isCompact) "compact" else "standard"}-${if (isDark) "dark" else "light"}${if (scale == 1f) "" else "-large-text"}.png"
            File(out, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    private fun assertBubbleCornerScale(home: Boolean) {
        var compact by mutableStateOf(false)
        rule.setContent {
            LayoutDensity.apply(if (compact) LayoutDensity.Compact else LayoutDensity.Standard)
            Column(Modifier.background(Backdrop)) {
                CompositionLocalProvider(
                    LocalHomeChrome provides if (home) HomeChrome(emptyList(), "chat", {}, {}) else null,
                ) {
                    PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                        ComposerFrame(Modifier.testTag("composer").width(320.dp)) { Text("Message") }
                    }
                }
            }
        }
        val standard = cornerPixel()

        compact = true
        rule.waitForIdle()
        assertNotEquals("the Compact frame covers the probe that Standard leaves outside its corner", standard, cornerPixel())

        compact = false
        rule.waitForIdle()
        assertEquals("returning to Standard restores the original corner", standard, cornerPixel())
    }

    private fun cornerPixel(): Int {
        val bitmap = rule.onNodeWithTag("composer").captureToImage().asAndroidBitmap()
        val x = with(rule.density) { (Tokens.Space.m + 4.dp).toPx() }.toInt()
        val y = with(rule.density) { (Tokens.Space.s + 4.dp).toPx() }.toInt()
        return bitmap.getPixel(x, y)
    }

    private companion object {
        val Backdrop = Color(0xFFFF00FF)
    }
}
