package persona.core.designsystem

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isMetaPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcons

// The phone composer grows to six lines, then scrolls inside and offers the full-screen editor.
// The same draft and caret are retained when expanding and collapsing.
const val ComposerGrowLines = 6

/**
 * Expand shows once the wrapped draft has more lines than the field shows. With the button the text wraps in a
 * narrower width (more lines), so the state it settles in never flips back by itself: it is stable both ways.
 */
fun composerOffersExpand(lineCount: Int, growLines: Int): Boolean = lineCount > growLines

/** Room the field's text leaves on its right for [ComposerExpandButton], only while the button shows. */
val ComposerExpandInset: Dp = 28.dp

/**
 * The small ↗↙ at the top right of the composer's text ([expanded] false: open the full editor) or the ↙↗ at the
 * top right of [ExpandedComposer] (collapse). Its tap area reaches into the field's padding, so it does not grow the field.
 */
@Composable
fun ComposerExpandButton(expanded: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier) {
    val spoken = stringResource(if (expanded) R.string.designsystem_composer_collapse else R.string.designsystem_composer_expand)
    val size = if (expanded) 48.dp else 40.dp
    Box(
        modifier.then(if (expanded) Modifier else Modifier.offset(x = 10.dp, y = (-8).dp)).size(size).clip(CircleShape)
            .clickable(role = Role.Button, onClick = onClick).semantics { contentDescription = spoken }
            .testTag(if (expanded) "composer-collapse" else "composer-expand"),
        contentAlignment = Alignment.Center,
    ) {
        JIcon(if (expanded) PhIcons.ArrowsInSimple else PhIcons.ArrowsOutSimple, null,
            size = if (expanded) 22.dp else 18.dp, tint = PersonaTheme.palette.ink2)
    }
}

/**
 * The draft full screen: collapse at the top right, the text filling the page, [send] at the bottom right above
 * the keyboard. It edits the same [value] (text and caret) as the small field, so collapsing changes nothing.
 * Back collapses too.
 */
@Composable
fun ExpandedComposer(
    value: TextFieldValue,
    onValueChange: (TextFieldValue) -> Unit,
    placeholder: String,
    onCollapse: () -> Unit,
    enabled: Boolean = true,
    send: @Composable RowScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val hardware = LocalComposerHardware.current
    val focus = remember { FocusRequester() }
    val hardwareModifier = if (hardware == null) Modifier else Modifier.onPreviewKeyEvent { event ->
        if (value.composition != null) return@onPreviewKeyEvent false
        when {
            event.key == Key.Escape -> {
                if (event.type == KeyEventType.KeyDown && event.nativeKeyEvent.repeatCount == 0) {
                    val collapse = hardware.collapseForEscape
                    if (collapse != null) collapse(onCollapse) else onCollapse()
                }
                true
            }
            (event.key == Key.Enter || event.key == Key.NumPadEnter) && (event.isCtrlPressed || event.isMetaPressed) -> {
                if (event.type == KeyEventType.KeyDown && event.nativeKeyEvent.repeatCount == 0 && hardware.send != null) {
                    val collapse = hardware.collapseForSend
                    if (collapse != null) collapse(onCollapse) else onCollapse()
                    hardware.send.invoke()
                }
                true
            }
            else -> false
        }
    }
    JFullScreen(onCollapse) {
        InterfaceContent {
            Column(Modifier.fillMaxSize().systemBarsPadding().imePadding().testTag("composer-expanded")) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp.dense), horizontalArrangement = Arrangement.End) {
                    ComposerExpandButton(expanded = true, onClick = onCollapse)
                }
                ReadingContent {
                    BasicTextField(
                        value, onValueChange, Modifier.then(hardwareModifier).weight(1f).fillMaxWidth().padding(horizontal = 20.dp.dense).focusRequester(focus).hardwareShortcutTextInput().testTag("composer-expanded-input"),
                        enabled = enabled, textStyle = PersonaTheme.type.body.copy(color = palette.ink), cursorBrush = SolidColor(palette.ink),
                        decorationBox = { field ->
                            Box {
                                if (value.text.isEmpty()) InterfaceContent {
                                    Text(placeholder, style = PersonaTheme.type.body, color = palette.ink3)
                                }
                                field()
                            }
                        },
                    )
                }
                Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp.dense, vertical = 4.dp.dense), horizontalArrangement = Arrangement.End,
                    verticalAlignment = Alignment.CenterVertically, content = send)
            }
        }
    }
    LaunchedEffect(Unit) { val _ = runCatching { focus.requestFocus() } }
}
