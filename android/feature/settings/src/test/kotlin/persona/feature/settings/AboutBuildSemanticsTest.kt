package persona.feature.settings

import android.content.Context
import android.content.res.Configuration
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.AccessibilityAction
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsConfiguration
import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.assertHasNoClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRow
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.feature.settings.generated.AboutBuildRow

/**
 * Native JVM metadata and Compose semantics for the test-only Build row (#13030). These are not TalkBack, an
 * accessibility service, a physical device, visual or performance evidence. Null is an optional-view-input case:
 * Android's non-null longVersionCode, including zero, never becomes a fabricated missing-value fallback.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AboutBuildSemanticsTest {
    @get:Rule val rule = createComposeRule()
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    @Test fun realPackageInfoKeepsNumericBuildValuesWithoutAnAbsentFallback() {
        val packages = shadowOf(context.packageManager)
        val info = packages.getInternalMutablePackageInfo(context.packageName)
        val previous = info.longVersionCode
        try {
            for (value in listOf(0L, 42L, 9_007_199_254_740_993L)) {
                info.setLongVersionCode(value)
                val installed = context.packageManager.getPackageInfo(context.packageName, 0)
                assertEquals(value, installed.longVersionCode)
                assertEquals(value.toString(), AboutBuildSnapshot.from(installed).buildNumber)
            }
        } finally {
            info.setLongVersionCode(previous)
        }
        assertEquals(null, AboutBuildSnapshot(null).buildNumber)
    }

    @Test fun realPackageMetadataFlowsThroughTheTypedInputIntoGeneratedSemantics() {
        val packages = shadowOf(context.packageManager)
        val info = packages.getInternalMutablePackageInfo(context.packageName)
        val previous = info.longVersionCode
        try {
            info.setLongVersionCode(9_007_199_254_740_993L)
            val snapshot = AboutBuildSnapshot.from(context.packageManager.getPackageInfo(context.packageName, 0))
            rule.setContent {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    AboutBuildRow(snapshot.buildNumber, Modifier.testTag("generated-from-package"))
                }
            }
            rule.waitForIdle()
            val semantics = rule.onNodeWithTag("generated-from-package", useUnmergedTree = true).readRowSemantics()
            assertEquals(listOf("Build", "9007199254740993"), semantics.text)
            assertTrue(semantics.clickLabels.isEmpty())
        } finally {
            info.setLongVersionCode(previous)
        }
    }

    @Test fun generatedAndNativeRowsExposeTheSameLocalizedReadOnlySemantics() {
        var language by mutableStateOf(language("en"))
        var build by mutableStateOf<String?>("9007199254740993")
        rule.setContent {
            CompositionLocalProvider(LocalContext provides language, LocalResources provides language.resources) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    Column {
                        JGroupItem(GroupPosition.Single) {
                            JRow(stringResource(R.string.settings_about_build), trailing = {
                                Text(build ?: stringResource(R.string.settings_not_provided),
                                    style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
                            }, modifier = Modifier.testTag("native-build"))
                        }
                        JGroupItem(GroupPosition.Single) {
                            AboutBuildRow(AboutBuildSnapshot(build).buildNumber, Modifier.testTag("generated-build"))
                        }
                    }
                }
            }
        }
        for ((tag, title, fallback) in listOf(
            Triple("en", "Build", "Not provided"),
            Triple("zh", "构建号", "未提供"),
            Triple("ja", "ビルド番号", "情報なし"),
        )) {
            language = language(tag)
            for (value in listOf("9007199254740993", null)) {
                rule.runOnIdle { build = value }
                rule.waitForIdle()
                val native = rule.onNodeWithTag("native-build", useUnmergedTree = true)
                val generated = rule.onNodeWithTag("generated-build", useUnmergedTree = true)
                native.assertIsDisplayed().assertHasNoClickAction()
                generated.assertIsDisplayed().assertHasNoClickAction()
                val nativeSemantics = native.readRowSemantics()
                val generatedSemantics = generated.readRowSemantics()
                assertEquals("$tag value=$value spoken label and value", nativeSemantics.text, generatedSemantics.text)
                assertEquals("$tag value=$value content descriptions", nativeSemantics.descriptions, generatedSemantics.descriptions)
                assertEquals("$tag value=$value click labels", nativeSemantics.clickLabels, generatedSemantics.clickLabels)
                assertTrue("$tag value=$value generated must be read-only", generatedSemantics.clickLabels.isEmpty())
                assertTrue("$tag value=$value native must be read-only", nativeSemantics.clickLabels.isEmpty())
                assertEquals("$tag value=$value custom actions", nativeSemantics.customActions, generatedSemantics.customActions)
                assertTrue("$tag value=$value generated must have no custom action", generatedSemantics.customActions.isEmpty())
                assertTrue("$tag value=$value native must have no custom action", nativeSemantics.customActions.isEmpty())
                assertEquals("$tag value=$value full spoken label and value",
                    listOf(title, value ?: fallback), generatedSemantics.text)
                native.assertMinimumTouchTarget("$tag value=$value native")
                generated.assertMinimumTouchTarget("$tag value=$value generated")
                // The rows are stacked; their absolute origins differ while their hit-area dimensions must match.
                assertEquals("$tag value=$value native/generated touch width",
                    native.fetchSemanticsNode().touchBoundsInRoot.width, generated.fetchSemanticsNode().touchBoundsInRoot.width)
                assertEquals("$tag value=$value native/generated touch height",
                    native.fetchSemanticsNode().touchBoundsInRoot.height, generated.fetchSemanticsNode().touchBoundsInRoot.height)
            }
        }
    }

    @Test fun anUnlabeledChildClickCannotPassTheSubtreeReadOnlyCheck() {
        rule.setContent {
            Box(Modifier.testTag("clickable-fixture")) {
                Text("Child", Modifier.clickable(onClickLabel = null) {})
            }
        }
        val semantics = rule.onNodeWithTag("clickable-fixture", useUnmergedTree = true).readRowSemantics()
        assertEquals(listOf<String?>(null), semantics.clickLabels)
        val failure = runCatching {
            assertTrue("An unlabeled child click must fail this read-only check", semantics.clickLabels.isEmpty())
        }.exceptionOrNull()
        assertTrue("An unlabeled child click must be detected even when its label is null", failure is AssertionError)
    }

    private fun language(tag: String): Context = context.createConfigurationContext(
        Configuration(context.resources.configuration).apply { setLocale(Locale.forLanguageTag(tag)) })

    private fun SemanticsNodeInteraction.readRowSemantics(): RowSemantics {
        val descendants = fetchSemanticsNode().descendants()
        // The generated Box is an extra empty wrapper; compare accessibility content, not wrapper-node cardinality.
        return RowSemantics(
            text = descendants.flatMap { it.config.getOrNull(SemanticsProperties.Text).orEmpty() }.map { it.text },
            descriptions = descendants.flatMap { it.config.getOrNull(SemanticsProperties.ContentDescription).orEmpty() },
            clickLabels = descendants.mapNotNull { it.config.clickAction() }.map { it.label },
            customActions = descendants.mapNotNull { it.config.getOrNull(SemanticsActions.CustomActions) }
                .map { actions -> actions.map { action -> action.label } },
        )
    }

    private fun SemanticsConfiguration.clickAction(): AccessibilityAction<() -> Boolean>? =
        if (contains(SemanticsActions.OnClick)) this[SemanticsActions.OnClick] else null

    private fun SemanticsNode.descendants(): List<SemanticsNode> = listOf(this) + children.flatMap { it.descendants() }

    private data class RowSemantics(
        val text: List<String>,
        val descriptions: List<String>,
        val clickLabels: List<String?>,
        val customActions: List<List<String>>,
    )

    private fun SemanticsNodeInteraction.assertMinimumTouchTarget(label: String) {
        val bounds = fetchSemanticsNode().touchBoundsInRoot
        val minimum = with(rule.density) { Tokens.MinTouch.android.toPx() }
        assertTrue("$label touch width ${bounds.width} is below $minimum", bounds.width >= minimum)
        assertTrue("$label touch height ${bounds.height} is below $minimum", bounds.height >= minimum)
    }
}
