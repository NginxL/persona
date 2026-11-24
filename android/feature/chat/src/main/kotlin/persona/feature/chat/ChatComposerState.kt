package persona.feature.chat

import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import persona.core.contracts.api.*
import persona.core.contracts.JsonCodec
import persona.core.domain.AttachedFile
import persona.core.domain.ComposerMessage
import persona.core.domain.parseAttachedFiles
import kotlinx.serialization.encodeToString

data class ComposerAttachment(
    val id: String,
    val name: String,
    val uri: String,
    val image: Boolean,
    val path: String? = null,
    val error: String? = null,
    val size: Long? = null,
    val uploadState: DraftUploadState = DraftUploadState.Pending,
    /**
     * A video from the photo picker (iOS `VideoAttachments`, docs/protocol/protocol.md "Sending videos"): uploaded as it is,
     * like a file, and sent as a `<persona-file kind="video">` tag after the words rather than as a path in them.
     */
    val video: Boolean = false,
    /** A video's length in seconds, when the phone could read it. */
    val duration: Double? = null,
) {
    /** A photo or a video: the two share the picker's four places (iOS). */
    val media: Boolean get() = image || video
}

/** The most photos and videos a message carries together (iOS `mediaPicker`'s `maxSelectionCount`). */
internal const val MAX_COMPOSER_MEDIA = 4

data class ChatComposerDraft(
    val text: String = "", val attachments: List<ComposerAttachment> = emptyList(),
    val sessionVersion: DraftSessionVersion? = null,
    val hasPickedSavedPrompt: Boolean = false,
)

private fun DraftAttachment.composerAttachment() = ComposerAttachment(
    id, name, uri, type == DraftAttachmentType.Image, uploadedPath, error, size, uploadState,
    video = type == DraftAttachmentType.Video, duration = duration)

internal fun DraftSnapshot.composerDraft() =
    ChatComposerDraft(text, attachments.map { it.composerAttachment() }, sessionVersion, hasPickedSavedPrompt)

internal fun ChatComposerDraft.storedAttachments() = attachments.map {
    DraftAttachment(it.id, when {
            it.image -> DraftAttachmentType.Image
            it.video -> DraftAttachmentType.Video
            else -> DraftAttachmentType.File
        }, it.uri, it.name, it.size, when {
            it.uploadState == DraftUploadState.Invalid -> DraftUploadState.Invalid
            it.path != null -> DraftUploadState.Uploaded
            it.error != null -> DraftUploadState.Failed
            else -> it.uploadState
        }, it.path, it.error, it.duration)
}

@Stable
class ChatComposerState(initial: ChatComposerDraft = ChatComposerDraft()) {
    var focused by mutableStateOf(false)
    var focusRequest by mutableIntStateOf(0)
    /** What [message] says for photos sent without words; [ChatComposer] keeps it in the app's language. */
    var photosOnlyText: String = ""
    /** What [message] says for videos sent without words or photos ("Please look at the video", iOS); kept like [photosOnlyText]. */
    var videosOnlyText: String = ""
    /** What [message] says for ordinary files sent without words or photos; kept like [photosOnlyText]. */
    var filesOnlyText: String = ""
    private var fieldValue by mutableStateOf(TextFieldValue(initial.text, TextRange(initial.text.length)))
    internal var hasPickedSavedPrompt by mutableStateOf(initial.hasPickedSavedPrompt)
        private set
    var value: TextFieldValue
        get() = fieldValue
        set(value) {
            if (value.text != fieldValue.text) hasPickedSavedPrompt = false
            fieldValue = value
        }
    var draftLoaded by mutableStateOf(false)
    private var attachmentOrder by mutableStateOf(initial.attachments.map { it.id })
    private val attachmentStates = mutableStateMapOf<String, ComposerAttachment>().apply { putAll(initial.attachments.associateBy { it.id }) }
    /** Edited photos' sources, edits and history by attachment id (#4992): kept in memory beside the draft, never sent. */
    internal var photoEdits by mutableStateOf<Map<String, PhotoEdits>>(emptyMap())
    /** The draft's attachments; a photo that leaves it takes its kept edits along. */
    var attachments: List<ComposerAttachment>
        get() = attachmentOrder.mapNotNull(attachmentStates::get)
        set(value) {
            val ids = value.mapTo(mutableSetOf()) { it.id }
            attachmentStates.keys.filter { it !in ids }.forEach(attachmentStates::remove)
            value.forEach { attachmentStates[it.id] = it }
            attachmentOrder = value.map { it.id }
            if (photoEdits.keys.any { id -> id !in ids }) photoEdits = photoEdits.filterKeys { it in ids }
        }

