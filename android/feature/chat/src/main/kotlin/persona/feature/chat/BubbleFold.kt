package persona.feature.chat

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.DisableSelection
import androidx.compose.material3.Text
import androidx.compose.material3.minimumInteractiveComponentSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.Constraints
import kotlin.math.floor
import kotlin.math.max
import kotlin.math.roundToInt
import persona.core.designsystem.JIcon
import persona.core.designsystem.LocalSelectionAbove
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SharedMotion
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

/**
 * When a user's message folds, aligned with iOS `BubbleFoldRule` (`apple/Persona/Views/Chat/BubbleFold.swift`, #5676):
 * past [limit] rendered lines it shows its first [shown] lines, the last visible line fading out, with Show more / Show
 * less inside the bubble. At accessibility text scales ([LARGE_FONT_SCALE] and above) ten lines outgrow a phone screen,
 * so the rule folds past 9 lines to 5.
 */
internal enum class BubbleFoldRule(val limit: Int, val shown: Int) {
    Regular(limit = 14, shown = 10),
    Large(limit = 9, shown = 5);

    companion object {
        /** Accessibility text scale threshold matching iOS `DynamicTypeSize.isAccessibilitySize`. */
        const val LARGE_FONT_SCALE = 1.35f

        /** Characters per line in the narrowest bubble (wide CJK in a narrow split) at 1x text scale. */
        const val NARROWEST = 9f

        /** Typical characters per line in a phone bubble at 1x text scale, used as the initial fold guess. */
        const val TYPICAL = 16f

        /** Maximum characters scanned when extracting a folded start slice. */
        const val START_CHARACTERS = 3000

        fun of(fontScale: Float): BubbleFoldRule = if (fontScale >= LARGE_FONT_SCALE) Large else Regular

        /** Approximate width of a Unicode code point in ems: CJK, kana, hangul, full-width and emoji take 1 em; Latin 0.4. */
        fun ems(codePoint: Int): Float = if (codePoint >= 0x2E80) 1f else 0.4f

        /** Characters per line at [fontScale], given [base] at default text size. */
        fun perLine(base: Float, fontScale: Float): Int = max(1, floor(base / max(fontScale, 1f)).toInt())

        /** Character count of each newline-delimited line in [text]. */
        fun lengths(text: String): List<Int> = text.split('\n').map { it.length }

        /** Estimated wrapped line count of [lengths] at [perLine] characters per line. */
        fun lines(lengths: List<Int>, perLine: Int): Int {
            val width = max(1, perLine)
            return lengths.sumOf { max(1, (it + width - 1) / width) }
        }

        /**
         * Short-circuiting check for whether [text] exceeds [limit] estimated wrapped lines at [perLine] characters per
         * line, avoiding `split('\n')` allocations on huge pasted logs.
         */
        private fun exceedsLines(text: String, perLine: Int, limit: Int): Boolean {
            val width = max(1, perLine)
            var total = 0
            var lineLen = 0
            for (i in 0 until text.length) {
                if (text[i] == '\n') {
                    total += max(1, (lineLen + width - 1) / width)
                    if (total > limit) return true
                    lineLen = 0
                } else {
                    lineLen++
                }
            }
            total += max(1, (lineLen + width - 1) / width)
            return total > limit
        }

        /** Fast-path gate: false means the message cannot exceed [limit] lines even in the narrowest bubble. */
        fun mayFold(text: String, fontScale: Float): Boolean {
            if (text.isEmpty()) return false
            val rule = of(fontScale)
            return exceedsLines(text, perLine(NARROWEST, fontScale), rule.limit)
        }

        /** Initial guess before the first layout measurement, avoiding an extra recomposition on long messages. */
        fun likelyFolds(text: String, fontScale: Float): Boolean {
            if (text.isEmpty()) return false
            val rule = of(fontScale)
            return exceedsLines(text, perLine(TYPICAL, fontScale), rule.limit)
        }

        /**
         * First [lines] lines of [text] wrapped at [ems] per line when provided, capped at [START_CHARACTERS];
         * null when the slice covers the entire string.
         */
        fun start(text: String, lines: Int, ems: Float? = null): String? {
            if (lines <= 0 || text.isEmpty()) return null
            var count = 0
            var breaks = 0
            var x = 0f
            var index = 0
            while (index < text.length) {
                if (count >= START_CHARACTERS) break
                val cp = text.codePointAt(index)
                val step = Character.charCount(cp)
                if (cp == '\n'.code) {
                    breaks++
                    x = 0f
                    if (breaks >= lines) break
                } else if (ems != null) {
                    val w = ems(cp)
                    if (x > 0f && x + w > ems) {
                        breaks++
                        x = 0f
                        if (breaks >= lines) break
                    }
                    x += w
                }
                count++
                index += step
            }
            return if (index >= text.length) null else text.substring(0, index)
        }
    }
}

/**
 * Folds long user bubble text past [BubbleFoldRule.limit] rendered lines down to [BubbleFoldRule.shown] lines with a
 * bottom-line alpha fade and an in-bubble Show more / Show less toggle.
 *
 * Short messages skip measurement completely; folded messages only pass a bounded prefix ([BubbleFoldRule.start],
 * capped at `limit + 2` lines and [BubbleFoldRule.START_CHARACTERS] chars) to [content] so huge pasted logs are never
 * fully laid out while folded; active in-place text selection ([LocalSelectionAbove]) always renders the full text so
 * every word remains selectable.
 */
