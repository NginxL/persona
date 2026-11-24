package persona.feature.files

import android.media.MediaPlayer
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import persona.core.contracts.api.SessionFileContent
import persona.core.hub.HubFailure
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import java.io.File
import java.security.MessageDigest
import java.util.Base64
import java.util.Locale

/** Audio in the file viewer (docs/android/android.md "Files and Git in chat", web `ui/Media.tsx`): what the player knows to open. */
internal object AudioFiles {
    private val extensions = setOf("mp3", "m4a", "aac", "wav", "ogg", "oga", "opus", "flac", "amr", "3gp", "mid", "midi")

    fun isAudio(file: SessionFileContent): Boolean =
        file.text == null && (file.mime.orEmpty().startsWith("audio/") || FilesPaths.extension(file.name) in extensions)

    /**
     * Where a file's bytes are kept while it plays: by machine path, size and the hub's [revision] (when it sends one),
     * so a changed file is fetched again, even at the same size.
     */
    fun cacheName(path: String, size: Long, name: String, revision: String? = null): String {
        val key = "$path\u0000$size" + if (revision == null) "" else "\u0000$revision"
        val digest = MessageDigest.getInstance("SHA-256").digest(key.toByteArray()).joinToString("") { "%02x".format(it) }
        val ext = FilesPaths.extension(name).filter { it.isLetterOrDigit() }.take(8)
        return digest.take(24) + if (ext.isEmpty()) "" else ".$ext"
    }

    /** A time as the player shows it: m:ss, or h:mm:ss from an hour. */
    fun clock(ms: Long): String {
        val s = (ms.coerceAtLeast(0) / 1000)
        return if (s >= 3600) "%d:%02d:%02d".format(Locale.ROOT, s / 3600, s / 60 % 60, s % 60) else "%d:%02d".format(Locale.ROOT, s / 60, s % 60)
    }
}

private sealed interface AudioLoad {
    data object Fetching : AudioLoad
    class Failed(val reason: String) : AudioLoad
    class Ready(val file: File) : AudioLoad
}

/**
 * An audio file played on the phone: its whole bytes come from the hub's file/download (the preview's base64 stops
 * at 2 MB) into this module's cache, then MediaPlayer plays them. Play / pause, a seek bar and the times; leaving
 * the page stops it. A codec the phone can't play says so.
 */
@Composable
internal fun AudioFile(file: SessionFileContent, download: suspend (path: String, into: File) -> Long, contentPadding: PaddingValues,
    header: LazyListScope.() -> Unit) {
    val context = LocalContext.current
    val resources = LocalResources.current
    val load by produceState<AudioLoad>(AudioLoad.Fetching, file.path, file.size, file.revision) {
        value = withContext(Dispatchers.IO) {
            val dir = File(context.cacheDir, "files-audio").apply { mkdirs() }
            val into = File(dir, AudioFiles.cacheName(file.path, file.size, file.name, file.revision))
            if (into.exists() && into.length() == file.size) return@withContext AudioLoad.Ready(into)
            // Only the last finished file stays (audio can be large; the page fetches again when it's gone). Parts
            // another page is still writing are left alone.
            dir.listFiles()?.forEach { if (it != into && !it.name.contains(".part")) it.delete() }
            try {
                val _ = download(file.path, into)
                AudioLoad.Ready(into)
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                // A transport without file/download, or a machine whose node is too old for it (409 node_outdated):
                // the preview's bytes, when they are the whole file.
                val data = file.data
                val older = e is UnsupportedOperationException || (e as? HubFailure)?.status == 409
                if (older && data != null && !file.truncated) {
                    val part = File(into.path + ".part-" + System.nanoTime())
                    part.writeBytes(Base64.getMimeDecoder().decode(data))
                    if (part.renameTo(into)) AudioLoad.Ready(into) else AudioLoad.Failed(resources.getString(R.string.files_audio_cache_failed)).also { part.delete() }
                } else AudioLoad.Failed(if (older) resources.getString(R.string.files_audio_node_outdated) else filesLoadError(resources, e))
            }
        }
    }
    LazyColumn(Modifier.fillMaxSize(), contentPadding = contentPadding) {
        header()
        item("player") {
            Box(Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.l)) {
                when (val state = load) {
                    AudioLoad.Fetching -> AudioCard(file.name) { JLoading(text = stringResource(R.string.files_audio_loading)) }
                    is AudioLoad.Failed -> JEmptyState(stringResource(R.string.files_audio_failed), state.reason)
                    is AudioLoad.Ready -> AudioPlayer(file.name, state.file)
                }
            }
        }
    }
}

