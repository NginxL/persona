package persona.feature.settings

import persona.core.designsystem.text
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.api.ClaudeSetting
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonPrimitive

/**
 * Settings › Claude Code / Codex / Antigravity settings: the common options of each computer's agent ([kind]), written to
 * one or all online machines. [owns] keeps the computers listed (the current assistant's, docs/assistant/managers.md "Clients").
 */
@Composable
internal fun ClaudeSettingsPage(sessions: SessionsRepository, management: MachineManagement?, onBack: () -> Unit,
    kind: AgentSettingsKind = AgentSettingsKind.Claude, owns: (persona.core.contracts.Machine) -> Boolean = { true }) {
    if (management == null) {
        JListPage(title = stringResource(kind.title), onBack = onBack) { item { JNotice(stringResource(kind.demo)) } }
        return
    }
    val context = LocalContext.current
    val res = LocalResources.current
    val model = remember(management, kind) { ClaudeSettingsModel(management, StoredClaudeSettingsChoices(context), kind) }
    val state by model.state.collectAsState()
    val machines by sessions.machines.collectAsState()
    val online = machines.filter { it.online && owns(it) }
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var picking by remember { mutableStateOf(false) }
    var choosing by remember { mutableStateOf<ClaudeSetting?>(null) }
    val drafts = remember { mutableStateMapOf<String, String>() }
    LaunchedEffect(online.map { it.id }) { model.follow(online) }
    LaunchedEffect(state.machineId) { drafts.clear(); model.load() }
    // "Live Replies" and "Blocked config directories" are Claude Code's alone.
    val (liveReplies, liveRepliesState) = rememberLiveReplies(management, if (kind == AgentSettingsKind.Claude) state.machineId else "")
    val (dirs, dirsState) = rememberClaudeDirs(management, if (kind == AgentSettingsKind.Claude) state.machineId else "")
    var dirInput by remember { mutableStateOf("") }
    LaunchedEffect(state.machineId) { dirInput = "" }
    val saveDirs = claudeDirsSaver(dirs, machineId = { state.machineId }) { dirInput = "" }
    fun set(key: String, value: JsonElement) {
        scope.launch { model.set(key, value, online)?.let { feedback?.notice(it.resolve(res)) } }
    }
    JListPage(title = stringResource(kind.title), onBack = onBack, actions = {
        JIconButton(PhIcons.ArrowClockwise, stringResource(R.string.settings_src_refresh), {
            scope.launch {
                model.load()
                if (kind == AgentSettingsKind.Claude) {
                    liveReplies.load(state.machineId)
                    dirs.load(state.machineId)
                }
            }
        }, enabled = !state.loading && state.machineId.isNotBlank())
    }) {
        item("target") {
            JGroupItem(GroupPosition.of(0, 2), Modifier.padding(top = Tokens.Space.s)) {
                JRow(stringResource(R.string.settings_src_claude_machine), leadingIcon = PhIcons.Laptop, value = machines.firstOrNull { it.id == state.machineId }?.displayName
                    ?: if (online.isEmpty()) stringResource(R.string.settings_src_claude_no_online) else null, enabled = online.isNotEmpty(), onClick = { picking = true })
            }
        }
        item("apply-all") {
            JGroupItem(GroupPosition.of(1, 2)) {
                JSwitchRow(stringResource(R.string.settings_src_claude_apply_all), state.applyAll, model::applyAll, leadingIcon = PhIcons.Stack)
            }
        }
        item("target-note") {
            SettingsCaption(stringResource(kind.caption),
                Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
        }
        state.error?.let { error -> item("error") { JInlineStatus(error.text(), Modifier.padding(vertical = Tokens.Space.s), tone = InlineTone.Failed) } }
        when {
            online.isEmpty() -> item("none") { JEmptyState(stringResource(R.string.settings_src_claude_no_online), stringResource(R.string.settings_src_claude_no_online_hint), icon = PhIcons.Laptop) }
            state.settings.isEmpty() && state.loading -> item("loading") { JLoading() }
        }
        itemsIndexed(state.settings, key = { _, it -> "setting-${it.key}" }) { index, setting ->
            JGroupItem(GroupPosition.of(index, state.settings.size), if (index == 0) Modifier.padding(top = Tokens.Space.m) else Modifier) {
                when (setting.type) {
                    "bool" -> JRow(onClick = { set(setting.key, JsonPrimitive(!setting.boolValue)) }, chevron = false, role = Role.Switch,
                        trailing = { JSwitch(setting.boolValue, null) }) { SettingTitle(setting) }
                    "enum" -> JRow(onClick = { choosing = setting }, trailing = {
                        Text(setting.stringValue, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink3, maxLines = 1)
                    }, chevron = true) { SettingTitle(setting) }
                    else -> Column(Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        SettingTitle(setting)
                        val focus = LocalFocusManager.current
                        fun commit() {
                            focus.clearFocus()
                            val value = drafts.remove(setting.key)?.trim() ?: return
                            // Emptied: back to the agent's default (the node removes the key).
                            if (value != setting.stringValue) set(setting.key, if (value.isEmpty()) JsonNull else JsonPrimitive(value))
                        }
                        JTextField(drafts[setting.key] ?: setting.stringValue, { drafts[setting.key] = it }, Modifier.fillMaxWidth(),
                            placeholder = setting.placeholder.text(), singleLine = true,
                            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done), keyboardActions = KeyboardActions(onDone = { commit() }))
                    }
                }
            }
        }
        if (kind == AgentSettingsKind.Claude && state.machineId.isNotBlank()) {
            // A frame can pass between picking another computer and its read starting: until it does, the section reads
            // this computer's own state, or it would show - and a tap would write - the computer shown before (iOS
            // draws the section only when `live.ref == ref`).
            val live = liveRepliesState.forMachine(state.machineId)
            if (!live.unsupported) {
                val others = if (state.applyAll) online.filter { it.id != state.machineId } else emptyList()
                val shownName = online.firstOrNull { it.id == state.machineId }?.displayName
                liveRepliesSection(
                    live,
                    onToggle = { next -> scope.launch { liveReplies.set(next, others, shownName)?.let { feedback?.notice(it.resolve(res)) } } },
                    onRetry = { scope.launch { liveReplies.load(state.machineId) } },
                )
            }
        }
        if (kind == AgentSettingsKind.Claude && state.machineId.isNotBlank()) {
            // A frame can pass between picking another computer and its read starting: until it does, the section reads
            // this computer's own state, or it would show - and a tap would write - the computer shown before (#9276).
            val dirsUi = dirsState.forMachine(state.machineId)
            if (!dirsUi.unsupported) {
                claudeDirsSection(
                    dirsUi,
                    dirInput,
                    { dirInput = it },
                    onRetry = { scope.launch { dirs.load(state.machineId) } },
                    onSave = { blocked, key, done -> saveDirs(blocked, key, done) },
                )
            }
        }
    }
    if (picking) JActionSheet(online.map { machine ->
        JAction(machine.displayName, PhIcons.Laptop, checked = machine.id == state.machineId, onClick = { model.choose(machine.id) })
    }, { picking = false }, title = stringResource(R.string.settings_src_claude_pick_machine))
    choosing?.let { setting ->
        JActionSheet(setting.options.orEmpty().map { option ->
            JAction(option, checked = option == setting.stringValue, onClick = { if (option != setting.stringValue) set(setting.key, JsonPrimitive(option)) })
        }, { choosing = null }, title = setting.title.ifBlank { setting.key })
    }
}

/** An option's title, "Default" while it is unset, and what it does. */
@Composable
private fun SettingTitle(setting: ClaudeSetting) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(setting.title.ifBlank { setting.key }, Modifier.weight(1f, fill = false), style = PersonaTheme.type.body,
                color = PersonaTheme.palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (setting.isDefault) JTag(stringResource(R.string.settings_src_default))
        }
        if (setting.detail.isNotBlank()) Text(setting.detail, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
    }
}
