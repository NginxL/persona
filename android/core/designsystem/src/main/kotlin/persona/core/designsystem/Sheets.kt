package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.getValue
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.DpOffset
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcon
import kotlinx.coroutines.launch

/** A [JMenu]'s group title ([JAction.section]): lined up with the items' labels. */
object MenuTokens {
    /** A menu item's side padding (Material's), which a group's title shares. */
    val itemPadding = 12.dp
    /** Above and below a group's title. */
    val sectionPadding = 6.dp
    /** A disabled choice's subtitle, fainter than its label. */
    const val disabledSubtitle = 0.7f
}

/** [JActionSheet]'s preview: as many lines as iOS's conversation preview shows (`AssistantSidebar.row`). */
object ActionSheetTokens {
    const val previewLines = 6
}

/** One action in a menu or action sheet. */
@Immutable
data class JAction(
    val label: String,
    val icon: PhIcon? = null,
    val destructive: Boolean = false,
    val enabled: Boolean = true,
    val checked: Boolean = false,
    /** Draw a rule above this action, to group menus. */
    val separatorBefore: Boolean = false,
    /** A quiet line under the label (how many a choice would show: "20 to-dos"). */
    val subtitle: String? = null,
    /** A group's title drawn above this action, with a rule above it unless it is the first: "Show", "Sort by". */
    val section: String? = null,
    val onClick: () -> Unit,
)

/** A bottom sheet in the style's shape and surface. Ordinary sheets are opaque; adaptive Settings may clear the full-window container. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun JSheet(
    onDismiss: () -> Unit,
    modifier: Modifier = Modifier,
    title: String? = null,
    skipPartiallyExpanded: Boolean = true,
    /** False while an operation must finish (the sheet ignores drags, the scrim and back). */
    dismissible: Boolean = true,
    /** Called when a drag, the scrim or back tries to close a sheet that is not [dismissible] (to ask about unsaved edits). */
    onBlockedDismiss: (() -> Unit)? = null,
    /** Scroll the whole content; leave false when the content has its own lazy list. */
    scrollable: Boolean = false,
    contentPadding: androidx.compose.ui.unit.Dp? = null,
    bottomPadding: androidx.compose.ui.unit.Dp? = null,
    /** Leave the window-sized container transparent so a page-sized Settings card can supply the only opaque surface. */
    clearContainer: Boolean = false,
    /** The page-sized Settings frame already draws the window scrim; do not blend a second one over it. */
    clearScrim: Boolean = false,
    /** Marks an intentional blank-tap focus clear, distinct from a presentation-driven focus transfer. */
    onClearFocus: () -> Unit = {},
    showDragHandle: Boolean = true,
    content: @Composable ColumnScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val canDismiss by androidx.compose.runtime.rememberUpdatedState(dismissible)
    val blocked by androidx.compose.runtime.rememberUpdatedState(onBlockedDismiss)
    // Brief's surface is its card grey, the fill of its text fields and chips: on a grey sheet they vanish (no border
    // either), so its sheets are paper like its pages.
    val container = when {
        clearContainer -> palette.surfaceOpaque.copy(alpha = 0f)
        PersonaTheme.style == AppStyle.Brief -> palette.background
        else -> palette.surfaceOpaque
    }
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = skipPartiallyExpanded, confirmValueChange = {
        (it != SheetValue.Hidden || canDismiss).also { ok -> if (!ok) blocked?.invoke() }
    })
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    val shortcutHelpOwner = androidx.compose.runtime.remember { Any() }
    val densities = currentTextDensities()
    ModalBottomSheet(
        onDismissRequest = { if (canDismiss) onDismiss() }, modifier = modifier,
        sheetState = sheetState,
        shape = PersonaTheme.shapes.sheetShape, containerColor = container, contentColor = palette.ink,
        scrimColor = if (clearScrim) palette.scrim.copy(alpha = 0f) else palette.scrim, tonalElevation = 0.dp,
        dragHandle = if (!showDragHandle) null else ({ Box(Modifier.padding(top = 10.dp.dense, bottom = 6.dp.dense).size(36.dp, 4.dp).clip(CircleShape).background(palette.ink3.copy(alpha = 0.5f))) }),
    ) {
        RetainedTextDensity(densities) {
        InterfaceContent {
        // Opaque over a scrim: glass inside the sheet must not sample (and show through) the page behind it.
        androidx.compose.runtime.CompositionLocalProvider(
            LocalReadingInset provides 0.dp,
            LocalGlassSource provides null,
            LocalSplitDetail provides null,
            LocalHardwareShortcutHelpOwner provides shortcutHelpOwner,
        ) {
        Column(Modifier.fillMaxWidth().hardwareShortcutDismiss({
            if (canDismiss) onDismiss() else blocked?.invoke()
        }, shortcutHelpOwner).dismissKeyboardOnBlankTap(onClearFocus)
            // Content that grows once the sheet is open ("Session info" after it loads) would stay where the opening left the
            // sheet, its rest below the screen and out of reach: settle the sheet again at its new height.
            .onSizeChanged {
                if (skipPartiallyExpanded && sheetState.targetValue == SheetValue.Expanded) scope.launch { sheetState.expand() }
            }
            .then(if (scrollable) Modifier.verticalScroll(androidx.compose.foundation.rememberScrollState()) else Modifier).padding(horizontal = contentPadding ?: pagePadding).padding(bottom = bottomPadding ?: 16.dp.dense)) {
            title?.let {
                Text(it, style = PersonaTheme.type.title, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Spacer(Modifier.height(12.dp.dense))
            }
            content()
        }
        }
        }
        }
    }
    HardwareShortcutHelp(shortcutHelpOwner)
}