    /** A background upload updates its own row, never a stale copy of another attachment's state. */
    internal fun updateAttachment(id: String, update: (ComposerAttachment) -> ComposerAttachment) {
        attachmentStates[id]?.let { attachmentStates[id] = update(it) }
    }
    var sessionVersion by mutableStateOf(initial.sessionVersion)
    var currentSessionVersion by mutableStateOf<DraftSessionVersion?>(null)
    val oldVersion get() = hasContent && (currentSessionVersion?.replaced == true || sessionVersion?.let { origin ->
        currentSessionVersion?.let(origin::differsFrom)
    } == true)
    val draft get() = ChatComposerDraft(value.text, attachments, sessionVersion, hasPickedSavedPrompt)
    val ready get() = attachments.all { it.path != null && it.uploadState != DraftUploadState.Invalid }
    val hasContent get() = value.text.isNotBlank() || attachments.isNotEmpty()

    internal fun beginEdit() {
        if (!hasContent) sessionVersion = currentSessionVersion
    }

    internal fun fillSavedPrompt(text: String) {
        beginEdit()
        value = TextFieldValue(text, TextRange(text.length))
        hasPickedSavedPrompt = true
        focusRequest++
    }

    fun insert(text: String, atSelection: Boolean) {
        beginEdit()
        val start = if (atSelection) value.selection.min.coerceIn(0, value.text.length) else value.text.length
        val end = if (atSelection) value.selection.max.coerceIn(start, value.text.length) else start
        val prefix = value.text.take(start)
        val suffix = value.text.drop(end)
        val addition = (if (prefix.isNotEmpty() && !prefix.last().isWhitespace()) " " else "") + text +
            (if (suffix.isNotEmpty() && !suffix.first().isWhitespace()) " " else "")
        value = TextFieldValue(prefix + addition + suffix, TextRange(start + addition.length))
    }

    fun message(): String {
        check(ready)
        val images = attachments.filter { it.image }.mapNotNull { it.path }
        val files = attachments.filterNot { it.media }.mapNotNull { it.attachedFile() }
        val videos = attachments.filter { it.video }.mapNotNull { it.attachedFile() }
        return ComposerMessage.text(value.text, images, files, videos,
            photosOnly = photosOnlyText, videosOnly = videosOnlyText, filesOnly = filesOnlyText)
    }

    fun clearIfUnchanged(sent: ChatComposerDraft) {
        if (draft == sent) { value = TextFieldValue(); attachments = emptyList(); sessionVersion = currentSessionVersion }
    }

    /** Fills an empty composer with a stored draft: the composer is drawn before the draft is read (#4045). */
    internal fun load(draft: ChatComposerDraft) {
        val tagged = !draft.hasPickedSavedPrompt && parseAttachedFiles(draft.text).first.any { it.kind == "file" || it.kind == "video" }
        if (tagged) {
            // Drafts written by an older build can still carry tags as words; bring their files back as ready rows.
            editMessage(draft.text)
            val restored = attachments
            attachments = restored + draft.attachments.filter { item ->
                restored.none { it.path == item.path && it.image == item.image && it.video == item.video }
            }
        } else {
            value = TextFieldValue(draft.text, TextRange(draft.text.length))
            attachments = draft.attachments
        }
        hasPickedSavedPrompt = draft.hasPickedSavedPrompt
        sessionVersion = draft.sessionVersion
        draftLoaded = true
    }

