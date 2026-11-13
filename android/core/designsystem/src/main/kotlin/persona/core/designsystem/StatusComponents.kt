package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.annotation.StringRes
import android.content.Context
import androidx.compose.animation.core.*
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcons

/**
 * A status mark: a glowing dot in glass, a dot in brief, a small square holder on the board.
 * Decorative for accessibility; always pair it with visible text. [waiting]: the working agents it stands for only wait in
 * the background, so Glass draws it in that state's lighter tone ([PersonaPalette.backgroundWaiting]) without the darker rim.
 * [spinning]: a lead row whose Agents work while its own conversation is idle (#6230). It draws the working spinner instead
 * of a dot, so "something under this row is running" reads the same as a turn the conversation runs itself; reduced motion
 * freezes it as a static arc. The spinner turns while drawing only ([JSpinner]); iOS's dense rows turn it too (`RowStateMark`).
 * [breathes]: false where iOS draws Needs You as a still dot or mark (the quick filters, a heading's counts, a badge on an
 * avatar or a button, a request's card and panel, the sidebar's rows): the same dot, without the ring.
 */
@Composable
fun JStatusDot(status: Status, modifier: Modifier = Modifier, size: Dp = 8.dp, waiting: Boolean = false, spinning: Boolean = false, breathes: Boolean = true) {
    val tone = PersonaTheme.palette.status(status, waiting)
    if (spinning) {
        // the dense row's mark slot is small; the arc keeps the dot's footprint rather than the 20dp default spinner
        JSpinner(modifier.size(size + Tokens.Space.Standard.xs), color = tone.dot, stroke = SpinnerTokens.compactStroke)
        return
    }
    when (PersonaTheme.style) {
        AppStyle.Glass -> {
            val breath = needsYouBreath(breathes && status == Status.NeedsYou && !PersonaTheme.reduceMotion)
            // the lighter dot keeps its glow as light as itself, and no darker rim
            val lighter = waiting && status == Status.Working
            val glow = tone.dot.copy(alpha = if (lighter) tone.dot.alpha * 0.55f else 0.55f)
            Canvas(modifier.size(size).clearAndSetSemantics {}) {
                drawCircle(Brush.radialGradient(listOf(glow, Color.Transparent), radius = this.size.minDimension), radius = this.size.minDimension)
                drawCircle(tone.dot)
                if (!lighter) drawCircle(tone.text, style = Stroke(1.dp.toPx()))
                breath?.let { drawBreathingRing(tone.dot, this.size.minDimension / 2, it()) }
            }
        }
        AppStyle.Brief -> Box(modifier.size(size).clip(CircleShape).background(tone.dot).border(1.dp, tone.text, CircleShape).clearAndSetSemantics {})
        AppStyle.Board -> Box(modifier.size(size).clip(RoundedCornerShape(2.dp)).background(tone.dot).border(1.dp, tone.text, RoundedCornerShape(2.dp)).clearAndSetSemantics {})
    }
}

/**
 * An agent's state dot where iOS draws `StatusDot(state:size:waiting:)` small (the chat header's second line, its slim bar):
 * [JStatusDot], or, on Glass, the clock of one that only waits in the background ([waiting]), as wide as the ring around a
 * working dot (1.7 times) in that state's lighter tone ([PersonaPalette.backgroundWaiting]). Brief and Board keep the dot.
 */
@Composable
fun JStateDot(status: Status, size: Dp, waiting: Boolean = false, modifier: Modifier = Modifier) {
    if (waiting && PersonaTheme.style == AppStyle.Glass) {
        JIcon(persona.core.designsystem.icons.PersonaIcons.backgroundWaiting, null, modifier, size = size * WaitClockScale,
            tint = PersonaTheme.palette.backgroundWaiting.dot, decorative = true)
    } else JStatusDot(status, modifier, size)
}

/**
 * The Needs You breath, as iOS draws it (`StatusDot`, `Theme.Motion.needsYouBreath*`): every breath a ring leaves the dot
 * and grows to `1 + scale` times it as it fades, eased as a whole by iOS's curve. Null when the dot doesn't breathe; read
 * the returned progress only while drawing, so a breath redraws the canvas and nothing else.
 */
@Composable
internal fun needsYouBreath(breathes: Boolean): (() -> Float)? {
    if (!breathes) return null
    val progress = rememberInfiniteTransition(label = "needs-you").animateFloat(
        0f, 1f,
        infiniteRepeatable(tween(Tokens.Motion.NeedsYouBreath.duration, easing = BreathEasing), RepeatMode.Restart),
        label = "breath",
    )
    return { progress.value }
}

private val BreathEasing = CubicBezierEasing(1f / 3, 2f / 3, 2f / 3, 1f)

