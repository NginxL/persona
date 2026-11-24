package persona.feature.assistant

import android.content.res.Resources
import androidx.compose.runtime.MutableState
import kotlinx.coroutines.CancellationException
import persona.core.contracts.ModelPick
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantConversations
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.AssistantSpace
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.MessageSender
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.JToastState
import persona.core.designsystem.operationFeedback
import persona.core.designsystem.readinessAction
import persona.feature.chat.ChatComposerState
import persona.feature.chat.ChatDraftEditor
import persona.feature.chat.ForwardPlacements
import persona.feature.chat.StarterMentions

/** Preserve a failed first message on its original assistant page. */
internal fun failAssistantFirstMessage(
    text: String,
    reason: String,
    fresh: NewChatStart?,
    launcher: String?,
    page: String,
    failedStarts: FailedStarts,
    state: AssistantSendState,
) {
    state.first.value = FirstMessage(text, failed = true, reason = reason,
        space = fresh?.space, launcher = launcher).also { failedStarts.write(page, it) }
}

/** Everything a prepared send needs, captured at the page's submit time. */
internal class AssistantPreparedSend(
    val text: String,
    val operationId: String,
    val active: SessionKey?,
    val fresh: NewChatStart?,
    val launcher: String?,
    val startsOne: Boolean,
    val forwards: List<persona.feature.chat.ChatLinkContext>,
    val sentDraft: persona.feature.chat.ChatComposerDraft?,
    val sentQuotes: List<persona.feature.chat.ChatLinkContext>,
    val manager: String?,
    val shownManager: String,
    val choice: AssistantManagerChoice.Choice,
    val snapshot: AssistantSnapshot,
    val page: String,
    val starterModel: String?,
    val starterEffort: String?,
)

/** Why a new conversation did not start: a coded setup refusal, else the existing generic failure. */
internal suspend fun assistantNewChatRefused(
    error: Exception,
    readinessOn: Boolean,
    readiness: MainReadiness,
    resources: Resources,
): String {
    val coded = operationFeedback(error).takeIf { it.action.readinessAction() != null }
    if (coded != null && readinessOn) readiness.refresh()
    return coded?.text?.resolve(resources) ?: resources.getString(R.string.assistant_new_conversation_failed)
}

/**
 * Why a domain space's first conversation did not start (#13500): what is missing for a coded refusal, as on any new
 * chat, else "Couldn't start the conversation:" with the reason, as the web says.
 */
internal suspend fun assistantSpaceStartRefused(
    error: Exception,
    readinessOn: Boolean,
    readiness: MainReadiness,
    resources: Resources,
): String {
    val feedback = operationFeedback(error)
    if (feedback.action.readinessAction() != null) return assistantNewChatRefused(error, readinessOn, readiness, resources)
    return resources.getString(R.string.assistant_spaces_start_failed, feedback.text.resolve(resources))
}

/** The background part of Assistant's original submit, with the page's existing state objects. */
internal suspend fun sendAssistantPrepared(
    request: AssistantPreparedSend,
    repository: AssistantRepository,
    conversations: AssistantConversations?,
    mainSender: MainConversationSender?,
    sender: MessageSender,
    state: AssistantSendState,
    composer: ChatComposerState,
    draftEditor: ChatDraftEditor?,
    mentions: StarterMentions,
    failedStarts: FailedStarts,
    resources: Resources,
    feedback: AppFeedback?,
    toast: JToastState,
    newChat: MutableState<NewChatStart?>,
    viewingMainQueue: MutableState<Boolean>,
    healthKeys: MutableState<Set<SessionKey>>,
    startConversation: suspend () -> SessionKey,
    startConversationWith: (suspend (String) -> SessionKey)?,
    startConversationPicked: (suspend (ModelPick) -> SessionKey)?,
    onStartFailure: suspend (Exception) -> String,
    onOpenConversation: (SessionKey) -> Unit,
    /** Every chat known to be a domain space's (#13500), by space; a chat started in one joins it. */
    spaceKeys: MutableState<Map<SessionKey, String>>? = null,
) {
    var accepted = false
    val submitted = request.text
    val launcher = request.launcher
    val fresh = request.fresh
    val startsOne = request.startsOne
    val forwards = request.forwards
    fun failed(reason: String) {
        state.first.value = FirstMessage(submitted, failed = true, reason = reason,
            space = fresh?.space, launcher = launcher).also { failedStarts.write(request.page, it) }
    }
    try {
        val target = when {
            launcher != null && startConversationWith != null -> {
                val picked = ModelPick(launcher, request.starterModel, request.starterEffort)
                val start: suspend (String) -> SessionKey = { l ->
                    if ((picked.model != null || picked.effort != null) && startConversationPicked != null) startConversationPicked(picked.copy(launcher = l))
                    else startConversationWith(l)
                }
                starterTarget(launcher, state.started.value, start).also { state.started.value = it }.session
            }
            fresh != null -> fresh.started ?: try {
                val space = fresh.space
                if (space != null) requireNotNull(conversations).startConversationIn(space).also {
                    newChat.value = fresh.copy(started = it)
                    // Only the health space's chats are health chats; another space's is that space's (#13500).
                    if (space == AssistantSpace.HEALTH) healthKeys.value = healthKeys.value + it
                    else spaceKeys?.let { keys -> keys.value = keys.value + (it to space) }
                } else startConversation().also { newChat.value = fresh.copy(started = it) }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                failed(onStartFailure(error))
                return
            }
            else -> request.active ?: if (conversations != null) {
                managerMainTarget(request.shownManager, request.snapshot, conversations::mainConversationOf)
            } else request.manager?.let { error("managers unavailable") }
        }
        sendAssistantMessage(mainSender, sender, target, submitted, request.operationId)
        accepted = true
        if (request.sentDraft != null) {
            composer.clearIfUnchanged(request.sentDraft)
            if (forwards.isNotEmpty()) ForwardPlacements.home =
                ForwardPlacements.home.filterNot { ref -> forwards.any { it.id == ref.id } }
            draftEditor?.changed(composer.draft)
        }
        mentions.sent(request.sentQuotes)
        if (startsOne) state.first.value = null
        state.clientId.value = null
        state.text.value = null
        if (AssistantManagerChoice.current.value != request.choice) return
        if (launcher != null && target != null) {
            state.launcher.value = null
            state.model.value = null
            state.effort.value = null
            state.started.value = null
            assistantAttempt { repository.refresh() }
            onOpenConversation(target)
        } else if (target != null) {
            onOpenConversation(target)
        } else if (request.active == null) viewingMainQueue.value = true
    } catch (cancelled: CancellationException) {
        throw cancelled
    } catch (error: Exception) {
        if (!accepted && !startsOne && forwards.isNotEmpty()) ForwardPlacements.home =
            restoreForwards(forwards, ForwardPlacements.home)
        if (startsOne) failed(operationFeedback(error).text.resolve(resources))
        else if (feedback?.failure(error) != true)
            toast.show(FeedbackNotice(operationFeedback(error).text.resolve(resources), style = FeedbackStyle.Error))
    } finally {
        state.sending.value = false
    }
}
