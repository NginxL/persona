package persona.feature.assistant

import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.Saver
import androidx.compose.runtime.setValue
import androidx.compose.ui.res.stringResource
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.launcherModel
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.JChip
import persona.core.designsystem.icons.PhIcons

/**
 * The new conversation's launcher (iOS StarterComposer's model menu, web StarterLauncherChip): a chip over the home's
 * composer with what it starts with, "Default: X" until one is [picked] for this conversation alone (then just "X", drawn selected:
 * the pick stays with the conversation, so no one-off mark, #7513). A
 * tap lists the default and the assistant machine's [launchers] (null while they load; [failed] when they could not).
 */
@Composable
internal fun StarterLauncherChip(
    picked: String?, defaultName: String?, launchers: List<AgentLauncher>?, failed: Boolean, enabled: Boolean,
    /** The list opens: read the launchers (again after a failure) and the default, as the web does on each open. */
    onOpen: () -> Unit = {},
    onPick: (String?) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    val defaultLabel = defaultName?.takeIf(String::isNotBlank)?.let { stringResource(R.string.assistant_launcher_default_named, it) }
        ?: stringResource(R.string.assistant_launcher_default)
    val label = picked ?: defaultLabel
    // Always tappable-looking: a chip without onClick is a smaller quiet label, so it would shrink on every send.
    JChip(label, onClick = { if (enabled) { open = true; onOpen() } }, icon = PhIcons.RocketLaunch, selected = picked != null)
    if (!open) return
    val loading = stringResource(R.string.assistant_launcher_loading)
    val unreadable = stringResource(R.string.assistant_launcher_failed)
    val none = stringResource(R.string.assistant_launcher_none)
    val actions = buildList {
        add(JAction(defaultLabel, checked = picked == null) { onPick(null) })
        when {
            launchers == null -> add(JAction(if (failed) unreadable else loading, enabled = false) {})
            launchers.isEmpty() -> add(JAction(none, enabled = false) {})
            else -> launchers.forEach { launcher ->
                val text = listOfNotNull(launcher.name, launcherModel(launcher)).joinToString(" · ")
                add(JAction(text, checked = launcher.name == picked) { onPick(launcher.name) })
            }
        }
    }
    JActionSheet(actions, onDismiss = { open = false }, title = stringResource(R.string.assistant_launcher_title),
        note = stringResource(R.string.assistant_launcher_note))
}

/** The conversation a launcher picked on the new-conversation page started: a retry of the same message reuses it. */
internal data class StarterStart(val launcher: String, val session: SessionKey)

/** Kept across rotation, so a retry after the send failed does not open a second conversation. */
internal val StarterStartSaver = Saver<StarterStart?, List<String>>(
    save = { start -> start?.let { listOf(it.launcher, it.session.machineId, it.session.sessionId) } },
    restore = { saved -> StarterStart(saved[0], SessionKey(saved[1], saved[2])) },
)

/**
 * Where a message from the new-conversation page goes on [launcher]: the conversation [started] on that very launcher
 * (a retry), else a new one from [start] (a first try, or the launcher was changed since).
 */
internal suspend fun starterTarget(launcher: String, started: StarterStart?, start: suspend (String) -> SessionKey): StarterStart =
    started?.takeIf { it.launcher == launcher } ?: StarterStart(launcher, start(launcher))
