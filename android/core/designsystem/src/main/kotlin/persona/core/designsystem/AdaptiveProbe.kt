package persona.core.designsystem

import android.content.Context
import android.content.pm.ApplicationInfo
import android.os.Process
import android.os.SystemClock
import android.util.AtomicFile
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.text.input.TextFieldValue
import java.io.File
import java.security.SecureRandom
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.serialization.Serializable
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.encodeToString
import kotlin.math.roundToInt
import persona.core.contracts.JsonCodec

private const val ADAPTIVE_PROBE_VERSION = 1
private const val ADAPTIVE_PROBE_FILE = "hub-data/adaptive-probe.json"
private const val ADAPTIVE_HMAC_ALGORITHM = "HmacSHA256"

@Serializable
internal data class AdaptiveListEvidence(
    val firstVisibleKey: String? = null,
    val firstVisibleOffsetPx: Int? = null,
    val columns: Int? = null,
)

@Serializable
internal data class AdaptiveSelectionEvidence(val start: Int, val end: Int)

@Serializable
internal data class AdaptiveAttachmentEvidence(val id: String, val uploadState: String)

@Serializable
internal data class AdaptiveComposerEvidence(
    val draftHmac: String? = null,
    val selection: AdaptiveSelectionEvidence? = null,
    val focused: Boolean? = null,
    val attachments: List<AdaptiveAttachmentEvidence>? = null,
    val draftLoaded: Boolean? = null,
)

@Serializable
internal data class AdaptiveRectEvidence(val left: Int, val top: Int, val right: Int, val bottom: Int)

@Serializable
internal data class AdaptiveBoundsEvidence(val content: AdaptiveRectEvidence, val hinge: AdaptiveRectEvidence? = null)

@Serializable
internal data class AdaptivePageEvidence(
    val updatedAtNanos: Long,
    val list: AdaptiveListEvidence? = null,
    val composer: AdaptiveComposerEvidence? = null,
    val bounds: AdaptiveBoundsEvidence? = null,
)

@Serializable
internal data class AdaptiveProbeSnapshot(
    val fieldVersion: Int,
    val session: String,
    val pid: Int,
    val updatedAtNanos: Long,
    val pages: Map<String, AdaptivePageEvidence>,
)

/** Process-local, automation-only evidence. No file or snapshot listener exists while disabled. */
object AdaptiveProbe {
    private val lock = Any()
    private var file: File? = null
    private var pages = linkedMapOf<String, AdaptivePageEvidence>()
    private var session = ""
    private var hmacKey: SecretKeySpec? = null

    @Volatile
    var enabled: Boolean = false
        private set

    /** The DEBUG entry calls this only for debug.automation=true; the flag check here also excludes release builds. */
    fun enable(context: Context) {
        val app = context.applicationContext
        if (app.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE == 0) return
        synchronized(lock) {
            if (enabled) return
            val key = ByteArray(32).also(SecureRandom()::nextBytes)
            hmacKey = SecretKeySpec(key, ADAPTIVE_HMAC_ALGORITHM)
            session = java.util.UUID.randomUUID().toString()
            pages = linkedMapOf()
            file = File(app.noBackupFilesDir, ADAPTIVE_PROBE_FILE)
            file?.let { AtomicFile(it).delete() }
            enabled = true
        }
    }

    fun disable() {
        synchronized(lock) {
            enabled = false
            pages.clear()
            hmacKey = null
            session = ""
            file?.let { AtomicFile(it).delete() }
            file = null
        }
    }

    /** Compose focus is not part of TextFieldValue, so the composer's onFocusChanged reports it separately. */
    fun publishComposerFocus(page: String, focused: Boolean) {
        updateComposer(page) { it.copy(focused = focused) }
    }

    internal fun publishList(page: String, evidence: AdaptiveListEvidence) {
        publishList(page, evidence, synchronized(lock) { pages[page]?.bounds })
    }

    internal fun publishList(page: String, evidence: AdaptiveListEvidence, bounds: AdaptiveBoundsEvidence?) {
        synchronized(lock) {
            if (!enabled) return
            val old = pages[page]
            val retained = evidence.copy(columns = evidence.columns ?: old?.list?.columns)
            if (old?.list == retained && old.bounds == bounds) return
            pages[page] = AdaptivePageEvidence(now(), list = retained, composer = old?.composer, bounds = bounds)
            writeLocked()
        }
    }

