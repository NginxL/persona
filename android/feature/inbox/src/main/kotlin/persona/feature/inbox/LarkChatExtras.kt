package persona.feature.inbox

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import java.time.Instant
import java.time.ZoneId
import persona.core.inbox.*

// A Feishu chat's extras that iOS has (LarkChatPins, LarkReadReceipts, LarkMessageDisplay): "Pinned messages", the read receipt
// beside my messages, and message display ("Keep recalled messages", "Edit history") applied to the rows.

/** A recalled message's kept words show with "Keep recalled messages" on; a message never recalled always shows. */
fun LarkMessageDisplay.showsBody(message: LarkMessage): Boolean =
    !message.recalled || (antiRecall && message.retained && message.parts.isNotEmpty())

/** "Edited" opens the earlier versions with "Edit history" on, when the Lark gateway kept any. */
fun LarkMessageDisplay.showsHistory(message: LarkMessage): Boolean =
    showEditHistory && showsBody(message) && message.editHistory.isNotEmpty()

data class LarkChatPin(
    val id: String,
    val messageId: String,
    val message: LarkMessage? = null,
    val operatorName: String? = null,
    val createdAt: String? = null,
    /** The original message is gone (deleted upstream): the row says so and does not open. */
    val deleted: Boolean = false,
) {
    val canRemove: Boolean get() = message?.canUnpin == true
}

data class LarkChatPinsPage(val accountId: String, val items: List<LarkChatPin>, val nextCursor: String?, val total: Int)

/** Where a pin's message is: [root] is the thread it replies in (it opens there), else the chat around it. */
data class LarkPinTarget(val message: LarkMessage, val root: LarkMessage? = null)

/** What a pin change could not say: the account changed, the result is unknown, or the hub has no "Pinned messages" yet. */
class LarkPinFailure(val kind: Kind) : Exception(kind.name) {
    enum class Kind { AccountChanged, ResultUnknown, UpgradeRequired }
}

