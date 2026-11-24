package persona.feature.settings

import android.content.Context
import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import java.io.File

/** What the app keeps on this phone, by kind (iOS `StorageKind`); everything but [AppData] can be fetched again from the hub. */
internal enum class StorageKind(@StringRes val title: Int, @StringRes val detail: Int, val icon: PhIcon, val clearable: Boolean = true) {
    ChatImages(R.string.settings_storage_images, R.string.settings_storage_images_detail, PhIcons.Image),
    InboxMedia(R.string.settings_storage_inbox, R.string.settings_storage_inbox_detail, PhIcons.Paperclip),
    Web(R.string.settings_storage_web, R.string.settings_storage_web_detail, PhIcons.GlobeSimple),
    Other(R.string.settings_storage_other, R.string.settings_storage_other_detail, PhIcons.Archive),
    AppData(R.string.settings_storage_app, R.string.settings_storage_app_detail, PhIcons.GearSix, clearable = false),
}

/**
 * Measures and clears the app's caches (iOS `StorageUsage`), from its own directories: [cache] is `cacheDir`, [data]
 * the directories that are the app's own data (files, no-backup, databases). In the cache, the list and conversation
 * databases (`items.db`, open while the app runs), attachments still to be sent (`chat-uploads`, `chat-captures`) and
 * files something holds open (an update being installed, an open PDF, a live share) count as app data and are never
 * deleted. [delete] removes a batch of cache files and keeps the ones in use (the data layer's `CacheClearing`).
 * The WebView's files are only measured: its own `clearCache` empties them.
 */
internal class StorageUsage(
    private val cache: File,
    private val data: List<File>,
    private val delete: (List<File>) -> Unit = { files -> files.forEach { it.delete() } },
) {
    fun kindOf(file: File): StorageKind {
        val parts = file.relativeTo(cache).invariantSeparatorsPath.split('/')
        val top = parts.firstOrNull().orEmpty()
        return when {
            top == "hub-data" -> when {
                file.name.startsWith("items.db") -> StorageKind.AppData
                "images" in parts -> StorageKind.ChatImages
                "inbox-media" in parts -> StorageKind.InboxMedia
                else -> StorageKind.Other
            }
            top == "inbox-downloads" -> StorageKind.InboxMedia
            top == "WebView" || top == "org.chromium.android_webview" -> StorageKind.Web
            // A draft's photo and an attachment still being sent: kept until the message goes.
            top == "chat-uploads" || top == "chat-captures" -> StorageKind.AppData
            // Held open by something else: an open PDF, a file handed to another app, an update being installed.
            top == "files-pdf" || top == "files-share" || top == "shared" -> StorageKind.AppData
            persona.core.contracts.ProtocolCompatibility.isUpdateAPK(top) -> StorageKind.AppData
            else -> StorageKind.Other
        }
    }

    /** Bytes on disk per kind; every kind is present (0 when empty). */
    fun scan(): Map<StorageKind, Long> {
        val out = StorageKind.entries.associateWith { 0L }.toMutableMap()
        cache.walkTopDown().filter { it.isFile }.forEach { out[kindOf(it)] = out.getValue(kindOf(it)) + it.length() }
        data.forEach { root -> out[StorageKind.AppData] = out.getValue(StorageKind.AppData) + root.walkTopDown().filter { it.isFile }.sumOf { it.length() } }
        return out
    }

    /** Deletes the cache files of [kinds] (app data and the WebView's files never), then the directories they leave empty. */
    fun clear(kinds: Set<StorageKind>) {
        val wanted = kinds.filter { it.clearable && it != StorageKind.Web }.toSet()
        if (wanted.isEmpty()) return
        val files = cache.walkTopDown().filter { it.isFile && kindOf(it) in wanted }
            // A write in progress (a download's `.part`, a put's `.tmp`) is renamed into place when it finishes.
            .filterNot { it.name.endsWith(".part") || it.name.endsWith(".tmp") }.toList()
        delete(files)
        cache.walkBottomUp().forEach { f ->
            if (f.isDirectory && f != cache && f.list()?.isEmpty() == true && kindOf(f) in wanted) f.delete()
        }
    }

    companion object {
        fun of(context: Context, delete: ((List<File>) -> Unit)? = null): StorageUsage = StorageUsage(context.cacheDir,
            listOfNotNull(context.filesDir, context.noBackupFilesDir, context.getDatabasePath("x").parentFile, File(context.applicationInfo.dataDir, "app_webview")),
            delete ?: { files -> files.forEach { it.delete() } })
    }
}

internal fun storageText(context: Context, bytes: Long): String = android.text.format.Formatter.formatShortFileSize(context, bytes)

/** Each kind's colour in the bar, from the palette's role colours. */
@Composable
private fun StorageKind.color(): Color {
    val p = PersonaTheme.palette
    return when (this) {
        StorageKind.ChatImages -> p.ai
        StorageKind.InboxMedia -> p.done.dot
        StorageKind.Web -> p.needsYou.dot
        StorageKind.Other -> p.working.dot
        StorageKind.AppData -> p.idle.dot
    }
}

