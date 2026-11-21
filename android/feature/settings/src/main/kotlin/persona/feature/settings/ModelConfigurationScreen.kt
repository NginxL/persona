package persona.feature.settings

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import android.content.res.Resources
import persona.core.contracts.Machine
import persona.core.contracts.api.ModelConfiguration
import persona.core.contracts.api.ModelPurpose
import persona.core.contracts.api.ProviderSetup
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.LocalDetailKey
import persona.core.navigation.ModelPurposeRoute
import kotlinx.coroutines.launch

/**
 * Settings › Model configuration (docs/assistant/model-config.md): each AI use of the hub with what is in effect for it. The hub owns the
 * catalog; a row opens that use's page. The current assistant's (docs/assistant/managers.md "Clients"): another one current, the
 * page reads that one's.
 */
@Composable
internal fun ModelConfigurationPage(
    models: ModelConfiguration?, onBack: () -> Unit, onPurpose: (String, String) -> Unit,
    providers: ProviderSetup? = null, onProviders: (() -> Unit)? = null,
    machines: List<Machine> = emptyList(),
) {
    val owner = currentAssistant().id
    val default = LocalManagerRoster.current?.defaultManagerId
    key(models, owner) {
        ModelConfigurationDetails(models, onBack, { onPurpose(it, owner) },
            remember(providers, owner, default) { providers?.namingOwner(owner, default) }, owner, onProviders, machines)
    }
}

