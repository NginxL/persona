package persona.feature.settings

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Test
import persona.core.contracts.api.AgentTriage
import persona.core.contracts.api.AppUpdates
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ChannelManagement
import persona.core.contracts.api.ConnectorsManagement
import persona.core.contracts.api.CurrentAssistant
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.FeishuBotSettings
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.HubSent
import persona.core.contracts.api.ManagerPins
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.api.ModelConfiguration
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.ProviderSetup
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.StandingAuthorizationsSource
import java.lang.reflect.Proxy

/**
 * Settings' first group names the current assistant on its requests only when it is not the default
 * (docs/assistant/managers.md "Clients"): the default's, one assistant's and an older hub's requests are the ones the hub
 * always got, with no manager named.
 */
class SettingsAssistantScopeTest {
    private val seen = mutableListOf<Pair<String, String?>>()

    /** A stand-in for [type] recording each call with the manager its coroutine names (a request manager, else a pin). */
    @Suppress("UNCHECKED_CAST")
    private fun <T> recorder(type: Class<T>, name: String): T =
        Proxy.newProxyInstance(type.classLoader, arrayOf(type)) { proxy, method, args ->
            val context = (args?.lastOrNull() as? kotlin.coroutines.Continuation<*>)?.context
            when (method.name) {
                "getAvailable" -> flowOf(true)
                "getConversations" -> flowOf(emptyList<persona.core.contracts.Session>())
                "getChanges" -> emptyFlow<persona.core.contracts.api.KnowledgeSelectionChange>()
                "getMachines" -> MutableStateFlow(emptyList<persona.core.contracts.Machine>())
                "getState" -> MutableStateFlow(persona.core.contracts.api.ConnectionState.Online)
                "getIsPaired" -> MutableStateFlow(true)
                "hashCode" -> System.identityHashCode(proxy)
                "equals" -> proxy === args?.get(0)
                "toString" -> name
                else -> {
                    seen += name to (context?.get(RequestManager)?.id ?: context?.get(ManagerPins)?.manager(listOf("extensions")))
                    throw UnsupportedOperationException(method.name)
                }
            }
        } as T

    /** A call this test does not make. */
    private fun record(name: String): Nothing = throw UnsupportedOperationException(name)

    /** Model providers and model configuration: their own `forManager` is the interface's, so these implement them. */
    private inner class Providers : ProviderSetup {
        override suspend fun presets() = record("providers")
        override suspend fun add(provider: persona.core.contracts.api.NewProvider) = record("providers")
        override suspend fun launchers(): List<persona.core.contracts.api.HubLauncher> {
            seen += "providers" to kotlin.coroutines.coroutineContext[RequestManager]?.id
            throw UnsupportedOperationException("launchers")
        }
    }

    private inner class Models : ModelConfiguration {
        override suspend fun catalog(): persona.core.contracts.api.ModelCatalog {
            seen += "models" to kotlin.coroutines.coroutineContext[RequestManager]?.id
            throw UnsupportedOperationException("catalog")
        }
        override suspend fun purpose(id: String) = record("models")
        override suspend fun save(purpose: persona.core.contracts.api.ModelPurpose, draft: persona.core.contracts.api.ModelConfigurationDraft) = record("models")
        override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?) = record("models")
    }

    private val services = SettingsServices(
        pairing = recorder(PairingService::class.java, "pairing"),
        connection = recorder(HubConnection::class.java, "connection"),
        sessions = recorder(SessionsRepository::class.java, "sessions"),
        updates = recorder(AppUpdates::class.java, "updates"),
        providers = Providers(),
        models = Models(),
        triage = recorder(AgentTriage::class.java, "triage"),
        knowledgeSelection = recorder(persona.core.contracts.api.KnowledgeSelection::class.java, "knowledgeSelection"),
        connectors = recorder(ConnectorsManagement::class.java, "connectors"),
        channels = recorder(ChannelManagement::class.java, "channels"),
        feishuBot = recorder(FeishuBotSettings::class.java, "bot"),
        sent = recorder(HubSent::class.java, "sent"),
        extensions = recorder(ExtensionManagement::class.java, "extensions"),
        standing = recorder(StandingAuthorizationsSource::class.java, "standing"),
    )

    private val two = ManagerRoster(listOf(HubManager("assistant", "助手", online = true, isDefault = true), HubManager("m-2", "工作", online = true)), "assistant")
    private fun current(roster: ManagerRoster?, shown: String?) =
        CurrentAssistant(roster, AssistantManagerChoice.Choice("hub", shown, roster?.defaultManagerId ?: ManagerRoster.ASSISTANT_MANAGER))

    /** Every first-group service called once through [scoped]: which manager each call named. */
    private fun calls(scoped: SettingsServices): List<Pair<String, String?>> = runBlocking {
        seen.clear()
        val _ = runCatching { scoped.providers!!.launchers() }
        val _ = runCatching { scoped.models!!.catalog() }
        val _ = runCatching { scoped.triage!!.switches() }
        val _ = runCatching { scoped.knowledgeSelection!!.selection() }
        val _ = runCatching { scoped.connectors!!.connectors() }
        val _ = runCatching { scoped.channels!!.channels() }
        val _ = runCatching { scoped.feishuBot!!.status() }
        val _ = runCatching { scoped.sent!!.page() }
        val _ = runCatching { scoped.extensions!!.extensions() }
        val _ = runCatching { scoped.standing!!.list() }
        seen.toList()
    }

    private val names = listOf("providers", "models", "triage", "knowledgeSelection", "connectors", "channels", "bot", "sent", "extensions", "standing")

    @Test fun anOlderHubTheDefaultAndOneAssistantNameNone() {
        val one = ManagerRoster(listOf(HubManager("assistant", "助手", online = true, isDefault = true)), "assistant")
        listOf(current(null, null), current(one, null), current(two, null), current(two, "assistant")).forEach { assistant ->
            assertSame(services, services.forAssistant(assistant))
            assertEquals(names.map { it to null }, calls(services.forAssistant(assistant)))
        }
    }

    @Test fun anotherAssistantIsNamedOnEveryRequestOfTheGroup() {
        assertEquals(names.map { it to "m-2" }, calls(services.forAssistant(current(two, "m-2"))))
    }
}
