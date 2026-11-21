package persona.feature.assistant

import android.content.res.Resources
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.Session
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantRepository
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.Haptic
import persona.core.designsystem.JToastState
import persona.core.designsystem.operationFeedback

/** Existing hub conversation mutations, with the page's optimistic state and feedback callbacks. */
internal class AssistantConversationMutations(
    private val management: AssistantManagement?,
    private val repository: AssistantRepository,
    private val scope: CoroutineScope,
    private val haptics: (Haptic) -> Unit,
    private val feedback: AppFeedback?,
    private val toast: JToastState,
    private val resources: Resources,
) {
    private fun failed(error: Exception) {
        if (feedback?.failure(error) != true) toast.show(FeedbackNotice(
            operationFeedback(error).text.resolve(resources), style = FeedbackStyle.Error))
    }

    fun archive(session: Session, archivedWords: String, hide: () -> Unit, restore: () -> Unit) {
        val api = management ?: return
        hide()
        haptics(Haptic.Tap)
        scope.launch {
            try {
                api.archiveConversation(session.key)
                if (feedback?.notice(archivedWords, FeedbackStyle.Success) != true)
                    toast.show(FeedbackNotice(archivedWords, style = FeedbackStyle.Success))
                assistantAttempt { repository.refresh() }.onSuccess { restore() }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                restore()
                failed(error)
            }
        }
    }

    fun setLeader(session: Session, leaders: List<String>) {
        val api = management ?: return
        val leader = session.sessionId in leaders
        scope.launch {
            try {
                if (leader) api.endDirection(session.key) else api.makeDirection(session.key)
                assistantAttempt { repository.refresh() }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                failed(error)
            }
        }
    }
}