internal fun androidx.compose.ui.graphics.drawscope.DrawScope.drawBreathingRing(color: Color, radius: Float, progress: Float) {
    val alpha = Tokens.Motion.NeedsYouBreath.ringOpacity * (1f - progress)
    drawCircle(color.copy(alpha = color.alpha * alpha), radius = radius * (1f + Tokens.Motion.NeedsYouBreath.scale * progress),
        style = Stroke(Tokens.Size.lineMedium.toPx()))
}

/** The older dot; same as [JStatusDot]. */
@Composable
fun StatusDot(status: Status, modifier: Modifier = Modifier) = JStatusDot(status, modifier)

/**
 * The leading mark of a session row, larger than a dot: glass glows, brief turns a quiet ring while working and
 * shows a check when done, the board shows a filled square. [animated] lets working sessions move.
 */
@Composable
fun JStatusMark(status: Status, modifier: Modifier = Modifier, animated: Boolean = true) {
    val palette = PersonaTheme.palette
    val tone = palette.status(status)
    val moving = animated && !PersonaTheme.reduceMotion
    when (PersonaTheme.style) {
        AppStyle.Glass -> Box(modifier.size(28.dp), contentAlignment = Alignment.Center) {
            val breath = needsYouBreath(moving && status == Status.NeedsYou)
            Canvas(Modifier.size(26.dp).clearAndSetSemantics {}) {
                val dot = 4.5.dp.toPx()
                drawCircle(Brush.radialGradient(listOf(tone.dot.copy(alpha = 0.5f), Color.Transparent)))
                drawCircle(tone.dot, radius = dot)
                drawCircle(tone.text, radius = dot, style = Stroke(1.dp.toPx()))
                breath?.let { drawBreathingRing(tone.dot, dot, it()) }
            }
        }
        AppStyle.Brief -> Box(modifier.size(36.dp).clip(CircleShape).background(palette.surface), contentAlignment = Alignment.Center) {
            when (status) {
                Status.Working, Status.Waiting -> JSpinner(Modifier.size(18.dp), color = palette.ink)
                Status.Unread -> JIcon(PhIcons.CheckCircle, null, size = 20.dp, tint = palette.done.dot, selected = true)
                Status.NeedsYou -> Box(Modifier.size(12.dp).clip(CircleShape).background(palette.marker).border(1.5.dp, palette.ink, CircleShape))
                Status.Idle, Status.Unknown, Status.Muted -> JIcon(PhIcons.Check, null, size = 17.dp, tint = palette.ink3)
                Status.Offline -> Box(Modifier.size(8.dp).clip(CircleShape).background(palette.offline.dot))
            }
        }
        AppStyle.Board -> Box(modifier.size(22.dp).clip(RoundedCornerShape(4.dp)).background(tone.dot), contentAlignment = Alignment.Center) {
            when (status) {
                Status.Working, Status.Waiting -> if (moving) JSpinner(Modifier.size(13.dp), color = tone.onDot, stroke = 1.8.dp)
                    else JIcon(PhIcons.Play, null, size = 12.dp, tint = tone.onDot, selected = true)
                Status.NeedsYou -> JIcon(PhIcons.Hand, null, size = 13.dp, tint = palette.needsYou.onDot, selected = true)
                Status.Unread -> JIcon(PhIcons.Check, null, size = 13.dp, tint = tone.onDot)
                else -> JIcon(PhIcons.Checks, null, size = 13.dp, tint = tone.onDot)
            }
        }
    }
}

/**
 * A state in words with the style's dot before it ("Connected", "Not connected", "Disabled"), for a row's trailing side. [problem] puts a
 * warning mark and the words in the danger colour instead ("Error").
 */
@Composable
fun JStateLabel(text: String, status: Status, modifier: Modifier = Modifier, problem: Boolean = false) {
    val palette = PersonaTheme.palette
    Row(modifier.semantics(mergeDescendants = true) {}, horizontalArrangement = Arrangement.spacedBy(6.dp.dense), verticalAlignment = Alignment.CenterVertically) {
        if (problem) JIcon(PhIcons.WarningCircle, null, size = 14.dp, tint = palette.danger.dot) else JStatusDot(status)
        Text(text, style = PersonaTheme.type.caption, color = if (problem) palette.danger.text else palette.ink2, maxLines = 1, softWrap = false,
            overflow = TextOverflow.Ellipsis)
    }
}

@Composable
fun StatusLabel(status: Status, modifier: Modifier = Modifier, text: String = status.localizedLabel) {
    Text(text, modifier, color = PersonaTheme.palette.status(status).text, style = PersonaTheme.type.caption, maxLines = 1, overflow = TextOverflow.Ellipsis)
}

/** Where an outgoing message or operation stands. Pages show [localizedLabel] (or [labelRes] outside composition). */
enum class PendingStatus {
    Sending, Waiting, Unconfirmed, Verifying, NeedsReview, Refused, WaitingForNetwork;

