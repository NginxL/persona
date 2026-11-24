package persona.feature.agents

import androidx.compose.foundation.lazy.LazyListItemInfo
import androidx.compose.foundation.lazy.LazyListLayoutInfo
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.snapshotFlow
import kotlin.math.abs

private data class AgentReadingRow(val key: Any, val offset: Int, val size: Int) {
    fun fits(top: Int, bottom: Int, measuredSize: Int = size): Boolean =
        offset >= top && offset + measuredSize <= bottom
}

private fun LazyListItemInfo.readingRow() = AgentReadingRow(key, offset, size)

private data class AgentReadingCorrection(val key: Any, val index: Int, val offset: Int)

private data class PendingReadingCorrection(val request: AgentReadingCorrection, val before: LazyListLayoutInfo)

private data class AgentReadingLayout(
    val layout: LazyListLayoutInfo,
    val scrolling: Boolean,
    val canScrollBackward: Boolean,
    val entries: List<AgentTreeEntry>,
    val hasFooter: Boolean,
    val restoring: Boolean,
)

/** Keep the middle row at its screen position while live updates move or resize the items around it. */
@Composable
internal fun KeepAgentReadingPosition(
    state: LazyListState,
    tree: List<AgentTreeEntry>,
    paginationFooter: Boolean,
    scope: List<Any?>,
    restoring: Boolean,
    onProgrammaticScroll: () -> Unit = {},
) {
    val currentTree = rememberUpdatedState(tree)
    val hasFooter = rememberUpdatedState(paginationFooter)
    val restoreInProgress = rememberUpdatedState(restoring)
    val onProgrammaticScrollNow = rememberUpdatedState(onProgrammaticScroll)
    LaunchedEffect(state, scope) {
        var reading: AgentReadingRow? = null
        var settledRows = emptyList<AgentReadingRow>()
        var pendingCorrection: PendingReadingCorrection? = null
        snapshotFlow {
            AgentReadingLayout(state.layoutInfo, state.isScrollInProgress, state.canScrollBackward,
                currentTree.value, hasFooter.value, restoreInProgress.value)
        }.collect { (layout, scrolling, scrolled, entries, footer, restoring) ->
            if (restoring) {
                // Process restoration owns the list until its key-based scroll finishes. An anchor taken from the
                // list's index-based restored layout would otherwise immediately undo that scroll.
                reading = null
                settledRows = emptyList()
                pendingCorrection = null
                return@collect
            }
            if (scrolling) {
                reading = null
                settledRows = emptyList()
                pendingCorrection = null
                return@collect
            }

            // The tree is contiguous before its optional footer. Headers and notices above it are dynamic.
            val start = layout.totalItemsCount - entries.size - (if (footer) 1 else 0)
            if (start < 0) return@collect
            val visible = layout.visibleItemsInfo.filter { it.index - start in entries.indices }
            // Keep the last settled geometry until Compose reports a layout that agrees with the new tree.
            if (visible.isEmpty() || visible.any { entries[it.index - start].key != it.key }) return@collect
            val top = layout.viewportStartOffset + layout.beforeContentPadding
            val bottom = layout.viewportEndOffset - layout.afterContentPadding
            val rows = visible.filter { it.offset + it.size > top && it.offset < bottom }
            // A clipped row at either edge cannot be relied on to restore its position after it moves.
            val fullyVisible = rows.filter { it.offset >= top && it.offset + it.size <= bottom }

            val current = reading
            if (current == null) {
                if (!scrolled) return@collect
                val middle = top + (bottom - top) / 2
                // Prefer the row covering the midpoint. In a gap, use the closest row center.
                val item = fullyVisible.firstOrNull { middle >= it.offset && middle < it.offset + it.size }
                    ?: fullyVisible.minByOrNull { abs(it.offset + it.size / 2 - middle) }
                reading = item?.readingRow()
            } else {
                var anchor: AgentReadingRow? = current
                var laidOut = rows.firstOrNull { it.key == current.key }
                var index = laidOut?.index ?: entries.indexOfFirst { it.key == current.key }.takeIf { it >= 0 }?.plus(start)
                if (index == null || !current.fits(top, bottom, laidOut?.size ?: current.size) ||
                    (laidOut != null && laidOut !in fullyVisible)
                ) {
                    // A removed or clipped anchor gives way to a surviving neighbor whose old position can still fit.
                    val neighbor = settledRows.sortedBy { abs(it.offset - current.offset) }
                        .firstNotNullOfOrNull { candidate ->
                            if (candidate.key == current.key) return@firstNotNullOfOrNull null
                            val measured = rows.firstOrNull { it.key == candidate.key }
                            if (!candidate.fits(top, bottom, measured?.size ?: candidate.size) ||
                                (measured != null && measured !in fullyVisible)
                            ) return@firstNotNullOfOrNull null
                            val target = measured?.index
                                ?: entries.indexOfFirst { it.key == candidate.key }.takeIf { it >= 0 }?.plus(start)
                            target?.let { candidate to it }
                        }
                    anchor = neighbor?.first
                    index = neighbor?.second
                    laidOut = rows.firstOrNull { it.key == anchor?.key }
                    reading = anchor
                }
                // An offscreen anchor needs to be restored by key; a visible one only needs an offset correction.
                if (anchor != null && index != null && (laidOut == null || laidOut.offset != anchor.offset)) {
                    val request = AgentReadingCorrection(anchor.key, index, -anchor.offset)
                    val pending = pendingCorrection?.takeIf { it.request == request }
                    if (pending == null) {
                        if (!state.isScrollInProgress) {
                            pendingCorrection = PendingReadingCorrection(request, layout)
                            onProgrammaticScrollNow.value()
                            state.requestScrollToItem(index, request.offset)
                        }
                        return@collect
                    }
                    if (layout === pending.before) return@collect
                    // Near the end Compose may clamp the request. Keep its actual position if the row still fits.
                    reading = laidOut?.readingRow()
                }
            }
            pendingCorrection = null
            settledRows = fullyVisible.map { it.readingRow() }
        }
    }
}
