package persona.feature.settings

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.api.ExtensionTile
import persona.core.designsystem.GaugeSize
import persona.core.designsystem.HueTintAlpha
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.hue
import kotlin.math.roundToInt

/** The percentage a gauge draws and reads, clamped like the other clients'. */
internal fun gaugePercent(ratio: Double): Int = ((if (ratio.isFinite()) ratio else 0.0).coerceIn(0.0, 1.0) * 100).roundToInt()

/** A tile's complete gauge readout; the ring and its visible centre are decorative to TalkBack. */
internal fun gaugeReadout(tile: ExtensionTile): String {
    val value = tile.value?.takeIf { it.isNotBlank() }
    val gauge = tile.gauge
    val unit = gauge?.unit?.takeIf { it.isNotBlank() }
    val amount = value?.let { listOfNotNull(it, unit).joinToString(" ") }
    val percentage = gauge?.let { "${gaugePercent(it.ratio)}%" }
    return listOfNotNull(amount, percentage).joinToString(", ", prefix = "${tile.title}: ")
}

/**
 * UI version 9's ring: from the top, clockwise, with round ends. Its value and unit sit in the centre, or its percentage
 * when the tile has no value. The tile owns the merged accessibility label and meter semantics.
 */
@Composable
internal fun ExtensionGaugeRing(tile: ExtensionTile, compact: Boolean, modifier: Modifier = Modifier) {
    val gauge = tile.gauge ?: return
    val target = (if (gauge.ratio.isFinite()) gauge.ratio else 0.0).toFloat().coerceIn(0f, 1f)
    // Reduce Motion: the ring jumps to its ratio; the eased value is then never read.
    val reduceMotion = PersonaTheme.reduceMotion
    val eased = animateFloatAsState(target, animationSpec = PersonaTheme.motion.enter(), label = "Gauge ratio")
    val palette = PersonaTheme.palette
    val hue = ExtensionViewText.hue(gauge.color) ?: ExtensionViewText.hue(tile.color)
    val colours = hue?.let(palette::hue)
    val arc = colours?.dot ?: palette.accent
    val track = colours?.tint ?: palette.accent.copy(alpha = HueTintAlpha)
    val value = tile.value?.takeIf { it.isNotBlank() } ?: "${gaugePercent(gauge.ratio)}%"
    val unit = gauge.unit?.takeIf { tile.value?.isNotBlank() == true && it.isNotBlank() }
    Box(modifier.size(if (compact) GaugeSize.compact else GaugeSize.page), contentAlignment = Alignment.Center) {
        Canvas(Modifier.fillMaxSize().clearAndSetSemantics {}) {
            val stroke = GaugeSize.stroke.toPx()
            val inset = stroke / 2
            val arcSize = Size(size.width - stroke, size.height - stroke)
            val topLeft = Offset(inset, inset)
            val ratio = if (reduceMotion) target else eased.value
            drawArc(track, -90f, 360f, false, topLeft, arcSize, style = Stroke(stroke, cap = StrokeCap.Round))
            if (ratio > 0f) drawArc(arc, -90f, ratio * 360f, false, topLeft, arcSize, style = Stroke(stroke, cap = StrokeCap.Round))
        }
        Column(Modifier.fillMaxWidth(GaugeSize.centreWidth).clearAndSetSemantics {}, horizontalAlignment = Alignment.CenterHorizontally) {
            Text(value, style = (if (compact) PersonaTheme.type.callout else PersonaTheme.type.title).copy(
                fontWeight = FontWeight.SemiBold, fontFeatureSettings = "tnum"), color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            unit?.let { Text(it, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        }
    }
}
