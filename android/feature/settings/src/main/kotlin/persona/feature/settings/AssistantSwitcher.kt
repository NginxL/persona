package persona.feature.settings

import android.content.res.Resources
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.HubGroupInfo
import persona.core.contracts.api.HubGroups
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.HubManagers
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.InlineTone
import persona.core.designsystem.JAction
import persona.core.designsystem.JAssistantSwitcherHeader
import persona.core.designsystem.JAssistantSwitcherRow
import persona.core.designsystem.JDefaultManagerFace
import persona.core.designsystem.JInlineStatus
import persona.core.designsystem.JManagerAvatar
import persona.core.designsystem.JMenu
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.shownName
import java.time.Instant

// The sidebar's switcher (docs/assistant/managers.md "Clients"): the current assistant at the sidebar's head, and under
// it, when opened, the hub's assistants to switch between, New assistant and Manage assistants… (Settings › Assistants).

/** The switcher's line for an assistant: Online · its computer, Offline · last synced …, Installing · code expires in n minutes. */
internal fun switcherLine(res: Resources, manager: HubManager, now: Instant): String {
    val line = ManagerText.statusLine(res, manager, now)
    val host = manager.host?.trim()?.takeIf { it.isNotEmpty() && manager.online && !manager.isInstalling } ?: return line
    return res.getString(R.string.settings_switcher_line, line, host)
}

/**
 * The switcher in the sidebar. [managers] is the selected hub's roster API (null in demo mode); without it, or before
 * the roster is read, or on a hub without one, [fallback] is drawn instead. A single installed assistant keeps its
 * name, avatar and roster entry in both the sidebar and Settings' first group ([head]: its title and Switch).
 * [waiting] is what waits for the user at each assistant
 * ([persona.core.contracts.api.waiting]); [onManage] opens Settings › Assistants.
 */
@Composable
fun AssistantSwitcher(
    managers: HubManagers?,
    waiting: Map<String, Int>,
    hubOnline: Boolean,
    onManage: () -> Unit,
    modifier: Modifier = Modifier,
    /** The head's own padding in the sidebar; the roster under it lines up with the sidebar's rows. */
    headModifier: Modifier = Modifier,
    /** False draws the time once (the pictures and the tests). */
    ticking: Boolean = true,
    /** Multi-hub groups for unified hub + assistant switching in the sidebar (matching iOS #12594). */
    hubs: HubGroups? = null,
    trailing: (@Composable () -> Unit)? = null,
    fallback: @Composable () -> Unit = {},
    head: SwitcherHead? = null,
) {
    val reported = LocalManagerRoster.current
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val emptyGroups = remember { MutableStateFlow(emptyList<HubGroupInfo>()) }
    val groupList by (hubs?.groups ?: emptyGroups).collectAsState()
    val multiHubGroups = if (groupList.size > 1) groupList else emptyList()
    val pickHub: (HubGroupInfo) -> Unit = { group ->
        if (!group.selected && hubs != null) {
            scope.launch {
                try {
                    hubs.select(group.id)
                    feedback?.notice(res.getString(R.string.settings_switcher_switched, group.name))
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) {
                    feedback?.notice(res.getString(R.string.settings_switcher_hub_switch_failed))
                }
            }
        }
    }
    if (managers == null || reported == null || reported.installed.isEmpty()) {
        if (multiHubGroups.isNotEmpty() && hubs != null && head == null) {
            MultiHubOnlySwitcher(multiHubGroups, onPickHub = pickHub, modifier = modifier, headModifier = headModifier, trailing = trailing)
            return
        }
        fallback()
        return
    }
    // Without the hub nothing is known to be online, as the assistant page reads it.
    val roster = if (hubOnline) reported else reported.copy(managers = reported.managers.map { it.copy(online = false) })
    val choice by AssistantManagerChoice.current.collectAsState()
    val model = remember(managers) { ManagersModel(managers) }
    val state by model.state.collectAsState()
    var expanded by rememberSaveable { mutableStateOf(false) }
    var naming by remember { mutableStateOf(false) }
    val counting = ticking && (state.code != null || (expanded && roster.managers.any { it.isInstalling }))
    var now by remember { mutableStateOf(Instant.now()) }
    LaunchedEffect(counting) {
        now = Instant.now()
        while (counting) { delay(1000); now = Instant.now() }
    }
    AssistantSwitcherContent(roster, choice.shown, waiting, expanded, now, modifier, headModifier,
        failure = state.failure.takeIf { !naming && state.code == null },
        hubGroups = multiHubGroups,
        onToggle = { expanded = !expanded; if (expanded) model.clearFailure() },
        onDismiss = { expanded = false },
        onPickHub = { group -> expanded = false; pickHub(group) },
        onPick = { manager ->
            if (manager.isInstalling) scope.launch {
                // Its code is all there is of it: straight to the install commands (a new code, as on its page). The
                // menu stays until the code is there, to say so in it when asking fails.
                if (state.roster == null) model.load()
                if (model.newCode(manager.id)) expanded = false
            } else {
                expanded = false
                if (manager.id != choice.shown) {
                    AssistantManagerChoice.pick(manager.id)
                    feedback?.notice(res.getString(R.string.settings_switcher_switched, manager.shownName))
                }
            }
        },
        onNew = { model.clearFailure(); naming = true },
        onManage = { expanded = false; onManage() },
        trailing = trailing, head = head)
    if (naming) NameDialog(
        title = stringResource(R.string.settings_managers_new), confirm = stringResource(R.string.settings_managers_create), initial = "",
        busy = state.busy, failure = state.failure, onDismiss = { naming = false },
        onConfirm = { name -> scope.launch { if (model.create(name)) naming = false } },
    )
    ManagerCodeSheetHost(state, now, model)
}

