package persona.feature.inbox

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.inbox.*

private fun List<InboxMute>.newestFirst() = sortedByDescending { it.createdAt.orEmpty() }

/**
 * The "Muted" list's state: the current assistant's mutes, newest first (each manager keeps its own, read and taken
 * back with it named). Unmute takes one off at once and puts it back on failure.
 */
@Stable
class InboxMutesModel(private val repository: InboxRepository) {
    var mutes by mutableStateOf<List<InboxMute>?>(null); private set

    /** Nothing to show: no mutes. */
    val isEmpty: Boolean get() = mutes?.isEmpty() == true

    private var reads = 0
    private var shownOf: String? = null

    /** Reads [manager]'s list (null: one manager, none named); only the newest read lands. Throws when it couldn't be read. */
    suspend fun load(manager: String? = null) {
        val read = ++reads
        // Another assistant's mutes never stay under this one's name.
        if (manager != shownOf) { mutes = null; shownOf = manager }
        val list = try { repository.inboxMutes(manager) }
        catch (e: CancellationException) { throw e }
        catch (e: Exception) { if (read != reads) return; throw e }
        if (read != reads) return
        mutes = if (manager == null) list else list.map { it.copy(manager = manager) }
    }

    /** Takes [m] back (on its manager) at once; throws what the hub said, with the row back. */
    suspend fun unmute(m: InboxMute) {
        val same = { x: InboxMute -> x.id == m.id && x.manager == m.manager }
        mutes = mutes?.filterNot(same)
        try {
            repository.unmute(m.id, m.manager)
            // A reload read before the unmute reached the hub may have brought it back.
            mutes = mutes?.filterNot(same)
        }
        catch (e: Exception) {
            // Put back only this one: another unmute that went through meanwhile stays gone.
            mutes = mutes?.let { list -> (list.filterNot(same) + m).newestFirst() }
            throw e
        }
    }
}

/**
 * "Muted" (the inbox's ⋯ menu): the repositories, chats and senders the current assistant has muted from an item's
 * menu, in every source, newest first. A tap takes one back; nothing it covered comes back, only what comes in later
 * (iOS `InboxMutesView`).
 */
@Composable
internal fun InboxMutesSheet(repository: InboxRepository, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val owner = currentAssistant().listOwner()
    val model = remember(repository) { InboxMutesModel(repository) }
    var failure by remember { mutableStateOf<String?>(null) }
    fun unmute(m: InboxMute) {
        scope.launch {
            try { model.unmute(m) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e) != true) failure = res.getString(R.string.inbox_unmute_failed) }
        }
    }
    fun load() {
        scope.launch {
            failure = null
            try { model.load(owner.id) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                if (model.mutes == null) failure = res.getString(if (owner.offline) R.string.inbox_mutes_offline else R.string.inbox_mutes_failed)
                else feedback?.failure(e)
            }
        }
    }
    LaunchedEffect(repository, owner) { load() } // another assistant, or this one back online, reads it again
    JSheet(onDismiss, title = stringResource(R.string.inbox_mutes_title), scrollable = true) {
        val list = model.mutes
        @Composable fun mute(m: InboxMute) =
            JRow(m.name, subtitle = listOf(m.sourceName ?: m.source, stringResource(m.tagName)).filter { it.isNotBlank() }.joinToString(" · "),
                leadingIcon = PhIcons.Prohibit, subtitleLines = 1,
                trailing = { JTextButton(stringResource(R.string.inbox_unmute), { unmute(m) }) })
        when {
            failure != null -> JNotice(failure.orEmpty(), status = Status.NeedsYou, actionLabel = stringResource(R.string.inbox_retry), onAction = ::load)
            list == null -> JLoading()
            model.isEmpty -> JEmptyState(stringResource(R.string.inbox_mutes_empty_title), stringResource(R.string.inbox_mutes_empty_text), icon = PhIcons.Prohibit)
            else -> JGroup(rows = list.map { m -> { mute(m) } })
        }
    }
}
