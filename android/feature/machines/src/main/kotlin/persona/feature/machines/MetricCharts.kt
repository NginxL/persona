package persona.feature.machines

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectDragGesturesAfterLongPress
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.*
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.clipRect
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.dp
import androidx.annotation.StringRes
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.contracts.MetricPoint
import persona.core.designsystem.currentLocale
import java.util.Locale
import persona.core.designsystem.JCard
import persona.core.designsystem.MetricColor
import persona.core.designsystem.MetricPalette
import persona.core.designsystem.PersonaTheme
import kotlin.math.max
import persona.core.designsystem.dense

/** [percent] kinds are drawn against a fixed 0–100 axis. Disk points carry a percentage ([diskPercentPoints]). */
internal enum class MetricKind(@StringRes val label: Int, val percent: Boolean = false) {
    Cpu(R.string.machines_metric_cpu, percent = true), Memory(R.string.machines_metric_memory), Rx(R.string.machines_metric_rx),
    Tx(R.string.machines_metric_tx), Load(R.string.machines_metric_load), Disk(R.string.machines_metric_disk, percent = true);
    fun color(palette: MetricPalette) = when (this) {
        Cpu -> palette.cpu; Memory -> palette.mem
        Rx -> palette.rx; Tx -> palette.tx; Load -> palette.load; Disk -> palette.disk
    }
    fun value(point: MetricPoint): Double? = when (this) {
        Cpu -> point.cpu; Memory -> point.mem; Rx -> point.rx; Tx -> point.tx; Load -> point.load; Disk -> point.disk
    }
    fun format(value: Double?, locale: Locale): String = when (this) {
        Cpu, Disk -> MetricFormat.percent(value, locale)
        Memory -> MetricFormat.bytes(value, locale)
        Rx, Tx -> MetricFormat.rate(value, locale)
        Load -> MetricFormat.number(value, locale)
    }
}

private fun selectedMetricPoint(points: List<MetricPoint>, time: Long, step: Int, kinds: List<MetricKind>): MetricPoint? =
    selectedPoint(points, time, step) { point -> kinds.any { kind -> kind.value(point)?.isFinite() == true } }

