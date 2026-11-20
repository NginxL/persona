package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
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

private enum class ReaderSlot { Top, Bottom, Content }

/** Gives the scroll controls room outside the visible transcript and measures them with the current font scale. */
@Composable
internal fun ChatReaderLayout(
    modifier: Modifier,
    topInset: Dp,
    bottomInset: Dp,
    collapse: @Composable () -> Unit,
    navigation: @Composable () -> Unit,
    content: @Composable BoxScope.(bottomInset: Dp) -> Unit,
) {
    SubcomposeLayout(modifier) { constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val touchHeight = Tokens.MinTouch.android.roundToPx()
        val footerInset = bottomInset.roundToPx()
        val navigationRoom = constraints.maxHeight - footerInset - topInset.roundToPx() - touchHeight
        val bottom = subcompose(ReaderSlot.Bottom, navigation).singleOrNull()
            ?.takeIf { navigationRoom >= touchHeight + Tokens.Space.s.roundToPx() }
            ?.measure(loose.copy(maxHeight = navigationRoom))
        val readingHeight = constraints.maxHeight - (bottom?.let { it.height + footerInset } ?: 0)
        val collapseRoom = readingHeight - touchHeight
        val top = subcompose(ReaderSlot.Top, collapse).singleOrNull()
            ?.takeIf { collapseRoom >= touchHeight }
            ?.measure(loose)
            ?.takeIf { it.height <= collapseRoom }
        val clippedTop = top?.height ?: 0

        // Keep the reverse list's logical top stable as the collapse chip appears and disappears. Resizing the
        // list here could hide the very row that made the chip appear, causing it to oscillate at group boundaries.
        val transcript = subcompose(ReaderSlot.Content) {
            Layout(
                content = { Box(Modifier.fillMaxSize()) { content(if (bottom == null) bottomInset else Tokens.Space.flush) } },
                modifier = Modifier.clipToBounds(),
            ) { measurables, viewport ->
                val listBody = measurables.single().measure(Constraints.fixed(viewport.maxWidth, readingHeight))
                layout(viewport.maxWidth, viewport.maxHeight) { listBody.place(0, -clippedTop) }
            }
        }.single().measure(Constraints.fixed(constraints.maxWidth, readingHeight - clippedTop))

        layout(constraints.maxWidth, constraints.maxHeight) {
            transcript.place(0, clippedTop)
            top?.placeRelative(0, 0)
            bottom?.placeRelative(0, readingHeight)
        }
    }
}

/** In a reverse-layout list, ordinary sticky headers attach to the newest edge. */
@Composable
internal fun WorkCollapseControl(
    lines: List<ChatLine>, list: LazyListState, rowIds: List<String>, topInset: Dp,
    onCollapse: (String) -> Unit, modifier: Modifier = Modifier,
) {
    val byKey = remember(lines) { lines.associateBy { it.contentKey } }
    val inset = with(LocalDensity.current) { topInset.roundToPx() }
    val current by remember(lines, list, inset) {
        val hasOpenWork = lines.any { it is ChatLine.Work && it.open }
        derivedStateOf {
            if (!hasOpenWork) return@derivedStateOf null
            val layout = list.layoutInfo
            val topEdge = layout.viewportEndOffset - inset
            val visible = layout.visibleItemsInfo.filter {
                it.size > 0 && it.offset < topEdge && it.offset + it.size > layout.viewportStartOffset
            }
            val top = visible.maxByOrNull { it.index }?.key
            val group = when (val line = byKey[top]) {
                is ChatLine.Step -> line.groupId
                is ChatLine.End -> line.groupId
                else -> null
            }
            (byKey[group] as? ChatLine.Work)?.takeIf { work -> work.open && visible.none { it.key == work.contentKey } }
        }
    }
    var pending by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(lines, rowIds, pending) {
        val id = pending ?: return@LaunchedEffect
        if ((byKey[id] as? ChatLine.Work)?.open != false) return@LaunchedEffect
        withFrameNanos { }
        val index = rowIds.indexOf(id)
        if (index >= 0) list.scrollToItem(index)
        pending = null
    }
    current?.let { work ->
        val count = lines.filterIsInstance<ChatLine.End>().firstOrNull { it.groupId == work.id }?.count ?: work.steps.size
        JChip(pluralStringResource(R.plurals.chat_steps_collapse, count, count), modifier,
            onClick = { pending = work.id; onCollapse(work.id) }, icon = PhIcons.CaretUp, role = Role.Button, opaque = true)
    }
}
