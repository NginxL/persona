package persona.feature.inbox

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.withContext
import persona.core.contracts.api.ConnectorReplyOutcome
import persona.core.contracts.api.ConnectorThreadMessage
import persona.core.contracts.api.ConnectorThreadPage
import persona.core.contracts.api.ConnectorsManagement
import persona.core.inbox.*

/** How many older pages a tap on a quote reads back before saying the quoted message is not near. */
internal const val QuoteJumpPages = 3

/** An older page put before the one on screen: its messages first (none twice), the chat as the first page had it. */
internal fun mergeOlder(have: ConnectorThreadPage, older: ConnectorThreadPage): ConnectorThreadPage {
    val ids = have.messages.mapTo(HashSet()) { it.id }
    return ConnectorThreadPage(have.chat, older.messages.filter { it.id !in ids } + have.messages, older.before)
}

/** The newest page again (after a reply, on a refresh): it replaces the newest messages, the older ones read stay. */
internal fun mergeNewest(have: ConnectorThreadPage?, newest: ConnectorThreadPage): ConnectorThreadPage {
    if (have == null || newest.messages.isEmpty()) return newest
    val cut = have.messages.indexOfFirst { it.id == newest.messages.first().id }
    if (cut < 0) return newest
    // `members` comes on the first page only: keep the count the page had when this one leaves it out.
    val chat = if (newest.chat.members == null) newest.chat.copy(members = have.chat.members) else newest.chat
    return ConnectorThreadPage(chat, have.messages.take(cut) + newest.messages, have.before)
}

/**
 * Reads older pages until [id] is on the page or [pages] pages were read (or the thread starts): the page with what was
 * read, and whether the message is on it.
 */
internal suspend fun reachMessage(
    have: ConnectorThreadPage,
    id: String,
    pages: Int = QuoteJumpPages,
    older: suspend (before: String) -> ConnectorThreadPage,
): Pair<ConnectorThreadPage, Boolean> {
    var page = have
    var left = pages
    while (page.messages.none { it.id == id }) {
        val before = page.before?.takeIf { it.isNotEmpty() } ?: return page to false
        if (left-- <= 0) return page to false
        page = mergeOlder(page, older(before))
    }
    return page to true
}

/** The thread page's state: [failure] is why the thread could not be read (the page then shows the item instead). */
internal data class ConnectorThreadState(
    val page: ConnectorThreadPage? = null,
    val loaded: Boolean = false,
    val failure: Throwable? = null,
    val older: Boolean = false,
    val sending: Boolean = false,
    /** How the last reply went; null while one is being sent or before any. */
    val outcome: ConnectorReplyOutcome? = null,
    /** Why the last reply did not reach the hub at all (the network): shown in the app's words for it. */
    val replyError: Throwable? = null,
)

/**
 * One connector item's thread ([thread] of the connector [id]): its pages and its replies, through the hub only, with
 * [pins] (its page's) on each.
 */
internal class ConnectorThreadModel(private val api: ConnectorsManagement, val id: String, val thread: String,
    private val pins: kotlin.coroutines.CoroutineContext = kotlin.coroutines.EmptyCoroutineContext) {
    private val mutable = MutableStateFlow(ConnectorThreadState())
    val state: StateFlow<ConnectorThreadState> = mutable.asStateFlow()

    suspend fun load() {
        try {
            val newest = withContext(pins) { api.connectorThread(id, thread) }
            mutable.update { it.copy(page = mergeNewest(it.page, newest), loaded = true, failure = null) }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            mutable.update { it.copy(loaded = true, failure = e) }
        }
    }

    /** One page further back; the failure when it could not be read (the page keeps what it has). */
    suspend fun older(): Throwable? {
        val before = state.value.page?.before?.takeIf { it.isNotEmpty() } ?: return null
        if (state.value.older) return null
        mutable.update { it.copy(older = true) }
        return try {
            val page = withContext(pins) { api.connectorThread(id, thread, before) }
            mutable.update { s -> s.copy(page = s.page?.let { mergeOlder(it, page) } ?: page) }
            null
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            e
        } finally {
            mutable.update { it.copy(older = false) }
        }
    }

    /** Reads back until the quoted message [messageId] is on the page; false when it is not within [QuoteJumpPages]. */
    suspend fun reach(messageId: String): Boolean {
        val have = state.value.page ?: return false
        return try {
            reachMessage(have, messageId) { before ->
                withContext(pins) { api.connectorThread(id, thread, before) }.also { older ->
                    // Each page read goes in front of the page as it is when it comes, so a reload meanwhile (a new
                    // message) stays; only while the page still starts where it was read from (#9935).
                    mutable.update { s -> s.page?.takeIf { it.before == before }?.let { s.copy(page = mergeOlder(it, older)) } ?: s }
                }
            }
            state.value.page?.messages?.any { it.id == messageId } == true
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            false
        }
    }

    /**
     * A reply through the page's own action (`chat.reply`), quoting [replyTo] when set. The hub decides whether it goes
     * out (its gate may ask for approval first); the outcome is shown as it came back, and a sent one reloads the page.
     */
    suspend fun reply(text: String, replyTo: ConnectorThreadMessage?): ConnectorReplyOutcome? {
        val action = state.value.page?.chat?.reply?.takeIf { it.isNotBlank() } ?: return null
        if (text.isBlank() || state.value.sending) return null
        mutable.update { it.copy(sending = true, outcome = null, replyError = null) }
        var error: Throwable? = null
        val outcome = try {
            withContext(pins) { api.connectorThreadReply(id, thread, action, text.trim(), replyTo?.id) }
        } catch (e: CancellationException) {
            mutable.update { it.copy(sending = false) }
            throw e
        } catch (e: Exception) {
            error = e
            ConnectorReplyOutcome.Failed(e.message.orEmpty())
        }
        mutable.update { it.copy(sending = false, outcome = outcome, replyError = error) }
        if (outcome is ConnectorReplyOutcome.Sent) load()
        return outcome
    }
}

/** The page's line under the title: the chat's kind and, for a group or a channel, how many are in it. */
internal enum class ConnectorChatKind { Dm, Group, Channel, Other }

internal fun connectorChatKind(kind: String) = when (kind) {
    "dm" -> ConnectorChatKind.Dm
    "group" -> ConnectorChatKind.Group
    "channel" -> ConnectorChatKind.Channel
    else -> ConnectorChatKind.Other
}