@Composable
private fun ModelConfigurationDetails(models: ModelConfiguration?, onBack: () -> Unit, onPurpose: (String) -> Unit,
    providers: ProviderSetup?, owner: String, onProviders: (() -> Unit)?, machines: List<Machine>) {
    if (models == null) {
        JListPage(title = stringResource(R.string.settings_home_models), onBack = onBack) {
            item { JNotice(stringResource(R.string.settings_src_model_demo)) }
            onProviders?.let { open ->
                item { JSectionHeader(stringResource(R.string.settings_pedit_advanced)) }
                item { JGroupItem(GroupPosition.Single) { JRow(stringResource(R.string.settings_providers_title), onClick = open) } }
            }
        }
        return
    }
    val default = LocalManagerRoster.current?.defaultManagerId
    val scopedModels = remember(models, owner, default) { models.namingOwner(owner, default) }
    val model = remember(scopedModels) { ModelCatalogModel(scopedModels) }
    val profilesModel = remember(scopedModels) { ModelProfilesModel(scopedModels) }
    val state by model.state.collectAsState()
    val profilesState by profilesModel.state.collectAsState()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    var query by remember { mutableStateOf("") }
    var advanced by rememberSaveable { mutableStateOf(false) }
    var selectedProfileId by rememberSaveable(owner) { mutableStateOf<String?>(null) }
    var editingProfile by rememberSaveable(owner) { mutableStateOf(false) }
    var editRevision by rememberSaveable(owner) { mutableLongStateOf(0L) }
    // "New model provider" opens over this page (`debug.newProvider` opens it for captures).
    var adding by remember { mutableStateOf<String?>(if (providers != null) NewProviderPreview.take() else null) }
    LaunchedEffect(model, profilesModel) {
        launch { model.load() }
        launch { profilesModel.load() }
    }
    if (adding != null && providers != null) {
        NewProviderPage(providers, onBack = { adding = null }, onSaved = { adding = null; scope.launch { model.load() } },
            preferPreset = adding == "preset", manager = owner, machines = machines)
        return
    }
    val selectedProfile = selectedProfileId?.let { id -> profilesState.view?.profiles?.firstOrNull { it.id == id } }
    if (selectedProfile != null) {
        if (editingProfile) {
            ModelProfileEditorPage(
                profile = selectedProfile,
                revision = editRevision,
                catalog = state.catalog,
                busy = profilesState.busy,
                error = profilesState.actionError ?: profilesState.error,
                conflict = profilesState.conflict,
                onSave = { input ->
                    scope.launch {
                        if (profilesModel.save(selectedProfile.id, input)) {
                            editRevision = profilesModel.state.value.view?.revision ?: editRevision
                            editingProfile = false
                            feedback?.notice(res.getString(R.string.settings_model_profiles_saved))
                            model.load()
                        }
                    }
                },
                onReload = {
                    scope.launch {
                        profilesModel.clearErrors()
                        val fresh = profilesModel.load()
                        if (fresh != null) editRevision = fresh.revision
                    }
                },
                onBack = {
                    if (!profilesState.busy) {
                        editingProfile = false
                        profilesModel.clearErrors()
                    }
                },
            )
        } else {
            ModelProfileDetailPage(
                profile = selectedProfile,
                catalog = state.catalog,
                busy = profilesState.busy,
                error = profilesState.actionError ?: profilesState.error,
                onBack = {
                    if (!profilesState.busy) {
                        selectedProfileId = null
                        editingProfile = false
                        profilesModel.clearErrors()
                    }
                },
                onEdit = {
                    profilesModel.clearErrors()
                    editRevision = profilesState.view?.revision ?: 0L
                    editingProfile = true
                },
                onApply = {
                    scope.launch {
                        if (profilesModel.apply(selectedProfile.id)) {
                            selectedProfileId = null
                            editingProfile = false
                            feedback?.notice(res.getString(R.string.settings_model_profiles_applied, selectedProfile.displayName))
                            model.load()
                        }
                    }
                },
            )
        }
        return
    }
    val catalog = state.catalog
    val groups = catalog?.let { modelPurposeGroups(res, it.purposes, it.providers, query) }.orEmpty()
    val detail = LocalDetailKey.current
    val refreshing = (state.loading || profilesState.loading) && (catalog != null || profilesState.view != null)
    fun refreshAll() {
        scope.launch {
            launch { model.load() }
            launch { profilesModel.load() }
        }
    }
    JListPage(title = stringResource(R.string.settings_src_model_title), onBack = onBack, refreshing = refreshing, onRefresh = ::refreshAll,
        actions = {
            JIconButton(PersonaIcons.retry, stringResource(R.string.settings_src_refresh), ::refreshAll, enabled = !state.loading && !profilesState.loading)
        }) {
        item("manager") { ModelConfigurationOwner(owner) }
        item("search") {
            JSearchField(query, { query = it }, stringResource(R.string.settings_src_model_search), Modifier.padding(top = Tokens.Space.s))
        }
        modelProfilesSection(
            res = res,
            state = profilesState,
            query = query,
            onSelect = { profile ->
                profilesModel.clearErrors()
                selectedProfileId = profile.id
                editingProfile = false
            },
            onRollback = {
                scope.launch {
                    if (profilesModel.rollback()) {
                        feedback?.notice(res.getString(R.string.settings_model_profiles_rolled_back))
                        model.load()
                    }
                }
            },
            onRetry = { scope.launch { profilesModel.load() } },
        )
        when {
            state.unsupported -> item("unsupported") {
                JNotice(stringResource(R.string.settings_src_model_unsupported), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou)
            }
            state.error != null -> item("error") {
                JNotice(state.error?.text().orEmpty(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou,
                    actionLabel = stringResource(R.string.settings_src_retry), onAction = { scope.launch { model.load() } })
            }
        }
        if (catalog == null && state.loading) item("loading") { JLoading() }
        if (catalog != null && groups.isEmpty()) item("empty") {
            JEmptyState(stringResource(if (query.isBlank()) R.string.settings_src_model_none else R.string.settings_src_model_no_match), stringResource(R.string.settings_src_model_no_match_hint), icon = PersonaIcons.search)
        }
        groups.forEach { (group, purposes) ->
            item("group-$group") { JSectionHeader(group, count = purposes.size) }
            itemsIndexed(purposes, key = { _, it -> "purpose-${it.id}" }) { index, purpose ->
                JGroupItem(GroupPosition.of(index, purposes.size)) {
                    JRow(purpose.displayName, subtitle = purposeSubtitle(res, purpose, requireNotNull(catalog)), subtitleLines = purposeSubtitleLines(purpose),
                        selected = detail == ModelPurposeRoute(purpose.id, owner), onClick = { onPurpose(purpose.id) })
                }
            }
        }
        item("advanced") {
            JSectionHeader(stringResource(R.string.settings_pedit_advanced), Modifier.heightIn(min = Tokens.MinTouch.android),
                leading = { JIcon(if (advanced) PersonaIcons.collapse else PersonaIcons.expand, null, decorative = true) },
                open = advanced, onClick = { advanced = !advanced })
        }
        if (advanced) {
            val entries = listOfNotNull(
                onProviders?.let { R.string.settings_providers_title to it },
                providers?.let { R.string.settings_model_add_provider to { adding = "manual" } },
            )
            itemsIndexed(entries, key = { _, entry -> "advanced-${entry.first}" }) { index, (title, action) ->
                JGroupItem(GroupPosition.of(index, entries.size)) {
                    JRow(stringResource(title), onClick = action)
                }
            }
            item("how-models-run") {
                JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                    JRow(stringResource(R.string.settings_model_how_runs),
                        subtitle = stringResource(R.string.settings_model_how_runs_detail), subtitleLines = Int.MAX_VALUE,
                        leadingIcon = PersonaIcons.run, chevron = false)
                }
            }
            item("advanced-note") {
                SettingsCaption(stringResource(R.string.settings_src_model_caption),
                    Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
            }
        }
    }
}

/** One AI use: what is in effect, and its own provider, model and effort (blank: inherit). */
@Composable
fun ModelPurposePage(models: ModelConfiguration?, purposeId: String, onBack: () -> Unit, providers: ProviderSetup? = null, manager: String? = null,
    machines: List<Machine> = emptyList()) {
    val owner = manager?.takeIf(String::isNotBlank) ?: LocalManagerRoster.current?.defaultManagerId
        ?: persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER
    val default = LocalManagerRoster.current?.defaultManagerId
    key(models, purposeId, owner) {
        ModelPurposeDetails(models, purposeId, onBack, remember(providers, owner, default) { providers?.namingOwner(owner, default) }, owner, machines)
    }
}

@Composable
private fun ModelPurposeDetails(models: ModelConfiguration?, purposeId: String, onBack: () -> Unit, providers: ProviderSetup?, owner: String,
    machines: List<Machine>) {
    if (models == null) {
        JListPage(title = stringResource(R.string.settings_src_model_title), onBack = onBack) { item { JNotice(stringResource(R.string.settings_src_model_demo)) } }
        return
    }
    val default = LocalManagerRoster.current?.defaultManagerId
    val model = remember(models, purposeId, owner, default) { ModelPurposeModel(models.namingOwner(owner, default), purposeId) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    var choosingProvider by remember { mutableStateOf(false) }
    var choosingEffort by remember { mutableStateOf(false) }
    var choosingPermissionMode by remember { mutableStateOf(false) }
    var pickingModel by remember { mutableStateOf(false) }
    var adding by remember { mutableStateOf(false) }
    var requestedOpen by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(model) { model.load() }
    LaunchedEffect(model, state.listProvider) { model.loadModels() }
    if (adding && providers != null) {
        // A provider added from here is picked for this use (the edit is kept; saving is still the user's).
        NewProviderPage(providers, onBack = { adding = false }, onSaved = { id -> adding = false; scope.launch { model.providerAdded(id) } }, manager = owner,
            machines = machines)
        return
    }
    fun save(restore: Boolean) {
        scope.launch { model.save(restore)?.let { notice -> feedback?.notice(notice.resolve(res)); onBack() } }
    }
    val purpose = state.purpose
    JListPage(title = purpose?.displayName ?: stringResource(R.string.settings_src_model_title), onBack = onBack, largeTitle = false, bottomBar = if (purpose == null) null else ({
        Row(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.s)) {
            JButton(stringResource(R.string.settings_src_save), { save(false) }, Modifier.fillMaxWidth(), loading = state.busy,
                enabled = !state.busy && !state.conflict && !(state.candidateChanged && state.candidateProblem != null))
        }
    })) {
        item("manager") { ModelConfigurationOwner(owner) }
        if (purpose == null && state.busy) item("loading") { JLoading() }
        // what the purpose is for, under its name (#5086); a task's, an extension's and an older hub's have none
        purpose?.about?.let { about ->
            item("about") { SettingsCaption(about, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s).testTag("model-purpose-about")) }
        }
        if (purpose != null) {
            effective(res, purpose, state)
            extensionCandidates(state, requestedOpen, onRequested = { requestedOpen = !requestedOpen },
                onEdit = { model.edit(state.draft.copy(candidates = it)) })
            form(res, purpose, state, onProvider = { choosingProvider = true }, onEffort = { choosingEffort = true },
                onPermissionMode = { choosingPermissionMode = true }, onEdit = model::edit, onRestore = { save(true) },
                onPickModel = { pickingModel = true }, onRetryModels = { scope.launch { model.loadModels(refresh = true) } })
        }
        state.error?.let { error ->
            item("error") {
                Column(Modifier.padding(top = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JInlineStatus(error.text(), tone = InlineTone.Failed)
                    if (state.conflict || purpose == null) JButton(stringResource(if (state.conflict) R.string.settings_src_model_reload else R.string.settings_src_retry),
                        { scope.launch { model.load() } }, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary, enabled = !state.busy)
                }
            }
        }
    }
    val modelList = state.modelList
    if (pickingModel && modelList != null && modelList.locked == null) {
        val provider = state.provider?.displayName.orEmpty()
        val machine = modelList.machine.ifBlank { null }
        val inherit = state.draft.provider.isEmpty()
        JModelSheet(
            note = when {
                machine == null && inherit -> stringResource(R.string.settings_src_model_sheet_note_inherit, provider)
                machine == null -> stringResource(R.string.settings_src_model_sheet_note_default, provider)
                inherit -> stringResource(R.string.settings_src_model_sheet_note_inherit_from, provider, machine)
                else -> stringResource(R.string.settings_src_model_sheet_note_default_from, provider, machine)
            },
            models = modelList.models, selected = state.draft.model, defaultDetail = state.defaultModelDetail,
            loading = state.models.loading, error = state.models.error?.text(),
            onPick = { model.edit(state.draft.copy(model = it)) }, onRefresh = { scope.launch { model.loadModels(refresh = true) } },
            onDismiss = { pickingModel = false }, custom = true,
        )
    }
    if (choosingProvider && purpose != null) JActionSheet(providerOptions(res, purpose, state.detail?.providers.orEmpty(), state.draft.provider).map { option ->
        JAction(option.label, checked = option.id == state.draft.provider, onClick = { model.edit(state.draft.choosingProvider(option.id)) })
    } + listOfNotNull(providers?.let {
        JAction(stringResource(R.string.settings_src_provider_new_more), icon = PhIcons.Plus, separatorBefore = true, onClick = { adding = true })
    }), { choosingProvider = false }, title = stringResource(R.string.settings_src_model_provider))
    if (choosingEffort && purpose != null) {
        val efforts = ModelProfileRules.EFFORT_LEVELS
        val current = state.draft.effort
        val actions = listOf(
            JAction(stringResource(R.string.settings_src_model_follow_default), checked = current.isEmpty(),
                onClick = { model.edit(state.draft.copy(effort = "")) })
        ) + efforts.map { level ->
            JAction(effortDisplayLabel(res, level), checked = current == level,
                onClick = { model.edit(state.draft.copy(effort = level)) })
        } + listOfNotNull(
            current.takeIf { it.isNotEmpty() && it !in efforts }?.let { custom ->
                JAction(custom, checked = true, onClick = {})
            }
        )
        JActionSheet(actions, { choosingEffort = false }, title = stringResource(R.string.settings_src_model_effort))
    }
    if (choosingPermissionMode && purpose != null) {
        val modes = state.provider?.permissionModes?.takeIf { it.size >= 2 } ?: ModelProfileRules.PERMISSION_MODES
        val current = state.draft.permissionMode
        val actions = listOf(
            JAction(stringResource(R.string.settings_src_model_follow_default), checked = current.isEmpty(),
                onClick = { model.edit(state.draft.copy(permissionMode = "")) })
        ) + modes.map { mode ->
            JAction(permissionModeDisplayLabel(res, mode), checked = current == mode,
                onClick = { model.edit(state.draft.copy(permissionMode = mode)) })
        } + listOfNotNull(
            current.takeIf { it.isNotEmpty() && it !in modes }?.let { custom ->
                JAction(permissionModeDisplayLabel(res, custom), checked = true, onClick = {})
            }
        )
        JActionSheet(actions, { choosingPermissionMode = false }, title = stringResource(R.string.settings_model_profiles_permission_mode))
    }
}

private fun androidx.compose.foundation.lazy.LazyListScope.effective(res: Resources, purpose: ModelPurpose, state: ModelPurposeState) {
    val providers = state.detail?.providers.orEmpty()
    val rows = listOfNotNull(
        R.string.settings_src_model_effective_provider to (purpose.effective.provider?.let { id -> providers.firstOrNull { it.id == id }?.displayName ?: id }
            ?: res.getString(R.string.settings_src_state_not_configured)),
        R.string.settings_src_model_effective_model to (purpose.effective.model?.takeIf { it.isNotBlank() } ?: res.getString(R.string.settings_src_model_provider_default)),
        purpose.effective.effort?.takeIf { it.isNotBlank() }?.let { R.string.settings_src_model_effort to effortDisplayLabel(res, it) },
        purpose.effective.permissionMode?.takeIf { it.isNotBlank() }?.let { R.string.settings_model_profiles_permission_mode to permissionModeDisplayLabel(res, it) },
        purpose.status?.takeIf { it.isNotBlank() }?.let { R.string.settings_src_status_header to it },
    )
    item("effective-title") { JSectionHeader(stringResource(R.string.settings_src_model_effective)) }
    itemsIndexed(rows, key = { _, it -> "effective-${it.first}" }) { index, (label, value) ->
        JGroupItem(GroupPosition.of(index, rows.size)) { JRow(stringResource(label), value = value) }
    }
    agentsOverrideMode(res, purpose)?.let { mode ->
        item("agents-override-mode") { SettingsCaption(mode, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
    }
    purpose.inheritedFrom?.takeIf { it.isNotBlank() }?.let { from ->
        item("effective-note") { SettingsCaption(stringResource(R.string.settings_src_model_inherited_from, from), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
    }
}

private fun androidx.compose.foundation.lazy.LazyListScope.form(
    res: Resources, purpose: ModelPurpose, state: ModelPurposeState, onProvider: () -> Unit,
    onEffort: () -> Unit, onPermissionMode: () -> Unit,
    onEdit: (persona.core.contracts.api.ModelConfigurationDraft) -> Unit, onRestore: () -> Unit,
    onPickModel: () -> Unit, onRetryModels: () -> Unit,
) {
    val draft = state.draft
    val editable = !state.busy
    item("form-title") { JSectionHeader(stringResource(R.string.settings_src_model_form)) }
    if (purpose.supports("provider")) item("provider") {
        val label = providerOptions(res, purpose, state.detail?.providers.orEmpty(), draft.provider).firstOrNull { it.id == draft.provider }?.label
        JGroupItem(GroupPosition.Single) { JRow(stringResource(R.string.settings_src_model_provider), value = label ?: stringResource(R.string.settings_src_model_follow_default), enabled = editable, onClick = onProvider) }
    }
    if (purpose.supports("model")) item("model") {
        if (state.picksModel) {
            // A launcher's /model list (#943): the pick, "Default", or an id the list does not have.
            val list = state.modelList
            val picked = list?.models?.firstOrNull { it.id == draft.model }
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                if (list == null) JRow(stringResource(R.string.settings_src_model_model), subtitle = stringResource(R.string.settings_src_model_list_loading, state.provider?.displayName.orEmpty()), chevron = false,
                    trailing = { JSpinner() })
                // The launcher passes its own --model: a pick would not reach it (saving one fails the hub's check).
                else if (list.locked != null) JRow(stringResource(R.string.settings_src_model_model), value = draft.model.ifEmpty { stringResource(R.string.settings_src_default) }, subtitle = list.locked, subtitleLines = 3,
                    enabled = false, chevron = false, trailing = { JIcon(PhIcons.LockSimple, stringResource(R.string.settings_src_model_locked), tint = PersonaTheme.palette.ink3) })
                else JRow(stringResource(R.string.settings_src_model_model), value = picked?.label ?: draft.model.ifEmpty { stringResource(R.string.settings_src_default) },
                    subtitle = when {
                        picked != null -> picked.detailLine()
                        draft.model.isNotEmpty() -> stringResource(R.string.settings_src_model_not_listed)
                        else -> state.defaultModelDetail
                    }, enabled = editable, onClick = onPickModel, trailing = if (state.models.loading) ({ JSpinner() }) else null)
            }
        } else {
            JTextField(draft.model, { onEdit(draft.copy(model = it)) }, Modifier.fillMaxWidth().padding(top = Tokens.Space.m), label = stringResource(R.string.settings_src_model_model),
                placeholder = (if (purpose.kind == "plugin") stringResource(R.string.settings_src_model_follow_extension) else null) ?: state.provider?.model ?: purpose.effective.model
                    ?: stringResource(R.string.settings_src_model_follow_default),
                singleLine = true, enabled = editable)
            state.models.error?.takeIf { state.models.provider == state.listProvider && state.listProvider.isNotEmpty() }?.let { error ->
                Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                    JInlineStatus(stringResource(R.string.settings_src_model_list_failed, error.text()), Modifier.weight(1f), tone = InlineTone.Failed)
                    JButton(stringResource(R.string.settings_src_retry), onRetryModels, enabled = editable, kind = JButtonKind.Secondary, size = JButtonSize.Small)
                }
            }
        }
    }
    if (showsEffort(purpose, state.provider)) item("effort") {
        val effortLabel = draft.effort.takeIf(String::isNotBlank)?.let { effortDisplayLabel(res, it) }
            ?: stringResource(R.string.settings_src_model_follow_default)
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_src_model_effort), value = effortLabel, enabled = editable, onClick = onEffort)
        }
    }
    if (purpose.kind == "launcher" && purpose.supports("permission_mode")) item("permission-mode") {
        val modeLabel = draft.permissionMode.takeIf(String::isNotBlank)?.let { permissionModeDisplayLabel(res, it) }
            ?: stringResource(R.string.settings_src_model_follow_default)
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_model_profiles_permission_mode), value = modeLabel, enabled = editable, onClick = onPermissionMode)
        }
    }
    item("form-note") {
        SettingsCaption(stringResource(R.string.settings_src_model_form_note),
            Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m))
    }
    if (purpose.kind == "plugin") item("plugin") { JNotice(stringResource(R.string.settings_src_model_plugin_note), title = stringResource(R.string.settings_src_model_plugin_title)) }
    item("restore") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_src_model_restore), leadingIcon = PhIcons.ArrowUUpLeft,
                enabled = editable && !state.conflict && !state.candidateChanged, chevron = false, onClick = onRestore)
        }
    }
}

@Composable
internal fun ModelConfigurationOwner(owner: String) {
    val name = LocalManagerRoster.current?.manager(owner)?.shownName ?: owner
    JGroupItem(GroupPosition.Single) {
        JRow(stringResource(R.string.settings_model_assistant), value = name, chevron = false)
    }
}

/**
 * [this] naming [owner] on its requests; the default assistant's, and any on a hub without a roster ([default] null), name
 * none, the requests the hub always got (docs/assistant/managers.md "Clients").
 */
internal fun ModelConfiguration.namingOwner(owner: String, default: String?): ModelConfiguration =
    if (default == null || owner.isBlank() || owner == default) this else forManager(owner)

/** [ModelConfiguration.namingOwner] for model providers. */
internal fun ProviderSetup.namingOwner(owner: String, default: String?): ProviderSetup =
    if (default == null || owner.isBlank() || owner == default) this else forManager(owner)
