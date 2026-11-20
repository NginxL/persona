package persona.core.data

import androidx.room.withTransaction
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/*
 * When two hub groups merge (their hubs joined one mesh), the absorbed group's data must not take unsent messages
 * and drafts with it (docs/android/android.md "Multiple hubs"). Its storage stays as a PendingMove; what it still owes moves into the
 * group it merged into, then the storage goes. Caches stay behind: they are fetched again.
 */

/** What a group still owes its hubs: messages not yet shown or cancelled, drafts and their hand-off journals. */
class UnsentData internal constructor(
    internal val messages: List<OutboxRow>,
    internal val drafts: List<DraftRow>,
    internal val agentHandOffs: List<InboxAgentHandOff> = emptyList(),
    internal val sourceScope: String? = null,
) {
    val messageCount: Int get() = messages.size
    val draftCount: Int get() = drafts.size
    val isEmpty: Boolean get() = messages.isEmpty() && drafts.isEmpty() && agentHandOffs.isEmpty()
}

private val settled = setOf(OutboxState.Cancelled.wire, OutboxState.Shown.wire, OutboxState.Kept.wire, OutboxState.Held.wire)

internal fun DraftRow.hasContent() = text.isNotEmpty() || attachments != "[]"

internal fun unsentOf(
    messages: List<OutboxRow>,
    drafts: List<DraftRow>,
    agentHandOffs: List<InboxAgentHandOff> = emptyList(),
    sourceScope: String? = null,
) = UnsentData(messages.filter { it.state !in settled }, drafts.filter { it.hasContent() }, agentHandOffs, sourceScope)

/**
 * The rows as the group with scope [into] stores them. Messages keep their client_msg_id (the hub deduplicates it,
 * so delivery means the same as before); one interrupted while sending is unknown, to be checked before anything is
 * sent again. Machine and session ids stay: the merged mesh keeps machine ids (plan 2.6). A draft never replaces one
 * the group already has with something in it.
 */
internal fun adopted(unsent: UnsentData, into: String, draftsWithContent: Set<Pair<String, String>>): Pair<List<OutboxRow>, List<DraftRow>> =
    unsent.messages.map { row ->
        row.copy(position = 0, scope = into, state = if (row.state == OutboxState.Sending.wire) OutboxState.Unknown.wire else row.state)
    } to unsent.drafts.filter { (it.machine to it.session) !in draftsWithContent }.map { it.copy(scope = into) }

/** Takes over [unsent] (another group's, see [HubDataStore.unsentAt]) and has the outbox look at it; returns how many rows moved. */
suspend fun ScopedHubData.adopt(unsent: UnsentData): Int = withContext(Dispatchers.IO) {
    checkActive()
    val dao = persistent.records()
    val existing = dao.drafts(scope.path).filter { it.hasContent() }.map { it.machine to it.session }.toSet()
    val (rows, drafts) = adopted(unsent, scope.path, existing)
    val handOffs = unsent.agentHandOffs.map { handOff ->
        // Only an entry owned by the absorbed inbox moves to a new origin. A target journal can instead carry an
        // external origin's copy; re-parenting that copy would make its Accepted evidence unreachable by the owner.
        val ownedBySource = unsent.sourceScope == null || handOff.scope == unsent.sourceScope
        val moved = handOff.copy(
            scope = if (ownedBySource) scope.path else handOff.scope,
            deliveryScopes = handOff.deliveryScopes + scope.path,
        )
        if (!ownedBySource && !moved.recorded) {
            // The absorbed target directory is about to go away: its replacement joins the origin's replay set first.
            check(InboxReplyJournal.linkAgentHandOffTarget(moved, scope.path, inboxReplies)) {
                "Cross-pairing hand-off origin is not linked"
            }
        }
        moved
    }
    if (handOffs.isNotEmpty()) {
        // Commit metadata before rows, as at enqueue: a crash can only leave an idempotent, not-yet-sendable entry.
        inboxReplies.update(scope.path) { saved ->
            handOffs.fold(saved) { document, handOff -> document.mergeAgentHandOff(handOff) }
        }
    }
    var moved = 0
    persistent.withTransaction {
        rows.forEach { if (dao.adoptMessage(it) != -1L) moved++ }
        drafts.forEach { dao.putDraft(it); moved++ }
    }
    messages.nudge()
    moved
}
