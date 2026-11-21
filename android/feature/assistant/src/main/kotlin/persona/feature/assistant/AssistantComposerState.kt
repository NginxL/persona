package persona.feature.assistant

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.remember
import androidx.compose.runtime.snapshotFlow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftRepository
import persona.feature.chat.ChatComposerState
import persona.feature.chat.ChatDraftEditor
import persona.feature.chat.rememberChatComposerState

/** The existing composer/send state, created with the same manager keys and lifecycle as the page had. */
internal class AssistantComposerState(
    val composer: ChatComposerState,
    val savedComposer: ChatComposerState?,
    val draftEditor: ChatDraftEditor?,
    val draftError: Int?,
    val draftReady: Boolean,
    val sendState: AssistantSendState,
    val failedStarts: FailedStarts,
    val firstPage: String,
)

@Composable
internal fun rememberAssistantComposerState(
    hubId: String?,
    shown: String,
    draftRepository: DraftRepository?,
    draftScope: CoroutineScope?,
    demo: Boolean,
    suppliedComposer: ChatComposerState?,
): AssistantComposerState {
    val transientComposer = key(hubId, shown) { rememberChatComposerState() }
    val draftEditors = remember(draftRepository, draftScope) { mutableMapOf<Pair<String?, String>, ChatDraftEditor>() }
    val draftEditor = if (draftRepository != null && draftScope != null) {
        draftEditors.getOrPut(hubId to shown) {
            ChatDraftEditor(draftRepository, draftScope, DraftKey.Assistant(shown.takeUnless {
                it == persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER
            }?.let { "main@$it" } ?: "main"))
        }
    } else null
    DisposableEffect(draftEditors) { onDispose { draftEditors.values.forEach { it.close() } } }
    val savedComposer by key(draftEditor) {
        (draftEditor?.composer ?: remember { MutableStateFlow<ChatComposerState?>(null) }).collectAsStateWithLifecycle()
    }
    val draftError by key(draftEditor) {
        (draftEditor?.error ?: remember { MutableStateFlow<Int?>(null) }).collectAsStateWithLifecycle()
    }
    val draftRead by key(draftEditor) {
        (draftEditor?.ready ?: remember { MutableStateFlow(true) }).collectAsStateWithLifecycle()
    }
    LaunchedEffect(draftEditor, savedComposer) {
        savedComposer?.let { state -> snapshotFlow { state.draft }.collect { draftEditor?.changed(it) } }
    }
    val sends = remember(draftRepository, draftScope) { mutableMapOf<Pair<String?, String>, AssistantSendState>() }
    val appContext = androidx.compose.ui.platform.LocalContext.current.applicationContext
    val failedStarts = remember(appContext) { FailedStarts(appContext) }
    val firstPage = "${hubId.orEmpty()}/$shown"
    val sendState = sends.getOrPut(hubId to shown) {
        AssistantSendState().apply { first.value = failedStarts.read(firstPage).takeUnless { demo } }
    }
    return AssistantComposerState(savedComposer ?: suppliedComposer ?: transientComposer, savedComposer, draftEditor, draftError,
        draftEditor == null || draftRead, sendState, failedStarts, firstPage)
}
