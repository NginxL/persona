package persona.feature.chat

import android.content.res.Resources
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import persona.core.contracts.Session
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.sort.READ_ONLY
import persona.core.domain.sort.canAttach
import persona.core.domain.sort.canContinueCodex
import persona.core.domain.sort.canTakeover
import persona.core.domain.sort.source

/** Why a request in this session cannot be answered from the phone: a Codex thread not joined yet, or a read-only source. */
internal fun approvalBlockedReason(res: Resources, session: Session?): String =
    res.getString(if (session?.canAttach == true) R.string.chat_source_attach_to_answer else R.string.chat_source_no_phone_approval)

/** What the read-only note says after the source's name: the source's reason, or that the session can only be read. */
internal fun readOnlyNote(res: Resources, session: Session): String {
    if (session.readOnlyReason.isNullOrBlank() && session.capabilities?.takeover?.running == true) {
        return res.getString(R.string.chat_takeover_running_reason)
    }
    session.source?.readOnlyReason?.let { return res.getString(R.string.chat_source_read_only_reason, it.resolve(res)) }
    return READ_ONLY.resolve(res)
}

/**
 * In place of what would be typed, for a session whose source only lets the phone read it (a Codex history thread, a
 * runtime that did not declare sending): the source's name as a tag, then why. When [session] offers explicit Codex
 * takeover, renders the compact single-line action strip (`[JTag] reason [Secondary primary action] [⋯]`), falling
 * back to two lines (reason above, actions trailing below) on narrow widths or large text sizes without shrinking type.
 */
@Composable
fun ChatSourceNote(
    session: Session,
    modifier: Modifier = Modifier,
    takeover: CodexTakeoverCoordinator? = null,
    takeoverEnabled: Boolean = true,
) {
    val source = session.source
    val res = LocalResources.current
    val reason = readOnlyNote(res, session)
    if (takeover == null || !session.canContinueCodex) {
        Row(
            modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            source?.let { JTag(it.label.text()) }
            Text(
                reason,
                Modifier.weight(1f),
                style = PersonaTheme.type.caption,
                color = PersonaTheme.palette.ink2,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
        }
        return
    }

    val canResume = session.canTakeover
    val primaryLabel = stringResource(if (canResume) R.string.chat_takeover_action else R.string.chat_takeover_continue_short)
    val overflowLabel = stringResource(R.string.chat_chrome_more)
    val continueLongLabel = stringResource(R.string.chat_takeover_continue_long)
    var menuOpen by remember(session.key) { mutableStateOf(false) }
    val busy = takeover.isBusyFor(session)
    val enabled = takeoverEnabled && !takeover.inFlight

    val gap = Tokens.Space.s
    val rowGap = Tokens.Space.xs
    val minReasonWidth = Tokens.Size.box80

    val actionsContent: @Composable () -> Unit = {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        ) {
            JButton(
                text = primaryLabel,
                onClick = { if (canResume) takeover.takeOver(session) else takeover.continueInNewSession(session) },
                modifier = Modifier.testTag("chat-takeover-primary"),
                kind = JButtonKind.Secondary,
                size = JButtonSize.Small,
                enabled = enabled,
                loading = busy,
            )
            if (canResume) {
                Box {
                    JIconButton(
                        icon = PersonaIcons.moreMenu,
                        contentDescription = overflowLabel,
                        onClick = { menuOpen = true },
                        modifier = Modifier.testTag("chat-takeover-more"),
                        enabled = enabled,
                        raised = false,
                    )
                    JMenu(
                        expanded = menuOpen,
                        onDismiss = { menuOpen = false },
                        actions = listOf(
                            JAction(
                                label = continueLongLabel,
                                icon = PersonaIcons.compose,
                                enabled = enabled,
                                onClick = { takeover.continueInNewSession(session) },
                            ),
                        ),
                    )
                }
            }
        }
    }

    SubcomposeLayout(
        modifier
            .fillMaxWidth()
            .testTag("chat-source-takeover")
            .padding(horizontal = pagePadding, vertical = Tokens.Space.xs),
    ) { constraints ->
        val maxWidth = constraints.maxWidth
        val gapPx = gap.roundToPx()
        val rowGapPx = rowGap.roundToPx()
        val minReasonPx = minReasonWidth.roundToPx()
        val loose = Constraints(minWidth = 0, maxWidth = Constraints.Infinity, minHeight = 0, maxHeight = Constraints.Infinity)

        val tagPlaceable = source?.let { info ->
            subcompose("tag") { JTag(info.label.text()) }.firstOrNull()?.measure(loose)
        }
        val tagUsed = if (tagPlaceable != null) tagPlaceable.width + gapPx else 0

        val measuredActions = subcompose("actions-measure", actionsContent).first().measure(loose)
        val singleLineReasonMax = maxWidth - tagUsed - gapPx - measuredActions.width
        val fitsSingleLine = singleLineReasonMax >= minReasonPx && measuredActions.width <= maxWidth

        if (fitsSingleLine) {
            val reasonPlaceable = subcompose("reason-single") {
                Text(
                    reason,
                    style = PersonaTheme.type.caption,
                    color = PersonaTheme.palette.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }.first().measure(Constraints(minWidth = 0, maxWidth = singleLineReasonMax, minHeight = 0, maxHeight = Constraints.Infinity))
            val height = maxOf(tagPlaceable?.height ?: 0, reasonPlaceable.height, measuredActions.height)
            layout(maxWidth, height) {
                tagPlaceable?.placeRelative(0, (height - tagPlaceable.height) / 2)
                reasonPlaceable.placeRelative(tagUsed, (height - reasonPlaceable.height) / 2)
                measuredActions.placeRelative(maxWidth - measuredActions.width, (height - measuredActions.height) / 2)
            }
        } else {
            val reasonMax = (maxWidth - tagUsed).coerceAtLeast(0)
            val reasonPlaceable = subcompose("reason-stacked") {
                Text(
                    reason,
                    style = PersonaTheme.type.caption,
                    color = PersonaTheme.palette.ink2,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }.first().measure(Constraints(minWidth = 0, maxWidth = reasonMax, minHeight = 0, maxHeight = Constraints.Infinity))
            val actionsPlaceable = if (measuredActions.width <= maxWidth) measuredActions
            else subcompose("actions-fit", actionsContent).first().measure(
                Constraints(minWidth = 0, maxWidth = maxWidth, minHeight = 0, maxHeight = Constraints.Infinity),
            )
            val topHeight = maxOf(tagPlaceable?.height ?: 0, reasonPlaceable.height)
            val totalHeight = topHeight + rowGapPx + actionsPlaceable.height
            layout(maxWidth, totalHeight) {
                tagPlaceable?.placeRelative(0, (topHeight - tagPlaceable.height) / 2)
                reasonPlaceable.placeRelative(tagUsed, (topHeight - reasonPlaceable.height) / 2)
                actionsPlaceable.placeRelative((maxWidth - actionsPlaceable.width).coerceAtLeast(0), topHeight + rowGapPx)
            }
        }
    }
}
