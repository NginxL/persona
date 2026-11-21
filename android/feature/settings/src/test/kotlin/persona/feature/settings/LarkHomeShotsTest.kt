package persona.feature.settings

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.LarkAccount
import persona.core.contracts.api.LarkAccounts
import persona.core.contracts.api.LarkStatus
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.LocalAppStyleStore
import persona.core.designsystem.PersonaTheme

/** The real Lark connector page and its device-local switch, off and on, in every appearance. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
class LarkHomeShotsTest {
    @get:Rule val rule = createComposeRule()

    private object Connectors : ConnectorsApiStub() {
        override suspend fun larkStatus() = LarkStatus(configured = true, connected = true, name = "Example workspace")
        override suspend fun larkAccounts() = LarkAccounts(listOf(LarkAccount("default", "Alex")), default = "default")
    }

    private object MultiAccountConnectors : ConnectorsApiStub() {
        override suspend fun larkStatus() = LarkStatus(configured = true, connected = true, name = "Example workspace")
        override suspend fun larkAccounts() = LarkAccounts(
            listOf(LarkAccount("default", "Alex"), LarkAccount("second", "Sam")),
            default = "default",
        )
    }

    @Test fun switchOffAndOnInEveryStyleThemeAndTextSize() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        context.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE).edit().clear().commit()
        val store = AppStyleStore(context)
        assertFalse(store.larkHomeMessages.value)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableStateOf(1f)
        var run by mutableIntStateOf(0)
        rule.setContent {
            CompositionLocalProvider(
                LocalDensity provides Density(LocalDensity.current.density, scale),
                LocalAppStyleStore provides store,
            ) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    key(run) {
                        Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                            FeishuConnectorPage(Connectors, onBack = {}, onAgentModel = {})
                        }
                    }
                }
            }
        }

        var clickChecked = false
        for (enabled in listOf(false, true)) for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            for ((textSize, fontScale) in listOf("default" to 1f, "text-200" to 2f)) {
                rule.runOnIdle {
                    store.setLarkHomeMessages(enabled)
                    style = appearance
                    dark = night
                    scale = fontScale
                    run++
                }
                rule.waitForIdle()
                rule.onNodeWithText("Example workspace").assertExists()
                rule.onNodeWithText("Show messages on home").assertExists()
                if (enabled) rule.onNodeWithTag("settings.lark.homeMessages").assertIsOn()
                else rule.onNodeWithTag("settings.lark.homeMessages").assertIsOff()
                val state = if (enabled) "on" else "off"
                capture("lark-home-$state-${appearance.name.lowercase()}-${if (night) "dark" else "light"}-$textSize")
                if (enabled && !clickChecked) {
                    rule.onNodeWithTag("settings.lark.homeMessages").performClick()
                    rule.onNodeWithTag("settings.lark.homeMessages").assertIsOff()
                    assertFalse(store.larkHomeMessages.value)
                    clickChecked = true
                }
            }
        }
        assertTrue(clickChecked)
    }

    @Test fun theExistingAccountCaptionChangesOnlyWhenMessagesIsEnabled() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        context.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE).edit().clear().commit()
        val store = AppStyleStore(context)
        rule.setContent {
            CompositionLocalProvider(LocalAppStyleStore provides store) {
                PersonaTheme(reduceMotion = true) {
                    FeishuConnectorPage(MultiAccountConnectors, onBack = {}, onAgentModel = {})
                }
            }
        }
        rule.onNodeWithText("Messages page can switch between them", substring = true).assertExists()
        rule.onNodeWithText("Messages, /agent", substring = true).assertDoesNotExist()

        rule.onNodeWithTag("settings.lark.homeMessages").performClick()
        rule.onNodeWithText("Messages, /agent", substring = true).assertExists()
        rule.onNodeWithText("Messages page can switch between them", substring = true).assertDoesNotExist()
    }

    @Test fun withoutAConversationRepositoryTheSwitchStaysHiddenAndTheChoiceSurvives() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        context.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE).edit().clear().commit()
        val store = AppStyleStore(context).apply { setLarkHomeMessages(true) }
        rule.setContent {
            CompositionLocalProvider(LocalAppStyleStore provides store) {
                PersonaTheme(reduceMotion = true) {
                    FeishuConnectorPage(null, onBack = {}, onAgentModel = {})
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithTag("settings.lark.homeMessages").assertDoesNotExist()
        rule.onNodeWithText("Show messages on home").assertDoesNotExist()
        assertTrue(store.larkHomeMessages.value)
    }

    private fun capture(name: String) {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
            if (bitmap.getPixel(x, y) != background) other++
        }
        assertTrue("$name drew nothing", other > 100)
        val out = File(System.getProperty("persona.shots") ?: "build/lark-home-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use {
            assertTrue("$name could not be written", bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }
}
