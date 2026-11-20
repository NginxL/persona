package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowDialog
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionMarket
import persona.core.contracts.api.HubExtension
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** Real removed-market pages and the actual removal dialog, in every style, appearance and required text size. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2200dp-xhdpi")
class ExtensionMarketRemovalShotsTest {
    @get:Rule val rule = createComposeRule()

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private val market = ExtensionMarket("m1", "https://plugins.example.com/one/", "Market one", extensions = 2)
    private val affectedBody = "The hub stops reading this market. 2 extensions installed from it stay, but stop upgrading until the market is added again with the same address."
    private val recovery = "Add the same market address again to restore upgrades"

    private fun write(name: String, bitmap: Bitmap, light: IntArray?): IntArray {
        assertTrue("capture has dimensions", bitmap.width > 0 && bitmap.height > 0)
        val pixels = IntArray(bitmap.width * bitmap.height)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
        assertTrue("content rendered", pixels.indices.step(97).map { pixels[it] }.toSet().size > 20)
        if (light != null) assertFalse("light and dark differ", pixels.contentEquals(light))
        File(File("build/extension-market-removal-shots").also { it.mkdirs() }, "$name.png").outputStream().use {
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
        return pixels
    }

    private fun captureDialog(name: String, light: IntArray?): IntArray {
        lateinit var bitmap: Bitmap
        rule.runOnIdle {
            val decor = checkNotNull(ShadowDialog.getLatestDialog().window).decorView
            bitmap = Bitmap.createBitmap(decor.width, decor.height, Bitmap.Config.ARGB_8888)
            decor.draw(android.graphics.Canvas(bitmap))
        }
        return write(name, bitmap, light)
    }

    private fun capturePage(name: String, light: IntArray?): IntArray =
        write(name, rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), light)

    private fun assertOneWholeLine(label: String) {
        val layouts = mutableListOf<TextLayoutResult>()
        rule.onNodeWithText(label).fetchSemanticsNode().config.getOrNull(SemanticsActions.GetTextLayoutResult)?.action?.invoke(layouts)
        assertTrue("$label is laid out once", layouts.size == 1)
        assertTrue("$label stays on one line", layouts.single().lineCount == 1)
        assertFalse("$label is not truncated", layouts.single().isLineEllipsized(0))
    }

    private fun showConfirmation() {
        val api = object : ExtensionApiStub() {
            override suspend fun markets() = listOf(market)
            override suspend fun autoUpgrades() = true
            override suspend fun extensions() = listOf(
                HubExtension("one", source = "installed", market = "m1", autoUpgrade = true),
                HubExtension("two", source = "installed", market = "m1", autoUpgrade = false),
                HubExtension("other", source = "installed", market = "m2", autoUpgrade = true),
            )
        }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) { ExtensionMarketsPage(api, {}) }
            }
        }
        rule.waitUntil { rule.onAllNodesWithText(market.name).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText(market.name).performClick()
        rule.onNodeWithText("Remove market").performClick()
        rule.waitUntil { rule.onAllNodesWithText(affectedBody).fetchSemanticsNodes().isNotEmpty() }
        rule.onNode(isDialog()).assertExists()
    }

    private fun confirmationShots(large: Boolean) {
        showConfirmation()
        if (large) {
            assertOneWholeLine("Remove")
            assertOneWholeLine("Cancel")
        }
        for (nextStyle in AppStyle.entries) {
            var light: IntArray? = null
            for (nextDark in listOf(false, true)) {
                rule.runOnIdle { style = nextStyle; dark = nextDark }
                rule.waitForIdle()
                light = captureDialog("confirmation-${nextStyle.key}-${if (nextDark) "dark" else "light"}${if (large) "-large" else ""}", light)
            }
        }
        rule.onNodeWithText("Cancel").performClick()
        rule.onNode(isDialog()).assertDoesNotExist()
    }

    @Test fun confirmationInEveryStyle() = confirmationShots(large = false)

    @Config(qualifiers = "en-w400dp-h3000dp-xhdpi", fontScale = 2f)
    @Test fun confirmationAtTheLargestTextSize() = confirmationShots(large = true)

    private fun showRemovedMarketSettings() {
        val detail = ExtensionDetail(
            HubExtension("example-notes", "Example Notes", "ui", "running", source = "installed", enabled = true,
                autoUpgrade = true, autoUpgradePaused = "market_removed"),
            origin = "market:m1/example-notes",
        )
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String) = detail
        }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) { ExtensionPage(api, "example-notes", {}) }
            }
        }
        rule.waitUntil { rule.onAllNodesWithText(recovery, substring = true).fetchSemanticsNodes().size == 1 }
        rule.onAllNodesWithText(recovery, substring = true).assertCountEquals(1)
        rule.onNodeWithText("Auto-upgrade").assertIsOn().assertIsNotEnabled()
        rule.onNodeWithText("Check for updates").assertIsEnabled()
    }

    private fun settingsShots(large: Boolean) {
        showRemovedMarketSettings()
        for (nextStyle in AppStyle.entries) {
            var light: IntArray? = null
            for (nextDark in listOf(false, true)) {
                rule.runOnIdle { style = nextStyle; dark = nextDark }
                rule.waitForIdle()
                light = capturePage("settings-${nextStyle.key}-${if (nextDark) "dark" else "light"}${if (large) "-large" else ""}", light)
            }
        }
    }

    @Test fun removedMarketSettingsInEveryStyle() = settingsShots(large = false)

    @Config(qualifiers = "en-w400dp-h3000dp-xhdpi", fontScale = 2f)
    @Test fun removedMarketSettingsAtTheLargestTextSize() = settingsShots(large = true)
}