    fun editMessage(text: String) {
        beginEdit()
        val paths = composerImagePaths(text)
        // Videos and ordinary files come back already on the machine; kinds this composer does not own keep their tags.
        val (files, rest) = parseComposerFileTags(text)
        // The media cap limits new picks; older or hand-written messages keep every video and document on edit.
        val videos = files.filter { it.kind == "video" }
        val documents = files.filter { it.kind == "file" }
        val restored = files.filter { it.kind == "video" || it.kind == "file" }
        val source = if (restored.isEmpty()) text else rest + files.filter { it !in restored }.joinToString("") { "\n\n" + it.toXml() }
        val plain = source.replace(composerImagePlaceholders, "").replace(composerImageTags, "").trimEnd()
        value = TextFieldValue(plain, TextRange(plain.length))
        attachments = paths.mapIndexed { index, path ->
            ComposerAttachment("restored-$index-$path", path.substringAfterLast('/'), "", true, path,
                uploadState = DraftUploadState.Uploaded)
        } + videos.mapIndexed { index, file ->
            ComposerAttachment("restored-video-$index-${file.path}", file.name, "", false, file.path, size = file.size.takeIf { it > 0 },
                uploadState = DraftUploadState.Uploaded, video = true, duration = file.duration)
        } + documents.mapIndexed { index, file ->
            ComposerAttachment("restored-file-$index-${file.path}", file.name, "", false, file.path, size = file.size.takeIf { it > 0 },
                uploadState = DraftUploadState.Uploaded)
        }
    }

    internal fun restoreMessage(text: String) {
        if (!hasContent) { editMessage(text); return }
        val restored = ChatComposerState().apply { editMessage(text) }
        val combined = listOf(value.text, restored.value.text).filter(String::isNotBlank).joinToString("\n\n")
        value = TextFieldValue(combined, TextRange(combined.length))
        attachments = attachments + restored.attachments.filter { item -> attachments.none { it.id == item.id } }
    }

    /**
     * A queued message pulled back (the queue strip's "Take back to edit"): the draft if empty, else after it on a new line (iOS); photos it
     * carried come back as attachments rather than as their tag.
     */
    internal fun restoreQueued(text: String) {
        beginEdit()
        val restored = ChatComposerState().apply { editMessage(text) }
        val words = restored.value.text.let {
            if (restored.attachments.isNotEmpty() && (it.trim() in composerPhotoStandIns || it.trim() == photosOnlyText ||
                    it.trim() == videosOnlyText || it.trim() == filesOnlyText)) "" else it
        }
        val combined = when {
            value.text.isEmpty() -> words
            words.isEmpty() -> value.text
            else -> value.text + "\n" + words
        }
        value = TextFieldValue(combined, TextRange(combined.length))
        attachments = attachments + restored.attachments.filter { item -> attachments.none { it.id == item.id } }
    }

    /**
     * Photos taken back with a queued message (the hub returns a Persona conversation's beside its queue, [restoredPhotos]),
     * kept in the draft as the photos they were. All of them stay, as on iOS: a message went out with at most four, and the
     * draft's four-place limit is the pickers', not the send's.
     */
    internal fun restorePhotos(photos: List<ComposerAttachment>) {
        if (photos.isEmpty()) return
        beginEdit()
        attachments = attachments + photos
    }

    companion object {
        val Saver = listSaver<ChatComposerState, String>(
            save = { state -> listOf(state.value.text, JsonCodec.encodeToString(state.draft.storedAttachments()),
                state.sessionVersion?.let { JsonCodec.encodeToString(it) }.orEmpty(), state.hasPickedSavedPrompt.toString()) },
            restore = { values ->
                if (values.size == 3 || values.size == 4) ChatComposerState(ChatComposerDraft(values[0],
                    JsonCodec.decodeFromString<List<DraftAttachment>>(values[1]).map { it.composerAttachment() },
                    values[2].takeIf(String::isNotEmpty)?.let { JsonCodec.decodeFromString<DraftSessionVersion>(it) },
                    values.getOrNull(3).toBoolean()))
                else ChatComposerState(ChatComposerDraft(values.first(), values.drop(1).chunked(6).map {
                    ComposerAttachment(it[0], it[1], it[2], it[3].toBoolean(), it[4].ifEmpty { null }, it[5].ifEmpty { null })
                }))
            },
        )
    }
}

