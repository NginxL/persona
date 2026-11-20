package persona.core.designsystem

import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.compositeOver
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight

/**
 * The Glass Agents home uses the iPhone's grouped page colors on a phone. On larger windows the repository list has
 * the workspace tint. Each repository draws its own card ([LocalRepoCard]).
 */
@Composable
fun JAgentHomeSurface(repository: Boolean, content: @Composable () -> Unit) {
    val palette = PersonaTheme.palette
    val home = LocalHomeChrome.current != null && PersonaTheme.style == AppStyle.Glass && repository
    val window = LocalWindowLayout.current
    val compact = !window.expanded && !window.tabletop
    val page = when {
        compact && palette.dark -> palette.background
        compact -> Tokens.Semantic.Glass.Light.card
        palette.dark -> HomeTokens.repositoryDark
        else -> HomeTokens.repositoryLight
    }
    val repoCard = when {
        compact && palette.dark -> palette.surfaceOpaque
        palette.dark -> palette.sunken.compositeOver(page)
        else -> palette.background
    }
    CompositionLocalProvider(
        LocalPalette provides if (home) palette.copy(background = page) else palette,
        LocalRepoCard provides repoCard.takeIf { home },
    ) { content() }
}

/** The iPhone's filters wrap into another line instead of adding a horizontal navigation strip. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun HomeAgentFilters(bays: List<Bay>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier) {
    if (LocalHomeChrome.current == null || PersonaTheme.style != AppStyle.Glass) {
        QuickFilterBar(bays, selected, onSelect, modifier)
        return
    }
    val palette = PersonaTheme.palette
    FlowRow(modifier.padding(horizontal = Tokens.Space.l),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        bays.forEachIndexed { index, bay ->
            val chosen = index == selected
            // The chosen chip takes its own state's color, the accent for "All" (iOS `QuickFilterBar`, #4338 A11); Waiting in
            // the background its lighter tone, whose words are the secondary gray. Like iOS's opacity, the fill and edge scale
            // the tint's own alpha.
            val tone = bay.status?.let { palette.status(it, bay.waiting) }
            val tint = tone?.text ?: palette.accent
            Row(Modifier.clip(CircleShape)
                .background(if (chosen) tint.copy(alpha = tint.alpha * HomeTokens.filterSelectedAlpha) else palette.sunken)
                .border(Tokens.Space.Standard.xxs / 2, if (chosen) tint.copy(alpha = tint.alpha * HomeTokens.filterStrokeAlpha) else Color.Transparent, CircleShape)
                .clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = chosen }
                .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                bay.icon?.let { icon -> tone?.let { JIcon(icon, null, size = Tokens.Size.iconXS, tint = it.dot, selected = true, decorative = true) } }
                    ?: bay.status?.let { JStatusDot(it, waiting = bay.waiting, breathes = false) } // still, as iOS's chips
                Text(bay.label, style = PersonaTheme.type.callout.copy(fontWeight = if (chosen) FontWeight.SemiBold else FontWeight.Medium),
                    color = if (chosen) tint else palette.ink, maxLines = 1, softWrap = false)
                if (index != 0) bay.count?.let { Text("$it", style = PersonaTheme.type.callout, color = if (chosen) tint else palette.ink2, maxLines = 1) }
            }
        }
    }
}
