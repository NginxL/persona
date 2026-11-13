package persona.feature.chat

import android.content.ClipData
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.text.contextmenu.builder.item
import androidx.compose.foundation.text.contextmenu.modifier.appendTextContextMenuComponents
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.text.selection.rememberSelectionState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.movableContentOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.isSpecified
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.Clipboard
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.LocalSelectionAbove

/*
 * Select, where the message is (iOS `LiftSelection.swift`, alignment plan #4210 T5). Choosing "Select" in a message's menu
 * selects all of its words in the chat, the system's way: handles to drag, a long press that picks a word, and the floating
 * menu with Copy and, where the chat quotes, Quote (Select all is there until everything is). iOS lifts the message over the
 * dimmed chat and draws its words again in a text view, because its Markdown cannot select; Android draws them with `Text`,
 * so one SelectionContainer round the message's words is all it takes, and nothing is lifted.
 *
 * While the words are selected the message has no long press, swipe to quote or inline-code copy (`messageMenu`,
 * `LocalSelectionAbove`): they would end the selection or fight it. The selection ends, as iOS's does, when nothing is
 * selected any more (a tap on the words, Copy, Quote); and besides that when a tap starts outside the message
 * (`endSelectionOnTapOutside`; iOS: a tap on the dimmed chat), on Back, and when the message has left the screen.
 *
 * What Compose gives: `SelectionState.selectAll()` starts with every word selected, as iOS's Select Text does. It has no
 * public way to start with the word under a finger (`select` takes a range of the container's text, and the Markdown
 * renderer shows no text layouts to find an offset by), so a long press on a word of a selected message picks that word, as
 * iOS's second long press does.
 */

/** Whether the message [id]'s words are selected in place: read for this message alone, so a change recomposes only it. */
@Composable
internal fun rememberSelecting(menu: MessageMenuState?, id: String?): Boolean {
    if (menu == null || id == null) return false
    val selecting by remember(menu, id) { derivedStateOf { menu.selecting?.id == id } }
    return selecting
}

/**
 * The words of the message [target] (one layout node, [content]): drawn as they are, until "Select" selects them in place
 * ([MessageMenuState.select]), then inside a SelectionContainer. They are one piece that moves between the two places
 * ([movableContentOf]), so an opened quote or an unfolded code block stays as it was.
 */
@Composable
internal fun SelectableWords(target: MessageTarget?, content: @Composable () -> Unit) {
    val menu = LocalChatRowActions.current?.menu
    val selecting = rememberSelecting(menu, target?.id)
    val latest by rememberUpdatedState(content)
    val words = remember { movableContentOf { latest() } }
    if (selecting && menu != null && target != null) SelectedWords(menu, target) {
        CompositionLocalProvider(LocalSelectionAbove provides true) { words() }
    } else words()
}

@Composable
private fun SelectedWords(menu: MessageMenuState, target: MessageTarget, content: @Composable () -> Unit) {
    val state = rememberSelectionState()
    val quote = menu.quoteWords?.takeIf { target.pending == null }
    val quoteLabel = stringResource(R.string.chat_menu_quote)
    val system = LocalClipboard.current
    val clipboard = remember(system) { PickedWordsClipboard(system) }
    // Everything selected to begin with, once the words have registered with the container as it lays out; a message with
    // nothing to select (pictures alone) has nothing to be selecting.
    LaunchedEffect(state) {
        repeat(SELECT_FRAMES) {
            withFrameNanos { }
            val ready = try { state.getSelectableTexts().isNotEmpty() } catch (_: IllegalStateException) { false }
            if (ready) { state.selectAll(); return@LaunchedEffect }
        }
        menu.endSelection(target.id)
    }
    // It is over when nothing is selected any more after something was: a tap on the words, Copy or Quote.
    val picked = state.selectedTexts.isNotEmpty()
    var had by remember { mutableStateOf(false) }
    LaunchedEffect(picked) {
        if (picked) had = true
        else if (had) {
            // A long press on another word lets go of the words before it picks the new ones: give it a moment.
            repeat(SETTLE_FRAMES) { withFrameNanos { } }
            if (state.selectedTexts.isEmpty()) menu.endSelection(target.id)
        }
    }
    BackHandler { menu.endSelection(target.id) }
    DisposableEffect(menu, target.id) { onDispose { menu.endSelection(target.id) } }
    CompositionLocalProvider(LocalClipboard provides clipboard) {
        SelectionContainer(state, Modifier
            .onGloballyPositioned { places ->
                menu.selectingBounds = places.boundsInWindow()
                // Scrolled out of the screen: nothing of it shows, and nothing is left to select.
                if (places.isAttached && menu.selectingBounds.isEmpty) menu.endSelection(target.id)
            }
            .appendTextContextMenuComponents {
                if (quote != null) item(QuoteItem, quoteLabel) {
                    val words = pickedWords(state.selectedTexts.joinToString("\n") { it.text })
                    if (words.isNotEmpty()) quote(target, words)
                    close()
                    menu.endSelection(target.id)
                }
            }) { content() }
    }
}

