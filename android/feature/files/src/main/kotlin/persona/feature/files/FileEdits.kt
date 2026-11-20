package persona.feature.files

import android.content.res.Resources
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import persona.core.designsystem.FeedbackAction
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.Haptic
import persona.core.designsystem.JDialog
import persona.core.designsystem.JTextField
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.rememberHaptics
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import persona.core.contracts.JsonCodec
import persona.core.hub.HubFailure

// File changes in the browser (docs/protocol/protocol.md "Changing files", iOS FolderEdits). The machine allows them
// only inside the home. A trash goes to the machine's trash and the toast's Undo puts it back.

/** Why a name can't be a file's, said before asking (iOS `FolderEdits.nameProblem`); the machine checks again. */
internal enum class NameProblem { Empty, Dots, Slash }

internal fun nameProblem(name: String): NameProblem? {
    val n = name.trim()
    return when {
        n.isEmpty() -> NameProblem.Empty
        n == "." || n == ".." -> NameProblem.Dots
        '/' in n || '\u0000' in n -> NameProblem.Slash
        else -> null
    }
}

/** Why a change did not happen, by the code the machine or the hub answered with (iOS `FolderEdits.message`). */
internal sealed interface EditRefusal {
    /** Outside the home, or in ~/.persona or ~/Library (409 `read_only`). */
    data object ReadOnly : EditRefusal
    /** The name is taken there (409 `exists`): the hub's `name`, or the name asked for when the hub gave none. */
    data class Exists(val name: String) : EditRefusal
    /** The file, or the trashed copy Undo looks for, is gone (404 `not_found`). */
    data object Gone : EditRefusal
    /** The computer's personad predates these changes: the hub's words, which name the computer (409 `node_outdated`). */
    data class NodeOutdated(val message: String) : EditRefusal
    /** A hub without the routes (a plain 404). */
    data object HubTooOld : EditRefusal
    /**
     * Anything else the node refused (502 `internal`): iOS says the service failed rather than repeat the node's words, with
     * the hub's request id ([HubFailure.requestId], `X-Request-Id`) to find its log line.
     */
    data class Unavailable(val status: Int, val requestId: String? = null) : EditRefusal
    /** The connection, or another failure: said as a folder that could not be read says it. */
    data class Other(val error: Throwable) : EditRefusal
}

/** [error] as an [EditRefusal]; [name] is the name asked for, said by a 409 `exists` whose body names none. */
internal fun editRefusal(error: Throwable, name: String?): EditRefusal {
    val failure = (error as? HubFailure) ?: generateSequence(error) { it.cause }.take(8).filterIsInstance<HubFailure>().firstOrNull()
            ?: return EditRefusal.Other(error)
    val status = failure.status ?: 0
    return when {
        failure.code == "read_only" -> EditRefusal.ReadOnly
        failure.code == "exists" -> EditRefusal.Exists(bodyName(failure.body) ?: name.orEmpty())
        failure.code == "not_found" -> EditRefusal.Gone
        failure.code == "node_outdated" -> EditRefusal.NodeOutdated(failure.message)
        failure.code == null && status == 404 -> EditRefusal.HubTooOld
        failure.code == "internal" && status >= 500 -> EditRefusal.Unavailable(status, failure.requestId)
        else -> EditRefusal.Other(error)
    }
}

/** The result of a file change, reported with a haptic or a toast. */
internal sealed interface FileEdit {
    data class Invalid(val problem: NameProblem) : FileEdit
    data object Renamed : FileEdit
    data class Moved(val name: String, val folder: String) : FileEdit
    /** [trashIds] put them back ([FilesController.restore]); empty when the machine gave none (no Undo then). */
    data class Trashed(val paths: List<String>, val trashIds: List<String>) : FileEdit
    data object Restored : FileEdit
    /** [restoring]: Undo failed, said as "Couldn't put it back: …". */
    data class Failed(val refusal: EditRefusal, val restoring: Boolean = false) : FileEdit
}

internal fun nameProblemText(resources: Resources, problem: NameProblem): String = resources.getString(when (problem) {
    NameProblem.Empty -> R.string.files_name_empty
    NameProblem.Dots -> R.string.files_name_dots
    NameProblem.Slash -> R.string.files_name_slash
})

