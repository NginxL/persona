package persona.feature.agents

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertHasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoDelivery
import persona.core.designsystem.PersonaTheme

/** The To-dos row names its lead Agent as Overview's task card does (#10904), without changing its existing content. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp")
class TodoAgentMetadataTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-19T10:00:00Z")

    private fun agent(
        id: String,
        machine: String = "mac",
        state: String = "idle",
        model: String? = null,
        effort: String? = null,
        updatedAt: String = "2026-11-19T08:00:00Z",
        stateSince: String? = null,
    ) = Session(
        machineId = machine,
        sessionId = id,
        title = "Synthetic $id",
        status = if (state == "working") "working" else "idle",
        state = state,
        model = model,
        effort = effort,
        updatedAt = updatedAt,
        stateSince = stateSince,
        lastText = "Synthetic $id summary",
    )

    private fun todo(id: String, agents: List<TodoAgent>, delivery: TodoDelivery? = null) = Todo(
        id = id,
        title = "Synthetic task $id",
        createdAt = "2026-11-19T07:00:00Z",
        updatedAt = "2026-11-19T07:00:00Z",
        agents = agents,
        delivery = delivery,
    )

    private fun show(todo: Todo, sessions: List<Session>) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodosScreen(listOf(todo), sessions, null, onBack = {}, onOpenTodo = {}, now = now)
            }
        }
        rule.waitForIdle()
    }

    private fun hasText(text: String, substring: Boolean = false) =
        rule.onAllNodesWithText(text, substring = substring, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()

    /** Give the untouched row's red assertion its actual localized count, not a missing-node error. */
    private fun assertAgentMetadata(expected: String, currentFallback: String) {
        val actual = when {
            hasText(expected, substring = true) -> expected
            hasText(currentFallback) -> currentFallback
            else -> null
        }
        assertEquals("Agent metadata", expected, actual)
        rule.onAllNodesWithText(currentFallback, useUnmergedTree = true).assertCountEquals(0)
    }

    private fun assertMore(count: Int) {
        rule.onNodeWithText("+$count", substring = true, useUnmergedTree = true).assertExists()
    }

    @Test
    fun oneAgentNamesComputerFriendlyModelAndEffortWithoutChangingTheRow() {
        val todo = todo("one", listOf(TodoAgent("mac", "s1")))
        show(todo, listOf(agent("s1", state = "working", model = "claude-opus-5-5", effort = "xhigh")))

        rule.onNodeWithText(todo.title, useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Synthetic s1 summary", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("In progress", useUnmergedTree = true).assertExists()
        rule.onNode(hasContentDescription("Done")).assertHasClickAction()
        assertAgentMetadata("mac · Opus 5.5 · Very high", "1 agent")
        rule.onAllNodesWithText("+1", substring = true, useUnmergedTree = true).assertCountEquals(0)
    }

    @Test
    fun severalAgentsPreferTheOneWorkingAndCountOtherUnreleasedAgents() {
        val todo = todo("working", listOf(
            TodoAgent("mac", "idle"),
            TodoAgent("Studio", "working"),
            TodoAgent("mac", "later"),
            TodoAgent("mac", "released", releasedAt = "2026-11-19T07:30:00Z"),
        ))
        val sessions = listOf(
            agent("idle", model = "claude-sonnet-5-5", effort = "low", updatedAt = "2026-11-19T09:00:00Z"),
            agent("working", machine = "Studio", state = "working", model = "claude-opus-5-5", effort = "max",
                updatedAt = "2026-11-19T08:00:00Z"),
            agent("later", model = "claude-haiku-5-5", updatedAt = "2026-11-19T09:30:00Z"),
            agent("released", state = "working", model = "claude-haiku-5-5", updatedAt = "2026-11-19T09:45:00Z"),
        )
        show(todo, sessions)

        assertAgentMetadata("Studio · Opus 5.5 · Max", "3 agents")
        assertMore(2)
    }

    @Test
    fun severalWorkingAgentsUseTheMostRecentlyMovedOne() {
        val todo = todo("working-order", listOf(TodoAgent("mac", "first"), TodoAgent("Studio", "second")))
        val sessions = listOf(
            agent("first", state = "working", model = "claude-haiku-5-5", effort = "low",
                updatedAt = "2026-11-19T08:00:00Z", stateSince = "2026-11-19T10:00:00Z"),
            agent("second", machine = "Studio", state = "working", model = "claude-opus-5-5", effort = "high",
                updatedAt = "2026-11-19T09:00:00Z", stateSince = "2026-11-19T09:00:00Z"),
        )
        show(todo, sessions)

        assertAgentMetadata("mac · Haiku 5.5 · Low", "2 agents")
        assertMore(1)
    }

    @Test
    fun severalAgentsWithNoneWorkingUseMaxUpdatedAtAndStateSince() {
        val todo = todo("idle-order", listOf(TodoAgent("mac", "old"), TodoAgent("Studio", "new")))
        val sessions = listOf(
            agent("old", model = "claude-sonnet-5-5", updatedAt = "2026-11-19T08:00:00Z"),
            agent("new", machine = "Studio", model = "claude-opus-5-5", updatedAt = "2026-11-19T08:00:00Z",
                stateSince = "2026-11-19T09:00:00Z"),
        )
        show(todo, sessions)

        assertAgentMetadata("Studio · Opus 5.5", "2 agents")
        assertMore(1)
    }

    @Test
    fun noEffortOmitsTheEffortSegment() {
        val todo = todo("no-effort", listOf(TodoAgent("mac", "codex")))
        show(todo, listOf(agent("codex", model = "gpt-5.5")))

        assertAgentMetadata("mac · GPT-5.5", "1 agent")
    }

    @Test
    fun unknownModelIdUsesAFriendlyNameRatherThanTheRawId() {
        val rawModel = "acme/orbit-7"
        val todo = todo("unknown-model", listOf(TodoAgent("mac", "unknown")))
        show(todo, listOf(agent("unknown", model = rawModel, effort = "medium")))

        assertAgentMetadata("mac · Orbit 7 · Medium", "1 agent")
        rule.onAllNodesWithText(rawModel, substring = true, useUnmergedTree = true).assertCountEquals(0)
    }

    @Test
    fun oneKnownSessionNamesItsAgentAndCountsTheUnknownOther() {
        val todo = todo("partial", listOf(TodoAgent("mac", "known"), TodoAgent("mac", "not-loaded")))
        show(todo, listOf(agent("known", model = "claude-opus-5-5")))

        assertAgentMetadata("mac · Opus 5.5", "2 agents")
        assertMore(1)
    }

    @Test
    fun minimalEffortUsesTheAgentChatLabel() {
        val todo = todo("minimal", listOf(TodoAgent("mac", "minimal")))
        show(todo, listOf(agent("minimal", model = "claude-opus-5-5", effort = "minimal")))

        assertAgentMetadata("mac · Opus 5.5 · Minimal", "1 agent")
    }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp")
    @Test
    fun noKnownSessionRetainsTheLocalizedAgentCountFallback() {
        val todo = todo("fallback", listOf(TodoAgent("mac", "not-loaded"), TodoAgent("mac", "deleted")))
        show(todo, emptyList())

        rule.onNodeWithText("2 个 Agent", useUnmergedTree = true).assertExists()
        rule.onAllNodesWithText("+1", substring = true, useUnmergedTree = true).assertCountEquals(0)
    }

    @Test
    fun existingDeliveryInformationStaysBeforeAgentMetadata() {
        val todo = todo("held", listOf(TodoAgent("mac", "held")),
            delivery = TodoDelivery("pr", unblock = "user", at = "2026-11-19T08:00:00Z"))
        show(todo, listOf(agent("held", model = "claude-opus-5-5", effort = "high")))

        val hold = "Stopped at a PR · needs your OK"
        rule.onNodeWithText(hold, useUnmergedTree = true).assertExists()
        assertAgentMetadata("mac · Opus 5.5 · High", "1 agent")
        val holdTop = rule.onNodeWithText(hold, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot.top
        val metadataTop = rule.onNodeWithText("mac · Opus 5.5 · High", substring = true, useUnmergedTree = true)
            .fetchSemanticsNode().boundsInRoot.top
        assertTrue("Delivery information stays before Agent metadata", holdTop < metadataTop)
    }
}