internal fun larkPinsPage(body: JsonObject, map: (JsonObject) -> LarkMessage): LarkChatPinsPage {
    val items = (body["items"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { p ->
        val id = p.text("id").takeIf { it.isNotBlank() } ?: return@mapNotNull null
        val message = p["message"] as? JsonObject
        LarkChatPin(id, p.text("message_id"), message?.let(map), p.cursor("operator_name"), p.cursor("created_at"),
            deleted = message?.flag("deleted") == true)
    }.distinctBy { it.id }
    return LarkChatPinsPage(body.text("account_id"), items, body.cursor("next_cursor"),
        maxOf(body.number("total")?.toInt() ?: 0, items.size))
}

/** A pin's line in the list: what it says, or why it cannot (iOS `LarkChatPinsView.summary`). */
@Composable
internal fun larkPinSummary(pin: LarkChatPin, display: LarkMessageDisplay): String {
    val m = pin.message
    if (m == null || pin.deleted) return stringResource(R.string.inbox_lark_pin_unavailable)
    if (!display.showsBody(m)) return stringResource(R.string.inbox_lark_recalled)
    val image = stringResource(R.string.inbox_image)
    val text = m.parts.joinToString(" ") { part ->
        when (part) {
            is LarkPart.Text -> part.text
            is LarkPart.Code -> part.code
            is LarkPart.Image -> "[$image]"
            is LarkPart.File -> "[" + part.name + "]"
            else -> ""
        }
    }.trim()
    val shown = text.ifBlank { stringResource(R.string.inbox_lark_pin_message) }
    return if (m.recalled) stringResource(R.string.inbox_lark_pin_recalled_suffix, shown) else shown
}

/**
 * "Pinned messages" (the chat's ⋯ menu): the chat's pins with who wrote each, a tap opens the message where it is, ⋯ "Remove pin"
 * where the chat allows it. Opened only on request: it never loads from the inbox (iOS `LarkChatPinsView`).
 */
@Composable
internal fun LarkPinsSheet(repository: InboxRepository, feedId: String, onOpen: (LarkPinTarget) -> Unit, onDismiss: () -> Unit,
    /** A topic group: the hub resolves a pin's thread with it. */
    topic: Boolean = false,
) {
    val scope = rememberCoroutineScope()
    val res = LocalResources.current
    val feedback = LocalAppFeedback.current
    val display = LocalLarkMessageDisplay.current
    val connection by repository.connectionState.collectAsStateWithLifecycle(persona.core.contracts.api.ConnectionState.Connecting)
    val onlineSince = rememberOnlineSince(connection)
    var page by remember { mutableStateOf<LarkChatPinsPage?>(null) }
    var loading by remember { mutableStateOf(false) }
    var problem by remember { mutableStateOf<String?>(null) }
    var locating by remember { mutableStateOf<String?>(null) }
    var removing by remember { mutableStateOf<LarkChatPin?>(null) }
    var menu by remember { mutableStateOf<LarkChatPin?>(null) }
    // The Feishu account changed under the sheet: the old account's pins are gone and nothing in it acts any more.
    var blocked by remember { mutableStateOf(false) }
    // A change whose result is unknown (or just made): "Remove pin" waits for a list loaded after it (iOS `requiresRecheck`).
    var requiresRecheck by remember { mutableStateOf(false) }
    fun failureText(e: Exception): String = res.getString(when ((e as? LarkPinFailure)?.kind) {
        LarkPinFailure.Kind.AccountChanged -> R.string.inbox_lark_pin_account_changed
        LarkPinFailure.Kind.ResultUnknown -> R.string.inbox_lark_pin_unknown
        LarkPinFailure.Kind.UpgradeRequired -> R.string.inbox_lark_pin_upgrade
        null -> R.string.inbox_lark_pins_failed
    })
    fun fail(e: Exception) {
        if ((e as? LarkPinFailure)?.kind == LarkPinFailure.Kind.AccountChanged) { page = null; blocked = true; menu = null; removing = null }
        problem = failureText(e)
    }
    fun load(more: Boolean = false) {
        if (loading || blocked) return
        val cursor = if (more) page?.nextCursor ?: return else null
        scope.launch {
            loading = true
            try {
                val next = repository.larkChatPins(feedId, page?.accountId?.takeIf { it.isNotBlank() }, cursor)
                val before = page
                page = if (more && before != null) next.copy(items = (before.items + next.items).distinctBy { it.id }) else next
                problem = null
                if (!more) requiresRecheck = false
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { fail(e) }
            finally { loading = false }
        }
    }
    LaunchedEffect(feedId) { load() }
    JSheet(onDismiss, title = stringResource(R.string.inbox_lark_pins_title), scrollable = true) {
        problem?.let {
            if (blocked) JNotice(it, status = Status.NeedsYou)
            else JNotice(it, status = Status.NeedsYou, actionLabel = stringResource(R.string.inbox_lark_pins_reload), onAction = { load() })
        }
        val p = page
        when {
            p == null && loading -> JLoading(text = stringResource(R.string.inbox_lark_pins_loading))
            p != null && p.items.isEmpty() && problem == null ->
                JEmptyState(stringResource(R.string.inbox_lark_pins_empty_title), stringResource(R.string.inbox_lark_pins_empty_text), icon = PhIcons.PushPin)
            p != null && p.items.isNotEmpty() -> {
                JSectionHeader(pluralStringResource(R.plurals.inbox_lark_pins_count, p.total, p.total))
                // Kept recalled words only under the "Keep recalled messages" of the account these pins answer for.
                val policy = larkPinPolicy(display, p.accountId)
                val removable = !blocked && !requiresRecheck
                JGroup(rows = p.items.map { pin -> {
                    val m = pin.message
                    JRow(
                        onClick = if (blocked || m == null || pin.deleted || locating != null) null else ({
                            locating = pin.id
                            scope.launch {
                                try { onOpen(repository.larkPinTarget(feedId, pin.messageId, p.accountId, topic)) }
                                catch (e: CancellationException) { throw e }
                                catch (e: Exception) { fail(e) }
                                finally { locating = null }
                            }
                        }),
                        onLongClick = if (pin.canRemove && removable) ({ menu = pin }) else null,
                        chevron = false,
                        trailing = {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                if (locating == pin.id) JSpinner()
                                if (pin.canRemove && !blocked) JIconButton(PhIcons.DotsThree, stringResource(R.string.inbox_lark_pin_actions), { menu = pin }, enabled = removable)
                            }
                        },
                    ) {
                        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                            if (m != null) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                                LarkAvatar(m.senderName.ifBlank { stringResource(R.string.inbox_lark_avatar_fallback) }, m.avatar, onlineSince)
                                Text(m.senderName, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            }
                            Text(larkPinSummary(pin, policy), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink, maxLines = 3, overflow = TextOverflow.Ellipsis)
                            pin.createdAt?.let { larkPinTime(it, currentLocale) }?.let { Text(it, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3) }
                        }
                    }
                } })
                if (p.nextCursor != null) Box(Modifier.fillMaxWidth().padding(top = Tokens.Space.s), contentAlignment = Alignment.Center) {
                    JTextButton(stringResource(if (loading) R.string.inbox_lark_loading else R.string.inbox_lark_pins_more), { load(more = true) }, enabled = !loading && !blocked)
                }
            }
        }
    }
    menu?.let { pin ->
        JActionSheet(listOf(JAction(stringResource(R.string.inbox_lark_pin_remove), PhIcons.Trash, destructive = true) { removing = pin }),
            onDismiss = { menu = null }, title = stringResource(R.string.inbox_lark_pin_actions))
    }
    removing?.let { pin ->
        JDialog(onDismiss = { removing = null }, title = stringResource(R.string.inbox_lark_pin_remove_title),
            text = stringResource(R.string.inbox_lark_pin_remove_text), destructive = true,
            confirm = stringResource(R.string.inbox_lark_pin_remove) to {
                removing = null
                val account = page?.accountId ?: return@to
                requiresRecheck = true
                scope.launch {
                    try { repository.larkChangePin(feedId, pin.messageId, pin.id, account); load() }
                    catch (e: CancellationException) { throw e }
                    catch (e: Exception) {
                        // Only a hub answer that says it did not happen lets "Remove pin" act again before a reload; a timeout or
                        // an unknown result may have removed it.
                        requiresRecheck = when (val failure = e) {
                            is LarkPinFailure -> failure.kind == LarkPinFailure.Kind.ResultUnknown
                            is persona.core.hub.HubFailure -> failure.status == null
                            else -> true
                        }
                        fail(e)
                    }
                }
            })
    }
}

/** The message display settings to read pins with: the chat's, when it is for the account the pins answer for; else kept content stays hidden. */
internal fun larkPinPolicy(display: LarkMessageDisplay, pinsAccountId: String): LarkMessageDisplay =
    if (display.accountId.isNotBlank() && display.accountId == pinsAccountId) display else LarkMessageDisplay.Hidden

/** A pin's or edit's date and time in the language the app is in: "Nov 5, 2026, 2:30 PM" in English, the same in each language's own form. */
internal fun larkPinTime(text: String, locale: java.util.Locale, zone: ZoneId = ZoneId.systemDefault()): String? = runCatching {
    LocalizedFormats.format(Instant.parse(text), LocalizedFormats.DATE_TIME, locale, zone)
}.getOrNull()

// MARK: Read receipts

/** The Lark gateway's read receipt of one of my messages: how many in the chat have read it and how many have not. */
data class LarkReadState(val messageId: String, val readCount: Int, val unreadCount: Int) {
    /** Read by everyone it went to: it will not change, so it is not asked about again. */
    val isFinal: Boolean get() = unreadCount == 0 && readCount > 0
}

internal fun larkReadStates(body: JsonObject): List<LarkReadState> =
    (body["states"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { s ->
        val id = s.text("message_id").takeIf { it.isNotBlank() } ?: return@mapNotNull null
        LarkReadState(id, s.number("read_count")?.toInt() ?: 0, s.number("unread_count")?.toInt() ?: 0)
    }

/** What sits beside a message of mine: "Read" / "Unread" with one reader, else how many have not read it, or "All read". */
sealed interface LarkReadMark {
    data object Read : LarkReadMark
    data object Unread : LarkReadMark
    data object AllRead : LarkReadMark
    data class Progress(val read: Int, val total: Int) : LarkReadMark

    companion object {
        /**
         * Null without a receipt or with nobody to read it: nothing is shown rather than a state never reported.
         * [isGroup] as iOS: a one-to-one chat reads "Read" / "Unread", a group "All read" or how many have not; null (a chat the
         * inbox does not list, so its kind is unknown here) goes by the count.
         */
        fun of(state: LarkReadState?, isGroup: Boolean?): LarkReadMark? {
            val s = state ?: return null
            if (s.readCount < 0 || s.unreadCount < 0 || s.readCount + s.unreadCount == 0) return null
            return when {
                !(isGroup ?: (s.readCount + s.unreadCount > 1)) -> if (s.unreadCount == 0) Read else Unread
                s.unreadCount == 0 -> AllRead
                else -> Progress(s.readCount, s.readCount + s.unreadCount)
            }
        }
    }
}

/**
 * Which receipts to ask for, and when (iOS `LarkReadReceipts`): mine already in Feishu, at most [BATCH] ids a request,
 * one request per chat every [GAP_MS]; one everyone has read is not asked again, and an old hub (404) not during the visit.
 */
class LarkReadReceipts {
    var states: Map<String, LarkReadState> = emptyMap(); private set
    private var lastAsked = 0L
    var unsupported = false; private set

    fun tracks(m: LarkMessage) = m.mine && !m.system && !m.recalled && m.id.isNotBlank() && m.position != null && m.delivery == LarkDelivery.Sent

    /** The ids to ask about now among [messages] (oldest first: the newest are kept), or empty when it is too soon. */
    fun due(messages: List<LarkMessage>, now: Long): List<String> {
        if (unsupported || now - lastAsked < GAP_MS) return emptyList()
        val ids = messages.filter(::tracks).filter { states[it.id]?.isFinal != true }.map { it.id }.distinct().takeLast(BATCH)
        if (ids.isNotEmpty()) lastAsked = now
        return ids
    }

    /** How long until the next request may go (0: now). */
    fun wait(now: Long): Long = (lastAsked + GAP_MS - now).coerceAtLeast(0)

    fun record(got: List<LarkReadState>) { states = states + got.associateBy { it.messageId } }

    fun failed(status: Int?) { if (status == 404 || status == 405) unsupported = true }

    companion object {
        const val BATCH = 100
        const val GAP_MS = 3_000L
    }
}

@Composable
internal fun LarkReadMarkView(mark: LarkReadMark) {
    val palette = PersonaTheme.palette
    val label = when (mark) {
        LarkReadMark.Read -> stringResource(R.string.inbox_lark_read)
        LarkReadMark.Unread -> stringResource(R.string.inbox_lark_unread)
        LarkReadMark.AllRead -> stringResource(R.string.inbox_lark_all_read)
        is LarkReadMark.Progress -> stringResource(R.string.inbox_lark_unread_count, mark.total - mark.read)
    }
    Row(Modifier.semantics(mergeDescendants = true) { contentDescription = label }, verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Text(label, style = PersonaTheme.type.micro, maxLines = 1,
            color = if (mark == LarkReadMark.Read || mark == LarkReadMark.AllRead) palette.ink3 else palette.accent)
    }
}

// MARK: Edit history

/** "Edit history" of a message (iOS `LarkEditHistoryView`): its earlier versions with when each was replaced. */
@Composable
internal fun LarkEditHistorySheet(message: LarkMessage, onDismiss: () -> Unit) {
    val display = LocalLarkMessageDisplay.current
    JSheet(onDismiss, title = stringResource(R.string.inbox_lark_edit_history), scrollable = true) {
        if (!display.showsHistory(message)) Text(stringResource(R.string.inbox_lark_edit_history_hidden), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        else JGroup(rows = message.editHistory.map { v -> {
            JRow {
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    larkPinTime(v.editedAt, currentLocale)?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
                    Text(v.text, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
                }
            }
        } })
    }
}
