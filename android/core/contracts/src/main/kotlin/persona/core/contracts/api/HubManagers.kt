package persona.core.contracts.api

import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Settings › Managers (docs/assistant/managers.md, docs/protocol/protocol.md"Managers"): the selected hub's roster of managers, each a whole
 * assistant, and the one-time install codes that put a new one (or one moved elsewhere) on a computer. The hub answers
 * these itself (`/api/v1/managers`). Bound to the active pairing's selected hub.
 */
interface HubManagers {
    /** The hub's `managers` pushes (sent only to an app that asks for the `managers` feature): read the list again. */
    val changes: Flow<Unit> get() = emptyFlow()

    /** `GET /api/v1/managers`; a hub from before the roster is a [ManagersUnsupported]. */
    suspend fun roster(): ManagerRoster

    /** `POST /api/v1/managers {name}`: a new manager, still installing, and its install code. */
    suspend fun create(name: String): ManagerCode

    /** `POST /api/v1/managers/{id}/code`: a new install code (moving it elsewhere, or one whose code ran out). */
    suspend fun newCode(id: String): ManagerCode

    /** `PATCH /api/v1/managers/{id} {name}`. */
    suspend fun rename(id: String, name: String): HubManager

    /** `PATCH /api/v1/managers/{id} {default: true}`. */
    suspend fun makeDefault(id: String): HubManager

    /**
     * `PATCH /api/v1/managers/{id} {hide}`: keep the hub's own assistant out of the switcher while it is not used, or
     * show it there again. Only the built-in manager takes it; hiding is refused (409 `builtin_in_use`) while it is the
     * default or owns a computer.
     */
    suspend fun setHide(id: String, hide: Boolean)

    /** `DELETE /api/v1/managers/{id}`: its token stops working at once (an installing one's code is void). */
    suspend fun delete(id: String)
}

/** A manager name the hub accepts: 1 to 40 characters once trimmed. */
const val MANAGER_NAME_MAX = 40
fun validManagerName(name: String): Boolean = name.trim().let {
    it.isNotEmpty() && it.codePointCount(0, it.length) <= MANAGER_NAME_MAX
}

/** The hub has no `/api/v1/managers` (404): it predates the roster. */
class ManagersUnsupported : Exception("This hub has no managers")

/** GET /api/v1/managers: the default first, the installing ones last. */
@Serializable
data class ManagerRoster(
    val managers: List<HubManager> = emptyList(),
    @SerialName("default") val defaultId: String = "",
) {
    /** The installed managers (an installing one is not there yet), the default first, then in the roster's order. */
    val installed: List<HubManager> get() = managers.filterNot { it.isInstalling }
        .sortedBy { if (it.id == defaultManagerId) 0 else 1 }

    /** More than one installed manager: another one's computers, conversations and offline state are told apart. */
    val isMulti: Boolean get() = installed.size > 1

    /**
     * The switcher's rows: the installed managers (the default first), then the ones still installing, leaving out a
     * [HubManager.hidden] one (the hub's own assistant, kept out while it is not used). [installed] and [isMulti] still
     * count it: what is its keeps its place, and the badges and the Default tag read as before.
     */
    val switcherRows: List<HubManager> get() = (installed + managers.filter { it.isInstalling }).filterNot { it.hidden }

    /** The default manager's id: the roster's `default`, else the one marked default, else the hub's own assistant. */
    val defaultManagerId: String get() = defaultId.ifBlank { managers.firstOrNull { it.isDefault }?.id ?: ASSISTANT_MANAGER }

    fun manager(id: String?): HubManager? = managers.firstOrNull { it.id == id }

    /**
     * The installed manager a thing stamped [manager] is shown under: its own, or the default's for no stamp and for
     * one the roster does not list (deleted, still installing), so nothing is left on no assistant's pages.
     */
    fun shownUnder(manager: String?): String =
        manager?.takeIf { id -> id.isNotBlank() && managers.any { it.id == id && !it.isInstalling } } ?: defaultManagerId

    /** Which manager an item stamped with [manager] belongs to: no stamp is the default manager's. */
    fun ownerOf(manager: String?): String = manager?.takeIf { it.isNotBlank() } ?: defaultManagerId

    companion object {
        /** The hub's own assistant's id: what an unstamped live update comes from. */
        const val ASSISTANT_MANAGER = "assistant"
    }
}

