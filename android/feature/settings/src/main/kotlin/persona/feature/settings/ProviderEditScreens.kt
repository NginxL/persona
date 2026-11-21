package persona.feature.settings

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Machine
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.api.PresetModel
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

// The parts "New model provider"'s form gains for editing a provider (iOS ProviderWizardView's models step and Advanced,
// ProviderProxyPage; the web's ProviderScope): the models picked, the tiers and limits, the proxy's page, headers and
// environment as write-only secrets, and the "Computers" page of the device scope. The state is NewProviderModel.

/** The scope in one line: "Chosen computers: Work Mac · on demand". */
@Composable
internal fun scopeSummaryText(v: HubLauncher, machines: List<Machine>): String {
    val res = LocalResources.current
    val sep = stringResource(R.string.settings_providers_list_sep)
    return scopeSummary(v, machineName(machines), sep).joinToString(" · ") { it.resolve(res) }
}

/** The models picked (the default tagged), the rest of what the gateway serves to pick, and "Add" by id. */
internal fun LazyListScope.modelsSection(
    state: NewProviderState, editable: Boolean, onEdit: (NewProviderDraft) -> Unit, newModel: String, onNewModel: (String) -> Unit,
) {
    val draft = state.draft
    val found = state.found?.models.orEmpty()
    val rows = draft.models + found.filter { f -> draft.models.none { it.id == f.id } }
    item("models-header") { JSectionHeader(stringResource(R.string.settings_pedit_models), Modifier.padding(top = Tokens.Space.m), count = draft.models.size.takeIf { it > 0 }) }
    rows.forEachIndexed { index, m ->
        item("pick-model-${m.id}") {
            val picked = draft.models.any { it.id == m.id }
            JGroupItem(GroupPosition.of(index, rows.size)) {
                JRow(m.label, subtitle = modelDetail(m), chevron = false, enabled = editable, role = Role.Checkbox,
                    onClick = { onEdit(draft.toggleModel(m)) },
                    trailing = {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                            if (m.id == draft.model.trim()) JTag(stringResource(R.string.settings_pedit_default_tag), status = Status.Working)
                            JCheckbox(picked, null)
                        }
                    })
            }
        }
    }
    item("models-add") {
        Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JTextField(newModel, onNewModel, Modifier.weight(1f), placeholder = stringResource(R.string.settings_pedit_add_model), singleLine = true, enabled = editable)
            JButton(stringResource(R.string.settings_pedit_add), {
                onEdit(draft.addModel(newModel, found)); onNewModel("")
            }, kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = editable && newModel.isNotBlank())
        }
    }
    item("models-note") { SettingsCaption(stringResource(R.string.settings_pedit_models_note), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
}

/** A model's window and thinking levels, when known. */
@Composable
private fun modelDetail(m: PresetModel): String? = listOfNotNull(
    m.id.takeIf { m.name?.isNotBlank() == true },
    m.contextWindow?.takeIf { it > 0 }?.let { stringResource(R.string.settings_pedit_context_tokens, tokensText(it)) },
    m.efforts?.takeIf { it.isNotEmpty() }?.joinToString("/"),
).joinToString(" · ").ifEmpty { null }

/** 200000 -> "200K", 1000000 -> "1M". */
internal fun tokensText(n: Int): String = when {
    n >= 1_000_000 && n % 1_000_000 == 0 -> "${n / 1_000_000}M"
    n >= 1_000 && n % 1_000 == 0 -> "${n / 1_000}K"
    else -> n.toString()
}

/** A thinking level's words. */
@Composable
internal fun effortText(level: String): String = when (level) {
    "low" -> stringResource(R.string.settings_pedit_effort_low)
    "medium" -> stringResource(R.string.settings_pedit_effort_medium)
    "high" -> stringResource(R.string.settings_pedit_effort_high)
    "xhigh" -> stringResource(R.string.settings_pedit_effort_xhigh)
    "max" -> stringResource(R.string.settings_pedit_effort_max)
    "" -> stringResource(R.string.settings_pedit_effort_model)
    else -> level
}

/** The Proxy row's value: the choice, Manual's server itself. */
@Composable
internal fun proxySummary(d: NewProviderDraft): String = when (d.proxyMode) {
    ProxyMode.Follow -> stringResource(R.string.settings_pedit_proxy_follow)
    ProxyMode.Direct -> stringResource(R.string.settings_pedit_proxy_direct)
    ProxyMode.Manual -> d.proxy.trim().ifEmpty { stringResource(R.string.settings_pedit_proxy_manual) }
}

/**
 * "Advanced" (iOS's fold): the tiers, the small and subagent models' force, the thinking level, the limits, the proxy's
 * page, the headers and the environment. Headers and the environment are never shown: a stored one says so, typing
 * replaces it, "Clear saved" removes it when saving.
 */
internal fun LazyListScope.advancedSection(
    state: NewProviderState, editable: Boolean, onEdit: (NewProviderDraft) -> Unit, onEffort: () -> Unit, onProxy: () -> Unit,
) {
    val d = state.draft
    val stored = state.existing
    if (d.chat) {
        item("adv-chat") { SettingsCaption(stringResource(R.string.settings_pedit_chat_note), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
        return
    }
    val claude = d.kind != "api" && d.agent == "claude"
    if (claude) {
        listOf(
            "Opus" to d.opusModel to { v: String -> d.copy(opusModel = v) },
            "Sonnet" to d.sonnetModel to { v: String -> d.copy(sonnetModel = v) },
            "Haiku" to d.haikuModel to { v: String -> d.copy(haikuModel = v) },
            "Fable" to d.fableModel to { v: String -> d.copy(fableModel = v) },
        ).forEach { (pair, set) ->
            val (tier, value) = pair
            item("adv-tier-$tier") {
                JTextField(value, { onEdit(set(it)) }, Modifier.fillMaxWidth().padding(top = Tokens.Space.m), label = stringResource(R.string.settings_pedit_tier, tier),
                    placeholder = stringResource(R.string.settings_pedit_same_as_default), singleLine = true, enabled = editable)
            }
        }
        item("adv-force") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                JSwitchRow(stringResource(R.string.settings_pedit_subagent_force), d.subagentForce, { onEdit(d.copy(subagentForce = it)) },
                    subtitle = stringResource(R.string.settings_pedit_subagent_force_hint), enabled = editable)
            }
        }
        item("adv-effort") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                JRow(stringResource(R.string.settings_pedit_effort), value = effortText(d.effort), enabled = editable, onClick = onEffort)
            }
        }
    }
    item("adv-limits") {
        Column(Modifier.padding(top = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            val number = KeyboardOptions(keyboardType = KeyboardType.Number)
            if (claude) {
                JTextField(d.compactWindow, { onEdit(d.copy(compactWindow = it.filter(Char::isDigit))) }, Modifier.fillMaxWidth(),
                    label = stringResource(R.string.settings_pedit_compact_window), placeholder = stringResource(R.string.settings_pedit_by_model), singleLine = true,
                    enabled = editable, keyboardOptions = number)
                JTextField(d.maxContext, { onEdit(d.copy(maxContext = it.filter(Char::isDigit))) }, Modifier.fillMaxWidth(),
                    label = stringResource(R.string.settings_pedit_max_context), placeholder = stringResource(R.string.settings_pedit_by_model), singleLine = true,
                    enabled = editable, keyboardOptions = number)
            }
            JTextField(d.timeoutMs, { onEdit(d.copy(timeoutMs = it.filter(Char::isDigit))) }, Modifier.fillMaxWidth(),
                label = stringResource(R.string.settings_pedit_timeout), placeholder = stringResource(R.string.settings_pedit_timeout_default), singleLine = true,
                enabled = editable, keyboardOptions = number)
        }
    }
    item("adv-proxy") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_pedit_proxy), value = proxySummary(d), enabled = editable, onClick = onProxy)
        }
        d.proxyProblem()?.let { JInlineStatus(it.text(), Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed) }
    }
    item("adv-headers") {
        SecretLines(d.headers, { onEdit(d.copy(headers = it)) }, editable,
            label = stringResource(R.string.settings_pedit_headers),
            placeholder = stringResource(if (stored?.hasHeaders == true && !d.clearHeaders) R.string.settings_pedit_headers_stored else R.string.settings_pedit_headers_hint),
            stored = stored?.hasHeaders == true, cleared = d.clearHeaders, onClear = { onEdit(d.copy(clearHeaders = it, headers = if (it) "" else d.headers)) },
            clearLabel = stringResource(R.string.settings_pedit_headers_clear), clearedNote = stringResource(R.string.settings_pedit_headers_cleared),
            note = null, problem = d.headers.takeIf { it.isNotBlank() }?.let(::headersProblem)?.text())
    }
    item("adv-env") {
        SecretLines(d.env, { onEdit(d.copy(env = it)) }, editable,
            label = stringResource(R.string.settings_pedit_env),
            placeholder = stringResource(if (stored?.hasEnv == true && !d.clearEnv) R.string.settings_pedit_env_stored else R.string.settings_pedit_env_hint),
            stored = stored?.hasEnv == true, cleared = d.clearEnv, onClear = { onEdit(d.copy(clearEnv = it, env = if (it) "" else d.env)) },
            clearLabel = stringResource(R.string.settings_pedit_env_clear), clearedNote = stringResource(R.string.settings_pedit_env_cleared),
            note = stringResource(R.string.settings_pedit_env_note), problem = d.env.takeIf { it.isNotBlank() }?.let(::envProblem)?.text())
    }
}

