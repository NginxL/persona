package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import java.net.URI
import java.net.URLDecoder
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.api.KnowledgeKind

/** A page of the hub's web app that Android can show itself (docs/protocol/deep-links.md). */
sealed interface HubPage {
    /** `/s/<machine>/<session>`, or the older `/t/<machine>/<space>/<slot>/<session>`. */
    data class Agent(
        val machineId: String,
        val sessionId: String,
        val itemId: String? = null,
        val view: String? = null,
    ) : HubPage

    /** `/persona/<conversation>`; null is Persona's own page. */
    data class Conversation(
        val sessionId: String?,
        val itemId: String? = null,
        val machineId: String? = null,
        val managerId: String? = null,
    ) : HubPage

    /** `/persona/new`: a blank conversation, distinct from the main conversation. */
    data object NewConversation : HubPage

    /** `/kb` or `/kb/<kind>`. */
    data class Knowledge(val kind: String? = null) : HubPage

    /** `/kb/<kind>/<id>`; an escaped slash in the last path segment is part of [id]. */
    data class KnowledgeEntry(val kind: String, val id: String) : HubPage

    /** A destination whose route key is shared with the rest of the app. */
    data class Screen(val key: NavKey) : HubPage
}

object HubLinks {
    data class Hub(val id: String, val addresses: List<String>)

    sealed interface Result {
        data class Page(val page: HubPage, val hub: String? = null) : Result
        data class Rejected(val reason: Reason) : Result
    }

    enum class Reason { External, NotAPage, Unsafe }

    /**
     * Reads a paired hub's web link, a canonical app link, or an older app link. A web page carries the id of the hub
     * whose address matched. An app page carries an explicit `hub=` only when that hub is paired; without one it opens
     * on the current hub. No unknown hub falls back to the current one.
     */
    fun parse(url: String, hubs: Collection<Hub>): Result {
        val value = url.trim()
        val scheme = value.substringBefore(':', "").lowercase()
        val link = uri(value)
        if (link == null) return rejected(if (scheme in ProtocolCompatibility.appSchemes) Reason.Unsafe else Reason.External)
        when (link.scheme?.lowercase()) {
            "http", "https" -> return webPage(link, hubs)
            in ProtocolCompatibility.appSchemes -> Unit
            else -> return rejected(Reason.External)
        }
        if (link.rawUserInfo != null || link.port >= 0) return rejected(Reason.Unsafe)
        val query = query(link.rawQuery) ?: return rejected(Reason.Unsafe)
        val parts = segments(link.rawPath) ?: return rejected(Reason.Unsafe)
        if (parts.any { it.unsafeSegment() }) return rejected(Reason.Unsafe)
        val requestedHub = query.value("hub")
        val page = when (link.host?.lowercase()) {
            "open" -> openPage(link, parts, query) ?: return openFailure(link, parts, query)
            "session" -> if (parts.size == 2) agent(parts[0], parts[1], query) else null
            "task" -> if (parts.size == 1) HubPage.Screen(TodoDetailRoute(parts[0])) else null
            "kb" -> if (parts.size >= 2 && knowledgeKind(parts[0]) != null)
                HubPage.KnowledgeEntry(parts[0], parts.drop(1).joinToString("/")) else null
            "settings" -> when (parts) {
                listOf("sent") -> HubPage.Screen(SettingsPageRoute(SettingsPage.Sent))
                else -> if (parts.size == 2 && parts[0] == "extensions")
                    HubPage.Screen(ExtensionRoute(parts[1])) else null
            }
            "extension" -> if (parts.size == 2) extension(parts[0], parts[1], query) else null
            else -> null
        } ?: return rejected(Reason.NotAPage)
        if (requestedHub != null && hubs.none { it.id == requestedHub }) return rejected(Reason.External)
        return Result.Page(page, requestedHub)
    }

    /**
     * Compatibility wrapper for callers that only need a page, not its rejection reason or hub identity. It retains
     * the old wrapper's treatment of `view` as a display hint; new callers use [parse] to retain that state.
     */
    fun page(url: String, hubs: Collection<String>): HubPage? {
        val profiles = hubs.map { Hub("", listOf(it)) }
        return (parse(url, profiles) as? Result.Page)?.page?.let { page ->
            if (page is HubPage.Agent) page.copy(view = null) else page
        }
    }

