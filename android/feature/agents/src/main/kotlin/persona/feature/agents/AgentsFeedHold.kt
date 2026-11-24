package persona.feature.agents

import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch
import persona.core.designsystem.JLatestPill
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status

/** What the user was told about when they left the top, and the controls it was told under. */
internal class AgentsFeedBaseline(val mark: AgentsFeedMark, val scope: List<Any?>)

/**
 * The hold's state. Which page is held is a plain field that nothing observes: a scroll starting or settling draws
 * nothing, so it must not recompose the screen. Only a landing that changes what is drawn bumps [revision], which the
 * page's own composition reads. [baseline] is read only by the pill.
 */
@Stable
internal class AgentsFeed {
    var frozen: AgentsFeedFrozen? = null
        private set
    /** Scrolls the screen itself makes (back to the latest): nested or repeated jumps all hold nothing. */
    private var programmaticDepth = 0
    val programmatic: Boolean get() = programmaticDepth > 0
    fun beginProgrammatic() { programmaticDepth++ }
    fun endProgrammatic() { programmaticDepth-- }
    var revision by mutableIntStateOf(0)
        private set
    var baseline by mutableStateOf<AgentsFeedBaseline?>(null)
        private set

    fun hold(page: AgentsPageState, scope: List<Any?>) { if (frozen == null) frozen = AgentsFeedFrozen(page, scope) }

    /**
     * Lets what waited land. [holdAgain] starts a new hold from [latest] at once, for a list that is still being scrolled:
     * the next push waits again, and its wait starts over. [redraw] asks the page to be chosen again; a caller that is
     * already drawing [latest] does not.
     */
    fun land(latest: AgentsPageState, scope: List<Any?>, holdAgain: Boolean, redraw: Boolean = true) {
        val waited = frozen?.let { it.page !== latest } == true
        frozen = if (holdAgain) AgentsFeedFrozen(latest, scope) else null
        if (waited && redraw) revision++
    }

    fun rebase(next: AgentsFeedBaseline?) { baseline = next }
}

/** What the Agents list holds: the page to draw, and the way back to the latest. */
internal class AgentsFeedView(
    val page: AgentsPageState,
    val showLatest: () -> Unit,
    internal val feed: AgentsFeed,
    internal val scope: List<Any?>,
    internal val markOf: (AgentsPageState) -> AgentsFeedMark,
)

/**
 * Holds the page still while the list scrolls away from the top, for at most [AgentsFeedTiming.CAP_MILLIS], and lands it
 * once the list has rested [AgentsFeedTiming.SETTLE_MILLIS], when it reaches the top, or when a control changes
 * ([holdsAgainst]). Nothing is dropped: what came meanwhile is the next page, and the reading position keeps the row being
 * read where it was as it lands. What needs the user does not wait ([landCritical]). A release while the list is still
 * moving holds again from the page it landed, so a later push waits too.
 *
 * [needsYou] is the Needs You section's rows of a page ([agentsFeedNeedsYou]); [scope] the controls the list is drawn
 * under. The screen's own scrolls (the jump to the latest) hold nothing.
 */
