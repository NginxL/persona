package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import persona.core.contracts.api.ReplyImageSource
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.remember
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.InboxRoute
import persona.core.navigation.LarkMessagesRoute
import persona.core.navigation.Navigator
import persona.core.contracts.api.ManagerPins
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** The shell supplies the chat Markdown renderer and its existing image viewer for external thread pictures. */
typealias InboxPictureMarkdown = @Composable (String, ReplyImageSource, Modifier) -> Unit
internal val LocalInboxPictureMarkdown = staticCompositionLocalOf<InboxPictureMarkdown?> { null }

@Composable
internal fun InboxPictureText(text: String, source: ReplyImageSource?) {
    val pictures = LocalInboxPictureMarkdown.current
    if ("![" in text && source != null && pictures != null) pictures(text, source, Modifier)
    else androidx.compose.material3.Text(text, style = persona.core.designsystem.PersonaTheme.type.body,
        color = persona.core.designsystem.PersonaTheme.palette.ink)
}

val LocalInboxRepository = staticCompositionLocalOf<InboxRepository?> { null }
internal val LocalInboxNavigator = staticCompositionLocalOf<Navigator?> { null }

@Serializable @SerialName("persona.feature.inbox.LarkRoute")
data class LarkRoute(
    val feedId: String,
    val itemId: String? = null,
    val startPosition: Long? = null,
    /** Only the optional Messages browser may reuse its summary; Inbox and deep links keep their established lookup. */
    val fromMessages: Boolean = false,
) : NavKey

fun EntryProviderScope<NavKey>.inboxEntries(
    navigator: Navigator,
    repository: InboxRepository? = null,
    /** A real pairing is still assembling its repository; keep a restored route in loading rather than deleting it. */
    larkMessagesLoading: Boolean = false,
    /** The hub's connectors (their threads and replies); null in demo mode: a connector's item opens its detail page. */
    connectors: persona.core.contracts.api.ConnectorsManagement? = null,
    mailTransport: persona.core.contracts.api.MailTransport? = null,
    onMailAskAi: ((MailAiContext) -> Unit)? = null,
    onMailOpenLink: ((String) -> Unit)? = null,
    /** Opens a request card outside any session on Agents (#5610). */
    onOpenCard: ((persona.core.navigation.RequestRef) -> Unit)? = null,
    pictureMarkdown: InboxPictureMarkdown? = null,
) {
    entry<InboxRoute> {
        val source = repository ?: LocalInboxRepository.current ?: injectedInboxRepository()
        InboxScreen(source, navigator, onOpenCard)
    }
    entry<MailRoute> { route ->
        val shared = repository ?: LocalInboxRepository.current ?: injectedInboxRepository()
        val pins = itemPins(shared, route.itemId)
        val source = remember(shared, pins) { shared.pinned(pins) }
        val pairedMail by source.mailTransport.collectAsStateWithLifecycle()
        val mail = mailTransport ?: LocalMailTransport.current ?: pairedMail
        val pinnedMail = remember(mail, pins) { mail?.pinnedForPage(pins) }
        LarkLinkHost(source, itemManager(source, route.itemId)) {
            val opener = LocalLarkLinkOpener.current
            MailScreen(source, pinnedMail, route, navigator,
                onMailAskAi, onMailOpenLink ?: { url -> opener?.invoke(url, null); Unit })
        }
    }
    entry<LarkMessagesRoute> {
        val source = repository ?: LocalInboxRepository.current ?: injectedInboxRepository()
        val messages by source.larkMessages.collectAsStateWithLifecycle()
        val connection by source.connectionState.collectAsStateWithLifecycle(
            initialValue = persona.core.contracts.api.ConnectionState.Connecting,
        )
        CompositionLocalProvider(LocalInboxNavigator provides navigator, LocalInboxPictureMarkdown provides pictureMarkdown) {
            messages?.let { LarkMessagesScreen(it, connection, navigator) } ?: LarkMessagesUnavailable(larkMessagesLoading)
        }
    }
    entry<LarkRoute> { route ->
        val source = pinnedForItem(repository ?: LocalInboxRepository.current ?: injectedInboxRepository(), route.itemId)
        CompositionLocalProvider(LocalInboxNavigator provides navigator, LocalInboxPictureMarkdown provides pictureMarkdown) {
            LarkScreen(source, route, navigator)
        }
    }
    entry<InboxItemDetailRoute> { route ->
        val source = pinnedForItem(repository ?: LocalInboxRepository.current ?: injectedInboxRepository(), route.itemId)
        CompositionLocalProvider(LocalInboxNavigator provides navigator, LocalInboxPictureMarkdown provides pictureMarkdown) {
            LarkLinkHost(source, itemManager(source, route.itemId)) { InboxItemDetailScreen(source, route, navigator) }
        }
    }
    entry<InboxEventRoute> { route ->
        val source = pinnedForItem(repository ?: LocalInboxRepository.current ?: injectedInboxRepository(), route.itemId)
        CompositionLocalProvider(LocalInboxNavigator provides navigator, LocalInboxPictureMarkdown provides pictureMarkdown) {
            LarkLinkHost(source, itemManager(source, route.itemId)) { InboxEventScreen(source, route, navigator) }
        }
    }
    entry<ConnectorThreadRoute> { route ->
        val shared = repository ?: LocalInboxRepository.current ?: injectedInboxRepository()
        val pins = itemPins(shared, route.itemId)
        val source = remember(shared, pins) { shared.pinned(pins) }
        CompositionLocalProvider(LocalInboxNavigator provides navigator, LocalInboxPictureMarkdown provides pictureMarkdown) {
            LarkLinkHost(source, itemManager(source, route.itemId)) {
                if (connectors == null) InboxItemDetailScreen(source, InboxItemDetailRoute(route.itemId), navigator)
                else ConnectorThreadScreen(source, connectors, route, navigator, pins)
            }
        }
    }
    entry<LarkMomentRoute> { route ->
        val source = pinnedForItem(repository ?: LocalInboxRepository.current ?: injectedInboxRepository(), route.itemId)
        CompositionLocalProvider(LocalInboxNavigator provides navigator, LocalInboxPictureMarkdown provides pictureMarkdown) {
            LarkMomentsScreen(source, route, navigator)
        }
    }
}

