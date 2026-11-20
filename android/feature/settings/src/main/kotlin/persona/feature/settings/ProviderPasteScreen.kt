package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import persona.core.contracts.api.ProviderPasteDraft
import persona.core.contracts.api.ProviderPasteNote
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.InlineTone
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.JInlineStatus
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JTextButton
import persona.core.designsystem.JTextField
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rowPadding

/** The two additional ways into Add provider, only on a hub with launcher_import_parse. */
internal fun LazyListScope.otherProviderWays(
    state: NewProviderState,
    onPaste: () -> Unit,
    onAlias: (FoundAlias) -> Unit,
    includeHeader: Boolean = false,
) {
    if (!state.importParse || state.editing || state.pasteContinued) return
    if (includeHeader) item("other-ways-title") { JSectionHeader(stringResource(R.string.settings_paste_other_ways)) }
    item("paste-entry") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = if (includeHeader) Tokens.Space.xxs else Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_paste_title), leadingIcon = PersonaIcons.document,
                subtitle = stringResource(R.string.settings_paste_entry_detail), subtitleLines = 3, onClick = onPaste)
        }
    }
    if (state.foundAliases.isNotEmpty()) {
        item("found-title") { JSectionHeader(stringResource(R.string.settings_paste_found), Modifier.padding(top = Tokens.Space.s)) }
        state.foundAliases.forEachIndexed { index, alias ->
            item("found-${alias.name}") {
                JGroupItem(GroupPosition.of(index, state.foundAliases.size)) {
                    JRow(alias.name, leadingIcon = PersonaIcons.machine,
                        subtitle = listOfNotNull(
                            providerHost(alias.baseUrl),
                            alias.computer,
                            stringResource(if (alias.online) R.string.settings_online else R.string.settings_offline),
                        ).joinToString("\n"), subtitleLines = 3, onClick = { onAlias(alias) })
                }
            }
        }
    }
}

