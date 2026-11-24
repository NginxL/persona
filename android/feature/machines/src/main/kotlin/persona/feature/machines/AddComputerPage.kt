package persona.feature.machines

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.PersistableBundle
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import persona.core.contracts.Machine
import persona.core.contracts.api.ComputerCode
import persona.core.contracts.api.ComputerOs
import persona.core.contracts.api.ComputerPairing
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.launch

/**
 * "Add a computer" / "Confirm computer" ([confirm]: the computer to confirm, "" for the first one waiting). Without [api] (demo mode) or on a
 * hub that cannot add computers from a device yet, it says where to add one instead.
 */
@Composable
internal fun AddComputerPage(api: ComputerPairing?, machines: StateFlow<List<Machine>>, confirm: String?, onBack: () -> Unit) {
    val canAdd by (api?.canAdd ?: flowOf(false)).collectAsState(initial = null)
    if (api == null || (confirm == null && canAdd == false)) { OlderHub(onBack); return }
    if (confirm == null && canAdd == null) { JListPage(title = stringResource(R.string.machines_onb_title), onBack = onBack, largeTitle = false) { item { JLoading() } }; return }
    val model = remember(api, confirm) { AddComputerModel(api, machines, confirm) }
    val state by model.state.collectAsState()
    // Every assistant's computers (the hub's list): the add's own one, and the ones in the way, whoever's they are.
    val listed by machines.collectAsState()
    // A new computer is the current assistant's (docs/assistant/managers.md "Clients"); not while it is offline.
    val assistant = currentAssistant()
    val offline = assistant.offlineForAdding()
    val scope = rememberCoroutineScope()
    // Only while the page is shown: a hidden tab, the app in the background or the screen off asks the hub nothing.
    // Back in front, the countdown catches up at once and the next read finds a computer that joined meanwhile.
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(model, lifecycle) { lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) { model.tick(); model.run() } }
    val machine = state.machine(listed)
    val stage = state.stage(listed)
    val shown = state.shown(machine)
    val title = when {
        confirm != null -> stringResource(R.string.machines_onb_title_confirm)
        else -> stringResource(R.string.machines_onb_title)
    }
    // A new link revokes the pairing the computer has: only for one that never connected, not one that is asleep.
    val relink = canAdd == true && machine?.errorCode == "never_connected"
    val bar: @Composable RowScope.() -> Unit = {
        when (stage) {
            AddStage.Form -> if (offline != null) JButton(stringResource(R.string.machines_onb_close), onBack, Modifier.weight(1f), kind = JButtonKind.Secondary)
                else JButton(stringResource(R.string.machines_onb_next), { scope.launch { model.add(assistant) } }, Modifier.weight(1f),
                    enabled = state.name.isNotBlank() && state.removing == null, loading = state.busy)
            AddStage.Joined -> {
                JButton(stringResource(R.string.machines_onb_reject), { scope.launch { model.answer(false) } }, Modifier.weight(1f), kind = JButtonKind.Secondary,
                    enabled = state.answering == null, loading = state.answering == AddOutcome.Rejected)
                JButton(stringResource(R.string.machines_onb_confirm), { scope.launch { model.answer(true) } }, Modifier.weight(1f),
                    enabled = state.answering == null, loading = state.answering == AddOutcome.Confirmed)
            }
            AddStage.Offline -> {
                JButton(stringResource(R.string.machines_onb_remove), { scope.launch { model.answer(false) } }, Modifier.weight(1f), kind = JButtonKind.Secondary,
                    enabled = state.answering == null && !state.busy, loading = state.answering == AddOutcome.Rejected)
                if (relink) JButton(stringResource(R.string.machines_onb_new_link), { scope.launch { model.again() } }, Modifier.weight(1f),
                    enabled = state.answering == null, loading = state.busy)
            }
            AddStage.Expired -> JButton(stringResource(R.string.machines_onb_new_code), { scope.launch { model.again() } }, Modifier.weight(1f), loading = state.busy)
            AddStage.Connected, AddStage.Gone -> JButton(stringResource(R.string.machines_onb_done), onBack, Modifier.weight(1f))
            AddStage.Waiting -> JButton(stringResource(R.string.machines_onb_close), onBack, Modifier.weight(1f), kind = JButtonKind.Secondary)
        }
    }
    JListPage(title = title, onBack = onBack, largeTitle = false, bottomBar = {
        Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.s), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), content = bar)
    }) {
        when (stage) {
            // The current assistant offline: nothing to fill in, only why (as the web's dialog says it).
            AddStage.Form -> if (offline != null) item("assistant-offline") {
                JNotice(stringResource(R.string.machines_onb_assistant_offline, offline.shownName), Modifier.padding(top = Tokens.Space.s))
            } else form(state, model::setOs, model::setName)
            AddStage.Waiting -> state.code?.let { code -> waiting(state, code, shown, relink = confirm != null, onOs = model::setOs, listed = listed, roster = assistant.roster) }
            AddStage.Expired -> item("expired") { JEmptyState(stringResource(R.string.machines_onb_expired), stringResource(R.string.machines_onb_expired_hint), icon = PhIcons.ClockCountdown) }
            AddStage.Joined -> {
                item("joined") {
                    val about = listOfNotNull(machine?.system?.name?.takeIf { it.isNotBlank() } ?: machine?.os, machine?.hostname).joinToString(" · ")
                    JEmptyState(stringResource(R.string.machines_onb_joined, shown),
                        listOf(about, stringResource(R.string.machines_onb_joined_hint)).filter { it.isNotBlank() }.joinToString("\n"), icon = PhIcons.Desktop)
                }
                placedUnder(state, listed, assistant.roster)
            }
            AddStage.Connected -> item("connected") { JEmptyState(stringResource(R.string.machines_onb_confirmed, shown), stringResource(R.string.machines_onb_confirmed_hint), icon = PhIcons.CheckCircle) }
            AddStage.Gone -> item("gone") {
                if (state.machineId == null) JEmptyState(stringResource(R.string.machines_onb_none_waiting), stringResource(R.string.machines_onb_none_waiting_hint), icon = PhIcons.CheckCircle)
                else JEmptyState(stringResource(if (state.outcome == AddOutcome.Rejected) R.string.machines_onb_rejected else R.string.machines_onb_gone, shown),
                    stringResource(R.string.machines_onb_rejected_hint), icon = PhIcons.Trash)
            }
            AddStage.Offline -> item("offline") {
                val reason = machineErrorText(machine?.errorCode)?.let { stringResource(it) }
                JEmptyState(stringResource(R.string.machines_onb_offline, shown),
                    listOfNotNull(reason, stringResource(if (relink) R.string.machines_onb_offline_hint else R.string.machines_onb_offline_hint_old)).joinToString("\n"),
                    icon = PhIcons.Desktop)
            }
        }
        val blocking = if (state.tooMany && stage == AddStage.Form) inTheWay(listed, assistant.group) else emptyList()
        if (blocking.isNotEmpty()) inTheWay(blocking, state.removing) { id -> scope.launch { model.remove(id) } }
        else state.error?.let { error -> item("error") { JInlineStatus(error.text(), Modifier.padding(top = Tokens.Space.m), tone = InlineTone.Failed) } }
    }
}

