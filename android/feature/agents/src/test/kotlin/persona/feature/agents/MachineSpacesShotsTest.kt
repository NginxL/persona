package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentPreferences
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.AgentSpace
import persona.core.domain.sort.agentSpaces
import java.io.File
import java.time.Instant

/**
 * A computer's projects page as iOS draws it (`MachineSpacesView`, #4338 A11), on a computer with a pinned project, one that
 * only waits in the background, a muted one, two idle ones and two agents of another host that are in no space: Glass light
 * and dark, Brief and Board for the record, and the page of a computer that is offline with nothing running. PNGs in
 * `build/machine-projects-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
class MachineSpacesShotsTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-01-02T12:00:00Z")

    private fun agent(
        id: String, title: String, state: String, words: String?, space: String?, tab: String? = null, index: Int? = null,
        updated: String = "2026-01-02T11:00:00Z", machine: String = "mac", waiting: Boolean = false, awaits: Boolean = false,
    ) = AgentSession(Session(machine, id, title = title, cwd = "/code/${space ?: "loose"}", state = state, spaceKind = space?.let { "herdr" },
        spaceId = space, spaceName = space, slotId = tab?.let { "t-$it" }, slotName = tab, slotIndex = index, lastText = words, updatedAt = updated,
        backgroundWaiting = waiting, awaitsReply = awaits, agent = "claude", model = "claude-sonnet-4-5"))

    private val rows = listOf(
        agent("1", "Fix the login bug", "working", "Updating the retry logic in the login handler.", "app", "login", 1, "2026-01-02T11:55:00Z"),
        agent("2", "Approve the migration", "needs_you", "Run the migration on the staging database?", "app", "migration", 2, "2026-01-02T11:40:00Z"),
        agent("3", "Update the docs", "unread", "The guide is updated.", "app", "docs", 3, "2026-01-02T10:00:00Z"),
        agent("4", "Review the refactor", "idle", "Two places swallow errors.", "app", "review", 4, "2026-01-01T09:00:00Z"),
        agent("5", "Watch the CI run", "working", "Waiting for the nightly test job to finish.", "weather", "ci", 1, "2026-01-02T11:30:00Z", waiting = true),
        agent("6", "Try another layout", "working", "Moving the forecast card up.", "notes", "layout", 1, "2026-01-02T08:00:00Z"),
        agent("7", "Draft the launch post", "idle", "Drafted three openings.", "blog", "draft", 1, "2026-01-01T12:00:00Z"),
        agent("8", "Resize the images", "idle", "All images resized.", "blog", "images", 2, "2025-12-30T12:00:00Z"),
        agent("9", "Sort the receipts", "idle", "Receipts sorted by month.", "scratch", "receipts", 1, "2025-12-28T12:00:00Z"),
        agent("10", "Draft the release notes", "working", "Writing the summary of this week's changes.", null, updated = "2026-01-02T11:50:00Z"),
        agent("11", "Check the changelog", "idle", "Nothing missing.", null, updated = "2026-01-02T07:00:00Z"),
        agent("12", "Roll out the fix", "working", "Rolling out to the second region.", "api", "deploy", 1, machine = "server"),
    )
    private val machines = listOf(
        Machine("mac", "MacBook Pro", online = true, icon = "laptop", hostname = "example-mac.local"),
        Machine("server", "Workstation", online = true, icon = "server"),
        Machine("away", "Old laptop", online = false, icon = "laptop"),
        Machine("quiet", "Spare desktop", online = true, icon = "desktop"),
    )
    private val preferences = AgentsPreferencesSnapshot(
        pins = AgentPreferences(spaces = listOf("mac/app")), mutes = AgentPreferences(spaces = listOf("mac/notes")))

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var machineId by mutableStateOf("mac")
    private val calls = mutableListOf<String>()

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val out = File(System.getProperty("persona.shots") ?: "build/machine-projects-shots").also { it.mkdirs() }
        File(out, "$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    /**
     * How much lighter (0 to 255) the summary card is than the page around it, read from the drawn picture on the line of the
     * computer's status word: left of the computer's tile, inside the card's padding, against the page's own margin.
     */
    private fun cardLift(): Int {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val density = rule.density.density
        val word = rule.onNodeWithText("Online").getUnclippedBoundsInRoot()
        val y = ((word.top.value + word.bottom.value) / 2 * density).toInt()
        fun luma(x: Float): Int = bitmap.getPixel((x * density).toInt(), y).let { (android.graphics.Color.red(it) + android.graphics.Color.green(it) + android.graphics.Color.blue(it)) / 3 }
        return luma(24f) - luma(2f)
    }

    private fun show(rows: List<AgentSession> = this.rows) = rule.setContent {
        PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("shot").size(400.dp, 1100.dp).background(PersonaTheme.palette.background)) {
                MachineSpacesScreen(machineId, machines.firstOrNull { it.id == machineId }, rows, preferences, now, loaded = true,
                    onBack = { calls += "back" }, onDetails = { calls += "details" },
                    onOpenSpace = { calls += "space:${it.id}" }, onOpenAgent = { calls += "agent:${it.id}" })
            }
        }
    }

    @Test fun drawsTheProjectsInGlassLightAndDark() {
        show()
        for (nextDark in listOf(false, true)) {
            rule.runOnIdle { dark = nextDark }
            rule.waitForIdle()
            // The summary: the computer, online, its host, how many projects and agents.
            rule.onNodeWithText("MacBook Pro").assertIsDisplayed()
            rule.onNodeWithText("Online").assertIsDisplayed()
            rule.onNodeWithText("example-mac.local").assertIsDisplayed()
            rule.onNodeWithText("6 projects · 11 agents").assertIsDisplayed()
            // Sections: the pinned project first, then those with something going on; idle ones below.
            rule.onNodeWithText("Working").assertIsDisplayed()
            rule.onNodeWithText("Idle").assertIsDisplayed()
            rule.onNodeWithText("app").assertIsDisplayed()
            // A busy project says its lead (the most urgent agent: the one that needs you) and its words.
            rule.onNodeWithText("migration").assertIsDisplayed()
            rule.onNodeWithText("Run the migration on the staging database?", substring = true).assertIsDisplayed()
            // An idle one: how many agents and its tabs.
            rule.onNodeWithText("2 agents · draft · images").assertIsDisplayed()
            // Dark: the cards are the system's lighter gray on the black page (translucent, they were near-invisible beside their
            // edge highlight, which hung over nothing). Light keeps its own fill, a shade off the white page.
            if (nextDark) assertTrue("the dark card is lighter than the page (${cardLift()})", cardLift() >= 20)
            capture("machine-projects")
        }
    }

    @Test fun briefAndBoardDrawTheSamePage() {
        show()
        for (next in listOf(AppStyle.Brief, AppStyle.Board)) {
            rule.runOnIdle { style = next }
            rule.waitForIdle()
            rule.onNodeWithText("MacBook Pro").assertIsDisplayed()
            capture("machine-projects")
        }
    }

    @Test fun aTapOpensAProjectOrAnAgentOutsideAnySpaceAndTheToolbarOpensTheComputersDetails() {
        show()
        rule.onNodeWithText("weather").performClick()
        rule.onNodeWithText("blog").performClick()
        rule.onNodeWithText("Draft the release notes").performClick()
        rule.onNodeWithContentDescription("Machine details").performClick()
        rule.onNodeWithContentDescription("Back").performClick()
        // The agents outside any space are one project: each is a row of its own, which opens the agent and nothing else.
        assertEquals(listOf("space:mac/weather", "space:mac/blog", "agent:mac/10", "details", "back"), calls)
        // The agent of another computer is not listed.
        assertEquals(0, rule.onAllNodesWithText("Roll out the fix").fetchSemanticsNodes().size)
    }

    @Test fun aComputerWithNothingRunningSaysSoAndHowToStartSomething() {
        machineId = "away"
        show()
        rule.onNodeWithText("Old laptop").assertIsDisplayed()
        rule.onNodeWithText("Offline").assertIsDisplayed()
        rule.onNodeWithText("0 projects · 0 agents").assertIsDisplayed()
        rule.onNodeWithText("No agents running").assertIsDisplayed()
        rule.onNodeWithText("This machine is offline.").assertIsDisplayed()
        capture("machine-projects-offline")
        // Online and empty, it says how to start something.
        rule.runOnIdle { machineId = "quiet"; dark = true }
        rule.onNodeWithText("Spare desktop").assertIsDisplayed()
        rule.onNodeWithText("Online").assertIsDisplayed()
        rule.onNodeWithText("Start Claude, Codex, or TRAE CLI in herdr on this machine and it shows up here.").assertIsDisplayed()
        capture("machine-projects-empty")
    }

    @Test fun beforeTheComputersAreReadThePageIsNamedByItsIdAndShowsNoEmptyState() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                Box(Modifier.testTag("shot").size(400.dp, 600.dp).background(PersonaTheme.palette.background)) {
                    MachineSpacesScreen("mac", null, emptyList(), preferences, now, loaded = false, onBack = {}, onDetails = {}, onOpenSpace = {}, onOpenAgent = {})
                }
            }
        }
        rule.onNodeWithText("mac").assertIsDisplayed()
        assertEquals(0, rule.onAllNodesWithText("No agents running").fetchSemanticsNodes().size)
    }
}
