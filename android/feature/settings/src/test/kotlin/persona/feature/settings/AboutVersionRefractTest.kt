package persona.feature.settings

import android.content.Context
import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
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
import androidx.compose.ui.test.assertHasNoClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
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
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRow
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.feature.settings.generated.AboutVersionModel
import persona.feature.settings.generated.AboutVersionRow
import java.io.File
import java.util.Locale

/**
 * Native JVM Compose component evidence, not a device/emulator or performance test. The generated Version row must
 * be pixel-identical to the original row and leave the handwritten three-row rollback and native Commit untouched.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AboutVersionRefractTest {
    @get:Rule val rule = createComposeRule()

    private val context: Context get() = ApplicationProvider.getApplicationContext()
    private val outputDirectory by lazy {
        newAboutCaptureRun("build/about-version-refract-shots", System.getProperty("persona.shots"))
    }

    private fun language(tag: String): Context = context.createConfigurationContext(
        Configuration(context.resources.configuration).apply { setLocale(Locale.forLanguageTag(tag)) })

    @Test fun modelResolvesOnlyTheVersionAndHasNoWriter() {
        val base = context
        val info = base.packageManager.getPackageInfo(base.packageName, 0)
        val model = ImmutableAboutVersionModel(AboutVersionSnapshot.read(base), base.resources)
        assertEquals(info.versionName ?: base.getString(R.string.settings_not_provided), model.versionName.value)
        val fallback = base.getString(R.string.settings_not_provided)
        assertEquals(fallback, ImmutableAboutVersionModel(AboutVersionSnapshot(null), base.resources).versionName.value)
        assertFalse(AboutVersionModel::class.java.declaredMethods.any { it.name.startsWith("set") })
        assertTrue(AboutVersionModel::class.java.declaredMethods.single().returnType.name.endsWith("State"))
    }

    @Test fun generatedRowMatchesTheUnchangedNativeRowInEveryStyleLanguageAndSize() {
        var language by mutableStateOf(language("en"))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var largeText by mutableStateOf(false)
        val version = "1.42.0-experimental-long-version"
        rule.setContent {
            CompositionLocalProvider(LocalContext provides language, LocalResources provides language.resources) {
                val density = LocalDensity.current
                CompositionLocalProvider(LocalDensity provides Density(density.density, if (largeText) 2f else 1f)) {
                    PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                        Box(Modifier.width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.m)) {
                            Column {
                                Box(Modifier.testTag("native").background(PersonaTheme.palette.background)) {
                                    JGroupItem(GroupPosition.of(0, 3)) {
                                        JRow(language.getString(R.string.settings_about_version), value = version)
                                    }
                                }
                                Box(Modifier.testTag("generated").background(PersonaTheme.palette.background)) {
                                    JGroupItem(GroupPosition.of(0, 3)) {
                                        AboutVersionRow(ImmutableAboutVersionModel(AboutVersionSnapshot(version), language.resources))
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
        for ((tag, title) in mapOf("en" to "Version", "zh" to "版本", "ja" to "バージョン"))
            for (selectedStyle in AppStyle.entries) for (selectedDark in listOf(false, true)) for (selectedLarge in listOf(false, true)) {
                language = language(tag); style = selectedStyle; dark = selectedDark; largeText = selectedLarge
                rule.waitForIdle()
                rule.onNode(hasTestTag("generated") and hasAnyDescendant(hasText(title)))
                    .assertIsDisplayed().assertHasNoClickAction()
                val native = capture("native")
                val generated = capture("generated")
                assertEquals("${selectedStyle.name} $tag dark=$selectedDark large=$selectedLarge dimensions",
                    native.width to native.height, generated.width to generated.height)
                assertTrue("${selectedStyle.name} $tag dark=$selectedDark large=$selectedLarge pixels differ",
                    native.sameAs(generated))
                val suffix = "$tag-${selectedStyle.name.lowercase()}-${if (selectedDark) "dark" else "light"}-${if (selectedLarge) "large" else "standard"}"
                File(outputDirectory, "native-$suffix.png").writePng(native)
                File(outputDirectory, "generated-$suffix.png").writePng(generated)
            }
    }

    @Test fun missingVersionUsesTheSameLocalizedFallback() {
        var language by mutableStateOf(language("zh"))
        rule.setContent {
            CompositionLocalProvider(LocalContext provides language, LocalResources provides language.resources) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    Column {
                        Box(Modifier.testTag("native")) {
                            JGroupItem(GroupPosition.of(0, 3)) {
                                JRow(language.getString(R.string.settings_about_version),
                                    value = language.getString(R.string.settings_not_provided))
                            }
                        }
                        Box(Modifier.testTag("generated")) {
                            JGroupItem(GroupPosition.of(0, 3)) {
                                AboutVersionRow(ImmutableAboutVersionModel(AboutVersionSnapshot(null), language.resources))
                            }
                        }
                    }
                }
            }
        }
        language = language("zh")
        rule.waitForIdle()
        assertTrue(capture("native").sameAs(capture("generated")))
        rule.onNode(hasTestTag("generated") and hasAnyDescendant(hasText("未提供"))).assertIsDisplayed()
    }

    @Test fun handwrittenRollbackRetainsTheNativeCommitRow() {
        // This is the unchanged production composable, not a generated screen selected through a hidden preference.
        rule.setContent { PersonaTheme(darkTheme = false, reduceMotion = true) { Column { AboutSettingsPage(null) } } }
        rule.onNodeWithText("Commit").assertHasNoClickAction()
        rule.onNodeWithText("Unknown", useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithText("Commit").performClick()
        assertTrue("Unknown commit must keep the native no-op", rule.onAllNodes(androidx.compose.ui.test.isRoot())
            .fetchSemanticsNodes().isNotEmpty())
    }

    private fun capture(tag: String): Bitmap =
        rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()

    private fun File.writePng(bitmap: Bitmap) {
        outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