/**
 * The computers keeping a new one from being added (the hub takes 3 waiting, whichever assistant's): what the hub's
 * refusal means, then each with how it stands and Remove (as the web's AddComputer lists them).
 */
private fun LazyListScope.inTheWay(machines: List<Machine>, removing: String?, onRemove: (String) -> Unit) {
    item("in-the-way") {
        val never = machines.all { it.errorCode == "never_connected" }
        JNotice(pluralStringResource(if (never) R.plurals.machines_onb_too_many_never else R.plurals.machines_onb_too_many_waiting, machines.size, machines.size),
            Modifier.padding(top = Tokens.Space.m, bottom = Tokens.Space.s))
    }
    items(machines.size, key = { "in-the-way:${machines[it].id}" }) { index ->
        val m = machines[index]
        JGroupItem(GroupPosition.of(index, machines.size)) {
            JRow(m.displayName, subtitle = stringResource(when {
                m.errorCode == "never_connected" -> R.string.machines_onb_never_connected
                m.online -> R.string.machines_onb_waiting_confirm
                else -> R.string.machines_status_offline
            }), leadingIcon = PhIcons.Desktop, chevron = false, trailing = {
                JButton(stringResource(R.string.machines_onb_remove), { onRemove(m.id) }, kind = JButtonKind.Secondary, size = JButtonSize.Small,
                    enabled = removing == null, loading = removing == m.id)
            })
        }
    }
}

