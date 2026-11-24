package persona.feature.agents

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Direction
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.DirectionTree
import persona.core.contracts.Supervision
import persona.core.contracts.TaskReview
import persona.core.contracts.Todo
import persona.core.contracts.TodoAsk
import persona.core.contracts.TodoDelivery
import persona.core.contracts.TodoLineageStep
import persona.core.designsystem.PersonaTheme

/** A cancelled task stays distinct from completed work, and its detail opens either side of a takeover (#5732). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp")
class TodoCancellationLineageTest {
    @get:Rule val rule = createComposeRule()

    private val cancelled = Todo(
        id = "new",
        title = "Revised attempt",
        doneAt = "2026-11-12T08:00:00Z",
        doneBy = "cancelled",
        cancelledAt = "2026-11-12T08:00:00Z",
        cancelledReason = "Superseded by the revised plan",
        lineage = listOf(
            TodoLineageStep("old", title = "First attempt", supersededBy = "new"),
            TodoLineageStep("new", title = "Revised attempt", continues = "old", current = true),
        ),
    )

    @Test fun cancelledListRowHasItsOwnSectionAndReason() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodosScreen(listOf(cancelled), emptyList(), null, onBack = {}, onOpenTodo = {})
            }
        }
        rule.onNodeWithText("Cancelled").performClick()
        rule.onNodeWithText("Revised attempt").assertExists()
        rule.onNodeWithText("Cancelled: Superseded by the revised plan").assertExists()
    }

    @Test fun cancelledRowStaysOutOfAStaleDirectionTree() {
        val open = Todo("open", title = "Open work")
        val tree = DirectionTree(listOf(Direction("release", "Release direction", tasks = listOf(
            DirectionTask(cancelled.id, state = DirectionTaskState.Working),
            DirectionTask(open.id, state = DirectionTaskState.Working),
        ))))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodosScreen(listOf(cancelled, open), emptyList(), null, onBack = {}, onOpenTodo = {}, directions = tree)
            }
        }
        rule.onNodeWithTag("todos-filter").performClick()
        rule.onNodeWithText("Pinned").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Release direction").assertExists()
        rule.onNodeWithText("Open work").assertExists()
        // The stale tree still names the cancelled id, but the row belongs only to its folded Cancelled section.
        rule.onNodeWithText("Revised attempt").assertDoesNotExist()
        rule.onNodeWithText("Cancelled").performClick()
        rule.onNodeWithText("Revised attempt").assertExists()
        rule.onNodeWithText("Cancelled: Superseded by the revised plan").assertExists()
    }

    @Test fun detailShowsCancellationAndOpensThePreviousCard() {
        var opened: String? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodoDetailScreen(
                    "new", listOf(cancelled), emptyList(), emptyList(), null,
                    onBack = {}, onOpenSession = {}, onHandOff = {}, onOpenTodo = { opened = it },
                )
            }
        }
        rule.onNodeWithText("Cancelled").assertExists()
        rule.onNodeWithText("Cancelled: Superseded by the revised plan").assertExists()
        rule.onNodeWithText("Task history").assertExists()
        rule.onNodeWithText("Continues from First attempt", substring = true).assertExists()
        rule.onNodeWithTag("todo-lineage-old").performClick()
        assertEquals("old", opened)
    }

    @Test fun oldDetailOpensTheCardThatTookOver() {
        val old = cancelled.copy(
            id = "old",
            title = "First attempt",
            cancelledReason = "Replaced by a revised attempt",
            lineage = cancelled.lineage?.map { it.copy(current = it.todoId == "old") },
        )
        var opened: String? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodoDetailScreen(
                    "old", listOf(old, cancelled), emptyList(), emptyList(), null,
                    onBack = {}, onOpenSession = {}, onHandOff = {}, onOpenTodo = { opened = it },
                )
            }
        }
        rule.onNodeWithText("Continued by Revised attempt", substring = true).assertExists()
        rule.onNodeWithTag("todo-lineage-new").performClick()
        assertEquals("new", opened)
    }

    @Test fun deliveredTaskDoesNotReadNotStarted() {
        val delivered = Todo(
            "delivery", title = "Delivered work",
            supervision = Supervision(supervisor = "main", state = "pending_review"),
            delivery = TodoDelivery(state = "live", at = "2026-11-12T08:00:00Z"),
        )
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodoDetailScreen("delivery", listOf(delivered), emptyList(), emptyList(), null,
                    onBack = {}, onOpenSession = {}, onHandOff = {})
            }
        }
        rule.onNodeWithText("Not started").assertDoesNotExist()
        rule.onNodeWithText("Delivered · awaiting review").assertExists()
    }

    @Test fun acceptedOwnerDecisionReadsWaitingForYou() {
        val waiting = Todo(
            "waiting-accepted", title = "Owner action after reopening",
            supervision = Supervision(supervisor = "main", state = "accepted",
                reviews = listOf(TaskReview(at = "2026-11-12T07:00:00Z", verdict = "accepted"))),
            ask = TodoAsk(outcome = "decide", verdict = "Choose the release scope", askedAt = "2026-11-12T08:00:00Z"),
        )
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodoDetailScreen("waiting-accepted", listOf(waiting), emptyList(), emptyList(), null,
                    onBack = {}, onOpenSession = {}, onHandOff = {})
            }
        }
        rule.onNodeWithText("Waiting for you").assertExists()
    }

    @Test fun ownerWaitReadsWaitingForYou() {
        val waiting = Todo("waiting", title = "Owner action", supervision = Supervision(supervisor = "main", state = "needs_user"))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodoDetailScreen("waiting", listOf(waiting), emptyList(), emptyList(), null,
                    onBack = {}, onOpenSession = {}, onHandOff = {})
            }
        }
        rule.onNodeWithText("Waiting for you").assertExists()
    }
}