    internal fun publishMachineGridColumns(page: String, columns: Int) {
        synchronized(lock) {
            if (!enabled) return
            val old = pages[page]
            val list = (old?.list ?: AdaptiveListEvidence()).copy(columns = columns)
            if (old?.list == list) return
            pages[page] = AdaptivePageEvidence(now(), list = list, composer = old?.composer, bounds = old?.bounds)
            writeLocked()
        }
    }

    internal fun removeMachineGridColumns(page: String) {
        synchronized(lock) {
            if (!enabled) return
            val old = pages[page] ?: return
            val list = old.list ?: return
            pages[page] = old.copy(updatedAtNanos = now(), list = list.copy(columns = null))
            writeLocked()
        }
    }

    internal fun publishBounds(page: String, evidence: AdaptiveBoundsEvidence) {
        synchronized(lock) {
            if (!enabled) return
            val old = pages[page]
            if (old?.bounds == evidence) return
            pages[page] = AdaptivePageEvidence(now(), list = old?.list, composer = old?.composer, bounds = evidence)
            writeLocked()
        }
    }

    internal fun publishComposer(page: String, value: TextFieldValue, attachmentStates: List<Pair<String, String>>, draftLoaded: Boolean? = null) {
        val draft = value.text.hmac()
        updateComposer(page) { old ->
            old.copy(
                draftHmac = draft,
                selection = AdaptiveSelectionEvidence(value.selection.start, value.selection.end),
                attachments = attachmentStates.map { AdaptiveAttachmentEvidence(it.first, it.second) },
                draftLoaded = draftLoaded ?: old.draftLoaded,
            )
        }
    }

    internal fun removeList(page: String) = removeEvidence(page, composer = false)
    internal fun removeComposer(page: String) = removeEvidence(page, composer = true)
    internal fun removeBounds(page: String) {
        synchronized(lock) {
            if (!enabled) return
            val old = pages[page] ?: return
            val updated = old.copy(bounds = null)
            if (updated.list == null && updated.composer == null) pages.remove(page) else pages[page] = updated
            writeLocked()
        }
    }

    internal fun touch(page: String) {
        synchronized(lock) {
            if (!enabled) return
            val old = pages[page] ?: return
            pages[page] = old.copy(updatedAtNanos = now())
            writeLocked()
        }
    }

    private fun removeEvidence(page: String, composer: Boolean) {
        synchronized(lock) {
            if (!enabled) return
            val old = pages[page] ?: return
            val updated = if (composer) old.copy(composer = null) else old.copy(list = null)
            if (updated.list == null && updated.composer == null && updated.bounds == null) pages.remove(page) else pages[page] = updated
            writeLocked()
        }
    }

    private fun updateComposer(page: String, change: (AdaptiveComposerEvidence) -> AdaptiveComposerEvidence) {
        synchronized(lock) {
            if (!enabled) return
            val oldPage = pages[page]
            val old = oldPage?.composer ?: AdaptiveComposerEvidence()
            val updated = change(old)
            if (oldPage?.composer == updated) return
            pages[page] = AdaptivePageEvidence(now(), list = oldPage?.list, composer = updated, bounds = oldPage?.bounds)
            writeLocked()
        }
    }

    private fun writeLocked() {
        val target = file ?: return
        target.parentFile?.mkdirs()
        val snapshot = AdaptiveProbeSnapshot(ADAPTIVE_PROBE_VERSION, session, Process.myPid(), now(), pages.toMap())
        val atomic = AtomicFile(target)
        val _ = runCatching {
            val output = atomic.startWrite()
            try {
                output.write(JsonCodec.encodeToString(snapshot).toByteArray(Charsets.UTF_8))
                atomic.finishWrite(output)
            } catch (error: Exception) {
                atomic.failWrite(output)
                throw error
            }
        }
    }

    private fun String.hmac(): String {
        val key = synchronized(lock) { if (enabled) hmacKey else null } ?: return ""
        val mac = Mac.getInstance(ADAPTIVE_HMAC_ALGORITHM)
        mac.init(key)
        return mac.doFinal(toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it.toInt() and 0xff) }
    }

    private fun now() = SystemClock.elapsedRealtimeNanos()
}