/**
 * The current assistant as the pages ask about it (docs/assistant/managers.md "Clients"): every page lists what is its, by the
 * `manager` its things carry. [roster] is the selected hub's (null until read, or from a hub without managers: the
 * choice's own default decides then), [group] the group that hub is of ([persona.core.contracts.GroupKeys]).
 */
data class CurrentAssistant(
    val roster: ManagerRoster? = null,
    val choice: AssistantManagerChoice.Choice = AssistantManagerChoice.Choice(),
    val group: String? = null,
) {
    /** The current assistant's id. */
    val id: String get() = choice.shown

    /** It as the roster has it; null before the roster is read. */
    val manager: HubManager? get() = roster?.manager(id)

    /** Whether a thing stamped [manager] (an inbox item, a to-do; no stamp: the default's) is the current assistant's. */
    fun owns(manager: String?): Boolean = roster?.let { it.shownUnder(manager) == id } ?: choice.owns(manager)

    /**
     * The same for a thing on the computer [machineId] (a session, a card, the computer itself). Another group's
     * computers are not this hub's managers' at all: they are on every assistant's pages, as before.
     */
    fun ownsOn(machineId: String, manager: String?): Boolean =
        persona.core.contracts.GroupKeys.namespaceOf(machineId) != group || owns(manager)

    /** What waits for the user at the other assistants ([ManagerRoster.waiting]'s tally): the dot on the sidebar's button. */
    fun othersWaiting(waiting: Map<String, Int>): Boolean = waiting.any { (manager, count) -> count > 0 && manager != id }
}

/**
 * The current assistant: the manager every page shows (docs/assistant/managers.md "Clients"), picked in the sidebar's
 * switcher and kept per hub on this device. Null is the default manager, and requests go as before; an id puts
 * `X-Persona-Manager` on the requests that are the current assistant's (`/api/v1/assistant…`, the knowledge base, a new
 * to-do) to [hubId]'s hub, and the pages list that manager's conversations, agents, computers, inbox and to-dos. Live
 * updates that name a conversation (feed items, cards, plans) go by it, whichever manager's it is, so another
 * manager's conversation open elsewhere stays live. Process-wide like [persona.core.contracts.ClientLanguage]: the
 * switcher sets it, the hub client, the repositories and the pages read it.
 */
object AssistantManagerChoice {
    /** [manager] null: the default; [defaultId] is the roster's default (whose conversations the page lists then). */
    data class Choice(val hubId: String? = null, val manager: String? = null, val defaultId: String = ManagerRoster.ASSISTANT_MANAGER) {
        /** The manager the page shows. */
        val shown: String get() = manager ?: defaultId.ifBlank { ManagerRoster.ASSISTANT_MANAGER }

        /** Whether an item stamped [stamp] (absent: the default manager's) is the shown manager's. */
        fun owns(stamp: String?): Boolean = (stamp?.takeIf { it.isNotBlank() } ?: defaultId.ifBlank { ManagerRoster.ASSISTANT_MANAGER }) == shown
    }

    private val state = MutableStateFlow(Choice())
    val current: StateFlow<Choice> = state.asStateFlow()

    /** Shows [manager] of [hubId]'s hub (null or the default: the default, with no header). */
    fun choose(hubId: String?, manager: String?, defaultId: String) {
        val id = manager?.takeIf { it.isNotBlank() && it != defaultId }
        state.value = Choice(hubId, id, defaultId.ifBlank { ManagerRoster.ASSISTANT_MANAGER })
    }

    /** The switcher picked [manager] (null or the default: the default) of the hub it is on now. */
    fun pick(manager: String?) = state.value.let { choose(it.hubId, manager, it.defaultId) }

    /** Back to the default (another hub, unpaired, a test's end). */
    fun reset() { state.value = Choice() }

    /**
     * Now on [hubId]'s hub, its roster not read yet: the default, under that hub, so what its lists note before the
     * roster lands is kept under it (and found when its requests ask).
     */
    fun atHub(hubId: String?) { state.value = Choice(hubId) }

    // The things a merged list showed with their manager (an inbox item) and the conversations each manager's page
    // listed, by hub and path under /api/v1 (`inbox/<id>`, `assistant/conversations/<id>`): requests for one name its
    // manager, as the hub asks ("When a list item carries manager, include it in requests for that item"). Every stamp
    // is kept; whether it needs a header is decided when asked, against the default then (the roster may move it). The
    // web keeps the same registry (manager-choice.ts).
    private val items = java.util.concurrent.ConcurrentHashMap<Pair<String?, List<String>>, String>()

