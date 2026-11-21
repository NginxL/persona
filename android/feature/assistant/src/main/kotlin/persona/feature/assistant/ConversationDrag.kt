package persona.feature.assistant

import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.AnimationSpec
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.awaitLongPressOrCancellation
import androidx.compose.foundation.gestures.drag
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyListItemInfo
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.hapticfeedback.HapticFeedback
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.changedToUp
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.positionChange
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SharedMotion
import persona.core.designsystem.Tokens

/** Pixel state for Arrange's drag gesture; the optimistic order and its writes live in [ConversationArranger]. */
@Stable
internal class ConversationDragState(
    private val list: LazyListState,
    private val arranger: ConversationArranger,
    private val scope: CoroutineScope,
) {
    internal var activityIds: () -> List<String> = { emptyList() }
    internal var edge = 0f
    internal var speed = 0f
    internal var liftSpec: AnimationSpec<Float> = SharedMotion.instant()
    internal var settleSpec: AnimationSpec<Float> = SharedMotion.instant()
    internal var haptics: HapticFeedback? = null

    var dragging: String? by mutableStateOf(null)
        private set
    var settling: String? by mutableStateOf(null)
        private set
    private var pickedTop = 0f
    private var travel by mutableFloatStateOf(0f)
    private var direction = 0
    private var droppedAt = 0f
    private val lift = Animatable(0f)
    private val glide = Animatable(0f)
    private var lifting: Job? = null
    private var scrolling: Job? = null

    fun lifted(id: String): Boolean = id == dragging || id == settling

    fun translation(id: String): Float {
        val slot = item(id)?.offset ?: return 0f
        return when (id) {
            dragging -> pickedTop + travel - slot
            settling -> (droppedAt - slot) * glide.value
            else -> 0f
        }
    }

    fun liftOf(id: String): Float = if (lifted(id)) lift.value else 0f

    fun start(id: String, longPress: Boolean = false) {
        if (dragging != null) return
        val item = item(id) ?: return
        val drawn = item.offset + translation(id)
        arranger.startDrag(id, activityIds())
        if (arranger.ui.value.drag?.id != id) return
        settling = null
        dragging = id
        pickedTop = drawn
        travel = 0f
        direction = 0
        haptics?.performHapticFeedback(if (longPress) HapticFeedbackType.LongPress else HapticFeedbackType.GestureThresholdActivate)
        lifting?.cancel()
        lifting = scope.launch { lift.animateTo(1f, liftSpec) }
        scrolling?.cancel()
        scrolling = scope.launch { scrollNearEdges() }
    }

    fun moveBy(id: String, dy: Float) {
        if (id != dragging || dy == 0f) return
        travel += dy
        direction = if (dy > 0f) 1 else -1
        reposition()
    }

    fun stop(id: String, dropped: Boolean) {
        if (id != dragging) return
        droppedAt = pickedTop + travel
        dragging = null
        settling = id
        scrolling?.cancel()
        if (dropped) scope.launch(start = CoroutineStart.UNDISPATCHED) { arranger.endDrag(activityIds()) }
        else arranger.cancelDrag()
        haptics?.performHapticFeedback(HapticFeedbackType.GestureEnd)
        lifting?.cancel()
        lifting = scope.launch {
            glide.snapTo(1f)
            launch { lift.animateTo(0f, settleSpec) }
            glide.animateTo(0f, settleSpec)
            if (settling == id) settling = null
        }
    }

    private fun itemKey(id: String) = "arrange:$id"
    private fun item(id: String): LazyListItemInfo? = list.layoutInfo.visibleItemsInfo.firstOrNull { it.key == itemKey(id) }

    private fun rows(order: List<String>): List<LazyListItemInfo> {
        val keys = order.mapTo(HashSet(), ::itemKey)
        return list.layoutInfo.visibleItemsInfo.filter { it.key in keys }
    }

    private fun reposition() {
        val id = dragging ?: return
        val order = arranger.ui.value.drag?.order ?: return
        val visible = rows(order)
        val to = conversationDragMove(order, visible.map {
            ConversationRowSpan((it.key as String).removePrefix("arrange:"), it.offset, it.size)
        }, id, pickedTop + travel) ?: return
        val first = list.firstVisibleItemIndex
        if (visible.any { it.index == first && (it.key == itemKey(id) || it.key == itemKey(order[to])) }) {
            list.requestScrollToItem(first, list.firstVisibleItemScrollOffset)
        }
        arranger.dragTo(to)
        haptics?.performHapticFeedback(HapticFeedbackType.SegmentTick)
    }

    private suspend fun scrollNearEdges() {
        var last = withFrameNanos { it }
        while (true) {
            val now = withFrameNanos { it }
            val seconds = ((now - last) / 1_000_000_000f).coerceAtMost(MaxFrameSeconds)
            last = now
            val id = dragging ?: return
            val order = arranger.ui.value.drag?.order ?: return
            val info = list.layoutInfo
            val self = item(id) ?: continue
            val visible = rows(order)
            val top = pickedTop + travel
            val velocity = conversationEdgeScrollVelocity(
                top = top, bottom = top + self.size, start = 0f,
                end = (info.viewportEndOffset - info.afterContentPadding).toFloat(),
                zone = edge, maxSpeed = speed, direction = direction,
                groupTop = visible.firstOrNull { it.key == itemKey(order.first()) }?.offset?.toFloat(),
                groupBottom = visible.firstOrNull { it.key == itemKey(order.last()) }?.let { (it.offset + it.size).toFloat() },
            )
            if (velocity != 0f && list.scrollBy(velocity * seconds) != 0f) reposition()
        }
    }
}

