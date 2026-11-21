package persona.feature.settings

import android.content.Context
import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File
import java.util.Locale

/**
 * About with a build commit, and without one, drawn on the JVM in English, Chinese and Japanese, in every style, light and
 * dark. Each run writes PNGs to a fresh `run-*` child beneath `build/about-commit-shots/` (or the `persona.shots`
 * base) and reports its exact directory for review evidence.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AboutBuildCommitShotsTest {
    @get:Rule val rule = createComposeRule()

    private val outputDirectory by lazy {
        newAboutCaptureRun("build/about-commit-shots", System.getProperty("persona.shots"))
    }

    private fun inLanguage(tag: String): Context {
        val base = ApplicationProvider.getApplicationContext<Context>()
        return base.createConfigurationContext(Configuration(base.resources.configuration).apply { setLocale(Locale.forLanguageTag(tag)) })
    }

    @Test fun everyLanguageStyleAndSchemeDrawsBothStates() {
        val commit = BuildCommit.parse("0123456789abcdef0123456789abcdef01234567")
        var language by mutableStateOf(inLanguage("en"))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf<BuildCommit?>(commit)
        rule.setContent {
            CompositionLocalProvider(LocalContext provides language, LocalResources provides language.resources) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.m)) {
                        Column { AboutSettingsPage(shown) }
                    }
                }
            }
        }
        val words = mapOf("en" to ("Commit" to "Unknown"), "zh" to ("提交" to "未知"), "ja" to ("コミット" to "不明"))
        for ((tag, pair) in words) for (s in AppStyle.entries) for (d in listOf(false, true)) for (present in listOf(true, false)) {
            language = inLanguage(tag); style = s; dark = d; shown = if (present) commit else null
            rule.waitForIdle()
            rule.onNodeWithText(pair.first, useUnmergedTree = true).assertExists()
            if (present) rule.onNodeWithText("0123456789ab", useUnmergedTree = true).assertExists()
            else rule.onNodeWithText(pair.second, useUnmergedTree = true).assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val background = bitmap.getPixel(1, 1)
            var other = 0
            for (y in 0 until bitmap.height step 5) for (x in 0 until bitmap.width step 5) if (bitmap.getPixel(x, y) != background) other++
            assertTrue("drew nothing", other > 100)
            val name = "about-commit-$tag-${if (present) "present" else "unknown"}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(outputDirectory, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