    private fun webPage(link: URI, hubs: Collection<Hub>): Result {
        if (link.rawUserInfo != null) return rejected(Reason.Unsafe)
        val host = link.host?.lowercase()?.trim('[', ']')?.takeIf { it.isNotEmpty() } ?: return rejected(Reason.External)
        val path = link.rawPath.orEmpty()
        val match = hubs.flatMap { hub -> hub.addresses.mapNotNull { address -> base(address)?.let { hub to it } } }
            .filter { (_, base) -> base.host == host && base.port == port(link) }
            .filter { (_, base) -> path == base.path || path.startsWith("${base.path}/") }
            .maxByOrNull { (_, base) -> base.path.length } ?: return rejected(Reason.External)
        val (hub, base) = match
        val parts = segments(path.removePrefix(base.path)) ?: return rejected(Reason.NotAPage)
        val query = query(link.rawQuery) ?: return rejected(Reason.NotAPage)
        val page = route(parts, query) ?: return rejected(Reason.NotAPage)
        return Result.Page(page, hub.id)
    }

    /** The three forms behind `persona://open`: a path, the desktop's route=, and the Live Activity's typed ids. */
    private fun openPage(link: URI, parts: List<String>, query: Query): HubPage? {
        if (link.rawPath.orEmpty().startsWith("//") || parts.any { it == "." || it == ".." }) return null
        if (parts.isNotEmpty()) return route(parts, query)
        if (!link.rawPath.isNullOrEmpty()) return null
        query.value("route")?.let { return routePage(it) }
        val machine = query.value("machine")
        val session = query.value("session")
        if (machine != null && session != null) return HubPage.Agent(machine, session)
        return null
    }

    /** Distinguish a malformed open link (unsafe) from a well-formed link to a path outside the route table. */
    private fun openFailure(link: URI, parts: List<String>, query: Query): Result {
        val unsafe = link.rawPath.orEmpty().startsWith("//") || parts.any { it == "." || it == ".." } ||
            parts.isEmpty() && link.rawPath.isNullOrEmpty() && query.value("route")?.let { !it.safeRoute() } == true
        return rejected(if (unsafe) Reason.Unsafe else Reason.NotAPage)
    }

    private fun routePage(route: String): HubPage? {
        if (!route.safeRoute()) return null
        val link = uri(route) ?: return null
        if (link.scheme != null || link.rawAuthority != null) return null
        val parts = segments(link.rawPath) ?: return null
        if (parts.any { it == "." || it == ".." || it.unsafeSegment() }) return null
        val query = query(link.rawQuery) ?: return null
        return route(parts, query)
    }

    private fun route(p: List<String>, query: Query): HubPage? {
        if (p.any { it.isBlank() }) return null
        return when (ProtocolCompatibility.canonicalAssistantPath(p.firstOrNull())) {
            null -> HubPage.Conversation(null)
            "s" -> if (p.size == 3) agent(p[1], p[2], query) else null
            "t" -> when (p.size) {
                5 -> agent(p[1], p[4], query)
                4 -> HubPage.Screen(AgentsRoute) // the older place link retained by the original Android wrapper
                else -> null
            }
            "persona" -> when {
                p.size == 1 -> HubPage.Conversation(null)
                p.size == 2 && p[1] == "new" -> HubPage.NewConversation
                p.size == 2 -> HubPage.Conversation(p[1], at(query), query.value("machine"), query.value("manager"))
                else -> null
            }
            "agents", "a" -> if (p.size == 1) HubPage.Screen(AgentsRoute) else null
            "inbox" -> when (p.size) {
                1 -> HubPage.Screen(InboxRoute)
                2 -> HubPage.Screen(InboxItemRoute(p[1], manager = query.value("manager")))
                else -> null
            }
            "todos" -> when {
                p.size == 2 -> HubPage.Screen(TodoDetailRoute(p[1]))
                p.size == 1 -> query.value("id")?.let { HubPage.Screen(TodoDetailRoute(it)) } ?: HubPage.Screen(TodosRoute)
                else -> null
            }
            "kb" -> when (p.size) {
                1 -> HubPage.Knowledge()
                2 -> knowledgeKind(p[1])?.let { HubPage.Knowledge(it.wire) }
                3 -> knowledgeKind(p[1])?.let { HubPage.KnowledgeEntry(it.wire, p[2]) }
                else -> null
            }
            "machines" -> when (p.size) {
                1 -> HubPage.Screen(MachinesRoute)
                2 -> HubPage.Screen(MachineDetailRoute(p[1], range = query.value("range")))
                else -> null
            }
            "settings" -> when {
                p == listOf("settings", "sent") -> HubPage.Screen(SettingsPageRoute(SettingsPage.Sent))
                p.size == 1 -> settings(query)
                else -> null
            }
            "extension", "plugin" -> if (p.size == 3) extension(p[1], p[2], query) else null
            else -> null
        }
    }

