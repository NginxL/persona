package persona.feature.chat

import android.os.SystemClock
import android.text.format.DateUtils
import android.view.MotionEvent
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.AwaitPointerEventScope
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.PointerInputChange
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.util.VelocityTracker
import androidx.compose.ui.input.pointer.util.addPointerInputChange
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.delay
import persona.core.designsystem.JIconButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

// Design system §4.2. Distances and velocities are density-independent and only leftward travel can cancel.
internal const val DictationHoldMillis = 300L
internal fun cancelDictationHold(dx: Float, release: Boolean, velocityX: Float = 0f) =
    if (release) dx < -100f || velocityX < -400f else dx < -150f

@Composable
fun DictationMic(control: DictationControl, label: String, enabled: Boolean = true) {
    val session = control.session
    val listening = session?.listening == true
    val canActivate = enabled && (session?.active != true || listening)
    val stop = stringResource(R.string.chat_dictation_stop)
    val state = stringResource(R.string.chat_dictation_listening)
    val gesture = if (!canActivate || session == null) Modifier else Modifier.pointerInput(session) {
        awaitEachGesture {
            val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
            down.consume()
            val velocity = VelocityTracker().also { it.addPointerInputChange(down) }
            var held = false
            try {
                // While it already listens any activation is the stop tap, however long the finger stays down.
                val up = if (session.active) trackMic(down, velocity) {} else withTimeoutOrNull(DictationHoldMillis) {
                    trackMic(down, velocity) {}
                }
                if (up != null) {
                    if (up.change != null) control.tap()
                } else {
                    held = true
                    session.hold()
                    val end = trackMic(down, velocity) { dx -> if (cancelDictationHold(dx / density, false)) session.release(true) }
                    session.release(end.change?.let { cancelDictationHold((it.position.x - down.position.x) / density,
                        true, velocity.calculateVelocity().x / density) } == true)
                }
            } finally {
                // The OS may take the pointer, or navigation may take the mic. Keep the words already heard.
                if (held) session.release(false)
            }
        }
    }
    val semantics = if (listening) Modifier.semantics { stateDescription = state; onClick(label = stop, action = null) } else Modifier
    JIconButton(if (listening) PersonaIcons.interrupt else PersonaIcons.dictate, if (listening) stop else label,
        control::tap, modifier = gesture.then(semantics), enabled = canActivate,
        tint = if (listening) PersonaTheme.palette.accent else null, selected = listening)
}

/** A missing change means the parent or system took the gesture. */
private class MicEnd(val change: PointerInputChange? = null)

private suspend fun AwaitPointerEventScope.trackMic(down: PointerInputChange, velocity: VelocityTracker,
    moved: (Float) -> Unit): MicEnd {
    while (true) {
        val event = awaitPointerEvent(PointerEventPass.Initial)
        val change = event.changes.firstOrNull { it.id == down.id } ?: return MicEnd()
        if (change.isConsumed) return MicEnd()
        change.consume()
        if (!change.pressed && (event.motionEvent == null || event.motionEvent?.actionMasked == MotionEvent.ACTION_CANCEL)) return MicEnd()
        // Give the tracker the real up so a finger that stopped moving before lifting has no flick velocity.
        velocity.addPointerInputChange(change)
        if (!change.pressed) return MicEnd(change)
        moved(change.position.x - down.position.x)
    }
}

@Composable
internal fun DictationStatus(control: DictationControl) {
    val session = control.session ?: return
    if (!session.listening) return
    var elapsed by remember(session) { mutableLongStateOf(0L) }
    LaunchedEffect(session) {
        val start = SystemClock.elapsedRealtime()
        while (true) { elapsed = (SystemClock.elapsedRealtime() - start) / 1000; delay(1000) }
    }
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Spacer(Modifier.size(Tokens.Size.dotS).background(PersonaTheme.palette.danger.dot, CircleShape))
        Text(DateUtils.formatElapsedTime(elapsed), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink)
        Column(Modifier.weight(1f)) {
            if (control.heard.isNotBlank()) Text(control.heard, maxLines = 1, overflow = TextOverflow.Ellipsis,
                style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
            Text(stringResource(if (session.holding) R.string.chat_dictation_slide else R.string.chat_dictation_listening),
                style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        }
    }
}