@Composable
internal fun rememberAgentsFeed(
    latest: AgentsPageState,
    needsYou: (AgentsPageState) -> Set<String>,
    scope: List<Any?>,
    list: LazyListState,
    onProgrammaticScroll: () -> Unit = {},
): AgentsFeedView {
    val feed = remember { AgentsFeed() }
    val frozen = feed.frozen
    val holds = remember(frozen, latest, scope, feed.revision) { frozen != null && holdsAgainst(frozen, latest, scope) }
    val shown = remember(frozen, latest, holds) { if (holds && frozen != null) landCritical(frozen.page, latest) else latest }

    val shownNow by rememberUpdatedState(shown)
    val latestNow by rememberUpdatedState(latest)
    val scopeNow by rememberUpdatedState(scope)
    val needsYouNow by rememberUpdatedState(needsYou)
    val onProgrammaticScrollNow by rememberUpdatedState(onProgrammaticScroll)
    val markOf = remember { { page: AgentsPageState -> agentsFeedMark(page, needsYouNow(page)) } }

    // A hold that no longer applies lets go for good, or a later push would find it again; a list still moving holds anew.
    if (frozen != null && !holds) SideEffect {
        feed.land(latest, scope, holdAgain = list.isScrollInProgress && list.canScrollBackward && !feed.programmatic, redraw = false)
    }
    LaunchedEffect(feed, list) {
        snapshotFlow { list.isScrollInProgress to list.canScrollBackward }.collectLatest { (scrolling, away) ->
            when {
                scrolling && away -> if (!feed.programmatic) feed.hold(shownNow, scopeNow)
                // At the top, or resting: what waits lands. The top shows new arrivals and keeps no position.
                scrolling -> feed.land(latestNow, scopeNow, holdAgain = false)
                else -> {
                    delay(AgentsFeedTiming.SETTLE_MILLIS)
                    feed.land(latestNow, scopeNow, holdAgain = false)
                }
            }
        }
    }
    // However long the list keeps moving, what waits lands after the cap, and the next push waits again.
    val waiting = holds && frozen != null && latest !== frozen.page
    LaunchedEffect(frozen, waiting) {
        if (waiting) {
            delay(AgentsFeedTiming.CAP_MILLIS)
            feed.land(latestNow, scopeNow, holdAgain = list.isScrollInProgress && list.canScrollBackward && !feed.programmatic)
        }
    }
    // Further down, what is new is what comes after the user left the top; the controls changing starts that over.
    LaunchedEffect(feed, list, scope) {
        snapshotFlow { list.canScrollBackward }.distinctUntilChanged().collect { away ->
            feed.rebase(if (away) AgentsFeedBaseline(markOf(latestNow), scopeNow) else null)
        }
    }

    val scroll = rememberCoroutineScope()
    val reduceMotion = PersonaTheme.reduceMotion
    return AgentsFeedView(shown, {
        // Everything that waits lands first, and the scroll that follows is the screen's, not the user's reading.
        feed.beginProgrammatic()
        feed.land(latestNow, scopeNow, holdAgain = false)
        scroll.launch {
            try {
                onProgrammaticScrollNow()
                if (reduceMotion) list.scrollToItem(0) else list.animateScrollToItem(0)
            } finally {
                feed.endProgrammatic()
                // The user took the list over mid-way: from here it is theirs, and what comes waits.
                if (list.isScrollInProgress && list.canScrollBackward) feed.hold(latestNow, scopeNow)
            }
        }
    }, feed, scope, markOf)
}

/**
 * The quiet note over the list: what came while it was read further down, by what a tap shows. The most urgent kind speaks;
 * a tap goes to the latest. It reads the baseline and [latest] here, so the list's own scope does not recompose with it.
 */
@Composable
internal fun AgentsFeedNote(feed: AgentsFeedView, latest: AgentsPageState, modifier: Modifier = Modifier) {
    val baseline = feed.feed.baseline
    val notice = remember(baseline, latest) {
        if (baseline == null || baseline.scope != feed.scope) AgentsFeedNotice()
        else agentsFeedNotice(baseline.mark, feed.markOf(latest))
    }
    if (notice.isEmpty) return
    val (text, status) = when {
        notice.needsYou > 0 -> pluralStringResource(R.plurals.agents_feed_needs_you, notice.needsYou, notice.needsYou) to Status.NeedsYou
        notice.offline > 0 -> pluralStringResource(R.plurals.agents_feed_offline, notice.offline, notice.offline) to Status.Offline
        else -> pluralStringResource(R.plurals.agents_feed_new, notice.fresh, notice.fresh) to null
    }
    JLatestPill(text, stringResource(persona.core.designsystem.R.string.designsystem_jump_latest), feed.showLatest, modifier, status)
}