/**
 * Headers or the environment: lines typed in, hidden while typed (they hold credentials). A stored value is never
 * shown: its placeholder says it is set, and "Clear saved" (undone with "Undo") removes it when saving.
 */
@Composable
private fun SecretLines(
    value: String, onValue: (String) -> Unit, editable: Boolean, label: String, placeholder: String, stored: Boolean, cleared: Boolean,
    onClear: (Boolean) -> Unit, clearLabel: String, clearedNote: String, note: String?, problem: String?,
) {
    Column(Modifier.padding(top = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JTextField(value, onValue, Modifier.fillMaxWidth(), label = label, placeholder = placeholder, minLines = 2, maxLines = 5,
            enabled = editable && !cleared, isError = problem != null, supportingText = problem ?: note)
        when {
            cleared -> Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(clearedNote, Modifier.weight(1f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.cautionText)
                JTextButton(stringResource(R.string.settings_pscope_undo), { onClear(false) }, enabled = editable)
            }
            stored && value.isBlank() -> JTextButton(clearLabel, { onClear(true) }, enabled = editable, icon = PhIcons.Trash)
        }
    }
}

/** The proxy's page, as iOS Settings' Configure Proxy: Follow this computer, No proxy or Manual (its server), and the addresses that go direct. */
@Composable
internal fun ProviderProxyPage(draft: NewProviderDraft, editable: Boolean, onEdit: (NewProviderDraft) -> Unit, onBack: () -> Unit) {
    androidx.activity.compose.BackHandler(onBack = onBack)
    JListPage(title = stringResource(R.string.settings_pedit_proxy), onBack = onBack, largeTitle = false) {
        val modes = ProxyMode.entries
        modes.forEachIndexed { index, mode ->
            item("mode-$mode") {
                JGroupItem(GroupPosition.of(index, modes.size), if (index == 0) Modifier.padding(top = Tokens.Space.m) else Modifier) {
                    JRow(stringResource(when (mode) {
                        ProxyMode.Follow -> R.string.settings_pedit_proxy_follow
                        ProxyMode.Direct -> R.string.settings_pedit_proxy_direct
                        ProxyMode.Manual -> R.string.settings_pedit_proxy_manual
                    }), selected = draft.proxyMode == mode, enabled = editable, onClick = { onEdit(draft.copy(proxyMode = mode)) },
                        trailing = if (draft.proxyMode == mode) ({ JIcon(PhIcons.Check, stringResource(R.string.settings_src_provider_selected), tint = PersonaTheme.palette.accent) }) else ({}))
                }
            }
        }
        item("mode-note") {
            SettingsCaption(stringResource(when (draft.proxyMode) {
                ProxyMode.Follow -> R.string.settings_pedit_proxy_follow_note
                ProxyMode.Direct -> R.string.settings_pedit_proxy_direct_note
                ProxyMode.Manual -> R.string.settings_pedit_proxy_manual_note
            }), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
        }
        if (draft.proxyMode == ProxyMode.Manual) item("server") {
            val problem = draft.proxyProblem()
            JTextField(draft.proxy, { onEdit(draft.copy(proxy = it)) }, Modifier.fillMaxWidth().padding(top = Tokens.Space.m),
                label = stringResource(R.string.settings_pedit_proxy_server), placeholder = "http://127.0.0.1:7890", singleLine = true, enabled = editable,
                isError = problem != null && draft.proxy.isNotBlank(), supportingText = problem?.takeIf { draft.proxy.isNotBlank() }?.text(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri))
        }
        if (draft.proxyMode != ProxyMode.Direct) item("bypass") {
            JTextField(draft.noProxy, { onEdit(draft.copy(noProxy = it)) }, Modifier.fillMaxWidth().padding(top = Tokens.Space.m),
                label = stringResource(R.string.settings_pedit_no_proxy), placeholder = "localhost, 127.0.0.1, .example.com", singleLine = true, enabled = editable,
                supportingText = stringResource(if (draft.proxyMode == ProxyMode.Follow) R.string.settings_pedit_no_proxy_follow_note else R.string.settings_pedit_no_proxy_manual_note),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri))
        }
    }
}

/**
 * "Computers" (docs/integrations/launchers.md "Device scope and on-demand delivery", the web's ProviderScope): where it runs
 * (all my computers, chosen ones, none), whether each computer keeps a copy of the key, whether it also runs where Claude
 * Code / Codex has a service of its own, and keys for single computers (never shown; a stored one can be removed and
 * the removal undone). [old] is the stored provider (its key_machines, legacy_scope), null for a new one. [save]: the
 * page was opened by itself (from the provider's page) and saves on its own.
 */
@Composable
internal fun ProviderComputersPage(
    scope: ScopeForm, onChange: (ScopeForm) -> Unit, machines: List<Machine>, old: HubLauncher?, kind: String, agent: String?,
    defaultUse: Boolean, onBack: () -> Unit, editable: Boolean = true,
    save: (() -> Unit)? = null, saving: Boolean = false, canSave: Boolean = false, error: String? = null,
    referencesAvailable: Boolean = false,
) {
    val name = machineName(machines)
    var keysOpen by rememberSaveable { mutableStateOf(false) }
    JListPage(title = stringResource(R.string.settings_pscope_title), onBack = onBack, largeTitle = false, bottomBar = if (save == null) null else ({
        Row(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.s)) {
            JButton(stringResource(R.string.settings_src_save), save, Modifier.fillMaxWidth(), loading = saving, enabled = canSave)
        }
    })) {
        if (old?.legacyScope == true) item("legacy") {
            JNotice(stringResource(R.string.settings_pscope_legacy_note), Modifier.padding(top = Tokens.Space.m), icon = PhIcons.Info)
        }
        item("share-header") { JSectionHeader(stringResource(R.string.settings_pscope_share), Modifier.padding(top = Tokens.Space.m)) }
        val shares = listOf(
            Triple(Share.All, R.string.settings_pscope_share_all, R.string.settings_pscope_share_all_hint),
            Triple(Share.Selected, R.string.settings_pscope_share_selected, R.string.settings_pscope_share_selected_hint),
            Triple(Share.None, R.string.settings_pscope_share_none, R.string.settings_pscope_share_none_hint),
        )
        shares.forEachIndexed { index, (value, title, hint) ->
            item("share-$value") {
                JGroupItem(GroupPosition.of(index, shares.size)) {
                    JRow(stringResource(title), subtitle = stringResource(hint), subtitleLines = 3, selected = scope.share == value, enabled = editable,
                        onClick = { onChange(scope.copy(share = value)) },
                        trailing = if (scope.share == value) ({ JIcon(PhIcons.Check, stringResource(R.string.settings_src_provider_selected), tint = PersonaTheme.palette.accent) }) else ({}))
                }
            }
        }
        if (kind == "api" && scope.share != Share.None) item("api-hint") {
            SettingsCaption(stringResource(R.string.settings_pscope_api_hint), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
        }
        if (scope.share == Share.Selected) {
            val chosen = choosable(machines, scope.machines)
            item("machines-header") { JSectionHeader(stringResource(R.string.settings_pscope_machines), Modifier.padding(top = Tokens.Space.m), count = scope.machines.size) }
            if (chosen.isEmpty()) item("machines-none") {
                SettingsCaption(stringResource(R.string.settings_pscope_no_machines), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
            }
            checkRows("machine", chosen, scope.machines, name, editable) { onChange(scope.toggle(it)) }
            scope.problem?.let { problem -> item("machines-problem") { JInlineStatus(problem.text(), Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed) } }
        }
        val work = if (scope.share == Share.All) addableWork(machines, scope.work) else emptyList()
        if (work.isNotEmpty()) {
            item("work-header") { JSectionHeader(stringResource(R.string.settings_pscope_work), Modifier.padding(top = Tokens.Space.m), count = scope.work.size) }
            checkRows("work", work, scope.work, name, editable) { onChange(scope.toggleWork(it)) }
            item("work-hint") { SettingsCaption(stringResource(R.string.settings_pscope_work_hint), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
        }
        if (scope.share != Share.None) {
            val own = ownServiceDetected(kind, agent)
            item("switches") {
                Column(Modifier.padding(top = Tokens.Space.m)) {
                    // Each switch on its own with its hint in full under it: the hints are longer than a row's two-line subtitle.
                    JGroupItem(GroupPosition.Single) {
                        JSwitchRow(stringResource(R.string.settings_pscope_push), scope.delivery == Delivery.Push,
                            { onChange(scope.copy(delivery = if (it) Delivery.Push else Delivery.OnDemand)) }, enabled = editable)
                    }
                    SettingsCaption(stringResource(R.string.settings_pscope_push_hint), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
                    if (own) {
                        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
                            JSwitchRow(stringResource(R.string.settings_pscope_use_own), scope.ownServiceOf(defaultUse) == OwnService.Use,
                                { onChange(scope.copy(ownService = if (it) OwnService.Use else OwnService.Skip)) }, enabled = editable)
                        }
                        SettingsCaption(stringResource(R.string.settings_pscope_use_own_hint), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
                    }
                }
            }
        }
        val stored = (old?.keyMachines.orEmpty() + old?.machineKeyReferences.orEmpty().keys).distinct()
        val keyed = (keyMachines(scope, machines) + stored).distinct()
        if (keyed.isNotEmpty()) {
            val count = scope.keyCount(stored)
            item("keys-header") {
                JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                    JRow(stringResource(R.string.settings_pscope_machine_keys), leadingIcon = PhIcons.Key,
                        value = if (keysOpen) null else if (count > 0) pluralStringResource(R.plurals.settings_pscope_machine_keys_count, count, count)
                            else stringResource(R.string.settings_pscope_machine_keys_badge),
                        trailing = { JIcon(if (keysOpen) PhIcons.CaretUp else PhIcons.CaretDown, null, decorative = true, tint = PersonaTheme.palette.ink3) },
                        onClick = { keysOpen = !keysOpen })
                }
            }
            if (keysOpen) {
                item("keys-hint") { SettingsCaption(stringResource(R.string.settings_pscope_machine_keys_hint), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
                keyed.forEach { id ->
                    item("key-$id") { MachineKeyField(id, name(id), scope, id in stored, editable, onChange,
                        old?.machineKeyReferences?.get(id), referencesAvailable) }
                }
            }
        }
        error?.let { item("error") { JInlineStatus(it, Modifier.padding(top = Tokens.Space.m), tone = InlineTone.Failed) } }
    }
}

private fun LazyListScope.checkRows(key: String, ids: List<String>, checked: List<String>, name: (String) -> String, editable: Boolean, onToggle: (String) -> Unit) {
    ids.forEachIndexed { index, id ->
        item("$key-$id") {
            JGroupItem(GroupPosition.of(index, ids.size)) {
                JRow(name(id), leadingIcon = PhIcons.Laptop, chevron = false, enabled = editable, role = Role.Checkbox, onClick = { onToggle(id) },
                    trailing = { JCheckbox(id in checked, null) })
            }
        }
    }
}

/** One computer's own key or reference; stored metadata is visible, its resolved value never is. */
@Composable
private fun MachineKeyField(
    id: String, machine: String, scope: ScopeForm, has: Boolean, editable: Boolean, onChange: (ScopeForm) -> Unit,
    storedReference: persona.core.contracts.api.SecretReference?, referencesAvailable: Boolean,
) {
    val removed = id in scope.remove
    val typed = scope.keys[id].orEmpty()
    val byReference = id in scope.keyRefs
    Column(Modifier.padding(top = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        ProviderKeyField(typed,
            { onChange(scope.typeKey(id, it).let { s -> if (it.isNotBlank()) s.keepKey(id) else s }) },
            byReference,
            onByReferenceChange = if (referencesAvailable) ({ on -> onChange(scope.referenceKey(id, on)) }) else null,
            stored = storedReference,
            label = stringResource(R.string.settings_pscope_key_for, machine), modifier = Modifier.fillMaxWidth(),
            enabled = editable && (referencesAvailable || !byReference),
            placeholder = stringResource(if (has && !removed) R.string.settings_pscope_replace_key else R.string.settings_pscope_uses_provider_key),
            supportingText = if (byReference && typed.isNotBlank()) stringResource(R.string.settings_provider_reference_saved) else null)
        if (has && typed.isBlank()) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(stringResource(if (removed) R.string.settings_pscope_key_removed else R.string.settings_pscope_has_own_key), Modifier.weight(1f),
                style = PersonaTheme.type.caption, color = if (removed) PersonaTheme.palette.cautionText else PersonaTheme.palette.ink2,
                maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (removed) JTextButton(stringResource(R.string.settings_pscope_undo), { onChange(scope.keepKey(id)) }, enabled = editable)
            else JTextButton(stringResource(R.string.settings_pscope_remove_key), { onChange(scope.removeKey(id)) }, enabled = editable, icon = PhIcons.Trash)
        }
    }
}

/** "Discard changes?" before leaving an edit with something changed. */
@Composable
internal fun DiscardChangesDialog(onDiscard: () -> Unit, onKeep: () -> Unit) {
    JDialog(onDismiss = onKeep, title = stringResource(R.string.settings_pedit_discard_title), text = stringResource(R.string.settings_pedit_discard_text),
        destructive = true, confirm = stringResource(R.string.settings_pedit_discard) to onDiscard,
        dismiss = stringResource(R.string.settings_pedit_keep_editing) to onKeep)
}
