package persona.feature.chat

import androidx.compose.ui.platform.LocalResources
import android.content.res.Resources
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import persona.core.contracts.SHELL_WAITING
import persona.core.contracts.START_CONTINUE_FEATURE
import persona.core.contracts.Session
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.shellQuestion
import persona.core.designsystem.InlineTone
import persona.core.designsystem.JInlineStatus
import persona.core.designsystem.JNotice
import persona.core.designsystem.ShellQuestionCard
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.lines.isLiveStatus

internal data class StartingNote(val text: String, val failed: Boolean, val coded: Boolean = false)

/**
 * A session Persona is still starting (docs/protocol/protocol.md "New session"): what is sent now goes in once the agent is ready.
 * Or why it did not start; sends then fail with the same reason and can be sent again.
 */
internal fun startingNote(res: Resources, session: Session?): StartingNote? = when {
    session == null -> null
    !session.startError.isNullOrBlank() -> {
        val coded = !session.startErrorCode.isNullOrBlank()
        StartingNote(
            if (coded) session.startError.orEmpty().trim()
            else res.getString(R.string.chat_starting_failed, session.startError.orEmpty().trim()),
            failed = true,
            coded = coded,
        )
    }
    session.starting -> StartingNote(res.getString(R.string.chat_starting, session.agentLabel), failed = false)
    else -> null
}

/** Above the composer while the session starts, or when starting it failed. */
@Composable
internal fun ChatStartingNote(session: Session?, modifier: Modifier = Modifier) {
    val note = startingNote(LocalResources.current, session) ?: return
    if (note.coded) {
        JNotice(
            note.text,
            modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs),
            title = androidx.compose.ui.res.stringResource(R.string.chat_start_failed_title),
            status = Status.Unknown,
            icon = PersonaIcons.error,
        )
    } else {
        JInlineStatus(note.text, modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs),
            tone = if (note.failed) InlineTone.Failed else InlineTone.Busy)
    }
}

/** A confirmed answer clears this start locally; only its exact shell_waiting refusals are queued again, in order. */
internal suspend fun continueSessionStart(
    session: Session,
    choice: String,
    actions: ChatActions,
    sender: MessageSender,
    sessions: SessionsRepository,
) {
    val answered = actions.continueStart(session, choice)
    sessions.adopt(answered.copy(
        starting = true,
        status = "idle",
        startError = null,
        startErrorCode = null,
        startErrorArgs = null,
    ))
    sender.retryRefused(session.key, SHELL_WAITING)
    try {
        sessions.refresh()
    } catch (cancelled: kotlinx.coroutines.CancellationException) {
        throw cancelled
    } catch (_: Exception) {
        // The answer is already confirmed; the next session push reconciles it.
    }
}

/** The coded start failure and, where this hub takes it, the terminal question that stopped the start. */
@Composable
internal fun ChatStartFailure(
    session: Session?,
    actions: ChatActions?,
    sender: MessageSender,
    sessions: SessionsRepository,
    features: Set<String>,
    modifier: Modifier = Modifier,
) {
    if (session == null) return
    var answered by remember(session.key, session.rev, session.startError, session.startErrorCode) { mutableStateOf(false) }
    val shown = if (answered) session.copy(
        starting = true,
        status = "idle",
        startError = null,
        startErrorCode = null,
        startErrorArgs = null,
    ) else session
    Column(modifier) {
        ChatStartingNote(shown)
        val question = shellQuestion(session.startErrorCode, session.startErrorArgs)
        if (!answered && actions != null && START_CONTINUE_FEATURE in features && question != null) {
            ShellQuestionCard(
                question,
                answer = { choice ->
                    continueSessionStart(session, choice, actions, sender, sessions)
                    true
                },
                modifier = Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs),
                onAnswered = { answered = true },
            )
        }
    }
}

/** Who an Agent's chat talks to, for its words (iOS `agentLabel`): Claude for a session not known yet. */
internal fun chatAgentName(session: Session?): String = session?.agentLabel?.takeIf(String::isNotBlank) ?: "Claude"

/** An Agent chat's empty composer names its agent (iOS `Composer` placeholder): "Message Claude", "Message Codex". */
internal fun agentPlaceholder(res: Resources, session: Session?): String =
    if (session?.awaitingFirstMessage == true) res.getString(R.string.chat_codex_first_message)
    else res.getString(R.string.chat_placeholder_agent, chatAgentName(session))

/** A message on its way to the agent: sending, or sent and not in the transcript yet (iOS `ChatAwaitingTurn.onItsWay`). */
private val DeliveryState.onItsWay get() = this == DeliveryState.Sending || this == DeliveryState.Sent

/**
 * Going in: sending, sent, or waiting for the network. Refused and turned-down messages go nowhere, and neither does one
 * whose delivery the phone has to check (iOS's failed message, `mayHaveArrived`).
 */
private val DeliveryState.goingIn get() = onItsWay || this == DeliveryState.Queued || this == DeliveryState.WaitingForNetwork

/**
 * "Claude is getting ready…" under your newest message (iOS `startingTurn`): a message of yours is still going in, no
 * work line runs for it yet ([liveWork]), and the agent is at work or waiting on you, still starting up, or the message is
 * on its way while the hub may not have caught up (its status still idle or done, the start not failed). The turn's work
 * line takes over when it comes; null when the line doesn't show. [name] says who: the agent, or in Persona's
 * conversation Persona ("Persona is getting ready…", #4339 C18).
 */
internal fun gettingReady(res: Resources, session: Session?, messages: List<OutboxMessage>, liveWork: Boolean,
                          name: String = chatAgentName(session)): String? {
    if (liveWork || messages.none { it.state.goingIn }) return null
    val status = session?.status
    val catchingUp = mayBeCatchingUp(status, !session?.startError.isNullOrBlank()) && messages.any { it.state.onItsWay }
    if (!isLiveStatus(status) && session?.starting != true && !catchingUp) return null
    return res.getString(R.string.chat_getting_ready, name)
}
