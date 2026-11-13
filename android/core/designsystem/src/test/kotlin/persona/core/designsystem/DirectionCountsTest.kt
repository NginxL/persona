package persona.core.designsystem

import android.content.Context
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Direction
import persona.core.contracts.DirectionCounts

/**
 * A direction's end on the To-dos page's 常驻 sections (#6626), as the drawer's 常驻 row shows it: the working spinner
 * exactly while anything under it runs (the hub's `running`, not the working count), then "N need you"; never "N working".
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class DirectionCountsTest {
    @get:Rule val rule = createComposeRule()

    private fun draw(direction: Direction) =
        rule.setContent { PersonaTheme(darkTheme = false, reduceMotion = true) { JDirectionCounts(direction) } }

    /** The spinner is the only node carrying the loading description ([JSpinner]). */
    private fun spinners() = rule.onAllNodes(hasContentDescription(
        ApplicationProvider.getApplicationContext<Context>().getString(R.string.designsystem_loading)))

    @Test fun spinsWhileSomethingRunsWithWhatWaitsOnYouInWords() {
        // a task waits on you while another of the direction's Agents is still at work: no working task counted
        draw(Direction("a", counts = DirectionCounts(working = 0, needsYou = 1), running = true))
        spinners().assertCountEquals(1)
        rule.onNodeWithText("1 need you").assertExists()
        rule.onAllNodesWithText("working", substring = true).assertCountEquals(0)
    }

    @Test fun aWorkingCountWithNothingRunningDoesNotSpin() {
        // its working task's Agents all stopped: nothing to spin for, and nothing waits on you
        draw(Direction("a", counts = DirectionCounts(working = 2), running = false))
        spinners().assertCountEquals(0)
        rule.onAllNodesWithText("working", substring = true).assertCountEquals(0)
    }
}
