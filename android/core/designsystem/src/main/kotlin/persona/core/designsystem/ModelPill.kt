package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.material3.minimumInteractiveComponentSize
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.disabled
import androidx.compose.ui.semantics.role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import persona.core.designsystem.icons.PhIcons

/**
 * How much of the model pill the composer has room for (the web's container rules on the composer card, #5032): from 600
 * the whole pill; under it a narrower one whose model name gives way first; under 520 a step smaller; under 420 (a phone)
 * the provider's tile and the chevron alone. [AgentComposer] is the compact Agent toolbar's model and effort in words.
 */
enum class ModelPillWidth {
    Wide, Narrow, Small, Compact, AgentComposer;

    companion object {
        fun of(composerDp: Float): ModelPillWidth = when {
            composerDp >= WIDE -> Wide
            composerDp >= NARROW -> Narrow
            composerDp >= SMALL -> Small
            else -> Compact
        }
        private const val WIDE = 600f
        private const val NARROW = 520f
        private const val SMALL = 420f
    }
}

/**
 * What the model pill draws ([JModelPill]): the provider whose letter tile leads, the model's name (null: none known),
 * the engine's mark, the thinking effort in the pill's words, and the workflow mark while ultracode is on. While a
 * switch to another provider waits, [pending] is that provider: a clock comes first and the setup is the next message's.
 */
data class ModelPillContent(
    val provider: String,
    val model: String? = null,
    val engine: AgentMarkSpec? = null,
    val effort: String? = null,
    val ultracode: Boolean = false,
    val pending: Boolean = false,
    /** The words for a list being read with no model known yet ("Loading model…"); null: not loading. */
    val loading: String? = null,
    /** The model's list could not be read and no model is known: the warning in the danger color. */
    val failed: Boolean = false,
)

/** A provider's letter tile (the web's `ProviderMark`): its first letter in a small framed square, in the secondary ink. */
@Composable
fun JProviderTile(name: String, modifier: Modifier = Modifier, size: Dp = Tokens.Size.markS) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(Tokens.Space.xs)
    Box(modifier.size(size).clip(shape).border(Tokens.Size.line, palette.ink2, shape), contentAlignment = Alignment.Center) {
        Text(name.trim().take(1).uppercase(), style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold,
            fontSize = PersonaTheme.type.micro.fontSize * TILE_LETTER), color = palette.ink2, maxLines = 1)
    }
}

private const val TILE_LETTER = 0.85f

/**
 * The composer's model pill (#5055, the web's `ModelPill` from #5032; DESIGN_SYSTEM.md 3.8 "Model pill and unified model
 * panel"): `[tile][model][engine][effort][⌄]`, no frame at rest, a capsule on the pressed fill while its panel is [open].
 * The engine's mark is the separator (no "·"); the effort is in the text color, never its tier color. [width] is how much
 * room the composer gives ([ModelPillWidth]); on a phone it is the tile and the chevron. [onClick] null: a label, not a
 * button (nothing can be switched); not [enabled]: dimmed while a switch is on its way. The screen reader hears [spoken].
 */
@Composable
fun JModelPill(
    content: ModelPillContent,
    spoken: String,
    onClick: (() -> Unit)?,
    modifier: Modifier = Modifier,
    width: ModelPillWidth = ModelPillWidth.Narrow,
    open: Boolean = false,
    enabled: Boolean = true,
) {
    val palette = PersonaTheme.palette
    val agentComposer = width == ModelPillWidth.AgentComposer
    val small = width == ModelPillWidth.Small || width == ModelPillWidth.Compact
    val text = when {
        agentComposer -> PersonaTheme.type.callout
        small -> PersonaTheme.type.micro
        else -> PersonaTheme.type.caption
    }
    val mark = if (small) Tokens.Size.markXS else Tokens.Size.markS
    val chevron = if (small) Tokens.Size.markXS else Tokens.Size.markS
    Row(
        modifier.heightIn(min = Tokens.Size.wellXS)
            .then(if (onClick != null) Modifier.minimumInteractiveComponentSize() else Modifier)
            .clip(CircleShape)
            .then(if (open) Modifier.background(palette.surfaceStrong).border(Tokens.Size.line, palette.line, CircleShape) else Modifier)
            .then(if (onClick != null) Modifier.clickable(enabled = enabled, role = Role.Button, onClick = onClick) else Modifier)
            .clearAndSetSemantics {
                contentDescription = spoken
                if (onClick != null) role = Role.Button
                if (!enabled) disabled()
            }
            .stableAlpha(if (enabled) 1f else DISABLED)
            .padding(horizontal = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        if (content.pending) JIcon(PhIcons.Clock, null, size = mark, tint = palette.ink3)
        if (!agentComposer || content.model == null && content.loading == null) JProviderTile(content.provider, size = mark)
        when {
            width == ModelPillWidth.Compact -> {
                // only the tile: a spinner or the warning stays inside the compact pill
                if (content.loading != null) JSpinner(Modifier.size(mark))
                else if (content.failed) JIcon(PhIcons.Warning, null, size = mark, tint = palette.danger.dot)
            }
            content.loading != null -> {
                JSpinner(Modifier.size(mark))
                Text(content.loading, style = text, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            content.failed -> JIcon(PhIcons.Warning, null, size = mark, tint = palette.danger.dot)
            else -> {
                if (width == ModelPillWidth.Wide && content.pending)
                    Text(content.provider, Modifier.widthIn(max = Tokens.Size.box80), style = text, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                content.model?.let {
                    // the name gives way first: at most this much below the widest composer
                    Text(it, Modifier.weight(1f, fill = false).widthIn(max = if (width == ModelPillWidth.Wide) Tokens.Size.box150 * 2 else Tokens.Size.box120),
                        style = if (agentComposer) text.copy(fontWeight = FontWeight.SemiBold) else text,
                        color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                if (!agentComposer) content.engine?.let { JAgentMark(it, size = Tokens.Size.markXS) }
                content.effort?.let { Text(it, style = text, color = if (agentComposer) palette.ink2 else palette.ink, maxLines = 1, softWrap = false) }
                if (content.ultracode) JIcon(PhIcons.Stack, null, size = Tokens.Size.markXS, tint = palette.ink2)
            }
        }
        if (!agentComposer || content.model == null && content.loading == null)
            JIcon(PhIcons.CaretDown, null, size = chevron, tint = palette.ink2)
    }
}

private const val DISABLED = 0.5f

/**
 * The line across the chat where the replies' model changed (#5055, the web's `ModelChangeDivider` from #5034): a chip
 * "Opus 5.5 → Sonnet 4.5" between two hairlines, not a control. The screen reader hears [spoken].
 */
@Composable
fun JModelChangeDivider(text: String, spoken: String, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    androidx.compose.foundation.layout.BoxWithConstraints(modifier.fillMaxWidth().padding(vertical = Tokens.Space.s)
        .clearAndSetSemantics { contentDescription = spoken }) {
        // the chip is measured first, keeping a short stretch of line each side; the lines share what it leaves
        val chip = maxWidth - (Tokens.Space.l + Tokens.Space.m) * 2
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Box(Modifier.weight(1f).height(Tokens.Size.line).background(palette.line))
            Row(Modifier.widthIn(max = chip).clip(CircleShape).border(Tokens.Size.line, palette.line, CircleShape)
                .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm)) {
                JIcon(persona.core.designsystem.icons.PersonaIcons.model, null, size = Tokens.Size.markXS, tint = palette.ink3)
                Text(text, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Box(Modifier.weight(1f).height(Tokens.Size.line).background(palette.line))
        }
    }
}
