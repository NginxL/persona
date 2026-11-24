package persona.feature.inbox

import persona.core.domain.inbox.InboxItem
import kotlinx.serialization.Serializable
import persona.core.inbox.*

@Serializable
internal data class InboxLocalState(
    val records: List<CachedInboxRecord> = emptyList(),
    val preferences: InboxPreferences = InboxPreferences(),
    val drafts: Map<String, ReplyDraft> = emptyMap(),
    val replies: Map<String, StoredInboxReply> = emptyMap(),
)

@Serializable
internal data class StoredInboxReply(
    val target: ReplyTarget,
    val request: ReplyRequest,
    val feedId: String? = null,
    val delivery: ReplyDelivery = ReplyDelivery.Unknown,
)

@Serializable
internal data class CachedInboxRecord(
    val id: String,
    val account: String,
    val kind: String,
    val title: String,
    val sender: String?,
    val preview: String,
    val summary: String?,
    val tier: String?,
    val updatedAt: String?,
    val doneAt: String?,
    val sourceLabel: String,
    val feedId: String?,
    val startPosition: Long?,
    val count: Int,
    val agent: InboxAgent?,
    /** The hub's `version` as listed (a bulk "Done" sends it back); absent in records cached before it. */
    val version: String? = null,
    /** lark / mail…, and a document's notice: what opening the item shows; absent in records cached before them. */
    val source: String = "",
    val doc: InboxDocRef? = null,
    /** A connector's item: the thread it opens as; absent in records cached before connectors' items were listed. */
    val connector: InboxConnectorRef? = null,
    /** A Feishu chat's kind; absent in records cached before it was kept. */
    val isGroup: Boolean? = null,
    val isTopic: Boolean? = null,
) {
    fun record() = InboxRecord(
        InboxItem(id, account, kind, title, sender, preview, summary, tier, updatedAt, doneAt, version),
        sourceLabel, feedId, startPosition, count, agent, source = source, doc = doc, connector = connector,
        isGroup = isGroup, isTopic = isTopic,
    )
}

internal fun InboxRecord.cached() = CachedInboxRecord(
    item.id, item.account, item.kind, item.title, item.sender, item.preview, item.summary,
    item.tier, item.updatedAt, item.doneAt, sourceLabel, feedId, startPosition, count, agent, item.version, source, doc, connector,
    isGroup, isTopic,
)
internal fun ReplyTarget.localKey() = when (this) {
    is ReplyTarget.Inbox -> "inbox:$itemId"
    is ReplyTarget.Lark -> "lark:$feedId"
}
