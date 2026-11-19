package persona.android.di

import android.app.Application
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent
import persona.android.PersonaApp
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.hub.HttpAgentArchives
import persona.core.hub.HttpAgentOperations
import persona.core.hub.HttpAgentCreationService
import persona.core.hub.HttpHubPreferencesTransport
import persona.core.hub.HubError
import persona.core.hub.HubChatTransport
import persona.core.data.PersistentAgentOperations
import persona.feature.agents.*
import persona.platform.notify.Notifier
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import javax.inject.Singleton

@Module
@InstallIn(SingletonComponent::class)
object AgentsWiringModule {
    @Provides @Singleton
    fun displayHubs(stacks: GroupStacks): AgentsDisplayHubs = AgentsDisplayHubs {
        combine(stacks.stacks, stacks.selected) { list, selected ->
            list.map { stack ->
                AgentsDisplayHub(stack.data.scope.path, stack.namespace, stack === selected,
                    stack.connection.currentHello, HttpHubPreferencesTransport(stack.connection) { stack.current })
            }
        }
    }

    @Provides
    @Singleton
    @OptIn(ExperimentalCoroutinesApi::class)
    fun wiring(
        application: Application,
        stacks: GroupStacks,
        assistant: AssistantConversations,
        notifier: Notifier,
    ): AgentsWiring = object : AgentsWiring {
        private val perGroup = ConcurrentHashMap<GroupStack, StateFlow<AgentsWiringState>>()
        private fun of(stack: GroupStack) = perGroup.getOrPut(stack) { groupAgentsState(stack, assistant, notifier) }
        override val state = stacks.stacks.flatMapLatest { list ->
            perGroup.keys.retainAll(list.toSet())
            when (list.size) {
                0 -> flowOf(AgentsWiringState())
                1 -> of(list.single())
                else -> combine(stacks.selected, combine(list.map(::of)) { it.toList() }) { selected, states ->
                    mergedAgentsState(list.map { it.names }, states, list.indexOf(selected).coerceAtLeast(0))
                }
            }
        }.stateIn((application as PersonaApp).scope, SharingStarted.Eagerly, AgentsWiringState())
    }
}

/** One group's Agents operations (what this module built for the only pairing), in merged ids. */
private fun groupAgentsState(stack: GroupStack, assistant: AssistantConversations, notifier: Notifier): StateFlow<AgentsWiringState> {
    val data = stack.data
    val connection = stack.connection
    val names = stack.names
    fun current() = stack.current && data.coroutineScope.coroutineContext[Job]?.isActive == true
    fun api(): AgentOperations {
        if (!current()) throw CancellationException("Pairing changed")
        val route = connection.route.value ?: throw persona.core.hub.HubFailure(HubError.NoNetwork)
        return HttpAgentOperations(data.scope.path, route.endpoint.url, connection.token,
            connection.http.client, isCurrent = { current() && connection.route.value == route })
    }
    suspend fun publish(pins: AgentOperationPreferences? = null, mutes: AgentOperationPreferences? = null) {
        if (!current()) throw CancellationException("Pairing changed")
        try {
            data.coroutineScope.async {
                fun AgentOperationPreferences.shared() = HubPreferenceSet(spaces, sessions, allowed)
                stack.repositories.preferences?.acceptAgentPreferences(pins?.shared(), mutes?.shared())
            }.await()
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* A local cache failure must not turn a confirmed write into a retry. */ }
    }
    val operations = object : AgentOperations {
        override val scope = data.scope.path
        override suspend fun pins() = api().pins()
        override suspend fun mutes() = api().mutes()
        override suspend fun savePins(value: AgentOperationPreferences) { api().savePins(value); publish(pins = value) }
        override suspend fun saveMutes(value: AgentOperationPreferences) { api().saveMutes(value); publish(mutes = value) }
        override suspend fun renameSession(key: SessionKey, title: String?, tabName: String?) = api().renameSession(key, title, tabName)
        override suspend fun renameSpace(machineId: String, workspaceId: String, name: String) {
            val target = api()
            target.renameSpace(machineId, workspaceId, name)
            try { publish(target.pins(), target.mutes()) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { /* The rename has already been acknowledged. */ }
        }
        override suspend fun closeSession(key: SessionKey) = api().closeSession(key)
        override suspend fun attachSession(key: SessionKey) = api().attachSession(key)
        override suspend fun deleteSession(key: SessionKey) = api().deleteSession(key)
    }
    // Notifications are posted with merged ids; the plain key a closed session reports is put back into them.
    val bindings = AgentsOperationsBindings(PersistentAgentOperations(operations, data.operations, HubChatTransport(connection)), assistant) { key ->
        if (current()) notifier.retractSession(names.key(key))
    }
    val drafts = AgentsDraftSource { key ->
        data.drafts.observe(DraftKey.Chat(key)).map { AgentDraft(it.text, it.attachments.isNotEmpty()) }
    }
    val archives = object : AgentArchives {
        override val supported get() = AgentArchives.FEATURE in connection.serverFeatures.value
        private fun archiveApi(): AgentArchives {
            if (!current()) throw CancellationException("Pairing changed")
            val route = connection.route.value ?: throw persona.core.hub.HubFailure(HubError.NoNetwork)
            return HttpAgentArchives(route.endpoint.url, connection.token, connection.http.client,
                available = { supported }, isCurrent = { current() && connection.route.value == route })
        }
        override suspend fun list() = archiveApi().list()
        override suspend fun archive(key: SessionKey) = archiveApi().archive(key)
        override suspend fun restore(key: SessionKey) = archiveApi().restore(key)
    }
    fun build(route: persona.core.hub.HubRoute?) = AgentsWiringState(data.scope.path, bindings,
        creation = route?.let {
            HttpAgentCreationService(data.scope.path, it.endpoint.url, connection.token,
                connection.http.client, isCurrent = { current() && connection.route.value == route })
        }, drafts = drafts, mainSender = data.messages, creationPreferences = data.listPreferences, archives = archives)
        .inGroup(names, data.coroutineScope)
    return combine(connection.route, connection.serverFeatures) { route, _ -> build(route) }.stateIn(data.coroutineScope, SharingStarted.Eagerly, build(connection.route.value))
}
