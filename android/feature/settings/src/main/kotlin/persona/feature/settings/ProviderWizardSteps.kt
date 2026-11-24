package persona.feature.settings

import android.content.res.Resources
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import persona.core.contracts.api.ProviderAdded
import persona.core.contracts.api.ProviderPreset
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

// "New model provider"'s wizard on a hub with built-in presets (docs/assistant/onboarding.md 4.3 "Three paths" B, the web's ProviderWizard):
// ① the providers to pick from, ② the key, ③ what now runs on it. The state is NewProviderModel.

/** ①: the hub's providers in its order (a built-in one says who it is for; an extension's its kind and host), the gateway typed in last. */
internal fun LazyListScope.pickStep(
    res: Resources, state: NewProviderState, onPick: (String?) -> Unit,
    onPaste: () -> Unit = {}, onAlias: (FoundAlias) -> Unit = {},
) {
    item("pick-intro") { SettingsCaption(stringResource(R.string.settings_onb_pick_intro), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m)) }
    val rows = state.presets.size + 1
    itemsIndexed(state.presets, key = { _, it -> "pick-${it.id}" }) { index, p ->
        JGroupItem(GroupPosition.of(index, rows)) {
            JRow(p.title, leadingIcon = if (p.builtin) PhIcons.Sparkle else PhIcons.PuzzlePiece,
                subtitle = listOfNotNull(p.description?.takeIf { it.isNotBlank() }, presetSubtitle(res, p).takeUnless { p.builtin }).joinToString("\n").ifEmpty { null },
                subtitleLines = 3, value = if (p.builtin) null else stringResource(R.string.settings_onb_extension_badge), onClick = { onPick(p.id) })
        }
    }
    item("pick-custom") {
        JGroupItem(GroupPosition.of(rows - 1, rows)) {
            JRow(stringResource(R.string.settings_onb_custom), leadingIcon = PhIcons.LinkSimple, subtitle = stringResource(R.string.settings_onb_custom_detail),
                subtitleLines = 3, onClick = { onPick(null) })
        }
    }
    otherProviderWays(state, onPaste, onAlias)
    state.presetsError?.let { error -> item("pick-error") { JInlineStatus(error.text(), Modifier.padding(top = Tokens.Space.m), tone = InlineTone.Caution) } }
}