@Composable
internal fun rememberConversationDrag(
    list: LazyListState,
    arranger: ConversationArranger,
    activityIds: () -> List<String>,
): ConversationDragState {
    val scope = rememberCoroutineScope()
    val state = remember(list, arranger) { ConversationDragState(list, arranger, scope) }
    val density = LocalDensity.current
    val haptics = LocalHapticFeedback.current
    val reduce = PersonaTheme.reduceMotion
    val latestIds = androidx.compose.runtime.rememberUpdatedState(activityIds)
    SideEffect {
        state.activityIds = { latestIds.value() }
        state.edge = with(density) { EdgeZone.toPx() }
        state.speed = with(density) { EdgeSpeed.toPx() }
        state.haptics = haptics
        state.liftSpec = if (reduce) SharedMotion.instant() else SharedMotion.expand()
        state.settleSpec = if (reduce) SharedMotion.instant() else SharedMotion.conceal()
    }
    return state
}

/** The handle picks up on first down; the list does not scroll from that touch. */
internal fun Modifier.conversationDragHandle(state: ConversationDragState, id: String): Modifier = pointerInput(state, id) {
    awaitEachGesture {
        val down = awaitFirstDown(requireUnconsumed = true)
        down.consume()
        state.start(id)
        var dropped = false
        try {
            dropped = drag(down.id) { change ->
                state.moveBy(id, change.positionChange().y)
                change.consume()
            }
        } finally {
            state.stop(id, dropped)
        }
    }
}

/** A long press on the row is the gesture alternative to its visible handle. */
internal fun Modifier.conversationDragAfterLongPress(state: ConversationDragState, id: String): Modifier = pointerInput(state, id) {
    awaitEachGesture {
        val down = awaitFirstDown(requireUnconsumed = true)
        val held = awaitLongPressOrCancellation(down.id) ?: return@awaitEachGesture
        state.start(id, longPress = true)
        var dropped = false
        try {
            dropped = drag(held.id) { change ->
                state.moveBy(id, change.positionChange().y)
                change.consume()
            }
            if (dropped) currentEvent.changes.forEach { if (it.changedToUp()) it.consume() }
        } finally {
            state.stop(id, dropped)
        }
    }
}

@Composable
internal fun ConversationGrip(color: Color, modifier: Modifier = Modifier) {
    Canvas(modifier.size(GripWidth, GripHeight)) {
        val radius = GripDot.toPx() / 2
        val gap = GripGap.toPx()
        val x = size.width / 2
        val y = size.height / 2
        for (column in listOf(x - gap / 2, x + gap / 2)) {
            for (row in listOf(y - gap, y, y + gap)) drawCircle(color, radius, Offset(column, row))
        }
    }
}

private val GripDot = Tokens.Space.Standard.xs
private val GripGap = Tokens.Space.Standard.xs + Tokens.Space.Standard.xxs
internal val GripWidth = Tokens.Space.Standard.m
private val GripHeight = Tokens.Space.Standard.l
private val EdgeZone = Tokens.MinTouch.android
private val EdgeSpeed = Tokens.MinTouch.android * 16
private const val MaxFrameSeconds = 0.05f

internal data class ConversationRowSpan(val id: String, val top: Int, val size: Int) {
    val middle: Float get() = top + size / 2f
}

/** Move after the dragged row's middle crosses another row's middle; stale layouts return null. */
internal fun conversationDragMove(order: List<String>, rows: List<ConversationRowSpan>, dragged: String, top: Float): Int? {
    val shown = rows.map { it.id }
    val onScreen = shown.toHashSet()
    if (order.filter { it in onScreen } != shown) return null
    val self = shown.indexOf(dragged).takeIf { it >= 0 } ?: return null
    val middle = top + rows[self].size / 2f
    var target = self
    rows.forEachIndexed { index, row ->
        if (index > self && middle > row.middle) target = maxOf(target, index)
        if (index < self && middle < row.middle) target = minOf(target, index)
    }
    return if (target == self) null else order.indexOf(shown[target])
}

internal fun conversationEdgeScrollVelocity(
    top: Float, bottom: Float, start: Float, end: Float, zone: Float, maxSpeed: Float, direction: Int,
    groupTop: Float?, groupBottom: Float?,
): Float {
    if (zone <= 0f) return 0f
    return when {
        direction > 0 && bottom > end - zone && (groupBottom == null || groupBottom > end) ->
            maxSpeed * ((bottom - (end - zone)) / zone).coerceIn(0f, 1f)
        direction < 0 && top < start + zone && (groupTop == null || groupTop < start) ->
            -maxSpeed * ((start + zone - top) / zone).coerceIn(0f, 1f)
        else -> 0f
    }
}