@Composable
private fun AudioCard(name: String, content: @Composable ColumnScope.() -> Unit) {
    val palette = PersonaTheme.palette
    JCard(Modifier.fillMaxWidth().testTag("audio")) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            JIcon(PhIcons.Waveform, null, tint = palette.accent, decorative = true)
            Text(name, Modifier.weight(1f), style = PersonaTheme.type.bodyStrong, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        Spacer(Modifier.height(Tokens.Space.m))
        content()
    }
}

/** MediaPlayer over a local file, with its state for the controls; released with the page. */
@Composable
internal fun AudioPlayer(name: String, file: File) {
    var player by remember(file) { mutableStateOf<MediaPlayer?>(null) }
    var failed by remember(file) { mutableStateOf(false) }
    var playing by remember(file) { mutableStateOf(false) }
    var duration by remember(file) { mutableLongStateOf(0L) }
    var position by remember(file) { mutableLongStateOf(0L) }
    var seeking by remember(file) { mutableStateOf<Float?>(null) }
    DisposableEffect(file) {
        val p = MediaPlayer()
        try {
            p.setDataSource(file.path)
            p.setOnPreparedListener { duration = it.duration.toLong().coerceAtLeast(0); player = it }
            p.setOnCompletionListener { playing = false; position = duration }
            p.setOnErrorListener { _, _, _ -> failed = true; playing = false; true }
            p.prepareAsync()
        } catch (_: Exception) {
            failed = true
        }
        onDispose { p.release(); player = null }
    }
    // The app going to the background pauses it (the page stays, so the composition doesn't end).
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle, player) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP && playing) { val _ = runCatching { player?.pause() }; playing = false }
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    LaunchedEffect(player, playing) {
        val p = player ?: return@LaunchedEffect
        while (isActive && playing) {
            position = runCatching { p.currentPosition.toLong() }.getOrDefault(position)
            delay(250)
        }
    }
    AudioCard(name) {
        if (failed) {
            Text(stringResource(R.string.files_audio_codec), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
            return@AudioCard
        }
        val p = player
        val total = duration.coerceAtLeast(1)
        JSlider(seeking ?: (position.toFloat() / total), { seeking = it }, enabled = p != null && duration > 0,
            onValueChangeFinished = {
                seeking?.let { f -> val to = (f * total).toLong(); p?.seekTo(to.toInt()); position = to }
                seeking = null
            }, contentDescription = stringResource(R.string.files_audio_progress))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(AudioFiles.clock(seeking?.let { (it * total).toLong() } ?: position), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            Spacer(Modifier.weight(1f))
            JIconButton(if (playing) PhIcons.Pause else PhIcons.Play, if (playing) stringResource(R.string.files_audio_pause) else stringResource(R.string.files_audio_play), {
                val mp = p ?: return@JIconButton
                if (playing) { mp.pause(); playing = false } else {
                    if (position >= duration) { mp.seekTo(0); position = 0 }
                    mp.start(); playing = true
                }
            }, enabled = p != null)
            Spacer(Modifier.weight(1f))
            Text(if (duration > 0) AudioFiles.clock(duration) else "--:--", style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        }
    }
}
