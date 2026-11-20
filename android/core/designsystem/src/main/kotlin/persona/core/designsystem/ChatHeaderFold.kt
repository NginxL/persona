package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt

/**
 * An Agent chat's header that folds into its slim bar (iOS `ChromeHeader`, #4339 C4). [fold] is how far, 0 open to 1
 * folded; it is read only while laying out and drawing, so a finger moving it redraws this header and nothing else.
 * The open header slides up under a shrinking clip and is gone two thirds of the way; [compact], [ChatSlimBar] under
 * the status bar, fades in over the second half and is not there before. Folded, [full] stays composed (a sheet it
 * opened stays up) and keeps its measured height, but takes no touches and leaves the screen reader. [onRange] gets
 * how far it folds in pixels, the open header's height less the slim bar's; [onTouch] any touch on it (it stays
 * open while in use).
 */
@Composable
fun JFoldingHeader(
    fold: () -> Float,
    onRange: (Int) -> Unit,
    onTouch: () -> Unit,
    modifier: Modifier = Modifier,
    full: @Composable () -> Unit,
    compact: @Composable () -> Unit,
) {
    val progress by rememberUpdatedState(fold)
    val touch by rememberUpdatedState(onTouch)
    val gone by remember { derivedStateOf { progress() * FullFade >= 1f } }
    val showsCompact by remember { derivedStateOf { progress() > 0.5f } }
    val statusBar = WindowInsets.statusBars
    Layout(
        contents = listOf(
            { Box(if (gone) Modifier.clearAndSetSemantics {}.takesNoTouches() else Modifier) { full() } },
            { if (showsCompact) compact() },
        ),
        modifier = modifier.clipToBounds().pointerInput(Unit) {
            awaitEachGesture { awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial); touch() }
        },
    ) { (open, slim), constraints ->
        val loose = constraints.copy(minHeight = 0, maxHeight = Constraints.Infinity)
        val opened = open.map { it.measure(loose) }
        val slimmed = slim.map { it.measure(loose) }
        val openHeight = opened.maxOfOrNull { it.height } ?: 0
        val slimHeight = min(statusBar.getTop(this) + Tokens.Size.slimBar.roundToPx(), openHeight)
        val range = max(0, openHeight - slimHeight)
        onRange(range)
        val p = progress().coerceIn(0f, 1f)
        val folded = (range * p).roundToInt()
        // Larger text makes the slim bar taller than its least height: then it takes what it needs.
        val slimNeeds = slimmed.maxOfOrNull { it.height } ?: 0
        layout(constraints.maxWidth, maxOf(openHeight - folded, slimHeight, slimNeeds)) {
            opened.forEach { it.placeWithLayer(0, -folded) { alpha = 1f - min(1f, progress().coerceIn(0f, 1f) * FullFade) } }
            slimmed.forEach { it.placeWithLayer(0, 0) { alpha = max(0f, (progress().coerceIn(0f, 1f) - 0.5f) * 2f) } }
        }
    }
}

/** The open header is clear by two thirds of the fold (iOS: `1 - p * 1.5`). */
private const val FullFade = 1.5f

/** Takes every touch before what is inside sees it: the faded open header under the slim bar. */
private fun Modifier.takesNoTouches(): Modifier = pointerInput(Unit) {
    awaitPointerEventScope { while (true) awaitPointerEvent(PointerEventPass.Initial).changes.forEach { it.consume() } }
}

/**
 * The folded header (iOS `compactChrome`): back, and in the middle a capsule with the agent's mark, the state's dot and
 * the [title], or while the hub is out of reach a spinner and [linkText]; a tap on the capsule opens the header again
 * ([onOpen], said as [openLabel]). The [trailing] control has the same space as back to keep the capsule in the middle. [waiting]:
 * the agent works only because its background work waits, so the dot is the clock (iOS `StatusDot(waiting:)`).
 */
@Composable
fun ChatSlimBar(
    title: String,
    openLabel: String,
    onOpen: () -> Unit,
    modifier: Modifier = Modifier,
    onBack: (() -> Unit)? = null,
    status: Status? = null,
    agentMark: AgentMarkSpec? = null,
    linkText: String? = null,
    waiting: Boolean = false,
    trailing: (@Composable () -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val chip = (if (palette.dark) Tokens.Common.Dark.Fill.chip else Tokens.Common.Light.Fill.chip)
        .compositeOn(palette.surfaceOpaque)
    Row(modifier.fillMaxWidth().statusBarsPadding().heightIn(min = Tokens.Size.slimBar).padding(horizontal = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically) {
        val side = Modifier.width(Tokens.MinTouch.android)
        when (val leading = resolveDetailLeading(LocalSplitDetail.current, onBack)) {
            is DetailLeading.ShowList -> JIconButton(persona.core.designsystem.icons.PersonaIcons.sidebar, stringResource(R.string.designsystem_sidebar), leading.onClick, side, raised = false)
            is DetailLeading.Back -> JIconButton(persona.core.designsystem.icons.PersonaIcons.back, stringResource(R.string.designsystem_back), leading.onClick, side, raised = false)
            DetailLeading.Hidden, null -> Spacer(side)
        }
        Box(Modifier.weight(1f), contentAlignment = Alignment.Center) {
            Row(
                Modifier.heightIn(min = Tokens.Size.wellXS).clip(CircleShape).background(chip)
                    .clickable(role = Role.Button, onClick = onOpen).semantics(mergeDescendants = true) { contentDescription = openLabel }
                    .padding(horizontal = Tokens.Space.m),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
            ) {
                val words = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold)
                if (linkText != null) {
                    JSpinner(Modifier.size(Tokens.Size.markXS), stroke = Tokens.Space.Standard.xxs)
                    Text(linkText, style = words, color = palette.ink, maxLines = 1)
                } else {
                    agentMark?.let { JAgentMark(it, size = Tokens.Size.markXS) }
                    status?.let { JStateDot(it, Tokens.Size.dotS, waiting) }
                    Text(title, Modifier.weight(1f, fill = false), style = words, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
        if (trailing == null) Spacer(side) else Box(side, contentAlignment = Alignment.Center) { trailing() }
    }
}