/**
 * A long-press or overflow menu as a bottom sheet: easy to reach, room for a note under the actions. [preview] is what
 * the thing pressed last said, under the [title] and above the actions, with [previewCaption] (when) under it: iOS's
 * context-menu preview of a conversation (`AssistantSidebar.row`).
 */
@Composable
fun JActionSheet(
    actions: List<JAction>, onDismiss: () -> Unit, title: String? = null, note: String? = null,
    preview: String? = null, previewCaption: String? = null,
) {
    val palette = PersonaTheme.palette
    // When some actions have an icon, the others keep its place, so every label starts in one column.
    val iconColumn = actions.any { it.icon != null }
    JSheet(onDismiss, title = title) {
        if (preview != null || previewCaption != null) {
            Column(Modifier.padding(horizontal = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                preview?.let { Text(it, style = PersonaTheme.type.callout, color = palette.ink2, maxLines = ActionSheetTokens.previewLines, overflow = TextOverflow.Ellipsis) }
                previewCaption?.let { Text(it, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1) }
            }
            JDivider(Modifier.padding(vertical = Tokens.Space.s))
        }
        Column(Modifier.weight(1f, fill = false).verticalScroll(androidx.compose.foundation.rememberScrollState())) {
        actions.forEachIndexed { index, action ->
            if (action.separatorBefore || (action.section != null && index > 0)) JDivider(Modifier.padding(vertical = 6.dp.dense))
            // A group's title, as [JMenu] draws it (the Agents page's Display), over its labels' column.
            action.section?.let {
                Text(it, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(start = Tokens.Space.s + if (iconColumn) Tokens.Size.iconS + Tokens.Space.l else Tokens.Space.flush,
                        end = Tokens.Space.s, top = MenuTokens.sectionPadding, bottom = MenuTokens.sectionPadding).semantics { heading() })
            }
            Row(
                Modifier.fillMaxWidth().heightIn(min = 52.dp.row).clip(PersonaTheme.shapes.controlShape)
                    .clickable(enabled = action.enabled, role = Role.Button) { onDismiss(); action.onClick() }
                    .stableAlpha(if (action.enabled) 1f else 0.4f).padding(horizontal = 8.dp.dense),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                val color = if (action.destructive) palette.danger.text else palette.ink
                val icon = action.icon
                if (icon != null) { JIcon(icon, null, size = 22.dp, tint = if (action.destructive) palette.danger.dot else palette.ink2); Spacer(Modifier.width(16.dp.dense)) }
                else if (iconColumn) Spacer(Modifier.width(22.dp + 16.dp))
                Text(action.label, style = PersonaTheme.type.body, color = color, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                if (action.checked) JIcon(persona.core.designsystem.icons.PhIcons.Check, null, size = 18.dp, tint = palette.ink)
            }
        }
        }
        note?.let {
            Spacer(Modifier.height(8.dp.dense))
            Text(it, style = PersonaTheme.type.caption, color = palette.ink3, modifier = Modifier.padding(horizontal = 8.dp.dense))
        }
    }
}

/**
 * A small anchored menu for a few choices next to the control that opened it. A tap outside closes it; what does not
 * fit the window scrolls. [content] goes above the [actions], with a rule between them: choices that are more than a
 * label (the sidebar's assistant switcher lists its assistants there, then New assistant and Manage assistants…).
 */
@Composable
fun JMenu(
    expanded: Boolean, onDismiss: () -> Unit, actions: List<JAction>, modifier: Modifier = Modifier, offset: DpOffset = DpOffset(0.dp, 4.dp),
    /** Wrap long labels and subtitles instead of losing their meaning at accessibility text sizes. */
    multilineActions: Boolean = false,
    content: (@Composable ColumnScope.() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val densities = currentTextDensities()
    DropdownMenu(
        expanded, onDismiss, modifier, offset = offset, shape = PersonaTheme.shapes.controlShape,
        containerColor = palette.surfaceOpaque, tonalElevation = 0.dp, shadowElevation = 12.dp,
    ) {
        RetainedTextDensity(densities) {
        InterfaceContent {
        content?.let {
            it()
            if (actions.isNotEmpty()) HorizontalDivider(color = palette.line)
        }
        actions.forEachIndexed { index, action ->
            // [content] draws its own rule under it
            if (action.separatorBefore || (action.section != null && index > 0)) HorizontalDivider(color = palette.line)
            action.section?.let {
                Text(it, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(horizontal = MenuTokens.itemPadding, vertical = MenuTokens.sectionPadding).semantics { heading() })
            }
            DropdownMenuItem(
                text = {
                    // a disabled choice reads as one (its own colors would hide Material's)
                    val ink = when {
                        !action.enabled -> palette.ink3
                        action.destructive -> palette.danger.text
                        else -> palette.ink
                    }
                    val label = @Composable {
                        Text(action.label, style = PersonaTheme.type.body, color = ink,
                            maxLines = if (multilineActions) Int.MAX_VALUE else 1,
                            overflow = if (multilineActions) TextOverflow.Clip else TextOverflow.Ellipsis)
                    }
                    if (action.subtitle == null) label()
                    else Column {
                        label()
                        Text(action.subtitle, style = PersonaTheme.type.caption, color = if (action.enabled) palette.ink3 else palette.ink3.copy(alpha = MenuTokens.disabledSubtitle),
                            maxLines = if (multilineActions) Int.MAX_VALUE else 1,
                            overflow = if (multilineActions) TextOverflow.Clip else TextOverflow.Ellipsis)
                    }
                },
                leadingIcon = action.icon?.let { { JIcon(it, null, size = 20.dp, tint = if (!action.enabled) palette.ink3 else if (action.destructive) palette.danger.dot else palette.ink2) } },
                trailingIcon = if (action.checked) ({ JIcon(persona.core.designsystem.icons.PhIcons.Check, null, size = 18.dp, tint = palette.ink) }) else null,
                enabled = action.enabled, onClick = { onDismiss(); action.onClick() },
            )
        }
        }
        }
    }
}

/** A confirmation dialog. [confirm] is the primary action; mark it [destructive] for irreversible ones. */
@Composable
fun JDialog(
    onDismiss: () -> Unit,
    title: String,
    text: String? = null,
    confirm: Pair<String, () -> Unit>? = null,
    dismiss: Pair<String, () -> Unit>? = stringResource(R.string.designsystem_cancel) to onDismiss,
    destructive: Boolean = false,
    confirmEnabled: Boolean = true,
    confirmLoading: Boolean = false,
    dismissEnabled: Boolean = !confirmLoading,
    content: (@Composable ColumnScope.() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val shortcutHelpOwner = androidx.compose.runtime.remember { Any() }
    val densities = currentTextDensities()
    val dismissRequest: () -> Unit = if (!dismissEnabled) ({}) else onDismiss
    androidx.compose.runtime.CompositionLocalProvider(
        LocalReadingInset provides 0.dp,
        LocalHardwareShortcutHelpOwner provides shortcutHelpOwner,
    ) {
    AlertDialog(
        onDismissRequest = dismissRequest, modifier = Modifier.hardwareShortcutDismiss(dismissRequest, shortcutHelpOwner),
        containerColor = palette.surfaceOpaque, shape = PersonaTheme.shapes.panelShape, tonalElevation = 0.dp,
        title = { RetainedTextDensity(densities) { InterfaceContent { Text(title, style = PersonaTheme.type.title, color = palette.ink) } } },
        text = if (text == null && content == null) null else ({
            RetainedTextDensity(densities) {
                InterfaceContent {
                    Column {
                        text?.let { Text(it, style = PersonaTheme.type.callout, color = palette.ink2) }
                        content?.let { Spacer(Modifier.height(if (text == null) 0.dp else 12.dp)); it() }
                    }
                }
            }
        }),
        confirmButton = {
            RetainedTextDensity(densities) {
                InterfaceContent {
                    confirm?.let { (label, action) ->
                        JButton(label, action, kind = if (destructive) JButtonKind.Destructive else JButtonKind.Primary,
                            size = JButtonSize.Medium, enabled = confirmEnabled, loading = confirmLoading)
                    }
                }
            }
        },
        dismissButton = dismiss?.let { (label, action) -> {
            RetainedTextDensity(densities) { InterfaceContent { JButton(label, action, kind = JButtonKind.Quiet, size = JButtonSize.Medium, enabled = dismissEnabled) } }
        } },
    )
    HardwareShortcutHelp(shortcutHelpOwner)
    }
}

/** Translucent-safe colour for Material components that draw their own surfaces. */
internal val PersonaPalette.opaqueSurface: Color get() = surfaceOpaque
