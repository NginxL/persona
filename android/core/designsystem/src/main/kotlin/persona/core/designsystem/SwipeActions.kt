package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.composed
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import persona.core.designsystem.icons.PhIcon

/**
 * The tint iOS gives a swipe action (`.tint(Theme.positive)` for finishing something, `.tint(Theme.accent)` for putting it
 * back). Glass draws the action as a fill of that colour; Brief and Board keep their neutral backing.
 */
enum class SwipeTone { Accent, Positive }

/**
 * A swipe gesture on a row. The row springs back; the data change removes it. [destructive] ones ("Close") are drawn in the
 * danger colour instead of the status's. [tone] (Glass only) fills the action with iOS's tint, its icon and words in the colour
 * the palette sets on that fill; without one, or in Brief and Board, the action is drawn in its [status]'s colour.
 */
@Immutable
data class SwipeAction(
    val label: String, val icon: PhIcon, val status: Status, val onTrigger: () -> Unit, val destructive: Boolean = false,
    val tone: SwipeTone? = null,
)

/** What a revealed swipe action is painted with: the fill behind it, the colour of its icon and words, and whether the fill is a tint. */
internal data class SwipeColors(val fill: Color, val content: Color, val tinted: Boolean = false)

/** The contrast iOS holds white on its tints to (decision 2 of #4194: white on the accent, 3.5:1). */
private const val WhiteOnTintFloor = 3.5

/**
 * The colours of [action] revealed: on Glass a [SwipeTone] fills it with iOS's tint, its icon and words white as iOS draws
 * them, wherever white reads at [WhiteOnTintFloor] (the accent, the positive green in light), else in the words the palette
 * sets on that fill (the bright positive green in dark); every other action, and Brief and Board, keep the neutral backing
 * and the action's own colour.
 */
internal fun PersonaPalette.swipeColors(action: SwipeAction, glass: Boolean): SwipeColors = when {
    action.destructive || !glass -> neutral(action)
    action.tone == SwipeTone.Accent -> SwipeColors(accent, onAccent, tinted = true)
    action.tone == SwipeTone.Positive -> SwipeColors(positive,
        if (ExtensionThemeColors.contrast(onAccent, positive) >= WhiteOnTintFloor) onAccent
        else positiveRole?.onDot ?: ExtensionThemeColors.on(positive), tinted = true)
    else -> neutral(action)
}

private fun PersonaPalette.neutral(action: SwipeAction) =
    SwipeColors(statusSurface, if (action.destructive) danger.text else status(action.status).text)

/** The actions of the [JSwipeActions] around a row, as a screen reader's custom actions; a row puts them on its own node. */
private val LocalSwipeActions = compositionLocalOf<List<CustomAccessibilityAction>> { emptyList() }

/**
 * Puts the actions of the [JSwipeActions] around this row on the row's own node, as custom accessibility actions: a
 * screen reader (TalkBack) or switch access cannot swipe, and reaches the same actions from the row's Actions menu. A
 * row inside [JSwipeActions] applies it to the node that carries its click, since only that node takes the focus (an
 * ancestor's custom actions are not reached); outside one, or while the swipe has no actions, it adds nothing.
 */
fun Modifier.swipeActionSemantics(): Modifier = composed {
    val actions = LocalSwipeActions.current
    if (actions.isEmpty()) this else semantics { customActions = actions }
}

@Composable
fun JSwipeActions(
    modifier: Modifier = Modifier,
    start: SwipeAction? = null,
    end: SwipeAction? = null,
    content: @Composable () -> Unit,
) {
    if (start == null && end == null) return Box(modifier) { content() }
    val palette = PersonaTheme.palette
    val glass = PersonaTheme.style == AppStyle.Glass
    val state = rememberSwipeToDismissBoxState()
    val scope = rememberCoroutineScope()
    val actions = rememberUpdatedState(start to end)
    // The same actions for a screen reader. Kept while the labels hold, so a row that redraws with the same actions does not
    // redraw its readers; each runs the latest action, as the gesture does.
    val screenReader = remember(start?.label, end?.label) {
        listOfNotNull(
            start?.let { CustomAccessibilityAction(it.label) { actions.value.first?.onTrigger?.invoke(); true } },
            end?.let { CustomAccessibilityAction(it.label) { actions.value.second?.onTrigger?.invoke(); true } },
        )
    }
    // Material keys its dismissal effect by this callback as well as the settled anchor. Keep the callback
    // stable while a data update changes the row and its actions during the return animation.
    val onDismiss: (SwipeToDismissBoxValue) -> Unit = remember(state, scope) {
        { value ->
            val action = when (value) {
                SwipeToDismissBoxValue.StartToEnd -> actions.value.first
                SwipeToDismissBoxValue.EndToStart -> actions.value.second
                SwipeToDismissBoxValue.Settled -> null
            }
            scope.launch {
                try { action?.onTrigger?.invoke() }
                finally { state.reset() }
            }
        }
    }
    CompositionLocalProvider(LocalSwipeActions provides screenReader) {
        SwipeToDismissBox(
            state = state, modifier = modifier,
            enableDismissFromStartToEnd = start != null, enableDismissFromEndToStart = end != null,
            onDismiss = onDismiss,
            backgroundContent = {
                // Only while dragging, and only in the gap the row leaves: rows without a fill of their own (the Agents
                // tree in glass and brief) and translucent ones would otherwise show the action colour under their words.
                val action = when (state.dismissDirection) {
                    SwipeToDismissBoxValue.StartToEnd -> start
                    SwipeToDismissBoxValue.EndToStart -> end
                    SwipeToDismissBoxValue.Settled -> null
                }
                if (action != null) {
                    val colors = palette.swipeColors(action, glass)
                    val fromEnd = state.dismissDirection == SwipeToDismissBoxValue.EndToStart
                    val gap = with(androidx.compose.ui.platform.LocalDensity.current) {
                        kotlin.math.abs(runCatching { state.requireOffset() }.getOrDefault(0f)).toDp()
                    }
                    Box(Modifier.fillMaxSize(), contentAlignment = if (fromEnd) Alignment.CenterEnd else Alignment.CenterStart) {
                        Box(
                            Modifier.fillMaxHeight().width(gap).clip(PersonaTheme.shapes.cardShape).background(colors.fill),
                            contentAlignment = if (fromEnd) Alignment.CenterEnd else Alignment.CenterStart,
                        ) {
                            // The words keep their size and show as the gap opens.
                            Row(
                                Modifier.wrapContentWidth(if (fromEnd) Alignment.End else Alignment.Start, unbounded = true)
                                    .padding(horizontal = 24.dp.dense),
                                verticalAlignment = Alignment.CenterVertically,
                            ) {
                                // A tint is a fill with plain marks on it, as iOS draws them; the neutral backing keeps the filled mark.
                                JIcon(action.icon, null, size = 22.dp, tint = colors.content, selected = !colors.tinted)
                                Spacer(Modifier.width(8.dp.dense))
                                Text(action.label, style = PersonaTheme.type.label, color = colors.content, maxLines = 1)
                            }
                        }
                    }
                }
            },
        ) { content() }
    }
}