/** (2): only the key, tested by itself; whether the main conversation takes it; the name and another model under "More options". */
internal fun LazyListScope.keyStep(
    state: NewProviderState, preset: ProviderPreset, editable: Boolean, onEdit: (NewProviderDraft) -> Unit, onAssign: (Boolean) -> Unit,
    onRetest: () -> Unit, onModel: () -> Unit, more: Boolean, onMore: () -> Unit,
    /** "Computers" under "More options", on a hub with device scopes. */
    onComputers: () -> Unit = {},
    referencesAvailable: Boolean = false,
) {
    val draft = state.draft
    item("key-preset") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(preset.title, leadingIcon = if (preset.builtin) PhIcons.Sparkle else PhIcons.PuzzlePiece,
                subtitle = if (preset.builtin) presetHost(preset) else stringResource(R.string.settings_src_provider_preset_note, preset.extension))
        }
    }
    if (!state.hubNames) nameStepField(state, editable, onEdit)
    item("key") {
        ProviderKeyField(draft.apiKey, { onEdit(draft.copy(apiKey = it)) }, draft.apiKeyByReference,
            onByReferenceChange = if (referencesAvailable) ({ on ->
                onEdit(draft.copy(apiKey = "", apiKeyByReference = on))
            }) else null,
            stored = null, label = "API Key", // i18n-ignore: providers' own field name
            modifier = Modifier.fillMaxWidth().padding(top = Tokens.Space.m), enabled = editable && (referencesAvailable || !draft.apiKeyByReference),
            placeholder = stringResource(R.string.settings_onb_key_paste),
            supportingText = if (state.referencePending) stringResource(R.string.settings_provider_reference_saved)
            else preset.keyHint?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.settings_onb_key_hint_with, it) }
                ?: stringResource(R.string.settings_src_provider_key_hint))
    }
    preset.signupLink?.let { link ->
        item("key-where") {
            val uri = LocalUriHandler.current
            JTextButton(stringResource(R.string.settings_onb_where_key), { val _ = runCatching { uri.openUri(link) } }, icon = PhIcons.ArrowSquareOut)
        }
    }
    item("key-test") { KeyTestStatus(state, preset, onRetest) }
    if (state.hubNames && preset.runsClaude) item("key-assign") {
        val checking = state.readinessPending && state.assignSet == null
        val machine = state.readiness?.machine?.name?.takeIf { it.isNotBlank() }
        val hint = when {
            checking -> stringResource(R.string.settings_onb_assign_checking)
            !state.works -> stringResource(R.string.settings_onb_assign_hint)
            machine != null -> stringResource(R.string.settings_onb_assign_hint_works, machine)
            else -> stringResource(R.string.settings_onb_assign_hint_works_here)
        }
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            if (checking) JRow(stringResource(R.string.settings_onb_assign_label), subtitle = hint, subtitleLines = 4, trailing = { JSpinner() })
            else JSwitchRow(stringResource(R.string.settings_onb_assign_label), state.assign, onAssign, subtitle = hint, enabled = editable)
        }
    }
    if (state.hubNames && !preset.runsClaude) item("key-direct-note") {
        JNotice(stringResource(R.string.settings_onb_direct_note), Modifier.padding(top = Tokens.Space.m), status = Status.Idle)
    }
    item("key-more") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_onb_more_options),
                value = if (more) null else stringResource(if (state.hubNames) R.string.settings_onb_more_options_badge else R.string.settings_src_provider_default_model),
                trailing = { JIcon(if (more) PhIcons.CaretUp else PhIcons.CaretDown, null, decorative = true, tint = PersonaTheme.palette.ink3) }, onClick = onMore)
        }
    }
    if (more) {
        if (state.hubNames) nameStepField(state, editable, onEdit)
        item("key-model") {
            if (preset.choices.isEmpty()) {
                JTextField(draft.presetModel, { onEdit(draft.copy(presetModel = it)) }, Modifier.fillMaxWidth().padding(top = Tokens.Space.m),
                    label = stringResource(R.string.settings_src_provider_default_model), singleLine = true, enabled = editable)
            } else {
                val picked = preset.choices.firstOrNull { it.id == draft.presetModel }
                JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                    JRow(stringResource(R.string.settings_src_provider_default_model), value = picked?.label ?: draft.presetModel.ifEmpty { stringResource(R.string.settings_src_provider_extension_default) },
                        enabled = editable, onClick = onModel)
                }
            }
        }
        item("key-model-note") {
            SettingsCaption(preset.modelsError?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.settings_src_provider_models_error, it) }
                ?: stringResource(R.string.settings_src_provider_model_note), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
        }
        computersRow(state, onComputers)
    }
}

/** The name, optional on a hub that makes one up (left empty, it is named after the provider). */
private fun LazyListScope.nameStepField(state: NewProviderState, editable: Boolean, onEdit: (NewProviderDraft) -> Unit) {
    item("key-name") {
        JTextField(state.draft.name, { onEdit(state.draft.copy(name = it)) }, Modifier.fillMaxWidth().padding(top = Tokens.Space.m),
            label = stringResource(R.string.settings_src_provider_name),
            placeholder = if (state.hubNames) stringResource(R.string.settings_onb_name_auto) else stringResource(R.string.settings_src_provider_name_example, "jv_team"),
            singleLine = true, enabled = editable, isError = state.nameProblem != null,
            supportingText = state.nameProblem?.text() ?: stringResource(R.string.settings_src_provider_name_hint))
    }
}

