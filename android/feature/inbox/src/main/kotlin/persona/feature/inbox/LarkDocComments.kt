package persona.feature.inbox

import androidx.annotation.StringRes
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.json.*
import persona.core.hub.HubFailure
import java.time.Instant
import java.util.UUID
import persona.core.inbox.*

// Comments on a Feishu document in the reader (#1290 / #2964; iOS FeishuDocsCommentsModel, web lib/doc-comments): the
// threads from the hub's read-only Lark gateway window and, when the plugin says `lark_doc_comments`, the user's own
// writes from this phone: a comment on the whole document or on a passage, a reply to a thread, resolve and reopen.
// They are not the AI's, so no gate asks (as with replies in the inbox, #1203). Routes: docs/protocol/protocol.md Lark API.

data class LarkDocCommentSegment(val kind: String, val text: String, val href: String? = null)

data class LarkDocCommentReply(
    val id: String, val author: String, val content: List<LarkDocCommentSegment>,
    val createdAt: Instant? = null, val edited: Boolean = false, val imageCount: Int = 0,
) {
    val text: String get() = content.joinToString("") { it.text }
}

data class LarkDocCommentThread(
    val id: String,
    /** A comment on the whole document; else it is on [quote]. */
    val whole: Boolean,
    val resolved: Boolean,
    val quote: String,
    val anchorBlockId: String? = null,
    val createdAt: Instant? = null,
    val replies: List<LarkDocCommentReply> = emptyList(),
    /** Seen by some people only (Feishu's partial visibility). */
    val partial: Boolean = false,
) {
    internal val lastActivity: Instant? get() = replies.mapNotNull { it.createdAt }.maxOrNull() ?: createdAt
}

/** Someone to @ in a comment (the plugin's people search, the shape of a group's members). */
data class LarkDocPerson(val userId: String, val name: String)

/** Where "Send" goes: a new comment on the whole document, a reply to a thread, or a new comment on a passage. */
sealed interface LarkDocCommentTarget {
    data object Document : LarkDocCommentTarget
    data class Thread(val id: String, val name: String, val text: String) : LarkDocCommentTarget
    data class Passage(val quote: String, val blockId: String? = null) : LarkDocCommentTarget
}

private fun JsonObject.string(key: String) = (get(key) as? JsonPrimitive)?.takeIf { it.isString }?.content
private fun instant(value: String?) = value?.let { runCatching { Instant.parse(it) }.getOrNull() }

