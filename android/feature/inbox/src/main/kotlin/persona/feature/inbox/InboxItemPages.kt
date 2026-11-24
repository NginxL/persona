package persona.feature.inbox

import androidx.compose.ui.res.stringResource

import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.ui.Modifier
import androidx.navigation3.runtime.NavKey
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.designsystem.JNotice
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.navigation.InboxItemUiRoute
import persona.core.inbox.*

// An inbox item's page drawn by an extension (docs/extensions/extensions.md "UI slots" `inbox_item`, #1683): an extension's connector (the
// Feishu extension's `lark`) may draw items it brings in. Chats keep the native conversation and reply composer by default.
// Opening another such item shows its native header over the extension's view (InboxItemExtensionScreen); every other
// item, and every item on a hub that lists no
// such page, opens its own page as before. When the extension cannot draw an item, its own page opens instead, saying so
// in a line with "Retry" when the extension broke rather than declined.

/**
 * The `inbox_item` pages the hub lists, by the connector whose items each draws; [failures] are the items whose page
 * broke (item id → what to say), shown on their own pages until [retry] tries the extension's page again in place of
 * the page it is called from. Until the hub's list is [known] (at launch, before its hello), an item opens its own
 * page and [provisional] hears of it, so the shell can replace eligible pages once the list comes.
 */
data class InboxItemPages(
    val places: List<ExtensionUiPlace> = emptyList(),
    val failures: Map<String, String> = emptyMap(),
    val retry: (from: NavKey, itemId: String) -> Unit = { _, _ -> },
    val known: Boolean = true,
    val provisional: (NavKey) -> Unit = {},
    val nativeChats: Boolean = true,
) {
    /** The extension's page for [record], when the connector it comes from has one; the first listed wins. */
    fun place(record: InboxRecord): ExtensionUiPlace? {
        val source = record.source.takeIf { it.isNotEmpty() } ?: return null
        return places.firstOrNull { it.placement == Placement && it.connector == source && it.extension.isNotEmpty() && it.id.isNotEmpty() }
    }

    /** The device preference controls item chats, including late slot discovery and extension retries. */
    fun route(record: InboxRecord): InboxItemUiRoute? {
        if (nativeChats && inboxItemPage(record) is LarkRoute) return null
        return place(record)?.let { InboxItemUiRoute(record.item.id, it.extension, it.id, record.item.title) }
    }

    /** Opening [record] (from the list, or an item link): [inboxOpenPage], told to [provisional] while the list is unknown. */
    fun open(record: InboxRecord): NavKey = inboxOpenPage(record, this).also(::opened)

    /** [page] was opened for an item: an own page opened before the list was known may be the extension's to draw. */
    fun opened(page: NavKey) { if (!known && page !is InboxItemUiRoute) provisional(page) }

    /**
     * Once the list is known: the item pages of [stack] opened before it was ([provisional]) whose connector's extension
     * draws them, each with the extension's page to put in its place. Native conversations stay chats while preferred, even if provisional.
     */
    fun swaps(stack: List<NavKey>, provisional: Set<NavKey>, records: List<InboxRecord>): List<Pair<NavKey, InboxItemUiRoute>> =
        stack.filter { it in provisional }.mapNotNull { key ->
            val itemId = when (key) { is LarkRoute -> key.itemId; is MailRoute -> key.itemId; is LarkMomentRoute -> key.itemId
                is InboxItemDetailRoute -> key.itemId; is ConnectorThreadRoute -> key.itemId; else -> null }
            records.inboxRecord(itemId)?.let(::route)?.let { key to it }
        }

    companion object {
        const val Placement = "inbox_item"
    }
}

/** The shell's list of `inbox_item` pages; none in demo mode and on a hub without them. */
val LocalInboxItemPages = compositionLocalOf { InboxItemPages() }

/**
 * The item's own page: a Moments post, its Feishu chat at the item's first message, the mail, the assistant's conversation card,
 * another hub connector's thread (any connector, #2726), or else its detail page (a Feishu document's notice, and any item of a shape this app does not
 * know: the mail page could only fail for it, #2323).
 */
fun inboxItemPage(record: InboxRecord): NavKey = record.moment?.let { LarkMomentRoute(it.postId, record.item.id) }
    ?: record.feedId?.let { LarkRoute(it, record.item.id, record.startPosition) }
    ?: MailRoute(record.item.id).takeIf { record.isMail && record.doc == null }
    ?: InboxItemDetailRoute(record.item.id).takeIf { record.isAssistantRow }
    ?: ConnectorThreadRoute(record.item.id).takeIf { record.isConnector && record.connector?.thread?.isNotBlank() == true && record.doc == null }
    ?: InboxItemDetailRoute(record.item.id)

/** Preferred chats open natively; other items use the connector page when available, else their own page. */
fun inboxOpenPage(record: InboxRecord, pages: InboxItemPages): NavKey = pages.route(record) ?: inboxItemPage(record)

/**
 * Where an extension's `persona://inbox/<id>/source` goes: item [itemId]'s own page, what opening it did before extensions
 * drew items (for Feishu, the chat at the item's first message). Null when the item is not in [records].
 */
fun inboxSourcePage(itemId: String, records: List<InboxRecord>): NavKey? = records.inboxRecord(itemId)?.let(::inboxItemPage)

/**
 * On an item's own page opened because its extension's page broke: the one line saying so, with "Retry" (the extension's page
 * again, in place of [from]). Nothing otherwise.
 */
@Composable
internal fun InboxExtensionNotice(itemId: String?, from: NavKey, modifier: Modifier = Modifier.padding(bottom = Tokens.Space.s)) {
    val pages = LocalInboxItemPages.current
    if (pages.nativeChats && from is LarkRoute) return
    val message = itemId?.let { pages.failures[it] } ?: return
    JNotice(message, modifier, status = Status.NeedsYou, actionLabel = stringResource(R.string.inbox_retry), onAction = { pages.retry(from, itemId) })
}
