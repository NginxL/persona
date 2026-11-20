package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Item
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.ToolCall
import persona.core.domain.tasks.QuickResult
import persona.core.domain.tasks.QuickResultUndo

internal fun parsedQuickResult(resultJson: String, toolName: String = "phone_calendar_create_event"): QuickResult =
    requireNotNull(QuickResult.of(ToolCall(
        LineItem(Item("use-quick-result", kind = "tool_use", toolName = toolName)),
        LineItem(Item("result-quick-result", kind = "tool_result", text = resultJson)), emptyList())))

/** A quick result shows what was added, as on iOS, without inventing a source device (#6864), with its Undo (#9340). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class QuickResultCardTest {
    @get:Rule val rule = createComposeRule()

    private val result = QuickResult(reminder = false, callId = "call-1", itemId = 42, title = "Dentist",
        time = "2026-11-13T15:30:00Z", place = "Personal")

    private fun details(result: QuickResult): String {
        val time = LocalizedFormats.format(Instant.parse(result.time!!), LocalizedFormats.MONTH_DAY_TIME, Locale.ENGLISH)
        return "$time · ${result.title} · ${result.place}"
    }

    @Test fun showsResultDetailsWithoutInventingADeviceSource() {
        val time = LocalizedFormats.format(Instant.parse(result.time!!), LocalizedFormats.MONTH_DAY_TIME, Locale.ENGLISH)
        rule.setContent { PersonaTheme(reduceMotion = true) { QuickResultCard(result) } }

        rule.onNodeWithText("Added to calendar").assertExists()
        rule.onNodeWithText("$time · ${result.title} · ${result.place}").assertExists()
        rule.onAllNodes(hasText("iPhone", substring = true), useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun showsTheAndroidSourceAndKeepsResultDetailsAndUndo() {
        val result = parsedQuickResult("""{"id":"42","title":"Dentist","start":"2026-11-13T15:30:00Z","calendar":"Personal","source_device_name":"Pixel 9","source_device_platform":"android"}""")
        rule.setContent { PersonaTheme(reduceMotion = true) { QuickResultCard(result, onUndo = {}) } }

        rule.onNodeWithText("Added to calendar").assertExists()
        rule.onNodeWithText("Pixel 9", substring = true).assertExists()
        rule.onNodeWithText(details(result)).assertExists()
        rule.onNodeWithText("Undo").assertIsEnabled()
        rule.onAllNodes(hasText("iPhone", substring = true), useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun showsTheActualIPhoneSource() {
        val result = parsedQuickResult("""{"id":"R1","title":"Call","due":"2026-11-13T15:30:00Z","list":"Personal","source_device_name":"Work iPhone","source_device_platform":"ios"}""",
            toolName = "phone_reminders_create")
        rule.setContent { PersonaTheme(reduceMotion = true) { QuickResultCard(result) } }

        rule.onNodeWithText("Reminder added").assertExists()
        rule.onNodeWithText("Work iPhone", substring = true).assertExists()
        rule.onNodeWithText(details(result)).assertExists()
    }

    @Test fun showsAnotherKnownSourceWithoutCallingItAnIPhone() {
        val result = parsedQuickResult("""{"id":"E2","title":"Review","start":"2026-11-13T15:30:00Z","calendar":"Work","source_device_name":"Mate 70","source_device_platform":"harmony"}""")
        rule.setContent { PersonaTheme(reduceMotion = true) { QuickResultCard(result) } }

        rule.onNodeWithText("Added to calendar").assertExists()
        rule.onNodeWithText("Mate 70", substring = true).assertExists()
        rule.onNodeWithText(details(result)).assertExists()
        rule.onAllNodes(hasText("iPhone", substring = true), useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun showsAKnownPlatformWhenTheDeviceNameIsMissing() {
        val android = parsedQuickResult("""{"id":"E1","title":"Dentist","start":"2026-11-13T15:30:00Z","source_device_platform":"android"}""")
        val iphone = parsedQuickResult("""{"id":"R1","title":"Call","due":"2026-11-13T15:30:00Z","source_device_platform":"ios"}""",
            toolName = "phone_reminders_create")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Column {
                    QuickResultCard(android)
                    QuickResultCard(iphone)
                }
            }
        }

        rule.onAllNodes(hasText("Android", substring = true), useUnmergedTree = true).assertCountEquals(1)
        rule.onAllNodes(hasText("iPhone", substring = true), useUnmergedTree = true).assertCountEquals(1)
    }

    @Test fun missingOrBlankSourceMakesNoDeviceClaimAndKeepsDetails() {
        val missing = parsedQuickResult("""{"id":"E1","title":"Dentist","start":"2026-11-13T15:30:00Z","calendar":"Personal"}""")
        val blank = parsedQuickResult("""{"id":"E2","title":"Checkup","start":"2026-11-13T15:30:00Z","calendar":"Work","source_device_name":"  ","source_device_platform":""}""")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Column {
                    QuickResultCard(missing)
                    QuickResultCard(blank)
                }
            }
        }

        val expected = listOf("Added to calendar", details(missing), "Added to calendar", details(blank))
        val actual = rule.onAllNodes(hasText("", substring = true), useUnmergedTree = true).fetchSemanticsNodes()
            .flatMap { it.config[SemanticsProperties.Text] }.map { it.text }
        assertEquals(expected.sorted(), actual.sorted())
        rule.onAllNodes(hasText("iPhone", substring = true), useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun undoShowsOnlyWhileTheEventIsOnThisPhone() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                androidx.compose.foundation.layout.Column {
                    // Offered: the event is this phone's, nothing undone yet.
                    QuickResultCard(result, onUndo = {})
                    // A card without an event id has nothing to take back.
                    QuickResultCard(result.copy(itemId = null))
                    // Already taken back: the words say so and the button is gone.
                    QuickResultCard(result, QuickResultUndo.Undone, onUndo = {})
                }
            }
        }

        rule.onNodeWithText("Undo").assertExists()
        rule.onAllNodesWithText("Undo").assertCountEquals(1)
        rule.onNodeWithText("Undone").assertExists()
        rule.onAllNodesWithText("Added to calendar").assertCountEquals(2)
    }

    @Test fun undoRunsOnceAndTakesNoSecondTapWhileItRuns() {
        var taps = 0
        rule.setContent { PersonaTheme(reduceMotion = true) { QuickResultCard(result, onUndo = { taps++ }) } }

        rule.onNodeWithText("Undo").assertIsEnabled().performClick()
        assertEquals(1, taps)
    }

    @Test fun anUndoingCardTakesNoTap() {
        rule.setContent { PersonaTheme(reduceMotion = true) { QuickResultCard(result, QuickResultUndo.Undoing, onUndo = {}) } }

        rule.onNodeWithText("Undo").assertIsNotEnabled()
    }
}
