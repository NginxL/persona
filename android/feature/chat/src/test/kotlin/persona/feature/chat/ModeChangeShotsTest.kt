package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
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
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ModeChange
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

/** Native component evidence for every Android style, light/dark and default/large text; no device is required. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ModeChangeShotsTest {
    @get:Rule val rule = createComposeRule()
    private val out = File(System.getProperty("persona.shots") ?: "build/mode-change-shots").also { it.mkdirs() }
    private val at = "2026-11-18T09:01:00Z"

    @Test fun captureEveryStyleAppearanceAndTextSize() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            val current = LocalConfiguration.current
            val configuration = Configuration(current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(
                LocalConfiguration provides configuration,
                LocalDensity provides Density(LocalDensity.current.density, fontScale),
            ) {
                key(style, dark, fontScale) {
                    PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                        Column(
                            Modifier.width(360.dp).background(PersonaTheme.palette.background)
                                .padding(Tokens.Space.m).testTag("mode-change-shot"),
                        ) {
                            ModeChangeDivider(ModeChange(kind = "restart_done", to = "plan", at = at))
                            ModeChangeDivider(ModeChange(
                                kind = "set", from = "default", to = "acceptEdits", applied = "now",
                                actorKind = "device", deviceName = "Phone", at = at,
                            ))
                            ModeChangeDivider(ModeChange(
                                kind = "set", from = "acceptEdits", to = "bypassPermissions", applied = "restart",
                                actorKind = "device", deviceName = "A computer with a long device name", at = at,
                            ))
                            ModeChangeDivider(ModeChange(
                                kind = "restart_dropped", to = "bypassPermissions", reason = "process_replaced", at = at,
                            ))
                        }
                    }
                }
            }
        }
        for (shown in AppStyle.entries) for (appearance in listOf(false, true)) for (scale in listOf(1f, 2f)) {
            style = shown
            dark = appearance
            fontScale = scale
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            val image = rule.onNodeWithTag("mode-change-shot").captureToImage().asAndroidBitmap()
            assertTrue(image.width > 0 && image.height > 0)
            val name = "${shown.name.lowercase()}-${if (appearance) "dark" else "light"}-${if (scale > 1f) "large" else "default"}"
            File(out, "$name.png").outputStream().use { assertTrue(image.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
}
