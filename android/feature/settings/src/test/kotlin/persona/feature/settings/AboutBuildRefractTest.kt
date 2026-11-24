package persona.feature.settings

import android.content.Context
import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.Density
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRow
import persona.core.designsystem.PersonaTheme
import persona.feature.settings.generated.AboutBuildRow
import androidx.compose.material3.Text
import androidx.compose.ui.res.stringResource

/**
 * Native JVM Compose evidence for the independent generated Build row (#13001), not a device, accessibility-service
 * or performance result. Production About and the original Version consumer remain unchanged and are not selected here.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AboutBuildRefractTest {
    @get:Rule val rule = createComposeRule()
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    @Test fun optionalValueAndLocalizedFallbackMatchHandwrittenPixels() {
        var locale by mutableStateOf(locale("en"))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var largeText by mutableStateOf(false)
        var build by mutableStateOf<String?>("42")
        rule.setContent {
            CompositionLocalProvider(LocalContext provides locale, LocalResources provides locale.resources) {
                val density = LocalDensity.current
                CompositionLocalProvider(LocalDensity provides Density(density.density, if (largeText) 2f else 1f)) {
                    PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                        Surface(color = PersonaTheme.palette.background) {
                            Column {
                                JGroupItem(GroupPosition.Single) {
                                    JRow(title = stringResource(R.string.settings_about_build), trailing = {
                                        Text(build ?: stringResource(R.string.settings_not_provided),
                                            style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
                                    }, modifier = Modifier.testTag("native"))
                                }
                                JGroupItem(GroupPosition.Single) {
                                    AboutBuildRow(AboutBuildSnapshot(build).buildNumber, Modifier.testTag("generated"))
                                }
                            }
                        }
                    }
                }
            }
        }
        val output = newAboutCaptureRun("build/about-build-refract-shots", System.getProperty("persona.shots"))
        for ((tag, expectedFallback) in mapOf("en" to "Not provided", "zh" to "未提供", "ja" to "情報なし"))
            for (selectedStyle in AppStyle.entries) for (selectedDark in listOf(false, true))
                for (selectedLarge in listOf(false, true)) for (value in listOf("42", null)) {
                    locale = locale(tag); style = selectedStyle; dark = selectedDark; largeText = selectedLarge; build = value
                    rule.waitForIdle()
                    val native = capture("native")
                    val generated = capture("generated")
                    assertTrue("$tag $selectedStyle dark=$selectedDark large=$selectedLarge value=$value pixels differ",
                        native.sameAs(generated))
                    if (value == null) {
                        val texts = rule.onNodeWithTag("generated", useUnmergedTree = true).fetchSemanticsNode()
                            .descendants().flatMap {
                                it.config.getOrNull(androidx.compose.ui.semantics.SemanticsProperties.Text).orEmpty()
                            }.map { it.text }
                        assertTrue("Missing fallback in $tag: $texts", expectedFallback in texts)
                        assertTrue("Raw resource key leaked in $tag: $texts", "settings.not.provided" !in texts)
                    }
                    val suffix = "$tag-${selectedStyle.name.lowercase()}-${if (selectedDark) "dark" else "light"}-" +
                        "${if (selectedLarge) "large" else "standard"}-${value ?: "absent"}"
                    output.resolve("native-$suffix.png").outputStream().use { native.compress(Bitmap.CompressFormat.PNG, 100, it) }
                    output.resolve("generated-$suffix.png").outputStream().use { generated.compress(Bitmap.CompressFormat.PNG, 100, it) }
                }
    }

    @Test fun handwrittenAboutRemainsTheOnlyProductionBuildRow() {
        rule.setContent { PersonaTheme(darkTheme = false, reduceMotion = true) { AboutSettingsPage(null) } }
        val rows = rule.onNodeWithText("Build", useUnmergedTree = true).fetchSemanticsNode()
        assertTrue("The native About Build row is missing", rows.id >= 0)
    }

    private fun locale(tag: String): Context = context.createConfigurationContext(
        Configuration(context.resources.configuration).apply { setLocale(Locale.forLanguageTag(tag)) })

    private fun capture(tag: String): Bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()

    private fun SemanticsNode.descendants(): List<SemanticsNode> = listOf(this) + children.flatMap { it.descendants() }
}
