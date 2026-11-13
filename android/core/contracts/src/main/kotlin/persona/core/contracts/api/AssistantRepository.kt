package persona.core.contracts.api

import persona.core.contracts.DirectionTree
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * What a current leader's row shows of its direction (#4352, #6626), from `GET /api/v1/assistant/directions/tree`:
 * [running], anything under it at work (its row's one activity mark), and [needsYou], its tasks waiting on the user (its
 * 「等你 N」). No working count: the mark says it.
 */
@Serializable
data class LeaderCounts(
    val running: Boolean = false,
    @SerialName("needs_you") val needsYou: Int = 0,
)

data class AssistantSnapshot(
    val mainSession: SessionKey? = null, val conversations: List<Session> = emptyList(),
    /** Whose conversations these are (docs/assistant/managers.md step 4): the manager the page showed when read; null: the default. */
    val manager: String? = null,
    /** Stable owner identity, including when this manager becomes or ceases to be the default. */
    val managerId: String = manager ?: ManagerRoster.ASSISTANT_MANAGER,
    /**
     * The leader conversations (`assistant.leaders`, docs/assistant/unified-entry.md "Leader conversations"), in the hub's
     * order: the drawer's 常驻 group. Empty when there are none or from an older hub.
     */
    val leaders: List<String> = emptyList(),
    /**
     * Each direction's name by leader conversation id (`GET /api/v1/assistant/directions/tree`, else the leaders
     * answer's), shown instead of its title.
     */
    val directionNames: Map<String, String> = emptyMap(),
    /**
     * The hub's direction tree (`GET /api/v1/assistant/directions/tree`, #4352), read and parsed in one place
     * (RealAssistantRepository): each direction's open to-dos, their states and agents. Null from an older hub (or before
     * it is read): the drawer keeps its flat rows and no list groups by it.
     */
    val directionTree: DirectionTree? = null,
    /**
     * The built-in background supervisor's conversation id (`background_supervisor` of `GET /api/v1/assistant`, #4927,
     * docs/assistant/unified-entry.md "Background supervisor"): listed, never a direction, never archived or ended from
     * the app. Null until the hub has made it and from an older hub.
     */
    val backgroundSupervisor: String? = null,
) {
    /**
     * What each current leader's row shows (the tree's `running` and `counts.needs_you`, #4352, #6626), by conversation
     * id: its spinner while anything under it runs, 「等你 N」. The secretary group is not a leader; empty from an older hub.
     */
    val leaderCounts: Map<String, LeaderCounts>
        get() = directionTree?.leaders.orEmpty().associate { it.id to LeaderCounts(it.running, it.counts.needsYou) }
}

/** The assistant's spaces besides its own (docs/assistant/health.md): their names on the wire (`space`). */
object AssistantSpace {
    const val HEALTH = "health"
}

interface AssistantRepository {
    val assistant: StateFlow<AssistantSnapshot>
    suspend fun refresh()
}
