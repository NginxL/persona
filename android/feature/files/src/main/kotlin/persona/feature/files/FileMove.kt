package persona.feature.files

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.api.SessionMoveConflictPolicy
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.R as DesignR

internal data class MoveSource(val path: String, val directory: Boolean) {
    fun contains(folder: String): Boolean =
        directory && FilesPaths.isWithin(FilesPaths.normalize(folder), FilesPaths.normalize(path))

    fun canMoveInto(folder: String): Boolean =
        folder.isNotBlank() && FilesPaths.normalize(folder) != FilesPaths.parent(FilesPaths.normalize(path)) && !contains(folder)
}

internal sealed interface FileMove {
    data object Pending : FileMove
    data class Conflict(val source: MoveSource, val directory: String, val name: String) : FileMove
}

/** Browse folders independently of the Files view behind the sheet. */
@Composable
internal fun MoveDestinationSheet(
    source: MoveSource,
    controller: FilesController,
    hidden: Boolean,
    onDismiss: () -> Unit,
    onMove: (String) -> Unit,
) {
    val scope = rememberCoroutineScope()
    val browser = remember(controller, source) { controller.destinationBrowser(scope) }
    val state by browser.state.collectAsStateWithLifecycle()
    LaunchedEffect(browser) { browser.directory(FilesPaths.parent(source.path) ?: controller.cwd) }
    val listing = state.directory
    val here = state.here
    val folders = remember(listing, hidden) {
        listing?.entries.orEmpty().filter { it.dir && (hidden || !it.name.startsWith('.')) }.sortedBy { it.name.lowercase() }
    }
    val ready = listing != null && !state.directoryLoading && state.directoryError == null
    val canMoveHere = ready && source.canMoveInto(here)
    JSheet(onDismiss, modifier = Modifier.testTag("files-move-destination"),
        title = stringResource(R.string.files_move_title, FilesPaths.name(source.path))) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(here, Modifier.weight(1f), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2,
                maxLines = 1, overflow = TextOverflow.MiddleEllipsis)
            JIconButton(PersonaIcons.queueUp, stringResource(R.string.files_up), browser::up, enabled = state.parent != null)
        }
        key(here) {
            LazyColumn(Modifier.fillMaxWidth().weight(1f, fill = false)) {
                val error = state.directoryError
                when {
                    error != null -> item {
                        RetryState(stringResource(R.string.files_folder_failed), error) { browser.directory() }
                    }
                    !ready -> item { Loading() }
                    else -> {
                        if (folders.isEmpty() && listing?.truncated != true) {
                            item { Notice(stringResource(R.string.files_move_no_folders), padded = false) }
                        }
                        itemsIndexed(folders, key = { _, folder -> folder.name }) { index, folder ->
                            val path = joinRemotePath(here, folder.name)
                            val blocked = source.contains(path)
                            JGroupItem(GroupPosition.of(index, folders.size)) {
                                JRow(folder.name, leadingIcon = PersonaIcons.folder, onClick = { browser.directory(path) },
                                    enabled = !blocked, chevron = !blocked)
                            }
                        }
                        if (listing?.truncated == true) item {
                            Notice(stringResource(R.string.files_too_many, LocalizedFormats.number(MAX_LISTED, currentLocale)), padded = false)
                        }
                    }
                }
            }
        }
        if (ready && !canMoveHere) {
            Notice(stringResource(if (source.contains(here)) R.string.files_move_inside_itself else R.string.files_move_same_folder), padded = false)
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JButton(stringResource(DesignR.string.designsystem_cancel), onDismiss, Modifier.weight(1f), kind = JButtonKind.Secondary)
            JButton(stringResource(R.string.files_move_here), { onMove(here) }, Modifier.weight(1f),
                enabled = canMoveHere)
        }
    }
}

@Composable
internal fun MoveConflictDialog(conflict: FileMove.Conflict, answer: (SessionMoveConflictPolicy?) -> Unit) {
    val cancel = { answer(null) }
    JDialog(cancel, title = stringResource(R.string.files_edit_exists, conflict.name),
        text = stringResource(R.string.files_move_conflict_message), dismiss = null) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JButton(stringResource(R.string.files_move_keep_both), { answer(SessionMoveConflictPolicy.KeepBoth) }, Modifier.fillMaxWidth())
            JButton(stringResource(R.string.files_move_replace), { answer(SessionMoveConflictPolicy.Replace) }, Modifier.fillMaxWidth(),
                kind = JButtonKind.Destructive)
            JButton(stringResource(DesignR.string.designsystem_cancel), cancel, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary)
        }
    }
}
