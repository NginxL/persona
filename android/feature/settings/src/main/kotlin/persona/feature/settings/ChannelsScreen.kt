package persona.feature.settings

import android.content.ContentValues
import android.content.Context
import android.content.res.Resources
import android.graphics.Bitmap
import android.provider.MediaStore
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.navigation3.runtime.NavKey
import persona.core.contracts.api.BindPrompt
import persona.core.contracts.api.ChannelDetail
import persona.core.contracts.api.ChannelManagement
import persona.core.contracts.api.HubChannel
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.ChannelRoute
import persona.core.navigation.LocalDetailKey
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Settings › Channels (docs/extensions/channels.md "UI"): every way of talking to Persona in three sections - Persona's own
 * apps, the ones installed (connected or not), and the ones an extension market offers (opening the market's page).
 */
@Composable
internal fun ChannelsPage(
    channels: ChannelManagement?, onBack: () -> Unit, onChannel: (String) -> Unit,
    onMarketExtension: ((market: String, extension: String) -> Unit)? = null,
) {
    if (channels == null) {
        JListPage(title = stringResource(R.string.settings_src_channels_title), onBack = onBack) { item { JNotice(stringResource(R.string.settings_src_channels_demo)) } }
        return
    }
    val model = remember(channels) { ChannelListModel(channels) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    LaunchedEffect(model) { model.load() }
    val list = state.channels
    val detail = LocalDetailKey.current
    JListPage(title = stringResource(R.string.settings_src_channels_title), onBack = onBack, refreshing = state.loading && list != null, onRefresh = { scope.launch { model.load() } }) {
        state.error?.let { error ->
            item("error") {
                JNotice(error.text(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou,
                    actionLabel = stringResource(R.string.settings_src_retry), onAction = { scope.launch { model.load() } })
            }
        }
        if (list == null && state.loading) item("loading") { JLoading() }
        if (list != null && list.isEmpty()) item("empty") {
            JEmptyState(stringResource(R.string.settings_src_channels_empty_title), stringResource(R.string.settings_src_channels_empty_body), icon = PhIcons.ChatsCircle)
        }
        if (list != null && list.isNotEmpty()) item("intro") {
            ChannelNote(stringResource(R.string.settings_src_channels_intro))
        }
        state.sections.forEach { section ->
            item("section-${section.id}") { JSectionHeader(stringResource(section.title), count = section.channels.size.takeIf { section.channels.size > 1 }) }
            itemsIndexed(section.channels, key = { _, it -> "channel-${it.id}" }) { index, channel ->
                val market = channel.source == "market"
                val open: (() -> Unit)? = if (!market) ({ onChannel(channel.id) })
                    else onMarketExtension?.let { go -> channel.market?.takeIf { it.isNotBlank() }?.let { m ->
                        channel.extension?.takeIf { it.isNotBlank() }?.let { p -> { go(m, p) } } } }
                JGroupItem(GroupPosition.of(index, section.channels.size)) {
                    JRow(channel.label, subtitle = ChannelText.subtitle(channel), leading = { JChannelMark(channel) },
                        trailing = { ChannelStateMark(channel) }, chevron = open != null, enabled = !market || open != null,
                        selected = detail == ChannelRoute(channel.id), onClick = open)
                }
            }
            item("footer-${section.id}") { ChannelNote(stringResource(section.footer)) }
        }
    }
}

/** Settings' standalone channel row: connected count, or broken channels in the danger colour. */
@Composable
internal fun ChannelsSettingsRow(list: List<HubChannel>?, position: GroupPosition, selected: Boolean, onClick: () -> Unit) {
    val summary = ChannelText.listSummary(LocalResources.current, list)
    val problem = ChannelText.hasProblem(list)
    JGroupItem(position) {
        JRow(selected = selected, title = stringResource(R.string.settings_src_channels_title), leadingIcon = PhIcons.ChatsCircle, subtitle = stringResource(R.string.settings_src_channels_row_detail),
            onClick = onClick, chevron = true, value = summary.takeUnless { problem },
            trailing = summary.takeIf { problem }?.let { text -> { JStateLabel(text, Status.Idle, problem = true) } })
    }
}

/** The state on a row's right: the style's dot and the words, in the danger colour when it went wrong. */
@Composable
private fun ChannelStateMark(channel: HubChannel) =
    JStateLabel(ChannelText.state(LocalResources.current, channel), ChannelText.status(channel), problem = ChannelText.isProblem(channel))

@Composable
private fun ChannelNote(text: String) = SettingsCaption(text, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))

/**
 * Settings › Channels › one: how it is doing; its switch; binding it (a step at a time: fields, a QR code to scan, a
 * code to send to the bot) or undoing that; where its messages go; its features; a test message; and its own fuller
 * page when Android has one ([onOpen]). Persona's own apps, /agent in Feishu and voice are set up elsewhere: only shown.
 */
@Composable
internal fun ChannelPage(channels: ChannelManagement?, channelId: String, onBack: () -> Unit, onOpen: ((NavKey) -> Unit)? = null) {
    if (channels == null) {
        JListPage(title = stringResource(R.string.settings_src_channels_title), onBack = onBack) { item { JNotice(stringResource(R.string.settings_src_channels_demo)) } }
        return
    }
    val res = LocalResources.current
    // The page is the current assistant's (its Settings' first group): what it links to is that one's too.
    val owner = currentAssistant().takeIf { it.roster != null }?.id
    val model = remember(channels, channelId) { ChannelModel(channels, channelId) }
    val state by model.state.collectAsState()
    val assistant by remember(channels) { channels.conversations }.collectAsState(emptyList())
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var picking by remember { mutableStateOf(false) }
    var unbinding by remember { mutableStateOf(false) }
    LaunchedEffect(model) { model.load() }
    // A code on screen: ask the hub again on its own until it is scanned or sent, while the page is shown and for a
    // while after (the user scans with another app); back in front, ask at once (`pollWhileShown`).
    val polled = state.bind.prompt?.takeIf { state.bind.polled }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    // When the page left, once per sign-in: a new code (the effect below starting again) keeps it.
    val away = remember(model, polled != null) { SignInAway() }
    LaunchedEffect(model, lifecycle, away, polled?.step, polled?.kind, polled?.userCode, polled?.url) {
        if (polled != null) pollWhileShown(lifecycle.startedFlow(), away, catchUp = { model.catchUp() }, poll = { model.poll() })
    }
    LaunchedEffect(state.boundAs) {
        val who = state.boundAs ?: return@LaunchedEffect
        feedback?.notice(if (who.isBlank()) res.getString(R.string.settings_src_channel_bound) else res.getString(R.string.settings_src_channel_bound_as, who))
        model.boundShown()
    }
    val detail = state.detail
    val channel = detail?.channel
    JListPage(title = channel?.label ?: stringResource(R.string.settings_src_channels_title), onBack = onBack, largeTitle = false,
        refreshing = state.loading && detail != null, onRefresh = { scope.launch { model.load() } }) {
        header(res, channelId, state, onRetry = { scope.launch { model.load() } })
        if (detail != null && channel != null) {
            val editable = ChannelText.editable(channel)
            if (editable) item("enabled") {
                Column {
                    JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                        JSwitchRow(stringResource(R.string.settings_src_enable), channel.enabled, { on -> scope.launch { model.setEnabled(on) } }, enabled = state.busy == null)
                    }
                    ChannelNote(stringResource(if (channel.source == "plugin") R.string.settings_src_channel_off_note_plugin else R.string.settings_src_channel_off_note))
                }
            }
            if (ChannelText.binds(channel)) binding(res, channel, state, model, onUnbind = { unbinding = true }, scope = scope)
            if (editable && channel.target != null) target(res, channel, onPick = { picking = true }, busy = state.busy != null)
            if (editable && channel.features.isNotEmpty()) features(channel, state, model, scope)
            recent(res, detail)
            abilities(res, channel)
            if (editable && channel.state == "connected") item("test") {
                Column(Modifier.padding(top = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    JButton(stringResource(R.string.settings_src_channel_test), { scope.launch { if (model.test()) feedback?.notice(res.getString(R.string.settings_src_channel_test_sent, channel.label)) } },
                        Modifier.fillMaxWidth(), kind = JButtonKind.Secondary, icon = PhIcons.PaperPlaneTilt,
                        loading = state.busy == ChannelBusy.Test, enabled = state.busy == null)
                    ChannelNote(stringResource(R.string.settings_src_channel_test_note))
                }
            }
            val more = ChannelText.settingsDestination(detail.settingsUrl, owner)
            if (more != null && onOpen != null) item("more") {
                Column {
                    JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.l)) {
                        JRow(stringResource(R.string.settings_src_channel_more_settings), leadingIcon = PhIcons.SlidersHorizontal, onClick = { onOpen(more) })
                    }
                    if (!editable) ChannelNote(stringResource(R.string.settings_src_channel_settings_elsewhere))
                }
            }
        }
    }
    if (picking && detail != null) {
        val options = remember(detail, assistant) { targetOptions(detail, assistant) }
        JActionSheet(options.map { option ->
            JAction(option.label ?: ChannelText.targetText(res, option.target), checked = option.selected, onClick = { scope.launch { model.setTarget(option.target) } })
        }, { picking = false }, title = stringResource(R.string.settings_src_channel_target_title), note = stringResource(R.string.settings_src_channel_target_note))
    }
    if (unbinding && channel != null) JDialog(
        onDismiss = { unbinding = false }, title = stringResource(R.string.settings_src_channel_unbind_title, channel.label),
        text = stringResource(R.string.settings_src_channel_unbind_body, channel.label),
        destructive = true, confirmEnabled = state.busy == null,
        confirm = stringResource(R.string.settings_src_channel_unbind) to { scope.launch { if (model.unbind()) { unbinding = false; feedback?.notice(res.getString(R.string.settings_src_channel_unbound)) } }; Unit },
    )
}

