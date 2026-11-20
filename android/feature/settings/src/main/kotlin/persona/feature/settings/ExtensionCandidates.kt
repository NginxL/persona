package persona.feature.settings

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.*
import persona.core.contracts.parseTime
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons

/** The extension's candidate policy is independent of the purpose's ordinary model binding. */
internal fun LazyListScope.extensionCandidates(
    state: ModelPurposeState, requestedOpen: Boolean, onRequested: () -> Unit, onEdit: (CandidateDraft) -> Unit,
) {
    val setting = state.purpose?.extensionCandidates() ?: return
    val draft = state.draft.candidates ?: return
    val providers = state.candidateProviders
    val enabled = !state.busy
    item("candidate-title") { JSectionHeader(stringResource(R.string.settings_candidate_title)) }
    item("candidate-mode") {
        var choosing by remember { mutableStateOf(false) }
        val choices = setting.modes.mapNotNull { mode -> modeLabel(mode)?.let { mode to stringResource(it) } }
        JGroupItem(GroupPosition.Single) {
            JRow(choices.firstOrNull { it.first == draft.mode }?.second ?: draft.mode,
                Modifier.testTag("candidate-mode"), titleLines = 3, onClick = { choosing = true }, enabled = enabled && choices.isNotEmpty())
        }
        if (choosing) JActionSheet(choices.map { (mode, label) ->
            JAction(label, checked = draft.mode == mode) {
                val next = draft.copy(mode = mode)
                onEdit(if (mode == "custom" && next.custom.isEmpty() && providers.isNotEmpty() && setting.most > 0) next.add(providers) else next)
            }
        }, { choosing = false }, title = stringResource(R.string.settings_candidate_title))
    }
    if (draft.mode == "custom") {
        item("candidate-order") {
            SettingsCaption(stringResource(R.string.settings_candidate_order_hint), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
        }
        itemsIndexed(draft.custom, key = { _, row -> "candidate-edit-${row.key}" }) { index, row ->
            val saved = setting.custom.firstOrNull { it.provider == row.provider && (it.configuredModel ?: it.model) == row.model }
            CandidateEditor(index, draft.custom.size, row, saved, providers, enabled,
                onChange = { onEdit(draft.replace(it)) }, onMove = { onEdit(draft.move(index, it)) },
                onRemove = { onEdit(draft.copy(custom = draft.custom.filterNot { it.key == row.key })) })
        }
        item("candidate-add") {
            JButton(stringResource(R.string.settings_candidate_add), { onEdit(draft.add(providers)) },
                Modifier.fillMaxWidth().padding(top = Tokens.Space.s), kind = JButtonKind.Secondary, icon = PersonaIcons.add,
                enabled = enabled && draft.custom.size < setting.most && providers.isNotEmpty())
        }
        state.candidateProblem?.let { issue ->
            item("candidate-problem") { JInlineStatus(issue.text(), Modifier.padding(vertical = Tokens.Space.s), tone = InlineTone.Failed) }
        }
    }
    if (setting.requested.isEmpty()) {
        item("candidate-empty") {
            SettingsCaption(stringResource(R.string.settings_candidate_no_requests), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
        }
    } else {
        item("candidate-requests") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
                JRow(stringResource(R.string.settings_candidate_requested, setting.requested.size),
                    Modifier.testTag("candidate-requests").foldToggle(requestedOpen, onRequested), titleLines = Int.MAX_VALUE,
                    trailing = { JIcon(if (requestedOpen) PersonaIcons.collapse else PersonaIcons.expand, null, decorative = true, tint = PersonaTheme.palette.ink3) })
            }
        }
        if (requestedOpen) itemsIndexed(setting.requested, key = { index, _ -> "candidate-request-$index" }) { _, request ->
            val locale = currentLocale
            val time = remember(request.lastUsed, locale) {
                parseTime(request.lastUsed)?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, locale) }
            }
            time?.let { SettingsCaption(stringResource(R.string.settings_candidate_last, it), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
            request.models.forEachIndexed { index, candidate ->
                JGroupItem(GroupPosition.of(index, request.models.size)) {
                    JRow(candidate.model, titleLines = Int.MAX_VALUE, subtitleLines = Int.MAX_VALUE,
                        subtitle = if (candidate.available) candidate.providerName.ifBlank { candidate.provider }
                        else listOf(stringResource(R.string.settings_candidate_unavailable), candidate.problem).filter(String::isNotBlank).joinToString("\n"))
                }
            }
        }
    }
}

private fun modeLabel(mode: String) = when (mode) {
    "extension" -> R.string.settings_candidate_extension
    "off" -> R.string.settings_candidate_off
    "custom" -> R.string.settings_candidate_custom
    else -> null
}

@Composable
private fun CandidateEditor(
    index: Int, count: Int, row: CandidateRow, saved: ModelCandidate?, providers: List<ModelProvider>, enabled: Boolean,
    onChange: (CandidateRow) -> Unit, onMove: (Int) -> Unit, onRemove: () -> Unit,
) {
    val position = index + 1
    var choosing by remember { mutableStateOf(false) }
    val provider = providers.firstOrNull { it.id == row.provider }
    JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
        Row(Modifier.fillMaxWidth().padding(horizontal = rowPadding), verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.settings_candidate_number, position), Modifier.weight(1f), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
            JIconButton(PersonaIcons.queueUp, stringResource(R.string.settings_candidate_up, position), { onMove(-1) }, enabled = enabled && index > 0, raised = false)
            JIconButton(PersonaIcons.queueDown, stringResource(R.string.settings_candidate_down, position), { onMove(1) }, enabled = enabled && position < count, raised = false)
            JIconButton(PersonaIcons.delete, stringResource(R.string.settings_candidate_remove, position), onRemove, enabled = enabled, raised = false)
        }
        JRow(stringResource(R.string.settings_src_model_provider), Modifier.testTag("candidate-provider-$position"),
            subtitle = provider?.displayName ?: saved?.providerName?.takeIf(String::isNotBlank) ?: row.provider.ifBlank { stringResource(R.string.settings_candidate_select) },
            subtitleLines = Int.MAX_VALUE, onClick = { choosing = true }, enabled = enabled && providers.isNotEmpty())
        JTextField(row.model, { onChange(row.copy(model = it)) }, Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.s)
            .testTag("candidate-model-$position"), label = stringResource(R.string.settings_candidate_model, position),
            placeholder = stringResource(R.string.settings_src_model_provider_default), singleLine = true, enabled = enabled)
        if (saved != null && !saved.available) {
            JInlineStatus(saved.problem.ifBlank { stringResource(R.string.settings_candidate_unavailable) },
                Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s), tone = InlineTone.Failed)
        }
    }
    if (choosing) JActionSheet(providers.map {
        JAction(it.displayName, checked = row.provider == it.id) {
            if (it.id != row.provider) onChange(row.copy(provider = it.id, model = ""))
        }
    }, { choosing = false }, title = stringResource(R.string.settings_candidate_provider, position))
}