/** Paste a configuration: the reviewed draft continues into the existing add or edit form. */
internal fun LazyListScope.pasteStep(
    state: NewProviderState,
    onText: (String) -> Unit,
    onRetry: () -> Unit,
    onSomethingElse: () -> Unit,
    onDraft: (Int) -> Unit,
    onAction: (ProviderPasteAction) -> Unit,
) {
    val paste = state.paste
    if (paste.fromLink) item("paste-link") {
        // The link may carry a key: show the same review as a paste, never the link itself (#5428).
        Column(Modifier.padding(top = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            SettingsCaption(stringResource(R.string.settings_paste_link_opened), Modifier.padding(horizontal = rowPadding))
            JTextButton(stringResource(R.string.settings_paste_something_else), onSomethingElse)
        }
    } else item("paste-input") {
        JTextField(paste.text, onText, Modifier.padding(top = Tokens.Space.m), label = stringResource(R.string.settings_paste_label),
            placeholder = stringResource(R.string.settings_paste_placeholder), supportingText = stringResource(R.string.settings_paste_hint),
            minLines = 6, maxLines = 12, enabled = !state.busy, autofill = false, monospace = true)
    }
    item("paste-status") {
        when {
            paste.reading -> JInlineStatus(stringResource(R.string.settings_paste_reading), Modifier.padding(top = Tokens.Space.m))
            paste.error != null -> JInlineStatus(paste.error.text(), Modifier.padding(top = Tokens.Space.m), tone = InlineTone.Failed,
                action = { JTextButton(stringResource(R.string.settings_retry_read), onRetry) })
            paste.answer != null && paste.answerText == paste.text -> {
                val format = when (paste.answer.format) {
                    "shell" -> R.string.settings_paste_format_shell
                    "settings" -> R.string.settings_paste_format_settings
                    "curl" -> R.string.settings_paste_format_curl
                    "cindy" -> R.string.settings_paste_format_cindy
                    "link" -> R.string.settings_paste_format_link
                    else -> null
                }
                format?.let { SettingsCaption(stringResource(it), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
            }
        }
    }
    val answer = paste.answer ?: return
    if (answer.drafts.isEmpty()) item("paste-none") {
        SettingsCaption(stringResource(R.string.settings_paste_nothing), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
    }
    if (answer.drafts.size > 1) item("paste-several") {
        SettingsCaption(pluralStringResource(R.plurals.settings_paste_several, answer.drafts.size, answer.drafts.size), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
    }
    answer.drafts.forEachIndexed { index, draft ->
        item("paste-draft-$index") { ProviderPasteDraftCard(draft, answer.drafts.size > 1, paste.draft == index, !state.busy) { onDraft(index) } }
    }
    val choice = paste.choice
    if (choice != null && choice.draft.matches.isNotEmpty()) {
        item("paste-action-title") { JSectionHeader(stringResource(R.string.settings_paste_action_label)) }
        val actions = choice.draft.actions()
        actions.forEachIndexed { index, action ->
            item("paste-action-${action.kind}-${action.provider}") {
                JGroupItem(GroupPosition.of(index, actions.size)) {
                    val match = choice.draft.matches.firstOrNull { it.name == action.provider }
                    val name = match?.displayName?.takeIf { it.isNotBlank() } ?: action.provider
                    val (title, hint) = when (action.kind) {
                        PasteActionKind.Merge -> stringResource(R.string.settings_paste_merge, name) to stringResource(R.string.settings_paste_merge_hint)
                        PasteActionKind.Key -> stringResource(R.string.settings_paste_key, name) to stringResource(R.string.settings_paste_key_hint)
                        PasteActionKind.New -> stringResource(R.string.settings_paste_new) to stringResource(R.string.settings_paste_new_hint)
                    }
                    JRow(title, subtitle = hint, subtitleLines = 3, selected = action == choice.action, chevron = false,
                        role = Role.RadioButton, enabled = !state.busy, onClick = { onAction(action) },
                        trailing = if (action == choice.action) ({ JIcon(PersonaIcons.success, stringResource(R.string.settings_src_provider_selected), tint = PersonaTheme.palette.accent) }) else ({}))
                }
            }
        }
    }
    item("paste-notes") { ProviderPasteNotes(answer.notes) }
}

@Composable
private fun ProviderPasteDraftCard(draft: ProviderPasteDraft, radio: Boolean, selected: Boolean, enabled: Boolean, onPick: () -> Unit) {
    val provider = draft.provider
    val host = providerHost(provider.baseUrl)
    val title = provider.displayName?.takeIf { it.isNotBlank() } ?: draft.label?.takeIf { it.isNotBlank() } ?: host ?: provider.baseUrl
    val auth = provider.auth?.takeUnless { provider.kind == "api" && provider.api in listOf("responses", "chat") }?.let {
        if (it == "api_key") "x-api-key" else "Bearer"
    }
    val modelCount = draft.modelsForProvider().size
    val subtitle = listOfNotNull(
        host?.takeIf { it != title },
        pluralStringResource(R.plurals.settings_paste_models_count, modelCount, modelCount),
        if (provider.kind == "api") stringResource(R.string.settings_providers_api_tag) else null,
        auth,
    ).joinToString("\n")
    JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
        JRow(title, subtitle = subtitle, subtitleLines = 4, selected = selected, enabled = enabled, chevron = false,
            role = if (radio) Role.RadioButton else Role.Button, onClick = if (radio) onPick else null,
            trailing = if (radio && selected) ({ JIcon(PersonaIcons.success, stringResource(R.string.settings_src_provider_selected), tint = PersonaTheme.palette.accent) }) else ({}))
        val lines = draft.configurationLines(androidx.compose.ui.platform.LocalResources.current)
        if (lines.isNotEmpty() || draft.notes.isNotEmpty()) {
            Column(Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                lines.forEach { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
                ProviderPasteNotes(draft.notes)
            }
        }
    }
}

@Composable
private fun ProviderPasteNotes(notes: List<ProviderPasteNote>) {
    if (notes.isEmpty()) return
    val resources = androidx.compose.ui.platform.LocalResources.current
    Column(Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        notes.forEach { note ->
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                JIcon(PersonaIcons.info, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink3)
                Text(providerPasteNoteText(note, resources), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
        }
    }
}
