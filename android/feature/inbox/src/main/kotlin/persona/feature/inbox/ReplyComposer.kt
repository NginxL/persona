package persona.feature.inbox

import persona.core.designsystem.*
import androidx.compose.runtime.Composable
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.ui.Modifier
import androidx.compose.runtime.remember
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.res.stringResource
import androidx.annotation.StringRes
import persona.core.contracts.api.LarkMention
import persona.core.contracts.api.MailReplyTarget
import persona.core.contracts.api.MailReplyTargetChanged
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.withContext
import kotlin.coroutines.CoroutineContext
import kotlin.coroutines.EmptyCoroutineContext
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

data class ReplyComposerState(
    val draft: ReplyDraft = ReplyDraft(""),
    val loading: Boolean = true,
    val sending: Boolean = false,
    val error: ReplyError? = null,
    val sent: Boolean = false,
    val delivery: ReplyDelivery = ReplyDelivery.Ready,
    val targetChanged: Boolean = false,
) {
    val canEdit: Boolean get() = !loading && !sending && delivery in setOf(ReplyDelivery.Ready, ReplyDelivery.Refused)
    val actionLabel: String @Composable get() = when {
        delivery == ReplyDelivery.Verifying -> PendingStatus.Verifying.localizedLabel
        delivery in setOf(ReplyDelivery.Unknown, ReplyDelivery.NeedsConfirmation) -> stringResource(R.string.inbox_check_result)
        delivery in setOf(ReplyDelivery.Queued, ReplyDelivery.WaitingForNetwork) -> PendingStatus.Waiting.localizedLabel
        sending -> PendingStatus.Sending.localizedLabel
        else -> stringResource(R.string.inbox_send)
    }
}

/** Why a reply did not go (or may not have): the composer runs outside composition, so it keeps the text's id. */
enum class ReplyError(@StringRes val text: Int) {
    Unconfirmed(R.string.inbox_reply_error_unconfirmed),
    UnconfirmedShort(R.string.inbox_reply_error_unconfirmed_short),
    DraftRead(R.string.inbox_reply_error_draft_read),
    DraftSave(R.string.inbox_reply_error_draft_save),
    Refused(R.string.inbox_reply_error_refused),
    NotSaved(R.string.inbox_reply_error_not_saved),
    TargetChanged(R.string.inbox_reply_error_target_changed),
    TargetChangedNotSaved(R.string.inbox_reply_error_target_changed_not_saved),
    NotSent(R.string.inbox_reply_error_not_sent),
    CantConfirm(R.string.inbox_reply_error_cant_confirm),
    CheckFailed(R.string.inbox_reply_error_check_failed),
}

/**
 * Serialize draft writes and preserve the request ID when a send needs retrying. [initialText] fills an empty reply
 * that has no saved draft; [initialAi] says it came from AI (the AI twin's draft), which sending then confirms.
 */
