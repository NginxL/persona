package persona.feature.inbox

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/**
 * A zero-action status is still a sentence, not a three-line summary. Direct cases exercise every tone; the detail
 * cases use records that really suppress both actions, including a due follow-up with a conclusion suggestion.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h1800dp-xhdpi")
class InboxZeroActionStatusLayoutTest {
    @get:Rule val rule = createComposeRule()
    private val repository = FakeInboxRepository()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun verify(cases: List<ZeroActionCase>, label: String) {
        var currentCase by mutableStateOf(cases.first())
        var recreation by mutableIntStateOf(0)
        var renderedText = ""
        repository.snapshot.value = InboxSnapshot(records = listOf(zeroActionRecord(cases.first())))
        rule.setContent {
            ZeroActionStatusFixture(currentCase, repository, navigator, AppStyle.Glass, dark = false,
                recreation = recreation, onText = { renderedText = it })
        }
        val measurements = mutableListOf<ZeroActionMeasurement>()
        for (case in cases) {
            rule.runOnIdle {
                repository.snapshot.value = InboxSnapshot(records = listOf(zeroActionRecord(case)))
                currentCase = case
                recreation++
                Snapshot.sendApplyNotifications()
            }
            rule.waitForIdle()
            measurements += measureZeroActionText(rule, case, renderedText)
        }
        writeZeroActionMeasurements(label, measurements)
        val failures = measurements.mapNotNull(ZeroActionMeasurement::failure)
        assertTrue("Zero-action status was cut (${failures.size}/${measurements.size} cases):\n" +
            failures.joinToString("\n"), failures.isEmpty())
    }

    @Test fun directEnglishEveryToneWidthScaleAndLength() {
        verify(zeroActionDirectCases("en"), "direct-en")
    }

    @Config(qualifiers = "zh-w360dp-h1800dp-xhdpi")
    @Test fun directSimplifiedChineseEveryToneWidthScaleAndLength() {
        verify(zeroActionDirectCases("zh-Hans"), "direct-zh-Hans")
    }

    @Config(qualifiers = "ja-w360dp-h1800dp-xhdpi")
    @Test fun directJapaneseEveryToneWidthScaleAndLength() {
        verify(zeroActionDirectCases("ja"), "direct-ja")
    }

    @Test fun detailEnglishRealZeroActionRecords() {
        verify(zeroActionDetailCases("en"), "detail-en")
    }

    @Config(qualifiers = "zh-w360dp-h1800dp-xhdpi")
    @Test fun detailSimplifiedChineseRealZeroActionRecords() {
        verify(zeroActionDetailCases("zh-Hans"), "detail-zh-Hans")
    }

    @Config(qualifiers = "ja-w360dp-h1800dp-xhdpi")
    @Test fun detailJapaneseRealZeroActionRecords() {
        verify(zeroActionDetailCases("ja"), "detail-ja")
    }
}
