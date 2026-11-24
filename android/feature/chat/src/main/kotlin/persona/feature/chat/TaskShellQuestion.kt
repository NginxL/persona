package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.flow.MutableStateFlow
import persona.core.contracts.PlanTask
import persona.core.contracts.TODO_START_ANSWER_FEATURE
import persona.core.contracts.Todo
import persona.core.contracts.shellQuestion
import persona.core.designsystem.ShellQuestionCard
import persona.core.domain.tasks.TasksStore

/**
 * A planned task's startup question, only on a hub that accepts its answer. The store shares Retry's busy key and
 * reconciles the returned to-do; unlike a session answer, no chat outbox message is resent here.
 */
@Composable
fun TaskShellQuestion(
    todo: Todo,
    task: PlanTask?,
    tasks: TasksStore,
    modifier: Modifier = Modifier,
): Boolean {
    val features by (tasks.features ?: remember { MutableStateFlow(emptySet<String>()) }).collectAsStateWithLifecycle()
    val busy by tasks.busy.collectAsStateWithLifecycle()
    val question = if (TODO_START_ANSWER_FEATURE in features) shellQuestion(task?.errorCode, task?.errorArgs) else null
    if (question == null) return false
    ShellQuestionCard(
        question,
        answer = { choice -> tasks.answerTaskStart(todo, choice) },
        modifier = modifier,
        enabled = TasksStore.retryKey(todo.id) !in busy,
    )
    return true
}
