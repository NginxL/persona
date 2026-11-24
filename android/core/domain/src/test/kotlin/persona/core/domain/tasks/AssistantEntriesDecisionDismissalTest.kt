package persona.core.domain.tasks

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.TaskReview
import persona.core.contracts.Todo
import java.time.Instant

/** A decision the owner marked Don't handle on another device leaves Needs you here too (#8262, web "review dismissal" cases). */
class AssistantEntriesDecisionDismissalTest {
    private val at = "2026-11-10T08:00:00.123456789Z"
    private val idle = Session(machineId = "box", sessionId = "worker", title = "Example Agent", cwd = "/example",
        state = "idle", status = "idle", updatedAt = at)

    /** As the hub sends it (docs/protocol/protocol.md `GET /api/v1/todos`), through the app's own decoder. */
    private fun todo(decisionReview: String?) = JsonCodec.decodeFromString(Todo.serializer(), """
        {"id":"task","title":"Example rollout","origin":"main","created_at":"$at","updated_at":"$at",
         "agents":[{"machine_id":"box","session_id":"worker","linked_at":"$at"}],
         "supervision":{"supervisor":"main","state":"needs_user","reviews":[{"at":"$at","verdict":"needs_user","note":"Choose the rollout route."}]}
         ${decisionReview?.let { ""","decision_review":$it""" } ?: ""}}
    """.trimIndent())

    private val dismissed = todo("""{"review_at":"$at","dismissal_id":"dismiss-demo"}""")

    private fun acceptedDecision(outcome: String, decisionReview: String?) = JsonCodec.decodeFromString(Todo.serializer(), """
        {"id":"task","title":"Example rollout","origin":"main","created_at":"$at","updated_at":"$at",
         "agents":[{"machine_id":"box","session_id":"worker","linked_at":"$at"}],
         "supervision":{"supervisor":"main","state":"accepted","reviews":[{"at":"$at","verdict":"accepted"}]},
         "ask":{"outcome":"$outcome","verdict":"Choose the rollout route.","asked_at":"$at"}
         ${decisionReview?.let { ""","decision_review":$it""" } ?: ""}}
    """.trimIndent())

    private fun run(todo: Todo, session: Session = idle, interactions: List<Interaction> = emptyList()) =
        assistantEntries("main", "main", listOf(todo), listOf(session), interactions, emptyList(), emptyList(),
            Instant.parse(at).toEpochMilli())

    @Test fun decodesTheHubsDecisionReview() {
        assertEquals("dismiss-demo", dismissed.decisionReview?.dismissalId)
        assertEquals(Instant.parse(at), dismissed.decisionReview?.reviewAt?.let(Instant::parse))
        assertNull(todo(null).decisionReview)
        assertTrue(dismissed.decisionDismissed())
    }

    @Test fun waitsOnTheOwnerUntilDismissed() {
        assertEquals(listOf("task:task"), run(todo(null)).needsYou.map { it.key })
        assertEquals(listOf("task:task"), run(todo("""{"review_at":"$at"}""")).needsYou.map { it.key })
    }

    @Test fun acceptedDecisionLeavesNeedsYouOnlyWhenDismissed() {
        for (outcome in listOf("decide", "cannot")) {
            val open = acceptedDecision(outcome, null)
            assertEquals("open $outcome", listOf("task:task"), run(open).needsYou.map { it.key })
            val current = acceptedDecision(outcome, """{"review_at":"$at","dismissal_id":"dismiss-current-round"}""")
            assertTrue(current.decisionDismissed())
            val got = run(current)
            assertTrue("dismissed $outcome needs you", got.needsYou.isEmpty())
            assertTrue("dismissed $outcome in progress", got.running.isEmpty())
        }
    }

    @Test fun hidesASameRoundDismissalAndDoesNotCountItInProgress() {
        val got = run(dismissed)
        assertTrue(got.needsYou.isEmpty())
        assertTrue(got.running.isEmpty())
    }

    @Test fun showsANewReviewOneNanosecondLater() {
        val next = "2026-11-10T08:00:00.123456790Z"
        val newer = dismissed.copy(supervision = dismissed.supervision!!.copy(
            reviews = listOf(TaskReview(at = next, verdict = "needs_user"))))
        assertFalse(newer.decisionDismissed())
        assertEquals(listOf("task:task"), run(newer).needsYou.map { it.key })
    }

    @Test fun keepsAPendingAgentQuestionVisible() {
        val question = Interaction("question", machineId = "box", sessionId = "worker", kind = "question", createdAt = at)
        val got = run(dismissed, interactions = listOf(question))
        assertEquals(listOf("task:task"), got.needsYou.map { it.key })
        assertEquals(listOf(question), (got.needsYou.single() as NeedsYouEntry.Task).asks)
    }

    @Test fun keepsAgentInputVisibleBeforeItsInteractionLoads() {
        val got = run(dismissed, idle.copy(state = "needs_you", status = "blocked"))
        assertEquals(listOf("task:task"), got.needsYou.map { it.key })
    }

    /** #8452: a review under an hour old is its supervisor's (In progress) whatever the dismissal; an overdue one is the
     *  owner's decision, so a same-round Don't handle hides it. */
    @Test fun dismissalOnlyHidesAReviewThatIsTheOwners() {
        fun pending(sinceMs: Long) = dismissed.copy(supervision = dismissed.supervision!!.copy(state = "pending_review",
            pendingSince = Instant.parse(at).minusMillis(sinceMs).toString()))
        val young = run(pending(REVIEW_OVERDUE_MS - 1))
        assertTrue(young.needsYou.isEmpty())
        assertEquals(listOf("task:task"), young.running.map { it.key })
        val overdue = run(pending(REVIEW_OVERDUE_MS))
        assertTrue(overdue.needsYou.isEmpty())
        assertTrue(overdue.running.isEmpty())
        val undismissed = pending(REVIEW_OVERDUE_MS).copy(decisionReview = null)
        assertEquals(listOf("task:task"), run(undismissed).needsYou.map { it.key })
    }
}
