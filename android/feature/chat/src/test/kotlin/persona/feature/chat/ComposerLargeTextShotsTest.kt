package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.ComposerSuggestion
import persona.core.designsystem.ComposerSuggestions
import persona.core.designsystem.JAction
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * The composer's / row and + sheet at 360dp, in every language, style, appearance and text size. No text line is
 * ellipsized and no labels overlap. Fitting default and capped rows keep their existing inline layout. The existing English and Japanese
 * /clear details already ellipsize at the default size, so the measured-width rule moves those below the command too.
 * PNGs go to `build/composer-large-text-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h900dp-xhdpi")
class ComposerLargeTextShotsTest {
    @get:Rule val rule = createComposeRule()

    private enum class Scene { Slash, Plus }

    private fun out() = File("build/composer-large-text-shots").apply { mkdirs() }

    @Config(sdk = [35], qualifiers = "en-w360dp-h900dp-xhdpi")
    @Test fun slashEn() = matrix(Scene.Slash, "en", 1f)
    @Config(sdk = [35], qualifiers = "en-w360dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun slashEnMax() = matrix(Scene.Slash, "en", 2f)
    @Config(sdk = [35], qualifiers = "zh-w360dp-h900dp-xhdpi")
    @Test fun slashZh() = matrix(Scene.Slash, "zh-Hans", 1f)
    @Config(sdk = [35], qualifiers = "zh-w360dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun slashZhMax() = matrix(Scene.Slash, "zh-Hans", 2f)
    @Config(sdk = [35], qualifiers = "ja-w360dp-h900dp-xhdpi")
    @Test fun slashJa() = matrix(Scene.Slash, "ja", 1f)
    @Config(sdk = [35], qualifiers = "ja-w360dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun slashJaMax() = matrix(Scene.Slash, "ja", 2f)
    @Config(sdk = [35], qualifiers = "en-w360dp-h900dp-xhdpi")
    @Test fun plusEn() = matrix(Scene.Plus, "en", 1f)
    @Config(sdk = [35], qualifiers = "en-w360dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun plusEnMax() = matrix(Scene.Plus, "en", 2f)
    @Config(sdk = [35], qualifiers = "zh-w360dp-h900dp-xhdpi")
    @Test fun plusZh() = matrix(Scene.Plus, "zh-Hans", 1f)
    @Config(sdk = [35], qualifiers = "zh-w360dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun plusZhMax() = matrix(Scene.Plus, "zh-Hans", 2f)
    @Config(sdk = [35], qualifiers = "ja-w360dp-h900dp-xhdpi")
    @Test fun plusJa() = matrix(Scene.Plus, "ja", 1f)
    @Config(sdk = [35], qualifiers = "ja-w360dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun plusJaMax() = matrix(Scene.Plus, "ja", 2f)

    private fun matrix(scene: Scene, language: String, fontScale: Float) {
        val resources = ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val clear = SlashCommand.clearCommand(resources)
        val labels = listOf(R.string.chat_composer_photos, R.string.chat_composer_take_photo,
            R.string.chat_composer_files, R.string.chat_mention).map(resources::getString)
        val spoken = resources.getString(persona.core.designsystem.R.string.designsystem_with_state, clear.word, clear.detail)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                when (scene) {
                    Scene.Slash -> Box(Modifier.testTag("shot").width(360.dp).background(PersonaTheme.palette.background)) {
                        ComposerSuggestions(listOf(ComposerSuggestion(clear.word, clear.detail, destructive = true)), onPick = {})
                    }
                    Scene.Plus -> ChatActionsSheet(labels.map { JAction(it, onClick = {}) }, onDismiss = {},
                        title = resources.getString(R.string.chat_composer_menu_title))
                }
            }
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { style = appearance; dark = night }
            rule.mainClock.advanceTimeBy(1_000)
            rule.waitForIdle()
            val where = "$language ${scene.name} ${appearance.name} dark=$night scale=$fontScale"
            if (scene == Scene.Slash) {
                val detail = assertComplete(clear.detail, where)
                val frame = rule.onNodeWithContentDescription(spoken).fetchSemanticsNode().boundsInRoot
                val wordFrame = rule.onNodeWithText(clear.word, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                val detailFrame = rule.onNodeWithText(clear.detail, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                assertFalse("$where: the command and detail overlap", wordFrame.overlaps(detailFrame))
                if (fontScale == 1f && language == "zh-Hans") {
                    // The fitting default row is pixel-for-pixel the original one (origin/main at xhdpi).
                    assertEquals("$where: the fitting default frame changed", Rect(0f, 8f, 720f, 88f), frame)
                } else if (fontScale == 2f && language == "zh-Hans") {
                    assertTrue("$where: the capped detail fits beside the command", detailFrame.top < wordFrame.bottom)
                } else if (fontScale == 2f) {
                    assertTrue("$where: the overflowing detail moved below the command", detailFrame.top >= wordFrame.bottom)
                }
                if (fontScale == 2f && language != "zh-Hans") {
                    assertTrue("$where: the long detail wraps instead of ellipsizing", detail.lineCount > 1)
                }
            } else {
                val textFrames = labels.map { label ->
                    assertComplete(label, where)
                    rule.onNodeWithText(label, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                }
                textFrames.zipWithNext().forEachIndexed { index, (upper, lower) ->
                    assertFalse("$where: labels $index and ${index + 1} overlap", upper.overlaps(lower))
                }
                val mentionFrame = rule.onNodeWithText(labels.last()).fetchSemanticsNode().boundsInRoot
                val mentionText = textFrames.last()
                assertTrue("$where: the label starts inside its row", mentionText.left >= mentionFrame.left)
                assertTrue("$where: the label ends inside its row", mentionText.right <= mentionFrame.right)
                assertTrue("$where: the label stays inside its row vertically", mentionText.top >= mentionFrame.top && mentionText.bottom <= mentionFrame.bottom)
                if (fontScale == 1f) {
                    // Default titleLines = 1 callers are unchanged; titleLines = 2 still takes one line when it fits.
                    val mainFrame = when (appearance) {
                        AppStyle.Glass -> Rect(32f, 1624f, 688f, 1736f)
                        AppStyle.Brief -> Rect(40f, 1624f, 680f, 1736f)
                        AppStyle.Board -> Rect(24f, 1624f, 696f, 1736f)
                    }
                    assertEquals("$where: the default + row frame changed", mainFrame, mentionFrame)
                }
            }
            val size = if (fontScale == 1f) "default" else "max"
            val name = "${scene.name.lowercase()}-$language-${appearance.name.lowercase()}-360-${if (night) "dark" else "light"}-$size.png"
            val bitmap = (if (scene == Scene.Slash) rule.onNodeWithTag("shot") else rule.onNode(isDialog()))
                .captureToImage().asAndroidBitmap()
            File(out(), name).outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }

    private fun assertComplete(text: String, where: String): TextLayoutResult {
        val layout = textLayout(text)
        assertTrue("$where: no text lines", layout.lineCount > 0)
        for (line in 0 until layout.lineCount) {
            assertFalse("$where: line $line is ellipsized", layout.isLineEllipsized(line))
        }
        return layout
    }

    private fun textLayout(text: String): TextLayoutResult {
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
            .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }
}
