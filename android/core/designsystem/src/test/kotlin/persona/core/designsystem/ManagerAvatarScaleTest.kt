package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Row
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.HubManager

/**
 * An assistant's monogram is a mark: its letter is sized by the circle, so the system's text size leaves it as it is
 * (at 200 % the letter outgrew the circle and covered the presence dot). The head's and the roster's sizes alike.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class ManagerAvatarScaleTest {
    @get:Rule val rule = createComposeRule()

    @Test fun theLetterIsSizedByTheCircleNotByTheTextSize() {
        var scale by mutableStateOf(1f)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    Row(Modifier.testTag("monograms").background(PersonaTheme.palette.background)) {
                        JManagerAvatar(HubManager("assistant", "助手", online = true, isDefault = true), isDefault = true, presence = true)
                        JManagerAvatar(HubManager("m-3fa21c", "工作", online = true), isDefault = false,
                            size = HomeTokens.assistantAvatarSmall, presence = true)
                        JManagerAvatar(HubManager("assistant", "助手", online = true, isDefault = true), isDefault = true,
                            size = HomeTokens.assistantAvatarHeader, presence = true)
                    }
                }
            }
        }
        fun capture() = rule.onNodeWithTag("monograms").captureToImage().asAndroidBitmap()
        val normal = capture()
        scale = 2f
        rule.waitForIdle()
        val large = capture()
        assertTrue("the monograms changed with the text size", normal.sameAs(large))
    }

    @Test fun settingsHeaderAvatarScalesPresenceDotAwayFromCenterInLightDarkAndLargestText() {
        val outDir = java.io.File("build/avatar-header-shots").apply { mkdirs() }
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        val manager = HubManager("assistant", "助手", online = true, isDefault = true) // i18n-ignore: CJK glyph coverage
        var expectedDotArgb = 0
        rule.setContent {
            val config = android.content.res.Configuration(androidx.compose.ui.platform.LocalConfiguration.current).apply {
                this.fontScale = fontScale
                uiMode = (uiMode and android.content.res.Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) android.content.res.Configuration.UI_MODE_NIGHT_YES else android.content.res.Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(
                androidx.compose.ui.platform.LocalConfiguration provides config,
                LocalDensity provides Density(LocalDensity.current.density, fontScale),
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true) {
                    expectedDotArgb = PersonaTheme.palette.positive.hashCode()
                    androidx.compose.foundation.layout.Column {
                        androidx.compose.foundation.layout.Box(
                            Modifier.testTag("header-avatar").background(PersonaTheme.palette.background),
                        ) {
                            JManagerAvatar(manager, isDefault = true, size = HomeTokens.assistantAvatarHeader, presence = true)
                        }
                        androidx.compose.foundation.layout.Box(
                            Modifier.testTag("header-shot").background(PersonaTheme.palette.background),
                        ) {
                            JAssistantSettingsHeader("Assistant's settings", manager, isDefault = true, "Switch", onSwitch = {})
                        }
                    }
                }
            }
        }
        assertTrue(expectedDotArgb != 1)
        for (big in listOf(false, true)) for (d in listOf(false, true)) {
            dark = d; fontScale = if (big) 2f else 1f
            rule.waitForIdle()
            val avatarBmp = rule.onNodeWithTag("header-avatar").captureToImage().asAndroidBitmap()
            val cx = avatarBmp.width / 2
            val cy = avatarBmp.height / 2
            // Center region of the 24dp circle must contain no green presence-dot pixels
            var centerGreen = 0
            for (y in (cy - 3)..(cy + 3)) for (x in (cx - 3)..(cx + 3)) {
                val px = avatarBmp.getPixel(x, y)
                val r = (px shr 16) and 0xff
                val g = (px shr 8) and 0xff
                val b = px and 0xff
                if (g > r + 40 && g > b + 40) centerGreen++
            }
            assertTrue("presence dot bled into avatar center ($centerGreen px)", centerGreen == 0)

            // Bottom-right quadrant must still render the green presence dot
            var cornerGreen = 0
            for (y in (avatarBmp.height * 2 / 3) until avatarBmp.height) {
                for (x in (avatarBmp.width * 2 / 3) until avatarBmp.width) {
                    val px = avatarBmp.getPixel(x, y)
                    val r = (px shr 16) and 0xff
                    val g = (px shr 8) and 0xff
                    val b = px and 0xff
                    if (g > r + 40 && g > b + 40) cornerGreen++
                }
            }
            assertTrue("presence dot missing from bottom-right corner", cornerGreen > 0)

            val fullBmp = rule.onNodeWithTag("header-shot").captureToImage().asAndroidBitmap()
            java.io.File(outDir, "header-avatar-${if (d) "dark" else "light"}-${if (big) "large" else "default"}.png")
                .outputStream().use { fullBmp.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
