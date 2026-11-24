package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.draw.clip
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.text.AnnotatedString
import persona.core.designsystem.icons.PhIcons

internal fun sidebarActionFill(palette: PersonaPalette): Color =
    if (palette.dark && palette.accent == Palettes.GlassDark.accent) Palettes.GlassLight.accent else palette.accent

@Composable
fun JSidebarNewChat(text: String, onClick: () -> Unit, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val compact = LocalHomeDrawer.current && PersonaTheme.style == AppStyle.Glass
    val fill = if (compact) sidebarActionFill(palette) else palette.accent
    CompositionLocalProvider(LocalPalette provides if (compact) palette.copy(action = fill, onAction = palette.onAccentFill(fill)) else palette) {
        JButton(text, onClick, modifier, icon = PhIcons.NotePencil)
    }
}

/** Keep the primary label whole when the paired controls need their own row. */
@Composable
fun JSidebarFooterActions(
    newChatLabel: String,
    onNewChat: () -> Unit,
    modifier: Modifier = Modifier,
    controls: @Composable RowScope.() -> Unit,
) {
    Layout(content = {
        JSidebarNewChat(newChatLabel, onNewChat, Modifier.fillMaxWidth())
        Row(Modifier.clip(CircleShape).background(PersonaTheme.palette.surfaceOpaque),
            verticalAlignment = Alignment.CenterVertically, content = controls)
    }, modifier = modifier.fillMaxWidth()) { children, constraints ->
        val width = constraints.maxWidth
        val gap = Tokens.Space.s.roundToPx()
        val childConstraints = constraints.copy(minWidth = 0, minHeight = 0)
        val paired = children[1].measure(childConstraints)
        val available = (width - paired.width - gap).coerceAtLeast(0)
        val stacked = children[0].maxIntrinsicWidth(paired.height) > available
        val primary = children[0].measure(childConstraints.copy(maxWidth = if (stacked) width else available))
        val height = if (stacked) primary.height + gap + paired.height else maxOf(primary.height, paired.height)
        layout(width, height) {
            primary.placeRelative(0, if (stacked) 0 else (height - primary.height) / 2)
            paired.placeRelative(width - paired.width, if (stacked) primary.height + gap else (height - paired.height) / 2)
        }
    }
}

internal fun sidebarHistoryRowSpins(status: Status, working: Boolean): Boolean =
    status == Status.Working || (working && status != Status.NeedsYou && status != Status.Unread)

/** Compact iPhone history is one title and a state mark, not a second conversation preview. */
@Composable
fun JSidebarHistoryRow(
    title: String,
    status: Status,
    selected: Boolean,
    onClick: (() -> Unit)?,
    onMenu: (() -> Unit)?,
    menuLabel: String,
    /** The line under the title (a leader conversation's open tasks, #4331); null: one line only. */
    hint: String? = null,
    /** A marked line under the title (the sentence a search found). Drawn instead of [hint] when set. */
    hintMark: AnnotatedString? = null,
    /** The title's quiet start ([JRow]'s `titleLead`): a hub title's day and kind. */
    titleLead: String? = null,
    /** A lead with anything at work under it while its own conversation is idle spins the working mark anyway (#6230, #6626). */
    working: Boolean = false,
    titleAccessory: (@Composable () -> Unit)? = null,
    /** Drawn before the ⋯ (a 常驻 lead's caret that folds its to-dos open, #4352); null: nothing. */
    trailingAccessory: (@Composable () -> Unit)? = null,
) {
    JRow(title, onClick = onClick, onLongClick = onMenu, selected = selected, chevron = false,
        subtitle = if (hintMark == null) hint else null, subtitleMark = hintMark,
        titleAccessory = titleAccessory, titleLead = titleLead, trailing = {
            // A Needs You dot is still, as iOS's `RowStateMark` in a dense list; a lead with work under it spins (#6230), as there.
            if (status == Status.NeedsYou || status == Status.Working || status == Status.Unread || working) JStatusDot(status, spinning = sidebarHistoryRowSpins(status, working), breathes = false)
            trailingAccessory?.invoke()
            onMenu?.let { JIconButton(PhIcons.DotsThree, menuLabel, it) }
        })
}
