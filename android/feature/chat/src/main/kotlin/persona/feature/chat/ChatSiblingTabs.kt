package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.PaneOrder
import persona.core.domain.sort.launcherOf
import persona.core.domain.sort.slotLabel
import persona.core.domain.sort.tabKey
import java.time.Instant

/**
 * The tabs of this chat's space, itself included (iOS `AppModel.siblings`): the live agents in the same herdr
 * workspace on the same machine, one per pane (after /clear or a resume a pane is briefly listed under two session
 * ids: the open one wins, then the latest), in tab order. Empty when the session is not in a space.
 */
internal fun spaceTabs(session: Session?, sessions: List<Session>): List<Session> {
    if (session == null) return emptyList()
    val space = session.spaceId?.takeIf(String::isNotBlank) ?: return emptyList()
    val listed = (sessions + session).mapTo(HashSet()) { it.key }
    val byPane = LinkedHashMap<String, Session>()
    val rest = mutableListOf<Session>()
    for (other in sessions + session) {
        if (other.machineId != session.machineId || other.assistant || other.agentState == AgentState.Offline) continue
        if (other.spaceId?.takeIf(String::isNotBlank) != space) continue
        if (other.key != session.key &&
            other.foldedUnder?.takeIf(String::isNotBlank)?.let { SessionKey(other.machineId, it) in listed } == true) continue
        val pane = other.paneId?.takeIf(String::isNotBlank)
        if (pane == null) { if (rest.none { it.key == other.key }) rest += other; continue }
        val had = byPane[pane]
        if (had != null && (had.key == session.key ||
                (other.key != session.key && (had.updated ?: Instant.EPOCH) >= (other.updated ?: Instant.EPOCH)))) continue
        byPane[pane] = other
    }
    // the tabs in order, a tab's agents by their place in it (#2320: the launcher first)
    return (byPane.values + rest).sortedWith(compareBy<Session> { it.slotIndex ?: Int.MAX_VALUE }.thenBy { it.slotId.orEmpty() }.then(PaneOrder))
}

/**
 * What a chip says (#2320): a tab of one agent by its name; the agents sharing a tab add who they are, and the tab's
 * launcher (the pane the others were split from) wears a short mark on its chip (as the web's pane strip: "Launcher"), so
 * that "#2" three times reads as one tab of three agents.
 */
internal data class SiblingTab(val session: Session, val name: String, val launcher: Boolean)

internal fun siblingTabs(tabs: List<Session>): List<SiblingTab> {
    val byTab = tabs.groupBy { it.tabKey ?: "s:${it.key.path}" }
    return tabs.map { tab ->
        val panes = byTab.getValue(tab.tabKey ?: "s:${tab.key.path}")
        if (panes.size == 1) return@map SiblingTab(tab, tabLabel(tab), false)
        val launcher = launcherOf(panes) { it }?.key == tab.key
        // the tab by its number, not its name: herdr often names a tab after a long task title, which said once per
        // chip filled the row (seen on a phone); two agents of one kind are told apart by their place, as on iOS
        val number = tab.slotIndex?.let { "#$it" } ?: tab.slotLabel
        val kind = panes.filter { it.agentLabel == tab.agentLabel }
        val agent = tab.agentLabel.ifBlank { null }
            ?.let { if (kind.size > 1) "$it ${kind.indexOfFirst { other -> other.key == tab.key } + 1}" else it }
        val name = listOfNotNull(number, agent).joinToString(" ").ifBlank { tab.displayTitle }
        SiblingTab(tab, name, launcher)
    }
}

/** A tab's name (iOS `Session.slotLabel`): its herdr tab name, "#n" for herdr's default number, else the title. */
internal fun tabLabel(session: Session): String = session.slotLabel ?: session.displayTitle

internal fun Session.status(): Status = Status.entries.firstOrNull { it.name == agentState.name } ?: Status.Unknown

/**
 * Under the chat's title (iOS `SiblingTabs`): the space's tabs as chips with their state's dot, the current one
 * selected; it opens scrolled to the current one. ⋯ lists them all with their state. The caller provides the tabs
 * of the current space and shows the strip only when there is a tab to switch to.
 */
@Composable
internal fun ChatSiblingTabs(
    session: Session, tabs: List<Session>, onOpenSession: (SessionKey) -> Unit,
    /** At the row's end, after ⋯: the loop and background pill on Glass (iOS `contextRow`, #4339 C8). */
    trailing: (@Composable () -> Unit)? = null,
) {
    val labels = remember(tabs) { siblingTabs(tabs) }
    var all by remember { mutableStateOf(false) }
    val at = tabs.indexOfFirst { it.key == session.key }
    val list = rememberLazyListState(initialFirstVisibleItemIndex = (at - 1).coerceAtLeast(0))
    var anchoredSession by remember { mutableStateOf(session.key) }
    LaunchedEffect(session.key) {
        if (anchoredSession != session.key) {
            anchoredSession = session.key
            if (at >= 0) list.animateScrollToItem((at - 1).coerceAtLeast(0))
        }
    }
    val waiting = tabs.any { it.key != session.key && it.agentState == AgentState.NeedsYou }
    Row(
        Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        LazyRow(Modifier.weight(1f), state = list, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            items(labels, key = { it.session.key.path }) { label ->
                val tab = label.session
                val on = tab.key == session.key
                val description = if (label.launcher) stringResource(R.string.chat_tabs_launcher_description, label.name, tab.status().localizedLabel)
                    else stringResource(R.string.chat_tabs_tab_description, label.name, tab.status().localizedLabel)
                JChip(if (label.launcher) stringResource(R.string.chat_tabs_launcher_chip, label.name) else label.name,
                    Modifier.semantics { contentDescription = description },
                    selected = on, status = tab.status(), onClick = { if (!on) onOpenSession(tab.key) })
            }
        }
        JIconButton(PhIcons.DotsThree, pluralStringResource(if (waiting) R.plurals.chat_tabs_all_waiting else R.plurals.chat_tabs_all, tabs.size, tabs.size),
            { all = true }, badge = waiting)
        trailing?.invoke()
    }
    if (all) JSheet(onDismiss = { all = false }, title = pluralStringResource(R.plurals.chat_tabs_sheet_title, tabs.size, tabs.size), scrollable = true) {
        labels.forEach { label ->
            val tab = label.session
            val on = tab.key == session.key
            JRow(label.name, subtitle = if (label.launcher) stringResource(R.string.chat_tabs_launcher_subtitle, tab.status().localizedLabel) else tab.status().localizedLabel,
                leading = { JStatusDot(tab.status()) }, selected = on, onClick = { all = false; if (!on) onOpenSession(tab.key) })
        }
        Spacer(Modifier.height(Tokens.Space.l))
    }
}