/** `documents/comments` as the plugin gives it, open threads first (newest activity first); what is not a thread is left out. */
fun parseLarkDocComments(data: JsonObject, unnamed: String = ""): List<LarkDocCommentThread> =
    (data["threads"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { th ->
        val id = th.string("id") ?: return@mapNotNull null
        val replies = (th["replies"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { r ->
            val rid = r.string("id") ?: return@mapNotNull null
            val content = (r["content"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { s ->
                s.string("text")?.let { LarkDocCommentSegment(s.string("kind") ?: "text", it, s.string("href")) }
            }
            LarkDocCommentReply(rid, (r["author"] as? JsonObject)?.string("name")?.ifBlank { null } ?: unnamed, content,
                instant(r.string("createdAt")), r.flag("edited"), r.number("imageCount")?.toInt() ?: 0)
        }
        LarkDocCommentThread(id, th.flag("whole"), th.flag("resolved"), th.string("quote").orEmpty(), th.string("anchorBlockId"),
            instant(th.string("createdAt")), replies, partial = th.string("visibility") != null)
    }.let(::sortLarkDocComments)

fun sortLarkDocComments(threads: List<LarkDocCommentThread>): List<LarkDocCommentThread> =
    threads.sortedWith(compareBy<LarkDocCommentThread> { it.resolved }.thenByDescending { it.lastActivity ?: Instant.MIN })

/** The people a comment's text still @s (each picked one's `@name` is in it), once each. */
fun larkDocMentions(text: String, picked: List<LarkDocPerson>): List<LarkDocPerson> =
    larkMentions(text, picked.map { persona.core.contracts.api.LarkMention(it.userId, it.name) })
        .map { LarkDocPerson(it.userId, it.name) }

/** The name being looked up after an @ at the end of [text] (after a space or at the start, no space in it), or null. */
fun larkDocMentionQuery(text: String): String? = larkMentionQuery(text)

/** The routes the comments use; [LarkDocCommentsTransport] is the real one, tests give their own. */
interface LarkDocCommentsApi {
    suspend fun threads(url: String): JsonObject
    /** [thread] null: a new comment. Answers `{ok, threads}` (threads null when Feishu could not be read back). */
    suspend fun write(thread: String?, body: JsonObject): JsonObject
    suspend fun resolve(thread: String, url: String, resolved: Boolean): JsonObject
    suspend fun people(url: String, query: String): List<LarkDocPerson>
}

/** Through [source]: its pairing, and its manager when it has one. */
class LarkDocCommentsTransport(private val source: LarkContentSource) : LarkDocCommentsApi {
    override suspend fun threads(url: String) = source.read { documentComments(url) }
    override suspend fun write(thread: String?, body: JsonObject) = source.read { writeDocumentComment(thread, body) }
    override suspend fun resolve(thread: String, url: String, resolved: Boolean) = source.read { resolveDocumentComment(thread, url, resolved) }
    override suspend fun people(url: String, query: String) = (source.read { documentPeople(url, query) }["members"] as? JsonArray).orEmpty()
        .mapNotNull { it as? JsonObject }.mapNotNull { m ->
            val id = m.string("user_id") ?: return@mapNotNull null
            m.string("name")?.let { LarkDocPerson(id, it) }
        }
}

/** A failed write: what was tried ([action], a string with one `%1$s`) and why (the hub's words; null: unknown). */
data class LarkDocCommentFailure(@StringRes val action: Int, val reason: String?, val unsupported: Boolean = false)

/**
 * A document's comment threads and what the user is writing (docs/android/android.md: the logic beside the page): loading
 * them, "Send" to where [target] says, resolving and reopening. A write's answer carries the threads as Feishu has them
 * afterwards; when it cannot, they are read again.
 */
class LarkDocCommentsModel(val url: String, private val api: LarkDocCommentsApi, target: LarkDocCommentTarget = LarkDocCommentTarget.Document,
    private val unnamed: String = "",
) {
    var threads by mutableStateOf<List<LarkDocCommentThread>?>(null); private set
    var loadFailed by mutableStateOf(false); private set
    var draft by mutableStateOf("")
    var mentions by mutableStateOf<List<LarkDocPerson>>(emptyList())
    var target by mutableStateOf(target)
    var sending by mutableStateOf(false); private set
    /** Threads being resolved or reopened. */
    var resolving by mutableStateOf<Set<String>>(emptySet()); private set
    /** What went wrong with the last write, for the page to say; the draft stays. */
    var failure by mutableStateOf<LarkDocCommentFailure?>(null)
    /** The id of the send being tried: a retry of the same words to the same place reuses it. */
    private var pending: Pair<String, String>? = null
    /**
     * Reads count up when asked for, a write's answer when it comes: [threads] shows the highest so far, so a slow read
     * never takes back what a later one or a write's answer showed (#9936).
     */
    private var asked = 0
    private var shown = 0

    fun threads(resolved: Boolean) = threads.orEmpty().filter { it.resolved == resolved }

    suspend fun load() {
        val read = ++asked
        try {
            val got = parseLarkDocComments(api.threads(url), unnamed)
            if (read > shown) show(read, got)
        } catch (e: CancellationException) { throw e }
        catch (_: Exception) { if (read > shown) loadFailed = true }
    }

    val canSend: Boolean get() = !sending && draft.isNotBlank()

    /** Writes the draft where [target] says; on success the box empties and goes back to commenting on the document. */
    suspend fun send() {
        if (!canSend) return
        val text = draft.trim()
        val goal = target
        val key = "$goal|$text"
        val id = pending?.takeIf { it.first == key }?.second ?: UUID.randomUUID().toString()
        pending = key to id
        val body = buildJsonObject {
            put("url", url)
            put("content", text)
            putJsonArray("mentions") {
                larkDocMentions(text, mentions).forEach { m -> addJsonObject { put("user_id", m.userId); put("name", m.name) } }
            }
            if (goal is LarkDocCommentTarget.Passage) {
                put("quote", goal.quote)
                goal.blockId?.let { put("block_id", it) }
            }
            put("client_id", id)
        }
        sending = true
        try {
            val answer = api.write((goal as? LarkDocCommentTarget.Thread)?.id, body)
            pending = null
            draft = ""
            mentions = emptyList()
            target = LarkDocCommentTarget.Document
            failure = null
            settle(answer)
        } catch (e: CancellationException) { throw e }
        catch (e: Exception) { failure = failureOf(R.string.inbox_lark_doc_comments_send_failed, e) }
        finally { sending = false }
    }

    /** Resolves an open thread or reopens a resolved one. */
    suspend fun setResolved(thread: LarkDocCommentThread, resolved: Boolean) {
        if (thread.id in resolving) return
        resolving = resolving + thread.id
        try {
            settle(api.resolve(thread.id, url, resolved))
            failure = null
        } catch (e: CancellationException) { throw e }
        catch (e: Exception) {
            failure = failureOf(if (resolved) R.string.inbox_lark_doc_comments_resolve_failed else R.string.inbox_lark_doc_comments_reopen_failed, e)
        } finally { resolving = resolving - thread.id }
    }

    /** Replying to a thread: the box says to whom, the draft is kept. */
    fun reply(thread: LarkDocCommentThread) {
        val last = thread.replies.lastOrNull()
        target = LarkDocCommentTarget.Thread(thread.id, last?.author ?: unnamed, last?.text ?: thread.quote)
    }

    /** Picks [person] for the @ being typed at the end of the draft: `@name ` in its place. */
    fun mention(person: LarkDocPerson) {
        val query = larkDocMentionQuery(draft) ?: return
        draft = draft.dropLast(query.length + 1) + "@${person.name} "
        mentions = (mentions + person).distinctBy { it.userId }
    }

    suspend fun people(query: String): List<LarkDocPerson> = api.people(url, query)

    private suspend fun settle(answer: JsonObject) {
        if (answer["threads"] is JsonArray) show(++asked, parseLarkDocComments(answer, unnamed)) else load()
    }

    private fun show(answer: Int, list: List<LarkDocCommentThread>) {
        shown = answer
        threads = list
        loadFailed = false
    }

    private fun failureOf(@StringRes action: Int, e: Exception): LarkDocCommentFailure {
        val hub = runCatching { HubFailure.from(e) }.getOrNull()
        // The hub's words (the plugin's: "The Lark gateway is not signed in to Lark"…); a bare error kind says nothing to the user.
        val said = hub?.message?.takeIf { it.isNotBlank() && it != hub.kind.name }
        return LarkDocCommentFailure(action, said, unsupported = hub?.code == "comment_unsupported")
    }
}
