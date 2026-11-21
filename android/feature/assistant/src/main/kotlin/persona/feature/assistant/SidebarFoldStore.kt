package persona.feature.assistant

import android.content.Context

/**
 * The sidebar fold choices kept on this device (#6228, the web's `localStorage` and iOS's `@StoredValue`):
 * `rememberSaveable` only survives configuration changes, so a cold start reopened the "Recent" section.
 * One small SharedPreferences-backed store, like [FailedStarts], process death included.
 */
internal class SidebarFoldStore(context: Context) {
    private val prefs = context.applicationContext
        .getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    /** Whether the "Recent" section starts folded; the default is open, the same as the web's and iOS's. */
    val recentFolded: Boolean get() = prefs.getBoolean(KEY_RECENT_FOLDED, false)

    fun setRecentFolded(folded: Boolean) {
        prefs.edit().putBoolean(KEY_RECENT_FOLDED, folded).apply()
    }

    val pagesFolded: Boolean get() = prefs.getBoolean("pages.folded", false)
    val chatsFolded: Boolean get() = prefs.getBoolean("chats.folded", false)

    fun setPagesFolded(folded: Boolean) {
        prefs.edit().putBoolean("pages.folded", folded).apply()
    }

    fun setChatsFolded(folded: Boolean) {
        prefs.edit().putBoolean("chats.folded", folded).apply()
    }

    companion object {
        const val PREFS_NAME = "assistant-sidebar-folds"
        const val KEY_RECENT_FOLDED = "recent.folded"
    }
}
