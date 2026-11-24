package persona.feature.inbox

import android.content.Context
import android.graphics.Rect
import android.view.ActionMode
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.webkit.WebView
import androidx.compose.runtime.staticCompositionLocalOf
import org.json.JSONTokener
import persona.core.inbox.*

/** Text picked on a page opened from the inbox. */
data class InboxWebSelection(val title: String, val url: String, val text: String)

/**
 * Supplied by the app shell: "Hand to Persona" for text picked on a page opened from the inbox. The inbox has no
 * conversation of its own to send it to, so the shell asks what Persona is to do and puts it in the main conversation.
 * Without it the page's selection menu is the system's own.
 */
val LocalInboxWebSelection = staticCompositionLocalOf<((InboxWebSelection) -> Unit)?> { null }

/**
 * A WebView whose text selection menu starts with "Hand to Persona" while [onPick] is set. Chromium fills the menu in
 * onPrepareActionMode, each time the selection changes, with items that ask to be action buttons; ours asks the same
 * in its group with the lowest order, so it comes before "Copy" (the chat's link sheet does the same).
 */
internal class InboxSelectionWebView(context: Context) : WebView(context) {
    var onPick: ((InboxWebSelection) -> Unit)? = null

    override fun startActionMode(callback: ActionMode.Callback): ActionMode? = super.startActionMode(wrap(callback))
    override fun startActionMode(callback: ActionMode.Callback, type: Int): ActionMode? = super.startActionMode(wrap(callback), type)

    private fun wrap(delegate: ActionMode.Callback) = object : ActionMode.Callback2() {
        override fun onCreateActionMode(mode: ActionMode, menu: Menu): Boolean = delegate.onCreateActionMode(mode, menu)
        override fun onPrepareActionMode(mode: ActionMode, menu: Menu): Boolean {
            val changed = delegate.onPrepareActionMode(mode, menu)
            menu.removeItem(HAND_TO_PERSONA)
            if (onPick == null) return changed
            val group = (0 until menu.size()).map { menu.getItem(it) }.minByOrNull { it.order }?.groupId ?: Menu.NONE
            menu.add(group, HAND_TO_PERSONA, 0, context.getString(R.string.inbox_hand_to_persona)).setShowAsAction(MenuItem.SHOW_AS_ACTION_ALWAYS)
            return true
        }
        override fun onActionItemClicked(mode: ActionMode, item: MenuItem): Boolean {
            if (item.itemId != HAND_TO_PERSONA) return delegate.onActionItemClicked(mode, item)
            val sourceUrl = url.orEmpty()
            val sourceTitle = title.orEmpty()
            // Read the selection before closing the menu: finishing first clears it.
            evaluateJavascript("window.getSelection().toString()") { result ->
                val selected = runCatching { JSONTokener(result).nextValue() as? String }.getOrNull().orEmpty().trim()
                if (selected.isNotEmpty() && url == sourceUrl) onPick?.invoke(InboxWebSelection(sourceTitle, sourceUrl, selected))
                mode.finish()
            }
            return true
        }
        override fun onDestroyActionMode(mode: ActionMode) = delegate.onDestroyActionMode(mode)
        override fun onGetContentRect(mode: ActionMode, view: View, outRect: Rect) {
            if (delegate is ActionMode.Callback2) delegate.onGetContentRect(mode, view, outRect)
            else super.onGetContentRect(mode, view, outRect)
        }
    }

    private companion object {
        const val HAND_TO_PERSONA = 0x6a0201
    }
}
