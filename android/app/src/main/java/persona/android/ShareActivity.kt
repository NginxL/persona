package persona.android

import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import android.app.Application
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.IntentCompat
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import dagger.hilt.android.AndroidEntryPoint
import dagger.hilt.android.lifecycle.HiltViewModel
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.RetainedTextDensity
import persona.core.designsystem.currentTextDensities
import persona.core.designsystem.dense
import persona.core.designsystem.dismissKeyboardOnBlankTap
import persona.core.contracts.SessionKey
import persona.core.contracts.contextBody
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.SessionsRepository
import persona.feature.agents.AgentsPreferences
import persona.feature.agents.AgentsPreferencesSnapshot
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.agentRank
import java.util.UUID
import javax.inject.Inject
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.first

@AndroidEntryPoint
class ShareActivity : ComponentActivity() {
    /** Text in the language of Settings › Language (docs/protocol/i18n.md). */
    override fun attachBaseContext(base: android.content.Context) = super.attachBaseContext(persona.core.designsystem.AppLanguages.wrap(base))

    @Inject lateinit var preferences: AgentsPreferences
    private lateinit var model: ShareViewModel
    @OptIn(ExperimentalMaterial3Api::class)
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        model = ViewModelProvider(this)[ShareViewModel::class.java]
        model.read(intent)
        setContent {
            PersonaTheme {
                val assistant by model.assistant.assistant.collectAsStateWithLifecycle()
                val sessions by model.sessions.sessions.collectAsStateWithLifecycle()
                val machines by model.sessions.machines.collectAsStateWithLifecycle()
                val sorting by preferences.snapshots.collectAsStateWithLifecycle(AgentsPreferencesSnapshot())
                val sheet = rememberModalBottomSheetState(skipPartiallyExpanded = true)
                LaunchedEffect(model.completed) {
                    model.completed?.let {
                        Toast.makeText(this@ShareActivity, it, Toast.LENGTH_SHORT).show()
                        finish()
                    }
                }
                val densities = currentTextDensities()
                ModalBottomSheet(onDismissRequest = { finish() }, sheetState = sheet) {
                    RetainedTextDensity(densities) {
                    Column(
                        Modifier.fillMaxWidth().dismissKeyboardOnBlankTap().verticalScroll(rememberScrollState())
                            .padding(horizontal = 24.dp.dense).padding(bottom = 24.dp.dense),
                        verticalArrangement = Arrangement.spacedBy(12.dp.dense),
                    ) {
                        Text(stringResource(R.string.app_share_title), style = MaterialTheme.typography.titleLarge)
                        Surface(color = MaterialTheme.colorScheme.surfaceContainer, shape = MaterialTheme.shapes.medium) {
                            Column(Modifier.fillMaxWidth().padding(16.dp.dense)) {
                                Text(model.payload.title.ifBlank { stringResource(R.string.app_share_content) }, maxLines = 2, overflow = TextOverflow.Ellipsis)
                                if (model.payload.text.isNotBlank()) {
                                    Text(model.payload.text, maxLines = 4, overflow = TextOverflow.Ellipsis,
                                        style = MaterialTheme.typography.bodyMedium)
                                }
                                if (model.payload.uris.isNotEmpty()) Text(pluralStringResource(R.plurals.app_share_attachments, model.payload.uris.size, model.payload.uris.size))
                            }
                        }
                        OutlinedTextField(
                            value = model.note,
                            onValueChange = model::updateNote,
                            modifier = Modifier.fillMaxWidth(),
                            enabled = !model.locked && !model.busy,
                            placeholder = { Text(stringResource(R.string.app_share_note)) },
                            maxLines = 4,
                        )
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
                        // Until the user picks, the sheet is on the last target if it was used within a day and is listed.
                        val selected = model.selected(rows.mapTo(HashSet()) { it.session.key })
                        ShareTargetList(
                            recentFirst(rows, { it.session.key }, model.recents).map { row ->
                                val session = row.session
                                ShareTargetRow(session.key, session.displayTitle,
                                    machines.firstOrNull { it.id == session.machineId }?.displayName ?: session.agentLabel)
                            },
                            selected, mainEnabled = !model.busy && !model.locked && model.paired,
                            enabled = !model.busy && !model.locked, select = model::select,
                        )
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text(stringResource(R.string.app_share_as_context), Modifier.weight(1f))
                            Switch(model.asContext, model::updateContext, enabled = !model.busy && !model.locked)
                        }
                        if (model.loading) Text(stringResource(R.string.app_share_loading))
                        model.error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
                        if (model.busy) {
                            LinearProgressIndicator(Modifier.fillMaxWidth())
                            Text(model.progress)
                        }
                        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
                            TextButton(onClick = { finish() }) { Text(stringResource(R.string.app_cancel), maxLines = 1) }
                            if (model.locked || !model.paired) {
                                Button(onClick = {
                                    startActivity(mainActivityIntent(this@ShareActivity).apply {
                                        model.sentTarget?.let { putExtra(MainActivity.EXTRA_SESSION, it.path) }
                                    })
                                    finish()
                                }, enabled = !model.busy) {
                                    Text(stringResource(if (model.paired) R.string.app_share_view_chat else R.string.app_open_persona), maxLines = 1)
                                }
                            } else {
                                if (model.error != null && !model.busy) {
                                    TextButton(onClick = model::refresh) { Text(stringResource(R.string.app_refresh), maxLines = 1) }
                                }
                                Button(onClick = { model.send(selected) },
                                    enabled = !model.busy && model.payload.hasContent && model.paired) {
                                    Text(stringResource(R.string.app_send), maxLines = 1)
                                }
                            }
                        }
                    }
                    }
                }
            }
        }
    }

    override fun onDestroy() {
        // Cancel / dismiss / back finish the activity; rotation keeps the staged copy for the recreated sheet.
        discardUnsentShare(isFinishing, model.locked, model::discardDraft)
        super.onDestroy()
    }
}

