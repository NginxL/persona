package persona.feature.assistant

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** Real Compose menu captures, synthetic data only: every style, light/dark, default and largest text. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class AssistantCompactionShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @After fun reset() {
        RuntimeEnvironment.setFontScale(1f)
        AssistantManagerChoice.reset()
    }

    @Test fun sideConversationMenuInEveryStyleAppearanceAndTextSize() {
        val main = Session(machineId = "example", sessionId = "main", title = "Main", assistant = true)
        val side = Session(machineId = "example", sessionId = "direction", title = "Example direction", assistant = true)
        val light = mutableMapOf<Pair<AppStyle, Float>, Bitmap>()
        var first = true
        for (style in AppStyle.entries) for (dark in listOf(false, true)) for (scale in listOf(1f, 2f)) {
            if (!first) rule.activityRule.scenario.recreate()
            first = false
            AssistantManagerChoice.choose("hub", null, "assistant")
            RuntimeEnvironment.setFontScale(scale)
            val hub = HistoryHub(main, side)
            rule.setContent {
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                CompositionLocalProvider(LocalConfiguration provides configuration,
                    LocalHubFeatures provides setOf(CompactFeature, NewMainFeature, ClearMainFeature)) {
                    PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                        AssistantScreen(navigator, hub, hub, startConversation = { error("Unexpected start") },
                            management = hub, requestedSession = side.key, composerService = null,
                            chatContent = { Text("Synthetic direction conversation.") })
                    }
                }
            }
            rule.waitForIdle()
            rule.onNodeWithText("Persona").performClick()
            rule.onNodeWithText("Compact context").assertExists()
            rule.onAllNodesWithText("Clear context").assertCountEquals(0)
            rule.onAllNodesWithText("New main chat").assertCountEquals(0)
            val name = "android-compaction-${style.name.lowercase()}-${if (dark) "dark" else "light"}-${if (scale > 1f) "large" else "default"}"
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            if (!dark) light[style to scale] = bitmap
            else assertFalse("night mode must change the rendered menu", light.getValue(style to scale).sameAs(bitmap))
            historyShot(bitmap, name)
        }
    }
}
