package persona.feature.agents

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.todoHandOffPrompt
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.launch
import kotlin.coroutines.cancellation.CancellationException

enum class HandOffMode(@androidx.annotation.StringRes val title: Int) {
    New(R.string.agents_handoff_new), Existing(R.string.agents_handoff_existing)
}

/** What a to-do's "Start a new one" fills in for a machine; the mode "default" sends none. */
internal data class HandOffDefaults(val cwd: String = "~", val launcher: String = "claude", val mode: String = "default")

/**
 * The machine "Start a new one" starts on (#2134): the last one New Agent started on in [scope] when it is online, else
 * the first online one, else the first.
 */
internal suspend fun handOffMachine(store: AgentCreationStore?, scope: String, machines: List<Machine>): Machine? {
    val online = machines.filter { it.online }
    val last = try { store?.lastMachine(scope) } catch (e: CancellationException) { throw e } catch (_: Exception) { null }
    return online.firstOrNull { it.id == last } ?: online.firstOrNull() ?: machines.firstOrNull()
}

/**
 * [machineId]'s folder, launcher ("" follows the model configuration) and permission mode as New Agent remembers them;
 * with nothing remembered, `~`, claude and the default mode.
 */
internal suspend fun handOffDefaults(store: AgentCreationStore?, scope: String, machineId: String): HandOffDefaults {
    val remembered = try { store?.defaults(scope, machineId) } catch (e: CancellationException) { throw e } catch (_: Exception) { null }
    if (remembered == null || remembered.cwd.isBlank()) return HandOffDefaults()
    return HandOffDefaults(remembered.cwd, remembered.launcher, remembered.mode.ifEmpty { "default" })
}

/**
 * The running agent "Hand off to an existing one" sends to: the one [picked] while it still runs, else the first (#4167).
 * The list is rebuilt and re-sorted as agents work, so the pick is held by its key, not by its row.
 */
internal fun handOffTarget(running: List<Session>, picked: SessionKey?): SessionKey? =
    running.firstOrNull { it.key == picked }?.key ?: running.firstOrNull()?.key

/** The pick kept after [shown] is drawn: an empty list for a moment (a refresh, a reconnect) keeps the old one. */
internal fun handOffPick(picked: SessionKey?, shown: SessionKey?): SessionKey? = shown ?: picked

/**
 * [store] and [scope] are New Agent's memory on this hub: a new Agent starts from it (the last machine, its folder,
 * launcher and mode) rather than from `~`, claude and the default mode each time.
 */
