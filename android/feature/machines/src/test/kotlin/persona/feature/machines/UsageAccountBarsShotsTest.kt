package persona.feature.machines

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.UsageAccount
import persona.core.contracts.api.UsageWindow
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.time.Instant

/**
 * "Subscription limits" as #13173 draws it: a card per account type, every account with its 5h and 7d bars (pace tick, protection
 * line where the hub says one), and the card's reset chart. Fixture data only (invented accounts). Every style in light and dark, to
 * `build/parity-shots/usage-*.png`, in Chinese and in English.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class UsageAccountBarsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/parity-shots").also { it.mkdirs() }
    private val now = Instant.parse("2026-11-23T04:00:00Z")
    private fun at(hours: Double) = now.plusSeconds((hours * 3600).toLong()).toString()
    private fun sampled(minutesAgo: Long) = now.minusSeconds(minutesAgo * 60).toString()
    private fun five(used: Double, hours: Double, protect: Double? = 98.0) = UsageWindow("5h", 300, used, at(hours), protectPercent = protect)
    private fun week(used: Double, hours: Double, protect: Double? = 99.0) = UsageWindow("7d", 10080, used, at(hours), protectPercent = protect)

    private val accounts = listOf(
        UsageAccount("c1", "claude", "Account 1", plan = "Team", sampledAt = sampled(4), windows = listOf(five(12.0, 1.5), week(72.0, 2.0))),
        UsageAccount("c2", "claude", "Account 2", plan = "Max", sampledAt = sampled(6), windows = listOf(five(3.0, 4.0), week(45.0, 18.0))),
        UsageAccount("c3", "claude", "Account 3", plan = "Max", sampledAt = sampled(9), windows = listOf(five(3.0, 5.0, 90.0), week(65.0, 50.0, 95.0))),
        UsageAccount("c4", "claude", "Account 4", plan = "Ultra", sampledAt = sampled(20), windows = listOf(five(0.0, 4.0), week(55.0, 100.0))),
        UsageAccount("c5", "claude", "Account 5", plan = "Team", sampledAt = sampled(30), windows = listOf(week(30.0, 150.0))),
        UsageAccount("x1", "codex", "Codex 1", plan = "Team", sampledAt = sampled(60), windows = listOf(five(0.0, 4.0, null), week(52.0, 72.0, null))),
        UsageAccount("x2", "codex", "Codex 2", plan = "Team", sampledAt = sampled(45), windows = listOf(five(0.0, 4.0, null), week(83.0, 18.0, null))),
    )

    private fun shoot(lang: String, expected: List<String>, described: List<String>) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var hidden by mutableStateOf(emptySet<String>())
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(2300.dp).background(PersonaTheme.palette.background).padding(horizontal = 16.dp)) {
                    LazyColumn { usageWindows(accounts, now, hidden) { provider, show -> hidden = if (show) hidden - provider else hidden + provider } }
                }
            }
        }
        for (s in AppStyle.entries) for (dk in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = dk }
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "usage-$lang-${s.name.lowercase()}-${if (dk) "dark" else "light"}.png")
                .outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            expected.forEach { assertTrue("${s.name} shows $it", rule.onAllNodesWithText(it, substring = true).fetchSemanticsNodes().isNotEmpty()) }
            // The protection line reads out with its value (the flag itself is part of the bar's description).
            described.forEach { assertTrue("${s.name} describes $it", rule.onAllNodesWithContentDescription(it, substring = true).fetchSemanticsNodes().isNotEmpty()) }
        }
    }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h2300dp-xhdpi")
    @Test fun chinese() = shoot("zh", listOf("订阅额度", "Claude", "5 个账号", "重置时间线", "显示的账号类型"), listOf("保护 98%"))

    @Config(sdk = [35], qualifiers = "en-w400dp-h2300dp-xhdpi")
    @Test fun english() = shoot("en", listOf("Subscription limits", "Claude", "5 accounts", "Reset timeline", "Account types shown"), listOf("Protect 98%"))
}