private fun LazyListScope.header(res: Resources, id: String, state: ChannelState, onRetry: () -> Unit) {
    val channel = state.channel
    item("header") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
            JRow(channel?.label ?: id, subtitle = if (channel != null) ChannelText.headerLine(res, channel).ifEmpty { null } else stringResource(if (state.loading) R.string.settings_src_loading else R.string.settings_src_unreadable),
                leading = { if (channel != null) JChannelMark(channel, size = MarkSize.Header) else JSourceMark(id, size = MarkSize.Header) },
                trailing = { if (channel == null && state.loading) JSpinner() else if (channel != null && channel.kind != "app") ChannelStateMark(channel) })
        }
    }
    channel?.description?.takeIf { it.isNotBlank() }?.let { item("description") { ChannelNote(it) } }
    if (channel != null && ChannelText.isProblem(channel)) item("channel-error") {
        Column(Modifier.padding(top = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JInlineStatus(channel.error?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_src_state_error), tone = InlineTone.Failed)
            if (ChannelText.binds(channel)) ChannelNote(stringResource(R.string.settings_src_channel_error_note))
        }
    }
    state.error?.let { error ->
        item("error") {
            Column(Modifier.padding(top = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JInlineStatus(error.text(), tone = InlineTone.Failed)
                if (channel == null) JButton(stringResource(R.string.settings_src_retry), onRetry, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary, enabled = !state.loading)
            }
        }
    }
}

