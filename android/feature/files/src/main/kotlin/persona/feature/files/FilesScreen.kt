package persona.feature.files

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.flow.StateFlow
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.ChatRoute
import java.time.Instant

/**
 * Embedded in the chat destination; Files/Git are panes, not navigation entries.
 *
 * [initialPath] opens a path the conversation mentioned (a file label, a tool's path, a diff's file header): relative
 * paths are in [cwd], "~…" goes to the hub as it is. A file opens straight in its preview and back returns to the
 * conversation; a folder (a trailing slash, or the hub answering `is_dir`) opens in the browser as its top.
 * [initialLine] (a message's `path:12`) opens the file's text at that line, marked.
 */
@Composable
fun SessionFilesScreen(
    session: ChatRoute,
    cwd: String,
    api: SessionFiles,
    onBackToChat: () -> Unit,
    onInsertPath: (String) -> Unit,
    initialPane: FilesPane = FilesPane.Files,
    modifier: Modifier = Modifier,
    initialPath: String? = null,
    initialLine: Int? = null,
    /** The hub connection: the pane reads again what failed once it is back online. */
    connection: StateFlow<ConnectionState>? = null,
) {
    val asked = initialPath?.trim()?.takeIf { it.isNotEmpty() }
    // A path from the conversation that names its machine fully can open before the session's folder is known.
    val base = cwd.ifBlank { asked?.takeIf { it.startsWith("/") || FilesPaths.isHome(it) }?.let { start ->
        if (FilesPaths.namesDirectory(start)) FilesPaths.resolve(start, "/") else FilesPaths.parent(FilesPaths.resolve(start, "/")) ?: "/"
    }.orEmpty() }
    key(session, base, api, asked, initialLine, connection) {
        if (base.isBlank()) {
            JListPage(stringResource(R.string.files_title), modifier, onBack = onBackToChat) {
                item { JEmptyState(stringResource(R.string.files_no_folder_title), stringResource(R.string.files_no_folder_message),
                    action = { JButton(stringResource(R.string.files_back_to_chat), onBackToChat) }) }
            }
        } else {
            val scope = rememberCoroutineScope()
            val resources = LocalResources.current
            val controller = remember { FilesController(api, session.machineId, base, scope, { filesLoadError(resources, it) },
                { resources.getString(if (it) R.string.files_git_pulled else R.string.files_git_pull_current) }, connection) }
            val images = remember { MarkdownImages(api, session.machineId, base) }
            val state by controller.state.collectAsStateWithLifecycle()
            var pane by rememberSaveable { mutableStateOf(if (asked != null) FilesPane.Files else initialPane) }
            LaunchedEffect(controller) { controller.start(asked, initialLine) }
            val back: () -> Unit = { if (!controller.back(pane)) onBackToChat() }
            BackHandler(onBack = back)
            Box(modifier.fillMaxSize()) {
                when (val top = state.selection) {
                    null -> FilesBrowser(state, controller, pane, { pane = it }, onBackToChat, onInsertPath)
                    is FileSelection.Commit -> CommitPage(top.hash, state, controller, back)
                    is FileSelection.CommitFile -> CommitFilePage(top, state, controller, back)
                    is FileSelection.Compare -> ComparePage(top, state, controller, back)
                    else -> FilePreview(state, controller, images, onInsertPath, back)
                }
            }
        }
    }
}

