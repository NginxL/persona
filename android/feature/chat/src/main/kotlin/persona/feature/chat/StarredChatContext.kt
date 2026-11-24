package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.StarredConversation

/** The open chat's starring identity, kept in its original recomposition scope. */
@Composable
internal fun rememberStarredChat(key: SessionKey, renderStyle: ChatRenderStyle, session: Session?, personaName: String): StarredChatContext =
    remember(key, renderStyle, session, personaName) {
        StarredChatContext(
            key = key,
            conversation = if (renderStyle == ChatRenderStyle.Assistant) {
                StarredConversation.Assistant
            } else {
                StarredConversation.Agent
            },
            title = session?.displayTitle.orEmpty(),
            speaker = if (renderStyle == ChatRenderStyle.Assistant) personaName else session?.displayTitle.orEmpty(),
            manager = session?.manager,
        )
    }
