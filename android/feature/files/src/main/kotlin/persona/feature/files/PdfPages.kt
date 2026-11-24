package persona.feature.files

import android.graphics.Bitmap
import android.graphics.pdf.PdfRenderer
import android.os.ParcelFileDescriptor
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.IntSize
import persona.core.contracts.api.SessionFileContent
import persona.core.designsystem.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.Closeable
import java.io.File
import java.util.Base64

private sealed interface PdfLoad {
    data object Opening : PdfLoad
    data object Failed : PdfLoad
    class Ready(val document: PdfDocument) : PdfLoad
}

/**
 * A PDF from the machine, page by page down the screen (PdfRenderer reads from a file: the bytes go to this module's
 * cache while it is open). Pages render at the width they are shown, in the background, as they scroll in. [whole]:
 * the file fetched whole ([rememberWholeFile]: a PDF over 2 MB, an Office document's PDF), else the preview's bytes.
 */
@Composable
internal fun PdfPages(file: SessionFileContent, contentPadding: PaddingValues, header: LazyListScope.() -> Unit, whole: WholeLoad? = null,
    fetching: String? = null, failedTitle: String? = null) {
    val context = LocalContext.current
    val ready = (whole as? WholeLoad.Ready)?.file
    val load by produceState<PdfLoad>(PdfLoad.Opening, file.path, file.data, whole) {
        if (whole != null && ready == null) return@produceState
        val document = withContext(Dispatchers.IO) {
            if (ready != null) PdfDocument.open(ready) else PdfDocument.open(File(context.cacheDir, "files-pdf"), file.data.orEmpty())
        }
        value = document?.let(PdfLoad::Ready) ?: PdfLoad.Failed
        awaitDispose { document?.close() }
    }
    val side = pagePadding
    LazyColumn(
        Modifier.fillMaxSize().background(PersonaTheme.palette.sunken),
        contentPadding = PaddingValues(top = contentPadding.calculateTopPadding(), bottom = contentPadding.calculateBottomPadding() + Tokens.Space.xl),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m),
    ) {
        header()
        when (val state = load) {
            PdfLoad.Opening -> item {
                if (whole is WholeLoad.Failed) JEmptyState(failedTitle ?: stringResource(R.string.files_pdf_failed), whole.reason)
                else JLoading(text = fetching.takeIf { whole is WholeLoad.Fetching })
            }
            PdfLoad.Failed -> item {
                JEmptyState(failedTitle ?: stringResource(R.string.files_pdf_failed), if (file.truncated && ready == null) stringResource(R.string.files_pdf_truncated) else stringResource(R.string.files_pdf_damaged))
            }
            is PdfLoad.Ready -> items(state.document.pages.size, key = { "page:$it" }, contentType = { "page" }) { index ->
                PdfPage(state.document, index, Modifier.padding(horizontal = side))
            }
        }
    }
}

@Composable
private fun PdfPage(document: PdfDocument, index: Int, modifier: Modifier) {
    val page = document.pages[index]
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val width = constraints.maxWidth.coerceIn(1, PdfDocument.MAX_WIDTH)
        val bitmap by produceState<Bitmap?>(null, document, index, width) {
            value = withContext(Dispatchers.IO) { document.render(index, width) }
        }
        val frame = Modifier.fillMaxWidth().aspectRatio(page.width.toFloat() / page.height.coerceAtLeast(1)).shadow(Tokens.Space.xxs)
        bitmap?.let { Image(it.asImageBitmap(), stringResource(R.string.files_pdf_page, index + 1), frame) }
            ?: Box(frame.background(PersonaTheme.palette.surfaceOpaque))
    }
}

/** An open PDF: page sizes up front, one page rendered at a time (PdfRenderer allows only one open page). */
internal class PdfDocument private constructor(
    private val file: File,
    private val descriptor: ParcelFileDescriptor,
    private val renderer: PdfRenderer,
    /** A temporary copy, deleted on close; a cached whole file stays. */
    private val owned: Boolean = true,
) : Closeable {
    val pages: List<IntSize> = (0 until renderer.pageCount).map { index -> renderer.openPage(index).use { IntSize(it.width, it.height) } }
    private var closed = false

    @Synchronized
    fun render(index: Int, width: Int): Bitmap? {
        if (closed) return null
        return runCatching {
            renderer.openPage(index).use { page ->
                val height = (width.toLong() * page.height / page.width.coerceAtLeast(1)).toInt().coerceAtLeast(1)
                Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888).also { bitmap ->
                    // Pages are paper: white under the content in light and dark alike.
                    bitmap.eraseColor(android.graphics.Color.WHITE)
                    page.render(bitmap, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
                }
            }
        }.getOrNull()
    }

    @Synchronized
    override fun close() {
        if (closed) return
        closed = true
        val _ = runCatching { renderer.close() }
        val _ = runCatching { descriptor.close() }
        if (owned) file.delete()
    }

    companion object {
        /** Pixels across a rendered page at most (a tablet's width): enough to read, bounded in memory. */
        const val MAX_WIDTH = 1800

        /** A PDF already on the phone (kept after close). */
        fun open(file: File): PdfDocument? = runCatching {
            val descriptor = ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY)
            try { PdfDocument(file, descriptor, PdfRenderer(descriptor), owned = false) }
            catch (failure: Exception) { descriptor.close(); throw failure }
        }.getOrNull()

        fun open(folder: File, data: String): PdfDocument? = runCatching {
            val bytes = Base64.getMimeDecoder().decode(data)
            folder.mkdirs()
            val file = File.createTempFile("view", ".pdf", folder).apply { writeBytes(bytes) }
            val descriptor = ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY)
            try { PdfDocument(file, descriptor, PdfRenderer(descriptor)) }
            catch (failure: Exception) { descriptor.close(); file.delete(); throw failure }
        }.getOrNull()
    }
}
