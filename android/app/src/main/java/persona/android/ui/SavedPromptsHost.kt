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
import persona.core.contracts.SavedPromptsEvent
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.SavedPromptsApi
import persona.core.hub.HttpSavedPrompts
import persona.core.hub.OkHttpHubConnection
import persona.feature.assistant.settings.LocalSavedPromptsStore
import persona.feature.assistant.settings.SavedPromptsStore
import persona.feature.chat.LocalSavedPrompts

@Composable
internal fun SavedPromptsHost(connection: OkHttpHubConnection?, content: @Composable () -> Unit) {
    SavedPromptsHost(connection, connection?.http?.hubId, { manager -> connection?.let { HttpSavedPrompts(it, manager) } }, content)
}

@Composable
internal fun SavedPromptsHost(
    connection: HubConnection?, hubId: String?, api: (String?) -> SavedPromptsApi?, content: @Composable () -> Unit,
) {
    val choice by AssistantManagerChoice.current.collectAsState()
    val store = remember(connection, hubId, choice) {
        if (connection == null || hubId != choice.hubId) null else api(choice.manager)?.let(::SavedPromptsStore)
    }
    LaunchedEffect(connection, store) {
        if (connection == null || store == null) return@LaunchedEffect
        merge(
            connection.currentHello.filterNotNull().map { Unit },
            connection.events.filterIsInstance<SavedPromptsEvent>().filter { choice.owns(it.manager) }.map { Unit },
        ).onStart {
            if (connection.currentHello.value == null) emit(Unit)
        }.collectLatest { store.refresh() }
    }
    val prompts = store?.state?.collectAsState()?.value?.prompts.orEmpty()
    CompositionLocalProvider(LocalSavedPromptsStore provides store, LocalSavedPrompts provides prompts, content = content)
}
