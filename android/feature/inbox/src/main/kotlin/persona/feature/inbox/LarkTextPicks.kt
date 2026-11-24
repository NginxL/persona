package persona.feature.inbox

import androidx.compose.foundation.text.contextmenu.builder.item
import androidx.compose.foundation.text.contextmenu.data.TextContextMenuItem
import androidx.compose.foundation.text.contextmenu.data.TextContextMenuKeys
import androidx.compose.foundation.text.contextmenu.modifier.appendTextContextMenuComponents
import androidx.compose.foundation.text.contextmenu.modifier.filterTextContextMenuComponents
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.Clipboard
import androidx.compose.ui.platform.LocalClipboard
import persona.core.inbox.*

/**
 * An item the selection menu of a native Feishu view adds ("AI edit", "Use as context", "Hand to Persona"): its label and what
 * becomes of the picked text. The link card supplies them; elsewhere the menu is the system's own.
 */
data class LarkTextPick(val label: String, val onPick: (String) -> Unit)

/**
 * The picks a native view in a link card offers, and what it reports back: whether it shows the web page instead
 * ([onWebpage]: then the card's "Refresh", progress and crash row are the page's), and once it has read its page the title
 * and the text the card's "Use as context" button sends for the whole of it (empty for a document: the card sends the
 * "whole document" marker and the agent reads it itself; a Moments post's own text). Supplied by the conversation's link
 * card; null everywhere else.
 */
class LarkLinkPicks(
    val picks: List<LarkTextPick>,
    val onWebpage: (Boolean) -> Unit = {},
    val onLoaded: (title: String, whole: String) -> Unit = { _, _ -> },
)

/**
 * Text the user may select (the document allows copying), with [picks] in its selection menu after "Copy". Compose
 * gives no read of a selection, so a pick runs the menu's own "Copy" into a clipboard that keeps the text instead of
 * putting it on the system's ([PickingClipboard]); "Copy" itself still copies.
 */
@Composable
internal fun LarkSelectable(enabled: Boolean, picks: List<LarkTextPick>, content: @Composable () -> Unit) {
    if (!enabled) { content(); return }
    if (picks.isEmpty()) { SelectionContainer { content() }; return }
    val system = LocalClipboard.current
    val clipboard = remember(system) { PickingClipboard(system) }
    val current by rememberUpdatedState(picks)
    val copy = remember { arrayOfNulls<TextContextMenuItem>(1) }
    CompositionLocalProvider(LocalClipboard provides clipboard) {
        SelectionContainer(Modifier
            .filterTextContextMenuComponents { component ->
                if (component.key == TextContextMenuKeys.CopyKey && component is TextContextMenuItem) copy[0] = component
                true
            }
            .appendTextContextMenuComponents {
                current.forEachIndexed { index, pick ->
                    item(PickKey(index), pick.label) {
                        val run = copy[0]
                        if (run == null) close()
                        else { clipboard.next = pick.onPick; run.onClick(this) }
                    }
                }
            }) { content() }
    }
}

private data class PickKey(val index: Int)

/** Passes everything to [system] except the one copy a pick asked for, which goes to [next] instead. */
internal class PickingClipboard(private val system: Clipboard) : Clipboard {
    var next: ((String) -> Unit)? = null
    override suspend fun getClipEntry(): ClipEntry? = system.getClipEntry()
    override suspend fun setClipEntry(clipEntry: ClipEntry?) {
        val pick = next ?: return system.setClipEntry(clipEntry)
        next = null
        val text = clipEntry?.clipData?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.text?.toString().orEmpty().trim()
        if (text.isNotEmpty()) pick(text)
    }
    override val nativeClipboard get() = system.nativeClipboard
}
