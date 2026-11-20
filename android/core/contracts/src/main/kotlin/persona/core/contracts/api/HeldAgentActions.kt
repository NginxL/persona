package persona.core.contracts.api

import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.StateFlow
import persona.core.contracts.HeldAgentMessage

const val AgentMessageHoldFeature = "agent_message_hold"

/** Actions on the manager that owns this Agent's session. Retry checks uncertain delivery at the manager. */
interface HeldAgentActions {
    val features: StateFlow<Set<String>>
    fun canRetry(row: HeldAgentMessage): Flow<Boolean>
    suspend fun retry(row: HeldAgentMessage)
    suspend fun cancel(id: String)
}
