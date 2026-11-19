package persona.feature.settings

import androidx.activity.ComponentActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.PersonaTheme

/** The request's plain words, privacy and localized relative age (#10319). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi")
class BrowserLoginContentTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val rawSource = "203.0.113.77"
    private val requestId = "a".repeat(40) + "b-_"
    private val request = BrowserLoginRequest(requestId, "hub-1")
    private var age by mutableIntStateOf(12)
    private var network by mutableStateOf<String?>("lan")
    private var revision by mutableIntStateOf(0)

    private data class Words(
        val browser: String,
        val browserNote: String,
        val from: String,
        val lan: String,
        val internet: String,
        val unknown: String,
        val unknownNote: String,
        val requested: String,
        val gets: String,
        val access: String,
        val revoke: String,
        val warning: String,
        val secondOne: String,
        val secondTwo: String,
        val minuteOne: String,
        val minuteTwo: String,
    )

    private val english = Words(
        browser = "Browser",
        browserNote = "The name the browser gave itself",
        from = "From",
        lan = "Your local network",
        internet = "The internet",
        unknown = "Can't tell",
        unknownNote = "The request was forwarded, so its real source can't be seen",
        requested = "Requested",
        gets = "Gets",
        access = "The same full access as this phone",
        revoke = "You can revoke it any time in Settings › Devices",
        warning = "This request came from the internet. Only allow a browser you are signing in to yourself, right now.",
        secondOne = "1 second ago",
        secondTwo = "2 seconds ago",
        minuteOne = "1 minute ago",
        minuteTwo = "2 minutes ago",
    )

    private val chinese = Words(
        browser = "浏览器",
        browserNote = "这是浏览器自己填的名字",
        from = "来自",
        lan = "你的局域网",
        internet = "互联网",
        unknown = "无法判断",
        unknownNote = "请求经过了转发，看不到真实来源",
        requested = "发起时间",
        gets = "会得到",
        access = "和这台手机一样的完整权限",
        revoke = "可随时在「设置 › 设备」撤销",
        warning = "这个请求来自互联网。只有你本人此刻正在登录的浏览器，才可以允许。",
        secondOne = "1 秒前",
        secondTwo = "2 秒前",
        minuteOne = "1 分钟前",
        minuteTwo = "2 分钟前",
    )

    private val japanese = Words(
        browser = "ブラウザ",
        browserNote = "ブラウザ自身が名乗った名前です",
        from = "送信元",
        lan = "ローカルネットワーク",
        internet = "インターネット",
        unknown = "判別できません",
        unknownNote = "リクエストが転送されたため、本当の送信元はわかりません",
        requested = "リクエスト",
        gets = "付与される権限",
        access = "このスマートフォンと同じフルアクセス",
        revoke = "設定 › デバイス でいつでも取り消せます",
        warning = "このリクエストはインターネットから届きました。いま自分でログインしようとしているブラウザだけを許可してください。",
        secondOne = "1秒前",
        secondTwo = "2秒前",
        minuteOne = "1分前",
        minuteTwo = "2分前",
    )

    private fun show() {
        val hub = ScriptedBrowserLoginHub { _, _ ->
            browserLoginDetails(source = rawSource, ageSeconds = age, network = network)
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                key(revision) { BrowserLoginApproval(hub, request, rememberCoroutineScope()) {} }
            }
        }
    }

    private fun present(seconds: Int, from: String?) {
        rule.runOnIdle {
            age = seconds
            network = from
            revision++
        }
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Chrome · Mac", substring = true).fetchSemanticsNodes().isNotEmpty() }
        rule.waitForIdle()
    }

    private fun assertText(text: String) = rule.onAllNodesWithText(text, substring = false).assertCountEquals(1)
    private fun assertNoRawIdentifiers() {
        rule.onAllNodesWithText(rawSource, substring = true).assertCountEquals(0)
        rule.onAllNodesWithText(requestId, substring = true).assertCountEquals(0)
    }

    private fun assertCommonFacts(words: Words, ageText: String) {
        assertText(words.browser)
        assertText("Chrome · Mac")
        assertText(words.browserNote)
        assertText(words.from)
        assertText(words.lan)
        assertText(words.requested)
        assertText(ageText)
        assertText(words.gets)
        assertText(words.access)
        assertText(words.revoke)
        assertNoRawIdentifiers()
    }

    @Test fun lanRequestUsesPlainFactsAndNeverShowsRawIdentifiers() {
        show()
        present(60, "lan")
        // Check privacy first: the base's Source: 203.0.113.77 is itself the red-first evidence.
        assertNoRawIdentifiers()
        assertCommonFacts(english, english.minuteOne)
    }

    @Test fun internetRequestProminentlyWarnsButStillAllows() {
        show()
        present(12, "internet")
        assertText(english.warning)
        assertText(english.internet)
        rule.onNode(hasText(rule.activity.getString(R.string.settings_browser_login_allow)) and hasClickAction()).assertIsEnabled()
        assertNoRawIdentifiers()
    }

    @Test fun unknownNetworkSaysForwardingHidesTheRealSource() {
        show()
        present(12, "unknown")
        assertText(english.unknown)
        assertText(english.unknownNote)
        rule.onAllNodesWithText(english.warning, substring = true).assertCountEquals(0)
        assertNoRawIdentifiers()
    }

    @Test fun englishRelativeAgeUsesSingularAndPluralForms() = checkAges(english)

    @Config(qualifiers = "zh-w400dp-h1800dp-xhdpi")
    @Test fun simplifiedChineseShipsTheFactsWarningAndPluralAge() {
        checkAges(chinese)
        assertCommonFacts(chinese, chinese.minuteTwo)
        present(12, "internet")
        assertText(chinese.internet)
        assertText(chinese.warning)
        present(12, "unknown")
        assertText(chinese.unknown)
        assertText(chinese.unknownNote)
        assertNoRawIdentifiers()
    }

    @Config(qualifiers = "ja-w400dp-h1800dp-xhdpi")
    @Test fun japaneseShipsTheFactsWarningAndPluralAge() {
        checkAges(japanese)
        assertCommonFacts(japanese, japanese.minuteTwo)
        present(12, "internet")
        assertText(japanese.internet)
        assertText(japanese.warning)
        present(12, "unknown")
        assertText(japanese.unknown)
        assertText(japanese.unknownNote)
        assertNoRawIdentifiers()
    }

    private fun checkAges(words: Words) {
        show()
        val cases = listOf(
            1 to words.secondOne,
            2 to words.secondTwo,
            60 to words.minuteOne,
            120 to words.minuteTwo,
        )
        for ((seconds, expected) in cases) {
            present(seconds, "lan")
            // This comes before the other fact checks so the base reports its singular/plural defect directly.
            assertText(expected)
        }
    }
}