@Composable
private fun FilesBrowser(
    state: FilesState,
    controller: FilesController,
    pane: FilesPane,
    onPane: (FilesPane) -> Unit,
    onBackToChat: () -> Unit,
    onInsertPath: (String) -> Unit,
) {
    val copy = rememberCopy()
    val pathCopied = stringResource(R.string.files_path_copied)
    var hidden by rememberShowHidden()
    var menu by remember { mutableStateOf(false) }
    var renaming by rememberSaveable { mutableStateOf<String?>(null) }
    var trashing by rememberSaveable { mutableStateOf<String?>(null) }
    var moving by remember { mutableStateOf<MoveSource?>(null) }
    val moveStatus by controller.moveStatus.collectAsStateWithLifecycle()
    val report = rememberFileEditReport(controller)
    val edits = controller.editsFiles
    val here = state.here
    JListPage(
        title = FilesPaths.name(here), subtitle = here, onBack = onBackToChat, largeTitle = true,
        actions = { JIconButton(PhIcons.ArrowClockwise, stringResource(R.string.files_refresh), { controller.directory(); controller.refreshGit() },
            enabled = !state.directoryLoading && !state.gitLoading) },
        bottomBar = if (moveStatus == FileMove.Pending) ({
            Box(Modifier.semantics { liveRegion = LiveRegionMode.Polite }) { Notice(stringResource(R.string.files_moving)) }
        }) else null,
    ) {
        item("panes") {
            val changes = state.git?.files?.size?.takeIf { it > 0 }
            // Git stays offered while its status loads (the chat menu only opens it for repositories); it goes when there is none.
            val chat = JSegment(stringResource(R.string.files_pane_chat))
            val files = JSegment(stringResource(R.string.files_pane_files))
            val options = if (state.git?.isRepo == false) listOf(chat, files)
                else listOf(chat, files, JSegment("Git", count = changes))
            JSegmented(options, if (pane == FilesPane.Files || options.size < 3) 1 else 2,
                { when (it) { 0 -> onBackToChat(); 1 -> onPane(FilesPane.Files); else -> onPane(FilesPane.Git) } },
                Modifier.padding(vertical = Tokens.Space.s))
        }
        if (pane == FilesPane.Files || state.git?.isRepo == false) {
            item("crumbs") {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    if (state.belowRoot) JIconButton(PhIcons.ArrowUp, stringResource(R.string.files_up), controller::up)
                    Crumbs(state.crumbs, onPick = { controller.directory(it) }, modifier = Modifier.weight(1f))
                    JIconButton(PhIcons.DotsThree, stringResource(R.string.files_options), { menu = true })
                }
            }
            val canEdit = edits && moveStatus == null
            filesBrowser(state, controller, hidden, onInsertPath, rename = if (canEdit) ({ renaming = it }) else null,
                move = if (canEdit) ({ moving = it }) else null,
                trash = if (canEdit) ({ trashing = it }) else null)
        } else gitBrowser(state, controller)
    }
    if (menu) JActionSheet(buildList {
        add(JAction(stringResource(R.string.files_show_hidden), PhIcons.Eye, checked = hidden) { hidden = !hidden })
        state.parent?.let { add(JAction(stringResource(R.string.files_parent_folder), PhIcons.ArrowUp) { controller.up() }) }
        add(JAction(stringResource(R.string.files_copy_folder), PhIcons.Copy) { copy(here, pathCopied) })
        add(JAction(stringResource(R.string.files_insert_folder), PhIcons.CursorText) { onInsertPath(here) })
    }, { menu = false }, title = stringResource(R.string.files_folder_actions))
    renaming?.let { path -> RenameFileDialog(FilesPaths.name(path), { controller.rename(path, it, report) }, { renaming = null }) }
    trashing?.let { path -> TrashFileDialog(FilesPaths.name(path), { controller.trash(listOf(path), report) }, { trashing = null }) }
    moving?.let { source ->
        MoveDestinationSheet(source, controller, hidden, onDismiss = { moving = null }) { destination ->
            moving = null
            controller.move(source, destination, report)
        }
    }
    (moveStatus as? FileMove.Conflict)?.let { conflict ->
        MoveConflictDialog(conflict) { policy -> controller.answerMove(conflict, policy, report) }
    }
}

/** The folders from the breadcrumb's top down to the one shown, as chips; it follows you to the end as you go in. */
@Composable
private fun Crumbs(levels: List<Pair<String, String>>, onPick: (String) -> Unit, modifier: Modifier) {
    val list = rememberLazyListState()
    LaunchedEffect(levels.size, levels.lastOrNull()) { if (levels.isNotEmpty()) list.animateScrollToItem(levels.lastIndex) }
    LazyRow(modifier, list, verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        itemsIndexed(levels, key = { _, (_, path) -> path }) { index, (name, path) ->
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                if (index > 0) JIcon(PhIcons.CaretRight, null, size = Tokens.Space.m, tint = PersonaTheme.palette.ink3)
                val current = index == levels.lastIndex
                JChip(name, selected = current, onClick = { if (!current) onPick(path) })
            }
        }
    }
}

/** File changes are offered through each row's menu when the hub supports `file_edit`. */
private fun LazyListScope.filesBrowser(state: FilesState, controller: FilesController, hidden: Boolean, insert: (String) -> Unit,
    rename: ((String) -> Unit)? = null, move: ((MoveSource) -> Unit)? = null, trash: ((String) -> Unit)? = null) {
    val listing = state.directory
    when {
        listing == null && state.directoryError != null -> item { RetryState(stringResource(R.string.files_folder_failed), state.directoryError) { controller.directory() } }
        listing == null -> item { Loading() }
        else -> {
            if (state.directoryError != null) item("stale") { Notice(stringResource(R.string.files_refresh_failed, state.directoryError), padded = false) }
            val entries = listing.entries.filter { hidden || !it.name.startsWith('.') }
                .sortedWith(compareByDescending<SessionFileEntry> { it.dir }.thenBy { it.name.lowercase() })
            if (entries.isEmpty()) item {
                if (listing.entries.isEmpty()) JEmptyState(stringResource(R.string.files_empty_folder), stringResource(R.string.files_empty_folder_message))
                else JEmptyState(stringResource(R.string.files_only_hidden), stringResource(R.string.files_only_hidden_message))
            }
            val now = Instant.now()
            itemsIndexed(entries, key = { _, it -> "entry:${it.name}" }, contentType = { _, _ -> "entry" }) { index, entry ->
                val copyText = rememberCopy()
                val pathCopied = stringResource(R.string.files_path_copied)
                val path = joinRemotePath(listing.path.ifEmpty { state.here }, entry.name)
                JGroupItem(GroupPosition.of(index, entries.size)) {
                    // Pictures, documents and videos show the machine's thumbnail in place of the icon (iOS FilesPane, #1370).
                    val thumbnail = if (!entry.dir && FileThumbnails.wanted(entry.name, entry.size)) controller.thumbnail(path, entry.size, entry.mtime) else null
                    FileRow(entry, gitChange(state.git, path, entry.dir), now, thumbnail = thumbnail,
                        open = { if (entry.dir) controller.directory(path) else controller.push(FileSelection.File(path)) },
                        copy = { copyText(path, pathCopied) }, insert = { insert(path) },
                        rename = rename?.let { { it(path) } }, move = move?.let { { it(MoveSource(path, entry.dir)) } },
                        trash = trash?.let { { it(path) } })
                }
            }
            if (listing.truncated) item { Notice(stringResource(R.string.files_too_many, LocalizedFormats.number(MAX_LISTED, currentLocale)), padded = false) }
        }
    }
}

