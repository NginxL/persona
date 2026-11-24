package persona.feature.assistant

import android.content.res.Resources
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.CopyAction
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.JToastState

/** Small non-composable helpers for the page's existing feedback and clipboard actions. */
internal fun assistantSay(feedback: AppFeedback?, toast: JToastState, message: String) {
    if (feedback?.notice(message) != true) toast.show(FeedbackNotice(message))
}

internal fun assistantFailure(
    error: Exception,
    feedback: AppFeedback?,
    toast: JToastState,
    resources: Resources,
) {
    if (feedback?.failure(error) != true) toast.show(FeedbackNotice(
        persona.core.designsystem.operationFeedback(error).text.resolve(resources),
        style = persona.core.designsystem.FeedbackStyle.Error))
}

internal fun assistantHistoryFailed(
    error: Throwable,
    feedback: AppFeedback?,
    toast: JToastState,
    resources: Resources,
) {
    if (feedback?.failure(error) != true) toast.show(FeedbackNotice(
        persona.core.designsystem.operationFeedback(error).text.resolve(resources),
        style = persona.core.designsystem.FeedbackStyle.Error))
}

internal fun assistantHistoryNotice(
    message: String,
    feedback: AppFeedback?,
    toast: JToastState,
) {
    if (feedback?.notice(message, persona.core.designsystem.FeedbackStyle.Success) != true)
        toast.show(FeedbackNotice(message, style = persona.core.designsystem.FeedbackStyle.Success))
}

internal fun copyAssistantSessionId(key: SessionKey, resources: Resources, copy: CopyAction) {
    copy(key.sessionId, resources.getString(R.string.assistant_copied_session_id))
}

internal fun copyAssistantDiagnostics(session: Session, resources: Resources, copy: CopyAction) {
    copy(conversationDiagnostics(session), resources.getString(R.string.assistant_diagnostics_copied))
}
