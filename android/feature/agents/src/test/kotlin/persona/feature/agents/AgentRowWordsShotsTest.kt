package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.Workflow
import persona.core.designsystem.*
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentListDisplay
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.agentSpaces
import java.io.File
import java.time.Instant

/**
 * What the Agents rows say in words (#4338 A9): Avatar's latest words take up to two lines as iOS's do; State cards spell out
 * "Not running" (a pinned agent whose computer is out of reach) and "Working (Background)" (a workflow runs after its turn
 * ended) at their foot; the space page's cards say the same. Glass, and Brief and Board where they must not change; light and
 * dark. PNGs in `build/row-words-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class AgentRowWordsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val workflow = Workflow(taskId = "t1", runId = "wf_1", name = "nightly-checks", status = "running", running = true,
        phases = listOf("Build", "Verify"), phase = "Verify", phaseIndex = 2, agentsStarted = 12, agentsDone = 9, agentsRunning = 3,
        startedAt = "2026-01-01T00:00:00Z")

    private fun session(id: String, title: String, state: String, words: String, tab: Int, workflows: List<Workflow>? = null, agent: String = "claude") =
        Session("mac", id, title = title, cwd = "/code/app", state = state, agent = agent, spaceKind = "herdr", spaceId = "w1", spaceName = "app",
            spaceIndex = 1, slotId = "t$tab", slotName = id, slotIndex = tab, paneId = "w1:p$tab", lastText = words, workflows = workflows,
            model = "claude-sonnet-4-5", updatedAt = "2026-01-01T00:00:00Z")

    private val sessions = listOf(
        session("login", "Fix the login bug", "working", tab = 1,
            words = "Updating the retry logic in the login handler, then adding a test for the expired-token case and running the whole auth suite again to confirm that nothing else broke."),
        session("migration", "Approve the migration", "needs_you", tab = 2,
            words = "Run the database migration on the staging cluster before the nightly backup starts? It drops two unused columns and cannot be rolled back."),
        session("ci", "Watch the CI run", "working", tab = 3, workflows = listOf(workflow), words = "Waiting for the nightly test job to finish."),
        session("layout", "Try the old layout", "offline", tab = 4, words = "The layout compiles; the screenshots are still to do."),
        session("docs", "Update the docs", "unread", tab = 5, words = "The guide is updated."),
    )
    private val machines = listOf(Machine("mac", "MacBook Pro", online = true, icon = "laptop"))

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)

    private fun open(): AgentsViewModel {
        val model = agentsModel(sessions, machines, pins = listOf("mac/layout"))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "Agents", onSelect = {}, leading = {})) {
                    AgentsScreen(model, NoNavigation, AgentsMetadata())
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
        return model
    }

    private fun out() = File(System.getProperty("persona.shots") ?: "build/row-words-shots").also { it.mkdirs() }

    private fun save(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
        File(out(), "$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun lines(words: String): Int {
        val results = mutableListOf<TextLayoutResult>()
        // The row's own text, not the merged node of the whole row (which reads its first text's layout).
        val _ = rule.onNode(hasText(words, substring = true), useUnmergedTree = true).fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action?.invoke(results)
        return results.single().lineCount
    }

    @Test fun avatarWordsAndCardWordsInEveryStyle() {
        val model = open()
        var avatarLines = 0
        var notRunning = 0
        var inBackground = 0
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d; model.grouping(AgentGrouping.MachineSpace); model.display(AgentListDisplay.Avatar) }
            rule.waitForIdle()
            save("avatar-words")
            if (s == AppStyle.Glass && !d) avatarLines = lines("Updating the retry logic")
            if (s == AppStyle.Glass) {
                rule.runOnIdle { model.display(AgentListDisplay.Cards) }
                rule.waitForIdle()
                save("cards-words")
                notRunning += rule.onAllNodesWithText("Not running").fetchSemanticsNodes().size
                inBackground += rule.onAllNodesWithText("Working (Background)").fetchSemanticsNodes().size
            }
        }
        // iOS gives the words two lines (`lineLimit(2)`); State cards spell out both states (light and dark: twice each).
        assertEquals(2, avatarLines)
        assertEquals(2, notRunning)
        assertEquals(2, inBackground)
    }

    /** The space page's cards write the state as iOS's `stateLabel`: Working (Background) while a workflow runs. */
    @Test fun theSpacePageSaysWorkingInTheBackground() {
        val space = agentSpaces(listOf(sessions[0], sessions[2], sessions[1], sessions[4]).map(::AgentSession)).single()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").size(400.dp, 700.dp).background(PersonaTheme.palette.background)) {
                    SpaceScreen(space, machines, emptyList<Interaction>(), AgentsPreferencesSnapshot(), busy = false, canStart = true,
                        toast = JToastState(), now = Instant.parse("2026-01-01T00:10:00Z"), onDismiss = {}, onOpen = {}, onNewTab = {},
                        spaceMenu = {}, sessionMenu = { emptyList() })
                }
            }
        }
        for (d in listOf(false, true)) {
            rule.runOnIdle { dark = d }
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "space-cards-glass-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.onNodeWithText("Working (Background)").assertExists()
    }
}
