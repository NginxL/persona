package persona.core.inbox

import persona.core.contracts.api.MailAttachment
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.MailTransport
import java.io.File

/** Keeps the cached file alive until the viewer returns; its descriptor has a separate lease. */
interface MailAttachmentFile : AutoCloseable {
    val file: File
}

/** Mail transport decorated by the pairing's budgeted media store. */
interface MailAttachmentTransport : MailTransport {
    suspend fun downloadAttachmentFile(ref: MailReference, uid: Long, attachment: MailAttachment,
        progress: (Long, Long?) -> Unit = { _, _ -> }): MailAttachmentFile
}
