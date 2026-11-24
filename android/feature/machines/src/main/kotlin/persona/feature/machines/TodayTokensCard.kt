package persona.feature.machines

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.MachineUsageCard
import persona.core.contracts.UsageTokens
import persona.core.designsystem.JCard
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.util.Locale
import kotlin.math.abs
import kotlin.math.pow
import kotlin.math.round
import kotlin.math.roundToInt

/** Formats a token count with K / M / B suffixes up to 3 significant digits (matching iOS `TokenFormat.count`). */
internal fun formatTokenCount(n: Long): String {
    val magnitude = abs(n).toDouble()
    val sign = if (n < 0) "-" else ""
    if (magnitude < 1_000.0) return "$sign${abs(n)}"
    val units = listOf(1e3 to "K", 1e6 to "M", 1e9 to "B")
    var idx = units.indexOfLast { magnitude >= it.first }.coerceAtLeast(0)
    while (true) {
        val x = magnitude / units[idx].first
        val digits = when {
            x >= 100.0 -> 0
            x >= 10.0 -> 1
            else -> 2
        }
        val scale = 10.0.pow(digits)
        val rounded = round(x * scale) / scale
        if (rounded >= 1000.0 && idx + 1 < units.size) {
            idx += 1
            continue
        }
        val formatted = String.format(Locale.US, "%.${digits}f", rounded)
            .let { s -> if ('.' in s) s.trimEnd('0').trimEnd('.') else s }
        return "$sign$formatted${units[idx].second}"
    }
}

/**
 * Computes segment widths so small shares keep [minimum] while larger shares absorb the difference, keeping the total
 * equal to [width] (matching iOS `ShareBar.widths`).
 */
internal fun computeShareBarWidths(values: List<Double>, width: Float, gap: Float, minimum: Float): List<Float> {
    if (values.isEmpty()) return emptyList()
    val count = values.size.toFloat()
    val room = (width - gap * (count - 1f)).coerceAtLeast(0f)
    if (minimum * count >= room) return List(values.size) { room / count }
    val widths = MutableList(values.size) { minimum }
    val free = values.indices.toMutableList()
    while (true) {
        val left = room - minimum * (values.size - free.size)
        val total = free.sumOf { values[it].coerceAtLeast(0.0) }
        val held = mutableListOf<Int>()
        for (i in free) {
            val w = if (total > 0.0) (left * (values[i].coerceAtLeast(0.0) / total)).toFloat() else left / free.size
            if (w < minimum) held += i else widths[i] = w
        }
        if (held.isEmpty()) return widths
        for (i in held) widths[i] = minimum
        free.removeAll(held)
    }
}

/** Share bar ink alpha for the i-th largest computer: darkest first (matching iOS `TodayTokensCard.shade`). */
internal fun todayTokensShade(index: Int): Float = (0.85f - index * 0.2f).coerceAtLeast(0.2f)

/**
 * Today's token usage summary card across the listed computers (matching iOS `TodayTokensCard` in `MachinesView.swift`):
 * total tokens, current tokens/min rate, overall cache hit rate, segmented share bar, and per-computer legend.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun TodayTokensCard(
    shares: List<Pair<String, MachineUsageCard>>,
    modifier: Modifier = Modifier,
) {
    if (shares.isEmpty()) return
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val today = shares.fold(UsageTokens()) { acc, (_, card) -> acc + card.today }
    val perMinute = shares.sumOf { (_, card) -> card.perMinute }
    val ranked = shares.filter { it.second.today.total > 0L }.sortedByDescending { it.second.today.total }
    val hitRateText = today.hitRate?.let { "${(it * 100.0).roundToInt()}%" } ?: "—"

    JCard(modifier.padding(bottom = Tokens.Space.m).testTag("machines-today-tokens"), status = null) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Text(
                stringResource(R.string.machines_tokens_today_title),
                style = type.headline,
                color = palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                TokenSummaryCell(
                    label = stringResource(R.string.machines_tokens_total),
                    value = formatTokenCount(today.total),
                    modifier = Modifier.weight(1f),
                )
                TokenSummaryCell(
                    label = stringResource(R.string.machines_tokens_now),
                    value = stringResource(R.string.machines_tokens_per_minute, formatTokenCount(perMinute)),
                    modifier = Modifier.weight(1f),
                )
                TokenSummaryCell(
                    label = stringResource(R.string.machines_tokens_cache_hit_rate),
                    value = hitRateText,
                    modifier = Modifier.weight(1f),
                )
            }
            if (today.total > 0L && ranked.isNotEmpty()) {
                val density = LocalDensity.current
                BoxWithConstraints(
                    Modifier
                        .fillMaxWidth()
                        .height(Tokens.Size.dotS)
                        .clearAndSetSemantics { },
                ) {
                    val gapPx = with(density) { Tokens.Space.xxs.toPx() }
                    val minPx = with(density) { Tokens.Size.dotS.toPx() }
                    val totalPx = with(density) { maxWidth.toPx() }
                    val widthsPx = computeShareBarWidths(ranked.map { it.second.today.total.toDouble() }, totalPx, gapPx, minPx)
                    Row(
                        Modifier.fillMaxWidth().fillMaxHeight(),
                        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
                    ) {
                        widthsPx.forEachIndexed { index, widthPx ->
                            val widthDp = with(density) { widthPx.toDp() }
                            Box(
                                Modifier
                                    .width(widthDp)
                                    .fillMaxHeight()
                                    .clip(CircleShape)
                                    .background(palette.ink.copy(alpha = todayTokensShade(index))),
                            )
                        }
                    }
                }
                FlowRow(
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                ) {
                    ranked.forEachIndexed { index, (name, usage) ->
                        Row(
                            Modifier.semantics(mergeDescendants = true) { },
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                        ) {
                            Box(
                                Modifier
                                    .size(Tokens.Size.dotS)
                                    .clip(CircleShape)
                                    .background(palette.ink.copy(alpha = todayTokensShade(index))),
                            )
                            Text(name, style = type.caption, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Text(
                                formatTokenCount(usage.today.total),
                                style = type.caption.copy(fontFeatureSettings = "tnum"),
                                color = palette.ink2,
                                maxLines = 1,
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun TokenSummaryCell(
    label: String,
    value: String,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(
        modifier.semantics(mergeDescendants = true) { },
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
    ) {
        Text(label, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Text(
            value,
            style = type.headline.copy(fontWeight = FontWeight.SemiBold, fontFeatureSettings = "tnum"),
            color = palette.ink,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}
