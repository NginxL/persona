package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** Access to the machine hosting a session: reads and file changes where the hub offers them. */
interface SessionFiles {
    suspend fun directory(machineId: String, cwd: String, path: String): SessionDirectory
    suspend fun file(machineId: String, cwd: String, path: String): SessionFileContent
    /**
     * [file] for the viewer, asked with `stream=1` (hub feature `file_stream`, web `fileURL`, iOS `fetchBytes`): a
     * picture, PDF, video, audio or Office file comes described ([SessionFileContent.stream], no `data`) and its bytes
     * from [download]. An older hub ignores the flag and sends the base64 as before; transports without it read [file].
     */
    suspend fun filePreview(machineId: String, cwd: String, path: String): SessionFileContent = file(machineId, cwd, path)
    suspend fun status(machineId: String, cwd: String): SessionGitStatus
    suspend fun diff(machineId: String, cwd: String, path: String, staged: Boolean): SessionGitDiff
    suspend fun log(machineId: String, cwd: String, skip: Int, limit: Int = 50): SessionGitLog
    suspend fun show(machineId: String, cwd: String, hash: String): SessionGitShow
    /**
     * Which of the paths a conversation names are there on the machine (`GET …/stat`, relative ones in [cwd]): the
     * chat links only those. A source that cannot say throws, and the paths stay plain text.
     */
    suspend fun stat(machineId: String, cwd: String, paths: List<String>): SessionPathStats =
        throw UnsupportedOperationException("stat")
    /**
     * A file's whole bytes (file/download, not capped at the 2 MB of [file]) written to [into]; the byte count.
     * Transports that can't stream say so with [UnsupportedOperationException].
     */
    suspend fun download(machineId: String, cwd: String, path: String, into: java.io.File): Long =
        throw UnsupportedOperationException("download")
    /** The authenticated file stream's bounded text suffix for a background command's live output. */
    suspend fun readLogTail(machineId: String, cwd: String, path: String, manager: String?): SessionLogTail =
        throw UnsupportedOperationException("log tail")
    /** Whether [blob] works for [machineId] (its hub's `git_blob` feature): the Git page compares documents and pictures. */
    fun comparesVersions(machineId: String): Boolean = false
    /**
     * A file as it is in [rev] (`HEAD`, `INDEX`, a commit or`hash^`), [path] relative to the repository's root
     * (`GET …/git/blob`): where the node put it, read with [download] or [file]; `missing` when that version has no
     * such file.
     */
    suspend fun blob(machineId: String, cwd: String, path: String, rev: String): SessionGitBlob =
        throw UnsupportedOperationException("blob")
    /**
     * A picture of the file at most [size] px on its long side (`GET …/file/thumb`, the first page of a document, a
     * frame of a video); null when the machine cannot draw it (404 `no_thumbnail`). Older transports throw.
     */
    suspend fun thumbnail(machineId: String, cwd: String, path: String, size: Int): ByteArray? =
        throw UnsupportedOperationException("thumbnail")
    /**
     * The file as a PDF the machine's LibreOffice writes (`GET …/file/convert?to=pdf`): where it is on the machine, read
     * then like any file. A machine without LibreOffice answers 404 `no_converter`.
     */
    suspend fun convertToPdf(machineId: String, cwd: String, path: String): SessionConvertedFile =
        throw UnsupportedOperationException("convert")
    /**
     * `POST …/git/pull`: a fast-forward pull of the working tree's branch from its upstream (docs/protocol/protocol.md); the
     * hub refuses a diverged branch, local changes, no upstream or a failed sign-in, with its reason.
     */
    suspend fun pull(machineId: String, cwd: String): SessionGitPull = throw UnsupportedOperationException("pull")
    /**
     * Whether [rename], [move], [trash] and [restore] work for [machineId] (its hub's `file_edit` feature). An unknown feature list
     * (an older hub's reply) offers them and lets the answer tell, as iOS `FolderEdits.available`.
     */
    fun editsFiles(machineId: String): Boolean = false
    /**
     * `POST …/files/rename` (docs/protocol/protocol.md "Changing files"): [path] (as in a listing) renamed to [name] in its
     * folder; the new path, in the form [path] used. The machine refuses outside the home (409 `read_only`) and a name
     * already taken (409 `exists`).
     */
    suspend fun rename(machineId: String, cwd: String, path: String, name: String): String = throw UnsupportedOperationException("rename")
    /**
     * `POST …/files/move`: one [path] into [directory]. A null [conflict] leaves both items in place on 409 `exists`.
     * The other policies are for a retry after the user chooses how to handle that conflict.
     */
    suspend fun move(machineId: String, cwd: String, path: String, directory: String, conflict: SessionMoveConflictPolicy? = null): SessionMovedFile =
        throw UnsupportedOperationException("move")
    /** `POST …/files/trash`: [paths] to the machine's trash, each with the id [restore] takes to put it back. */
    suspend fun trash(machineId: String, cwd: String, paths: List<String>): List<SessionTrashedFile> =
        throw UnsupportedOperationException("trash")
    /** `POST …/files/restore` (Undo): what [trash] took put back where it was; the paths it is at again. */
    suspend fun restore(machineId: String, trashIds: List<String>): List<String> = throw UnsupportedOperationException("restore")
}

