package persona.feature.inbox

import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.platform.LocalContext
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.Navigator
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch

/**
 * What "AI draft" / "AI revise" asks the hub for (the old app's wording): the user's request, then "Revise this version: <current draft>"
 * when there is a draft to revise; either may be missing.
 */
fun draftInstruction(request: String, draft: String): String = listOfNotNull(
    request.trim().takeIf { it.isNotEmpty() },
    draft.trim().takeIf { it.isNotEmpty() }?.let { "在这版上改：$it" }, // i18n-ignore: instruction for the model
).joinToString("\n")

/** Replies whose last send has an unclear result are checked, never sent again: no confirmation for that. */
internal val ReplyComposerState.checksResult: Boolean
    get() = delivery in setOf(ReplyDelivery.Unknown, ReplyDelivery.NeedsConfirmation)

/**
 * A request and "AI draft" (no draft yet) or "AI revise" (a draft to revise). The hub's text replaces the reply as an AI draft
 * (sending it asks first); a failure says why and keeps what was there.
 */
@Composable
internal fun InboxAiDraftRow(repository: InboxRepository, itemId: String, composer: ReplyComposer, modifier: Modifier = Modifier) {
    val state by composer.state.collectAsStateWithLifecycle()
    var request by rememberSaveable(itemId) { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    val revising = state.draft.text.isNotBlank()
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JTextField(request, { request = it }, Modifier.weight(1f), placeholder = stringResource(if (revising) R.string.inbox_reply_ai_revise_placeholder else R.string.inbox_reply_ai_request_placeholder),
                singleLine = true, enabled = !busy && state.canEdit)
            val res = LocalResources.current
            JButton(stringResource(if (revising) R.string.inbox_reply_ai_revise else R.string.inbox_ai_draft), {
                busy = true
                error = null
                val instruction = draftInstruction(request, state.draft.text)
                scope.launch {
                    try {
                        val text = repository.draftReply(itemId, instruction)
                        if (composer.replaceWithAi(text)) request = "" else error = res.getString(R.string.inbox_reply_busy)
                    } catch (e: CancellationException) { throw e }
                    catch (e: Exception) { error = res.getString(R.string.inbox_reply_draft_failed, failureReason(e).resolve(res)) }
                    finally { busy = false }
                }
            }, kind = JButtonKind.Secondary, size = JButtonSize.Medium, icon = PhIcons.Sparkle,
                enabled = state.canEdit, loading = busy, loadingText = stringResource(R.string.inbox_reply_drafting))
        }
        error?.let { JNotice(it, status = Status.NeedsYou) }
    }
}

/**
 * The reply to an item, AI-assisted (the old app's detail, the AI twin draft's "Revise"): why the AI twin drafted it and
 * its conversation, the reply itself, and "AI draft" / "AI revise". Sending an AI draft asks first ([SendAsYouDialog]); the
 * user's own words go as typed. Feishu items reply in their chat, mail items to the newest message (POST …/reply).
 */
@Composable
internal fun InboxDraftSheet(
    repository: InboxRepository,
    record: InboxRecord,
    composer: ReplyComposer,
    navigator: Navigator?,
    onDismiss: () -> Unit,
) {
    val state by composer.state.collectAsStateWithLifecycle()
    var confirming by remember { mutableStateOf(false) }
    val palette = PersonaTheme.palette
    val agent = record.agent
    val dismiss by rememberUpdatedState(onDismiss)
    // Close once this sheet's reply has gone (not because an earlier one had when it opened).
    LaunchedEffect(composer) { composer.state.map { it.sent }.distinctUntilChanged().drop(1).collect { if (it) dismiss() } }
    JSheet(onDismiss = onDismiss, title = stringResource(if (record.isLark) R.string.inbox_reply_sheet_lark else R.string.inbox_reply_to_mail, record.item.title),
        dismissible = !state.sending, scrollable = true) {
        // The sheet is opaque: the composer must not sample (and show) the page behind it as glass.
        CompositionLocalProvider(LocalGlassSource provides null) {
        Column(Modifier.fillMaxWidth().imePadding(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            if (agent != null && agent.state == "drafted" && agent.basis.isNotBlank()) {
                Text(stringResource(R.string.inbox_reply_ai_basis, agent.basis), style = PersonaTheme.type.callout, color = palette.ink2)
            }
            if (agent?.machineId != null && agent.sessionId != null) {
                JButton(stringResource(R.string.inbox_reply_view_process), {
                    onDismiss()
                    navigator?.navigate(inboxConversationRoute(agent.machineId, agent.sessionId, record.item.id))
                },
                    kind = JButtonKind.Secondary, size = JButtonSize.Small, icon = PhIcons.ListChecks)
            }
            ReplyComposerField(state, composer::update, onSend = {
                if (!state.checksResult && state.draft.ai) confirming = true else composer.send()
            }, placeholder = stringResource(R.string.inbox_reply_placeholder))
            InboxAiDraftRow(repository, record.item.id, composer)
        }
        }
    }
    if (confirming) SendAsYouDialog(sendConfirmation(record, state.draft.text),
        onSend = { confirming = false; composer.send() }, onDismiss = { confirming = false }, enabled = !state.sending)
}
