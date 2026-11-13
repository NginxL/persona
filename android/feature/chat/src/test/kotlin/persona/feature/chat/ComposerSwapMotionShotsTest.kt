package persona.feature.chat

import android.content.Context
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.pm.ResolveInfo
import android.content.res.Configuration
import android.graphics.Bitmap
import android.speech.RecognizerIntent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ComposerDelivery
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.NavItem
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.icons.PhIcons
import java.io.File

/**
 * The composer's send and stop buttons swapping, frame by frame on Glass with the clock in the test's hand (#4210 T3, iOS
 * `ShellComposerBar` and `Theme.Motion.composerSwap`): the first words typed in Persona's composer (send grows out of the
 * right edge), and the first words typed while an Agent works in an Agent's composer (stop shrinks away as send grows). Each
 * is the state before, then 0, 40, 80, 120, 200 and 400 ms after the first frame of the change. On `main` the buttons swap in
 * the first frame, and so do they here with the system's animations off (the `reduced` scene). Robolectric's native graphics
 * draw the blur send clears as it grows (the soft edge of its first frames). PNGs in `build/composer-swap-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ComposerSwapMotionShotsTest {
    @get:Rule val rule = createComposeRule()

    private val service = object : ChatComposerService {
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        override suspend fun upload(session: SessionKey, name: String, file: File) = name
        override suspend fun interrupt(session: SessionKey) {}
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean): ComposerDelivery = error("not here")
    }

    /** The phone has a voice input screen, so the composer offers its mic beside send. */
    @Before fun recognizer() {
        val info = ResolveInfo().apply { activityInfo = ActivityInfo().apply { packageName = "com.example.voice"; name = "Voice" } }
        shadowOf(ApplicationProvider.getApplicationContext<Context>().packageManager)
            .addResolveInfoForIntent(Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH), info)
    }

    private fun frames(scene: String, dark: Boolean, home: Boolean, working: Boolean, words: String, reduceMotion: Boolean = false) {
        val state = ChatComposerState()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = reduceMotion, style = AppStyle.Glass) {
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                val chrome = if (home) HomeChrome(listOf(NavItem("home", "Home", PhIcons.House)), "home", onSelect = {}, leading = {}) else null
                CompositionLocalProvider(LocalConfiguration provides configuration, LocalHomeChrome provides chrome) {
                    Box(Modifier.testTag("shot").width(400.dp).height(150.dp).background(PersonaTheme.palette.background), Alignment.BottomCenter) {
                        ChatComposer(SessionKey("box", "s1"), state, working = working, sending = false, canClear = false, service = service,
                            onSend = {}, onCommand = {}, onNotice = {})
                    }
                }
            }
        }
        // As a user does: a tap in the field opens the composer, then the words come.
        rule.onNode(hasSetTextAction()).performClick()
        rule.waitForIdle()
        rule.mainClock.autoAdvance = false
        val out = File(System.getProperty("persona.shots") ?: "build/composer-swap-shots").also { it.mkdirs() }
        fun capture(name: String) {
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out, "$scene-${if (dark) "dark" else "light"}-$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.mainClock.advanceTimeBy(500)
        capture("before")
        state.value = TextFieldValue(words, TextRange(words.length))
        Snapshot.sendApplyNotifications()
        var at = 0
        for (ms in listOf(0, 40, 80, 120, 200, 400)) {
            rule.mainClock.advanceTimeBy((ms - at).toLong().coerceAtLeast(16))
            at = ms
            capture(ms.toString().padStart(3, '0') + "ms")
        }
    }

    @Test fun personasComposerLight() = frames("typing", dark = false, home = true, working = false, words = "Draft a reply to Alex")
    @Test fun personasComposerDark() = frames("typing", dark = true, home = true, working = false, words = "Draft a reply to Alex")
    @Test fun anAgentsComposerLight() = frames("stop-to-send", dark = false, home = false, working = true, words = "Use the staging key")
    @Test fun anAgentsComposerDark() = frames("stop-to-send", dark = true, home = false, working = true, words = "Use the staging key")
    @Test fun anAgentsComposerWithAnimationsOffLight() =
        frames("stop-to-send-reduced", dark = false, home = false, working = true, words = "Use the staging key", reduceMotion = true)
    @Test fun anAgentsComposerWithAnimationsOffDark() =
        frames("stop-to-send-reduced", dark = true, home = false, working = true, words = "Use the staging key", reduceMotion = true)
}