/**
 * The switcher as drawn for [roster] with [shown] current at [now] (the pictures draw this with fixed data): the head,
 * and while [expanded] the roster in a menu under it ([JMenu]: as wide as the switcher, closed by a tap outside, scrolled
 * when it does not fit; [ManagerRoster.switcherRows]), then New assistant and Manage assistants….
 */
@Composable
internal fun AssistantSwitcherContent(
    roster: ManagerRoster, shown: String, waiting: Map<String, Int>, expanded: Boolean, now: Instant,
    modifier: Modifier = Modifier, headModifier: Modifier = Modifier, failure: ResText? = null,
    hubGroups: List<HubGroupInfo> = emptyList(),
    onToggle: () -> Unit, onDismiss: () -> Unit, onPickHub: (HubGroupInfo) -> Unit = {},
    onPick: (HubManager) -> Unit, onNew: () -> Unit, onManage: () -> Unit,
    trailing: (@Composable () -> Unit)? = null, head: SwitcherHead? = null,
) {
    val res = LocalResources.current
    val density = LocalDensity.current
    val default = roster.defaultManagerId
    val current = roster.manager(shown)?.takeIf { !it.isInstalling && !it.hidden } ?: roster.installed.first()
    val others = waiting.any { (id, count) -> count > 0 && id != current.id }
    // The installed ones, the default first, then the ones still installing, as the roster orders them; not the hub's
    // own assistant while it is kept out (`hidden`).
    val list = roster.switcherRows
    val listLabel = stringResource(R.string.settings_managers_title)
    var width by remember { mutableStateOf(Dp.Unspecified) }
    Box(modifier.fillMaxWidth().onSizeChanged { width = with(density) { it.width.toDp() } }) {
        if (head != null) head(current, current.id == default, expanded, onToggle)
        else JAssistantSwitcherHeader(current.shownName, switcherLine(res, current, now), expanded, onToggle, headModifier,
            // The default assistant shows the look's face (one identity with its main conversation, #6401);
            // any other keeps its first-letter monogram.
            avatar = {
                if (current.id == default) JDefaultManagerFace(current, presence = true, alert = others && !expanded)
                else JManagerAvatar(current, false, presence = true, alert = others && !expanded)
            }, trailing = trailing,
            othersWaiting = others && !expanded)
        JMenu(expanded, onDismiss, listOf(
            JAction(stringResource(R.string.settings_managers_new), PhIcons.Plus, onClick = onNew),
            JAction(stringResource(R.string.settings_switcher_manage), PhIcons.Gear, onClick = onManage),
        ), Modifier.width(width)) {
            Column(Modifier.fillMaxWidth().semantics { contentDescription = listLabel }) {
                HubSwitcherSection(groups = hubGroups, onPickHub = onPickHub)
                list.forEach { manager ->
                    JAssistantSwitcherRow(manager, manager.id == default, switcherLine(res, manager, now),
                        current = manager.id == current.id, badge = waiting[manager.id] ?: 0, onClick = { onPick(manager) },
                        defaultTag = stringResource(R.string.settings_managers_default).takeIf { manager.id == default && roster.isMulti })
                }
                failure?.let { JInlineStatus(it.text(), Modifier.padding(Tokens.Space.s), tone = InlineTone.Failed) }
            }
        }
    }
}

/** Another head for the switcher's roster: given the current assistant, whether it is the default, whether the roster is open, and what opens or closes it. */
typealias SwitcherHead = @Composable (current: HubManager, isDefault: Boolean, expanded: Boolean, toggle: () -> Unit) -> Unit