class ReplyComposer(
    private val repository: InboxRepository,
    private val target: ReplyTarget,
    private val scope: CoroutineScope,
    initialText: String = "",
    initialAi: Boolean = initialText.isNotBlank(),
) {
    private val mutableState = MutableStateFlow(ReplyComposerState())
    val state = mutableState.asStateFlow()
    private val writes = Mutex()
    /** Nothing saved and nothing typed yet: an AI draft offered later may still fill the reply. */
    @Volatile private var pristine = true
    @Volatile private var offered: String? = null

    init {
        scope.launch {
            try {
                val saved = repository.readDraft(target)
                if (saved != null) pristine = false
                val draft = saved ?: ReplyDraft(initialText, ai = initialAi && initialText.isNotBlank())
                val delivery = repository.replyStatus(target, draft.clientId)
                mutableState.value = ReplyComposerState(
                    draft = if (delivery == ReplyDelivery.Sent) ReplyDraft("") else draft,
                    loading = false, sent = delivery == ReplyDelivery.Sent,
                    delivery = if (delivery == ReplyDelivery.Sent) ReplyDelivery.Ready else delivery,
                    error = if (delivery == ReplyDelivery.Unknown) ReplyError.Unconfirmed else null,
                )
                applyOffer()
            } catch (error: CancellationException) {
                throw error
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(error = ReplyError.DraftRead)
            }
        }
    }

    /**
     * The AI twin's draft, known only once the inbox item is: it fills the reply if the user has neither a saved
     * draft nor typed anything (a page that opened before the list came in); otherwise the user's text stays.
     */
    fun offerAiDraft(text: String) {
        if (text.isBlank()) return
        offered = text
        applyOffer()
    }

    private fun applyOffer() {
        val text = offered ?: return
        val current = state.value
        if (!pristine || current.loading || current.sent || !current.canEdit || current.draft.text.isNotBlank()) return
        offered = null
        pristine = false
        mutableState.value = current.copy(draft = ReplyDraft(text, ai = true), error = null)
    }

    /**
     * Replaces the reply with a draft AI wrote ("AI draft" / "AI edit"), under a new request ID. Refused while a send is
     * on its way or its result is unclear: that request keeps its text and ID.
     */
    fun replaceWithAi(text: String): Boolean {
        val current = state.value
        if (!current.canEdit || text.isBlank()) return false
        pristine = false
        val draft = ReplyDraft(text, ai = true)
        mutableState.value = current.copy(draft = draft, error = null, sent = false, delivery = ReplyDelivery.Ready, targetChanged = false)
        save(draft)
        return true
    }

    init {
        scope.launch {
            repository.replyUpdates(target).collect {
                val current = state.value
                if (!current.loading && !current.sending) {
                    val clientId = current.draft.clientId
                    val delivery = repository.replyStatus(target, clientId)
                    // The answer is about the draft as it was when it was asked for: an edit (a new id) or a send
                    // started meanwhile stays as it is (#9907).
                    mutableState.update { now ->
                        when {
                            now.loading || now.sending || now.draft.clientId != clientId -> now
                            delivery == ReplyDelivery.Sent -> ReplyComposerState(draft = ReplyDraft(""), loading = false, sent = true)
                            else -> now.copy(delivery = delivery)
                        }
                    }
                }
            }
        }
    }

    fun update(text: String) = edit(text, state.value.draft.mentions)

    /** Pick a person for the active @ query; the new text and its recipients share one durable draft. */
    fun mention(mention: LarkMention) {
        val current = state.value
        if (!current.canEdit || larkMentionQuery(current.draft.text) == null) return
        edit(larkInsertMention(current.draft.text, mention), current.draft.mentions + mention)
    }

    private fun edit(text: String, picked: List<LarkMention>) {
        val current = state.value
        if (!current.canEdit || current.draft.text == text && current.draft.mentions == picked) return
        pristine = false
        // Edited AI text is still AI's draft; clearing it makes the reply the user's own. Deleted @names stay pruned even
        // if an undo later restores the characters: only a new explicit pick can restore that recipient.
        val draft = ReplyDraft(text, ai = current.draft.ai && text.isNotBlank(), mentions = larkMentions(text, picked))
        mutableState.value = current.copy(draft = draft, error = null, sent = false, delivery = ReplyDelivery.Ready)
        save(draft)
    }

    private fun save(draft: ReplyDraft) {
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            try {
                withContext(NonCancellable) { writes.withLock { repository.saveDraft(target, draft) } }
            } catch (error: CancellationException) {
                throw error
            } catch (_: Exception) {
                mutableState.value = state.value.copy(error = ReplyError.DraftSave)
            }
        }
    }

    /** [context]: the sending page's (its manager pins), as the composer is shared by every page of the target. */
    fun send(mailTarget: MailReplyTarget? = null, context: CoroutineContext = EmptyCoroutineContext) {
        val current = state.value
        if (current.loading || current.sending || current.draft.text.isBlank()) return
        if (current.delivery in setOf(ReplyDelivery.Queued, ReplyDelivery.WaitingForNetwork, ReplyDelivery.Verifying)) return
        if (current.delivery in setOf(ReplyDelivery.Unknown, ReplyDelivery.NeedsConfirmation)) { verify(); return }
        val text = current.draft.text
        val draft = current.draft.copy(mentions = larkMentions(text, current.draft.mentions))
        mutableState.value = current.copy(draft = draft, sending = true, error = null, sent = false,
            delivery = ReplyDelivery.Sending, targetChanged = false)
        scope.launch(context) {
            var submitted = false
            try {
                writes.withLock {
                    repository.saveDraft(target, draft)
                    submitted = true
                    repository.reply(target, ReplyRequest(text, draft.clientId, mailTarget, draft.mentions))
                }
                mutableState.value = ReplyComposerState(draft = ReplyDraft(""), loading = false, sent = true)
            } catch (error: CancellationException) {
                mutableState.value = state.value.copy(sending = false, delivery = ReplyDelivery.Unknown, error = ReplyError.UnconfirmedShort)
                throw error
            } catch (_: MailReplyTargetChanged) {
                targetChanged(current.draft)
            } catch (_: Exception) {
                val delivery = if (submitted) {
                    try { repository.replyStatus(target, current.draft.clientId).takeUnless { it == ReplyDelivery.Ready } ?: ReplyDelivery.Unknown }
                    catch (e: CancellationException) { throw e }
                    catch (_: Exception) { ReplyDelivery.Unknown }
                } else ReplyDelivery.Ready
                mutableState.value = state.value.copy(sending = false, delivery = delivery,
                    error = when (delivery) {
                        ReplyDelivery.Queued, ReplyDelivery.WaitingForNetwork -> null
                        ReplyDelivery.Refused -> ReplyError.Refused
                        ReplyDelivery.Ready -> ReplyError.NotSaved
                        else -> ReplyError.Unconfirmed
                    })
            }
        }
    }

    private suspend fun targetChanged(previous: ReplyDraft) {
        val draft = ReplyDraft(previous.text, ai = previous.ai, mentions = previous.mentions)
        mutableState.value = state.value.copy(draft = draft, sending = false, targetChanged = true,
            delivery = ReplyDelivery.Ready, error = ReplyError.TargetChanged)
        try { writes.withLock { repository.saveDraft(target, draft) } }
        catch (e: CancellationException) { throw e }
        catch (_: Exception) { mutableState.value = state.value.copy(error = ReplyError.TargetChangedNotSaved) }
    }

    private fun verify() {
        val draft = state.value.draft
        mutableState.value = state.value.copy(sending = true, delivery = ReplyDelivery.Verifying, error = null)
        scope.launch {
            try {
                val result = repository.verifyReply(target, draft.clientId)
                mutableState.value = if (result == ReplyDelivery.Sent) {
                    ReplyComposerState(draft = ReplyDraft(""), loading = false, sent = true)
                } else if (result == ReplyDelivery.Ready) {
                    val fresh = ReplyDraft(draft.text, ai = draft.ai, mentions = draft.mentions)
                    writes.withLock { repository.saveDraft(target, fresh) }
                    state.value.copy(draft = fresh, sending = false, delivery = ReplyDelivery.Ready, error = ReplyError.NotSent)
                } else state.value.copy(sending = false, delivery = result,
                    error = when (result) {
                        ReplyDelivery.Refused -> ReplyError.Refused
                        ReplyDelivery.Queued, ReplyDelivery.WaitingForNetwork -> null
                        else -> ReplyError.CantConfirm
                    })
            } catch (e: CancellationException) {
                mutableState.value = state.value.copy(sending = false, delivery = ReplyDelivery.Unknown)
                throw e
            } catch (_: MailReplyTargetChanged) {
                targetChanged(draft)
            } catch (_: Exception) {
                mutableState.value = state.value.copy(sending = false, delivery = ReplyDelivery.Unknown, error = ReplyError.CheckFailed)
            }
        }
    }
}

