package persona.feature.settings

import persona.core.designsystem.text
import android.content.Context
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.icons.PersonaIcons
import java.time.Instant

/**
 * Settings › Connectors › Mail (iOS `MailAccountsView`): list all accounts on the hub,
 * test connection, delete account, and link to add account.
 */
@Composable
internal fun MailAccountsPage(
    connectors: ConnectorsManagement?,
    onBack: () -> Unit,
    onAdd: () -> Unit,
) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    var accounts by remember { mutableStateOf<List<MailAccount>?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var deleting by remember { mutableStateOf<MailAccount?>(null) }
    var testingId by remember { mutableStateOf<String?>(null) }
    var actionAccount by remember { mutableStateOf<MailAccount?>(null) }

    fun load() {
        if (connectors == null) {
            loading = false
            return
        }
        scope.launch {
            loading = true
            try {
                accounts = connectors.mailAccounts()
                error = null
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: res.getString(R.string.settings_src_mail_read_failed)
            } finally {
                loading = false
            }
        }
    }

    LaunchedEffect(connectors) { load() }
    BackHandler(onBack = onBack)

    JListPage(title = stringResource(R.string.settings_src_mail_title), onBack = onBack) {
        val list = accounts
        when {
            loading && list == null -> item("loading") { JLoading() }
            list.isNullOrEmpty() -> {
                item("empty") {
                    JEmptyState(
                        title = stringResource(R.string.settings_src_mail_empty_title),
                        description = error ?: stringResource(R.string.settings_src_mail_empty_body),
                        icon = PhIcons.EnvelopeSimple,
                    )
                }
            }
            else -> {
                item("accounts-header") { JSectionHeader(stringResource(R.string.settings_src_mail_accounts_header)) }
                itemsIndexed(list, key = { _, a -> a.id }) { index, a ->
                    JGroupItem(GroupPosition.of(index, list.size)) {
                        JRow(
                            title = a.email,
                            subtitle = a.detail.text(),
                            subtitleLines = 2,
                            leadingIcon = PhIcons.EnvelopeSimple,
                            onClick = { actionAccount = a },
                            trailing = {
                                if (testingId == a.id) {
                                    JSpinner(stroke = Tokens.Space.Standard.xxs)
                                } else {
                                    JIconButton(PhIcons.DotsThreeVertical, stringResource(R.string.settings_src_more), onClick = { actionAccount = a })
                                }
                            },
                        )
                    }
                }
            }
        }
        item("add-header") { JSectionHeader(stringResource(R.string.settings_src_add_header)) }
        item("add-button") {
            JGroupItem(GroupPosition.Single) {
                JRow(stringResource(R.string.settings_src_mail_add), leadingIcon = PhIcons.Plus, onClick = onAdd)
            }
        }
        item("add-caption") {
            SettingsCaption(
                stringResource(R.string.settings_src_mail_secret_caption),
                Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.s),
            )
        }
        error?.let { err ->
            item("error") {
                JNotice(err, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou)
            }
        }
    }

    actionAccount?.let { a ->
        JActionSheet(
            actions = listOf(
                JAction(stringResource(R.string.settings_src_mail_test), onClick = {
                    val targetId = a.id
                    testingId = targetId
                    scope.launch {
                        try {
                            val r = connectors?.testMailAccount(targetId)
                            if (r?.ok == true) {
                                feedback?.notice(res.getString(R.string.settings_src_mail_test_ok))
                            } else {
                                feedback?.notice(res.getString(R.string.settings_src_mail_test_unreachable, r?.error ?: res.getString(R.string.settings_src_unknown_error)))
                            }
                            load()
                        } catch (e: CancellationException) {
                            throw e
                        } catch (e: Exception) {
                            feedback?.notice(e.message ?: res.getString(R.string.settings_src_mail_test_failed))
                        } finally {
                            testingId = null
                        }
                    }
                }, icon = PhIcons.PlugsConnected),
                JAction(stringResource(R.string.settings_src_delete), onClick = { deleting = a }, icon = PhIcons.Trash, destructive = true),
            ),
            onDismiss = { actionAccount = null },
            title = a.email,
        )
    }

    deleting?.let { a ->
        JDialog(
            title = stringResource(R.string.settings_src_mail_delete_title, a.email),
            content = {
                Text(
                    stringResource(R.string.settings_src_mail_delete_body),
                    style = PersonaTheme.type.callout,
                    color = PersonaTheme.palette.ink2,
                )
            },
            confirm = stringResource(R.string.settings_src_delete) to {
                val targetId = a.id
                deleting = null
                scope.launch {
                    try {
                        connectors?.deleteMailAccount(targetId)
                        load()
                        feedback?.notice(res.getString(R.string.settings_src_deleted))
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        feedback?.notice(res.getString(R.string.settings_src_mail_delete_failed, e.message.orEmpty()))
                    }
                }
            },
            dismiss = stringResource(R.string.settings_src_cancel) to { deleting = null },
            onDismiss = { deleting = null },
        )
    }
}

