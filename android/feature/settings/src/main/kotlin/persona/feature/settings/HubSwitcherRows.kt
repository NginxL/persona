package persona.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubGroupInfo
import persona.core.designsystem.JAssistantSwitcherHeader
import persona.core.designsystem.JDivider
import persona.core.designsystem.JIcon
import persona.core.designsystem.JMenu
import persona.core.designsystem.JRow
import persona.core.designsystem.JStatusDot
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

internal fun hubGroupStatus(state: ConnectionState): Status = when (state) {
    ConnectionState.Online, ConnectionState.Updating -> Status.Unread
    ConnectionState.Connecting -> Status.Working
    ConnectionState.WaitingForNetwork,
    ConnectionState.NeedsLocalNetworkPermission,
    ConnectionState.CertificateMismatch,
    ConnectionState.Revoked -> Status.Offline
}

/**
 * The hub section at the top of the sidebar switcher menu when multiple hub groups are paired (matching iOS #12594):
 * one row per group with its hub icon, live link dot, name and current check, then a divider and "Assistants on <hub>".
 */
@Composable
internal fun HubSwitcherSection(
    groups: List<HubGroupInfo>,
    onPickHub: (HubGroupInfo) -> Unit,
    showAssistantsHeading: Boolean = true,
) {
    if (groups.size <= 1) return
    val currentHub = groups.firstOrNull { it.selected }
    SwitcherSectionHeading(stringResource(R.string.settings_group_hub))
    groups.forEach { group ->
        HubSwitcherRow(group = group, onClick = { onPickHub(group) })
    }
    if (showAssistantsHeading) {
        JDivider(Modifier.padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs))
        currentHub?.let { hub ->
            SwitcherSectionHeading(stringResource(R.string.settings_switcher_assistants_on, hub.name))
        }
    }
}

@Composable
internal fun SwitcherSectionHeading(text: String) {
    Text(
        text = text,
        style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
        color = PersonaTheme.palette.ink2,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.xs)
            .semantics { heading() },
    )
}

@Composable
internal fun HubSwitcherRow(
    group: HubGroupInfo,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val spoken = if (group.selected) {
        stringResource(R.string.settings_switcher_hub_current, group.name)
    } else {
        group.name
    }
    JRow(
        modifier = Modifier
            .testTag("assistant-switcher-hub-${group.id}")
            .semantics {
                selected = group.selected
                contentDescription = spoken
            },
        onClick = onClick,
        chevron = false,
        selected = group.selected,
        trailing = if (group.selected) ({
            JIcon(
                PersonaIcons.done,
                null,
                size = Tokens.Size.iconXS,
                tint = palette.accent,
                decorative = true,
            )
        }) else null,
    ) {
        HubAvatar(group.state)
        Spacer(Modifier.width(Tokens.Space.m))
        Text(
            text = group.name,
            style = PersonaTheme.type.body,
            color = palette.ink,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f),
        )
    }
}

@Composable
private fun HubAvatar(state: ConnectionState) {
    val palette = PersonaTheme.palette
    val avatarSize = Tokens.Space.Standard.xxl
    Box(Modifier.size(avatarSize), contentAlignment = Alignment.Center) {
        Box(
            Modifier
                .size(avatarSize)
                .clip(CircleShape)
                .background(palette.sunken),
            contentAlignment = Alignment.Center,
        ) {
            JIcon(
                PersonaIcons.server,
                null,
                size = Tokens.Size.iconXS,
                tint = palette.ink,
                decorative = true,
            )
        }
        Box(
            Modifier
                .align(Alignment.BottomEnd)
                .clip(CircleShape)
                .background(palette.surfaceOpaque)
                .padding(Tokens.Size.line),
        ) {
            JStatusDot(hubGroupStatus(state), size = Tokens.Size.dotS, breathes = false)
        }
    }
}

/**
 * Fallback sidebar hub switcher when multiple hub groups exist on a hub without a manager roster (matching iOS #12601).
 */
@Composable
internal fun MultiHubOnlySwitcher(
    groups: List<HubGroupInfo>,
    onPickHub: (HubGroupInfo) -> Unit,
    modifier: Modifier = Modifier,
    headModifier: Modifier = Modifier,
    trailing: (@Composable () -> Unit)? = null,
) {
    val current = groups.firstOrNull { it.selected } ?: groups.firstOrNull() ?: return
    val density = LocalDensity.current
    var expanded by remember { mutableStateOf(false) }
    var width by remember { mutableStateOf(Dp.Unspecified) }
    val statusText = when (current.state) {
        ConnectionState.Online, ConnectionState.Updating -> stringResource(R.string.settings_managers_online)
        ConnectionState.Connecting -> stringResource(R.string.settings_switcher_hub_connecting)
        ConnectionState.WaitingForNetwork,
        ConnectionState.NeedsLocalNetworkPermission,
        ConnectionState.CertificateMismatch,
        ConnectionState.Revoked -> stringResource(R.string.settings_managers_offline)
    }
    Box(modifier.fillMaxWidth().onSizeChanged { width = with(density) { it.width.toDp() } }) {
        JAssistantSwitcherHeader(
            name = current.name,
            status = statusText,
            expanded = expanded,
            onClick = { expanded = !expanded },
            modifier = headModifier,
            avatar = { HubAvatar(current.state) },
            trailing = trailing,
        )
        JMenu(expanded, { expanded = false }, emptyList(), Modifier.width(width)) {
            Column(Modifier.fillMaxWidth()) {
                HubSwitcherSection(
                    groups = groups,
                    onPickHub = { group ->
                        expanded = false
                        onPickHub(group)
                    },
                    showAssistantsHeading = false,
                )
            }
        }
    }
}
