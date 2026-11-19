package persona.android.dev

import android.app.Application
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import dagger.hilt.android.lifecycle.HiltViewModel
import java.io.File
import javax.inject.Inject
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import persona.android.RecentShareTarget
import persona.android.ShareTargetStore
import persona.android.R
import persona.android.preselectedShareTarget
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.SessionsRepository
import persona.feature.chat.ChatWiringSource
import persona.feature.chat.prepareDiagnosticsDraft

internal data class DiagnosticsOpenChat(val target: SessionKey?)

@HiltViewModel
class DiagnosticsShareViewModel @Inject constructor(
    private val application: Application,
    val assistant: AssistantRepository,
    val sessions: SessionsRepository,
    private val wiring: ChatWiringSource,
    private val saved: SavedStateHandle,
    listPreferences: DataStore<Preferences>,
) : ViewModel() {
    private val targets = ShareTargetStore(listPreferences)
    private var owner: HubScope? = null
    private var initialized = false
    private var file: File? = null
    private val openedAt = System.currentTimeMillis()

    var fileName by androidx.compose.runtime.mutableStateOf("")
        private set
    var note by androidx.compose.runtime.mutableStateOf(saved.get<String>("note") ?: application.getString(R.string.app_diagnostics_note))
        private set
    var target by androidx.compose.runtime.mutableStateOf(saved.get<String>("targetMachine")?.let {
        SessionKey(it, saved.get<String>("targetSession").orEmpty())
    })
        private set
    private var chosen by androidx.compose.runtime.mutableStateOf(saved.get<Boolean>("targetChosen") ?: (target != null))
    internal var recents by androidx.compose.runtime.mutableStateOf<List<RecentShareTarget>>(emptyList())
        private set
    var busy by androidx.compose.runtime.mutableStateOf(false)
        private set
    var loading by androidx.compose.runtime.mutableStateOf(false)
        private set
    var error by androidx.compose.runtime.mutableStateOf<String?>(null)
        private set
    internal var openChat by androidx.compose.runtime.mutableStateOf<DiagnosticsOpenChat?>(null)
        private set
    val paired: Boolean get() = owner != null

    fun updateNote(value: String) { note = value; saved["note"] = value }
    fun select(value: SessionKey?) {
        target = value
        chosen = true
        saved["targetMachine"] = value?.machineId
        saved["targetSession"] = value?.sessionId
        saved["targetChosen"] = true
    }
    fun selected(listed: Set<SessionKey>): SessionKey? = if (chosen) target else preselectedShareTarget(recents, listed, openedAt)

    fun read(path: String?) {
        if (initialized) return
        initialized = true
        try {
            val candidate = File(requireNotNull(path))
            val root = File(application.cacheDir, "dev-exports").canonicalFile.toPath()
            file = candidate.canonicalFile.takeIf { it.isFile && it.toPath().startsWith(root) }
            requireNotNull(file)
            fileName = file!!.name
            owner = wiring.current.value?.scope
            if (owner == null) error = application.getString(R.string.app_share_pair_first)
        } catch (_: Exception) {
            error = application.getString(R.string.app_share_read_failed)
            return
        }
        viewModelScope.launch {
            if (owner == null) {
                owner = wiring.current.filterNotNull().first().scope
                if (error == application.getString(R.string.app_share_pair_first)) error = null
            }
            recents = try { targets.read() } catch (error: CancellationException) { throw error } catch (_: Exception) { emptyList() }
            refresh()
        }
    }

    fun refresh() {
        if (loading || owner == null) return
        loading = true
        viewModelScope.launch {
            try {
                assistant.refresh()
                sessions.refresh()
            } catch (error: CancellationException) { throw error }
            catch (_: Exception) { error = application.getString(R.string.app_share_list_stale) }
            finally { loading = false }
        }
    }

    /** Uploads and saves the draft, then and only then asks the activity to open the chat. It never sends. */
    suspend fun prepare(destination: SessionKey?) {
        if (busy || openChat != null) return
        val archive = file ?: return
        val originalOwner = owner
        if (originalOwner == null) { error = application.getString(R.string.app_share_pair_first); return }
        busy = true
        error = null
        try {
            val flow = destination?.let(wiring::bindingsFor) ?: wiring.current
            val bindings = withTimeout(10_000) { flow.filterNotNull().first() }
            if (bindings.scope != originalOwner) {
                error = application.getString(R.string.app_share_pairing_changed)
                return
            }
            val mainTarget = if (destination == null) bindings.mainUploadTarget?.invoke() else null
            prepareDiagnosticsDraft(
                bindings.drafts, bindings.composer, destination,
                { mainTarget }, archive, note,
            )
            openChat = DiagnosticsOpenChat(destination ?: mainTarget)
            try {
                targets.remember(destination, System.currentTimeMillis())
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { }
        } catch (_: TimeoutCancellationException) {
            error = application.getString(R.string.app_diagnostics_prepare_failed)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) {
            error = application.getString(R.string.app_diagnostics_prepare_failed)
        } finally {
            busy = false
        }
    }
}
