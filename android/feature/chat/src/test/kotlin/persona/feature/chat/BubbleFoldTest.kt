package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalSelectionAbove
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class BubbleFoldTest {
    @get:Rule val rule = createComposeRule()

    private fun linesOf(count: Int, prefix: String = "Line"): String =
        (1..count).joinToString("\n") { "$prefix $it: reviewing deployment notes and verifying checks." }

    @Test fun ruleThresholdsAndCjkEmEstimatesMatchIos() {
        assertEquals(BubbleFoldRule.Regular, BubbleFoldRule.of(1f))
        assertEquals(BubbleFoldRule.Regular, BubbleFoldRule.of(1.3f))
        assertEquals(BubbleFoldRule.Large, BubbleFoldRule.of(1.35f))
        assertEquals(BubbleFoldRule.Large, BubbleFoldRule.of(2f))
        assertEquals(14, BubbleFoldRule.Regular.limit)
        assertEquals(10, BubbleFoldRule.Regular.shown)
        assertEquals(9, BubbleFoldRule.Large.limit)
        assertEquals(5, BubbleFoldRule.Large.shown)

        assertEquals(0.4f, BubbleFoldRule.ems('a'.code), 0.001f)
        assertEquals(1f, BubbleFoldRule.ems('中'.code), 0.001f)
        assertEquals(1f, BubbleFoldRule.ems("🚀".codePointAt(0)), 0.001f)

        assertFalse(BubbleFoldRule.mayFold("Short hello", 1f))
        assertFalse(BubbleFoldRule.mayFold((1..14).joinToString("\n") { "short" }, 1f))
        assertTrue(BubbleFoldRule.mayFold((1..15).joinToString("\n") { "short" }, 1f))
        assertFalse(BubbleFoldRule.mayFold((1..9).joinToString("\n") { "short" }, 1.5f))
        assertTrue(BubbleFoldRule.mayFold((1..10).joinToString("\n") { "short" }, 1.5f))

        assertNull(BubbleFoldRule.start("one\ntwo\nthree", lines = 5))
        val sliced = BubbleFoldRule.start((1..20).joinToString("\n") { "line $it" }, lines = 16)
        assertNotNull(sliced)
        assertTrue(sliced!!.lines().size <= 16)
    }

    @Test fun shortUserBubbleDoesNotFold() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column(Modifier.width(380.dp).padding(Tokens.Space.l)) {
                    MergedUserBubble(listOf(Item("short-1", role = "user", kind = "text", text = linesOf(6))))
                }
            }
        }
        rule.onNodeWithTag("bubble-fold-expand").assertDoesNotExist()
        rule.onNodeWithTag("bubble-fold-collapse").assertDoesNotExist()
    }

    @Test fun longUserBubbleFoldsAndTogglesWithFullTouchTarget() {
        val longText = linesOf(24)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                Column(
                    Modifier.testTag("fold-shot").width(380.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                ) {
                    MergedUserBubble(
                        listOf(Item("long-1", role = "user", kind = "text", text = longText, clientMsgId = "c1", ts = "2026-11-22T10:30:00Z")),
                    )
                }
            }
        }
        val expand = rule.onNodeWithTag("bubble-fold-expand")
        expand.assertIsDisplayed()
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
        rule.onNodeWithText("Show more").assertIsDisplayed()
        val foldedHeight = rule.onNodeWithTag("fold-shot").fetchSemanticsNode().boundsInRoot.height
        val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }
        assertTrue("Show more keeps Android's 48dp touch target", expand.fetchSemanticsNode().touchBoundsInRoot.height >= minTouchPx)

        val out = File(System.getProperty("persona.shots") ?: "build/bubble-fold-shots").apply { mkdirs() }
        for (night in listOf(false, true)) {
            dark = night
            rule.waitForIdle()
            val bmp = rule.onNodeWithTag("fold-shot").captureToImage().asAndroidBitmap()
            File(out, "folded-${if (night) "dark" else "light"}.png").outputStream().use { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        dark = false
        rule.waitForIdle()

        expand.performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("bubble-fold-collapse").assertIsDisplayed()
        rule.onNodeWithText("Show less").assertIsDisplayed()
        val expandedHeight = rule.onNodeWithTag("fold-shot").fetchSemanticsNode().boundsInRoot.height
        assertTrue("Expanding reveals more height ($expandedHeight > $foldedHeight)", expandedHeight > foldedHeight)

        for (night in listOf(false, true)) {
            dark = night
            rule.waitForIdle()
            val bmp = rule.onNodeWithTag("fold-shot").captureToImage().asAndroidBitmap()
            File(out, "expanded-${if (night) "dark" else "light"}.png").outputStream().use { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        dark = false
        rule.waitForIdle()

        rule.onNodeWithTag("bubble-fold-collapse").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("bubble-fold-expand").assertIsDisplayed()
        assertEquals(foldedHeight, rule.onNodeWithTag("fold-shot").fetchSemanticsNode().boundsInRoot.height, 2f)
    }

    @Test fun largeFontScaleFoldsPastNineLines() {
        val mediumText = (1..11).joinToString("\n") { "Item $it" }
        rule.setContent {
            val base = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(base.density, fontScale = 1.5f)) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                    Column(Modifier.width(380.dp).padding(Tokens.Space.l)) {
                        MergedUserBubble(listOf(Item("large-1", role = "user", kind = "text", text = mediumText)))
                    }
                }
            }
        }
        rule.onNodeWithTag("bubble-fold-expand").assertIsDisplayed()
    }

    @Test fun selectionAboveShowsFullBubbleWithoutFoldToggle() {
        val longText = linesOf(24)
        var selecting by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(LocalSelectionAbove provides selecting) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                    Column(Modifier.testTag("sel-box").width(380.dp).padding(Tokens.Space.l)) {
                        MergedUserBubble(listOf(Item("sel-1", role = "user", kind = "text", text = longText)))
                    }
                }
            }
        }
        rule.onNodeWithTag("bubble-fold-expand").assertIsDisplayed()
        val foldedHeight = rule.onNodeWithTag("sel-box").fetchSemanticsNode().boundsInRoot.height
        selecting = true
        rule.waitForIdle()
        rule.onNodeWithTag("bubble-fold-expand").assertDoesNotExist()
        val fullHeight = rule.onNodeWithTag("sel-box").fetchSemanticsNode().boundsInRoot.height
        assertTrue("Active selection exposes full bubble height", fullHeight > foldedHeight)
    }

    @Test fun streamingUserBubbleDoesNotFoldMidStream() {
        val longText = linesOf(24)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column(Modifier.width(380.dp).padding(Tokens.Space.l)) {
                    MergedUserBubble(
                        items = listOf(Item("stream-1", role = "user", kind = "text", text = longText)),
                        updates = mapOf("stream-1" to ChatTextUpdate(longText, streaming = true)),
                    )
                }
            }
        }
        rule.onNodeWithTag("bubble-fold-expand").assertDoesNotExist()
    }

    @Test fun hugeMessageOnlyLaysOutBoundedPrefixWhileFolded() {
        val hugeText = (1..2_000).joinToString("\n") { "Log line $it: status ok" } + "\nTAIL_SENTINEL_2000"
        assertTrue(BubbleFoldRule.mayFold(hugeText, 1f))
        assertTrue(BubbleFoldRule.likelyFolds(hugeText, 1f))
        val observed = mutableListOf<String>()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column(Modifier.width(380.dp).padding(Tokens.Space.l)) {
                    BubbleFold(hugeText, foldKey = "huge-1", own = true) { shown ->
                        observed += shown
                        androidx.compose.material3.Text(shown, style = PersonaTheme.type.body)
                    }
                }
            }
        }
        rule.onNodeWithTag("bubble-fold-expand").assertIsDisplayed()
        assertTrue("Folded path passes only a bounded prefix", observed.isNotEmpty())
        assertTrue(
            "Folded prefix must not exceed START_CHARACTERS or include the tail",
            observed.all { it.length <= BubbleFoldRule.START_CHARACTERS && !it.contains("TAIL_SENTINEL_2000") },
        )

        rule.onNodeWithTag("bubble-fold-expand").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("bubble-fold-collapse").assertExists()
        assertTrue("Expanding passes the full text including the tail", observed.last().contains("TAIL_SENTINEL_2000"))
    }
}
