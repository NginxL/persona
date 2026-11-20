package persona.feature.settings

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ExtensionTile
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.generated.ExtensionGauge
import persona.core.designsystem.GaugeSize
import persona.core.designsystem.JTile
import persona.core.designsystem.PersonaTheme

/** UI version 9's percentage, complete readout, meter range and ordinary tile action. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ExtensionGaugeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun percentageClampsAndRoundsLikeTheSharedContract() {
        assertEquals(0, gaugePercent(Double.NaN))
        assertEquals(0, gaugePercent(Double.NEGATIVE_INFINITY))
        assertEquals(0, gaugePercent(Double.POSITIVE_INFINITY))
        assertEquals(0, gaugePercent(-0.2))
        assertEquals(42, gaugePercent(0.424))
        assertEquals(43, gaugePercent(0.425))
        assertEquals(100, gaugePercent(1.2))
    }

    @Test fun readoutUsesValueAndUnitOnlyWhenAValueExists() {
        assertEquals(
            "Storage: 42 GB, 42%",
            gaugeReadout(ExtensionTile(title = "Storage", value = "42", gauge = ExtensionGauge(0.424, "GB"))),
        )
        assertEquals(
            "Storage: 42%",
            gaugeReadout(ExtensionTile(title = "Storage", gauge = ExtensionGauge(0.424, "GB"))),
        )
        assertEquals(
            "Storage: 42, 42%",
            gaugeReadout(ExtensionTile(title = "Storage", value = "42", gauge = ExtensionGauge(0.424))),
        )
        assertEquals(
            "Storage: 42",
            gaugeReadout(ExtensionTile(title = "Storage", value = "42")),
        )
    }

    @Test fun aBadgeFloatsOverTheGaugeSoBadgedAndUnbadgedRingsStartAtOneHeight() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Row(Modifier.width(400.dp)) {
                    JTile(
                        "Used",
                        Modifier.weight(1f).testTag("plain-tile"),
                        gauge = { Box(Modifier.size(GaugeSize.page).testTag("plain-ring")) },
                        gaugePercent = 40f,
                        accessibilityLabel = "Used: 40%",
                    )
                    JTile(
                        "Fable this week",
                        Modifier.weight(1f).testTag("badged-tile"),
                        gauge = { Box(Modifier.size(GaugeSize.page).testTag("badged-ring")) },
                        gaugePercent = 62f,
                        accessibilityLabel = "Fable this week: 62%",
                        badge = { Text("Low") },
                    )
                }
            }
        }
        val plain = rule.onNodeWithTag("plain-ring", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val badged = rule.onNodeWithTag("badged-ring", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertEquals("both gauge tiles start their ring on one line", plain.top, badged.top, 1f)
        // The badge keeps living inside the tile's merged semantics, not beside it.
        val semantics = rule.onNodeWithTag("badged-tile").fetchSemanticsNode().config
        assertEquals("Fable this week: 62%", semantics.getOrNull(SemanticsProperties.ContentDescription)?.single())
        assertTrue(
            "the badge is read with the tile",
            semantics.getOrNull(SemanticsProperties.Text).orEmpty().any { it.text == "Low" },
        )
    }

    @Test fun actionableGaugeTileKeepsMeterAndButtonSemantics() {
        val action = ExtensionViewAction("open-storage", values = mapOf("source" to "gauge"))
        var pressed: ExtensionViewAction? = null
        val tile = ExtensionTile(
            title = "Storage",
            value = "42",
            gauge = ExtensionGauge(0.424, "GB"),
            action = action,
        )
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ExtensionTileView(tile, ExtensionViewState(), ExtensionViewHandle(
                    press = { _, sent -> pressed = sent },
                    edit = { _, _ -> },
                    choose = {},
                    open = {},
                ))
            }
        }
        val node = rule.onNodeWithContentDescription("Storage: 42 GB, 42%")
        node.assertIsDisplayed()
        val semantics = node.fetchSemanticsNode().config
        assertEquals(ProgressBarRangeInfo(42f, 0f..100f), semantics[SemanticsProperties.ProgressBarRangeInfo])
        node.performClick()
        assertEquals(action, pressed)
    }
}
