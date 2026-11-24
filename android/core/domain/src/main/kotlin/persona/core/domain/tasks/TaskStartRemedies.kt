package persona.core.domain.tasks

import persona.core.contracts.PlanTask
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo

/** The hub retries a failed task on the launcher named in the request. */
const val TASK_RETRY_LAUNCHER_FEATURE = "task_retry_launcher"

/**
 * The plan task a to-do runs as: its own plan first, then the first non-dismissed plan that took it over through
 * `todo_id`. A taken-over to-do need not have `plan_id`.
 */
fun planTaskOf(todo: Todo, plans: Map<String, TaskPlan>): PlanTask? {
    todo.planId?.let { id -> plans[id]?.tasks?.firstOrNull { it.todoId == todo.id }?.let { return it } }
    for (plan in plans.values) {
        if (plan.state == "dismissed") continue
        plan.tasks.firstOrNull { it.todoId == todo.id }?.let { return it }
    }
    return null
}

/** The first launcher the hub suggests, only when this hub accepts a launcher-specific Retry. */
fun retryLauncher(task: PlanTask?, features: Set<String>): String? {
    if (TASK_RETRY_LAUNCHER_FEATURE !in features) return null
    return task?.retryWith?.firstOrNull()?.takeIf(String::isNotBlank)
}

/** The coded start failures with a short, plain-language row line. */
sealed interface StartFailureCode {
    data class TerminalLauncher(val launcher: String) : StartFailureCode
    data object WorktreePathTaken : StartFailureCode
}

/** Read coded failure arguments before the raw error; unknown and nonterminal codes retain the text fallback. */
fun startFailureCode(code: String?, args: Map<String, String>?): StartFailureCode? = when (code) {
    "launcher_missing" -> args?.get("launcher")?.takeIf(String::isNotEmpty)
        ?.takeIf { args["where"] == "terminal" }?.let { StartFailureCode.TerminalLauncher(it) }
    "worktree_path_taken" -> StartFailureCode.WorktreePathTaken
    else -> null
}
