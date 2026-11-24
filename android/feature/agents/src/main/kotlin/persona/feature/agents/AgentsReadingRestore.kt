package persona.feature.agents

import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first

// The Agents list's place across a rebuild of the screen (a killed process, a restored back stack): a row's key and where it
// stood, not an index, since the list that comes back is not the list that was left. The list's own saver keeps an index,
// which names another row once the hub's order has moved; this puts the row back, or says why it cannot.

/** The first tree row in view when the list last rested, and its [offset] from the top of the list (as [AgentReadingPosition] keeps). */
internal data class SavedReadingAnchor(val key: String, val offset: Int)

/** The first of the [items] (lazy key, offset) that is a tree row; headers and notices above the tree are not keyed by it. */
internal fun savedAnchorOf(items: List<Pair<Any, Int>>, treeKeys: Set<String>): SavedReadingAnchor? =
    items.firstOrNull { it.first in treeKeys }?.let { (key, offset) -> SavedReadingAnchor(key as String, offset) }

/**
 * How long a saved row may be missing from a list that reports itself loaded before it is given up (a local setting, not
 * a measured value): the page the list draws is built apart from the hub's load and can trail it by a build or two.
 */
internal const val RESTORE_SETTLE_MILLIS = 1_500L

/** What to do with a saved place, given the tree as it is now. */
internal sealed interface ReadingRestore {
    /** The list is not in yet: look again when it is. */
    data object Wait : ReadingRestore
    /** The row is in the tree at [index] (among the tree's rows): put it back at [offset]. */
    data class Scroll(val index: Int, val offset: Int) : ReadingRestore
    /**
     * The list is in and the row is not (gone, closed, or on a page the list does not hold): start from the top. The list
     * can be one build behind the hub's load, so the place is kept for a moment ([RESTORE_SETTLE_MILLIS]) in case the row follows.
     */
    data object Top : ReadingRestore
}

/** An empty list that has loaded has no place to return to; one still loading may yet bring the row. */
internal fun readingRestore(saved: SavedReadingAnchor, keys: List<String>, loaded: Boolean): ReadingRestore {
    val index = keys.indexOf(saved.key)
    return when {
        index >= 0 -> ReadingRestore.Scroll(index, saved.offset)
        loaded -> ReadingRestore.Top
        else -> ReadingRestore.Wait
    }
}

/**
 * Remembers where the list rested, across a rebuilt screen, and puts it back when the tree has the row. A touch before
 * that wins: the user's own scroll is never undone. At the top nothing is kept, so a rebuilt list opens at the latest.
 * The return is true while such a restore owns the list, so live-read anchoring leaves it alone until the row is back.
 */
@Composable
internal fun RestoreAgentReadingPosition(
    state: LazyListState,
    tree: List<AgentTreeEntry>,
    paginationFooter: Boolean,
    loaded: Boolean,
    onProgrammaticScroll: () -> Unit = {},
): Boolean {
    var savedKey by rememberSaveable { mutableStateOf<String?>(null) }
    var savedOffset by rememberSaveable { mutableIntStateOf(0) }
    // Read once: what this screen saves from now on is not what it starts from.
    var pending by remember { mutableStateOf(savedKey?.let { SavedReadingAnchor(it, savedOffset) }) }
    val keys = remember(tree) { tree.map { it.key } }
    val keysNow by rememberUpdatedState(keys)
    // Built once per tree, not at every look at the list.
    val keySetNow by rememberUpdatedState(remember(keys) { keys.toHashSet() })
    val footer by rememberUpdatedState(paginationFooter)
    val onProgrammaticScrollNow by rememberUpdatedState(onProgrammaticScroll)

    LaunchedEffect(state) {
        snapshotFlow { state.isScrollInProgress }.first { it }
        pending = null
    }
    val saved = pending
    val action = if (saved == null) null else readingRestore(saved, keys, loaded)
    LaunchedEffect(action) {
        when (action) {
            null, ReadingRestore.Wait -> Unit
            ReadingRestore.Top -> {
                // Start from the top at once, but keep the place for a bounded time: a row that arrives in it (this effect
                // restarts as a scroll) is still put back; past it the list is the list as it is, and the next load is not moved back.
                onProgrammaticScrollNow()
                state.scrollToItem(0)
                delay(RESTORE_SETTLE_MILLIS)
                pending = null
            }
            is ReadingRestore.Scroll -> {
                // The tree follows whatever sits above it, and ends before the optional footer.
                snapshotFlow { state.layoutInfo.totalItemsCount }.first { it >= keysNow.size + (if (footer) 1 else 0) }
                val start = state.layoutInfo.totalItemsCount - keysNow.size - (if (footer) 1 else 0)
                onProgrammaticScrollNow()
                state.scrollToItem(start + action.index, -action.offset)
                pending = null
            }
        }
    }
    LaunchedEffect(state) {
        snapshotFlow {
            if (state.isScrollInProgress || pending != null) null
            else if (!state.canScrollBackward) SavedReadingAnchor("", 0)
            else savedAnchorOf(state.layoutInfo.visibleItemsInfo.map { it.key to it.offset }, keySetNow)
        }.filterNotNull().distinctUntilChanged().collect { anchor ->
            savedKey = anchor.key.takeIf { it.isNotEmpty() }
            savedOffset = anchor.offset
        }
    }
    return pending != null
}