@Serializable @SerialName("persona.feature.inbox.MailRoute")
data class MailRoute(val itemId: String, val uid: Long? = null) : NavKey

@Serializable @SerialName("persona.feature.inbox.LarkMomentRoute")
data class LarkMomentRoute(val postId: String, val itemId: String? = null) : NavKey

/** The own page of an item that is neither a chat, a Moments post nor a mail: a Feishu document's notice, or an unknown item. */
@Serializable @SerialName("persona.feature.inbox.InboxItemDetailRoute")
data class InboxItemDetailRoute(val itemId: String) : NavKey

/** A hub connector's item opened as its conversation (ConnectorThread.kt): the thread its `connector` block names. */
@Serializable @SerialName("persona.feature.inbox.ConnectorThreadRoute")
data class ConnectorThreadRoute(val itemId: String) : NavKey

val LocalMailTransport = staticCompositionLocalOf<persona.core.contracts.api.MailTransport?> { null }

/** What an inbox item's pages read besides the item itself (its Lark chat, its mail, its connector's thread). */
internal val ItemDetailPrefixes = listOf(
    listOf("lark", "conversations"), listOf("lark", "messages"), listOf("lark", "users"), listOf("lark", "moments"),
    listOf("lark", "media"), listOf("lark", "pinned"), listOf("lark", "documents"), listOf("lark", "raw"),
    listOf("mail", "messages"), listOf("connectors"),
)

/**
 * What an inbox item's page reads besides the item goes to the item's manager (the merged list's stamp,
 * docs/assistant/managers.md "Clients"): the pins of that page's own requests, carried by the APIs it hands its
 * children. An unstamped item (one manager) pins nothing. The default's stamp still makes pins, so the page's APIs are
 * still wrapped; they name no manager on the wire (no header, no `?manager=`).
 */
@androidx.compose.runtime.Composable
internal fun itemPins(source: InboxRepository, itemId: String?): ManagerPins? {
    val snapshot by source.snapshot.collectAsStateWithLifecycle()
    val record = snapshot.records.inboxRecord(itemId)
    val manager = record?.manager
    // the item itself too (its mail page reads it), by its id on the hub (InboxItemKey)
    val hubItemId = record?.hubItemId
    return remember(manager, hubItemId) {
        ManagerPins.of(ItemDetailPrefixes + listOfNotNull(hubItemId?.let { listOf("inbox", it) }), manager)
    }
}

/** [source] with the pins of the page of the item [itemId] (itemPins). */
@androidx.compose.runtime.Composable
internal fun pinnedForItem(source: InboxRepository, itemId: String?): InboxRepository {
    val pins = itemPins(source, itemId)
    return remember(source, pins) { source.pinned(pins) }
}

/** The manager the item [itemId] names is stamped with (null: none known), for the links its page opens (LarkLinkHost). */
@androidx.compose.runtime.Composable
internal fun itemManager(source: InboxRepository, itemId: String?): String? {
    val snapshot by source.snapshot.collectAsStateWithLifecycle()
    return snapshot.records.inboxRecord(itemId)?.manager
}