private fun LazyListScope.binding(
    res: Resources, channel: HubChannel, state: ChannelState, model: ChannelModel, onUnbind: () -> Unit, scope: kotlinx.coroutines.CoroutineScope,
) {
    val bind = state.bind
    val prompt = bind.prompt
    item("bind-title") { JSectionHeader(if (prompt != null) ChannelText.bindTitle(res, channel) else stringResource(R.string.settings_src_channel_bind)) }
    when {
        ChannelText.isBound(channel) -> item("bound") {
            Column {
                JGroupItem(GroupPosition.of(0, 2)) { JRow(stringResource(R.string.settings_src_channel_bound_account), value = channel.account?.name?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_src_channel_bound)) }
                JGroupItem(GroupPosition.of(1, 2)) {
                    JRow(stringResource(R.string.settings_src_channel_unbind), leadingIcon = PhIcons.X, destructive = true, enabled = state.busy == null, onClick = onUnbind, chevron = false)
                }
                ChannelNote(stringResource(R.string.settings_src_channel_bound_note))
            }
        }
        !channel.enabled -> item("bind-off") { ChannelNote(stringResource(R.string.settings_src_channel_bind_off)) }
        prompt == null -> item("bind-start") {
            Column(Modifier.padding(top = Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                JButton(ChannelText.bindTitle(res, channel), { scope.launch { model.startBind() } }, Modifier.fillMaxWidth(),
                    icon = if (channel.bind == "qr") PhIcons.QrCode else PhIcons.LinkSimple, loading = bind.sending)
                bind.error?.let { JInlineStatus(it.text(), tone = InlineTone.Failed) }
                ChannelNote(if (channel.bind == "qr") stringResource(R.string.settings_src_channel_bind_qr_note, channel.label) else stringResource(R.string.settings_src_channel_bind_note))
            }
        }
        else -> item("bind-step-${prompt.step}-${prompt.kind}") { BindStep(channel, prompt, bind, model, scope) }
    }
}

/**
 * One binding step: its hint and the hub's word on it, then a QR code, a code, or fields to fill in. "Start again" runs in
 * [pageScope], the page's: this list item's goes away when it scrolls out and would leave it spinning.
 */
