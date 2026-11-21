package persona.feature.chat

import androidx.compose.runtime.Stable
import androidx.compose.runtime.compositionLocalOf
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.Workflow
import persona.core.contracts.WorkflowAgent
import persona.core.contracts.api.ChatRepository
import persona.core.designsystem.ViewerImage
import persona.core.designsystem.WorkflowFolds
import java.util.Base64

/**
 * What a row deep in the chat list can do beyond drawing itself: the page builds one and provides it around the list,
 * so the row composables keep their parameter lists (the old App's ChatEnv).
 */
@Stable
internal class ChatRowActions(
    val session: SessionKey,
    /** The whole text of an item the hub cut for the page (`full_size`). */
    val fullText: suspend (Item) -> String,
    /** Opens a path from a message or a tool card on the session's machine, at its line if any; null: paths are not offered. */
    val openPath: ((path: String, line: Int?) -> Unit)?,
    /** Which paths messages name are there on the session's machine; null: not asked (paths stay as they were). */
    val paths: PathPresence? = null,
    /** Photos sent with messages (`<attached_images>`), read from the session's machine; null: they cannot load. */
    val photos: AttachedPhotos?,
    /** Every picture of the conversation in order, for the image viewer to page through (read when it opens). */
    val pictures: () -> List<ViewerImage>,
    /** Opens the image viewer on [images] at [index]. */
    val openImages: (images: List<ViewerImage>, index: Int) -> Unit,
    /** The long-press menu of messages; null where messages have none. */
    val menu: MessageMenuState?,
    /** The one API error card that offers "Retry" (the newest line, see [retryLine]); null: none does. */
    val retryLineId: String?,
    /** What "Retry" does: send "Continue". */
    val onRetry: () -> Unit,
    /** What a picture of the conversation is called to accessibility services, in the page's language. */
    val imageDescription: String = "",
    /** A file's thumbnail from the session's machine (`file/thumb`): its cache key and loader; null: icons only. */
    val thumbnails: ((path: String) -> Pair<String, suspend (Int) -> ByteArray?>)? = null,
    /** A picture of the session for the image viewer, called [description] (the turn's viewed images, `ViewedImagesRow`). */
    val viewerImage: (item: Item, description: String) -> ViewerImage = { item, description -> chatViewerImage(session, item, null, description) },
    /** The exhausted recovery chain the newest API error card explains (#7824, [exhaustedRecovery]); null: none. */
    val exhausted: ExhaustedRecovery? = null,
    val replyImages: persona.core.contracts.api.ReplyImageSource? = null,
    /** Local-path Markdown images on the session's machine (#12288); null when machine files are unavailable. */
    val localImages: LocalMarkdownImages? = null,
) {
    /** Opens the viewer on the conversation's pictures at [item]; a picture not among them opens alone. */
    fun openPicture(item: Item) {
        val all = pictures()
        val index = all.indexOfFirst { it.key == item.id }
        if (index >= 0) openImages(all, index)
        else openImages(listOf(chatViewerImage(session, item, null, imageDescription)), 0)
    }
}

internal val LocalChatRowActions = compositionLocalOf<ChatRowActions?> { null }

/**
 * The session's subagents by the Task / Agent call that started them, running or done: their conversations ([keys]) and
 * how to open one ([open], "View sub-agent" in Brief and Board; null where they cannot be opened: a read-only
 * conversation, no navigation), and the session's listing of each by `Subagents.callOf` ([agents]: what a task card says).
 */
@androidx.compose.runtime.Immutable
internal data class ChatSubagents(
    val keys: Map<String, SessionKey>,
    val open: ((SessionKey) -> Unit)?,
    val agents: Map<String, persona.core.contracts.Subagent> = emptyMap(),
)

internal val LocalChatSubagents = compositionLocalOf<ChatSubagents?> { null }

/**
 * The session's Workflow runs by the call that launched them ([runs], #3435): such a call draws its run's card in the
 * chat. [folds] keeps what the reader opened or folded in those cards while the chat is open; [open] opens one of a
 * run's agents, null where they cannot be opened (as [ChatSubagents]).
 */
@androidx.compose.runtime.Immutable
internal data class ChatWorkflows(
    val runs: Map<String, Workflow>,
    val folds: WorkflowFolds,
    val open: ((WorkflowAgent) -> Unit)?,
    val openBackground: ((Workflow) -> Unit)? = null,
)

internal val LocalChatWorkflows = compositionLocalOf<ChatWorkflows?> { null }

/**
 * A picture of the conversation for the viewer: its bytes come inline with the page when the hub sent them, else
 * from the repository by item id (pages drop picture bytes, docs/android/android.md "Cache size budgets").
 */
internal fun chatViewerImage(session: SessionKey, item: Item, repository: ChatRepository?, description: String = ""): ViewerImage {
    val image = item.image
    return ViewerImage(item.id, description, image?.width, image?.height) {
        val inline = image?.data?.takeIf { it.isNotEmpty() && image?.lazy != true }
        when {
            inline != null -> Base64.getMimeDecoder().decode(inline)
            repository != null -> repository.loadImage(session, item.id)
            else -> error("picture unavailable")
        }
    }
}

/** Every picture of the conversation in order: what the agent showed, the tools' screenshots, the photos it got. */
internal fun chatViewerImages(session: SessionKey, items: List<Item>, repository: ChatRepository?, description: String = ""): List<ViewerImage> =
    items.filter { it.kind == "image" && it.image != null }.map { chatViewerImage(session, it, repository, description) }

/** The image viewer to show: [images] from [index]. */
internal data class ViewerRequest(val images: List<ViewerImage>, val index: Int)
