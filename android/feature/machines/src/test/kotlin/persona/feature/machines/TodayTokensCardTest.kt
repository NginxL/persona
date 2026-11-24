package persona.feature.machines

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.JsonCodec
import persona.core.contracts.MachineMetrics
import persona.core.contracts.MachineUsageCard
import persona.core.contracts.UsageTokens
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class TodayTokensCardTest {
    @get:Rule val rule = createComposeRule()

    @Test fun formatsTokenCountsLikeIos() {
        assertEquals("0", formatTokenCount(0))
        assertEquals("999", formatTokenCount(999))
        assertEquals("1K", formatTokenCount(1_000))
        assertEquals("9.24M", formatTokenCount(9_240_000))
        assertEquals("46.6M", formatTokenCount(46_600_000))
        assertEquals("228M", formatTokenCount(228_000_000))
        assertEquals("30.2K", formatTokenCount(30_200))
        assertEquals("1B", formatTokenCount(999_900_000))
    }

    @Test fun shareBarWidthsRespectMinimumAndSumToTotal() {
        val widths = computeShareBarWidths(listOf(172.0, 46.6, 1.0), width = 300f, gap = 2f, minimum = 7f)
        assertEquals(3, widths.size)
        assertEquals(7f, widths[2], 0.01f)
        assertEquals(300f, widths.sum() + 2f * 2, 0.1f)
    }

    @Test fun deserializesMachineMetricsWithOptionalUsageCard() {
        val raw = """{"machine_id":"server","online":true,"points":[],"usage":{"today":{"in":100,"cache_write":200,"cache_read":9700,"out":500,"req":12},"per_minute":30200,"recent":[10,20]}}"""
        val decoded = JsonCodec.decodeFromString<MachineMetrics>(raw)
        val usage = requireNotNull(decoded.usage)
        assertEquals(10_500L, usage.today.total)
        assertEquals(0.97, requireNotNull(usage.today.hitRate), 0.001)
        assertEquals(30_200L, usage.perMinute)
        assertEquals(listOf(10L, 20L), usage.recent)
    }

    @Test fun todayTokensCardRendersTotalsRateHitRateAndRankedLegend() {
        val shares = listOf(
            "Mac Studio" to MachineUsageCard(UsageTokens(input = 1_000_000, cacheRead = 44_000_000, output = 1_600_000), perMinute = 4_400),
            "Server" to MachineUsageCard(UsageTokens(input = 5_000_000, cacheWrite = 3_000_000, cacheRead = 160_000_000, output = 4_000_000), perMinute = 25_800),
            "NAS" to MachineUsageCard(UsageTokens(input = 240_000, cacheRead = 8_500_000, output = 500_000), perMinute = 0),
        )
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                TodayTokensCard(shares)
            }
        }
        rule.onNodeWithText("Today's tokens").assertIsDisplayed()
        rule.onNodeWithText("228M").assertIsDisplayed()
        rule.onNodeWithText("30.2K/min").assertIsDisplayed()
        rule.onNodeWithText("96%").assertIsDisplayed()
        rule.onNodeWithText("Server").assertIsDisplayed()
        rule.onNodeWithText("172M").assertIsDisplayed()
        rule.onNodeWithText("Mac Studio").assertIsDisplayed()
        rule.onNodeWithText("46.6M").assertIsDisplayed()
        rule.onNodeWithText("NAS").assertIsDisplayed()
        rule.onNodeWithText("9.24M").assertIsDisplayed()
    }
}