private data object QuoteItem

/** How many frames the words get to register with their container before there is nothing to select. */
private const val SELECT_FRAMES = 60

/** How many frames nothing may be selected before the selection is taken to be over. */
private const val SETTLE_FRAMES = 8

/**
 * The words that were picked, as the chat shows them: without the thin spaces that pad inline code ([copyingCode]) and the
 * placeholders that stand for a formula's picture (a formula is left out: the Markdown cannot say its source from here),
 * and a list item's marker, which is a text of its own, back on the item's line.
 */
internal fun pickedWords(text: String): String =
    text.replace("\u2009", "").replace("\uFFFC", "").replace("\uFFFD", "")
        .lines().joinToString("\n") { it.trimEnd() }
        .replace(LIST_MARKER) { "${it.groupValues[1]} " }
        .trim()

private val LIST_MARKER = Regex("(?m)^([•◦▪‣∙–-]|\\d{1,3}[.)])\\n")

/** The system clipboard, except that the words the container copies are [pickedWords]. */
private class PickedWordsClipboard(private val system: Clipboard) : Clipboard {
    override suspend fun getClipEntry(): ClipEntry? = system.getClipEntry()
    override suspend fun setClipEntry(clipEntry: ClipEntry?) {
        val data = clipEntry?.clipData?.takeIf { it.itemCount > 0 }
        val text = data?.getItemAt(0)?.text?.toString() ?: return system.setClipEntry(clipEntry)
        system.setClipEntry(ClipEntry(ClipData.newPlainText(data.description.label, pickedWords(text))))
    }
    override val nativeClipboard get() = system.nativeClipboard
}

/**
 * A tap that starts outside the selected message ends the selection (Android's own text selection ends the same way; iOS: a
 * tap on the dimmed chat). The tap goes on to what is under it: this only watches, before the content sees it. A drag is not
 * a tap, so the chat can be scrolled with the words selected.
 */
@Composable
internal fun Modifier.endSelectionOnTapOutside(menu: MessageMenuState): Modifier {
    // Where this area is, to put a touch in the window's coordinates (the message's bounds are the window's); not observed.
    val places = remember { arrayOfNulls<LayoutCoordinates>(1) }
    return onGloballyPositioned { places[0] = it }.pointerInput(menu) {
        awaitEachGesture {
            val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
            if (menu.selecting == null) return@awaitEachGesture
            val at = places[0]?.takeIf { it.isAttached }?.localToWindow(down.position) ?: Offset.Unspecified
            if (at.isSpecified && menu.selectingBounds.contains(at)) return@awaitEachGesture
            var moved = false
            while (true) {
                val event = awaitPointerEvent(PointerEventPass.Initial)
                val change = event.changes.firstOrNull { it.id == down.id } ?: return@awaitEachGesture
                if ((change.position - down.position).getDistance() > viewConfiguration.touchSlop) moved = true
                if (!change.pressed) {
                    if (!moved) menu.endSelection()
                    return@awaitEachGesture
                }
            }
        }
    }
}