internal fun editRefusalText(resources: Resources, refusal: EditRefusal): String = when (refusal) {
    EditRefusal.ReadOnly -> resources.getString(R.string.files_edit_read_only)
    is EditRefusal.Exists -> resources.getString(R.string.files_edit_exists, refusal.name)
    EditRefusal.Gone -> resources.getString(R.string.files_edit_gone)
    is EditRefusal.NodeOutdated -> refusal.message
    EditRefusal.HubTooOld -> resources.getString(R.string.files_edit_hub_too_old)
    is EditRefusal.Unavailable -> withRequest(resources, resources.getString(R.string.files_edit_unavailable, refusal.status), refusal.requestId)
    is EditRefusal.Other -> filesLoadError(resources, refusal.error).let { text ->
        val failure = refusal.error as? HubFailure
        if ((failure?.status ?: 0) >= 500) withRequest(resources, text, failure?.requestId) else text
    }
}

/** The name a 409 `exists` body gives (`{"code":"exists","name":…}`), null when it gives none. */
private fun bodyName(body: String?): String? = body?.let {
    runCatching { JsonCodec.parseToJsonElement(it).jsonObject["name"]?.jsonPrimitive?.contentOrNull }.getOrNull()
}?.takeIf { it.isNotEmpty() }

/** A server error's words with the hub's request id after them, as iOS `APIError.errorDescription` adds it to a 5xx. */
internal fun withRequest(resources: Resources, text: String, requestId: String?): String =
    if (requestId.isNullOrEmpty()) text else resources.getString(R.string.files_edit_request, text, requestId)

/**
 * Says what a [FileEdit] came to, as iOS does: a tap for a rename, a success toast with Undo after a trash (Undo puts it
 * back and the folder is read again), nothing after Undo itself, and every refusal as an error toast in the user's words.
 */
@Composable
internal fun rememberFileEditReport(controller: FilesController): (FileEdit) -> Unit {
    val feedback = LocalAppFeedback.current
    val haptics = rememberHaptics()
    val resources = LocalResources.current
    return remember(controller, feedback, haptics, resources) {
        lateinit var report: (FileEdit) -> Unit
        report = { edit ->
            when (edit) {
                is FileEdit.Invalid -> feedback?.notice(nameProblemText(resources, edit.problem), FeedbackStyle.Error)
                FileEdit.Renamed -> haptics(Haptic.Tap)
                is FileEdit.Moved -> feedback?.notice(resources.getString(R.string.files_moved, edit.name, edit.folder), FeedbackStyle.Success)
                FileEdit.Restored -> Unit
                is FileEdit.Failed -> {
                    val text = editRefusalText(resources, edit.refusal)
                    feedback?.notice(if (edit.restoring) resources.getString(R.string.files_restore_failed, text) else text, FeedbackStyle.Error)
                }
                is FileEdit.Trashed -> {
                    val name = FilesPaths.name(edit.paths.first())
                    val message = resources.getString(R.string.files_trashed, name)
                    if (edit.trashIds.isEmpty()) feedback?.notice(message, FeedbackStyle.Success)
                    else {
                        // A toast with a button plays no haptic of its own: the success one plays here, as iOS's does.
                        haptics(Haptic.Success)
                        feedback?.notice(message, FeedbackAction(resources.getString(R.string.files_undo)) { controller.restore(edit.trashIds, name, report) })
                    }
                }
            }
        }
        report
    }
}

/** Rename's dialog (iOS's alert): the name in a field, Rename and Cancel; the name is checked on Rename. */
@Composable
internal fun RenameFileDialog(name: String, onRename: (String) -> Unit, onDismiss: () -> Unit) {
    var text by rememberSaveable(name) { mutableStateOf(name) }
    val rename = { onDismiss(); onRename(text) }
    JDialog(onDismiss, title = stringResource(R.string.files_rename), confirm = stringResource(R.string.files_rename) to rename) {
        JTextField(text, { text = it }, Modifier.fillMaxWidth().testTag("files-rename-field"), label = stringResource(R.string.files_rename_name),
            singleLine = true, autofill = false,
            keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false, imeAction = ImeAction.Done),
            keyboardActions = KeyboardActions(onDone = { rename() }))
    }
}

/** Move to Trash's confirmation (iOS's alert): where it goes, and that it can be undone. */
@Composable
internal fun TrashFileDialog(name: String, onTrash: () -> Unit, onDismiss: () -> Unit) {
    JDialog(onDismiss, title = stringResource(R.string.files_trash_title, name), text = stringResource(R.string.files_trash_message),
        confirm = stringResource(R.string.files_trash) to { onDismiss(); onTrash() }, destructive = true)
}
