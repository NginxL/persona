package persona.android.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.flow.filterIsInstance
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.merge
import kotlinx.coroutines.flow.onStart
import persona.core.contracts.StarredMessagesEvent
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.STARRED_MESSAGES_FEATURE
import persona.core.contracts.api.StarredMessagesApi
import persona.core.hub.HttpStarredMessages
import persona.core.hub.OkHttpHubConnection
import persona.feature.chat.LocalStarredMessagesStore
import persona.feature.chat.StarredMessagesStore

@Composable
internal fun StarredMessagesHost(connection: OkHttpHubConnection?, content: @Composable () -> Unit) {
    StarredMessagesHost(
        connection = connection,
        hubId = connection?.http?.hubId,
        api = { manager -> connection?.let { HttpStarredMessages(it, manager) } },
        content = content,
    )
}

@Composable
internal fun StarredMessagesHost(
    connection: HubConnection?,
    hubId: String?,
    api: (String?) -> StarredMessagesApi?,
    content: @Composable () -> Unit,
) {
    val choice by AssistantManagerChoice.current.collectAsState()
    val hello = connection?.currentHello?.collectAsState()?.value?.hello
    val offered = hello != null && STARRED_MESSAGES_FEATURE in hello.features
    val store = remember(connection, hubId, choice, offered) {
        if (connection == null || hubId != choice.hubId) {
            null
        } else {
            val client = if (offered) api(choice.manager) else null
            StarredMessagesStore(api = client, offered = offered && client != null)
        }
    }
    LaunchedEffect(connection, store, offered) {
        if (connection == null || store == null || !offered) return@LaunchedEffect
        merge(
            connection.currentHello.filterNotNull().map { Unit },
            connection.events.filterIsInstance<StarredMessagesEvent>().filter { choice.owns(it.manager) }.map { Unit },
        ).onStart {
            if (connection.currentHello.value == null) emit(Unit)
        }.collectLatest { store.refresh() }
    }
    CompositionLocalProvider(LocalStarredMessagesStore provides store, content = content)
}