internal fun decodeAdaptiveProbeSnapshot(text: String, nowNanos: Long, maxAgeNanos: Long): AdaptiveProbeSnapshot? =
    runCatching { JsonCodec.decodeFromString<AdaptiveProbeSnapshot>(text) }.getOrNull()?.takeIf {
        it.fieldVersion == ADAPTIVE_PROBE_VERSION && it.session.isNotBlank() && it.pid > 0 &&
            nowNanos >= it.updatedAtNanos && nowNanos - it.updatedAtNanos <= maxAgeNanos
    }

@Composable
fun AdaptiveListProbe(page: String, state: LazyListState) {
    if (!AdaptiveProbe.enabled) return
    val window = LocalWindowLayout.current
    LaunchedEffect(page, state, window) {
        snapshotFlow {
            val item = state.layoutInfo.visibleItemsInfo.firstOrNull { !isAdaptiveChromeKey(it.key) }
            val list = AdaptiveListEvidence(item?.key?.let(::adaptiveStableKey), item?.offset)
            val viewport = state.layoutInfo.viewportSize
            val content = AdaptiveRectEvidence(0, 0, viewport.width, viewport.height)
            val hinge = if (window.tabletop && window.hingeBottom > window.hingeTop) {
                AdaptiveRectEvidence(0, window.hingeTop.roundToInt(), viewport.width, window.hingeBottom.roundToInt())
            } else null
            list to AdaptiveBoundsEvidence(content, hinge)
        }.distinctUntilChanged().collect { AdaptiveProbe.publishList(page, it.first, it.second) }
    }
    DisposableEffect(page, state) { onDispose { AdaptiveProbe.removeList(page) } }
    LaunchedEffect(page, state) {
        while (true) {
            delay(2_000)
            AdaptiveProbe.touch(page)
        }
    }
}

/** The actual grid policy, independent of how many cards happen to be in a short final row. */
@Composable
fun AdaptiveMachineGridProbe(page: String, columns: Int) {
    if (!AdaptiveProbe.enabled) return
    SideEffect { AdaptiveProbe.publishMachineGridColumns(page, columns) }
    DisposableEffect(page) { onDispose { AdaptiveProbe.removeMachineGridColumns(page) } }
}

@Composable
fun AdaptiveComposerProbe(
    page: String,
    value: TextFieldValue,
    attachmentStates: List<Pair<String, String>>,
    draftLoaded: Boolean? = null,
) {
    if (!AdaptiveProbe.enabled) return
    SideEffect { AdaptiveProbe.publishComposer(page, value, attachmentStates, draftLoaded) }
    DisposableEffect(page) { onDispose { AdaptiveProbe.removeComposer(page) } }
    LaunchedEffect(page) {
        while (true) {
            delay(2_000)
            AdaptiveProbe.touch(page)
        }
    }
}

/** Publishes page-local content and hinge rectangles for JPage and other non-list containers. */
@Composable
fun AdaptiveBoundsProbe(page: String, contentBounds: Rect, hingeBounds: Rect?) {
    if (!AdaptiveProbe.enabled) return
    SideEffect { AdaptiveProbe.publishBounds(page, AdaptiveBoundsEvidence(contentBounds.adaptiveRect(), hingeBounds?.adaptiveRect())) }
    DisposableEffect(page) { onDispose { AdaptiveProbe.removeBounds(page) } }
    LaunchedEffect(page) {
        while (true) {
            delay(2_000)
            AdaptiveProbe.touch(page)
        }
    }
}

private fun Rect.adaptiveRect() = AdaptiveRectEvidence(
    left.roundToInt(), top.roundToInt(), right.roundToInt(), bottom.roundToInt())

internal fun isAdaptiveChromeKey(key: Any): Boolean {
    val value = key as? String ?: return false
    return value in setOf("page-title", "page-header", "page-summary", "page-search", "page-filters", "page-chips", "page-actions",
        "loading", "failure", "sessions-failure") ||
        value.startsWith("chrome-") || value.startsWith("chrome:") || value.startsWith("usage-")
}

internal fun adaptiveStableKey(key: Any): String = when (key) {
    is String -> key
    is Number, is Boolean, is Char -> key.toString()
    else -> "${key::class.qualifiedName}:${key}"
}
