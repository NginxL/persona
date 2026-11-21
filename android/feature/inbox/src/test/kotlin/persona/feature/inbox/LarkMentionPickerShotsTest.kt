package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.LarkMember
import persona.core.contracts.api.LarkMention
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** #576 G122: Everyone and the conversation's members, with light/dark Robolectric rendering evidence. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h500dp-xhdpi")
class LarkMentionPickerShotsTest {
    @get:Rule val rule = createComposeRule()
    private val members = listOf(LarkMember("u-tom", "Tom"), LarkMember("u-zhou", "Zhou"))

    @Test fun selectingEveryoneOrAPersonReturnsTheSelectedMention() {
        var query by mutableStateOf("")
        val picked = mutableListOf<LarkMention>()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                LarkMentionPicker(query = query, members = members, onPick = picked::add)
            }
        }
        rule.onNodeWithTag("lark.mentionPicker").assertExists()
        rule.onNodeWithText("Everyone").performClick()
        rule.onNodeWithText("Tom").performClick()
        assertEquals(listOf(LarkMention("all", "Everyone"), LarkMention("u-tom", "Tom")), picked)

        query = "To"
        rule.waitForIdle()
        rule.onNodeWithText("Everyone").assertDoesNotExist()
        rule.onNodeWithText("Tom").assertExists()
    }

    @Test fun thePickerRendersInLightAndDark() {
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("mentionShot").width(400.dp).background(PersonaTheme.palette.surfaceOpaque)) {
                    LarkMentionPicker(query = "", members = members, onPick = {})
                }
            }
        }
        val directory = File(System.getProperty("persona.shots") ?: "build/lark-mention-shots").also { it.mkdirs() }
        for (each in AppStyle.entries) for (night in listOf(false, true)) {
            style = each
            dark = night
            rule.waitForIdle()
            rule.onNodeWithTag("lark.mentionPicker").assertExists()
            rule.onNodeWithText("Everyone").assertExists()
            rule.onNodeWithText("Tom").assertExists()
            rule.onNodeWithText("Zhou").assertExists()
            val image = rule.onNodeWithTag("mentionShot").captureToImage().asAndroidBitmap()
            File(directory, "mentions-${each.name.lowercase()}-${if (night) "dark" else "light"}.png").outputStream().use {
                image.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
