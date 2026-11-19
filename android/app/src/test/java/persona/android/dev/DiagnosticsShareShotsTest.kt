package persona.android.dev

import android.app.Application
import android.content.Context
import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.util.Locale
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.android.R
import persona.android.ShareTargetRow
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/** JVM pictures of the diagnostics Forward target picker in every visual configuration. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class DiagnosticsShareShotsTest {
    @get:Rule val rule = createComposeRule()
    private fun out() = File(System.getProperty("persona.shots") ?: "build/diagnostics-share-shots").also { it.mkdirs() }

    private fun localized(tag: String, fontScale: Float): Context {
        val base = ApplicationProvider.getApplicationContext<Context>()
        return base.createConfigurationContext(Configuration(base.resources.configuration).apply {
            setLocale(Locale.forLanguageTag(tag))
            this.fontScale = fontScale
        })
    }

    @Test fun everyLanguageStyleSchemeSizeAndFontScaleDrawsThePicker() {
        val target = SessionKey("machine", "session")
        var language by mutableStateOf(localized("en", 1f))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var tablet by mutableStateOf(false)
        var large by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(
                LocalContext provides language,
                LocalResources provides language.resources,
                LocalConfiguration provides language.resources.configuration,
                LocalDensity provides Density(language.resources.displayMetrics.density, if (large) 1.3f else 1f),
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.testTag("shot").width(if (tablet) 700.dp else 400.dp)
                        .height(if (tablet || large) 1100.dp else 900.dp).background(PersonaTheme.palette.background)) {
                        DiagnosticsShareSheet(
                            fileName = "persona-diagnostics-android-20261117-1200.zip",
                            note = language.getString(R.string.app_diagnostics_note), onNote = {},
                            rows = listOf(ShareTargetRow(target, "Investigate chat", "Computer")),
                            selected = target, onSelect = {}, busy = false, loading = false, paired = true, error = null,
                            onOpen = {}, onCancel = {}, onOpenPersona = {},
                        )
                    }
                }
            }
        }
        val openWords = mapOf("en" to "Open chat", "zh" to "打开对话", "ja" to "チャットを開く")
        for ((tag, open) in openWords) for (s in AppStyle.entries) for (d in listOf(false, true))
            for (wide in listOf(false, true)) for (big in listOf(false, true)) {
                language = localized(tag, if (big) 1.3f else 1f)
                style = s; dark = d; tablet = wide; large = big
                rule.waitForIdle()
                rule.onNodeWithText(open).assertExists()
                val name = "diagnostics-target-$tag-${s.name.lowercase()}-${if (d) "dark" else "light"}-${if (wide) "tablet" else "phone"}-${if (big) "large" else "default"}.png"
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val background = bitmap.getPixel(0, 0)
                var other = 0
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
                assertTrue("blank capture $name", other > 30)
                File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
    }
}
