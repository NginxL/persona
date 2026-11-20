package persona.feature.chat

import android.graphics.Bitmap
import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JPointerMenu
import persona.core.designsystem.PersonaTheme
import java.io.File

/** Real popup-content captures; containment assertions tie each one to its pointer and host window. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w600dp-h900dp-xhdpi")
class MessagePointerMenuShotsTest {
    @get:Rule val rule = createComposeRule()
    private val resources = ApplicationProvider.getApplicationContext<Context>().resources
    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var anchorCase by mutableStateOf(AnchorCase.BottomRight)
    private var fontScale by mutableStateOf(1f)

    private enum class AnchorCase(val x: Float, val y: Float) {
        TopRight(0.94f, 0.08f), BottomLeft(0.06f, 0.92f), TopLeft(0.06f, 0.08f), BottomRight(0.94f, 0.92f),
    }

    @Test fun cornersAndLongMenusInEveryStyleAndAppearance() {
        setMenuContent()
        for (appearance in listOf(false, true)) for (current in AppStyle.entries) {
            val cases = if (current == AppStyle.Glass) AnchorCase.entries.toList() else listOf(AnchorCase.BottomRight)
            for (anchor in cases) {
                style = current; dark = appearance; anchorCase = anchor
                rule.waitForIdle()
                assertMenuContained()
                val name = "${current.name.lowercase()}-${if (appearance) "dark" else "light"}-${anchor.name.lowercase()}.png"
                val bitmap = rule.onNodeWithTag("pointer-menu").captureToImage().asAndroidBitmap()
                File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
    }

    @Test
    @Config(qualifiers = "zh-w600dp-h900dp-xhdpi", fontScale = 2f)
    fun chineseAtTheLargestTextKeepsEveryActionReachable() = localizedLargeText()

    @Test
    @Config(qualifiers = "ja-w600dp-h900dp-xhdpi", fontScale = 2f)
    fun japaneseAtTheLargestTextKeepsEveryActionReachable() = localizedLargeText()

    @Test
    @Config(qualifiers = "en-w600dp-h900dp-xhdpi", fontScale = 2f)
    fun englishAtTheLargestTextKeepsEveryActionReachable() = localizedLargeText()

    private fun localizedLargeText() {
        fontScale = 2f
        setMenuContent()
        for (appearance in listOf(false, true)) {
            style = AppStyle.Glass; dark = appearance; anchorCase = AnchorCase.BottomRight
            rule.waitForIdle()
            assertMenuContained()
            rule.onNodeWithText(resources.getString(R.string.chat_menu_rewind)).assertExists()
        }
    }

    private fun setMenuContent() {
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val window = LocalWindowInfo.current.containerSize
                    val actions = messageMenuActions(MessageTarget("user#1", "message", user = true), writes = true, forward = false,
                        LocalResources.current, MessageExtras(rewind = true)) {}
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("host")) {
                        JPointerMenu(Offset(window.width * anchorCase.x, window.height * anchorCase.y), {},
                            actions, Modifier.testTag("pointer-menu"))
                    }
                }
            }
        }
    }

    private fun assertMenuContained() {
        val host = rule.onNodeWithTag("host").getUnclippedBoundsInRoot()
        val menu = rule.onNodeWithTag("pointer-menu").getUnclippedBoundsInRoot()
        assertTrue(menu.left >= host.left && menu.top >= host.top)
        assertTrue(menu.right <= host.right && menu.bottom <= host.bottom)
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/message-pointer-shots").also { it.mkdirs() }
}