    @get:StringRes val labelRes: Int get() = when (this) {
        Sending -> R.string.designsystem_pending_sending
        Waiting -> R.string.designsystem_pending_waiting
        Unconfirmed -> R.string.designsystem_pending_unconfirmed
        Verifying -> R.string.designsystem_pending_verifying
        NeedsReview -> R.string.designsystem_pending_needs_review
        Refused -> R.string.designsystem_pending_refused
        WaitingForNetwork -> R.string.designsystem_pending_waiting_for_network
    }

    /** The words in the page's language. */
    val localizedLabel: String @Composable get() = stringResource(labelRes)

    /** The words in [context]'s language, for notifications and other code outside composition. */
    fun label(context: Context): String = context.getString(labelRes)
}

@Composable
fun PendingBadge(status: PendingStatus, modifier: Modifier = Modifier, reason: String? = null) {
    val palette = PersonaTheme.palette
    val caution = status != PendingStatus.Sending && status != PendingStatus.Waiting
    val color = if (caution) palette.cautionText else palette.ink2
    val label = if (status == PendingStatus.Refused && !reason.isNullOrBlank()) stringResource(R.string.designsystem_pending_refused_reason, reason)
        else status.localizedLabel
    val shape = RoundedCornerShape(if (PersonaTheme.style == AppStyle.Board) 4.dp else 8.dp.corner)
    Row(
        modifier.semantics(mergeDescendants = true) {}.clip(shape).background(palette.statusSurface)
            .padding(horizontal = 8.dp.dense, vertical = 4.dp.dense),
        horizontalArrangement = Arrangement.spacedBy(6.dp.dense), verticalAlignment = Alignment.CenterVertically,
    ) {
        if (status == PendingStatus.Sending || status == PendingStatus.Verifying) JSpinner(Modifier.size(14.dp), color = color, stroke = 1.8.dp)
        else JIcon(
            when (status) { PendingStatus.Waiting -> PhIcons.Clock; PendingStatus.Unconfirmed -> PhIcons.HourglassMedium; else -> PhIcons.WarningCircle },
            null, size = 14.dp, tint = color,
        )
        Text(label, style = PersonaTheme.type.caption, color = color, maxLines = 2, overflow = TextOverflow.Ellipsis)
    }
}

enum class StatusLineKind { Ended, MachineOffline, Submitting, Unconfirmed, NeedsYou, Stopping, Working, Waiting, Processing }

data class StatusLineState(
    val ended: Boolean = false,
    val replaced: Boolean = false,
    val machineOffline: Boolean = false,
    val lastUpdatedMinutes: Int? = null,
    val submitting: Boolean = false,
    val answering: Boolean = false,
    val unconfirmed: Boolean = false,
    val pendingRequests: Int = 0,
    val blocked: Boolean = false,
    val requestSummary: String? = null,
    val stopping: Boolean = false,
    val working: Boolean = false,
    val workDetail: String? = null,
    val waiting: Boolean = false,
    val waitingDetail: String? = null,
    /** Hub receipt only: does not imply that a model or tool has started. */
    val processing: Boolean = false,
)

/** Pages show [message] (`message.text()`, or [localizedText]) in their own language. */
data class StatusLineContent(val kind: StatusLineKind, val status: Status, val message: UiText) {
    val localizedText: String @Composable get() = message.text()

    val actionable: Boolean get() = kind != StatusLineKind.Submitting && kind != StatusLineKind.Stopping
}

/** Resolves only session state. Hub connectivity belongs in the page title. */
fun resolveStatusLine(state: StatusLineState): StatusLineContent? = with(state) {
    fun line(kind: StatusLineKind, status: Status, @StringRes id: Int, vararg args: Any) =
        StatusLineContent(kind, status, UiText.Res(id, args.toList()))
    val minutes = lastUpdatedMinutes?.coerceAtLeast(0)
    val detail = workDetail?.takeIf { it.isNotBlank() }
    when {
        ended || replaced -> line(StatusLineKind.Ended, Status.Offline, if (replaced) R.string.designsystem_line_replaced else R.string.designsystem_line_ended)
        machineOffline -> StatusLineContent(StatusLineKind.MachineOffline, Status.Offline,
            minutes?.let { UiText.Plural(R.plurals.designsystem_line_machine_offline_since, it) } ?: UiText.Res(R.string.designsystem_line_machine_offline))
        submitting || answering -> line(StatusLineKind.Submitting, Status.Idle, if (answering) R.string.designsystem_line_answering else R.string.designsystem_message_sending)
        unconfirmed -> line(StatusLineKind.Unconfirmed, Status.Unknown, R.string.designsystem_line_unconfirmed)
        pendingRequests > 0 || blocked -> when {
            pendingRequests > 1 -> StatusLineContent(StatusLineKind.NeedsYou, Status.NeedsYou,
                UiText.Plural(R.plurals.designsystem_line_needs_you_count, pendingRequests))
            !requestSummary.isNullOrBlank() -> line(StatusLineKind.NeedsYou, Status.NeedsYou, R.string.designsystem_line_needs_you_about, requestSummary)
            else -> line(StatusLineKind.NeedsYou, Status.NeedsYou, R.string.designsystem_line_needs_you)
        }
        stopping -> line(StatusLineKind.Stopping, Status.Working, R.string.designsystem_line_stopping)
        processing -> line(StatusLineKind.Processing, Status.Idle, R.string.designsystem_message_processing)
        working -> if (detail != null) line(StatusLineKind.Working, Status.Working, R.string.designsystem_line_working_on, detail) else line(StatusLineKind.Working, Status.Working, R.string.designsystem_line_working)
        waiting -> waitingDetail?.takeIf { it.isNotBlank() }?.let { StatusLineContent(StatusLineKind.Waiting, Status.Waiting, UiText.Plain(it)) }
            ?: line(StatusLineKind.Waiting, Status.Waiting, R.string.designsystem_line_waiting)
        else -> null
    }
}

