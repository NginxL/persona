package persona.core.designsystem

import android.os.Build
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedContentScope
import androidx.compose.animation.ContentTransform
import androidx.compose.animation.EnterExitState
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.SizeTransform
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.EaseIn
import androidx.compose.animation.core.EaseInOut
import androidx.compose.animation.core.EaseOut
import androidx.compose.animation.core.Easing
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.tween
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.BlurredEdgeTreatment
import androidx.compose.ui.draw.blur
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.LayoutDirection

/**
 * What the composer's action slot draws: [kind] says which round button, the rest how it looks. Two actions are equal when
 * they look alike; the taps are not part of it (the slot reads the latest when one comes), so a button that did not change
 * is not swapped for one that looks the same.
 */
@Immutable
data class ComposerAction(val kind: SendKind, val enabled: Boolean = true, val loading: Boolean = false)

/**
 * The composer's motion as `spec/tokens` and iOS's `Theme.Motion.composerSwap`, `composerSwapFade`, `composerPress` and
 * `composerRelease` give it. Nothing here is a number of its own.
 */
internal object ComposerMotion {
    /** `timingCurve(0.38, 0.7, 0.125, 1)`: the swap's, the press release's and the quote bar's curve on iOS. */
    val swapEasing: Easing = CubicBezierEasing(
        Tokens.Motion.ComposerSwap.curveX1, Tokens.Motion.ComposerSwap.curveY1,
        Tokens.Motion.ComposerSwap.curveX2, Tokens.Motion.ComposerSwap.curveY2,
    )

    /** What scales and blurs with the swap. */
    fun <T> swap(): FiniteAnimationSpec<T> = tween(Tokens.Motion.ComposerSwap.duration, easing = swapEasing)

    /** What fades with the swap fades faster than it grows. */
    fun <T> fade(): FiniteAnimationSpec<T> = tween(Tokens.Motion.ComposerSwap.fadeDuration, easing = curve(Tokens.Motion.ComposerSwap.fadeCurve))

    /** Send pressed down shrinks slowly, and comes back quicker when let go. */
    fun <T> pressIn(): FiniteAnimationSpec<T> = tween(Tokens.Motion.ComposerPress.duration, easing = curve(Tokens.Motion.ComposerPress.curve))
    fun <T> pressOut(): FiniteAnimationSpec<T> = tween(Tokens.Motion.ComposerPress.releaseDuration, easing = swapEasing)

    /** The curves `spec/tokens/motion.json` names, as SwiftUI's `Animation.easeIn`, `easeOut` and `easeInOut`. */
    fun curve(name: String): Easing = when (name) {
        "easeIn" -> EaseIn
        "easeOut" -> EaseOut
        "easeInOut" -> EaseInOut
        else -> error("spec/tokens/motion.json names a curve the design system does not know: $name")
    }

    /**
     * The blur send clears as it grows in. It is a render effect, which Android has from 12 (API 31); on 11 the swap goes
     * without it. Zero where there is none.
     */
    fun blur(sdk: Int = Build.VERSION.SDK_INT): Dp =
        if (sdk >= Build.VERSION_CODES.S) Tokens.Motion.ComposerSwap.blur else Dp(0f)
}

/**
 * The composer's action slot, iOS's `ShellComposerBar` ZStack of send and, in its place, stop (`ComposerRoundButton`): the one
 * round button the composer shows now, or none ([action] null). On Glass a change of button moves as it does on iOS
 * (`Theme.Motion.composerSwap`, `ComposerButtons.swift`):
 * - send grows out of the trailing edge, from a scale of [Tokens.Motion.ComposerSwap.from], clearing a slight blur
 *   (Android 12 and later), and fades in quicker than it grows; leaving, it does the same the other way;
 * - stop shrinks to a fifth of itself as it fades, from the middle, and grows from that coming back;
 * - the slot's width follows with the state spring, so what stands beside it glides rather than jumps;
 * - send shrinks under a finger ([SendButton]'s `pressScales`), stop does not;
 * - the button on its way out takes no touch and is not read out, and looks as it did when it left.
 * With the system's animations off, and in Brief and Board, the swap is instant and the slot is the [SendButton] it always
 * was. Only a change of [ComposerAction.kind] moves; a change of how the button looks (enabled, loading) happens in place.
 *
 * Inside [ComposerFrame] the slot keeps going when the frame moves its trailing content from one row to the other.
 */