/** The system and the name. */
private fun LazyListScope.form(state: AddComputerState, onOs: (ComputerOs) -> Unit, onName: (String) -> Unit) {
    item("os") {
        JSectionHeader(stringResource(R.string.machines_onb_os))
        OsSwitch(state.os, onOs)
    }
    item("name") {
        val example = stringResource(when (state.os) {
            ComputerOs.Mac -> R.string.machines_onb_name_placeholder_mac
            ComputerOs.Linux -> R.string.machines_onb_name_placeholder_linux
            ComputerOs.Windows -> R.string.machines_onb_name_placeholder_windows
        })
        JTextField(state.name, onName, Modifier.fillMaxWidth().padding(top = Tokens.Space.l), label = stringResource(R.string.machines_onb_name),
            placeholder = example, singleLine = true, enabled = !state.busy, supportingText = stringResource(R.string.machines_onb_name_note))
    }
}

@Composable
private fun OsSwitch(os: ComputerOs, onOs: (ComputerOs) -> Unit) {
    JSegmented(ComputerOs.entries.map { JSegment(stringResource(when (it) {
        ComputerOs.Mac -> R.string.machines_onb_os_mac; ComputerOs.Linux -> R.string.machines_onb_os_linux; ComputerOs.Windows -> R.string.machines_onb_os_windows
    })) }, os.ordinal, { onOs(ComputerOs.entries[it]) }, Modifier.fillMaxWidth())
}

/** How to use the link on that system, then the wait and its countdown. */
private fun LazyListScope.waiting(state: AddComputerState, code: ComputerCode, shown: String, relink: Boolean, onOs: (ComputerOs) -> Unit, listed: List<Machine>, roster: ManagerRoster?) {
    // The hub does not know which system a computer that never connected runs: the user can switch.
    if (relink) item("relink-os") { Box(Modifier.padding(top = Tokens.Space.s)) { OsSwitch(state.os, onOs) } }
    item("steps") { Instructions(state.os, code) }
    item("wait") {
        JInlineStatus(stringResource(R.string.machines_onb_waiting, shown) + "  " + stringResource(R.string.machines_onb_expires, clockText(state.secondsLeft)),
            Modifier.padding(top = Tokens.Space.l), tone = InlineTone.Busy)
    }
    placedUnder(state, listed, roster)
}

/** The hub put the computer under another assistant than the current one (or, an older hub, under its own): said plainly. */
private fun LazyListScope.placedUnder(state: AddComputerState, listed: List<Machine>, roster: ManagerRoster?) {
    state.placedUnder(listed, roster)?.let { name -> item("placed-under") {
        JNotice(stringResource(R.string.machines_onb_added_under, name), Modifier.padding(top = Tokens.Space.m))
    } }
}

