package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import persona.core.contracts.AgentState
import persona.core.contracts.Session
import persona.core.designsystem.InlineTone
import persona.core.designsystem.JInlineStatus
import persona.core.designsystem.JTextButton
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons

/** What the session's host says over the composer (iOS `composerStack`): why it waits for you, or what stopped it. */
internal sealed interface HostNote {
    data class Attention(val reason: String) : HostNote
    data class Failure(val reason: String, val text: String) : HostNote
}

/**
 * The note for [session]: its attention reason while it needs you, else its failure notice while it is not working
 * and the retry was not tapped for this very notice ([retried]). None while it is still starting (that has its own).
 */
internal fun hostNote(session: Session?, retried: String?): HostNote? {
    if (session == null || session.starting || !session.startError.isNullOrBlank()) return null
    val attention = session.attentionReason?.trim().orEmpty()
    if (attention.isNotEmpty() && session.agentState == AgentState.NeedsYou) return HostNote.Attention(attention)
    val reason = session.failureReason?.takeIf(String::isNotBlank) ?: return null
    val text = session.failureText ?: return null
    if (session.status == "working" || session.agentState == AgentState.Working || retried == reason) return null
    return HostNote.Failure(reason, text)
}

/**
 * Over the composer: why the host says it waits for you (caution), or that it stopped on an error it will not retry by
 * itself (iOS FailureNote) with "Retry", which sends "Continue" alone (not what is in the box). [onRetry] says whether that
 * message was taken (false while another is still on its way: nothing went, the note stays); the caller then hides the
 * note for this notice ([retried], kept by the chat so a send that fails can bring it back). A session its host shows
 * elsewhere (a botmux topic) offers "Open in Lark" on both, as iOS FeishuLink.
 */
@Composable
internal fun ChatHostNote(
    session: Session?, retried: String?, canRetry: Boolean, retryEnabled: Boolean, onRetry: (reason: String) -> Unit,
    modifier: Modifier = Modifier,
) {
    val note = hostNote(session, retried) ?: return
    val uriHandler = LocalUriHandler.current
    val link = session?.externalLink
    val inset = modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs)
    val openLink: (@Composable () -> Unit)? = link?.let { url -> {
        JTextButton(stringResource(R.string.chat_host_open_feishu), { val _ = runCatching { uriHandler.openUri(url) } }, icon = PhIcons.ArrowSquareOut)
    } }
    when (note) {
        is HostNote.Attention -> JInlineStatus(note.reason, inset, tone = InlineTone.Caution, action = openLink)
        is HostNote.Failure -> JInlineStatus(note.text, inset, tone = InlineTone.Failed, action = if (canRetry || openLink != null) ({
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
                openLink?.invoke()
                if (canRetry) JTextButton(stringResource(R.string.chat_failure_retry), { onRetry(note.reason) },
                    enabled = retryEnabled, icon = PhIcons.ArrowClockwise)
            }
        }) else null)
    }
}
