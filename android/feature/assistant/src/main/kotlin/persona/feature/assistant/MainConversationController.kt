package persona.feature.assistant

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.State
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.SessionCompaction
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantRefused
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.MainConversationSender
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.JToastState
import persona.core.designsystem.operationFeedback

/** Compact the selected Assistant conversation; replace and clear only the manager's main conversation. */
@Stable
internal class MainConversationController(
    private val management: AssistantManagement?,
    private val repository: AssistantRepository,
    private val sender: MainConversationSender?,
    private val scope: CoroutineScope,
    private val resources: Resources,
    private val feedback: AppFeedback?,
    private val toast: JToastState,
    private val owner: String,
    private val active: State<SessionKey?>,
) {
    var newMainOpen by mutableStateOf(false)
    var newMainBusy by mutableStateOf(false)
    val carrySummary = mutableStateOf(true)
    var clearBusy by mutableStateOf(false)
    var compacting by mutableStateOf(false)
    var compaction by mutableStateOf(CompactionStatus())
    private var compactionKey: SessionKey? = null
    val clearedMain = mutableStateOf<SessionKey?>(null)

    private fun snapshot() = repository.assistant.value.takeIf { it.managerId == owner }
    private fun main() = snapshot()?.mainSession

    private fun refusal(error: Exception): String =
        if (error is AssistantRefused) error.message.orEmpty() else operationFeedback(error).text.resolve(resources)

    private fun notify(message: String, style: FeedbackStyle = FeedbackStyle.Info) {
        if (feedback?.notice(message) != true) toast.show(FeedbackNotice(message, style = style))
    }

    private fun notifyError(message: String) {
        if (feedback?.notice(message, FeedbackStyle.Error) != true)
            toast.show(FeedbackNotice(message, style = FeedbackStyle.Error))
    }

    private suspend fun replaced(previous: SessionKey) {
        assistantAttempt { sender?.mainReplaced(previous) }
        assistantAttempt { repository.refresh() }
    }

    fun dismissCompaction() {
        compaction = compaction.dismissed()
    }

    fun observeCompaction(key: SessionKey?, run: SessionCompaction?) {
        if (compactionKey != key) {
            compactionKey = key
            compaction = CompactionStatus()
        }
        compaction = compaction.observe(run)
    }

    fun compact(active: SessionKey?, snapshot: AssistantSnapshot, shownCompaction: SessionCompaction?) {
        val key = active ?: return
        val api = management ?: return
        if (compacting) return
        compacting = true
        observeCompaction(key, shownCompaction)
        compaction = compaction.asked(shownCompaction)
        scope.launch {
            try {
                api.compactConversation(key)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                if (this@MainConversationController.active.value == key) {
                    compaction = compaction.refused(snapshot.conversations.firstOrNull { it.key == key }?.compaction)
                }
                notify(refusal(error))
            } finally {
                compacting = false
            }
        }
    }

    fun replace(main: SessionKey?, newConversation: () -> Unit) {
        val api = management ?: return
        val previous = main ?: return
        if (newMainBusy) return
        newMainBusy = true
        val carry = carrySummary.value
        scope.launch {
            try {
                val result = api.newMainConversation(carry, previous.sessionId)
                newMainOpen = false
                // The next message to the main conversation starts the new one, here and from every other entry.
                replaced(previous)
                newConversation()
                notify(resources.getString(newMainNotice(result.carried, carry)))
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                newMainOpen = false
                if ((error as? AssistantRefused)?.code == "main_changed") replaced(previous)
                notify(refusal(error))
            } finally {
                newMainBusy = false
            }
        }
    }

    /** No confirmation: the old conversation is archived. [onFailed] restores the composer's /clear. */
    fun clear(main: SessionKey?, active: SessionKey?, onFailed: () -> Unit = {}, onCleared: () -> Unit, onOpen: (SessionKey) -> Unit) {
        val api = management ?: return
        val previous = main?.takeIf { it == active } ?: return
        if (clearBusy) return
        clearBusy = true
        // Before the request: the hub's update without the main conversation may come before its answer.
        clearedMain.value = previous
        scope.launch {
            try {
                api.clearMainConversation(previous.sessionId)
                replaced(previous)
                val now = main()
                if (this@MainConversationController.active.value == previous) onCleared()
            } catch (cancelled: CancellationException) {
                clearedMain.value = null
                throw cancelled
            } catch (error: Exception) {
                clearedMain.value = null
                if ((error as? AssistantRefused)?.code == "main_changed") {
                    replaced(previous)
                    val now = main()
                    if (this@MainConversationController.active.value == previous) now?.takeUnless { it == previous }?.let(onOpen) ?: onCleared()
                    notify(refusal(error))
                } else {
                    onFailed()
                    notifyError(resources.getString(R.string.assistant_clear_context_failed, refusal(error)))
                }
            } finally {
                clearBusy = false
            }
        }
    }
}

/** Value-returning set-up keeps these state reads and effects in AssistantScreen's recomposition scope. */
@Composable
internal fun rememberMainConversationController(
    management: AssistantManagement?,
    repository: AssistantRepository,
    sender: MainConversationSender?,
    resources: Resources,
    feedback: AppFeedback?,
    toast: JToastState,
    owner: String,
    shownCompaction: SessionCompaction?,
    active: State<SessionKey?>,
    mainState: State<SessionKey?>,
): MainConversationController {
    val scope = rememberCoroutineScope()
    val controller = remember(management, repository, sender, resources, feedback, toast, owner, active) {
        MainConversationController(management, repository, sender, scope, resources, feedback, toast, owner, active)
    }
    LaunchedEffect(active.value, shownCompaction) { controller.observeCompaction(active.value, shownCompaction) }
    LaunchedEffect(controller.compaction.line) {
        if (controller.compaction.line == CompactionLine.Done) {
            delay(CompactionDoneMillis)
            controller.dismissCompaction()
        }
    }
    var carrySummary by controller.carrySummary
    val currentMain by mainState
    var clearedMain by controller.clearedMain
    LaunchedEffect(currentMain, clearedMain) {
        if (currentMain != null && currentMain != clearedMain) controller.clearedMain.value = null
    }
    return controller
}
