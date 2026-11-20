package persona.feature.chat

import java.io.File
import java.util.UUID
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.DraftAttachment
import persona.core.contracts.api.DraftAttachmentType
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.DraftUploadState

/** Uploads a diagnostics archive and leaves it, with its note, in the chosen chat's existing draft. */
suspend fun prepareDiagnosticsDraft(
    drafts: DraftRepository,
    composer: ChatComposerService,
    target: SessionKey?,
    mainTarget: suspend () -> SessionKey?,
    file: File,
    note: String,
    attachmentId: String = UUID.randomUUID().toString(),
): DraftSnapshot {
    val key = target?.let(DraftKey::Chat) ?: DraftKey.Assistant()
    require(drafts.structured) { "Diagnostics attachments need structured drafts" }
    require(file.isFile) { "Diagnostics archive is missing" }
    val uploadTarget = target ?: requireNotNull(mainTarget()) { "No main conversation is available" }
    val path = composer.upload(uploadTarget, file.name, file, "attachment")
    // Read after the upload so a draft typed meanwhile is preserved too. A compare-and-set collision fails the handoff;
    // it never overwrites the other edit and the caller keeps the page open for a retry.
    val previous = drafts.read(key)
    val addition = note.trim()
    val text = when {
        previous.text.isBlank() -> addition
        addition.isEmpty() -> previous.text
        else -> previous.text.trimEnd() + "\n\n" + addition
    }
    val attachment = DraftAttachment(
        id = attachmentId, type = DraftAttachmentType.File, uri = "", name = file.name,
        size = file.length(), uploadState = DraftUploadState.Uploaded, uploadedPath = path,
    )
    return drafts.save(key, text, previous.version, previous.attachments + attachment, previous.sessionVersion,
        hasPickedSavedPrompt = previous.hasPickedSavedPrompt && text == previous.text)
}