/**
 * One line of session state above the composer. Extra facts ("Loop", "Background 2", "Queued 1") ride along as [chips] instead
 * of stacking more rows.
 */
@Composable
fun StatusLine(
    state: StatusLineState,
    modifier: Modifier = Modifier,
    onClick: ((StatusLineKind) -> Unit)? = null,
    chips: List<String> = emptyList(),
    trailing: (@Composable RowScope.() -> Unit)? = null,
    /**
     * False where the page's header already says the state (an Agent's chat, #3957, as iOS, which shows none above its
     * composer): the line leaves the state out and keeps what the header does not say, the [chips] (queued, loop,
     * background) and a message whose delivery is not confirmed (it asks for a check); with neither it is not drawn.
     */
    sayState: Boolean = true,
) {
    val line = resolveStatusLine(state)?.takeIf { sayState || it.kind == StatusLineKind.Unconfirmed }
    if (line == null && chips.isEmpty()) return
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val status = line?.status ?: Status.Idle
    val tone = palette.status(status)
    val caution = line?.kind == StatusLineKind.Unconfirmed
    val color = if (caution) palette.cautionText else when (style) {
        AppStyle.Brief -> palette.ink
        else -> tone.text
    }
    val click = if (onClick != null && (line == null || line.actionable)) Modifier.clickable(role = Role.Button) { onClick(line?.kind ?: StatusLineKind.Waiting) } else Modifier
    val shape = RoundedCornerShape(if (style == AppStyle.Board) 8.dp.corner else 50.dp)
    val frame = when (style) {
        AppStyle.Glass -> Modifier.clip(shape).background(palette.statusSurface).border(1.dp, palette.strokeBottom, shape)
        AppStyle.Brief -> Modifier.clip(shape)
        AppStyle.Board -> Modifier.clip(shape).background(palette.surface).border(1.dp, palette.line, shape)
    }
    Row(
        modifier.fillMaxWidth().padding(horizontal = if (style == AppStyle.Brief) 6.dp.dense else 10.dp.dense, vertical = 4.dp.dense).then(frame).then(click)
            .heightIn(min = 40.dp.row).padding(horizontal = 12.dp.dense, vertical = 8.dp.dense)
            .semantics(mergeDescendants = true) { liveRegion = LiveRegionMode.Polite },
        horizontalArrangement = Arrangement.spacedBy(8.dp.dense), verticalAlignment = Alignment.CenterVertically,
    ) {
        if (line != null) {
            when {
                caution -> JIcon(PhIcons.HourglassMedium, null, size = 16.dp, tint = color)
                style == AppStyle.Brief && line.kind == StatusLineKind.NeedsYou -> Box(Modifier.size(10.dp).clip(CircleShape).background(palette.marker).border(1.5.dp, palette.ink, CircleShape))
                line.kind in setOf(StatusLineKind.Submitting, StatusLineKind.Processing) ||
                    (style == AppStyle.Brief && (line.kind == StatusLineKind.Working || line.kind == StatusLineKind.Stopping)) -> JSpinner(Modifier.size(14.dp), stroke = 1.8.dp)
                style == AppStyle.Board -> JStatusMark(status, animated = line.kind == StatusLineKind.Working)
                else -> JStatusDot(status, size = 8.dp)
            }
            Text(line.localizedText, color = color, style = PersonaTheme.type.caption, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        }
        if (chips.isNotEmpty()) {
            if (line != null) Spacer(Modifier.weight(1f))
            chips.forEach { Text(it, style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1) }
        }
        trailing?.invoke(this)
        if (onClick != null && line?.actionable != false) JIcon(PhIcons.CaretRight, null, size = 14.dp, tint = palette.ink3)
    }
}
