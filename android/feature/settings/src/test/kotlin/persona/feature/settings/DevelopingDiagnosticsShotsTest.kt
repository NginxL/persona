package persona.feature.settings

import android.content.Context
import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
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
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.nio.file.Files
import java.time.Instant
import java.util.Locale
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.data.dev.DevCategory
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/** JVM pictures of About, its deletion warning and a ready-to-share export in every visual configuration. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class DevelopingDiagnosticsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private fun out() = File(System.getProperty("persona.shots") ?: "build/developing-diagnostics-shots").also { it.mkdirs() }

    private fun localized(tag: String, fontScale: Float): Context {
        val base = ApplicationProvider.getApplicationContext<Context>()
        return base.createConfigurationContext(Configuration(base.resources.configuration).apply {
            setLocale(Locale.forLanguageTag(tag))
            this.fontScale = fontScale
        })
    }

    @Test fun everyLanguageStyleSchemeSizeAndFontScaleDrawsAllThreeStates() {
        val fake = ShotDiagnostics()
        var language by mutableStateOf(localized("en", 1f))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var tablet by mutableStateOf(false)
        var large by mutableStateOf(false)
        var page by mutableStateOf("about")
        rule.setContent {
            CompositionLocalProvider(
                LocalContext provides language,
                LocalResources provides language.resources,
                LocalConfiguration provides language.resources.configuration,
                LocalDensity provides Density(language.resources.displayMetrics.density, if (large) 1.3f else 1f),
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.testTag("shot").width(if (tablet) 700.dp else 400.dp)
                        .height(if (tablet) 1000.dp else 900.dp).background(PersonaTheme.palette.background)) {
                        if (page == "about") Column {
                            AboutSettingsPage(developing = fake, onExport = { page = "export" })
                        } else DevelopingDiagnosticsExportPage(fake, onBack = { page = "about" })
                    }
                }
            }
        }
        val words = mapOf(
            "en" to Words("Developing Persona", "Cancel", "Export Diagnostics…", "Create Archive", "Share File…"),
            "zh" to Words("Developing Persona", "取消", "导出诊断信息…", "生成压缩包", "分享文件…"),
            "ja" to Words("Developing Persona", "キャンセル", "診断情報を書き出す…", "アーカイブを作成", "ファイルを共有…"),
        )
        for ((tag, text) in words) for (s in AppStyle.entries) for (d in listOf(false, true))
            for (wide in listOf(false, true)) for (big in listOf(false, true)) {
                language = localized(tag, if (big) 1.3f else 1f)
                style = s; dark = d; tablet = wide; large = big; page = "about"
                rule.waitForIdle()
                rule.onNodeWithText(text.developing).assertExists()
                save(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "developing-about-${name(tag, s, d, wide, big)}.png")

                rule.onNodeWithText(text.developing).performClick()
                rule.onNode(isDialog()).assertExists()
                save(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "developing-confirm-${name(tag, s, d, wide, big)}.png")
                rule.onNodeWithText(text.cancel).performClick()
                rule.onNodeWithText(text.export).performClick()
                rule.onNodeWithText(text.create).performScrollTo().performClick()
                rule.waitForIdle()
                rule.onNodeWithText(text.share).performScrollTo()
                val layouts = mutableListOf<TextLayoutResult>()
                rule.onNodeWithText(language.getString(R.string.settings_developing_device), useUnmergedTree = true)
                    .fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action?.invoke(layouts)
                assertFalse("Device and App Version must not ellipsize", layouts.single().isLineEllipsized(0))
                save(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "developing-export-${name(tag, s, d, wide, big)}.png")
            }
    }

    private fun name(tag: String, style: AppStyle, dark: Boolean, tablet: Boolean, large: Boolean) =
        "$tag-${style.name.lowercase()}-${if (dark) "dark" else "light"}-${if (tablet) "tablet" else "phone"}-${if (large) "large" else "default"}"

    private fun save(bitmap: Bitmap, name: String) {
        assertTrue("empty capture $name", bitmap.width > 0 && bitmap.height > 0)
        val background = bitmap.getPixel(0, 0)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("blank capture $name", other > 30)
        File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private data class Words(val developing: String, val cancel: String, val export: String, val create: String, val share: String)

    private class ShotDiagnostics : DevelopingDiagnostics {
        override val state = MutableStateFlow(DevelopingDiagnosticsSnapshot(true, Instant.parse("2026-11-17T04:00:00Z"), 48_512))
        private val archive = File(Files.createTempDirectory("developing-diagnostics-shot").toFile(),
            "persona-diagnostics-android-20261117-1200.zip")
        override suspend fun setEnabled(enabled: Boolean) = Unit
        override suspend fun refresh() = Unit
        override suspend fun export(categories: Set<DevCategory>) = archive
        override fun share(context: Context, file: File) = Unit
        override fun sendToAgent(context: Context, file: File) = Unit
    }
}
