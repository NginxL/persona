package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.clipRect
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp

// The per-account subscription bars (#13173, docs/server/usage-windows.md): a window's bar with the tick for how far through the window
// time is and, when the hub says one, its protection line; and the reset chart of a card. Pure drawing; the numbers and the geometry
// come from core/domain's usage/UsageGroups.kt.

// Sizes are the design system's tokens (no hard-coded dp): a name column of 72 and a row of 36.
private val ChartNameWidth: Dp get() = Tokens.Space.xxl * 2 + Tokens.Space.s
private val ChartRowHeight: Dp get() = Tokens.Space.xxl + Tokens.Space.xs

/** A window's protection line (#13173): [fraction] 0 to 1 along the bar, and the flag's words ("Protect 98%"). */
data class JProtectLine(val fraction: Float, val label: String)

/**
 * A window's bar: [used] 0 to 1 and, with [elapsed], a tick where an even pace would be (the share of the window that has passed).
 * Use left of the tick is behind the pace. A [tone] turns the fill to that role's colour, as [JLinearProgress] does. With a [protect]
 * line: a thin caution notch at it with its flag above and a hatched zone from it to the end, where the scheduler sends no more work.
 * Without one nothing more is drawn.
 */
@Composable
fun JUsageBar(used: Float, modifier: Modifier = Modifier, elapsed: Float? = null, tone: Tone? = null, protect: JProtectLine? = null) {
    val palette = PersonaTheme.palette
    val caution = palette.tone(Tone.Caution)
    Column(modifier.fillMaxWidth().then(if (protect != null) Modifier.semantics(mergeDescendants = true) { contentDescription = protect.label } else Modifier)) {
        if (protect != null) BoxWithConstraints(Modifier.fillMaxWidth()) {
            // The flag ends at the notch, so it never runs off the bar's right edge.
            Box(Modifier.width(maxWidth * protect.fraction.coerceIn(0f, 1f) + Tokens.Space.xs).clearAndSetSemantics {}, contentAlignment = Alignment.CenterEnd) {
                Text(protect.label, style = PersonaTheme.type.caption, color = caution.text, fontWeight = FontWeight.SemiBold, maxLines = 1, softWrap = false)
            }
        }
        Box(Modifier.fillMaxWidth().height(Tokens.Size.dotXL), contentAlignment = Alignment.CenterStart) {
            JLinearProgress(used, tone = tone)
            BoxWithConstraints(Modifier.fillMaxWidth().height(Tokens.Size.dotXL)) {
                if (protect != null) {
                    val at = protect.fraction.coerceIn(0f, 1f)
                    Box(Modifier.offset(x = maxWidth * at).width(maxWidth * (1f - at)).height(Tokens.Space.xs).align(Alignment.CenterStart).hatched(caution.dot))
                    Box(Modifier.offset(x = maxWidth * at - Tokens.Size.line).width(Tokens.Space.xxs).fillMaxHeight().clip(CircleShape).background(caution.dot))
                }
                if (elapsed != null) Box(Modifier.offset(x = maxWidth * elapsed.coerceIn(0f, 1f) - Tokens.Size.line).width(Tokens.Space.xxs).fillMaxHeight().clip(CircleShape).background(palette.ink2))
            }
        }
    }
}

/** Thin diagonal lines over the area: the zone past a protection line. */
private fun Modifier.hatched(color: Color): Modifier = drawBehind {
    val step = Tokens.Space.xs.toPx()
    val stroke = Tokens.Size.line.toPx()
    clipRect {
        var x = -size.height
        while (x < size.width) {
            drawLine(color.copy(alpha = 0.55f), Offset(x, size.height), Offset(x + size.height, 0f), stroke)
            x += step
        }
    }
}

/** One window's capsule on the [JResetChart]: it ends at [endFraction] of the axis (0 is now, 1 the far end) and says [label] there. */
data class JResetBarSpec(val endFraction: Float, val label: String)

/** An account's row on the [JResetChart]: [name] and [sub] (the plan), its 5-hour and weekly capsule, whichever it has. */
data class JResetChartRowSpec(val name: String, val sub: String, val five: JResetBarSpec?, val week: JResetBarSpec?)

/** A label on the axis, centred at [fraction] (the first one, "now", starts at the left edge and the last ends at the right one). */
data class JResetChartTick(val fraction: Float, val label: String)

/** What a legend entry's swatch looks like. */
enum class JResetLegendKind { FiveHour, Week, Now, Protect }

/**
 * The reset chart of a card (#13173): one shared axis ([ticks], now at the left), a row per account in the order of the list above it,
 * each with a teal 5-hour capsule over a blue weekly one running from now to where that window resets, a hollow dot at the end and the
 * time left; a label whose capsule ends past four fifths of the width sits inside it in white. [description] is what a screen reader
 * says for the whole chart; the rows read as one. The look is the owner-approved design of #13173.
 */
@OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
@Composable
fun JResetChart(
    rows: List<JResetChartRowSpec>,
    ticks: List<JResetChartTick>,
    legend: List<Pair<JResetLegendKind, String>>,
    description: String,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    Column(modifier.fillMaxWidth().semantics(mergeDescendants = true) { contentDescription = description }, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Row(Modifier.fillMaxWidth().clearAndSetSemantics {}) {
            Spacer(Modifier.width(ChartNameWidth))
            BoxWithConstraints(Modifier.weight(1f).height(Tokens.Space.l)) {
                // A label that would run into the one before it is left out (a narrow chart cannot say "now" and "6h" side by side).
                var lastRight = Tokens.Space.flush
                ticks.forEachIndexed { i, t ->
                    val first = i == 0
                    val last = i == ticks.lastIndex && ticks.size > 1
                    val box = Tokens.Space.xxl + Tokens.Space.m
                    val half = Tokens.Space.l
                    val centre = maxWidth * t.fraction
                    if (!first && !last && centre - half < lastRight) return@forEachIndexed
                    if (last && maxWidth - half * 2 < lastRight) return@forEachIndexed
                    lastRight = if (first) Tokens.Space.xxl + Tokens.Space.xs else if (last) maxWidth else centre + half
                    Box(
                        Modifier.offset(x = if (first) Tokens.Space.flush else if (last) maxWidth - box else centre - box / 2).width(box),
                        contentAlignment = if (first) Alignment.CenterStart else if (last) Alignment.CenterEnd else Alignment.Center,
                    ) {
                        Text(t.label, style = PersonaTheme.type.caption, color = if (first) palette.ink else palette.ink3,
                            fontWeight = if (first) FontWeight.SemiBold else null, maxLines = 1, softWrap = false)
                    }
                }
            }
        }
        rows.forEachIndexed { index, row -> ChartRow(row, ticks, index % 2 == 1) }
        FlowRow(Modifier.fillMaxWidth().clearAndSetSemantics {}, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            for ((kind, text) in legend) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Box(Modifier.size(Tokens.Space.s, Tokens.Size.bar).clip(CircleShape).background(legendColor(kind)))
                Text(text, style = PersonaTheme.type.caption, color = palette.ink3)
            }
        }
    }
}

@Composable
private fun legendColor(kind: JResetLegendKind): Color = when (kind) {
    JResetLegendKind.FiveHour -> PersonaTheme.palette.hue(JHue.Teal).dot
    JResetLegendKind.Week -> PersonaTheme.palette.hue(JHue.Blue).dot
    JResetLegendKind.Now -> PersonaTheme.palette.ink
    JResetLegendKind.Protect -> PersonaTheme.palette.tone(Tone.Caution).dot
}

@Composable
private fun ChartRow(row: JResetChartRowSpec, ticks: List<JResetChartTick>, banded: Boolean) {
    val palette = PersonaTheme.palette
    val grid = palette.line
    val ink = palette.ink
    Row(
        Modifier.fillMaxWidth().height(ChartRowHeight).clip(RoundedCornerShape(Tokens.Radius.badge)).then(if (banded) Modifier.background(palette.surfaceStrong.copy(alpha = 0.18f)) else Modifier),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.width(ChartNameWidth).padding(end = Tokens.Space.xs)) {
            Text(row.name, style = PersonaTheme.type.caption, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (row.sub.isNotEmpty()) Text(row.sub, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        BoxWithConstraints(
            Modifier.weight(1f).fillMaxHeight().drawBehind {
                // The grid at the axis' labels, then the "now" line over it at the left edge.
                for (t in ticks.drop(1)) drawLine(grid, Offset(size.width * t.fraction, 0f), Offset(size.width * t.fraction, size.height), Tokens.Size.line.toPx())
                drawLine(ink, Offset(Tokens.Size.line.toPx(), 0f), Offset(Tokens.Size.line.toPx(), size.height), Tokens.Space.xxs.toPx())
            },
        ) {
            row.five?.let { Capsule(it, palette.hue(JHue.Teal), top = Tokens.Space.s, height = Tokens.Space.sm) }
            row.week?.let { Capsule(it, palette.hue(JHue.Blue), top = Tokens.Space.m + Tokens.Space.s, height = Tokens.Size.dotL) }
        }
    }
}

/** A capsule fading in from now to its end: a hollow dot there and the time left, outside it or, near the right edge, inside in white. */
@Composable
private fun androidx.compose.foundation.layout.BoxWithConstraintsScope.Capsule(bar: JResetBarSpec, colors: StatusColors, top: Dp, height: Dp) {
    val color = colors.dot
    val surface = PersonaTheme.palette.surface
    val end = bar.endFraction.coerceIn(0f, 1f)
    val dot = Tokens.Space.s
    val label = Tokens.Size.markS
    Box(Modifier.offset(y = top).fillMaxWidth(end).height(height).clip(CircleShape).background(Brush.horizontalGradient(listOf(color.copy(alpha = 0f), color))))
    Box(Modifier.offset(x = maxWidth * end - dot / 2, y = top + height / 2 - dot / 2).size(dot).clip(CircleShape).background(surface).border(Tokens.Space.xxs, color, CircleShape))
    if (end > 0.8f) Box(Modifier.offset(y = top + height / 2 - label / 2).width(maxWidth * end - Tokens.Size.dotXL).height(label), contentAlignment = Alignment.CenterEnd) {
        Text(bar.label, style = PersonaTheme.type.caption, color = colors.onDot, fontWeight = FontWeight.SemiBold, maxLines = 1, softWrap = false, textAlign = TextAlign.End)
    } else Box(Modifier.offset(x = maxWidth * end + dot + Tokens.Space.xxs, y = top + height / 2 - label / 2).height(label), contentAlignment = Alignment.CenterStart) {
        Text(bar.label, style = PersonaTheme.type.caption, color = color, fontWeight = FontWeight.SemiBold, maxLines = 1, softWrap = false)
    }
}
