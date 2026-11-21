package persona.feature.settings

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.launch
import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.TraeCliDisplayName
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.api.LauncherCheck
import persona.core.contracts.api.LauncherImportResult
import persona.core.contracts.api.ProviderSetup
import persona.core.contracts.isTraeCliId
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

/** The hub speaks its own machine ids; the merged list may carry a group's suffix (GroupKeys). */
internal fun machineLabel(machines: List<Machine>, id: String): String =
    machines.firstOrNull { it.id == id || GroupKeys.raw(it.id) == id }?.displayName?.takeIf { it.isNotBlank() } ?: id

/**
 * Settings › AI › Model providers (iOS `ModelLaunchersView`, docs/integrations/launchers.md): every provider the machines can start, kept by
 * Persona or found in a machine's ~/.zshrc (read again each time this opens), and the hidden ones, folded. A row's
 * menu (long press) hides a ~/.zshrc one or brings it back, without asking: it only changes what Persona lists; a
 * Persona one is deleted, key and all, so that one asks first. + adds a provider or imports from ~/.zshrc.
 */
@Composable
internal fun ProvidersPage(
    providers: ProviderSetup?,
    machines: List<Machine>,
    onBack: () -> Unit,
    onOpen: (String) -> Unit,
    onAdd: () -> Unit,
    onImport: () -> Unit,
    selected: (String) -> Boolean = { false },
) {
    if (providers == null) {
        JListPage(title = stringResource(R.string.settings_providers_title), onBack = onBack) {
            item { JNotice(stringResource(R.string.settings_providers_demo)) }
        }
        return
    }
    val model = remember(providers) { ProvidersModel(providers) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val res = LocalResources.current
    val feedback = LocalAppFeedback.current
    var query by rememberSaveable { mutableStateOf("") }
    var showHidden by rememberSaveable { mutableStateOf(false) }
    var adding by remember { mutableStateOf(false) }
    var menu by remember { mutableStateOf<HubLauncher?>(null) }
    var deleting by remember { mutableStateOf<HubLauncher?>(null) }
    LaunchedEffect(model) { model.load() }
    LaunchedEffect(state.notice) {
        val notice = state.notice ?: return@LaunchedEffect
        if (feedback?.notice(notice.resolve(res)) == true) model.clearNotice()
    }
    val name = { id: String -> machineLabel(machines, id) }
    val groups = state.launchers?.let { ProviderGroups.of(it, query, name) }
    JListPage(title = stringResource(R.string.settings_providers_title), onBack = onBack,
        refreshing = state.loading && state.launchers != null, onRefresh = { scope.launch { model.load() } },
        actions = { JIconButton(PhIcons.Plus, stringResource(R.string.settings_providers_add), { adding = true }) }) {
        item("search") {
            JSearchField(query, { query = it }, stringResource(R.string.settings_providers_search), Modifier.fillMaxWidth().padding(top = Tokens.Space.s))
        }
        if (feedback == null) state.notice?.let { notice -> item("notice") { JNotice(notice.text(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou) } }
        when {
            groups == null && state.error != null -> item("error") {
                JNotice(state.error!!.text(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou,
                    actionLabel = stringResource(R.string.settings_retry_read), onAction = { scope.launch { model.load() } })
            }
            groups == null -> item("loading") { JLoading() }
            query.isNotBlank() && groups.isEmpty -> item("none") {
                JEmptyState(stringResource(R.string.settings_providers_no_match), stringResource(R.string.settings_providers_no_match_detail, query.trim()), icon = PhIcons.MagnifyingGlass)
            }
            else -> {
                if (query.isBlank() || groups.managed.isNotEmpty()) {
                    item("managed") { JSectionHeader(stringResource(R.string.settings_providers_managed), count = groups.managed.size) }
                    if (groups.managed.isEmpty()) item("managed-empty") { SettingsCaption(stringResource(R.string.settings_providers_managed_empty), Modifier.padding(horizontal = Tokens.Space.xs)) }
                    // By harness (Claude Code, Codex, …, direct API) once there is more than one; one alone needs no labels.
                    val harnesses = harnessGroups(groups.managed)
                    if (harnesses.size > 1) harnesses.forEach { (harness, list) ->
                        item("managed-h-$harness") {
                            Text(harnessName(harness) ?: stringResource(R.string.settings_pscope_harness_api),
                                Modifier.padding(start = rowPadding, end = rowPadding, top = Tokens.Space.m, bottom = Tokens.Space.xs),
                                style = PersonaTheme.type.labelSmall, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        launcherRows("managed-$harness", list, name, selected, onOpen) { menu = it }
                    } else launcherRows("managed", groups.managed, name, selected, onOpen) { menu = it }
                    if (groups.managed.isNotEmpty()) item("managed-note") {
                        SettingsCaption(stringResource(R.string.settings_providers_managed_note), Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
                    }
                }
                if (groups.zshrc.isNotEmpty()) {
                    item("zshrc") { JSectionHeader(stringResource(R.string.settings_providers_zshrc), count = groups.zshrc.size) }
                    launcherRows("zshrc", groups.zshrc, name, selected, onOpen) { menu = it }
                    item("zshrc-note") {
                        SettingsCaption(stringResource(R.string.settings_providers_zshrc_note), Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
                    }
                }
                if (groups.hidden.isNotEmpty()) {
                    // Folded at first: they are hidden for a reason. A search shows them anyway.
                    val open = showHidden || query.isNotBlank()
                    item("hidden") {
                        JSectionHeader(stringResource(R.string.settings_providers_hidden), count = groups.hidden.size,
                            trailing = stringResource(if (open) R.string.settings_providers_fold else R.string.settings_providers_unfold),
                            onClick = if (query.isBlank()) ({ showHidden = !showHidden }) else null, open = open, trailingIsFoldAction = true)
                    }
                    if (open) {
                        launcherRows("hidden", groups.hidden, name, selected, onOpen) { menu = it }
                        item("hidden-note") {
                            SettingsCaption(stringResource(R.string.settings_providers_hidden_note), Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
                        }
                    }
                }
            }
        }
    }
    if (adding) JActionSheet(listOf(
        JAction(stringResource(R.string.settings_providers_add), PhIcons.Plus) { onAdd() },
        JAction(stringResource(R.string.settings_providers_import), PhIcons.TerminalWindow) { onImport() },
    ), { adding = false })
    menu?.let { l ->
        JActionSheet(listOf(when {
            l.managed -> JAction(stringResource(R.string.settings_providers_delete), PhIcons.Trash, destructive = true) { deleting = l }
            l.hidden -> JAction(stringResource(R.string.settings_providers_restore), PhIcons.Eye) { scope.launch { model.setHidden(l, false) } }
            else -> JAction(stringResource(R.string.settings_providers_hide), PhIcons.Prohibit) { scope.launch { model.setHidden(l, true) } }
        }), { menu = null }, title = l.settingsTitle)
    }
    deleting?.let { l ->
        JDialog(onDismiss = { deleting = null }, title = stringResource(R.string.settings_providers_delete_title, l.settingsTitle),
            text = stringResource(R.string.settings_providers_delete_text), destructive = true,
            confirm = stringResource(R.string.settings_providers_delete) to { deleting = null; scope.launch { model.delete(l) }; Unit })
    }
}

private fun androidx.compose.foundation.lazy.LazyListScope.launcherRows(
    key: String, list: List<HubLauncher>, machineName: (String) -> String, selected: (String) -> Boolean,
    onOpen: (String) -> Unit, onMenu: (HubLauncher) -> Unit,
) {
    list.forEachIndexed { index, l ->
        item("$key-${l.name}") {
            JGroupItem(GroupPosition.of(index, list.size)) { LauncherRow(l, machineName, selected(l.name), { onOpen(l.name) }, { onMenu(l) }) }
        }
    }
}

/** A provider in the list: its name, the model, where it goes, which machines have it. */
@Composable
private fun LauncherRow(l: HubLauncher, machineName: (String) -> String, selected: Boolean, onClick: () -> Unit, onLongClick: () -> Unit) {
    val palette = PersonaTheme.palette
    val machines = l.machines.joinToString(stringResource(R.string.settings_providers_list_sep), transform = machineName)
    JRow(selected = selected, onClick = onClick, onLongClick = onLongClick, onClickLabel = l.settingsTitle) {
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(l.settingsTitle, Modifier.weight(1f, fill = false), style = PersonaTheme.type.code, color = if (l.hidden) palette.ink3 else palette.ink,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (l.managed) JTag("Persona", status = Status.Working)
                if (l.isApi) JTag(stringResource(R.string.settings_providers_api_tag))
            }
            val detail = listOfNotNull(l.model?.takeIf { it.isNotBlank() }, providerHost(l.baseUrl)).joinToString(" · ")
            if (detail.isNotEmpty()) Text(detail, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (machines.isNotEmpty()) Text(machines, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/**
 * One provider (iOS `ModelLauncherDetailView`): what it sets, "Test availability" (on each of its online machines; a Persona one
 * and a direct API on the hub's), a ~/.zshrc one's function on each machine (keys masked), and hide / restore / delete.
 */
@Composable
internal fun ProviderDetailPage(providers: ProviderSetup?, name: String, machines: List<Machine>, onBack: () -> Unit) {
    val fallbackTitle = if (isTraeCliId(name)) TraeCliDisplayName else name
    if (providers == null) {
        JListPage(title = fallbackTitle, onBack = onBack) { item { JNotice(stringResource(R.string.settings_providers_demo)) } }
        return
    }
    val model = remember(providers, name) { ProviderDetailModel(providers, name) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    var confirm by remember { mutableStateOf(false) }
    // Editing in place: the form ("form"), or its "Computers" page by itself ("computers").
    var editing by rememberSaveable(name) { mutableStateOf<String?>(ProviderEditPreview.take()) }
    LaunchedEffect(model) { model.load() }
    LaunchedEffect(state.gone) { if (state.gone) onBack() }
    val label = { id: String -> machineLabel(machines, id) }
    val stored = state.launcher
    if (editing != null && stored != null && stored.managed) {
        NewProviderPage(providers, onBack = { editing = null }, onSaved = { editing = null; scope.launch { model.load() } },
            existing = stored, machines = machines, startAtComputers = editing == "computers")
        return
    }
    val facts = state.launcher?.let { providerFacts(it, label) }.orEmpty()
    val title = stored?.settingsTitle ?: fallbackTitle
    JListPage(title = title, onBack = onBack, actions = {
        if (stored?.managed == true) JIconButton(PhIcons.PencilSimple, stringResource(R.string.settings_pedit_edit), { editing = "form" })
    }) {
        val l = state.launcher
        when {
            l == null && state.error != null -> item("error") {
                JNotice(state.error!!.text(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou,
                    actionLabel = stringResource(R.string.settings_retry_read), onAction = { scope.launch { model.load() } })
            }
            l == null -> item("loading") { JLoading() }
            else -> {
                item("facts-header") { JSectionHeader(stringResource(R.string.settings_providers_settings)) }
                facts.forEachIndexed { index, (title, value, mono) ->
                    item("fact-$index") {
                        JGroupItem(GroupPosition.of(index, facts.size)) {
                            JRow(title, trailing = {
                                Text(value, Modifier.widthIn(max = Tokens.Space.xxl * 6), style = if (mono) PersonaTheme.type.code else PersonaTheme.type.callout,
                                    color = PersonaTheme.palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
                            })
                        }
                    }
                }
                if (l.hasScope) {
                    // Where it runs and which computers have a key of their own (docs/integrations/launchers.md "Device scope and on-demand delivery").
                    val keyed = l.keyMachines
                    val rows = if (keyed.isEmpty()) 1 else 2
                    item("scope-header") { JSectionHeader(stringResource(R.string.settings_pscope_title)) }
                    item("scope") {
                        JGroupItem(GroupPosition.of(0, rows)) {
                            JRow(stringResource(R.string.settings_pscope_share), leadingIcon = PhIcons.Laptop, subtitle = scopeSummaryText(l, machines),
                                subtitleLines = 3, onClick = { editing = "computers" })
                        }
                    }
                    if (keyed.isNotEmpty()) item("scope-keys") {
                        JGroupItem(GroupPosition.of(1, rows)) {
                            JRow(stringResource(R.string.settings_pscope_machine_keys), leadingIcon = PhIcons.Key,
                                subtitle = keyed.joinToString(stringResource(R.string.settings_providers_list_sep), transform = label),
                                value = pluralStringResource(R.plurals.settings_pscope_machine_keys_count, keyed.size, keyed.size),
                                onClick = { editing = "computers" })
                        }
                    }
                }
                if (l.managed) item("managed-note") {
                    SettingsCaption(if (l.isApi) stringResource(R.string.settings_providers_api_note) else stringResource(R.string.settings_providers_launch_note, l.name),
                        Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
                }
                // "Test availability": a ~/.zshrc one on each of its online machines, the others on the hub's.
                val targets: List<String?> = if (l.managed || l.isApi) listOf(null)
                    else l.machines.filter { id -> machines.any { (it.id == id || GroupKeys.raw(it.id) == id) && it.online } }
                item("check-header") { JSectionHeader(stringResource(R.string.settings_providers_check)) }
                if (targets.isEmpty()) item("check-none") { SettingsCaption(stringResource(R.string.settings_providers_check_offline), Modifier.padding(horizontal = Tokens.Space.xs)) }
                else {
                    targets.forEachIndexed { index, target ->
                        item("check-$index") {
                            val key = target ?: ""
                            JGroupItem(GroupPosition.of(index, targets.size)) {
                                CheckRow(target?.let(label) ?: stringResource(R.string.settings_providers_check_hub), key in state.checks, state.checks[key], state.checkFailures[key])
                            }
                        }
                    }
                    item("check-action") {
                        Box(Modifier.padding(top = Tokens.Space.s)) { Action(stringResource(if (state.checks.values.any { it == null } && state.checks.isNotEmpty()) R.string.settings_providers_checking else R.string.settings_providers_check_run),
                            enabled = state.checks.values.none { it == null }) { scope.launch { model.check(targets) } } }
                    }
                }
                if (l.definitions.isNotEmpty()) {
                    item("defs-header") { JSectionHeader(stringResource(if (l.pathScript) R.string.settings_providers_definition_script else R.string.settings_providers_definition)) }
                    l.definitions.toSortedMap().forEach { (id, text) ->
                        item("def-$id") {
                            Column(Modifier.padding(bottom = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                                if (l.definitions.size > 1) Text(label(id), style = PersonaTheme.type.labelSmall, color = PersonaTheme.palette.ink2, maxLines = 1)
                                JCodeBlock(text, small = true)
                            }
                        }
                    }
                    item("defs-note") { SettingsCaption(stringResource(R.string.settings_providers_definition_note), Modifier.padding(horizontal = Tokens.Space.xs)) }
                }
                state.notice?.let { notice -> item("notice") { JNotice(notice.text(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou) } }
                item("action") {
                    Box(Modifier.padding(top = Tokens.Space.section)) {
                        if (l.hidden) JButton(stringResource(R.string.settings_providers_restore), { scope.launch { model.restore() } }, Modifier.fillMaxWidth(),
                            kind = JButtonKind.Secondary, enabled = !state.busy, loading = state.busy)
                        else JButton(stringResource(if (l.managed) R.string.settings_providers_delete else R.string.settings_providers_hide_long), { confirm = true },
                            Modifier.fillMaxWidth(), kind = if (l.managed) JButtonKind.Destructive else JButtonKind.Secondary, enabled = !state.busy, loading = state.busy)
                    }
                }
            }
        }
    }
    val l = state.launcher
    if (confirm && l != null) JDialog(onDismiss = { confirm = false },
        title = stringResource(if (l.managed) R.string.settings_providers_delete_title else R.string.settings_providers_hide_title, l.settingsTitle),
        text = stringResource(if (l.managed) R.string.settings_providers_delete_text else R.string.settings_providers_hidden_note), destructive = l.managed,
        confirm = stringResource(if (l.managed) R.string.settings_providers_delete else R.string.settings_providers_hide) to { confirm = false; scope.launch { model.remove() }; Unit })
}

/** The provider's settings as label and value, in iOS's order; what it does not set is left out. */
@Composable
private fun providerFacts(l: HubLauncher, machineName: (String) -> String): List<Triple<String, String, Boolean>> = buildList<Pair<String, String>> {
    add(stringResource(R.string.settings_providers_source) to stringResource(when {
        l.managed -> R.string.settings_providers_source_managed
        l.pathScript -> R.string.settings_providers_source_path
        else -> R.string.settings_providers_source_zshrc
    }))
    if (l.isApi) add(stringResource(R.string.settings_providers_kind) to stringResource(R.string.settings_providers_kind_api, if (l.api == "responses") "Responses" else "Messages"))
    l.baseUrl?.takeIf { it.isNotBlank() }?.let { add("Base URL" to it) }
    l.model?.takeIf { it.isNotBlank() }?.let { add(stringResource(R.string.settings_providers_model) to it) }
    l.smallModel?.takeIf { it.isNotBlank() }?.let { add(stringResource(R.string.settings_providers_small_model) to it) }
    l.subagentModel?.takeIf { it.isNotBlank() }?.let { add(stringResource(R.string.settings_providers_subagent_model) to it) }
    l.customModel?.takeIf { it.isNotBlank() }?.let { add(stringResource(R.string.settings_providers_custom_model) to it) }
    l.origin?.takeIf { it.startsWith("zshrc:") }?.let { add(stringResource(R.string.settings_providers_origin) to "~/.zshrc · " + it.removePrefix("zshrc:")) }
    if (l.models.size > 1) add(stringResource(R.string.settings_providers_models) to l.models.joinToString(stringResource(R.string.settings_providers_list_sep)) { it.id })
    if (l.hasHeaders) add(stringResource(R.string.settings_providers_headers) to stringResource(R.string.settings_providers_set_hidden))
    if (l.hasEnv) add(stringResource(R.string.settings_providers_env) to stringResource(R.string.settings_providers_set_hidden))
    if (l.managed) add("API Key" to (l.keyReference?.let { providerReferenceState(LocalResources.current, it) }
        ?: stringResource(if (l.hasKey) R.string.settings_providers_set_hidden else R.string.settings_providers_unset)))
    if (l.managed && l.api != "responses") add(stringResource(R.string.settings_providers_auth) to if (l.auth == "bearer") "Bearer" else "x-api-key")
    if (l.args.isNotEmpty()) add(stringResource(R.string.settings_providers_args) to l.args.joinToString(" "))
    if (l.machines.isNotEmpty()) add(stringResource(R.string.settings_providers_machines) to l.machines.joinToString(stringResource(R.string.settings_providers_list_sep), transform = machineName))
}.map { (title, value) ->
    // Addresses, model ids and arguments read as code; the words ("Source", "Set") do not.
    Triple(title, value, value.startsWith("http") || value == l.model || value == l.smallModel || value == l.subagentModel ||
        value == l.customModel || l.args.isNotEmpty() && value == l.args.joinToString(" ") || title == "Base URL")
}

/** One machine's "Test availability": not run, running, or the answer (or why it failed). */
@Composable
private fun CheckRow(place: String, asked: Boolean, check: LauncherCheck?, failure: UiText? = null) {
    val subtitle = when {
        !asked -> stringResource(R.string.settings_providers_check_hint)
        check == null -> stringResource(R.string.settings_providers_checking)
        check.ok -> listOfNotNull(check.model?.takeIf { it.isNotBlank() }, check.answer?.takeIf { it.isNotBlank() }).joinToString(" · ")
            .ifEmpty { stringResource(R.string.settings_providers_check_ok) }
        else -> failure?.text() ?: check.error?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_providers_check_failed)
    }
    JRow(place, subtitle = subtitle, leadingIcon = PhIcons.Laptop, subtitleLines = 3, trailing = {
        when {
            asked && check == null -> JSpinner(Modifier.size(Tokens.Space.Standard.l))
            check != null -> JTag(if (check.ok) stringResource(R.string.settings_providers_check_ms, check.millis) else stringResource(R.string.settings_providers_check_failed),
                status = if (check.ok) Status.Unread else Status.NeedsYou)
            else -> {}
        }
    })
}

/**
 * "Import from ~/.zshrc" (iOS `LauncherImportModel`): pick a machine, see what its claude aliases come to (merged by gateway
 * and key), tick the ones to keep and import them; the hub reads the keys on the machine again itself, so none passes
 * through the phone.
 */
@Composable
internal fun ProviderImportPage(
    providers: ProviderSetup, machines: List<Machine>, onBack: () -> Unit, onDone: (LauncherImportResult) -> Unit,
    initialMachine: String? = null, initialAlias: String? = null,
) {
    val model = remember(providers, initialMachine, initialAlias) { ProviderImportModel(providers) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    // The current assistant's computers alone: the hub reads another assistant's through that one, not this one.
    val assistant = currentAssistant()
    val online = machines.filter { it.online && assistant.ownsOn(it.id, it.manager) }
    LaunchedEffect(model) {
        if (initialAlias != null && !initialMachine.isNullOrBlank()) {
            model.choose(initialMachine, initialAlias)
            model.load()
        }
    }
    LaunchedEffect(online.map { it.id }) {
        if (initialAlias == null && state.machine.isBlank()) (online.firstOrNull { it.kind == "local" } ?: online.firstOrNull())?.let { model.choose(GroupKeys.raw(it.id)) }
    }
    var picking by remember { mutableStateOf(false) }
    JListPage(title = stringResource(R.string.settings_providers_import), onBack = onBack, bottomBar = {
        Box(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.s)) {
            when (state.phase) {
                ImportPhase.Machine, ImportPhase.Loading -> JButton(stringResource(R.string.settings_providers_import_read), { scope.launch { model.load() } },
                    Modifier.fillMaxWidth(), enabled = state.machine.isNotBlank() && state.phase == ImportPhase.Machine, loading = state.phase == ImportPhase.Loading)
                ImportPhase.Preview, ImportPhase.Importing -> JButton(pluralStringResource(R.plurals.settings_providers_import_n, state.picked.size, state.picked.size),
                    { scope.launch { model.runImport() } }, Modifier.fillMaxWidth(), enabled = state.canImport, loading = state.phase == ImportPhase.Importing)
                ImportPhase.Done -> JButton(stringResource(R.string.settings_providers_import_done), { state.result?.let(onDone) }, Modifier.fillMaxWidth(), enabled = state.result != null)
            }
        }
    }) {
        item("intro") {
            val intro = initialAlias?.let { stringResource(R.string.settings_paste_alias_import_intro, it) } ?: stringResource(R.string.settings_providers_import_intro)
            SettingsCaption(intro, Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
        }
        item("machine") {
            JGroupItem(GroupPosition.Single) {
                JRow(stringResource(R.string.settings_src_claude_machine), leadingIcon = PhIcons.Laptop,
                    value = state.machine.takeIf { it.isNotBlank() }?.let { machineLabel(machines, it) } ?: stringResource(R.string.settings_src_claude_no_online),
                    enabled = initialAlias == null && online.isNotEmpty() && state.phase in setOf(ImportPhase.Machine, ImportPhase.Preview, ImportPhase.Done),
                    onClick = if (initialAlias == null) ({ picking = true }) else null)
            }
        }
        state.error?.let { error -> item("error") { JNotice(error.text(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou) } }
        when (state.phase) {
            ImportPhase.Preview, ImportPhase.Importing -> {
                if (state.providers.isEmpty()) item("empty") {
                    JEmptyState(stringResource(R.string.settings_providers_import_none), stringResource(R.string.settings_providers_import_none_detail), icon = PhIcons.TerminalWindow)
                } else {
                    item("found") { JSectionHeader(stringResource(R.string.settings_providers_import_found), count = state.providers.size) }
                    state.providers.forEachIndexed { index, p ->
                        item("p-${p.name}") {
                            JGroupItem(GroupPosition.of(index, state.providers.size)) {
                                JRow(p.name, subtitle = listOfNotNull(
                                    p.model.takeIf { it.isNotBlank() }, providerHost(p.baseUrl),
                                    p.aliases.takeIf { it.isNotEmpty() }?.joinToString(stringResource(R.string.settings_providers_list_sep)),
                                    p.existing?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.settings_providers_import_existing, it) },
                                    stringResource(R.string.settings_providers_import_no_key).takeIf { !p.hasKey },
                                ).joinToString(" · "), subtitleLines = 3, chevron = false,
                                    enabled = state.phase == ImportPhase.Preview, onClick = { model.toggle(p.name) }, role = Role.Checkbox,
                                    trailing = { JCheckbox(p.name in state.picked, null) })
                            }
                        }
                    }
                }
            }
            ImportPhase.Done -> state.result?.let { result ->
                item("done") {
                    JNotice(listOfNotNull(
                        result.created.takeIf { it.isNotEmpty() }?.let { stringResource(R.string.settings_providers_import_created, it.joinToString(stringResource(R.string.settings_providers_list_sep))) },
                        result.updated.takeIf { it.isNotEmpty() }?.let { stringResource(R.string.settings_providers_import_updated, it.joinToString(stringResource(R.string.settings_providers_list_sep))) },
                    ).joinToString("\n").ifEmpty { stringResource(R.string.settings_providers_import_nothing) },
                        Modifier.padding(top = Tokens.Space.m), status = Status.Unread, icon = PhIcons.CheckCircle)
                }
            }
            else -> {}
        }
    }
    if (picking) JActionSheet(online.map { machine ->
        val raw = GroupKeys.raw(machine.id)
        JAction(machine.displayName, PhIcons.Laptop, checked = raw == state.machine, onClick = { model.restart(); model.choose(raw) })
    }, { picking = false }, title = stringResource(R.string.settings_src_claude_pick_machine))
}
