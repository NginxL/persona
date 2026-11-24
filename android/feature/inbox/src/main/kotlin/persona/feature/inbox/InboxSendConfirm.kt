package persona.feature.inbox

import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.platform.LocalContext
import android.content.res.Resources
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import persona.core.designsystem.*
import persona.core.inbox.*

/**
 * Sending in the user's name asks first, the way the old app did: where it goes and every word of it. Every AI draft
 * (the AI twin's, "AI draft") goes through this; a reply the user typed is sent as typed (2026-11-02, settled for the port).
 */
data class SendConfirmation(val destination: String, val text: String, val warning: String? = null)

/** "Send to Lark "title"" for Feishu (chats, documents, posts), "Reply to mail "title"" for mail. */
fun sendConfirmation(res: Resources, record: InboxRecord, text: String): SendConfirmation =
    SendConfirmation(res.getString(if (record.isLark) R.string.inbox_reply_to_lark else R.string.inbox_reply_to_mail, record.item.title),
        text.trim())

@Composable
fun sendConfirmation(record: InboxRecord, text: String): SendConfirmation =
    sendConfirmation(LocalResources.current, record, text)

/** A mail reply names the message and who gets it; [targetChanged] warns that a newer message came in. */
fun mailSendConfirmation(res: Resources, subject: String, recipient: String, text: String, targetChanged: Boolean): SendConfirmation {
    val shown = subject.ifBlank { res.getString(R.string.inbox_mail_no_subject) }
    return SendConfirmation(
        if (recipient.isBlank()) res.getString(R.string.inbox_reply_to_mail, shown)
        else res.getString(R.string.inbox_reply_to_mail_recipient, shown, recipient),
        text.trim(),
        warning = if (targetChanged) res.getString(R.string.inbox_reply_newer_warning) else null,
    )
}

@Composable
internal fun SendAsYouDialog(confirmation: SendConfirmation, onSend: () -> Unit, onDismiss: () -> Unit, enabled: Boolean = true) {
    val palette = PersonaTheme.palette
    JDialog(onDismiss = onDismiss, title = stringResource(R.string.inbox_reply_confirm_title),
        confirm = stringResource(R.string.inbox_send) to onSend, confirmEnabled = enabled) {
        // The whole draft is shown, so a long one scrolls inside the dialog.
        Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            confirmation.warning?.let { Text(it, style = PersonaTheme.type.callout, color = palette.cautionText) }
            Text(stringResource(R.string.inbox_reply_destination, confirmation.destination), style = PersonaTheme.type.callout, color = palette.ink2)
            Text(confirmation.text, style = PersonaTheme.type.body, color = palette.ink)
        }
    }
}
