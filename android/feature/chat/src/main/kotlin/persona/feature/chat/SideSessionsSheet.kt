package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.JEmptyState
import persona.core.designsystem.JLoading
import persona.core.designsystem.JRow
import persona.core.designsystem.JSheet
import persona.core.designsystem.JStatusDot
import persona.core.designsystem.Status
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.sort.SideRollup
import persona.core.domain.sort.sideState

/**
 * The header's Side sessions control's spoken label (#7944): "Side sessions (N)", and that one needs you while one does
 * (the control's dot).
 */
@Composable
internal fun sideSessionsLabel(rollup: SideRollup): String = listOfNotNull(
    stringResource(R.string.chat_side_sessions_count, rollup.count),
    stringResource(R.string.chat_side_sessions_needs_you).takeIf { rollup.needsYou },
).joinToString("\n")

/**
 * A parent's side sessions (#7944), as the peers sheet lists a tab's sessions: flat, in [sides]' order (needs you, then
 * working, then newest; stopped last, `sideSessions`), each with its state's mark, its title and its state's word. A tap
 * opens the side's own chat. [rows] are the rows the chat holds, for the state of what a side's own rows wait on; while
 * the hub counts sides this page has not read yet, the sheet says it is loading instead of "No side sessions".
 */
@Composable
internal fun ChatSideSessionsSheet(sides: List<Session>, rows: List<Session>, rollup: SideRollup, onDismiss: () -> Unit,
    onOpen: (SessionKey) -> Unit) {
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.chat_side_sessions), scrollable = true) {
        when {
            sides.isNotEmpty() -> sides.forEach { side ->
                androidx.compose.runtime.key(side.key.path) {
                    val state = sideState(side, rows + sides)
                    val status = Status.entries.firstOrNull { it.name == state.name } ?: Status.Unknown
                    JRow(side.displayTitle, subtitle = status.localizedLabel, leading = { JStatusDot(status) },
                        onClick = { onOpen(side.key) })
                }
            }
            rollup.count > 0 -> JLoading()
            else -> JEmptyState(stringResource(R.string.chat_side_sessions_empty), "", icon = PersonaIcons.sideSessions)
        }
    }
}
