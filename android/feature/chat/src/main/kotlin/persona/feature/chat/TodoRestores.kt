package persona.feature.chat

import android.content.Context
import persona.core.domain.tasks.withRestore

/**
 * Shelved to-dos brought back by hand (#3206, docs/assistant/todo-status-sop.md "Shelving"): the moment of the restore
 * counts as activity, so the to-do leaves Shelved and goes back only after another 48 hours without a move. Kept on this
 * device only, as the web keeps it per browser; the hub keeps nothing about it and nothing of the to-do changes.
 */
class TodoRestores(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("todo_restores", Context.MODE_PRIVATE)

    /** To-do id → when it was restored (ms). */
    fun read(): Map<String, Long> =
        prefs.all.mapNotNull { (id, at) -> (at as? Long)?.let { id to it } }.toMap()

    /** Restores [id] at [now], dropping restores that would have shelved again anyway; returns the restores after it. */
    fun restore(id: String, now: Long): Map<String, Long> {
        val next = withRestore(read(), id, now)
        prefs.edit().clear().apply { next.forEach { (k, v) -> putLong(k, v) } }.apply()
        return next
    }

    /** Subscribes to changes made anywhere in this app so UI does not re-read SharedPreferences on every recomposition. */
    fun observe(onChange: (Map<String, Long>) -> Unit): () -> Unit {
        val listener = android.content.SharedPreferences.OnSharedPreferenceChangeListener { _, _ ->
            onChange(read())
        }
        prefs.registerOnSharedPreferenceChangeListener(listener)
        return { prefs.unregisterOnSharedPreferenceChangeListener(listener) }
    }
}
