package persona.core.navigation

import persona.core.contracts.ProtocolCompatibility

import android.net.Uri

/** A request card outside any session (`personad ask` in a plain terminal): its machine and id, app-local only. */
data class RequestRef(val machineId: String, val requestId: String)

/**
 * Shared notification/deep-link contract; the app supplies the Navigator implementation.
 * [request]: the interaction the notification is about, shown first in its conversation's request panel.
 * [card]: a request with no conversation of its own (#4808); the link opens Agents at its list with that card open.
 */
data class NotificationRoute(val tab: AppTab?, val chat: ChatRoute? = null, val request: String? = null, val card: RequestRef? = null) {
    fun uri(): Uri = Uri.Builder().scheme("persona").authority("open").apply {
        tab?.let { appendQueryParameter("tab", if (it == AppTab.Persona) "persona" else "agents") }
        chat?.let {
            appendQueryParameter("machine", it.machineId)
            appendQueryParameter("session", it.sessionId)
            request?.let { id -> appendQueryParameter("request", id) }
        } ?: card?.let {
            appendQueryParameter("machine", it.machineId)
            appendQueryParameter("request", it.requestId)
        }
    }.build()

    companion object {
        fun conversation(machineId: String, sessionId: String, assistant: Boolean?, request: String? = null) = NotificationRoute(
            when (assistant) { true -> AppTab.Persona; false -> AppTab.Agents; null -> null }, ChatRoute(machineId, sessionId),
            request?.takeIf { it.isNotBlank() },
        )

        /** A card outside any session opens Agents with that card; without an id, just Agents. */
        fun card(machineId: String, requestId: String?) = NotificationRoute(AppTab.Agents,
            card = requestId?.takeIf { it.isNotBlank() && machineId.isNotBlank() }?.let { RequestRef(machineId, it) })

        fun parse(uri: Uri): NotificationRoute? {
            // Notification targets have no path; a canonical page's tab= is view state, not a notification tab.
            if (uri.scheme !in ProtocolCompatibility.appSchemes || uri.host != "open" || !uri.path.isNullOrEmpty()) return null
            val tab = when (ProtocolCompatibility.canonicalTab(uri.getQueryParameter("tab"))) {
                "persona" -> AppTab.Persona
                "agents" -> AppTab.Agents
                null -> null
                else -> return null
            }
            val machine = uri.getQueryParameter("machine")
            val session = uri.getQueryParameter("session")
            val request = uri.getQueryParameter("request")?.takeIf { it.isNotBlank() }
            if (machine == null && session == null) return NotificationRoute(tab)
            if (session == null && tab == AppTab.Agents && !machine.isNullOrBlank() && request != null)
                return NotificationRoute(tab, card = RequestRef(machine, request))
            if (machine.isNullOrBlank() || session.isNullOrBlank()) return null
            return NotificationRoute(tab, ChatRoute(machine, session), request)
        }
    }
}
