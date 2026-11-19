package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
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
    var forwardDraft by remember(key) { mutableStateOf<ForwardDraft?>(null) }
    val menuTitle = session?.displayTitle ?: stringResource(R.string.chat_screen_untitled)
    MessageMenuSheet(messageMenu, messageMenuWrites(readOnly, session),
        onQuote = quoteTarget, onEdit = composer::edit,
        onContext = onContext,
        onForward = forwarding?.let { { target: MessageTarget -> forwardDraft = forwardDraftOf(target, key, session, machine, renderStyle, it) } },
        onNotice = onNotice, onFork = onFork, rewind = rewindMenu, onDelete = onDelete,
        share = shareCardSource(session, LocalPersonaName.current))
    val forwardFrom = forwardDraft
    if (forwardFrom != null && forwarding != null) ForwardSheet(forwardFrom, forwarding,
        complete = {
            val raw = forwardFrom.cut?.let { repository.fullText(key, it) } ?: forwardFrom.raw
            val restored = when {
                forwardFrom.photos.isEmpty() -> RestoredPhotos()
                photoReader == null -> RestoredPhotos(lost = forwardFrom.photos.size)
                else -> forwardedPhotos(context, forwardFrom.photos, photoReader::payload)
            }
            ForwardCompletion(ForwardMessage.ref(raw, forwardFrom.source, resources), restored)
        },
        onDismiss = { forwardDraft = null },
        onPicked = { target, completed ->
            deliverForward(target, completed.ref, key, forwarding, resources, completed.photos,
                onDuplicate = { handoff -> scope.launch { discardForwardedPhotos(context, handoff) } })
        })
}
