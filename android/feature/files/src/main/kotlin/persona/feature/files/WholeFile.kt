package persona.feature.files

import android.content.res.Resources
import android.widget.MediaController
import android.widget.VideoView
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import persona.core.contracts.api.SessionFileContent
import persona.core.designsystem.*
import persona.core.hub.HubFailure
import java.io.File
import java.util.Base64

/**
 * A machine's file fetched whole onto the phone (web `fileBytes`, iOS `fetchBytes`): the preview's base64 stops at
 * 2 MB, so a bigger picture, PDF or video, and an Office document's PDF, come from `file/download` into this module's
 * cache. A file the preview already carries whole is written from there.
 */
internal sealed interface WholeLoad {
    data object Fetching : WholeLoad
    class Failed(val reason: String) : WholeLoad
    class Ready(val file: File) : WholeLoad
}

internal object WholeFiles {
    /** Video players, by extension or type (iOS `VideoFileType`). */
    fun isVideo(file: SessionFileContent): Boolean =
        file.text == null && (file.mime.orEmpty().startsWith("video/") || FileThumbnails.isVideo(file.name))

    /**
     * Word, PowerPoint, OpenDocument, WPS and iWork (Pages, Keynote, Numbers) shown as the PDF the machine's
     * LibreOffice writes (web `OFFICE_RENDERED` + `OFFICE_CONVERTED`, and Numbers as iOS). RTF is not here: the hub
     * sends it as text and it shows as text. xlsx opens in the table viewer.
     */
    internal val office = setOf("docx", "docm", "dotx", "doc", "dot", "pptx", "pptm", "ppsx", "potx", "ppt", "pps",
        "odt", "odp", "pages", "key", "numbers", "wps", "wpd")

    fun isOffice(file: SessionFileContent): Boolean = file.text == null && FilesPaths.extension(file.name) in office

    fun isPdf(file: SessionFileContent): Boolean =
        file.mime.orEmpty().substringBefore(';').trim() == "application/pdf" || file.text == null && FilesPaths.extension(file.name) == "pdf"

    fun isImage(file: SessionFileContent): Boolean = file.mime.orEmpty().startsWith("image/") && !file.mime.orEmpty().startsWith("image/svg")

    /** The preview has the whole file: no download needed. A streamed answer (stream=1) never does. */
    fun carried(file: SessionFileContent): Boolean = !file.stream && file.data != null && !file.truncated

    /** There are bytes to show: the preview's, the start of them (over 2 MB), or a streamed file's from file/download. */
    fun hasBytes(file: SessionFileContent): Boolean = file.stream || file.data != null || file.truncated

    /** Why fetching failed, for the page: an old node or a machine without LibreOffice say so, else the usual words. */
    fun reason(resources: Resources, error: Throwable, converting: Boolean = false): String {
        val failure = error as? HubFailure
        return when {
            converting && failure?.code == "no_converter" -> resources.getString(R.string.files_office_no_converter)
            error is UnsupportedOperationException || failure?.status == 409 ->
                resources.getString(if (converting) R.string.files_office_node_outdated else R.string.files_audio_node_outdated)
            else -> filesLoadError(resources, error)
        }
    }
}

/**
 * [file]'s bytes on the phone, in `cacheDir/[folder]`: from the preview when it carries them whole (and [fetch] is not
 * [always] needed), else by [fetch]. Only the last [keep] finished files stay in the folder.
 */
@Composable
internal fun rememberWholeFile(file: SessionFileContent, folder: String, fetch: suspend (into: File) -> Long,
    always: Boolean = false, converting: Boolean = false, keep: Int = 2): State<WholeLoad> {
    val context = LocalContext.current
    val resources = LocalResources.current
    return produceState<WholeLoad>(WholeLoad.Fetching, file.path, file.size, file.revision, always) {
        // produceState keeps its value when the keys change: another file is not the last one's.
        value = WholeLoad.Fetching
        value = withContext(Dispatchers.IO) {
            val dir = File(context.cacheDir, folder).apply { mkdirs() }
            val into = File(dir, AudioFiles.cacheName(file.path + if (converting) "\u0000pdf" else "", file.size, if (converting) "a.pdf" else file.name, file.revision))
            if (into.exists() && (converting || into.length() == file.size)) return@withContext WholeLoad.Ready(into.also { it.setLastModified(System.currentTimeMillis()) })
            // Parts another page is still writing stay; finished files beyond the newest few go.
            dir.listFiles()?.filter { it != into && !it.name.contains(".part") }?.sortedByDescending { it.lastModified() }
                ?.drop(keep - 1)?.forEach { it.delete() }
            fun fromPreview(): WholeLoad {
                val part = File(into.path + ".part-" + System.nanoTime())
                part.writeBytes(Base64.getMimeDecoder().decode(file.data.orEmpty()))
                return if (part.renameTo(into)) WholeLoad.Ready(into) else WholeLoad.Failed(resources.getString(R.string.files_audio_cache_failed)).also { part.delete() }
            }
            if (!always && WholeFiles.carried(file)) return@withContext runCatching { fromPreview() }
                .getOrElse { WholeLoad.Failed(resources.getString(R.string.files_audio_cache_failed)) }
            try {
                val _ = fetch(into)
                WholeLoad.Ready(into)
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                WholeLoad.Failed(WholeFiles.reason(resources, e, converting))
            }
        }
    }
}

/**
 * A video from the machine (web `MediaView`, iOS `VideoFileView`): fetched whole, then the system's VideoView plays it
 * with its controls under it. The app going to the background pauses it; a codec the phone can't play says so.
 */
@Composable
internal fun VideoFile(file: SessionFileContent, download: suspend (path: String, into: File) -> Long, contentPadding: PaddingValues,
    header: LazyListScope.() -> Unit) {
    val load by rememberWholeFile(file, "files-video", { into -> download(file.path, into) }, keep = 1)
    LazyColumn(Modifier.fillMaxSize(), contentPadding = contentPadding) {
        header()
        item("video") {
            Box(Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.l).testTag("video"), contentAlignment = Alignment.Center) {
                when (val state = load) {
                    WholeLoad.Fetching -> JLoading(text = stringResource(R.string.files_video_loading))
                    is WholeLoad.Failed -> JEmptyState(stringResource(R.string.files_video_failed), state.reason)
                    is WholeLoad.Ready -> VideoPlayer(state.file)
                }
            }
        }
    }
}

@Composable
private fun VideoPlayer(file: File) {
    var failed by remember(file) { mutableStateOf(false) }
    if (failed) {
        JEmptyState(stringResource(R.string.files_video_failed), stringResource(R.string.files_video_codec))
        return
    }
    var view by remember(file) { mutableStateOf<VideoView?>(null) }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle, view) {
        val observer = LifecycleEventObserver { _, event -> if (event == Lifecycle.Event.ON_STOP) { val _ = runCatching { view?.pause() } } }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    DisposableEffect(file) { onDispose { val _ = runCatching { view?.stopPlayback() } } }
    AndroidView(
        factory = { context ->
            VideoView(context).apply {
                val controls = MediaController(context)
                controls.setAnchorView(this)
                setMediaController(controls)
                setOnErrorListener { _, _, _ -> failed = true; true }
                // The first frame instead of a black box until it plays.
                setOnPreparedListener { player -> player.isLooping = false; seekTo(1) }
                setVideoPath(file.path)
                view = this
            }
        },
        modifier = Modifier.fillMaxWidth().aspectRatio(16f / 9f).background(PersonaTheme.palette.sunken),
    )
}
