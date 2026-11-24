package persona.feature.chat

import android.content.Context
import android.content.SharedPreferences
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.core.content.edit
import persona.core.designsystem.Haptic
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.rememberHaptics

/**
 * How tall the user made a lone question card over the composer with its top-edge grabber (iOS `CardHeight`, #12306).
 * Persisted on-device as a share of the room (`0f..1f`; null = default cap) so it adapts across portrait and landscape.
 */
@Stable
internal class CardHeightStore(
    private val readShare: () -> Float? = { null },
    private val writeShare: (Float?) -> Unit = {},
) {
    /** User-chosen share in `0f..1f`, or null to follow the default cap. */
    var share: Float? by mutableStateOf(readShare()?.coerceIn(0f, 1f))
        private set

    /** The card currently being dragged and its live height under the finger. */
    var liveId: String? by mutableStateOf(null)
        private set
    var liveHeight: Dp? by mutableStateOf(null)
        private set

    /** Container cap for [id]: live dragged height, persisted share of [range], or [default]. */
    fun cap(default: Dp, range: ClosedRange<Dp>, id: String): Dp {
        val current = liveHeight
        if (liveId == id && current != null) return current.coerceIn(range.start, range.endInclusive)
        val chosen = share ?: return default
        return range.start + (range.endInclusive - range.start) * chosen
    }

    fun drag(id: String, to: Dp) {
        liveId = id
        liveHeight = to
    }

    /** Commits the dragged height as the persisted share once the finger lifts. */
    fun end(id: String, range: ClosedRange<Dp>) {
        val current = liveHeight
        if (liveId != id || current == null) return
        val span = range.endInclusive - range.start
        val clamped = current.coerceIn(range.start, range.endInclusive)
        val chosen = if (span > Tokens.Space.flush) ((clamped - range.start) / span).coerceIn(0f, 1f) else 1f
        liveId = null
        liveHeight = null
        set(chosen)
    }

    /** Tap on the grabber: switch to tallest room, or back to default from tallest. */
    fun toggle() {
        set(if (share == 1f) null else 1f)
    }

    /** Accessibility step adjustment: quarter of the room at a time. */
    fun step(delta: Float, from: Dp, range: ClosedRange<Dp>) {
        val span = range.endInclusive - range.start
        val current = share ?: if (span > Tokens.Space.flush) {
            ((from.coerceIn(range.start, range.endInclusive) - range.start) / span).coerceIn(0f, 1f)
        } else 1f
        set((current + delta).coerceIn(0f, 1f))
    }

    private fun set(value: Float?) {
        share = value
        writeShare(value)
    }

    companion object {
        private const val PREFS = "persona-chat-ui"
        private const val KEY = "ui.questionCardHeight"
        private const val UNSET = -1f

        fun persistent(context: Context): CardHeightStore {
            val prefs: SharedPreferences = context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            return CardHeightStore(
                readShare = { prefs.getFloat(KEY, UNSET).takeIf { it in 0f..1f } },
                writeShare = { value ->
                    prefs.edit {
                        if (value == null) remove(KEY) else putFloat(KEY, value.coerceIn(0f, 1f))
                    }
                },
            )
        }
    }
}

@Composable
internal fun rememberCardHeightStore(): CardHeightStore {
    val context = LocalContext.current
    return remember(context) { CardHeightStore.persistent(context) }
}

/** Resize parameters for a lone unfolded pending card above the composer when the keyboard is hidden. */
internal data class CardResize(
    val id: String,
    val height: () -> Dp,
    val range: ClosedRange<Dp>,
    val store: CardHeightStore,
)

/**
 * Top-edge pill grabber on a lone unfolded pending card (iOS `CardGrabber`, #12306): vertical drag resizes smoothly
 * between shortest and tallest room, tap toggles tallest / default, and TalkBack exposes toggle + quarter-step actions.
 */
@Composable
internal fun CardGrabber(resize: CardResize, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val density = LocalDensity.current
    val haptics = rememberHaptics()
    val label = stringResource(R.string.chat_card_height_label)
    val toggleLabel = stringResource(R.string.chat_card_height_toggle)
    val tallerLabel = stringResource(R.string.chat_card_height_taller)
    val shorterLabel = stringResource(R.string.chat_card_height_shorter)
    Box(
        modifier
            .fillMaxWidth()
            .height(Tokens.Space.l)
            .pointerInput(resize.id, resize.range, resize.store) {
                var startHeight = Tokens.Space.flush
                var dragPx = 0f
                detectVerticalDragGestures(
                    onDragStart = {
                        startHeight = resize.height()
                        dragPx = 0f
                        haptics(Haptic.Selection)
                    },
                    onVerticalDrag = { change, dragAmount ->
                        change.consume()
                        dragPx += dragAmount
                        val next = with(density) { startHeight - dragPx.toDp() }
                        resize.store.drag(resize.id, next)
                    },
                    onDragEnd = { resize.store.end(resize.id, resize.range) },
                    onDragCancel = { resize.store.end(resize.id, resize.range) },
                )
            }
            .pointerInput(resize.id, resize.store) {
                detectTapGestures {
                    haptics(Haptic.Tap)
                    resize.store.toggle()
                }
            }
            .semantics {
                contentDescription = label
                role = Role.Button
                onClick(label = toggleLabel) {
                    resize.store.toggle()
                    true
                }
                customActions = listOf(
                    CustomAccessibilityAction(tallerLabel) {
                        resize.store.step(0.25f, resize.height(), resize.range)
                        true
                    },
                    CustomAccessibilityAction(shorterLabel) {
                        resize.store.step(-0.25f, resize.height(), resize.range)
                        true
                    },
                )
            }
            .testTag("interaction-grabber"),
        contentAlignment = Alignment.Center,
    ) {
        Box(
            Modifier
                .width(Tokens.Space.xxl)
                .height(Tokens.Space.xs)
                .clip(CircleShape)
                .background(palette.ink3.copy(alpha = 0.4f)),
        )
    }
}