@Composable
private fun Instructions(os: ComputerOs, code: ComputerCode) {
    val context = LocalContext.current
    val uri = LocalUriHandler.current
    val feedback = LocalAppFeedback.current
    val copied = stringResource(R.string.machines_onb_copied)
    val copyLabel = stringResource(R.string.machines_onb_copy_command)
    Column(Modifier.padding(top = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        when (os) {
            ComputerOs.Linux -> {
                val install = code.linuxInstall
                Step(1, stringResource(if (install != null) R.string.machines_onb_linux_step_install else R.string.machines_onb_linux_step_pair))
                if (install == null) JTextButton(stringResource(R.string.machines_onb_linux_install_link), { val _ = runCatching { uri.openUri(LinkLinuxDoc) } }, icon = PhIcons.ArrowSquareOut)
                Command(install ?: pairCommand(code), copyLabel) { copySecret(context, copyLabel, it); feedback?.notice(copied) }
                Step(2, stringResource(R.string.machines_onb_linux_step_done))
            }
            ComputerOs.Windows -> {
                Step(1, stringResource(R.string.machines_onb_windows_step_get))
                Step(2, stringResource(R.string.machines_onb_windows_step_run))
                Command(windowsCommand(code.pairUrl), copyLabel) { copySecret(context, copyLabel, it); feedback?.notice(copied) }
                Text(stringResource(R.string.machines_onb_windows_note), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
            }
            ComputerOs.Mac -> {
                Step(1, stringResource(R.string.machines_onb_mac_step_install))
                JTextButton(stringResource(R.string.machines_onb_mac_install_link), { val _ = runCatching { uri.openUri(PersonaLinkInstallDoc) } }, icon = PhIcons.ArrowSquareOut)
                Step(2, stringResource(R.string.machines_onb_mac_step_link))
                val linkLabel = stringResource(R.string.machines_onb_copy_link)
                val shareTitle = stringResource(R.string.machines_onb_share_title)
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JButton(linkLabel, { copySecret(context, linkLabel, code.pairUrl); feedback?.notice(copied) }, kind = JButtonKind.Secondary, size = JButtonSize.Small, icon = PhIcons.Copy)
                    JButton(stringResource(R.string.machines_onb_share_link), { share(context, shareTitle, code.pairUrl) }, kind = JButtonKind.Secondary, size = JButtonSize.Small, icon = PhIcons.ShareNetwork)
                }
            }
        }
    }
}

@Composable
private fun Step(n: Int, text: String) {
    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Text("$n.", Modifier.alignByBaseline(), style = PersonaTheme.type.body, color = PersonaTheme.palette.accent)
        Text(text, Modifier.weight(1f).alignByBaseline(), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
    }
}

/** A command to paste whole, with its copy button (it carries the pairing link). */
@Composable
private fun Command(line: String, label: String, onCopy: (String) -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JCodeBlock(line, small = true)
        JButton(label, { onCopy(line) }, kind = JButtonKind.Secondary, size = JButtonSize.Small, icon = PhIcons.Copy)
    }
}

/** A hub that cannot add computers from a device yet (or demo mode): where to add one instead. */
@Composable
private fun OlderHub(onBack: () -> Unit) {
    JListPage(title = stringResource(R.string.machines_onb_title), onBack = onBack, largeTitle = false) {
        item("older") {
            JEmptyState(stringResource(R.string.machines_onb_older_title), stringResource(R.string.machines_onb_older_body), icon = PhIcons.Desktop,
                action = { JButton(stringResource(R.string.machines_onb_close), onBack, kind = JButtonKind.Secondary) })
        }
    }
}

/** m:ss, for the countdown. */
internal fun clockText(seconds: Int): String = "${maxOf(0, seconds) / 60}:${(maxOf(0, seconds) % 60).toString().padStart(2, '0')}"

/** Copies text that carries a one-time code; Android 13+ keeps it out of the clipboard preview. */
private fun copySecret(context: Context, label: String, text: String) {
    val clip = ClipData.newPlainText(label, text)
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) clip.description.extras = PersistableBundle().apply { putBoolean(ClipDescription.EXTRA_IS_SENSITIVE, true) }
    context.getSystemService(ClipboardManager::class.java)?.setPrimaryClip(clip)
}

private fun share(context: Context, title: String, text: String) {
    val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, text).putExtra(Intent.EXTRA_TITLE, title)
    val _ = runCatching { context.startActivity(Intent.createChooser(send, title).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
}