@Composable
internal fun MetricChart(
    title: String, points: List<MetricPoint>, kinds: List<MetricKind>, nowValues: List<Double?>,
    end: Long, span: Long, step: Int, selection: Long?, onSelect: (Long?) -> Unit,
    loadCores: Int? = null,
) {
    val picked = remember(points, kinds, selection, step) { selection?.let { selectedMetricPoint(points, it, step, kinds) } }
    val locale = currentLocale
    val metricColors = MetricColor.current
    val reference = loadCores?.takeIf { it > 0 }?.toDouble()
    JCard(Modifier.fillMaxWidth().padding(bottom = 12.dp.dense)) {
        Text(title, style = PersonaTheme.type.title, color = PersonaTheme.palette.ink)
        Text(selection?.let { MetricFormat.time(it, locale) } ?: stringResource(R.string.machines_now),
            Modifier.testTag("metric-selection-${kinds.first().name}"), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
        kinds.forEachIndexed { index, kind ->
            val value = if (selection != null) picked?.let(kind::value) else nowValues.getOrNull(index)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp.dense)) {
                Box(Modifier.padding(top = 6.dp.dense).size(8.dp).background(kind.color(metricColors), PersonaTheme.shapes.controlShape))
                Column {
                    Text("${stringResource(kind.label)} ${kind.format(value, locale)}", style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink)
                    if (kind == MetricKind.Load) loadPerCoreText(value, loadCores, locale)?.let {
                        Text(it, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2)
                    }
                }
            }
        }
        if (selection != null && picked == null) Text(stringResource(R.string.machines_chart_no_sample), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
        if (reference != null) Text(pluralStringResource(R.plurals.machines_chart_reference, reference.toInt(), reference.toInt()), style = PersonaTheme.type.micro,
            color = PersonaTheme.palette.ink3)
        val separator = stringResource(R.string.machines_chart_summary_separator)
        val summary = kinds.mapIndexed { index, kind ->
            val peak = points.filter { !it.gap && it.t in (end - span)..end }.mapNotNull(kind::value).maxOrNull()
            stringResource(R.string.machines_chart_summary, stringResource(kind.label), kind.format(nowValues.getOrNull(index), locale), kind.format(peak, locale))
        }.joinToString(separator)
        Plot(points, kinds, end, span, step, Modifier.fillMaxWidth().height(180.dp)
            .testTag("metric-plot-${kinds.first().name}").semantics { contentDescription = summary },
            axes = true, reference = reference, selection = selection, onSelect = onSelect)
        if (points.none { !it.gap && it.t in (end - span)..end && kinds.any { kind -> kind.value(it) != null } }) {
            Text(stringResource(R.string.machines_chart_empty), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
        }
    }
}

@Composable
internal fun Plot(
    points: List<MetricPoint>, kinds: List<MetricKind>, end: Long, span: Long, step: Int,
    modifier: Modifier = Modifier, axes: Boolean = false, reference: Double? = null,
    selection: Long? = null, onSelect: (Long?) -> Unit = {},
) {
    val runs = remember(points, kinds, step) { kinds.map { kind -> segments(points, step, kind::value) } }
    val start = end - span
    val maximum = if (kinds.all { it.percent }) 100.0 else max(1.0,
        max(reference ?: 0.0, runs.flatten().flatten().filter { it.t in start..end }.maxOfOrNull { it.value } ?: 0.0) * 1.1)
    val metricColors = MetricColor.current
    val axis = PersonaTheme.palette.ink3
    val grid = PersonaTheme.palette.line
    val measure = rememberTextMeasurer()
    val locale = currentLocale
    val textStyle = PersonaTheme.type.micro.copy(color = axis)
    val labels = if (axes) listOf(maximum, maximum / 2, 0.0).map { measure.measure(kinds.first().format(it, locale), textStyle) } else emptyList()
    val right = labels.maxOfOrNull { it.size.width }?.toFloat()?.plus(10f) ?: 0f
    val bottom = if (axes) measure.measure("00:00", textStyle).size.height.toFloat() + 10f else 0f
    val latestSelect by rememberUpdatedState(onSelect)
    val input = if (!axes) Modifier else Modifier.pointerInput(start, span, right) {
        fun pick(x: Float) {
            val width = (size.width - right).coerceAtLeast(1f)
            latestSelect(start + (x / width).coerceIn(0f, 1f).times(span).toLong())
        }
        detectDragGesturesAfterLongPress(onDragStart = { pick(it.x) }, onDragEnd = {}, onDragCancel = {}) { change, _ ->
            change.consume(); pick(change.position.x)
        }
    }
    Canvas(modifier.then(input)) {
        val width = (size.width - right).coerceAtLeast(1f)
        val height = (size.height - bottom).coerceAtLeast(1f)
        fun x(t: Long) = ((t - start).toDouble() / span * width).toFloat()
        fun y(v: Double) = (height * (1 - v / maximum)).toFloat()
        if (axes) {
            labels.forEachIndexed { index, label ->
                val yy = height * index / 2
                drawLine(grid, Offset(0f, yy), Offset(width, yy), strokeWidth = 1f)
                drawText(label, topLeft = Offset(width + 8f, (yy - label.size.height / 2).coerceIn(0f, max(0f, height - label.size.height))))
            }
            // Text is measured with an explicit neutral color, independent of the app accent.
            repeat(3) { index ->
                val label = measure.measure(MetricFormat.time(start + span * index / 2, locale), textStyle)
                drawText(label, topLeft = Offset((width * index / 2 - label.size.width / 2).coerceIn(0f, max(0f, width - label.size.width)), height + 8f))
            }
        }
        // Clip the plot itself, leaving the neutral axes outside the clip.
        clipRect(0f, 0f, width, height) {
            reference?.let { drawLine(axis, Offset(0f, y(it)), Offset(width, y(it)), strokeWidth = 1.dp.toPx(),
                pathEffect = PathEffect.dashPathEffect(floatArrayOf(5.dp.toPx(), 4.dp.toPx()))) }
            runs.forEachIndexed { index, series ->
                val color = kinds[index].color(metricColors)
                series.forEach { run ->
                    val line = Path().apply {
                        run.forEachIndexed { i, point -> if (i == 0) moveTo(x(point.t), y(point.value)) else lineTo(x(point.t), y(point.value)) }
                    }
                    if (kinds.size == 1) {
                        val area = Path().apply { addPath(line); lineTo(x(run.last().t), height); lineTo(x(run.first().t), height); close() }
                        drawPath(area, color.copy(alpha = .1f))
                    }
                    drawPath(line, color, style = Stroke((if (axes) 2.dp else 1.5.dp).toPx(), cap = StrokeCap.Round))
                    if (run.size == 1) drawCircle(color, 2.dp.toPx(), Offset(x(run[0].t), y(run[0].value)))
                }
            }
            selection?.let { time ->
                drawLine(axis, Offset(x(time), 0f), Offset(x(time), height), 1.dp.toPx())
                selectedMetricPoint(points, time, step, kinds)?.let { point ->
                    kinds.forEach { kind -> kind.value(point)?.takeIf(Double::isFinite)?.let {
                        drawCircle(kind.color(metricColors), 4.dp.toPx(), Offset(x(point.t), y(it)))
                    } }
                }
            }
        }
    }
}
