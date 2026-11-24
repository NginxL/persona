package persona.core.domain.tasks

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Test
import persona.core.contracts.PlanTask
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo

/** The failed start's plan lookup, launcher suggestion and coded remedies, kept in step with web #10365. */
class TaskStartRemediesTest {
    private val todo = Todo("task", origin = "main")

    @Test fun ownPlanIdTakesPrecedenceOverAnotherPlanThatTookTheTodoOver() {
        val own = PlanTask(title = "Own plan task", todoId = todo.id)
        val takeover = PlanTask(title = "Other plan task", todoId = todo.id)
        val plans = linkedMapOf(
            "takeover" to TaskPlan("takeover", state = "started", tasks = listOf(takeover)),
            "own" to TaskPlan("own", state = "started", tasks = listOf(own)),
        )

        assertSame(own, planTaskOf(todo.copy(planId = "own"), plans))
    }

    @Test fun withoutPlanIdTheFirstNonDismissedTakeoverPlanWins() {
        val dismissed = PlanTask(title = "Dismissed", todoId = todo.id)
        val first = PlanTask(title = "First live plan", todoId = todo.id)
        val later = PlanTask(title = "Later live plan", todoId = todo.id)
        val plans = linkedMapOf(
            "dismissed" to TaskPlan("dismissed", state = "dismissed", tasks = listOf(dismissed)),
            "first" to TaskPlan("first", state = "started", tasks = listOf(first)),
            "later" to TaskPlan("later", state = "open", tasks = listOf(later)),
        )

        assertSame(first, planTaskOf(todo, plans))
        assertNull(planTaskOf(todo, mapOf("dismissed" to plans.getValue("dismissed"))))
        assertNull(planTaskOf(todo, emptyMap()))
    }

    @Test fun retrySuggestsOnlyTheFirstNonBlankLauncherWithTheExactHubFeature() {
        val task = PlanTask(todoId = todo.id, retryWith = listOf("claude", "work"))
        val enabled = setOf(TASK_RETRY_LAUNCHER_FEATURE)

        assertEquals("claude", retryLauncher(task, enabled))
        assertNull(retryLauncher(task, setOf("todo_seen")))
        assertNull(retryLauncher(task, setOf("task_retry_launcher_extra")))
        assertNull(retryLauncher(task, setOf("TASK_RETRY_LAUNCHER")))
        assertNull(retryLauncher(task.copy(retryWith = listOf("  ", "work")), enabled))
        assertNull(retryLauncher(task.copy(retryWith = emptyList()), enabled))
        assertNull(retryLauncher(null, enabled))
    }

    @Test fun launcherMissingIsTerminalOnlyAndNeedsItsLauncherName() {
        val terminal = mapOf("where" to "terminal", "launcher" to "work", "missing" to "/opt/persona/personad")

        assertEquals(StartFailureCode.TerminalLauncher("work"), startFailureCode("launcher_missing", terminal))
        assertNull(startFailureCode("launcher_missing", terminal + ("where" to "list")))
        assertNull(startFailureCode("launcher_missing", terminal - "where"))
        assertNull(startFailureCode("launcher_missing", terminal - "launcher"))
        assertNull(startFailureCode("launcher_missing", terminal + ("launcher" to "")))
        assertNull(startFailureCode("launcher_missing", null))
    }

    @Test fun worktreePathTakenIsKnownAndOtherCodesKeepTheRawTextFallback() {
        assertSame(StartFailureCode.WorktreePathTaken,
            startFailureCode("worktree_path_taken", mapOf("path" to "/worktrees/task")))
        assertSame(StartFailureCode.WorktreePathTaken, startFailureCode("worktree_path_taken", null))
        assertNull(startFailureCode("something_new", mapOf("where" to "terminal", "launcher" to "work")))
        assertNull(startFailureCode(null, mapOf("where" to "terminal", "launcher" to "work")))
    }
}