    private fun agent(machine: String, session: String, query: Query) = HubPage.Agent(
        machine, session, at(query), query.value("view")?.takeIf { it == "chat" || it == "term" },
    )

    private fun extension(extension: String, ui: String, query: Query) =
        HubPage.Screen(ExtensionUiRoute(extension, ui, "", state = query.value("state")))

    private fun settings(query: Query): HubPage {
        val manager = query.value("manager")
        return when (query.value("tab")) {
            "sent" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Sent))
            "models" -> HubPage.Screen(query.value("purpose")?.let { ModelPurposeRoute(it, manager) }
                ?: SettingsPageRoute(SettingsPage.ModelConfig))
            "extensions" -> HubPage.Screen(query.value("extension")?.let { ExtensionRoute(it, manager = manager) }
                ?: SettingsPageRoute(SettingsPage.Extensions))
            "connectors" -> when (query.value("connector")) {
                "-sent" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Sent))
                null -> HubPage.Screen(SettingsPageRoute(SettingsPage.Connectors))
                else -> HubPage.Screen(ConnectorRoute(query.value("connector")!!, manager))
            }
            "hub" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Hubs))
            "persona" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Persona))
            "notifications" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Notifications))
            "agents" -> HubPage.Screen(SettingsPageRoute(SettingsPage.AgentSettings))
            "channels" -> HubPage.Screen(query.value("channel")?.let { ChannelRoute(it) }
                ?: SettingsPageRoute(SettingsPage.Channels))
            "devices" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Devices))
            "managers" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Managers))
            "knowledge" -> HubPage.Screen(SettingsPageRoute(SettingsPage.KnowledgeSelection))
            else -> HubPage.Screen(SettingsRoute)
        }
    }

    /** The message a conversation's link names; an empty anchor is no anchor. */
    private fun at(query: Query): String? = query.value("at")

    private fun knowledgeKind(value: String): KnowledgeKind? = KnowledgeKind.fromWire(value)

    private data class Base(val host: String, val port: Int, val path: String)

    /** A hub address as host, port and path prefix; an address without a scheme (`host:port`) is http. */
    private fun base(address: String): Base? {
        val value = address.trim().let { if ("://" in it) it else "http://$it" }
        val u = uri(value) ?: return null
        val host = u.host?.lowercase()?.trim('[', ']')?.takeIf { it.isNotEmpty() } ?: return null
        if (u.scheme?.lowercase() !in setOf("http", "https") || u.rawUserInfo != null) return null
        return Base(host, port(u), u.rawPath.orEmpty().trimEnd('/'))
    }

    private fun port(u: URI): Int = if (u.port >= 0) u.port else if (u.scheme.equals("https", ignoreCase = true)) 443 else 80

    private fun uri(value: String): URI? = try { URI(value) } catch (_: java.net.URISyntaxException) { null }

    /** Query values use form decoding (`+` is a space); path segments do not. */
    private fun query(raw: String?): Query? = try {
        val values = linkedMapOf<String, String>()
        for (part in raw.orEmpty().split('&').filter { it.isNotEmpty() }) {
            val name = decode(part.substringBefore('='))
            if (name !in values) values[name] = decode(part.substringAfter('=', ""))
        }
        Query(values)
    } catch (_: IllegalArgumentException) { null }

    private data class Query(private val values: Map<String, String>) {
        fun value(name: String): String? = values[name]?.takeIf { it.isNotEmpty() }
    }

    private fun segments(path: String?): List<String>? = try {
        path.orEmpty().split('/').filter { it.isNotEmpty() }.map { URLDecoder.decode(it.replace("+", "%2B"), "UTF-8") }
    } catch (_: IllegalArgumentException) { null }

    private fun decode(value: String): String = URLDecoder.decode(value, "UTF-8")

    private fun String.unsafeSegment(): Boolean = any { it == '\\' || it.isISOControl() }

    private fun String.safeRoute(): Boolean = startsWith("/") && !startsWith("//") &&
        none { it == '\\' || it.isISOControl() }

    private fun rejected(reason: Reason) = Result.Rejected(reason)
}
