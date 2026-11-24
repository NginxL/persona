package persona.feature.inbox

import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.platform.LocalContext
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.api.MailAccount
import persona.core.contracts.api.MailTransport
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.inbox.*

/**
 * Full-screen mail compose dialog (Compose / Reply / Reply all / Forward / mailto:).
 */
@Composable
fun ComposeMailDialog(
    transport: MailTransport,
    draft: MailDraft,
    onDone: () -> Unit,
) {
    var leaving by remember { mutableStateOf(false) }
    JFullScreen(onDismiss = { leaving = true }) {
        ComposeMailPage(
            transport = transport,
            draft = draft,
            onDone = onDone,
            leaveRequest = leaving,
            onLeaveHandled = { leaving = false },
        )
    }
}

@Composable
fun ComposeMailPage(
    transport: MailTransport,
    draft: MailDraft,
    onDone: () -> Unit,
    leaveRequest: Boolean = false,
    onLeaveHandled: () -> Unit = {},
) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current

    var accounts by remember { mutableStateOf<List<MailAccount>>(emptyList()) }
    var accountId by rememberSaveable { mutableStateOf(draft.accountId) }
    var to by rememberSaveable { mutableStateOf(draft.to) }
    var cc by rememberSaveable { mutableStateOf(draft.cc) }
    var bcc by rememberSaveable { mutableStateOf(draft.bcc) }
    var showBcc by rememberSaveable { mutableStateOf(draft.bcc.isNotBlank()) }
    var subject by rememberSaveable { mutableStateOf(draft.subject) }
    var body by rememberSaveable { mutableStateOf(draft.body) }
    var sending by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var confirmDiscard by remember { mutableStateOf(false) }

    LaunchedEffect(transport) {
        val _ = runCatching {
            val list = transport.accounts()
            accounts = list
            if (list.isNotEmpty() && list.none { it.id == accountId }) {
                accountId = list.first().id
            }
        }
    }

    val edited = to != draft.to || cc != draft.cc || bcc != draft.bcc || subject != draft.subject || body != draft.body
    val valid = accountId.isNotBlank() &&
        MailCompose.fieldValid(to, required = true) &&
        MailCompose.fieldValid(cc, required = false) &&
        MailCompose.fieldValid(bcc, required = false)

    fun leave() {
        // Closing mid-send would cancel the request with no result shown; wait for it to finish (#5706).
        if (sending) return
        if (edited) confirmDiscard = true else onDone()
    }

    LaunchedEffect(leaveRequest) {
        if (leaveRequest) {
            onLeaveHandled()
            leave()
        }
    }
    BackHandler { leave() }

    fun send() {
        if (!valid || sending) return
        sending = true
        error = null
        scope.launch {
            try {
                val req = MailCompose.request(
                    draft = draft.copy(
                        accountId = accountId,
                        to = to,
                        cc = cc,
                        bcc = bcc,
                        subject = subject,
                        body = body,
                    ),
                    bcc = bcc,
                )
                transport.sendMail(req)
                feedback?.notice(res.getString(R.string.inbox_sent))
                onDone()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: res.getString(R.string.inbox_mail_send_failed)
            } finally {
                sending = false
            }
        }
    }

    JListPage(
        title = stringResource(draft.title),
        onBack = ::leave,
        actions = {
            JButton(
                stringResource(R.string.inbox_send),
                onClick = ::send,
                kind = JButtonKind.Primary,
                size = JButtonSize.Small,
                enabled = valid && !sending,
                loading = sending,
            )
        },
    ) {
        item("fields") {
            JCard {
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    FromRow(accounts, accountId) { accountId = it }
                    JDivider()
                    FieldRow(stringResource(R.string.inbox_mail_to), to, { to = it }, valid = MailCompose.fieldValid(to, required = true), email = true)
                    JDivider()
                    FieldRow(
                        label = stringResource(R.string.inbox_mail_cc),
                        value = cc,
                        onValue = { cc = it },
                        valid = MailCompose.fieldValid(cc, required = false),
                        email = true,
                        trailing = if (!showBcc) ({
                            JTextButton(stringResource(R.string.inbox_mail_bcc), onClick = { showBcc = true })
                        }) else null,
                    )
                    if (showBcc) {
                        JDivider()
                        FieldRow(stringResource(R.string.inbox_mail_bcc), bcc, { bcc = it }, valid = MailCompose.fieldValid(bcc, required = false), email = true)
                    }
                    JDivider()
                    FieldRow(stringResource(R.string.inbox_mail_subject), subject, { subject = it }, valid = true, email = false)
                }
            }
        }
        error?.let { err ->
            item("error") {
                JNotice(err, status = Status.NeedsYou)
            }
        }
        item("body") {
            JCard {
                Box(
                    Modifier
                        .fillMaxWidth()
                        .heightIn(min = ContentTokens.mailBodyMinHeight)
                        .padding(Tokens.Space.m),
                ) {
                    if (body.isEmpty()) {
                        Text(stringResource(R.string.inbox_mail_body), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink3)
                    }
                    BasicTextField(
                        value = body,
                        onValueChange = { body = it },
                        textStyle = PersonaTheme.type.body.copy(color = PersonaTheme.palette.ink),
                        cursorBrush = SolidColor(PersonaTheme.palette.accent),
                        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Sentences),
                        modifier = Modifier.fillMaxWidth().heightIn(min = ContentTokens.mailBodyMinHeight),
                    )
                }
            }
        }
    }

    if (confirmDiscard) {
        JDialog(
            onDismiss = { confirmDiscard = false },
            title = stringResource(R.string.inbox_mail_discard_title),
            text = stringResource(R.string.inbox_mail_discard_text),
            confirm = stringResource(R.string.inbox_mail_discard) to {
                confirmDiscard = false
                onDone()
            },
            dismiss = stringResource(R.string.inbox_mail_keep_editing) to { confirmDiscard = false },
            destructive = true,
        )
    }
}