/**
 * Settings › Connectors › Add mail account (iOS `AddMailAccountView`, docs/extensions/mail.md).
 * Supports Gmail (Google OAuth or app password), Feishu (fixed servers), and other IMAP.
 */
@Composable
internal fun AddMailAccountPage(
    connectors: ConnectorsManagement?,
    onBack: () -> Unit,
    onAdded: () -> Unit,
    start: MailAccountForm = MailAccountForm(),
) {
    val context = LocalContext.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var form by rememberSaveable(stateSaver = MailAccountForm.Saver) { mutableStateOf(start) }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var gmailFallback by rememberSaveable { mutableStateOf<String?>(null) }

    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) {
        if (GoogleSignIn.isWaiting) {
            scope.launch {
                delay(1500)
                if (GoogleSignIn.isWaiting) GoogleSignIn.cancel()
            }
        }
    }

    LaunchedEffect(Unit) {
        GoogleSignIn.finished.collect {
            when (val f = GoogleSignIn.takeFinished()) {
                is GoogleSignIn.Finished.Added -> {
                    feedback?.notice(res.getString(R.string.settings_src_mail_added, f.account.email))
                    onAdded()
                }
                is GoogleSignIn.Finished.Error -> {
                    error = f.error.message ?: res.getString(R.string.settings_src_mail_google_failed)
                }
                null -> Unit
            }
        }
    }

    // A hub without Google sign-in goes straight to the app-password form; an older hub or a failed read keeps the button.
    LaunchedEffect(connectors) {
        if (connectors == null || gmailFallback != null) return@LaunchedEffect
        val config = try { connectors.googleConfig() } catch (e: CancellationException) { throw e } catch (_: Exception) { null }
        if (config?.configured == false) gmailFallback = res.getString(R.string.settings_src_mail_google_not_configured)
    }

    fun save() {
        if (connectors == null) return
        saving = true
        error = null
        scope.launch {
            try {
                val req = form.request()
                val added = connectors.addMailAccount(req)
                feedback?.notice(res.getString(R.string.settings_src_mail_added, added.email))
                onAdded()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: res.getString(R.string.settings_src_mail_connect_failed)
            } finally {
                saving = false
            }
        }
    }

    fun signInWithGoogle() {
        if (connectors == null) return
        saving = true
        error = null
        scope.launch {
            try {
                GoogleSignIn.signIn(context, connectors, form.email.takeIf { it.contains('@') })
            } catch (e: CancellationException) {
                throw e
            } catch (_: UnsupportedOperationException) {
                gmailFallback = res.getString(R.string.settings_src_mail_google_unsupported)
            } catch (e: Exception) {
                val msg = e.message.orEmpty()
                if (msg.contains("not configured", ignoreCase = true)) {
                    gmailFallback = res.getString(R.string.settings_src_mail_google_not_configured)
                } else {
                    error = msg.ifEmpty { res.getString(R.string.settings_src_mail_google_start_failed) }
                }
            } finally {
                saving = false
            }
        }
    }

    val outlook = rememberOutlookSignIn(connectors, form.kind == MailAccountForm.Kind.OUTLOOK) { error = it }
    val gmailSignIn = form.kind == MailAccountForm.Kind.GMAIL && gmailFallback == null
    val signsIn = gmailSignIn || form.kind == MailAccountForm.Kind.OUTLOOK
    val canConnect = form.canConnect(gmailFallback != null) && !saving
    BackHandler(onBack = onBack)

    JListPage(
        title = stringResource(R.string.settings_src_mail_add),
        onBack = onBack,
        bottomBar = if (!signsIn) {
            {
                Row(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.s)) {
                    JButton(stringResource(R.string.settings_src_mail_connect), ::save, Modifier.fillMaxWidth(), enabled = canConnect, loading = saving)
                }
            }
        } else null,
    ) {
        item("kind") {
            JSegmented(
                options = MailAccountForm.Kind.entries.map { JSegment(stringResource(it.title)) },
                selected = form.kind.ordinal,
                onSelect = {
                    form = form.copy(kind = MailAccountForm.Kind.entries[it])
                    error = null
                },
                modifier = Modifier.fillMaxWidth().padding(top = Tokens.Space.s),
            )
        }

        if (form.kind == MailAccountForm.Kind.OUTLOOK) {
            outlookSignIn(outlook, connectors) { error = it }
        } else if (gmailSignIn) {
            item("google-btn") {
                Column(Modifier.fillMaxWidth().padding(top = Tokens.Space.l)) {
                    JButton(
                        stringResource(if (saving) R.string.settings_src_mail_google_connecting else R.string.settings_src_mail_google_sign_in),
                        onClick = ::signInWithGoogle,
                        enabled = !saving,
                        loading = saving,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    SettingsCaption(
                        stringResource(R.string.settings_src_mail_google_caption),
                        Modifier.padding(top = Tokens.Space.s),
                    )
                }
            }
        } else {
            gmailFallback?.takeIf { form.kind == MailAccountForm.Kind.GMAIL }?.let { why ->
                item("fallback-notice") {
                    JNotice(why, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou)
                }
            }
            item("form-fields") {
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s), modifier = Modifier.padding(top = Tokens.Space.m)) {
                    JTextField(
                        value = form.email,
                        onValueChange = { form = form.copy(email = it.trim()) },
                        label = stringResource(R.string.settings_src_mail_address),
                        placeholder = "name@example.com",
                        singleLine = true,
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email, autoCorrectEnabled = false),
                        modifier = Modifier.fillMaxWidth(),
                    )
                    JTextField(
                        value = form.password,
                        onValueChange = { form = form.copy(password = it) },
                        label = stringResource(if (form.kind == MailAccountForm.Kind.GMAIL) R.string.settings_src_mail_app_password else R.string.settings_src_mail_password),
                        placeholder = stringResource(if (form.kind == MailAccountForm.Kind.GMAIL) R.string.settings_src_mail_app_password_hint else R.string.settings_src_required),
                        singleLine = true,
                        visualTransformation = PasswordVisualTransformation(),
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false),
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
            }
            val guide = form.codeGuide
            if (guide != null) item("form-code-guide") {
                // QQ / NetEase: where the authorization code is made, and that web mail (#5123).
                val uri = LocalUriHandler.current
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs), modifier = Modifier.padding(top = Tokens.Space.xs)) {
                    SettingsCaption(stringResource(guide.provider.steps))
                    JTextButton(stringResource(guide.provider.open), onClick = { runCatching { uri.openUri(guide.webMail) } }, icon = PhIcons.ArrowSquareOut)
                }
            } else item("form-footer") {
                SettingsCaption(
                    stringResource(when (form.kind) {
                        MailAccountForm.Kind.FEISHU -> R.string.settings_src_mail_feishu_footer
                        MailAccountForm.Kind.IMAP -> R.string.settings_src_mail_imap_footer
                        MailAccountForm.Kind.GMAIL, MailAccountForm.Kind.OUTLOOK -> R.string.settings_src_mail_gmail_footer
                    }),
                    Modifier.padding(top = Tokens.Space.xs),
                )
            }
            if (form.kind == MailAccountForm.Kind.IMAP) {
                item("servers-title") { JSectionHeader(stringResource(R.string.settings_src_mail_servers_header)) }
                item("servers-fields") {
                    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JTextField(
                            value = form.imapHost,
                            onValueChange = { form = form.copy(imapHost = it.trim()) },
                            label = stringResource(R.string.settings_src_mail_imap_server),
                            placeholder = "imap.example.com",
                            singleLine = true,
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, autoCorrectEnabled = false),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        JTextField(
                            value = form.imapPort,
                            onValueChange = { form = form.copy(imapPort = it.filter(Char::isDigit)) },
                            label = stringResource(R.string.settings_src_mail_imap_port),
                            placeholder = "993",
                            singleLine = true,
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        JTextField(
                            value = form.smtpHost,
                            onValueChange = { form = form.copy(smtpHost = it.trim()) },
                            label = stringResource(R.string.settings_src_mail_smtp_server),
                            placeholder = "smtp.example.com",
                            singleLine = true,
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, autoCorrectEnabled = false),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        JTextField(
                            value = form.smtpPort,
                            onValueChange = { form = form.copy(smtpPort = it.filter(Char::isDigit)) },
                            label = stringResource(R.string.settings_src_mail_smtp_port),
                            placeholder = "465",
                            singleLine = true,
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                }
            }
        }
        error?.let { err ->
            item("error") {
                JNotice(err, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou)
            }
        }
        item("ai-tip") {
            Row(
                Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.l),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            ) {
                JIcon(PhIcons.Sparkle, null, tint = PersonaTheme.palette.accent)
                Text(
                    stringResource(R.string.settings_src_mail_ai_tip),
                    style = PersonaTheme.type.caption,
                    color = PersonaTheme.palette.ink3,
                )
            }
        }
    }
}