@Composable
private fun KeyTestStatus(state: NewProviderState, preset: ProviderPreset, onRetest: () -> Unit) {
    val modifier = Modifier.padding(top = Tokens.Space.m)
    if (state.referencePending) {
        SettingsCaption(stringResource(R.string.settings_provider_reference_saved), modifier.padding(horizontal = rowPadding)); return
    }
    if (!state.hubNames) return
    if (!preset.testable) { SettingsCaption(stringResource(R.string.settings_onb_key_test_responses), modifier.padding(horizontal = rowPadding)); return }
    val test = state.keyTest
    when {
        test == null -> SettingsCaption(stringResource(if (state.draft.apiKey.isBlank()) R.string.settings_onb_key_test_cost else R.string.settings_onb_key_test_wait),
            modifier.padding(horizontal = rowPadding))
        test.running -> JInlineStatus(stringResource(R.string.settings_onb_key_testing), modifier, tone = InlineTone.Busy)
        test.result?.ok == true -> JInlineStatus(stringResource(R.string.settings_onb_key_test_ok, test.result.model.orEmpty(), test.result.ms), modifier, tone = InlineTone.Done)
        else -> JInlineStatus(test.failure?.text() ?: test.result?.error?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_onb_key_test_failed),
            modifier, tone = InlineTone.Failed, action = { JTextButton(stringResource(R.string.settings_onb_retest), onRetest) })
    }
}

/** ③: saved; what runs on it now and what was left as it was; that Claude Code is still missing on the computer. */
internal fun LazyListScope.doneStep(state: NewProviderState, added: WizardAdded) {
    item("done") {
        var help by remember { mutableStateOf(false) }
        val summary = assignSummary(added.result, added.asked)
        val machine = state.readiness?.machine?.name?.takeIf { it.isNotBlank() }
        val toMain = added.asked && added.result.assigned?.contains("assistant") == true
        val direct = state.selected?.runsClaude == false
        JEmptyState(stringResource(R.string.settings_onb_done_title, added.result.title),
            if (direct) stringResource(R.string.settings_onb_direct_note)
            else summary.assigned?.text() ?: stringResource(R.string.settings_onb_done_pick_later), icon = PhIcons.CheckCircle)
        summary.kept.forEach { SettingsCaption(it.text(), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.xs)) }
        if (!direct && stillNoClaude(added, state.readiness)) {
            val words = when {
                toMain && machine != null -> stringResource(R.string.settings_onb_done_no_claude, machine)
                toMain -> stringResource(R.string.settings_onb_done_no_claude_any)
                machine != null -> stringResource(R.string.settings_onb_done_no_claude_kept, machine)
                else -> stringResource(R.string.settings_onb_done_no_claude_kept_any)
            }
            JNotice(words, status = Status.NeedsYou, icon = PhIcons.Warning, actionLabel = stringResource(R.string.settings_onb_how_install), onAction = { help = true })
        }
        if (help) ReadinessHelpDialog(ReadinessHelp.InstallClaude, machine, onDismiss = { help = false }, onAction = { help = false }, canConnectModel = false)
    }
}

/** What "Now using it: main conversation, new agents" and each kept category say; [assigned] null when none was asked (or an older hub). */
internal data class AssignSummary(val assigned: ResText?, val kept: List<ResText>)

internal fun assignSummary(result: ProviderAdded, asked: Boolean): AssignSummary {
    val assigned = result.assigned
    if (!asked || assigned == null) return AssignSummary(null, emptyList())
    fun purpose(id: String): Any = when (id) {
        "assistant" -> ResText.of(R.string.settings_onb_purpose_assistant)
        "agent.new" -> ResText.of(R.string.settings_onb_purpose_agent_new)
        else -> id
    }
    return AssignSummary(
        assigned = assigned.takeIf { it.isNotEmpty() }?.let { ids ->
            ResText.of(if (ids.size > 1) R.string.settings_onb_assigned_both else R.string.settings_onb_assigned, *ids.map(::purpose).toTypedArray())
        },
        kept = result.kept.map { k ->
            ResText.of(R.string.settings_onb_kept, purpose(k.purpose), ResText.of(when (k.reason) {
                "customized" -> R.string.settings_onb_kept_customized
                "incompatible" -> R.string.settings_onb_kept_incompatible
                else -> R.string.settings_onb_kept_failed
            }))
        },
    )
}

/** The host a preset talks to, "" when its address does not parse. */
internal fun presetHost(preset: ProviderPreset): String = runCatching { java.net.URI(preset.baseUrl).host }.getOrNull().orEmpty()