/**
 * Settings › Storage (iOS `StorageView`): how much the app keeps on this phone, by kind, with one bar like the system's,
 * and clearing one kind or every cache; settings and the pairing stay.
 */
@Composable
internal fun StoragePage(onBack: () -> Unit, clearCacheFiles: ((List<File>) -> Unit)? = null, footer: @Composable () -> Unit = {}) {
    val context = LocalContext.current
    val usage = remember(context, clearCacheFiles) { StorageUsage.of(context, clearCacheFiles) }
    val scope = rememberCoroutineScope()
    var sizes by remember { mutableStateOf<Map<StorageKind, Long>?>(null) }
    var clearing by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf<Set<StorageKind>?>(null) }
    suspend fun measure() { sizes = withContext(Dispatchers.IO) { usage.scan() } }
    LaunchedEffect(usage) { measure() }
    val total = sizes?.values?.sum() ?: 0L
    val cacheTotal = sizes?.filterKeys { it.clearable }?.values?.sum() ?: 0L
    JListPage(title = stringResource(R.string.settings_storage_title), onBack = onBack, onRefresh = { scope.launch { measure() } }) {
        item("total") {
            JCard(Modifier.padding(top = Tokens.Space.s)) {
                Row(verticalAlignment = androidx.compose.ui.Alignment.Bottom) {
                    Text(sizes?.let { storageText(context, total) } ?: stringResource(R.string.settings_storage_measuring),
                        Modifier.weight(1f), style = PersonaTheme.type.largeTitle, color = PersonaTheme.palette.ink, maxLines = 1)
                    if (sizes != null) Text(stringResource(R.string.settings_storage_cache_part, storageText(context, cacheTotal)),
                        style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, maxLines = 1)
                }
                Spacer(Modifier.height(Tokens.Space.m))
                StorageBar(sizes, total)
            }
        }
        item("about") {
            SettingsCaption(stringResource(R.string.settings_storage_about), Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
        }
        StorageKind.entries.forEachIndexed { index, kind ->
            item("kind-${kind.name}") {
                val bytes = sizes?.get(kind)
                JGroupItem(GroupPosition.of(index, StorageKind.entries.size)) {
                    JRow(stringResource(kind.title), subtitle = stringResource(kind.detail), leadingIcon = kind.icon,
                        value = bytes?.let { storageText(context, it) } ?: "—", chevron = false,
                        onClick = if (kind.clearable && (bytes ?: 0) > 0 && !clearing) ({ confirm = setOf(kind) }) else null)
                }
            }
        }
        item("clear-all") {
            JButton(stringResource(R.string.settings_storage_clear_all), { confirm = StorageKind.entries.filter { it.clearable }.toSet() },
                Modifier.fillMaxWidth().padding(top = Tokens.Space.section), kind = JButtonKind.Destructive,
                enabled = sizes != null && !clearing && cacheTotal > 0, loading = clearing)
        }
        item("footer") { footer() }
    }
    confirm?.let { kinds ->
        val bytes = sizes?.filterKeys { it in kinds }?.values?.sum() ?: 0L
        val one = kinds.singleOrNull()
        JDialog(onDismiss = { confirm = null },
            title = if (one != null) stringResource(R.string.settings_storage_clear_one, stringResource(one.title), storageText(context, bytes))
                else stringResource(R.string.settings_storage_clear_all_title, storageText(context, bytes)),
            text = stringResource(R.string.settings_storage_clear_text), destructive = true,
            confirm = stringResource(R.string.settings_storage_clear) to {
                confirm = null
                scope.launch {
                    clearing = true
                    try {
                        // The WebView's own cache goes through its API (StorageUsage leaves its files alone).
                        if (StorageKind.Web in kinds) { val _ = runCatching { android.webkit.WebView(context).apply { clearCache(true); destroy() } } }
                        withContext(Dispatchers.IO) { usage.clear(kinds) }
                        measure()
                    } finally { clearing = false }
                }
                Unit
            })
    }
}

/** Like the system's storage page: one bar, a segment per kind. Decoration: the rows say the same in words. */
@Composable
private fun StorageBar(sizes: Map<StorageKind, Long>?, total: Long) {
    val palette = PersonaTheme.palette
    Row(Modifier.fillMaxWidth().height(Tokens.Space.s).clip(RoundedCornerShape(Tokens.Space.xs)).background(palette.surfaceStrong)
        .clearAndSetSemantics {}, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        if (sizes != null && total > 0) StorageKind.entries.forEach { kind ->
            val bytes = sizes[kind] ?: 0L
            if (bytes > 0) Box(Modifier.weight(maxOf(bytes.toFloat() / total, 0.01f)).fillMaxHeight().background(kind.color()))
        }
    }
}

/** Settings' "Storage" row, with how much the app takes (measured each time the list shows). */
@Composable
internal fun StorageRow(selected: Boolean, onClick: () -> Unit) {
    val context = LocalContext.current
    val total by produceState<Long?>(null, context) { value = withContext(Dispatchers.IO) { StorageUsage.of(context).scan().values.sum() } }
    JRow(stringResource(R.string.settings_row_storage), leadingIcon = PhIcons.Archive, selected = selected, onClick = onClick,
        value = total?.let { storageText(context, it) })
}