/**
 * One entry: its kind, the name (a hidden one quieter), size and when it changed, the Git change, ⋯. Its menu (⋯ or a long
 * press) inserts or copies the path and offers the available file changes (iOS `FilesPane.entryRow`).
 */
@Composable
private fun FileRow(entry: SessionFileEntry, change: SessionGitFile?, now: Instant, open: () -> Unit, copy: () -> Unit, insert: () -> Unit,
    thumbnail: Pair<String, suspend (Int) -> ByteArray?>? = null, rename: (() -> Unit)? = null, move: (() -> Unit)? = null,
    trash: (() -> Unit)? = null) {
    var menu by remember { mutableStateOf(false) }
    val palette = PersonaTheme.palette
    val resources = LocalResources.current
    val meta = listOfNotNull(if (entry.dir) stringResource(R.string.files_folder) else fileSize(entry.size, currentLocale),
        fileDate(resources, entry.mtime, now), if (entry.symlink) stringResource(R.string.files_symlink) else null)
    JRow(onClick = open, onLongClick = { menu = true }, trailing = {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (change != null) {
                if (entry.dir) JTag(stringResource(R.string.files_changed), status = if (change.conflicted) Status.NeedsYou else Status.Unknown) else GitLetter(change)
            }
            JIconButton(PhIcons.DotsThree, stringResource(R.string.files_entry_actions, entry.name), { menu = true })
        }
    }) {
        if (thumbnail != null) FileThumbnailIcon(thumbnail.first, fileIcon(entry.name), thumbnail.second)
        else JIcon(if (entry.dir) PhIcons.FolderSimple else fileIcon(entry.name), null, tint = palette.ink2, decorative = true)
        Spacer(Modifier.width(Tokens.Space.l))
        Column(Modifier.weight(1f)) {
            Text(entry.name, style = PersonaTheme.type.body, color = if (entry.name.startsWith('.')) palette.ink2 else palette.ink,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(meta.joinToString("  "), style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
    if (menu) JActionSheet(buildList {
        add(JAction(stringResource(R.string.files_insert_path), PhIcons.CursorText, onClick = insert))
        add(JAction(stringResource(R.string.files_copy_path), PhIcons.Copy, onClick = copy))
        rename?.let { add(JAction(stringResource(R.string.files_rename), PersonaIcons.edit, separatorBefore = true, onClick = it)) }
        move?.let { add(JAction(stringResource(R.string.files_move), PersonaIcons.folder, separatorBefore = rename == null, onClick = it)) }
        trash?.let { add(JAction(stringResource(R.string.files_trash), PersonaIcons.delete, destructive = true, separatorBefore = rename == null && move == null, onClick = it)) }
    }, { menu = false }, entry.name)
}

/** The icon of a file by its kind: documents, images, code and config, anything else. */
internal fun fileIcon(name: String): PhIcon = when (FilesPaths.extension(name)) {
    "md", "markdown", "mdx", "txt", "rtf", "pdf" -> PhIcons.FileText
    "png", "jpg", "jpeg", "gif", "webp", "heic", "bmp", "svg" -> PhIcons.Image
    "kt", "kts", "java", "go", "rs", "swift", "ts", "tsx", "js", "jsx", "py", "rb", "c", "h", "cpp", "m", "sh", "zsh",
    "json", "yaml", "yml", "toml", "xml", "html", "css", "sql", "gradle", "plist" -> PhIcons.Code
    else -> PhIcons.File
}

@Composable internal fun OneLine(text: String) { Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis) }

/** A line of explanation. [padded]: on its own the page's side margin; inside a list page that already has it, none. */
@Composable internal fun Notice(text: String, padded: Boolean = true) {
    Text(text, Modifier.padding(horizontal = if (padded) pagePadding else Tokens.Space.xs, vertical = Tokens.Space.s),
        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
}

@Composable internal fun Loading() { JLoading() }

@Composable internal fun RetryState(title: String, message: String, retry: () -> Unit) {
    JEmptyState(title, message, action = { JButton(stringResource(R.string.files_retry), retry, kind = JButtonKind.Secondary) })
}

/** The most entries a folder listing brings (the node cuts it there and says it's truncated). */
internal const val MAX_LISTED = 2000
