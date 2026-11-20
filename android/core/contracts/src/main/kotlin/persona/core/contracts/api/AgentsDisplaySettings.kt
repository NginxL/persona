package persona.core.contracts.api

import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class AgentsDisplaySettings(@SerialName("show_botmux") val showBotmux: Boolean, val configured: Boolean)

interface AgentDisplayApi {
    suspend fun read(): AgentsDisplaySettings
    /** The server returns the winning value even when another client configured it first. */
    suspend fun migrateHidden(): AgentsDisplaySettings
}

/** Stable pairing identity for the cache; namespace is only the row's identifier in the merged list. */
data class AgentsDisplayHub(val scope: String, val namespace: String?, val selected: Boolean,
    val hello: StateFlow<SocketHello?>, val api: AgentDisplayApi)

fun interface AgentsDisplayHubs {
    fun paired(): Flow<List<AgentsDisplayHub>>
}