@Composable
fun ComposerActionSlot(
    action: ComposerAction?,
    onSend: () -> Unit,
    onStop: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val send by rememberUpdatedState(onSend)
    val stop by rememberUpdatedState(onStop)
    if (PersonaTheme.style != AppStyle.Glass || PersonaTheme.reduceMotion) {
        if (action != null) {
            SendButton(if (action.kind == SendKind.Send) onSend else onStop, modifier, action.enabled, action.kind, action.loading,
                pressScales = action.kind == SendKind.Send)
        }
        return
    }
    val rtl = LocalLayoutDirection.current == LayoutDirection.Rtl
    // The neighbours move with iOS's state animation, which the card runs on `canSend` (ShellComposerBar).
    val glide = PersonaTheme.motion.enter<IntSize>()
    AnimatedContent(
        targetState = action,
        modifier = modifier,
        transitionSpec = { composerActionTransition(initialState, targetState, glide) },
        contentAlignment = if (rtl) Alignment.CenterStart else Alignment.CenterEnd,
        label = "composer-action",
        contentKey = { it?.kind },
    ) { shown ->
        if (shown != null) SwappingAction(shown, { send() }, { stop() }, rtl)
    }
}

/**
 * What [ComposerActionSlot] hands AnimatedContent: no enter or exit of its own, because the button animates itself from the
 * transition ([SwappingAction]), the scale and the blur on one curve and the fade on a shorter one. Only the slot's size
 * goes with the swap, and not when the button stays the same kind.
 */
internal fun composerActionTransition(from: ComposerAction?, to: ComposerAction?, glide: FiniteAnimationSpec<IntSize>): ContentTransform =
    ContentTransform(EnterTransition.None, ExitTransition.None,
        sizeTransform = if (from?.kind == to?.kind) null else SizeTransform(clip = false) { _, _ -> glide })

/**
 * One button of the slot while it comes, stays and goes. The transition's state is Visible while it is the current one and
 * PreEnter or PostExit before and after: send is then tiny at the trailing edge and blurred, stop at a fifth of itself in
 * its middle, both clear. The scale is applied inside the blur, as iOS's `scaleEffect(…).blur(…)` is, so the blur keeps its
 * size while the button is small.
 */
@Composable
private fun AnimatedContentScope.SwappingAction(action: ComposerAction, onSend: () -> Unit, onStop: () -> Unit, rtl: Boolean) {
    val grows = action.kind == SendKind.Send
    val away = if (grows) Tokens.Motion.ComposerSwap.from else Tokens.Motion.ComposerSwap.scale
    val origin = if (grows) TransformOrigin(if (rtl) 0f else 1f, 0.5f) else TransformOrigin.Center
    val scale = transition.animateFloat({ ComposerMotion.swap() }, "composer-action-scale") { if (it == EnterExitState.Visible) 1f else away }
    val fade = transition.animateFloat({ ComposerMotion.fade() }, "composer-action-fade") { if (it == EnterExitState.Visible) 1f else 0f }
    val blurred = transition.animateFloat({ ComposerMotion.swap() }, "composer-action-blur") { if (it == EnterExitState.Visible || !grows) 0f else 1f }
    val radius = ComposerMotion.blur() * blurred.value
    Box(
        Modifier
            .then(if (radius > Dp(0f)) Modifier.blur(radius, BlurredEdgeTreatment.Unbounded) else Modifier)
            .graphicsLayer { scaleX = scale.value; scaleY = scale.value; alpha = fade.value; transformOrigin = origin },
    ) {
        SendButtonBody(if (grows) onSend else onStop, Modifier, action.enabled, action.kind, action.loading, pressScales = grows,
            inert = transition.targetState == EnterExitState.PostExit)
    }
}
