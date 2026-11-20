package persona.feature.agents

import org.junit.Assert.assertEquals
import org.junit.Test

/** The steps of a to-do's stage bar (#4636): the six, or once its work closed with nothing left to deploy (#11590) the
 *  ones it went through and Done, never a step it did not take. */
class TodoStageStepsTest {
    @Test fun anOpenBarHasTheSixSteps() {
        val six = listOf(R.string.agents_todo_stage_plan, R.string.agents_todo_stage_develop, R.string.agents_todo_stage_review,
            R.string.agents_todo_stage_merge, R.string.agents_todo_stage_deploy, R.string.agents_todo_stage_verify)
        TodoStage.entries.forEach { assertEquals(six, todoStageSteps(it, done = false)) }
    }

    @Test fun aFinishedBarEndsWithDoneAfterTheStepsTaken() {
        assertEquals(listOf(R.string.agents_todo_stage_plan, R.string.agents_todo_stage_develop, R.string.agents_todo_stage_done),
            todoStageSteps(TodoStage.Develop, done = true))
        assertEquals(listOf(R.string.agents_todo_stage_plan, R.string.agents_todo_stage_develop, R.string.agents_todo_stage_review,
            R.string.agents_todo_stage_merge, R.string.agents_todo_stage_done), todoStageSteps(TodoStage.Merge, done = true))
    }
}