/**
 * Settings › Connectors › Lark (iOS `FeishuConnectorView`): connection state of the Lark gateway on the hub,
 * signed-in accounts, and features powered by Feishu.
 */
@Composable
internal fun FeishuConnectorPage(
    connectors: ConnectorsManagement?,
    onBack: () -> Unit,
    onAgentModel: () -> Unit,
) {
    val scope = rememberCoroutineScope()
    val res = LocalResources.current
    var status by remember { mutableStateOf<LarkStatus?>(null) }
    var accounts by remember { mutableStateOf<LarkAccounts?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var displayPage by remember { mutableStateOf(false) }
    val homePreference = LocalAppStyleStore.current
    val homeMessages = homePreference?.larkHomeMessages?.collectAsState()?.value ?: false
    if (displayPage) { LarkMessageDisplayPage(connectors, onBack = { displayPage = false }); return }

    fun load() {
        if (connectors == null) {
            loading = false
            return
        }
        scope.launch {
            loading = true
            try {
                status = connectors.larkStatus()
                accounts = connectors.larkAccounts()
                error = null
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: res.getString(R.string.settings_src_lark_read_failed)
            } finally {
                loading = false
            }
        }
    }

    LaunchedEffect(connectors) { load() }
    BackHandler(onBack = onBack)

    val st = status
    val stateLabel = stringResource(when {
        st == null -> if (loading) R.string.settings_src_checking else R.string.settings_src_status_unreadable
        !st.configured -> R.string.settings_src_state_not_configured
        st.connected -> R.string.settings_src_state_connected
        else -> R.string.settings_src_lark_unreachable
    })
    val statusDot = when {
        st?.connected == true -> Status.Idle
        st?.configured == true -> Status.NeedsYou
        else -> Status.Offline
    }

    JListPage(title = stringResource(R.string.settings_src_lark_title), onBack = onBack) {
        item("status-header") { JSectionHeader(stringResource(R.string.settings_src_lark_status_header)) }
        item("status-row") {
            JGroupItem(GroupPosition.Single) {
                JRow(
                    title = st?.name?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_src_lark_title),
                    subtitle = stateLabel,
                    leadingIcon = PhIcons.ChatCircleDots,
                    trailing = {
                        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                            JStatusDot(statusDot)
                            if (loading) JSpinner(stroke = Tokens.Space.Standard.xxs)
                        }
                    },
                )
            }
        }
        st?.lastEventAt?.takeIf { it > 0 }?.let { at ->
            item("last-event") {
                JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
                    JRow(stringResource(R.string.settings_src_lark_last_event), value = agoText(res, Instant.ofEpochSecond(at), days = true))
                }
            }
        }
        item("status-caption") {
            SettingsCaption(
                stringResource(if (st?.configured == false) R.string.settings_src_lark_not_configured_caption else R.string.settings_src_lark_caption),
                Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.s),
            )
        }

        val accs = accounts?.accounts.orEmpty()
        if (accs.size > 1) {
            item("accounts-header") { JSectionHeader(stringResource(R.string.settings_src_lark_accounts_header)) }
            itemsIndexed(accs, key = { _, a -> a.id }) { index, a ->
                val isDefault = a.id == accounts?.default
                val sub = when {
                    a.quarantined == true -> stringResource(R.string.settings_src_lark_quarantined)
                    !a.signedIn -> stringResource(R.string.settings_src_state_signed_out)
                    !a.tenant.isNullOrBlank() -> a.tenant
                    else -> stringResource(R.string.settings_src_signed_in)
                }
                JGroupItem(GroupPosition.of(index, accs.size)) {
                    JRow(
                        title = a.title,
                        subtitle = sub,
                        leading = { JAvatar(a.title) },
                        trailing = {
                            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                                if (isDefault) JTag(stringResource(R.string.settings_src_default))
                                if (a.signedIn && a.unread > 0) JBadge(a.unread)
                            }
                        },
                    )
                }
            }
            item("accounts-caption") {
                SettingsCaption(
                    stringResource(
                        if (homeMessages) R.string.settings_src_lark_accounts_caption_default
                        else R.string.settings_src_lark_accounts_caption
                    ),
                    Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.s),
                )
            }
        }

        // Demo has no conversation repository, so it must not offer a destination that can only say "unavailable".
        if (homePreference != null && connectors != null) {
            item("home-messages") {
                JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.l)) {
                    JSwitchRow(
                        title = stringResource(R.string.settings_src_lark_home_messages),
                        checked = homeMessages,
                        onCheckedChange = homePreference::setLarkHomeMessages,
                        modifier = Modifier.testTag("settings.lark.homeMessages"),
                        subtitle = stringResource(R.string.settings_src_lark_home_messages_detail),
                        leadingIcon = PersonaIcons.messagesApp,
                        subtitleLines = Int.MAX_VALUE,
                        titleLines = 2,
                    )
                }
            }
        }
        item("display") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.l)) {
                JRow(stringResource(R.string.settings_src_display_title), leadingIcon = PhIcons.Eye, onClick = { displayPage = true })
            }
        }
        item("uses-header") { JSectionHeader(stringResource(R.string.settings_src_lark_uses_header)) }
        item("use-inbox") {
            JGroupItem(GroupPosition.of(0, 5)) {
                JRow(stringResource(R.string.settings_src_lark_use_inbox), subtitle = stringResource(R.string.settings_src_lark_use_inbox_detail), leadingIcon = PhIcons.EnvelopeSimple)
            }
        }
        item("use-kb") {
            JGroupItem(GroupPosition.of(1, 5)) {
                JRow(stringResource(R.string.settings_src_lark_use_kb), subtitle = stringResource(R.string.settings_src_lark_use_kb_detail), leadingIcon = PhIcons.BookOpen)
            }
        }
        item("use-assistant") {
            JGroupItem(GroupPosition.of(2, 5)) {
                JRow(stringResource(R.string.settings_src_lark_use_assistant), subtitle = stringResource(R.string.settings_src_lark_use_assistant_detail), leadingIcon = PhIcons.Sparkle)
            }
        }
        item("use-agent") {
            JGroupItem(GroupPosition.of(3, 5)) {
                JRow("/agent", subtitle = stringResource(R.string.settings_src_lark_use_agent_detail), leadingIcon = PhIcons.Robot)
            }
        }
        item("use-model") {
            JGroupItem(GroupPosition.of(4, 5)) {
                JRow(stringResource(R.string.settings_src_lark_agent_model), subtitle = stringResource(R.string.settings_src_lark_agent_model_detail), leadingIcon = PhIcons.Brain, onClick = onAgentModel)
            }
        }
        error?.let { err ->
            item("error") {
                JNotice(err, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou)
            }
        }
    }
}