/** A sheet closed without sending drops its staged copy (#4815); a submitted share stays queued. */
internal fun discardUnsentShare(finishing: Boolean, submitted: Boolean, discard: () -> Unit) {
    if (finishing && !submitted) discard()
}

/** An Agent the sheet can send to: its title and where it runs. */
internal data class ShareTargetRow(val key: SessionKey, val title: String, val subtitle: String)

/** "Send to": the main chat, then the Agents in [rows]' order (the recent ones first, #2395). */
@Composable
internal fun ShareTargetList(
    rows: List<ShareTargetRow>, selected: SessionKey?, mainEnabled: Boolean, enabled: Boolean, select: (SessionKey?) -> Unit,
) {
    Text(stringResource(R.string.app_share_to), style = MaterialTheme.typography.labelLarge)
    ShareTarget(stringResource(R.string.app_main_chat), "Persona", selected == null, mainEnabled) { select(null) }
    Column(Modifier.heightIn(max = 200.dp).verticalScroll(rememberScrollState())) {
        rows.forEach { row -> ShareTarget(row.title, row.subtitle, selected == row.key, enabled) { select(row.key) } }
    }
}

@Composable
private fun ShareTarget(title: String, subtitle: String, selected: Boolean, enabled: Boolean, select: () -> Unit) {
    Row(Modifier.fillMaxWidth().selectable(selected, enabled = enabled, role = Role.RadioButton, onClick = select)
        .padding(vertical = 8.dp.dense), verticalAlignment = Alignment.CenterVertically) {
        RadioButton(selected, onClick = null, enabled = enabled)
        Column(Modifier.padding(start = 12.dp.dense).weight(1f)) {
            Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(subtitle, style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

internal data class SharePayload(val title: String = "", val text: String = "", val uris: List<Uri> = emptyList()) {
    val hasContent get() = title.isNotBlank() || text.isNotBlank() || uris.isNotEmpty()
}

/** ACTION_SEND with EXTRA_TEXT carries ClipData only as the Sharesheet preview thumbnail, not an attachment (#4817). */
internal fun <T> sharedUris(multiple: Boolean, streams: List<T>, clipUris: List<T>, hasText: Boolean): List<T> =
    (if (multiple || !hasText) streams + clipUris else streams).distinct()

@HiltViewModel
class ShareViewModel @Inject constructor(
    application: Application,
    val assistant: AssistantRepository,
    val sessions: SessionsRepository,
    private val queue: persona.android.platform.ShareQueue,
    private val saved: SavedStateHandle,
    listPreferences: DataStore<Preferences>,
) : ViewModel() {
    private val app = application as PersonaApp
    private val targets = ShareTargetStore(listPreferences)
    private var owner by mutableStateOf<persona.android.data.Hub?>(null)
    val paired get() = owner != null
    internal var payload by mutableStateOf(SharePayload())
        private set
    var note by mutableStateOf(saved.get<String>("note").orEmpty())
        private set
    var asContext by mutableStateOf(saved.get<Boolean>("context") ?: true)
        private set
    var target by mutableStateOf(saved.get<String>("targetMachine")?.let {
        SessionKey(it, saved.get<String>("targetSession").orEmpty())
    })
        private set
    /** Whether the user picked [target] in this sheet; until then the sheet is on the preselected one. */
    private var chosen by mutableStateOf(saved.get<Boolean>("targetChosen") ?: (target != null))
    /** Where earlier shares went, latest first (#2395). */
    internal var recents by mutableStateOf<List<RecentShareTarget>>(emptyList())
        private set
    private val openedAt = System.currentTimeMillis()
    var sentTarget: SessionKey? = null
        private set
    var locked by mutableStateOf(saved.get<Boolean>("submitted") ?: false)
        private set
    var busy by mutableStateOf(false)
        private set
    var loading by mutableStateOf(false)
        private set
    var progress by mutableStateOf("")
        private set
    var error by mutableStateOf<String?>(null)
        private set
    var completed by mutableStateOf<String?>(null)
        private set
    private var initialized = false
    private var staged = false
    private val id = saved.get<String>("messageId") ?: UUID.randomUUID().toString().also { saved["messageId"] = it }

    fun updateNote(value: String) { note = value; saved["note"] = value }
    fun updateContext(value: Boolean) { asContext = value; saved["context"] = value }
    fun select(value: SessionKey?) {
        target = value
        chosen = true
        saved["targetMachine"] = value?.machineId
        saved["targetSession"] = value?.sessionId
        saved["targetChosen"] = true
    }

    /** The target the sheet is on: the user's pick, or else the last one if it was used within a day and is [listed]. */
    fun selected(listed: Set<SessionKey>): SessionKey? =
        if (chosen) target else preselectedShareTarget(recents, listed, openedAt)

    fun read(intent: Intent) {
        if (initialized) return
        initialized = true
        try {
            require(intent.action == Intent.ACTION_SEND || intent.action == Intent.ACTION_SEND_MULTIPLE)
            val streams = if (intent.action == Intent.ACTION_SEND_MULTIPLE) {
                IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java).orEmpty()
            } else listOfNotNull(IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java))
            val clips = intent.clipData?.let { clip -> (0 until clip.itemCount).map(clip::getItemAt) }.orEmpty()
            val uris = sharedUris(intent.action == Intent.ACTION_SEND_MULTIPLE, streams, clips.mapNotNull { it.uri },
                intent.getCharSequenceExtra(Intent.EXTRA_TEXT) != null)
            require(uris.all { it.scheme == "content" })
            payload = SharePayload(
                intent.getCharSequenceExtra(Intent.EXTRA_SUBJECT)?.toString()
                    ?: intent.getCharSequenceExtra(Intent.EXTRA_TITLE)?.toString().orEmpty(),
                intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()
                    ?: clips.mapNotNull { it.text?.toString() }.distinct().joinToString("\n"), uris)
            require(payload.hasContent)
        } catch (_: Exception) {
            error = app.getString(R.string.app_share_read_failed)
            return
        }
        busy = true
        progress = app.getString(R.string.app_share_saving)
        viewModelScope.launch {
            try {
                kotlinx.coroutines.flow.combine(app.pairingReady, app.pairingRestoreError) { ready, failed -> ready || failed }.first { it }
                owner = app.hub.value
                check(owner != null)
                if (locked) { completed = app.getString(R.string.app_share_queued); return@launch }
                recents = try { targets.read() } catch (error: CancellationException) { throw error } catch (_: Exception) { emptyList() }
                locked = queue.stage(id, payload.title, payload.text, payload.uris)
                staged = true
                if (locked) completed = app.getString(R.string.app_share_queued)
            } catch (error: CancellationException) { throw error }
            catch (_: Exception) {
                this@ShareViewModel.error = app.getString(if (owner == null) R.string.app_share_pair_first
                    else R.string.app_share_save_failed)
            } finally { busy = false }
            refresh()
        }
    }

    fun refresh() {
        if (loading || busy || locked || owner == null) return
        loading = true
        viewModelScope.launch {
            try {
                assistant.refresh()
                sessions.refresh()
            } catch (error: CancellationException) { throw error }
            catch (_: Exception) { if (staged) this@ShareViewModel.error = app.getString(R.string.app_share_list_stale) }
            finally { loading = false }
        }
    }

    /** Outlives the finishing activity; waits for an in-flight stage and keeps an entry that submit already queued. */
    fun discardDraft() {
        app.scope.launch {
            try { queue.discardDraft(id) }
            catch (error: CancellationException) { throw error }
            catch (_: Exception) { }
        }
    }

    fun send(destination: SessionKey?) {
        if (busy || locked || !staged) return
        if (owner == null || app.hub.value !== owner) { error = app.getString(R.string.app_share_pairing_changed); return }
        busy = true
        error = null
        progress = app.getString(R.string.app_share_queueing)
        viewModelScope.launch {
            try {
                queue.submit(id, destination, note, asContext)
                sentTarget = destination
                // Outlives the sheet, which finishes once the share is queued; a lost write only loses the order.
                app.scope.launch {
                    try { targets.remember(destination, System.currentTimeMillis()) }
                    catch (error: CancellationException) { throw error }
                    catch (_: Exception) { }
                }
                saved["submitted"] = true
                locked = true
                completed = app.getString(R.string.app_share_queued_offline)
            } catch (error: CancellationException) { throw error }
            catch (_: Exception) { this@ShareViewModel.error = app.getString(R.string.app_share_queue_failed) }
            finally { busy = false }
        }
    }
}

internal suspend fun <T> sendSharedContent(
    sender: MessageSender, target: SessionKey, title: String, text: String, attachments: List<T>,
    note: String, asContext: Boolean, untitled: String, id: String, upload: suspend (T) -> String, beforeSend: () -> Unit = {},
) {
    val paths = attachments.map { upload(it) }
    beforeSend()
    sender.send(target, formatSharedContent(title, text, paths, note, asContext, untitled), id,
        null, null, null, persona.core.contracts.api.MessageVia.Share)
}

/** [untitled] is the context card's title when the share has none, in the app's language (`app_share_content`). */
internal fun formatSharedContent(
    title: String, text: String, attachments: List<String>, note: String, asContext: Boolean, untitled: String,
): String {
    val paths = attachments.joinToString(" ") { "'${it.replace("'", "'\\''")}'" }
    val content = listOf(title, text, paths).filter { it.isNotBlank() }.joinToString("\n")
    val shared = if (asContext) {
        fun escape(value: String) = value.replace("&", "&amp;").replace("\"", "&quot;")
            .replace("<", "&lt;").replace(">", "&gt;")
        val url = Regex("https?://[^\\s<>]+", RegexOption.IGNORE_CASE).find(text)?.value.orEmpty()
        "<persona-context kind=\"context\" title=\"${escape(title.ifBlank { untitled })}\" url=\"${escape(url)}\">\n" +
            contextBody(content) + "\n</persona-context>"
    } else content
    return listOf(shared, note.trim()).filter { it.isNotBlank() }.joinToString("\n\n")
}
