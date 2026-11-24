package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.PersonaTheme
import java.io.ByteArrayOutputStream
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class LarkImageViewerTest {
    @get:Rule val rule = createComposeRule()

    private fun image(key: String) = LarkPart.Image(AuthenticatedLarkImage(key) {
        ByteArrayOutputStream().also { Bitmap.createBitmap(8, 8, Bitmap.Config.ARGB_8888).compress(Bitmap.CompressFormat.PNG, 100, it) }.toByteArray()
    }, description = "photo $key", width = 8, height = 8)

    @Test fun theConversationsPicturesInOrderWithCovers() {
        val a = image("a"); val b = image("b"); val cover = image("c")
        val messages = listOf(
            LarkMessage("1", "u", "A", parts = listOf(LarkPart.Text("hi"), a)),
            LarkMessage("2", "u", "A", parts = listOf(b), recalled = true),
            LarkMessage("3", "u", "A", parts = listOf(LarkPart.External("Video", "https://example.com", "video", cover = cover), a)),
        )
        val all = larkImages(messages)
        assertEquals(listOf(a, cover), all)
        assertEquals(all to 1, larkGallery(all, cover))
        // One not among them (a card's) opens alone.
        assertEquals(listOf(b) to 0, larkGallery(all, b))
    }

    @Test fun aTapOpensThePicture() {
        val part = image("a")
        val opened = mutableListOf<LarkPart.Image>()
        rule.setContent {
            PersonaTheme {
                CompositionLocalProvider(LocalLarkImageOpener provides { opened += it }) {
                    LarkPartContent(part, FakeInboxRepository())
                }
            }
        }
        rule.waitUntil(5_000) { runCatching { rule.onNodeWithContentDescription("photo a").assertExists() }.isSuccess }
        rule.onNodeWithContentDescription("photo a").performClick()
        assertEquals(listOf(part), opened)
    }
}
