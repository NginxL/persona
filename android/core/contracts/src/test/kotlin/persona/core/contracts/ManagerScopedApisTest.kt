package persona.core.contracts

import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.api.AgentTriage
import persona.core.contracts.api.ChannelManagement
import persona.core.contracts.api.ConnectorsManagement
import persona.core.contracts.api.GitHubInboxOptions
import persona.core.contracts.api.FeishuBotSettings
import persona.core.contracts.api.HubSent
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.forManager
import java.lang.reflect.Proxy

/**
 * A settings page of the current assistant reads and writes that assistant's only (docs/assistant/managers.md "Clients"):
 * every call through a scoped API runs with its manager as the request manager, whichever method it is.
 */
class ManagerScopedApisTest {
    /** A stand-in for [type] that records, for each call, the manager its coroutine names, and answers with [answer]. */
    @Suppress("UNCHECKED_CAST")
    private fun <T> recorder(type: Class<T>, seen: MutableList<Pair<String, String?>>, answer: (String) -> Any? = { null }): T =
        Proxy.newProxyInstance(type.classLoader, arrayOf(type)) { _, method, args ->
            val continuation = args?.lastOrNull() as? kotlin.coroutines.Continuation<*>
            seen += method.name to continuation?.context?.get(RequestManager)?.id
            when (method.name) {
                "getAvailable" -> flowOf(true)
                "getConversations" -> flowOf(emptyList<Session>())
                else -> answer(method.name)
            }
        } as T

    @Test fun everyCallOfAScopedApiNamesItsManager() = runBlocking {
        val seen = mutableListOf<Pair<String, String?>>()
        // Outside, nothing is named.
        assertEquals(null, currentCoroutineContext()[RequestManager])
        recorder(AgentTriage::class.java, seen).forManager("m-2").let { runCatching { it.switches() } }
        recorder(ConnectorsManagement::class.java, seen).forManager("m-2").let { api ->
            val _ = runCatching { api.connectors() }
            val _ = runCatching { api.mailAccounts() }
            val _ = runCatching { api.larkStatus() }
            val _ = runCatching { api.deleteMailAccount("a") }
            val _ = runCatching { api.gitHubAuth() }
            val _ = runCatching { api.setGitHubAuth(null) }
            runCatching { api.setGitHubInboxOptions(GitHubInboxOptions()) }
        }
        recorder(ChannelManagement::class.java, seen).forManager("m-2").let { api -> val _ = runCatching { api.channels() }; runCatching { api.unbind("c") } }
        recorder(FeishuBotSettings::class.java, seen).forManager("m-2").let { runCatching { it.status() } }
        recorder(HubSent::class.java, seen).forManager("m-2").let { runCatching { it.page() } }
        assertEquals(listOf("switches", "connectors", "mailAccounts", "larkStatus", "deleteMailAccount", "gitHubAuth", "setGitHubAuth", "setGitHubInboxOptions", "channels", "unbind", "status", "page"),
            seen.map { it.first })
        assertEquals(List(seen.size) { "m-2" }, seen.map { it.second })
    }
}
