package persona.core.designsystem

import androidx.compose.animation.core.AnimationSpec
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.animateScrollBy
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import kotlinx.coroutines.flow.first
import persona.core.designsystem.icons.PersonaIcons

/** How much each edge of the status strip fades, from 0 (not at all) to 1 (in full): the drawn state, as the tests read it. */
internal data class AgentFilterFades(val leading: Float, val trailing: Float)
internal val AgentFilterFade = SemanticsPropertyKey<AgentFilterFades>("AgentFilterFade")

/**
 * Final E: only status choices scroll; grouping has one fixed, anchored menu at the trailing edge.
 * Every label shows whole, whatever the language or text size (#9121): a strip that does not fit scrolls, and an edge with
 * more hidden past it fades, the trailing one while choices lie beyond it and the leading one once the strip has moved from
 * its start. Counts stay beside their label and the chosen choice keeps its highlight. A choice is brought to the middle
 * of the strip when it is made; one wider than what lies between the fades goes to where that begins, so its first word
 * shows. Reduce Motion moves and fades at once.
 */
@Composable
fun JAgentFilterCapsule(
    bays: List<Bay>, selected: Int, onSelect: (Int) -> Unit,
    groupLabel: String, groupActions: List<JAction>, modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val surface = if (PersonaTheme.style == AppStyle.Glass && !palette.dark) Tokens.Semantic.Glass.Light.pageBackground else palette.surfaceOpaque
    val textScale = LocalDensity.current.fontScale.coerceAtLeast(1f)
    val visualHeight = AgentFilterTokens.height * textScale
    val fade = AgentFilterTokens.fade * textScale
    val state = rememberLazyListState()
    var menu by remember { mutableStateOf(false) }
    val reduceMotion = PersonaTheme.reduceMotion
    val fadeSpec: AnimationSpec<Float> = if (reduceMotion) snap() else tween(Tokens.Motion.State.duration)
    // Read in drawing and semantics only: the fade's frames redraw the strip and never recompose it.
    val leading = animateFloatAsState(if (state.canScrollBackward) 1f else 0f, fadeSpec, label = "filter-fade-leading")
    val trailing = animateFloatAsState(if (state.canScrollForward) 1f else 0f, fadeSpec, label = "filter-fade-trailing")
    var placed by remember { mutableStateOf(false) }
    val fadePx = with(LocalDensity.current) { fade.toPx() }
    LaunchedEffect(selected) {
        if (selected !in bays.indices) return@LaunchedEffect
        snapshotFlow { state.layoutInfo.visibleItemsInfo.isNotEmpty() }.first { it }
        withFrameNanos { }
        // A strip built anew starts at its first choice, and the page may have kept another: that one is placed at once.
        val first = !placed
        placed = true
        state.place(selected, fadePx, if (first || reduceMotion) null else tween(Tokens.Motion.State.duration))
    }
    Box(modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)) {
        Box(Modifier.align(Alignment.Center).fillMaxWidth().height(visualHeight)
            .clip(CircleShape).background(surface)
            .border(AgentFilterTokens.stroke, palette.line, CircleShape))
        Row(Modifier.align(Alignment.Center).fillMaxWidth().heightIn(min = Tokens.MinTouch.android).clip(CircleShape),
            verticalAlignment = Alignment.CenterVertically) {
            LazyRow(
                state = state,
                modifier = Modifier.weight(1f).testTag("agents-status-scroll")
                    .semantics { this[AgentFilterFade] = AgentFilterFades(leading.value, trailing.value) }
                    .agentFilterEdgeFade(surface, fade, { leading.value }, { trailing.value }),
                contentPadding = PaddingValues(horizontal = AgentFilterTokens.inset),
                horizontalArrangement = Arrangement.spacedBy(AgentFilterTokens.gap),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                items(bays.size, key = { bays[it].label }) { index ->
                    val bay = if (index == 0 && PersonaTheme.style == AppStyle.Glass) bays[index].copy(count = null) else bays[index]
                    CapsuleChoice(bay, selected == index, Modifier.testTag("agents-status-$index")) { onSelect(index) }
                }
            }
            Box(Modifier.width(AgentFilterTokens.stroke).height(AgentFilterTokens.separatorHeight).background(palette.line))
            Box(Modifier.width(AgentFilterTokens.groupWidth).heightIn(min = Tokens.MinTouch.android)
                .testTag("agents-grouping").clickable(role = Role.Button) { menu = true }
                .semantics { contentDescription = groupLabel }, contentAlignment = Alignment.Center) {
                JIcon(PersonaIcons.filter, null, size = AgentFilterTokens.icon, tint = palette.ink, decorative = true)
                JMenu(menu, { menu = false }, groupActions)
            }
        }
    }
}