@Composable
internal fun TodoHandOffSheet(
    todo: Todo,
    machines: List<Machine>,
    sessions: List<Session>,
    onDismiss: () -> Unit,
    onHandOffNew: (machineId: String, cwd: String, launcher: String, permissionMode: String?, prompt: String) -> Unit,
    onHandOffExisting: (target: SessionKey, prompt: String) -> Unit,
    /** The caller's start is under way; it ends on every outcome but an opened chat. */
    starting: Boolean = false,
    /** Why the last tap didn't open a chat (a rejected or unconfirmed start). */
    error: String? = null,
    store: AgentCreationStore? = null,
    scope: String = "",
) {
    var mode by remember { mutableStateOf(HandOffMode.New) }
    var instruction by remember { mutableStateOf("") }

    var selectedId by remember { mutableStateOf<String?>(null) }
    val selectedMachine = machines.firstOrNull { it.id == selectedId }
    var cwd by remember { mutableStateOf(HandOffDefaults().cwd) }
    var launcher by remember { mutableStateOf(HandOffDefaults().launcher) }
    var permissionMode by remember { mutableStateOf(HandOffDefaults().mode) }
    // What the machine remembers stays in the menus after another pick.
    var remembered by remember { mutableStateOf(HandOffDefaults()) }
    val coroutines = rememberCoroutineScope()
    fun pick(machine: Machine) {
        selectedId = machine.id
        coroutines.launch {
            val defaults = handOffDefaults(store, scope, machine.id)
            if (selectedId != machine.id) return@launch
            remembered = defaults
            cwd = defaults.cwd
            launcher = defaults.launcher
            permissionMode = defaults.mode
        }
    }
    // Picked once, and again only when the picked machine leaves the list.
    LaunchedEffect(machines) {
        if (machines.any { it.id == selectedId }) return@LaunchedEffect
        val machine = handOffMachine(store, scope, machines) ?: return@LaunchedEffect
        if (machines.none { it.id == selectedId }) pick(machine)
    }
    var showMachineMenu by remember { mutableStateOf(false) }
    var showLauncherMenu by remember { mutableStateOf(false) }
    var showModeMenu by remember { mutableStateOf(false) }

    val runningAgents = remember(sessions) {
        sessions.filter { !it.assistant && it.isLive }
            .sortedWith(compareByDescending<Session> { it.updated }.thenBy { it.sessionId })
    }
    // Picked once, and again only when the picked agent leaves the list (keyed on the list, any agent's output put the
    // pick back on the top row).
    var pickedTarget by remember { mutableStateOf<SessionKey?>(null) }
    val selectedTarget = handOffTarget(runningAgents, pickedTarget)
    SideEffect { pickedTarget = handOffPick(pickedTarget, selectedTarget) }

    JSheet(onDismiss = onDismiss, title = stringResource(R.string.agents_todo_hand_to_agent)) {
        Column(
            Modifier
                .fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.s),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)
        ) {
            Row(
                Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
            ) {
                HandOffMode.entries.forEach { m ->
                    JChip(
                        stringResource(m.title),
                        selected = mode == m,
                        onClick = { mode = m },
                        modifier = Modifier.weight(1f)
                    )
                }
            }

            if (mode == HandOffMode.New) {
                JSectionHeader(stringResource(R.string.agents_handoff_new_session))
                val densities = currentTextDensities()
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    Box {
                        JGroupItem(GroupPosition.of(0, 4)) {
                            JRow(
                                stringResource(R.string.agents_handoff_computer),
                                subtitle = selectedMachine?.let { if (it.online) it.displayName else stringResource(R.string.agents_handoff_machine_offline, it.displayName) }
                                    ?: stringResource(R.string.agents_handoff_pick_computer),
                                leadingIcon = PhIcons.Desktop,
                                chevron = true,
                                onClick = { showMachineMenu = true }
                            )
                        }
                        DropdownMenu(showMachineMenu, onDismissRequest = { showMachineMenu = false }) {
                            RetainedTextDensity(densities) {
                                machines.forEach { m ->
                                    DropdownMenuItem(
                                        text = { Text(if (m.online) m.displayName else stringResource(R.string.agents_handoff_machine_offline, m.displayName)) },
                                        onClick = {
                                            if (m.id != selectedId) pick(m)
                                            showMachineMenu = false
                                        }
                                    )
                                }
                            }
                        }
                    }

                    JGroupItem(GroupPosition.of(1, 4)) {
                        JTextField(cwd, { cwd = it }, label = stringResource(R.string.agents_handoff_directory), singleLine = true)
                    }

                    Box {
                        JGroupItem(GroupPosition.of(2, 4)) {
                            JRow(
                                stringResource(R.string.agents_new_launcher),
                                subtitle = handOffLauncherLabel(launcher),
                                leadingIcon = PhIcons.RocketLaunch,
                                chevron = true,
                                onClick = { showLauncherMenu = true }
                            )
                        }
                        DropdownMenu(showLauncherMenu, onDismissRequest = { showLauncherMenu = false }) {
                            RetainedTextDensity(densities) {
                                listOf(remembered.launcher, "claude", "codex").distinct().forEach { l ->
                                    DropdownMenuItem(
                                        text = { Text(handOffLauncherLabel(l)) },
                                        onClick = {
                                            launcher = l
                                            showLauncherMenu = false
                                        }
                                    )
                                }
                            }
                        }
                    }

                    Box {
                        JGroupItem(GroupPosition.of(3, 4)) {
                            JRow(
                                stringResource(R.string.agents_new_permission_mode),
                                subtitle = handOffModeLabel(permissionMode),
                                leadingIcon = PhIcons.ShieldCheck,
                                chevron = true,
                                onClick = { showModeMenu = true }
                            )
                        }
                        DropdownMenu(showModeMenu, onDismissRequest = { showModeMenu = false }) {
                            RetainedTextDensity(densities) {
                                listOf("default", BYPASS_MODE, remembered.mode).distinct().forEach { m ->
                                    DropdownMenuItem(
                                        text = { Text(handOffModeLabel(m)) },
                                        onClick = { permissionMode = m; showModeMenu = false }
                                    )
                                }
                            }
                        }
                    }
                }
            } else {
                JSectionHeader(stringResource(R.string.agents_handoff_pick_agent), count = runningAgents.size)
                if (runningAgents.isEmpty()) {
                    JEmptyState(stringResource(R.string.agents_handoff_no_agents), stringResource(R.string.agents_handoff_no_agents_hint))
                } else {
                    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                        runningAgents.forEachIndexed { index, session ->
                            val selected = selectedTarget == session.key
                            val machineName = machines.firstOrNull { it.id == session.machineId }?.displayName ?: session.machineId
                            JGroupItem(GroupPosition.of(index, runningAgents.size)) {
                                JRow(
                                    title = session.displayTitle,
                                    subtitle = stringResource(R.string.agents_todo_location, machineName, session.folderName),
                                    leading = {
                                        val status = Status.entries.firstOrNull { it.name == session.agentState.name } ?: Status.Idle
                                        JStatusDot(status)
                                    },
                                    trailing = if (selected) ({ JIcon(PhIcons.CheckCircle, null, selected = true) }) else null,
                                    chevron = false,
                                    onClick = { pickedTarget = session.key }
                                )
                            }
                        }
                    }
                }
            }

            JSectionHeader(stringResource(R.string.agents_handoff_what))
            JTextField(
                instruction,
                { instruction = it },
                label = stringResource(R.string.agents_handoff_instruction),
                placeholder = stringResource(R.string.agents_handoff_instruction_placeholder),
                minLines = 3,
                maxLines = 6
            )

            JSectionHeader(stringResource(R.string.agents_handoff_sent_along))
            JCard(Modifier.fillMaxWidth()) {
                Row(
                    Modifier.fillMaxWidth().padding(Tokens.Space.s),
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                    verticalAlignment = Alignment.Top
                ) {
                    Box(
                        Modifier
                            .width(Tokens.Space.xs)
                            .height(ContentTokens.taskQuoteMarkHeight)
                            .clip(RoundedCornerShape(Tokens.Space.xxs))
                            .background(PersonaTheme.palette.accent)
                    )
                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm)) {
                            JIcon(PhIcons.ListChecks, null, size = Tokens.Space.l, tint = PersonaTheme.palette.accent)
                            Text(
                                stringResource(R.string.agents_row_todo, todo.title),
                                style = PersonaTheme.type.headline,
                                color = PersonaTheme.palette.ink,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        }
                        if (todo.notes.isNotBlank()) {
                            Text(
                                todo.notes,
                                style = PersonaTheme.type.caption,
                                color = PersonaTheme.palette.ink2,
                                maxLines = 3,
                                overflow = TextOverflow.Ellipsis
                            )
                        }
                    }
                }
            }

            Spacer(Modifier.height(Tokens.Space.s))

            error?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text) }

            JButton(
                text = stringResource(if (starting) R.string.agents_handoff_starting else R.string.agents_todo_hand_to_agent),
                onClick = {
                    val prompt = todoHandOffPrompt(todo.title, todo.notes, instruction)
                    if (mode == HandOffMode.New) {
                        val m = selectedMachine ?: return@JButton
                        onHandOffNew(
                            m.id,
                            cwd.ifEmpty { "~" },
                            launcher,
                            permissionMode.takeIf { it != "default" },
                            prompt
                        )
                    } else {
                        val target = selectedTarget ?: return@JButton
                        onHandOffExisting(target, prompt)
                    }
                },
                enabled = !starting && (if (mode == HandOffMode.New) selectedMachine != null else selectedTarget != null),
                modifier = Modifier.fillMaxWidth()
            )
        }
    }
}

/** A launcher's name; "" follows the model configuration, in the form's words. */
@Composable
private fun handOffLauncherLabel(launcher: String): String = launcher.ifEmpty { stringResource(R.string.agents_new_follow_config) }

/** The sheet's own words for the default mode and Allow Everything; another mode New Agent remembered in its words, with its id. */
@Composable
private fun handOffModeLabel(mode: String): String = when (mode) {
    "default" -> stringResource(R.string.agents_handoff_mode_default)
    BYPASS_MODE -> stringResource(R.string.agents_handoff_mode_bypass)
    else -> permissionModes.firstOrNull { it.first == mode }?.second
        ?.let { stringResource(R.string.agents_handoff_mode_named, stringResource(it), mode) } ?: mode
}
