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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.JToast
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.tasks.QuickResultUndo

/**
 * A quick result's three states (#9340): Added with Undo, Undone dimmed, and the failed Undo, where the card stays as it
 * was and the chat's toast says so. The actual Android source is present in every state. Every style and appearance, on a
 * phone's width and an opened foldable's, at default and 200% text (#6864).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class QuickResultCardShotsTest {
    @get:Rule val rule = createComposeRule()

    private val result = parsedQuickResult("""{"id":"42","title":"Dentist","start":"2026-11-13T15:30:00Z","calendar":"Personal","source_device_name":"Pixel 9","source_device_platform":"android"}""")

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/quick-result-card-shots").also { it.mkdirs() }

    @Test fun phone() = shoot("phone", 400.dp)

    @Test @Config(fontScale = 2f) fun phoneLargeText() = shoot("phone", 400.dp, fontScale = 2f)

    /** The open foldable's inner screen (the Duo's 673 × 951 dp, as the shell's other captures use). */
    @Test @Config(qualifiers = "en-w673dp-h951dp-xhdpi") fun foldOpen() = shoot("fold", 560.dp)

    @Test @Config(qualifiers = "en-w673dp-h951dp-xhdpi", fontScale = 2f)
    fun foldOpenLargeText() = shoot("fold", 560.dp, fontScale = 2f)

    private fun shoot(window: String, width: Dp, fontScale: Float = 1f) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(width).background(PersonaTheme.palette.background)
                        .padding(Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                        // The failed undo: the toast the chat shows, then the card as it stayed.
                        JToast(FeedbackNotice(stringResource(R.string.chat_tasks_quick_undo_failed), style = FeedbackStyle.Error), dismiss = {})
                        QuickResultCard(result, QuickResultUndo.Added, onUndo = {})
                        QuickResultCard(result, QuickResultUndo.Undoing, onUndo = {})
                        QuickResultCard(result, QuickResultUndo.Undone, onUndo = {})
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            // Every Undo state keeps the actual source and the existing result details.
            rule.onAllNodes(hasText("Pixel 9", substring = true), useUnmergedTree = true).assertCountEquals(3)
            rule.onAllNodes(hasText("Dentist", substring = true), useUnmergedTree = true).assertCountEquals(3)
            rule.onAllNodesWithText("Undo").assertCountEquals(2)
            rule.onAllNodesWithText("Undone").assertCountEquals(1)
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val textSize = if (fontScale == 1f) "" else "-text-200"
            File(out(), "quick-result-$window-${s.name.lowercase()}-${if (d) "dark" else "light"}$textSize.png").outputStream()
                .use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
}
