package persona.android.debug

import androidx.compose.runtime.*
import persona.android.navigation.DeveloperTools
import persona.android.ui.ShellSession
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.inbox.InboxSnapshot
import kotlinx.coroutines.flow.MutableStateFlow

/** Fixed local snapshots keep captures independent of network timing and user pairing data. */
class DebugStatePreview : DeveloperTools by DeveloperPages {
    var state by mutableStateOf<String?>(null)

    @Composable
    override fun session(session: ShellSession): ShellSession {
        val state = state
        return remember(session, state) {
            if (state == null || session.demo == null) return@remember session
            val connection = object : HubConnection by session.connection {
                override val state = MutableStateFlow(when (state) {
                    "offline" -> ConnectionState.WaitingForNetwork
                    "connecting" -> ConnectionState.Connecting
                    "updating" -> ConnectionState.Updating
                    "pairing-expired" -> ConnectionState.Revoked
                    "certificate-mismatch" -> ConnectionState.CertificateMismatch
                    "local-permission" -> ConnectionState.NeedsLocalNetworkPermission
                    else -> ConnectionState.Online
                })
                override fun reconnect() = Unit
                override fun send(message: kotlinx.serialization.json.JsonObject) = false
            }
            val empty = state in setOf("empty", "loading", "load-failed")
            val sessions = object : SessionsRepository by session.sessions {
                override val sessions = if (empty) MutableStateFlow<List<Session>>(emptyList()) else session.sessions.sessions
                override val machines = if (empty) MutableStateFlow<List<Machine>>(emptyList()) else session.sessions.machines
                override suspend fun refresh() = Unit
            }
            val interactions = object : InteractionCenter by session.interactions {
                override val pending = if (empty) MutableStateFlow<List<Interaction>>(emptyList()) else session.interactions.pending
                override suspend fun refresh() = Unit
            }
            val inbox = object : persona.feature.inbox.InboxRepository by session.inbox {
                override val connectionState = connection.state
                override val snapshot = MutableStateFlow(if (empty) InboxSnapshot(
                    loading = state == "loading", error = if (state == "load-failed") "加载失败，请重试" else null,  // i18n-ignore: debug preview fixture data
                ) else session.inbox.snapshot.value)
                override suspend fun refresh() = Unit
                override suspend fun refreshNow() = Unit
            }
            val assistant = object : AssistantConversations by session.assistant {
                override val assistant = if (empty) MutableStateFlow(AssistantSnapshot()) else session.assistant.assistant
                override suspend fun refresh() = Unit
            }
            val chats = object : ChatRepository by session.chats {
                override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(
                    loading = state == "loading", error = if (state == "load-failed") "加载失败，请重试" else null,  // i18n-ignore: debug preview fixture data
                    items = emptyList(), verified = state !in setOf("loading", "load-failed"),
                ))
            }
            ShellSession("${session.identity}/preview-$state", connection, sessions,
                if (empty) chats else session.chats, interactions, session.sender, assistant, inbox,
                session.preferences, demo = session.demo)
        }
    }

    companion object {
        val states = setOf("offline", "connecting", "updating", "pairing-expired", "empty", "load-failed",
            "loading", "certificate-mismatch", "local-permission")
    }
}
