package persona.core.data

import persona.core.contracts.api.MailAttachment
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.MailTransport
import persona.core.inbox.MailAttachmentFile
import persona.core.inbox.MailAttachmentTransport
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

internal class InboxMailAttachments(
    private val transport: MailTransport,
    private val cache: BudgetFileCache,
    private val writeFiles: suspend (() -> Unit) -> Unit,
) : MailAttachmentTransport, MailTransport by transport {
    override suspend fun downloadAttachmentFile(ref: MailReference, uid: Long, attachment: MailAttachment,
        progress: (Long, Long?) -> Unit): MailAttachmentFile {
        require(uid in 1..4294967295L && uid in ref.uids && ref.accountId.isNotBlank() && attachment.id.isNotBlank())
        val key = cacheDigest("${ref.accountId}\u0000${ref.mailbox}\u0000$uid\u0000${attachment.id}")
        val safeName = attachment.filename.substringAfterLast('/').substringAfterLast('\\')
            .replace(Regex("[^\\p{L}\\p{N}._-]"), "_").take(100)
            .takeUnless { it.isBlank() || it == "." || it == ".." } ?: "attachment"
        val name = "mail/$key-$safeName"
        var result: BudgetFileLease? = null
        try {
            withContext(Dispatchers.IO) {
                writeFiles { result = cache.readPinned(name) }
                if (result != null) {
                    val size = requireNotNull(result).file.length()
                    progress(size, size)
                } else {
                    var pending: BudgetFileCache.PendingWrite? = null
                    try {
                        writeFiles { pending = cache.begin(name) }
                        transport.downloadAttachment(ref, uid, attachment.id, requireNotNull(pending), progress = progress)
                        writeFiles { result = requireNotNull(pending).commit() }
                    } finally { pending?.close() }
                }
            }
            val acquired = requireNotNull(result)
            return object : MailAttachmentFile {
                override val file = acquired.file
                override fun close() = acquired.close()
            }
        } catch (failure: Exception) { result?.close(); throw failure }
    }
}
