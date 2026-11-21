package persona.feature.assistant

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.SessionKey
import persona.feature.chat.ComposerFocusAsk

/** The existing Ask Persona focus handoff, including its ten-second expiry. */
internal fun askAssistantMain(
    main: SessionKey,
    focusAsk: ComposerFocusAsk,
    scope: CoroutineScope,
    openConversation: (SessionKey) -> Unit,
) {
    openConversation(main)
    focusAsk.session = main
    scope.launch {
        delay(10_000)
        if (focusAsk.session == main) focusAsk.session = null
    }
}