@Composable
private fun FieldLabel(text: String) {
    Text(
        text,
        modifier = Modifier.width(ContentTokens.mailLabelWidth),
        style = PersonaTheme.type.callout,
        color = PersonaTheme.palette.ink3,
        maxLines = 1,
    )
}

@Composable
private fun FromRow(accounts: List<MailAccount>, selected: String, onSelect: (String) -> Unit) {
    var menuOpen by remember { mutableStateOf(false) }
    val currentAccount = accounts.firstOrNull { it.id == selected }
    val displayText = currentAccount?.email ?: selected.ifBlank { stringResource(R.string.inbox_mail_no_account) }

    Box {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .heightIn(min = ContentTokens.fieldHeight)
                .clickable(enabled = accounts.size > 1) { menuOpen = true }
                .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
        ) {
            FieldLabel(stringResource(R.string.inbox_mail_from))
            Text(
                displayText,
                modifier = Modifier.weight(1f),
                style = PersonaTheme.type.body,
                color = if (accounts.isEmpty()) PersonaTheme.palette.ink3 else PersonaTheme.palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (accounts.size > 1) {
                JIcon(PhIcons.CaretDown, stringResource(R.string.inbox_mail_switch_account), size = Tokens.Space.l, tint = PersonaTheme.palette.ink3)
            }
        }
        if (accounts.size > 1) {
            JMenu(
                expanded = menuOpen,
                onDismiss = { menuOpen = false },
                actions = accounts.map { acc ->
                    JAction(
                        label = acc.email.ifBlank { acc.id },
                        checked = acc.id == selected,
                    ) {
                        onSelect(acc.id)
                    }
                },
            )
        }
    }
}

@Composable
private fun FieldRow(
    label: String,
    value: String,
    onValue: (String) -> Unit,
    valid: Boolean,
    email: Boolean,
    trailing: (@Composable () -> Unit)? = null,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = ContentTokens.fieldHeight)
            .padding(start = Tokens.Space.m, end = Tokens.Space.s, top = Tokens.Space.s, bottom = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
    ) {
        FieldLabel(label)
        BasicTextField(
            value = value,
            onValueChange = onValue,
            textStyle = PersonaTheme.type.body.copy(
                color = if (valid) PersonaTheme.palette.ink else PersonaTheme.palette.danger.text,
            ),
            cursorBrush = SolidColor(PersonaTheme.palette.accent),
            keyboardOptions = if (email) {
                KeyboardOptions(
                    keyboardType = KeyboardType.Email,
                    capitalization = KeyboardCapitalization.None,
                    autoCorrectEnabled = false,
                )
            } else {
                KeyboardOptions.Default
            },
            modifier = Modifier.weight(1f),
        )
        trailing?.invoke()
    }
}
