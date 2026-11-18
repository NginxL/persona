package persona.core.domain.ports

import java.net.URI

/**
 * A link to a port on the machine a conversation runs on (docs/operations/ports.md "Recognizing links in replies", iOS `PortLinks`): what an
 * agent prints for the dev server it started (http://localhost:5173/, http://127.0.0.1:8000/docs, http://0.0.0.0:3000,
 * http://[::1]:4173, or the machine's own name), opened through the hub, never on the phone's own localhost.
 * [path] is the path, query and fragment ("/" at least).
 */
data class PortLink(val port: Int, val path: String, val secure: Boolean)

/** A recognized link and its source range, for settled/tail partitioning. */
data class PortLinkMatch(val start: Int, val end: Int, val link: PortLink, val web: Boolean)

object PortLinks {
    /** Hosts that always mean "this machine" (java.net.URI keeps IPv6 literals in brackets). */
    private val loopback = setOf("localhost", "127.0.0.1", "0.0.0.0", "::1", "::", "[::1]", "[::]")

    /** Bare `localhost:3000/x` (no scheme), as servers often print it. */
    private val bare = Regex("""(?<![A-Za-z0-9_./:@\-])(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1]):(\d{1,5})(/[A-Za-z0-9_.~!$&'()*+,;=:@%/?#\-]*)?""")
    private val web = Regex("""https?://[\x21-\x7e]+""", RegexOption.IGNORE_CASE)

    /**
     * The port link [url] is, if it names this machine (`localhost`, a loopback address, a `*.localhost`, or one of
     * [hostnames], the machine's name with or without .local) with an explicit port.
     */
    fun link(url: String, hostnames: List<String> = emptyList()): PortLink? {
        val uri = runCatching { URI(url.trim()) }.getOrNull() ?: return null
        val scheme = uri.scheme?.lowercase()
        if (scheme != "http" && scheme != "https") return null
        val host = uri.host?.lowercase() ?: return null
        val port = uri.port
        if (port !in 1..65535) return null
        if (host !in loopback && !host.endsWith(".localhost") && host !in names(hostnames)) return null
        val path = buildString {
            append(uri.rawPath?.takeIf(String::isNotEmpty) ?: "/")
            uri.rawQuery?.let { append('?').append(it) }
            uri.rawFragment?.let { append('#').append(it) }
        }
        return PortLink(port, if (path.startsWith("/")) path else "/$path", scheme == "https")
    }

    /** The port links in [text], in order, one per port and path. */
    fun find(text: String, hostnames: List<String> = emptyList()): List<PortLink> =
        findMatches(text, hostnames).map(PortLinkMatch::link)

    /** The same links as [find], with source positions for settled/tail partitioning. */
    fun findMatches(text: String, hostnames: List<String> = emptyList()): List<PortLinkMatch> {
        if (':' !in text) return emptyList()
        val out = LinkedHashMap<PortLink, PortLinkMatch>()
        val covered = mutableListOf<IntRange>()
        for (m in web.findAll(text)) {
            covered += m.range
            link(trimTail(m.value), hostnames)?.let { out.putIfAbsent(it, PortLinkMatch(m.range.first, m.range.last + 1, it, web = true)) }
        }
        for (m in bare.findAll(text)) {
            if (covered.any { it.first <= m.range.last && m.range.first <= it.last }) continue
            link("http://" + trimTail(m.value))?.let { out.putIfAbsent(it, PortLinkMatch(m.range.first, m.range.last + 1, it, web = false)) }
        }
        return out.values.toList()
    }

    private fun trimTail(value: String): String = value.trimEnd('.', ',', ';', ':', '!', '?', ')', '>', '"', '\'')

    private fun names(hostnames: List<String>): Set<String> = buildSet {
        for (h in hostnames) {
            if (h.isBlank()) continue
            val n = h.lowercase()
            add(n)
            if (n.endsWith(".local")) add(n.removeSuffix(".local")) else if ('.' !in n) add("$n.local")
        }
    }
}
