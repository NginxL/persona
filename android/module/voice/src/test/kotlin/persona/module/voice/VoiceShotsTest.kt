package persona.module.voice

import android.Manifest
import android.app.Application
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * Voice: asking for the microphone, a conversation going on, and a model that cannot be used, in each of the app's
 * languages (docs/protocol/i18n.md); Glass light and dark, the other styles light. PNGs go to `build/i18n-shots/` (or
 * `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class VoiceShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @After fun clean() { VoiceAudioOverride.factory = null }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h800dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", ask = "允许使用麦克风", live = "在听", problem = "语音模型不可用",
        said = "今天下午有什么安排？", answer = "三点有一个设计评审。", detail = "女声 · 中文、英文 · 默认")

    @Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
    @Test fun english() = shots("en", ask = "Allow microphone", live = "Listening", problem = "Voice model unavailable",
        said = "What's on this afternoon?", answer = "A design review at three.", detail = "Female · Chinese, English · Default")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h800dp-xhdpi")
    @Test fun japanese() = shots("ja", ask = "マイクを許可", live = "聞き取り中", problem = "音声モデルを使用できません",
        said = "今日の午後の予定は？", answer = "3時にデザインレビューがあります。", detail = "女性 · 中国語、英語 · デフォルト")

    private fun shots(language: String, ask: String, live: String, problem: String, said: String, answer: String, detail: String) {
        assertEquals(detail, voiceDetail(VoiceOption("a", "A", "female", listOf("zh", "en")), isDefault = true, VoiceText(rule.activity.resources)))
        VoiceAudioOverride.factory = { FakeAudio() }
        val extensions = FakeExtensions()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var stage by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(760.dp).background(PersonaTheme.palette.background)) {
                    key(stage) { VoiceScreen(VoiceRoute("voice"), NoNavigator, extensions, "shots", onModelConfiguration = {}) }
                }
            }
        }
        fun capture(name: String, text: String) {
            for ((s, d) in listOf(AppStyle.Glass to false, AppStyle.Glass to true, AppStyle.Brief to false, AppStyle.Board to false)) {
                style = s; dark = d
                rule.waitForIdle()
                rule.onNodeWithText(text).assertExists()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                File(out(), "voice-$name-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                    .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
        capture("ask", ask)

        shadowOf(ApplicationProvider.getApplicationContext<Application>()).grantPermissions(Manifest.permission.RECORD_AUDIO)
        stage = 1
        rule.waitForIdle()
        extensions.extension.onOpen()
        extensions.extension.onText(Ready)
        extensions.extension.onText("""{"type":"transcript","id":"u1","role":"user","text":"$said","final":true}""")
        extensions.extension.onText("""{"type":"transcript","id":"a1","role":"assistant","text":"$answer","final":true}""")
        capture("live", live)

        extensions.choice = VoiceModelChoice("retired", null)
        stage = 2
        capture("problem", problem)
    }
}

private object NoNavigator : Navigator {
    override fun navigate(key: NavKey) = Unit
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}