/**
 * Fades what the strip shows into [surface] over [fade] at an edge with more hidden past it: [leading] and [trailing] are
 * how much each edge fades, 0 (not at all) to 1 (to nothing at the very edge). They are read in drawing only.
 */
internal fun Modifier.agentFilterEdgeFade(surface: Color, fade: Dp, leading: () -> Float, trailing: () -> Float): Modifier = this
    .graphicsLayer {
        compositingStrategy = if (leading() > 0f || trailing() > 0f) CompositingStrategy.Offscreen else CompositingStrategy.Auto
    }
    .drawWithContent {
        drawContent()
        val head = leading()
        val tail = trailing()
        if ((head > 0f || tail > 0f) && size.width > 0f) {
            val edge = (fade.toPx() / size.width).coerceIn(0f, 0.5f)
            // Only the alpha counts: DstIn keeps the content as opaque as the mask is.
            drawRect(Brush.horizontalGradient(0f to surface.copy(alpha = 1f - head), edge to surface,
                (1f - edge) to surface, 1f to surface.copy(alpha = 1f - tail)), blendMode = BlendMode.DstIn)
        }
    }

/** One choice: its dot or icon, its whole label and its count. The strip gives it all the width it asks for, so nothing is cut. */
@Composable
private fun CapsuleChoice(bay: Bay, selected: Boolean, modifier: Modifier, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type.callout.copy(fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium)
    Row(modifier.heightIn(min = Tokens.MinTouch.android).widthIn(min = Tokens.MinTouch.android).clip(CircleShape)
        .clickable(role = Role.Tab, onClick = onClick)
        .semantics(mergeDescendants = true) {
            this.selected = selected
            contentDescription = listOfNotNull(bay.label, bay.count?.toString()).joinToString(" ")
        }
        .padding(vertical = Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
        Row(Modifier.heightIn(min = AgentFilterTokens.chipHeight).clip(CircleShape)
            .background(if (selected) palette.accent.copy(alpha = AgentFilterTokens.selectedFillAlpha) else palette.surfaceOpaque.copy(alpha = 0f))
            .padding(horizontal = AgentFilterTokens.chipPadding), verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(AgentFilterTokens.contentGap)) {
            bay.icon?.let { JIcon(it, null, size = AgentFilterTokens.dot, tint = palette.status(bay.status ?: Status.Working, bay.waiting).dot, decorative = true) }
                ?: bay.status?.let {
                    Box(Modifier.size(AgentFilterTokens.dot).clip(CircleShape).background(palette.status(it, bay.waiting).dot))
                }
            // The chip speaks as one (its description, above), so the words it draws are no second node for a screen reader.
            Box(Modifier.clearAndSetSemantics { }) {
                Text(bay.label, style = type, color = if (selected) palette.accent else palette.ink, maxLines = 1, softWrap = false)
            }
            bay.count?.let { Text("$it", style = type, color = if (selected) palette.accent else palette.ink2,
                maxLines = 1, softWrap = false, modifier = Modifier.clearAndSetSemantics { }) }
        }
    }
}

/**
 * Moves the choice at [index] to the middle of the strip. A choice wider than what lies between the two fades ([fade] wide
 * each) goes to where that begins instead, so the first word of a long label is not dimmed. [spec] animates it; null moves
 * at once. The strip stops at its ends by itself.
 */
private suspend fun LazyListState.place(index: Int, fade: Float, spec: AnimationSpec<Float>?) {
    if (layoutInfo.visibleItemsInfo.none { it.index == index }) scrollToItem(index)
    val layout = layoutInfo
    val item = layout.visibleItemsInfo.firstOrNull { it.index == index } ?: return
    val delta = if (item.size > layout.viewportSize.width - 2 * fade) item.offset - (fade - layout.beforeContentPadding)
        else item.offset + item.size / 2f - (layout.viewportStartOffset + layout.viewportEndOffset) / 2f
    if (spec == null) scrollBy(delta) else animateScrollBy(delta, spec)
}

/**
 * Glass Inbox section capsule (iOS `CapsuleFilterBar` in `InboxView.swift`): pins the first choice ("Needs you") at the
 * leading edge so its count stays visible, scrolls the remaining choices with edge fades and reveals the selected choice
 * automatically, uses soft tinted selection fill instead of a solid dark/white slab, and hosts an optional inline
 * grouping menu at the trailing end.
 */
@Composable
fun JInboxSectionCapsule(
    bays: List<Bay>,
    selected: Int,
    onSelect: (Int) -> Unit,
    groupLabel: String,
    groupActions: List<JAction>,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val surface = if (PersonaTheme.style == AppStyle.Glass && !palette.dark) Tokens.Semantic.Glass.Light.pageBackground else palette.surfaceOpaque
    val textScale = LocalDensity.current.fontScale.coerceAtLeast(1f)
    val visualHeight = AgentFilterTokens.height * textScale
    val fadeWidth = AgentFilterTokens.fade * textScale
    val scroll = rememberScrollState()
    var menu by remember { mutableStateOf(false) }
    val reduceMotion = PersonaTheme.reduceMotion
    val fadeSpec: AnimationSpec<Float> = if (reduceMotion) SharedMotion.instant() else SharedMotion.reveal()
    val measured = scroll.viewportSize > 0
    val leading = animateFloatAsState(if (measured && scroll.canScrollBackward) 1f else 0f, fadeSpec, label = "inbox-capsule-leading")
    val trailing = animateFloatAsState(if (measured && scroll.canScrollForward) 1f else 0f, fadeSpec, label = "inbox-capsule-trailing")
    val fading by remember { derivedStateOf { leading.value > 0f || trailing.value > 0f } }
    val pinned = if (bays.size > 1) 1 else 0
    val fadeModifier = if (fading) Modifier.agentFilterEdgeFade(surface, fadeWidth, { leading.value }, { trailing.value }) else Modifier

    @Composable
    fun choiceChip(index: Int) {
        val bay = bays[index]
        val isSelected = index == selected
        val spoken = bay.countTone?.let { spokenCount(bay.label, bay.count) }
        val type = PersonaTheme.type.callout.copy(fontWeight = if (isSelected) FontWeight.SemiBold else FontWeight.Medium)
        Row(
            Modifier
                .revealWhenSelected(scroll.takeIf { index >= pinned }, isSelected, fadeWidth)
                .heightIn(min = Tokens.MinTouch.android)
                .clip(CircleShape)
                .selectable(selected = isSelected, role = Role.Tab, onClick = { onSelect(index) })
                .semantics(mergeDescendants = true) {
                    spoken?.let { contentDescription = it }
                }
                .padding(vertical = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Row(
                Modifier
                    .heightIn(min = AgentFilterTokens.chipHeight)
                    .clip(CircleShape)
                    .background(if (isSelected) palette.accent.copy(alpha = AgentFilterTokens.selectedFillAlpha) else palette.surfaceOpaque.copy(alpha = 0f))
                    .padding(horizontal = AgentFilterTokens.chipPadding),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(AgentFilterTokens.contentGap),
            ) {
                Text(
                    bay.label,
                    style = type,
                    color = if (isSelected) palette.accent else palette.ink,
                    maxLines = 1,
                    softWrap = false,
                )
                val count = bay.count
                val tone = bay.countTone
                if (count != null && tone != null) {
                    JToneCount(count, tone, onFill = false)
                }
            }
        }
    }

    Box(modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)) {
        Box(
            Modifier
                .align(Alignment.Center)
                .fillMaxWidth()
                .height(visualHeight)
                .clip(CircleShape)
                .background(surface)
                .border(AgentFilterTokens.stroke, palette.line, CircleShape),
        )
        Row(
            Modifier
                .align(Alignment.Center)
                .fillMaxWidth()
                .heightIn(min = Tokens.MinTouch.android)
                .clip(CircleShape),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (pinned > 0) {
                Box(Modifier.padding(start = AgentFilterTokens.inset)) { choiceChip(0) }
                Row(
                    Modifier
                        .weight(1f)
                        .then(fadeModifier)
                        .horizontalScroll(scroll)
                        .padding(start = AgentFilterTokens.gap, end = AgentFilterTokens.inset),
                    horizontalArrangement = Arrangement.spacedBy(AgentFilterTokens.gap),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    (pinned until bays.size).forEach { choiceChip(it) }
                }
            } else {
                Row(
                    Modifier
                        .weight(1f)
                        .then(fadeModifier)
                        .horizontalScroll(scroll)
                        .padding(horizontal = AgentFilterTokens.inset),
                    horizontalArrangement = Arrangement.spacedBy(AgentFilterTokens.gap),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    bays.indices.forEach { choiceChip(it) }
                }
            }
            if (groupActions.isNotEmpty()) {
                Box(Modifier.width(AgentFilterTokens.stroke).height(AgentFilterTokens.separatorHeight).background(palette.line))
                Box(
                    Modifier
                        .width(AgentFilterTokens.groupWidth)
                        .heightIn(min = Tokens.MinTouch.android)
                        .testTag("inbox-grouping")
                        .clickable(role = Role.Button) { menu = true }
                        .semantics { contentDescription = groupLabel },
                    contentAlignment = Alignment.Center,
                ) {
                    JIcon(PersonaIcons.filter, null, size = AgentFilterTokens.icon, tint = palette.ink, decorative = true)
                    JMenu(menu, { menu = false }, groupActions)
                }
            }
        }
    }
}
