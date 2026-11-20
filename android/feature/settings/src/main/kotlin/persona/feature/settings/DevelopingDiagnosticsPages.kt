package persona.feature.settings

import android.text.format.Formatter
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import java.io.File
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.data.dev.DevCategory
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JDialog
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JListPage
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSwitchRow
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale

@Composable
internal fun DevelopingPersonaAbout(
    developing: DevelopingDiagnostics,
    position: GroupPosition = GroupPosition.Single,
    onExport: () -> Unit,
) {
    val context = LocalContext.current
    val snapshot by developing.state.collectAsState()
    val scope = rememberCoroutineScope()
    var confirming by rememberSaveable { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var failed by remember { mutableStateOf(false) }

    fun change(enabled: Boolean, confirmed: Boolean = false) {
        if (busy) return
        scope.launch {
            busy = true
            failed = false
            try {
                if (!enabled && !confirmed) {
                    developing.refresh()
                    if ((developing.state.value.recordedBytes ?: 0) > 0) {
                        confirming = true
                        return@launch
                    }
                }
                developing.setEnabled(enabled)
                confirming = false
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                failed = true
            } finally {
                busy = false
            }
        }
    }

    LaunchedEffect(developing) { developing.refresh() }
    JGroupItem(position) {
        JSwitchRow(stringResource(R.string.settings_developing_title), snapshot.enabled, { change(it) },
            enabled = !busy, subtitleLines = Int.MAX_VALUE)
    }
    SettingsCaption(stringResource(R.string.settings_developing_footer), Modifier.padding(vertical = Tokens.Space.s))
    if (failed) SettingsCaption(stringResource(R.string.settings_developing_toggle_failed))
    if (snapshot.enabled) {
        val since = snapshot.since?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, currentLocale) } ?: "—"
        val bytes = Formatter.formatShortFileSize(context, snapshot.recordedBytes ?: 0)
        Column(Modifier.padding(top = Tokens.Space.s)) {
            JGroupItem(GroupPosition.of(0, 2)) {
                JRow(stringResource(R.string.settings_developing_recorded),
                    subtitle = stringResource(R.string.settings_developing_recorded_since, since, bytes), subtitleLines = 3)
            }
            JGroupItem(GroupPosition.of(1, 2)) {
                JRow(stringResource(R.string.settings_developing_export), onClick = onExport)
            }
        }
    }
    if (confirming) {
        JDialog(
            onDismiss = { if (!busy) confirming = false },
            title = stringResource(R.string.settings_developing_off_title),
            text = stringResource(R.string.settings_developing_off_text),
            confirm = stringResource(R.string.settings_developing_off_delete) to { change(false, confirmed = true) },
            dismiss = stringResource(persona.core.designsystem.R.string.designsystem_cancel) to { confirming = false },
            destructive = true,
            confirmEnabled = !busy,
        ) {
            JButton(stringResource(R.string.settings_developing_export_first), {
                confirming = false
                onExport()
            }, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary, enabled = !busy)
        }
    }
}

@Composable
internal fun DevelopingDiagnosticsExportPage(developing: DevelopingDiagnostics, onBack: () -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var selectedNames by rememberSaveable { mutableStateOf<Set<String>>(DevCategory.entries.mapTo(mutableSetOf()) { it.name }) }
    val selected = selectedNames.mapNotNull { name -> DevCategory.entries.firstOrNull { it.name == name } }.toSet()
    var busy by remember { mutableStateOf(false) }
    var archive by remember { mutableStateOf<File?>(null) }
    var error by remember { mutableStateOf<Int?>(null) }

    fun create() {
        if (busy) return
        scope.launch {
            busy = true
            error = null
            archive = null
            try {
                archive = developing.export(selected)
            } catch (_: Exception) {
                error = R.string.settings_developing_export_failed
            } finally {
                busy = false
            }
        }
    }

    fun share(agent: Boolean) {
        val file = archive ?: return
        try {
            if (agent) developing.sendToAgent(context, file) else developing.share(context, file)
            error = null
        } catch (_: Exception) {
            error = R.string.settings_developing_share_failed
        }
    }

    JListPage(title = stringResource(R.string.settings_developing_export_title), onBack = onBack) {
        item("categories-title") { JSectionHeader(stringResource(R.string.settings_developing_categories)) }
        val categories = listOf(
            DevCategory.Logs to R.string.settings_developing_logs,
            DevCategory.Errors to R.string.settings_developing_errors,
            DevCategory.Energy to R.string.settings_developing_energy,
        )
        categories.forEachIndexed { index, (category, label) ->
            item(category.name) {
                JGroupItem(GroupPosition.of(index, categories.size)) {
                    JSwitchRow(stringResource(label), category in selected, { on ->
                        selectedNames = if (on) selectedNames + category.name else selectedNames - category.name
                        archive = null
                        error = null
                    }, enabled = !busy)
                }
            }
        }
        item("device") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                JRow(stringResource(R.string.settings_developing_device),
                    subtitle = stringResource(R.string.settings_developing_device_always), subtitleLines = Int.MAX_VALUE)
            }
        }
        item("create") {
            JButton(stringResource(if (busy) R.string.settings_developing_creating else R.string.settings_developing_create), ::create,
                Modifier.fillMaxWidth().padding(top = Tokens.Space.m), enabled = !busy)
        }
        error?.let { message ->
            item("error") {
                Column(Modifier.padding(top = Tokens.Space.m)) {
                    Text(stringResource(message), color = PersonaTheme.palette.danger.dot, style = PersonaTheme.type.callout)
                    if (message == R.string.settings_developing_export_failed && !busy) {
                        JButton(stringResource(R.string.settings_developing_retry), ::create,
                            Modifier.fillMaxWidth().padding(top = Tokens.Space.s), kind = JButtonKind.Secondary)
                    }
                }
            }
        }
        archive?.let { file ->
            item("archive") {
                Column(Modifier.padding(top = Tokens.Space.m)) {
                    SettingsCaption(file.name, Modifier.padding(bottom = Tokens.Space.s))
                    JButton(stringResource(R.string.settings_developing_share), { share(false) }, Modifier.fillMaxWidth(),
                        kind = JButtonKind.Secondary)
                    JButton(stringResource(R.string.settings_developing_send_agent), { share(true) },
                        Modifier.fillMaxWidth().padding(top = Tokens.Space.s))
                }
            }
        }
        item("footer") { SettingsCaption(stringResource(R.string.settings_developing_footer), Modifier.padding(vertical = Tokens.Space.m)) }
    }
}
