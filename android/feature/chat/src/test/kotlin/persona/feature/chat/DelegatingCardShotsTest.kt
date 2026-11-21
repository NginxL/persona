package persona.feature.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * The looking and handed-over question in every language and style, light and dark (#544). PNGs are written to
 * `build/delegating-card-shots/` (or `-Dpersona.shots=<dir>`). Native JVM rendering: no system bars or glass refraction.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi", application = QuestionTestApplication::class)
class DelegatingCardShotsTest {
    @get:Rule val rule = createComposeRule()

    private val questions = """[{"question":"Which driver?","options":""" +
        """[{"label":"pgx","description":"The existing queries use it"},{"label":"sqlc"}]}]"""
    private val lookingCard = card(delegating = true)
    private val handedCard = card(delegate = """{"what":"Choose the database driver","why_now":"Queries start today",""" +
        """"recommend":"pgx","cost":"A later migration rewrites the queries","reason":"This changes the release",""" +
        """"deadline":"2026-11-08T10:00:00Z"}""")

    private fun card(delegating: Boolean = false, delegate: String? = null): Interaction =
        JsonCodec.decodeFromString(
            """{"id":"ix_delegate","machine_id":"box","session_id":"s1","kind":"question","delegating":$delegating,""" +
                """"questions":$questions""" + (delegate?.let { ""","delegate":$it""" } ?: "") + "}",
        )

    private data class Words(val looking: String, val contextTitle: String, val recommended: String)

    private fun shots(language: String, words: Words) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var handed by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background)) {
                    InteractionPanel(listOf(if (handed) handedCard else lookingCard), null, 1200.dp, null, emptySet(),
                        { _, _ -> }, null, {})
                }
            }
        }
        val output = File(System.getProperty("persona.shots") ?: "build/delegating-card-shots").also { it.mkdirs() }
        for (isHanded in listOf(false, true)) {
            handed = isHanded
            for (nextStyle in listOf(AppStyle.Glass, AppStyle.Brief, AppStyle.Board)) for (nextDark in listOf(false, true)) {
                style = nextStyle
                dark = nextDark
                rule.waitForIdle()
                if (isHanded) {
                    rule.onNodeWithText(words.contextTitle).assertExists()
                    rule.onAllNodesWithText(words.recommended, useUnmergedTree = true).assertCountEquals(1)
                } else {
                    rule.onNodeWithText(words.looking).assertExists()
                }
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val scene = if (isHanded) "handed" else "looking"
                val appearance = if (nextDark) "dark" else "light"
                File(output, "$scene-$language-${nextStyle.key}-$appearance.png").outputStream().use {
                    assertTrue(bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it))
                }
            }
        }
    }

    @Test fun englishEveryStyleLightAndDark() = shots("en",
        Words("Assistant is looking", "What the assistant found", "Assistant's pick"))

    @Config(qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun simplifiedChineseEveryStyleLightAndDark() = shots("zh-Hans",
        Words("助手在看", "助手补充的情况", "助手推荐"))

    @Config(qualifiers = "ja-w400dp-h1400dp-xhdpi")
    @Test fun japaneseEveryStyleLightAndDark() = shots("ja",
        Words("アシスタントが確認中", "アシスタントの補足", "アシスタントのおすすめ"))

    @Config(qualifiers = "en-w400dp-h1400dp-xhdpi", fontScale = 2f)
    @Test fun englishLargestTextEveryStyleLightAndDark() = shots("en-large",
        Words("Assistant is looking", "What the assistant found", "Assistant's pick"))
}
