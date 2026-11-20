package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import kotlinx.coroutines.launch
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.designsystem.LocalPersonaName

/**
 * Intent-only message actions and the forward sheet. Keeping these out of the already-large ChatPage composable lets
 * ART compile the page, while the draft and its complete-text lookup stay scoped to this interaction.
 */
@Composable
internal fun ChatMessageActions(
    key: SessionKey,
    session: Session?,
    machine: Machine?,
    renderStyle: ChatRenderStyle,
    repository: ChatRepository,
    readOnly: Boolean,
    composer: ChatComposerState,
    messageMenu: MessageMenuState,
    quoteTarget: (MessageTarget) -> Unit,
    onContext: (String) -> Unit,
    onNotice: (String) -> Unit,
    onFork: ((String) -> Unit)?,
    rewindMenu: RewindMenu?,
    onDelete: (MessageTarget) -> Unit,
) {
    val resources = LocalResources.current
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val forwarding = LocalChatForwarding.current
    val photoReader = LocalChatRowActions.current?.photos
    val pick = messageMenu.forwardPick
    val focus = LocalFocusManager.current
    MessageMenuSheet(messageMenu, messageMenuWrites(readOnly, session),
        onQuote = quoteTarget, onEdit = composer::edit,
        onContext = onContext, onForward = forwarding?.let { pick::open },
        onNotice = onNotice, onFork = onFork, rewind = rewindMenu, onDelete = onDelete,
        share = shareCardSource(session, LocalPersonaName.current))
    val targets = pick.sheet ?: return
    if (forwarding == null) return
    val drafts = remember(targets, key, session, machine, renderStyle, forwarding) {
        targets.map { forwardDraftOf(it, key, session, machine, renderStyle, forwarding) }
    }
    ForwardSheet(drafts, forwarding,
        complete = {
            val limit = ForwardMessage.limit(drafts.size)
            // Read all clipped text before staging any photos: a failed read leaves the picker ready to retry.
            val refs = drafts.map { draft ->
                val raw = draft.cut?.let { repository.fullText(key, it) } ?: draft.raw
                ForwardMessage.ref(raw, draft.source, resources, limit = limit)
            }
            val completed = mutableListOf<ForwardPlacement>()
            try {
                var remainingPhotos = MAX_COMPOSER_MEDIA
                for ((index, draft) in drafts.withIndex()) {
                    val paths = draft.photos.take(remainingPhotos)
                    val restored = when {
                        paths.isEmpty() -> RestoredPhotos()
                        photoReader == null -> RestoredPhotos(lost = paths.size)
                        else -> forwardedPhotos(context, paths, photoReader::payload)
                    }
                    remainingPhotos -= restored.attachments.size
                    completed += ForwardPlacement(refs[index], restored.copy(lost = restored.lost + draft.photos.size - paths.size))
                }
                completed
            } catch (failure: Throwable) {
                kotlinx.coroutines.withContext(kotlinx.coroutines.NonCancellable) {
                    completed.forEach { discardForwardedPhotos(context, it.photos) }
                }
                throw failure
            }
        },
        onSelectMessages = if (pick.selected != null) null else ({
            focus.clearFocus()
            pick.start()
        }),
        onDismiss = pick::dismiss,
        onPicked = { target, completed ->
            pick.end()
            deliverForward(target, completed, key, forwarding, resources,
                onDuplicate = { handoff -> scope.launch { discardForwardedPhotos(context, handoff) } })
        })
}
