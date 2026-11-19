package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.lines.ChatLine
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.first

@Composable
internal fun DiffNavigation(diff: ToolDiff, list: LazyListState, expanded: Boolean, expand: () -> Unit) {
    val changes = remember(diff) { diffChangeStarts(diff) }
    var selected by remember(diff) { mutableIntStateOf(-1) }
    val scope = rememberCoroutineScope()
    if (changes.isEmpty()) return
    fun jump(delta: Int) {
        selected = if (selected < 0) (if (delta > 0) 0 else changes.lastIndex)
            else (selected + delta).coerceIn(changes.indices)
        expand()
        val target = changes[selected]
        scope.launch {
            // The preview folds unchanged runs, so its rows do not line up with the lines: wait for the whole diff.
            if (!expanded) snapshotFlow { list.layoutInfo.totalItemsCount }.first { it == diff.lines.size }
            list.scrollToItem(target)
        }
    }
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        JTextButton(stringResource(R.string.chat_changes_previous), { jump(-1) }, enabled = selected != 0)
        Text(pluralStringResource(R.plurals.chat_changes_position, changes.size, selected + 1, changes.size), Modifier.weight(1f), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2, maxLines = 1)
        JTextButton(stringResource(R.string.chat_changes_next), { jump(1) }, enabled = selected < changes.lastIndex)
    }
}

/**
 * Reverse-layout lists need an overlay; regular sticky headers would attach to the wrong edge. [rowIds] are the list's
 * row keys in order (outbox bubbles included), so the scroll after collapsing finds the line wherever a bubble sits.
 */
@Composable
internal fun WorkCollapseOverlay(lines: List<ChatLine>, list: LazyListState, rowIds: List<String>, onCollapse: (String) -> Unit,
    modifier: Modifier = Modifier) {
    val byId = remember(lines) { lines.associateBy { it.id } }
    val current by remember(lines, list) {
        val hasOpenWork = lines.any { it is ChatLine.Work && it.open }
        derivedStateOf {
            if (!hasOpenWork) return@derivedStateOf null
            val layout = list.layoutInfo
            val visible = layout.visibleItemsInfo.filter {
                it.offset < layout.viewportEndOffset && it.offset + it.size > layout.viewportStartOffset
            }
            val top = visible.maxByOrNull { it.index }?.key
            val group = when (val line = byId[top]) {
                is ChatLine.Step -> line.groupId
                is ChatLine.End -> line.groupId
                else -> null
            }
            (byId[group] as? ChatLine.Work)?.takeIf { work -> work.open && visible.none { it.key == work.id } }
        }
    }
    var pending by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(lines, rowIds, pending) {
        val id = pending ?: return@LaunchedEffect
        if ((byId[id] as? ChatLine.Work)?.open != false) return@LaunchedEffect
        withFrameNanos { }
        val index = byId[id]?.let { rowIds.indexOf(it.contentKey) } ?: -1
        if (index >= 0) list.scrollToItem(index)
        pending = null
    }
    current?.let { work ->
        val count = lines.filterIsInstance<ChatLine.End>().firstOrNull { it.groupId == work.id }?.count ?: work.steps.size
        JChip(pluralStringResource(R.plurals.chat_steps_collapse, count, count), modifier, onClick = { pending = work.id; onCollapse(work.id) }, icon = PhIcons.CaretUp)
    }
}