@Composable
fun rememberChatComposerState(initial: ChatComposerDraft = ChatComposerDraft()): ChatComposerState {
    val state = rememberSaveable(saver = ChatComposerState.Saver) { ChatComposerState(initial) }
    SideEffect { state.draftLoaded = true }
    return state
}

internal fun quoteComposerPath(path: String): String = ComposerMessage.quotePath(path)

private val composerContextTags = Regex("<persona-context\\b[^>]*>[\\s\\S]*?</persona-context>")

/** Parse only this message's own file tags; a forwarded context's tags stay inside that context, byte for byte. */
private fun parseComposerFileTags(text: String): Pair<List<AttachedFile>, String> {
    val contexts = mutableListOf<Pair<String, String>>()
    val masked = composerContextTags.replace(text) { match ->
        val marker = "\u0000${contexts.size}\u0000"
        contexts += marker to match.value
        marker
    }
    val (files, rest) = parseAttachedFiles(masked)
    var restored = rest
    contexts.forEach { (marker, context) -> restored = restored.replace(marker, context) }
    return files to restored
}

private val composerImageTags = Regex("<attached_images>([\\s\\S]*?)</attached_images>")
private val composerImagePlaceholders = Regex("\\A(?:\\[Image #\\d+]\\s*)+")
// Photo-only words sent by current and older clients: wire data, not interface copy.
private val composerPhotoStandIns = setOf(
    "Please look at the image", "Please look at the images", "Please look at the photos", "See the images", "Please look at the video",
    "Please look at the attached files",
    "请看图片", // i18n-ignore
    "请看视频", // i18n-ignore
    "请看附件", // i18n-ignore
    "画像を見てください", // i18n-ignore
    "動画を見てください", // i18n-ignore
    "添付ファイルを見てください", // i18n-ignore
)

/** How many photos a message carries as paths on the computer (its `<attached_images>` block): what a queued row says it carries. */
internal fun composerPhotoCount(text: String): Int = composerImagePaths(text).size

private fun composerImagePaths(text: String): List<String> =
    composerImageTags.findAll(text).flatMap { it.groupValues[1].lineSequence() }
        .map { it.trim().removePrefix("path:").trim() }
        .filter { it.startsWith('/') || it.startsWith("~/") }.distinct().toList()

/**
 * [text] with its photos as one [photo] mark ("[Image]") and, given a [video] mark ("[Video]"), each `<persona-file>` tag as
 * that mark (a video) or its name, on lines of their own: the words that only stand in for them are left out.
 */
internal fun composerMessagePreview(text: String, photo: String, video: String? = null): String {
    val (files, rest) = if (video == null) emptyList<AttachedFile>() to text else parseAttachedFiles(text)
    val marks = files.map { if (it.kind == "video") video.orEmpty() else it.name }
    val clean = rest.replace(composerImagePlaceholders, "")
    val plain = clean.replace(composerImageTags, "").trim()
    val body = when {
        plain in composerPhotoStandIns && composerImagePaths(clean).isNotEmpty() -> photo
        plain in composerPhotoStandIns && marks.isNotEmpty() -> ""
        else -> clean.replace(composerImageTags, photo).trim()
    }
    return if (marks.isEmpty()) body else (listOf(body) + marks).filter(String::isNotBlank).joinToString("\n")
}

@Composable
internal fun composerMessagePreview(text: String): String =
    composerMessagePreview(text, stringResource(R.string.chat_composer_photo_placeholder), stringResource(R.string.chat_composer_video_placeholder))

/** A video or ordinary file on the session's machine as the tag its message carries; null for a photo or until uploaded. */
fun ComposerAttachment.attachedFile(): AttachedFile? =
    path?.takeIf { !image }?.let { AttachedFile(if (video) "video" else "file", it, name, size ?: 0, duration) }