@Composable
internal fun BubbleFold(
    text: String,
    foldKey: String?,
    own: Boolean,
    content: @Composable (shownText: String) -> Unit,
) {
    val selecting = LocalSelectionAbove.current
    val density = LocalDensity.current
    val fontScale = density.fontScale.coerceAtLeast(1f)
    if (foldKey.isNullOrBlank() || selecting || !BubbleFoldRule.mayFold(text, fontScale)) {
        content(text)
        return
    }
    val rule = BubbleFoldRule.of(fontScale)
    val palette = PersonaTheme.palette
    val bodyStyle = PersonaTheme.type.body
    val measurer = rememberTextMeasurer()
    val metrics = remember(bodyStyle, density, rule) {
        val sample = measurer.measure("A\nA", style = bodyStyle)
        val lineHeight = (sample.getLineBottom(0) - sample.getLineTop(0)).coerceAtLeast(1f)
        val stride = (sample.getLineTop(1) - sample.getLineTop(0)).coerceAtLeast(lineHeight)
        val shownHeight = (lineHeight + stride * (rule.shown - 1)).roundToInt().coerceAtLeast(1)
        val pastHeight = (lineHeight + stride * (rule.limit - 1) + lineHeight * 0.5f).roundToInt().coerceAtLeast(shownHeight + 1)
        BubbleLineMetrics(shownHeight = shownHeight, pastHeight = pastHeight, fadeHeight = lineHeight)
    }
    var open by rememberSaveable(foldKey) { mutableStateOf(false) }
    var whole by remember(foldKey, text, rule) { mutableStateOf(false) }
    var measuredLong by remember(foldKey, text, rule) { mutableStateOf(BubbleFoldRule.likelyFolds(text, fontScale)) }
    val start = remember(text, rule, open, whole) {
        if (!open && !whole) BubbleFoldRule.start(text, rule.limit + 2) else null
    }
    val shownText = start ?: text
    val folded = measuredLong && !open
    val revealAlpha by animateFloatAsState(
        targetValue = if (folded) 0f else 1f,
        animationSpec = if (PersonaTheme.reduceMotion) SharedMotion.instant() else SharedMotion.reveal(),
        label = "bubble-fold-reveal",
    )
    val maskInk = palette.ink
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Box(
            Modifier
                .clipToBounds()
                .bubbleFoldFade(
                    folded = folded,
                    revealAlpha = revealAlpha,
                    cutHeightPx = metrics.shownHeight.toFloat(),
                    fadeHeightPx = metrics.fadeHeight,
                    maskInk = maskInk,
                )
                .layout { measurable, constraints ->
                    val full = measurable.measure(constraints.copy(maxHeight = Constraints.Infinity))
                    val exceeds = full.height > metrics.pastHeight
                    if (start != null && !exceeds) {
                        // Only the bounded prefix was laid out and it did not exceed `limit`: draw whole instead.
                        if (!whole) whole = true
                    } else if (measuredLong != exceeds) {
                        measuredLong = exceeds
                    }
                    val targetHeight = if (exceeds && !open) minOf(full.height, metrics.shownHeight) else full.height
                    layout(full.width, targetHeight.coerceIn(constraints.minHeight, constraints.maxHeight)) {
                        full.placeRelative(0, 0)
                    }
                },
        ) {
            content(shownText)
        }
        if (measuredLong) {
            DisableSelection {
                val tint = if (own) palette.userBubbleText ?: palette.accent else palette.accent
                Row(
                    Modifier
                        .fillMaxWidth()
                        .minimumInteractiveComponentSize()
                        .clip(RoundedCornerShape(Tokens.Radius.control))
                        .clickable(role = Role.Button) { open = !open }
                        .padding(top = Tokens.Space.xs)
                        .testTag(if (open) "bubble-fold-collapse" else "bubble-fold-expand"),
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(
                        stringResource(if (open) R.string.chat_bubble_show_less else R.string.chat_bubble_show_more),
                        style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
                        color = tint,
                        maxLines = 1,
                    )
                    JIcon(
                        if (open) PersonaIcons.collapse else PersonaIcons.expand,
                        decorative = true,
                        size = Tokens.Size.markXS,
                        tint = tint,
                    )
                }
            }
        }
    }
}

private data class BubbleLineMetrics(val shownHeight: Int, val pastHeight: Int, val fadeHeight: Float)

/**
 * Fades the last visible line while folded and smoothly reveals the expanded region underneath on open.
 * At rest when expanded ([revealAlpha] >= 1f), no offscreen compositing layer is used.
 */
private fun Modifier.bubbleFoldFade(
    folded: Boolean,
    revealAlpha: Float,
    cutHeightPx: Float,
    fadeHeightPx: Float,
    maskInk: Color,
): Modifier {
    val active = folded || revealAlpha < 1f
    if (!active) return this
    return this
        .graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }
        .drawWithContent {
            drawContent()
            if (size.height <= 0f) return@drawWithContent
            val solid = maskInk.copy(alpha = 1f)
            val tailAlpha = if (folded) 0f else revealAlpha.coerceIn(0f, 1f)
            val tail = maskInk.copy(alpha = tailAlpha)
            val fadeEnd = (cutHeightPx / size.height).coerceIn(0f, 1f)
            val fadeStart = ((cutHeightPx - fadeHeightPx) / size.height).coerceIn(0f, fadeEnd)
            val brush = if (fadeEnd >= 1f) {
                Brush.verticalGradient(0f to solid, fadeStart to solid, 1f to tail)
            } else {
                Brush.verticalGradient(0f to solid, fadeStart to solid, fadeEnd to tail, 1f to tail)
            }
            drawRect(brush, blendMode = BlendMode.DstIn)
        }
}
