package persona.feature.inbox

import persona.core.inbox.*

/** Use the pairing's durable draft store for both the editor and its immutable pending request. */
internal class LarkThreadReplyRepository(
    private val base: InboxRepository,
    rootId: String,
    private val source: LarkThreadSource,
) : InboxRepository by base {
    // These keys are storage namespaces only; they never reach conversation endpoints.
    val target = ReplyTarget.Lark("thread:$rootId")
    private val pendingTarget = ReplyTarget.Lark("thread-pending:$rootId")

    private suspend fun pending() = base.readDraft(pendingTarget)?.takeIf { it.text.isNotBlank() }

    override suspend fun readDraft(target: ReplyTarget): ReplyDraft? =
        if (base.supportsThreadOutbox) base.readDraft(this.target) else pending() ?: base.readDraft(this.target)
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = base.saveDraft(this.target, draft)
    override suspend fun replyStatus(target: ReplyTarget, clientId: String) =
        if (base.supportsThreadOutbox) base.replyStatus(this.target, clientId)
        else if (pending()?.clientId == clientId) ReplyDelivery.Unknown else ReplyDelivery.Ready

    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) {
        if (base.supportsThreadOutbox) { base.reply(this.target, request); return }
        val previous = pending()
        require(previous == null || previous == ReplyDraft(request.text, request.clientId, mentions = request.mentions))
        base.saveDraft(pendingTarget, ReplyDraft(request.text, request.clientId, mentions = request.mentions))
        check(source.reply(request)) { "Thread reply outcome is pending" }
        confirm(request.clientId)
    }

    override suspend fun verifyReply(target: ReplyTarget, clientId: String): ReplyDelivery {
        if (base.supportsThreadOutbox) return base.verifyReply(this.target, clientId)
        val request = pending() ?: return ReplyDelivery.Ready
        require(request.clientId == clientId)
        var before: String? = null
        val cursors = mutableSetOf<String>()
        do {
            val page = source.page(before)
            if (page.messages.any { it.clientId == clientId && it.delivery == LarkDelivery.Sent }) {
                confirm(clientId)
                return ReplyDelivery.Sent
            }
            if (!page.hasOlder) return ReplyDelivery.Unknown
            before = page.olderCursor ?: return ReplyDelivery.Unknown
        } while (cursors.add(before))
        return ReplyDelivery.Unknown
    }

    private suspend fun confirm(clientId: String) {
        if (base.readDraft(target)?.clientId == clientId) base.saveDraft(target, ReplyDraft(""))
        base.saveDraft(pendingTarget, ReplyDraft(""))
    }
}