/** `POST …/files/rename` and each of `…/files/restore`'s answers: where the file is now. */
data class SessionLogTail(val text: String, val suffix: Boolean)

@Serializable data class SessionFilePath(val path: String)

enum class SessionMoveConflictPolicy { KeepBoth, Replace }

/** Where the item came from and where it landed, including any numbered name chosen by the node. */
@Serializable data class SessionMovedFile(val from: String, val to: String)

@Serializable data class SessionMovedFiles(val moved: List<SessionMovedFile> = emptyList())

/** One path `POST …/files/trash` moved to the machine's trash, and the opaque id that puts it back. */
@Serializable data class SessionTrashedFile(val path: String, @SerialName("trash_id") val trashId: String)

@Serializable data class SessionTrashedFiles(val trashed: List<SessionTrashedFile> = emptyList())

@Serializable data class SessionRestoredFiles(val restored: List<SessionFilePath> = emptyList())

/** `GET /api/v1/machines/{id}/git/blob`: the version's copy in the node's cache, or `missing`. */
@Serializable data class SessionGitBlob(val path: String? = null, val size: Long = 0, val missing: Boolean = false)

/** `GET /api/v1/machines/{id}/file/convert`: the converted file, an absolute path on the machine. */
@Serializable data class SessionConvertedFile(val path: String, val size: Long = 0, val mime: String = "")

/** What a pull did: the repository's status after it, and whether it brought anything. */
@Serializable data class SessionGitPull(val status: SessionGitStatus, val updated: Boolean = false)

/** `GET /api/v1/machines/{id}/stat`: each path as asked, `file`, `dir`, `denied` (macOS keeps it from the node) or `missing`. */
@Serializable data class SessionPathStats(val stats: List<SessionPathStat> = emptyList())

@Serializable data class SessionPathStat(val path: String, val kind: String = "missing", val size: Long = 0)

@Serializable data class SessionDirectory(
    val path: String,
    val entries: List<SessionFileEntry> = emptyList(),
    val truncated: Boolean = false,
)

@Serializable data class SessionFileEntry(
    val name: String,
    val dir: Boolean = false,
    val symlink: Boolean = false,
    val size: Long = 0,
    val mtime: String? = null,
)

@Serializable data class SessionFileContent(
    val path: String,
    val name: String,
    val size: Long = 0,
    val mime: String? = null,
    val text: String? = null,
    val data: String? = null,
    val truncated: Boolean = false,
    @SerialName("is_dir") val isDir: Boolean = false,
    val entries: List<String> = emptyList(),
    /** Asked with stream=1 and binary: no [data], the bytes come from file/download. */
    val stream: Boolean = false,
    /** The hub's name for this version of the file (size and modification time); null from an older hub. */
    val revision: String? = null,
)

@Serializable data class SessionGitFile(
    val path: String,
    @SerialName("orig_path") val originalPath: String? = null,
    val index: String = ".",
    val worktree: String = ".",
    val added: Int = 0,
    val deleted: Int = 0,
    val binary: Boolean = false,
) {
    val untracked get() = index == "?"
    val conflicted get() = index == "U" || worktree == "U" || index + worktree in setOf("AA", "DD")
    val staged get() = !untracked && index !in listOf(".", " ", "")
    val unstaged get() = untracked || worktree !in listOf(".", " ", "")
    val letter get() = when { conflicted -> "!"; untracked -> "U"; staged -> index; else -> worktree }
}

@Serializable data class SessionGitCommit(
    val hash: String,
    val short: String = "",
    val subject: String = "",
    val body: String? = null,
    val author: String = "",
    val date: String? = null,
    val refs: String? = null,
)

@Serializable data class SessionGitStatus(
    @SerialName("is_repo") val isRepo: Boolean,
    val root: String? = null,
    val branch: String? = null,
    val head: SessionGitCommit? = null,
    val upstream: String? = null,
    val ahead: Int = 0,
    val behind: Int = 0,
    val stashes: Int = 0,
    val files: List<SessionGitFile> = emptyList(),
    val commits: List<SessionGitCommit> = emptyList(),
    /** The upstream was deleted on the remote (a merged pull request's branch): ahead and behind are 0 and mean nothing. */
    val gone: Boolean = false,
)

@Serializable data class SessionGitDiff(val path: String = "", val diff: String = "", val truncated: Boolean = false)
@Serializable data class SessionGitLog(val commits: List<SessionGitCommit> = emptyList())
@Serializable data class SessionGitShow(
    val commit: SessionGitCommit,
    val files: List<SessionGitFile> = emptyList(),
    val diff: String = "",
    val truncated: Boolean = false,
)