    /** A merged list of [hubId]'s hub (the one shown now) showed the thing at [path] (segments under /api/v1) as [manager]'s. */
    fun noteItem(path: List<String>, manager: String?, hubId: String? = state.value.hubId) {
        if (path.isEmpty() || path.any { it.isEmpty() }) return
        val id = manager?.takeIf { it.isNotBlank() }
        if (id == null) items.remove(hubId to path) else items[hubId to path] = id
    }

    /**
     * The manager a request to [path] (segments under /api/v1) of [hubId]'s hub is for, from a list that showed it (or
     * the thing it is under); null: send none (none known, or the default). A page's own pins are [ManagerPins].
     */
    fun itemHeader(path: List<String>, hubId: String? = state.value.hubId): String? =
        itemManager(path, hubId)?.takeIf { it != state.value.defaultId }

    /** The recorded owner, including the default; delayed card actions must not follow the current selection. */
    fun itemManager(path: List<String>, hubId: String? = state.value.hubId): String? {
        for (n in path.size downTo 1) {
            items[hubId to path.subList(0, n)]?.let { return it }
        }
        return null
    }

    /** Another hub: what the last one's lists showed is not this one's. */
    fun forgetItems() = items.clear()

    /** The `X-Persona-Manager` value for an assistant request to the hub [hubId]; null: send none. */
    fun header(hubId: String?): String? = state.value.takeIf { it.hubId == hubId }?.manager
}

/** One manager as the roster has it; times are RFC 3339. */
@Serializable
data class HubManager(
    val id: String,
    val name: String = "",
    val online: Boolean = false,
    @SerialName("default") val isDefault: Boolean = false,
    /** The assistant the hub starts beside itself: it cannot be deleted or moved. */
    val builtin: Boolean = false,
    val host: String? = null,
    val os: String? = null,
    val arch: String? = null,
    /** How it runs: child (the hub's own), service / systemd / launchd, docker / container, external, or other words. */
    val deploy: String? = null,
    val version: String? = null,
    /** Connected since (online only). */
    val since: String? = null,
    /** Last connected (offline: Last synced at). */
    @SerialName("last_seen") val lastSeen: String? = null,
    /** Made in the app, its install code not traded yet: when that code runs out. */
    val installing: String? = null,
    /** Online on a build that reads no settings of its own (no `config.own`): it runs on built-in defaults until updated. */
    @SerialName("needs_update") val needsUpdate: Boolean = false,
    /**
     * Its inbox items still to do in "Needs you" (the inbox's badge); null when the hub cannot tell (offline, still
     * installing, an older hub), which counts as none.
     */
    @SerialName("inbox_needs_you") val inboxNeedsYou: Int? = null,
    /**
     * The user's choice to keep it out of the switcher while it is not used (the hub's own assistant only): what
     * Settings' Show in the switcher shows, turned around. Absent (false) from an older hub.
     */
    val hide: Boolean = false,
    /** That choice in effect now (the hub's word: [hide], not the default, owns no computer): the switcher leaves it out. */
    val hidden: Boolean = false,
) {
    val isInstalling: Boolean get() = !installing.isNullOrBlank()
}

/**
 * What waits for the user at each manager, for the switcher's badges (docs/assistant/managers.md "Clients", the same
 * rule on every client): its agents waiting for the user, each session once however many cards it has, plus its inbox's
 * "Needs you" as the roster counts it ([HubManager.inboxNeedsYou]; absent: none). An agent waits when it has a pending
 * card ([requests]) or is in Needs you; [sessionCounts] leaves out the sessions the tabs leave out too (quiet, muted),
 * and [counts] the cards. A card on no listed session counts once by itself. A card or session without a stamp is
 * the default manager's; a card goes by its session's manager when it names none.
 */