@Composable
private fun BindStep(channel: HubChannel, prompt: BindPrompt, bind: BindState, model: ChannelModel, pageScope: CoroutineScope) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val res = LocalResources.current
    val feedback = LocalAppFeedback.current
    val copy = rememberCopy()
    val uri = LocalUriHandler.current
    Column(Modifier.fillMaxWidth().padding(top = Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        // Words line up with the notes and rows' text (the style's row padding); the buttons span the page like the others.
        prompt.hint?.takeIf { it.isNotBlank() }?.let {
            Text(it, Modifier.padding(horizontal = rowPadding), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        }
        prompt.error?.takeIf { it.isNotBlank() }?.let { JInlineStatus(it, tone = InlineTone.Failed) }
        bind.error?.let { JInlineStatus(it.text(), tone = InlineTone.Failed) }
        val rows = prompt.qr?.rows.orEmpty()
        val view = StepView.of(prompt)
        when (view) {
            StepView.QrCode -> {
                // The page the code points at, opened directly: on the phone that has the chat app.
                prompt.link?.takeIf { StepView.isWebLink(it) }?.let { link ->
                    JButton(prompt.linkText?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_src_open_web), { val _ = runCatching { uri.openUri(link) } },
                        Modifier.fillMaxWidth(), icon = PhIcons.ArrowSquareOut)
                }
                JQrCode(rows, Modifier.align(Alignment.CenterHorizontally), label = stringResource(R.string.settings_src_channel_qr_label, channel.label))
                Waiting(stringResource(R.string.settings_src_channel_waiting_scan, channel.label))
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JButton(stringResource(R.string.settings_src_channel_save_qr), {
                        scope.launch {
                            val saved = saveQrImage(context, rows, channel.label)
                            feedback?.notice(if (saved) res.getString(R.string.settings_src_channel_qr_saved, channel.label) else res.getString(R.string.settings_src_channel_qr_not_saved))
                        }
                    }, Modifier.weight(1f), kind = JButtonKind.Secondary, icon = PhIcons.Image)
                    JButton(stringResource(R.string.settings_src_cancel), model::cancelBind, Modifier.weight(1f), kind = JButtonKind.Secondary)
                }
            }
            StepView.Code -> {
                val code = prompt.userCode.orEmpty()
                // No code: the page to open is the step (StepView.of); no empty box with a grey "Copy".
                if (code.isNotEmpty()) Row(Modifier.padding(start = rowPadding), verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    JCodeDisplay(code, Modifier.weight(1f))
                    JIconButton(PhIcons.Copy, stringResource(R.string.settings_src_channel_copy_code), { copy(code, null) })
                }
                Waiting(if (prompt.kind == "pair_code") stringResource(R.string.settings_src_channel_waiting_send, channel.label) else stringResource(R.string.settings_src_channel_waiting_authorize))
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    prompt.url?.takeIf { prompt.kind == "device_code" && StepView.isWebLink(it) }?.let { url ->
                        JButton(stringResource(R.string.settings_src_open_web), { val _ = runCatching { uri.openUri(url) } }, Modifier.weight(1f), kind = JButtonKind.Secondary, icon = PhIcons.ArrowSquareOut)
                    }
                    JButton(stringResource(R.string.settings_src_cancel), model::cancelBind, Modifier.weight(1f), kind = JButtonKind.Secondary)
                }
            }
            StepView.QrLink -> {
                QrLinkStep(prompt.url.orEmpty(), channel.label)
                Waiting(stringResource(R.string.settings_src_channel_waiting_confirm, channel.label))
                JButton(stringResource(R.string.settings_src_cancel), model::cancelBind, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary)
            }
            StepView.Unknown, StepView.Incomplete -> CannotDrawStep(prompt, bind.sending, { pageScope.launch { model.startBind() } }, model::cancelBind)
            StepView.Fields -> {
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
                    JButton(stringResource(R.string.settings_src_cancel), model::cancelBind, Modifier.weight(1f), kind = JButtonKind.Secondary, enabled = !bind.sending)
                }
            }
        }
    }
}

