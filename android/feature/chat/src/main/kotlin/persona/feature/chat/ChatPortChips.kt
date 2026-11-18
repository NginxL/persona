package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.rememberScrollState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import persona.core.contracts.ListeningPort
import persona.core.domain.ports.PortLink
import persona.core.domain.ports.PortLinks
import persona.core.designsystem.PathChip
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons

/** A port's chip text: `:5173` and the page's title when its `/` gave one (iOS `PortBadgeRow`). */
internal fun portChipLabel(port: ListeningPort): String =
    ":${port.port}" + (port.title?.trim()?.takeIf(String::isNotEmpty)?.let { " $it" } ?: "")

/** The session's ports, one each, in the order the hub lists them. */
internal fun chipPorts(ports: List<ListeningPort>?): List<ListeningPort> = ports.orEmpty().distinctBy { it.port }

/**
 * The ports an agent listens on (`Session.ports`, docs/operations/ports.md), under the conversation's header (iOS
 * `SessionPortChips`): a tap opens the page through the hub, by way of the link host, as a link to it would.
 * Nothing when the session lists none or the conversation cannot open ports.
 */
@Composable
internal fun ChatPortChips(ports: List<ListeningPort>?) {
    val shown = chipPorts(ports)
    if (shown.isEmpty() || LocalChatPorts.current == null) return
    val links = LocalUriHandler.current
    val openLabel = stringResource(R.string.chat_port_open)
    LazyRow(
        Modifier.fillMaxWidth().padding(vertical = Tokens.Space.xs),
        contentPadding = PaddingValues(horizontal = Tokens.Space.l), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        items(shown, key = { it.port }) { port ->
            val description = port.title?.let { stringResource(R.string.chat_port_desc_titled, port.port, it) }
                ?: stringResource(R.string.chat_port_desc, port.port)
            PathChip(portChipLabel(port), PhIcons.GlobeSimple, onClick = { links.openUri("http://localhost:${port.port}/") },
                modifier = Modifier.semantics { contentDescription = description },
                onClickLabel = openLabel)
        }
    }
}

/** A link's badge text: `:5173`, with the path when it has one (`:8000/docs`, cut past 24), and what is known of it. */
internal fun portBadgeLabel(link: PortLink, state: PortPresence.State?, stopped: String): String {
    val path = link.path.takeIf { it != "/" }.orEmpty().let { if (it.length > 24) it.take(23) + "…" else it }
    val detail = when (state) {
        is PortPresence.State.Live -> state.title?.takeIf(String::isNotBlank)
        PortPresence.State.Gone -> stopped
        null -> null
    }
    return ":${link.port}$path" + (detail?.let { " $it" } ?: "")
}

/**
 * Badges for a message's links to ports of its machine (docs/operations/ports.md, iOS `PortChips`): under an agent's reply and
 * under a Bash command's output (a dev server saying where it listens). Each is the port, its path and its page's
 * title, gray once nothing listens there; a tap opens it through the hub as the link would.
 */
@Composable
internal fun PortBadges(text: String, found: List<PortLink>? = null) {
    val ports = LocalChatPorts.current ?: return
    val links = found ?: remember(text, ports.hostnames) { PortLinks.find(text, ports.hostnames) }
    if (links.isEmpty()) return
    val presence = ports.presence
    if (presence != null) LaunchedEffect(presence, links, ports.listed) { presence.ask(links.map { it.port }.distinct(), ports.listed) }
    val open = LocalUriHandler.current
    val openLabel = stringResource(R.string.chat_port_open)
    val stopped = stringResource(R.string.chat_port_stopped)
    Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.xs).horizontalScroll(rememberScrollState()),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        links.forEach { link ->
            val state = presence?.state(link.port, ports.listed) ?: ports.listed[link.port]?.let { PortPresence.State.Live(it) }
            val gone = state == PortPresence.State.Gone
            val description = stringResource(if (gone) R.string.chat_port_desc_gone else R.string.chat_port_desc, link.port)
            PathChip(portBadgeLabel(link, state, stopped), PhIcons.GlobeSimple,
                onClick = { open.openUri("${if (link.secure) "https" else "http"}://localhost:${link.port}${link.path}") },
                modifier = Modifier.alpha(if (gone) GONE_ALPHA else 1f).semantics {
                    contentDescription = description
                },
                onClickLabel = openLabel)
        }
    }
}

/** A port nothing listens on any more: its badge stays, dimmed (iOS `LinkBadge(dimmed:)`). */
private const val GONE_ALPHA = 0.45f
