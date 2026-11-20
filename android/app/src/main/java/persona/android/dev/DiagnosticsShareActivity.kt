package persona.android.dev

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dagger.hilt.android.AndroidEntryPoint
import kotlinx.coroutines.launch
import persona.android.MainActivity
import persona.android.R
import persona.android.ShareTargetList
import persona.android.ShareTargetRow
import persona.android.mainActivityIntent
import persona.android.recentFirst
import persona.core.contracts.SessionKey
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.dense
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JCard
import persona.core.designsystem.JLinearProgress
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTextButton
import persona.core.designsystem.JTextField
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.agentRank
import persona.feature.agents.AgentsPreferences
import persona.feature.agents.AgentsPreferencesSnapshot
import javax.inject.Inject

@AndroidEntryPoint
class DiagnosticsShareActivity : ComponentActivity() {
    override fun attachBaseContext(base: android.content.Context) = super.attachBaseContext(persona.core.designsystem.AppLanguages.wrap(base))
    @Inject lateinit var preferences: AgentsPreferences
    private lateinit var model: DiagnosticsShareViewModel

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        model = ViewModelProvider(this)[DiagnosticsShareViewModel::class.java]
        model.read(intent.getStringExtra(EXTRA_FILE))
        setContent {
            PersonaTheme {
                val assistant by model.assistant.assistant.collectAsStateWithLifecycle()
                val sessions by model.sessions.sessions.collectAsStateWithLifecycle()
                val machines by model.sessions.machines.collectAsStateWithLifecycle()
                val sorting by preferences.snapshots.collectAsStateWithLifecycle(AgentsPreferencesSnapshot())
                val scope = rememberCoroutineScope()
                val opened = model.openChat
                LaunchedEffect(opened) {
                    if (opened != null) {
                        startActivity(mainActivityIntent(this@DiagnosticsShareActivity).apply {
                            opened.target?.let { target -> putExtra(MainActivity.EXTRA_SESSION, target.path) }
                        })
                        finish()
                    }
                }
                val rows = sessions.filter { it.isLive && !it.assistant && it.key != assistant.mainSession }
                    .distinctBy { it.key }
                    .map { session ->
                        val version = "${session.updatedAt.orEmpty()}|${session.rev ?: ""}"
                        val visible = if (session.agentState == persona.core.contracts.AgentState.Unread &&
                            sorting.seen[session.key.path] == version) session.copy(state = "idle") else session
                        AgentSession(visible, session.stateSince, session.subagents.orEmpty().any { it.running == true })
                    }
                    .sortedWith(compareBy<AgentSession> { agentRank(it, sorting.mutes) }
                        .thenByDescending { it.sortDate }.thenBy { it.session.sessionId }.thenBy { it.id })
                val selected = model.selected(rows.mapTo(HashSet()) { it.session.key })
                val targets = recentFirst(rows, { it.session.key }, model.recents).map { row ->
                    val session = row.session
                    ShareTargetRow(session.key, session.displayTitle,
                        machines.firstOrNull { it.id == session.machineId }?.displayName ?: session.agentLabel)
                }
                JSheet(onDismiss = { finish() }, scrollable = true) {
                    DiagnosticsShareSheet(
                        fileName = model.fileName, note = model.note, onNote = model::updateNote,
                        rows = targets, selected = selected, onSelect = model::select,
                        busy = model.busy, loading = model.loading, paired = model.paired, error = model.error,
                        onOpen = { target -> scope.launch { model.prepare(target) } },
                        onCancel = { finish() },
                        onOpenPersona = { startActivity(mainActivityIntent(this@DiagnosticsShareActivity)); finish() },
                    )
                }
            }
        }
    }

    companion object { const val EXTRA_FILE = "diagnostics_file" }
}

@Composable
internal fun DiagnosticsShareSheet(
    fileName: String,
    note: String,
    onNote: (String) -> Unit,
    rows: List<ShareTargetRow>,
    selected: SessionKey?,
    onSelect: (SessionKey?) -> Unit,
    busy: Boolean,
    loading: Boolean,
    paired: Boolean,
    error: String?,
    onOpen: (SessionKey?) -> Unit,
    onCancel: () -> Unit,
    onOpenPersona: () -> Unit,
) {
    Column(
        Modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m.dense),
    ) {
        Text(stringResource(R.string.app_diagnostics_title), style = PersonaTheme.type.title)
        val archiveLabel = stringResource(R.string.app_diagnostics_archive)
        JCard(
            modifier = Modifier.semantics(mergeDescendants = true) { contentDescription = archiveLabel },
            padding = Tokens.Space.m.dense,
        ) {
            Text(fileName, Modifier.fillMaxWidth(), maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        JTextField(
            value = note, onValueChange = onNote, modifier = Modifier.fillMaxWidth(), enabled = !busy,
            label = stringResource(R.string.app_share_note), maxLines = 4,
        )
        ShareTargetList(rows, selected, mainEnabled = !busy && paired, enabled = !busy, select = onSelect)
        if (loading) Text(stringResource(R.string.app_share_loading))
        error?.let { Text(it, color = PersonaTheme.palette.danger.dot) }
        if (busy) JLinearProgress(null, Modifier.fillMaxWidth())
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
            JTextButton(stringResource(R.string.app_cancel), onCancel, enabled = !busy)
            if (!paired) {
                JButton(stringResource(R.string.app_open_persona), onOpenPersona, enabled = !busy, size = JButtonSize.Medium)
            } else {
                JButton(stringResource(R.string.app_diagnostics_open_chat), { onOpen(selected) }, enabled = !busy, size = JButtonSize.Medium)
            }
        }
    }
}