/** Shared reply surface; delivery and draft ownership stay in the composer. */
@Composable
internal fun ReplyComposerField(
    state: ReplyComposerState,
    onUpdate: (String) -> Unit,
    onSend: () -> Unit,
    placeholder: String,
    modifier: Modifier = Modifier,
    sendEnabled: Boolean = true,
    probePage: String? = null,
    accessory: (@Composable ColumnScope.() -> Unit)? = null,
) {
    val focus = remember { FocusRequester() }
    var focused by remember { mutableStateOf(false) }
    InboxReplyDraftProbe(probePage, state.draft.text, focused)
    val keyboard = LocalSoftwareKeyboardController.current
    ComposerFrame(modifier,
        // Blank space around the field types too, as in the chat composer (#894, #4093).
        onBlankTap = { if (state.canEdit) { focus.requestFocus(); keyboard?.show() } },
        accessory = {
            accessory?.invoke(this)
            ReplyStatus(state)
        },
        trailing = {
            if (state.delivery in setOf(ReplyDelivery.Unknown, ReplyDelivery.NeedsConfirmation)) {
                JButton(stringResource(R.string.inbox_reply_check), onSend,
                    size = JButtonSize.Small,
                    enabled = sendEnabled && !state.loading && !state.sending && state.draft.text.isNotBlank())
            } else SendButton(onSend,
                loading = state.sending,
                enabled = sendEnabled && !state.loading && !state.sending && state.draft.text.isNotBlank() &&
                    state.delivery !in setOf(ReplyDelivery.Queued, ReplyDelivery.WaitingForNetwork, ReplyDelivery.Verifying))
        },
        field = {
            ComposerTextField(state.draft.text, onUpdate, placeholder,
                Modifier.focusRequester(focus).testTag("inbox-reply-field").onFocusChanged { focused = it.isFocused },
                enabled = state.canEdit)
        })
}
