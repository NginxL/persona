package persona.feature.chat

import android.content.Context
import android.content.res.Configuration
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.UndeliveredMessage
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

/** Kept owner messages in every Android style and appearance. Native JVM component captures, not emulator evidence. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ChatUndeliveredShotsTest {
    @get:Rule val rule = createComposeRule()
    private val context = ApplicationProvider.getApplicationContext<Context>()

    @Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
    @Test fun defaultText() = matrix(1f)

    @Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi", fontScale = 2f)
    @Test fun largeText() = matrix(2f)

    private fun matrix(fontScale: Float) {
        var failed by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration,
                LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        UndeliveredBubble(
                            UndeliveredMessage("kept", "Keep this message", images = 1,
                                at = "2026-11-17T10:00:00Z", state = if (failed) "failed" else "waiting",
                                error = if (failed) "The computer is away" else "The assistant could not start"),
                            glass = style == AppStyle.Glass, busy = false, canSend = true, onAction = { },
                        )
                    }
                }
            }
        }
        val out = File("build/undelivered-shots").apply { mkdirs() }
        val appearances = mutableMapOf<String, android.graphics.Bitmap>()
        for (gaveUp in listOf(false, true)) for (s in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle {
                failed = gaveUp
                style = s
                dark = night
                Snapshot.sendApplyNotifications()
            }
            rule.waitForIdle()
            val status = if (gaveUp) "Not delivered: The computer is away"
                else "Not delivered: The assistant could not start. It will be sent automatically."
            rule.onNodeWithText(status).assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val state = if (gaveUp) "failed" else "waiting"
            val appearance = if (night) "dark" else "light"
            val name = "kept-$state-${s.name.lowercase()}-$appearance-${fontScale.text()}.png"
            File(out, name).outputStream().use { assertTrue(bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)) }
            if (s == AppStyle.Glass && !gaveUp) appearances[appearance] = bitmap
        }
        assertFalse("light and dark captures must differ", appearances.getValue("light").sameAs(appearances.getValue("dark")))
    }

    private fun Float.text() = if (this == 1f) "1x" else "2x"
}