fun ManagerRoster.waiting(
    requests: List<persona.core.contracts.Interaction>,
    sessions: List<persona.core.contracts.Session>,
    /** The group this roster's hub is of: another group's cards and sessions are no manager's of this one. */
    group: String? = null,
    counts: (persona.core.contracts.Interaction) -> Boolean = { true },
    sessionCounts: (persona.core.contracts.Session) -> Boolean = { true },
): Map<String, Int> {
    fun here(machineId: String) = persona.core.contracts.GroupKeys.namespaceOf(machineId) == group
    val bySession = sessions.associateBy { it.key }
    val tally = HashMap<String, Int>()
    // Each waiting agent once, under its manager: (manager, session).
    val agents = HashSet<Pair<String, persona.core.contracts.SessionKey>>()
    requests.distinctBy { it.id to it.sessionKey }.filter { here(it.machineId) }.forEach { card ->
        val session = bySession[card.sessionKey]
        when {
            session == null -> if (counts(card)) tally.merge(shownUnder(card.manager), 1, Int::plus)
            sessionCounts(session) && counts(card) -> agents += shownUnder(card.manager ?: session.manager) to session.key
        }
    }
    // A session in Needs you with no card here (stuck on the computer's own prompt); one with a card is its cards'.
    val asked = requests.mapTo(HashSet()) { it.sessionKey }
    sessions.forEach { session ->
        if (session.agentState == persona.core.contracts.AgentState.NeedsYou && session.key !in asked && here(session.machineId) &&
            sessionCounts(session)) agents += shownUnder(session.manager) to session.key
    }
    agents.forEach { (manager, _) -> tally.merge(manager, 1, Int::plus) }
    managers.forEach { manager -> manager.inboxNeedsYou?.takeIf { it > 0 }?.let { tally.merge(manager.id, it, Int::plus) } }
    return tally
}

/** A one-time install code (10 minutes, used once) and, from a hub that gives them, the commands that use it. */
@Serializable
data class ManagerCode(
    val code: String = "",
    @SerialName("expires_at") val expiresAt: String = "",
    /** POST /api/v1/managers also answers the new manager. */
    val manager: HubManager? = null,
    val commands: ManagerCommands? = null,
)

/** Ready-made install commands with the code in them; either may be missing. */
@Serializable
data class ManagerCommands(val service: String? = null, val docker: String? = null)

/**
 * The manager the hub requests made inside it are for (an inbox item's, docs/assistant/managers.md "Clients"): the connection
 * names it on each request (`?manager=`) where the request is made, so one still going out after the page moved on
 * keeps it, and two things of the same id under two managers each go to their own. [id] "" is the default's: no manager
 * named, and no stamp a list noted for the path either.
 */
class RequestManager(val id: String) : kotlin.coroutines.AbstractCoroutineContextElement(Key) {
    companion object Key : kotlin.coroutines.CoroutineContext.Key<RequestManager>
}

/**
 * A page's pins (an extension's page, an inbox item's chat, mail or thread): the requests made inside it to a path under
 * one of its prefixes (segments under /api/v1) of [hubId]'s hub name its manager, as [RequestManager] does; the longest
 * prefix wins, and a [RequestManager] wins over it. It goes only with the page's own requests (the APIs it hands its
 * children carry it), so another page, a background refresh or the page under it on the stack is not pinned.
 */
class ManagerPins private constructor(private val prefixes: Map<List<String>, String>, val hubId: String?) :
    kotlin.coroutines.AbstractCoroutineContextElement(Key) {

    /** The manager pinned for [path], the default's id included; null: not pinned. */
    private fun owner(path: List<String>): String? = (path.size downTo 1).firstNotNullOfOrNull { prefixes[path.subList(0, it)] }

    /** What a request to [path] of [hubId]'s hub names: "" for the default (none, and no stamp); null: not pinned. */
    fun manager(path: List<String>, hubId: String? = AssistantManagerChoice.current.value.hubId): String? {
        if (hubId != this.hubId) return null
        val owner = owner(path) ?: return null
        return owner.takeIf { it != AssistantManagerChoice.current.value.defaultId } ?: ""
    }

    override fun equals(other: Any?) = other is ManagerPins && other.prefixes == prefixes && other.hubId == hubId
    override fun hashCode() = prefixes.hashCode() * 31 + hubId.hashCode()

    companion object Key : kotlin.coroutines.CoroutineContext.Key<ManagerPins> {
        /** Requests under each of [prefixes] name [manager]; null (nothing pinned) for no manager. */
        fun of(prefixes: List<List<String>>, manager: String?, hubId: String? = AssistantManagerChoice.current.value.hubId): ManagerPins? {
            val id = manager?.takeIf { it.isNotBlank() } ?: return null
            return ManagerPins(prefixes.filter { it.isNotEmpty() }.associateWith { id }, hubId)
        }
    }
}