/**
 * One sign-in step: its hint and the hub's word on it, then a QR code, a code to enter elsewhere, or fields to fill in.
 * [deviceLink] (an extension's sign-in, #4507) also shows a device code's page as a link to tap and its QR code (the hub
 * draws it) to scan with another phone. [name] is what is signed in to. "Start again" runs in [pageScope] (the page's, not this
 * list item's, which goes away when the item scrolls out and would leave it spinning).
 */
@Composable
internal fun ConnectorSignInStep(
    name: String, prompt: BindPrompt, bind: BindState, model: ConnectorSignInModel, deviceLink: Boolean = false,
    pageScope: CoroutineScope? = null,
) {
    val localScope = rememberCoroutineScope()
    val scope = pageScope ?: localScope
    val copy = rememberCopy()
    val res = LocalResources.current
    val uri = LocalUriHandler.current
    Column(Modifier.fillMaxWidth().padding(top = Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        prompt.hint?.takeIf { it.isNotBlank() }?.let {
            Text(it, Modifier.padding(horizontal = rowPadding), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        }
        prompt.error?.takeIf { it.isNotBlank() }?.let { JInlineStatus(it, tone = InlineTone.Failed) }
        bind.error?.let { JInlineStatus(it.text(), tone = InlineTone.Failed) }
        val rows = prompt.qr?.rows.orEmpty()
        val view = StepView.of(prompt)
        when {
            view == StepView.QrCode -> {
                JQrCode(rows, Modifier.align(Alignment.CenterHorizontally), label = stringResource(R.string.settings_src_connector_qr_label, name))
                SignInWaiting(stringResource(R.string.settings_src_connector_waiting_scan))
                JButton(stringResource(R.string.settings_src_cancel), model::cancel, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary)
            }
            view == StepView.Code && prompt.kind == "device_code" && deviceLink -> {
                val code = prompt.userCode.orEmpty()
                val link = prompt.url?.takeIf { StepView.isWebLink(it) }
                link?.let { url ->
                    JGroupItem(GroupPosition.Single) {
                        JRow(stringResource(R.string.settings_ext_auth_link_label), subtitle = url, subtitleLines = 2, leadingIcon = PhIcons.LinkSimple,
                            trailing = { JIcon(PhIcons.ArrowSquareOut, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3) },
                            onClick = { runCatching { uri.openUri(url) } })
                    }
                }
                // No code: the page alone is enough (the protocol leaves it out then).
                if (code.isNotEmpty()) Row(Modifier.padding(start = rowPadding), verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    JCodeDisplay(code, Modifier.weight(1f))
                    JIconButton(PhIcons.Copy, stringResource(R.string.settings_src_connector_copy_code), { copy(code, null) })
                }
                if (rows.isNotEmpty()) {
                    JQrCode(rows, Modifier.align(Alignment.CenterHorizontally), label = stringResource(R.string.settings_src_connector_qr_label, name))
                    Text(stringResource(R.string.settings_ext_auth_scan_other), Modifier.align(Alignment.CenterHorizontally),
                        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                SignInWaiting(stringResource(R.string.settings_src_channel_waiting_authorize))
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    link?.let { url ->
                        JButton(stringResource(R.string.settings_ext_auth_open_link), { runCatching { uri.openUri(url) } }, Modifier.weight(1f),
                            icon = PhIcons.ArrowSquareOut)
                    }
                    JButton(stringResource(R.string.settings_src_cancel), model::cancel, Modifier.weight(1f), kind = JButtonKind.Secondary)
                }
            }
            view == StepView.Code -> {
                val code = prompt.userCode.orEmpty()
                // No code: the page to open is the step (StepView.of); no empty box with a grey "Copy".
                if (code.isNotEmpty()) Row(Modifier.padding(start = rowPadding), verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    JCodeDisplay(code, Modifier.weight(1f))
                    JIconButton(PhIcons.Copy, stringResource(R.string.settings_src_connector_copy_code), { copy(code, null) })
                }
                // A pair code is sent to the bot from the chat app, not authorized on a page (as on Settings › Channels).
                SignInWaiting(if (prompt.kind == "pair_code") stringResource(R.string.settings_src_channel_waiting_send, name)
                    else stringResource(R.string.settings_src_channel_waiting_authorize))
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    prompt.url?.takeIf { StepView.isWebLink(it) }?.let { url ->
                        JButton(stringResource(R.string.settings_src_open_web), { runCatching { uri.openUri(url) } }, Modifier.weight(1f),
                            kind = JButtonKind.Secondary, icon = PhIcons.ArrowSquareOut)
                    }
                    JButton(stringResource(R.string.settings_src_cancel), model::cancel, Modifier.weight(1f), kind = JButtonKind.Secondary)
                }
            }
            view == StepView.QrLink -> {
                QrLinkStep(prompt.url.orEmpty(), name)
                SignInWaiting(stringResource(R.string.settings_src_connector_waiting_confirm))
                JButton(stringResource(R.string.settings_src_cancel), model::cancel, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary)
            }
            view == StepView.Unknown || view == StepView.Incomplete ->
                CannotDrawStep(prompt, bind.sending, { (pageScope ?: scope).launch { model.start() } }, model::cancel)
            else -> {
                prompt.fields.forEach { field ->
                    JTextField(bind.values[field.name].orEmpty(), { model.setValue(field.name, it) }, Modifier.fillMaxWidth(),
                        label = field.label.ifBlank { field.name }, placeholder = field.placeholder?.takeIf { it.isNotBlank() } ?: field.label,
                        singleLine = true, enabled = !bind.sending,
                        visualTransformation = if (field.secret) PasswordVisualTransformation() else VisualTransformation.None,
                        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false,
                            keyboardType = if (field.secret) KeyboardType.Password else KeyboardType.Ascii))
                }
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JButton(stringResource(R.string.settings_src_next), { scope.launch { model.submit() } }, Modifier.weight(1f), loading = bind.sending,
                        loadingText = stringResource(R.string.settings_src_verifying), enabled = bind.canSubmit)
                    JButton(stringResource(R.string.settings_src_cancel), model::cancel, Modifier.weight(1f), kind = JButtonKind.Secondary, enabled = !bind.sending)
                }
            }
        }
    }
}

/** A spinner and what the step waits for, on one line. */
@Composable
private fun SignInWaiting(text: String) {
    Row(Modifier.padding(horizontal = rowPadding), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JSpinner()
        Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}
