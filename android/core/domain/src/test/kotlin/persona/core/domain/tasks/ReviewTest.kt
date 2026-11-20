package persona.core.domain.tasks

import org.junit.Test
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import persona.core.contracts.JsonCodec
import persona.core.contracts.Todo

class ReviewTest {
    private fun todo(json: String) = JsonCodec.decodeFromString(Todo.serializer(), json)

    @Test
    fun anOlderHubSendsNoSupervision() {
        val t = todo("""{"id":"todo_1","title":"部署"}""")
        assertNull(t.supervision)
        assertNull(t.reviewStage)
    }

    @Test
    fun readsTheLedgerAndMarksEachCriterion() {
        val t = todo("""{"id":"todo_1","title":"部署","supervision":{"supervisor":"main","done_when":["PR 合并到 master","截图贴进 PR"],
            "expect_minutes":30,"state":"rework","round":2,"something_new":1,
            "reviews":[{"at":"2026-11-05T09:00:00Z","verdict":"rework","round":1,"checks":[{"criterion":"PR 合并到 master","met":true},{"criterion":"截图贴进 PR","met":false,"evidence":"只有浅色"}]},
                       {"at":"2026-11-05T10:00:00Z","verdict":"rework","round":2,"note":"深色也补","checks":[{"criterion":"截图贴进 PR","met":false,"evidence":"深色的糊了"},{"criterion":"另外的","met":true}]}]}}""")
        val sup = t.supervision!!
        assertEquals(ReviewStage.Rework, t.reviewStage)
        assertEquals(30, sup.expectMinutes)
        assertEquals(listOf(
            CheckLine("PR 合并到 master", CheckMark.Met, null),
            CheckLine("截图贴进 PR", CheckMark.Missing, "深色的糊了"),
            CheckLine("另外的", CheckMark.Met, null),
        ), sup.checklist())
        assertEquals(2 to 3, sup.checkCount())
        assertEquals("深色也补", sup.latestReview?.note)
    }

    @Test
    fun aReopenedAcceptedTaskGoesOn() {
        assertEquals(ReviewStage.Ongoing, todo("""{"id":"t","supervision":{"supervisor":"m","state":"accepted"}}""").reviewStage)
        assertEquals(ReviewStage.Accepted, todo("""{"id":"t","done_at":"2026-11-05T10:00:00Z","supervision":{"supervisor":"m","state":"accepted"}}""").reviewStage)
        assertEquals(ReviewStage.Pending, todo("""{"id":"t","supervision":{"supervisor":"m","state":"pending_review"}}""").reviewStage)
        assertEquals(CheckMark.Open, todo("""{"id":"t","supervision":{"supervisor":"m","done_when":["a"]}}""").supervision!!.checklist().single().mark)
        assertNull(todo("""{"id":"t","supervision":{"supervisor":"m"}}""").supervision!!.checkCount())
    }

    /**
     * A continuation's new round (#11553): task_plan continues reopened an accepted task with a new Agent, and the hub kept
     * the acceptance in the reviews but put the ledger back to going on. The history alone never makes the to-do accepted;
     * more work sent to the accepted task's Agent (#3156) keeps the ledger, and the stage, accepted.
     */
    @Test
    fun aContinuationShowsItsNewRoundNotTheEarlierAcceptance() {
        val earlier = """{"at":"2026-11-20T02:23:57Z","verdict":"accepted","checks":[{"criterion":"a","met":true}]}"""
        val continued = todo("""{"id":"t","reopened_at":"2026-11-20T02:35:24Z","supervision":{"supervisor":"m","done_when":["a"],"state":"","round":0,"reviews":[$earlier]}}""")
        assertEquals(false, continued.acceptanceStands)
        assertEquals("", continued.reviewState)
        assertEquals(ReviewStage.Ongoing, continued.reviewStage)
        val standing = todo("""{"id":"t","supervision":{"supervisor":"m","done_when":["a"],"state":"accepted","reviews":[$earlier]}}""")
        assertEquals(true, standing.acceptanceStands)
        assertEquals(ReviewStage.Accepted, standing.reviewStage)
    }
}
