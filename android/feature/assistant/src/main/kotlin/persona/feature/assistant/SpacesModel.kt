package persona.feature.assistant

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.Track
import persona.core.contracts.api.Tracks
import persona.core.contracts.api.TracksUnsupported

/**
 * The assistant's domain spaces as this app last read them (#13500, docs/android/android.md "Domain spaces"): read once
 * when shown, again on every hello and when the app comes back, kept when a read fails. A create or archive shows at
 * once ([note]). An assistant without tracks ([supported] false) draws no Spaces section, so the drawer stays as it was.
 */
@Stable
class SpacesModel {
    var tracks by mutableStateOf<List<Track>?>(null)
        private set
    /** Null until the first answer; false on an assistant from before tracks. */
    var supported by mutableStateOf<Boolean?>(null)
        private set
    private val loading = Mutex()

    /** What the Spaces section lists; null hides the section. Sorted once per read, not on every recomposition. */
    val sidebar: List<Track>? by derivedStateOf { tracks?.takeIf { supported == true }?.let(Tracks::sidebarSpaces) }

    fun track(slug: String): Track? = tracks?.firstOrNull { it.slug == slug }

    /** Reads the tracks again ([api] is the default manager's); one read at a time, a failed read keeps what was known. */
    suspend fun load(api: AssistantManagement) {
        if (!loading.tryLock()) return
        try {
            val read = api.tracks()
            tracks = read
            supported = true
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: TracksUnsupported) {
            tracks = null
            supported = false
        } catch (_: UnsupportedOperationException) {
            tracks = null
            supported = false
        } catch (error: Exception) {
            val status = persona.core.contracts.api.hubCodedFailure(error)?.status
            if (status == 404 || status == 405) {
                tracks = null
                supported = false
            }
        } finally {
            loading.unlock()
        }
    }

    /** A track just created or changed here, shown before the next read. */
    fun note(track: Track) {
        tracks = Tracks.noted(tracks, track)
        supported = true
    }

    companion object {
        /** [AssistantSidebarHost.openSpace]'s word for the new space form; never a space's address ([Tracks.formSlug]). */
        const val NEW = "new"
    }
}

/**
 * The spaces of the hub on screen, shared with the sidebar through [LocalAssistantSidebarHost], read while [enabled]. Null
 * without a [management] (demo mode, previews), where nothing about spaces is drawn.
 */
@Composable
internal fun rememberSpaces(hubId: String?, management: AssistantManagement?, enabled: Boolean = true): SpacesModel? {
    val host = LocalAssistantSidebarHost.current
    val model = remember(host, hubId) { host?.spaces(hubId) ?: SpacesModel() }
    // The tracks are the default manager's, whichever assistant the page shows (docs/assistant/managers.md "Clients").
    val api = remember(management, hubId) { management?.forManager(null) }
    val hello = LocalAssistantHost.current?.hello?.collectAsStateWithLifecycle()?.value?.generation
    val scope = rememberCoroutineScope()
    LaunchedEffect(model, api, hello, enabled) { if (enabled && api != null) model.load(api) }
    androidx.lifecycle.compose.LifecycleEventEffect(androidx.lifecycle.Lifecycle.Event.ON_RESUME) {
        if (enabled && api != null) scope.launch { model.load(api) }
    }
    return model.takeIf { enabled && api != null }
}