/** A spinner and what the page is waiting for, on one line. */
@Composable
private fun Waiting(text: String) {
    Row(Modifier.padding(horizontal = rowPadding), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JSpinner()
        Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

private fun LazyListScope.target(res: Resources, channel: HubChannel, onPick: () -> Unit, busy: Boolean) {
    item("target") {
        Column {
            JSectionHeader(stringResource(R.string.settings_src_channel_target_title))
            JGroupItem(GroupPosition.Single) {
                JRow(stringResource(R.string.settings_src_channel_send_to), leadingIcon = PhIcons.ChatCircle, value = ChannelText.targetText(res, channel.target), enabled = !busy, onClick = onPick)
            }
            ChannelNote(stringResource(R.string.settings_src_channel_target_note))
        }
    }
}

private fun LazyListScope.features(channel: HubChannel, state: ChannelState, model: ChannelModel, scope: kotlinx.coroutines.CoroutineScope) {
    item("features-title") { JSectionHeader(stringResource(R.string.settings_src_channel_features)) }
    itemsIndexed(channel.features, key = { _, it -> "feature-${it.id}" }) { index, feature ->
        JGroupItem(GroupPosition.of(index, channel.features.size)) {
            JSwitchRow(feature.title.ifBlank { feature.id }, feature.on, { on -> scope.launch { model.setFeature(feature.id, on) } },
                subtitle = feature.description?.takeIf { it.isNotBlank() }, enabled = state.busy == null)
        }
    }
}

private fun LazyListScope.recent(res: Resources, detail: ChannelDetail) {
    val waiting = ChannelText.waiting(res, detail)
    val conversations = detail.conversations
    if (waiting == null && conversations.isEmpty()) return
    val count = conversations.size + if (waiting != null) 1 else 0
    item("recent-title") { JSectionHeader(stringResource(R.string.settings_src_channel_recent)) }
    waiting?.let { item("waiting") { JGroupItem(GroupPosition.of(0, count)) { JRow(stringResource(R.string.settings_src_channel_waiting), leadingIcon = PhIcons.HourglassMedium, value = it) } } }
    val offset = if (waiting != null) 1 else 0
    itemsIndexed(conversations, key = { _, it -> "conversation-${it.machineId}/${it.sessionId}" }) { index, conversation ->
        JGroupItem(GroupPosition.of(index + offset, count)) {
            JRow(conversation.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_src_channel_a_chat), leadingIcon = PhIcons.ChatCircle,
                subtitle = persona.core.contracts.parseTime(conversation.at)?.let { stringResource(R.string.settings_src_channel_went_here_at, ChannelText.ago(res, it)) }
                    ?: stringResource(R.string.settings_src_channel_went_here))
        }
    }
}

private fun LazyListScope.abilities(res: Resources, channel: HubChannel) {
    val lines = ChannelText.capabilities(res, channel.capabilities)
    val surfaces = channel.surfaces.takeIf { it.isNotEmpty() }
    val count = lines.size + if (surfaces != null) 1 else 0
    item("abilities-title") { JSectionHeader(stringResource(R.string.settings_src_channel_abilities)) }
    itemsIndexed(lines, key = { _, it -> "ability-$it" }) { index, line ->
        JGroupItem(GroupPosition.of(index, count)) { JRow(line, leadingIcon = PhIcons.Check) }
    }
    surfaces?.let { list ->
        item("surfaces") {
            JGroupItem(GroupPosition.of(lines.size, count)) {
                JRow(stringResource(R.string.settings_src_channel_surfaces), leadingIcon = PhIcons.DeviceMobile, subtitle = list.joinToString(res.getString(R.string.settings_src_list_separator)), subtitleLines = 3)
            }
        }
    }
}

/** The code as a picture in the photo library (Pictures/Persona), for the chat app on this phone to scan from there. */
private suspend fun saveQrImage(context: Context, rows: List<String>, name: String): Boolean = withContext(Dispatchers.IO) {
    runCatching {
        val bitmap = qrBitmap(rows)
        val values = ContentValues().apply {
            put(MediaStore.Images.Media.DISPLAY_NAME, "persona-$name-${System.currentTimeMillis()}.png")
            put(MediaStore.Images.Media.MIME_TYPE, "image/png")
            put(MediaStore.Images.Media.RELATIVE_PATH, "Pictures/Persona")
            put(MediaStore.Images.Media.IS_PENDING, 1)
        }
        val resolver = context.contentResolver
        val uri = resolver.insert(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, values) ?: return@runCatching false
        val written = runCatching {
            resolver.openOutputStream(uri)?.use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) } == true
        }.getOrDefault(false)
        if (!written) { resolver.delete(uri, null, null); return@runCatching false }
        resolver.update(uri, ContentValues().apply { put(MediaStore.Images.Media.IS_PENDING, 0) }, null, null)
        true
    }.getOrDefault(false)
}
