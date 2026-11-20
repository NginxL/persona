package persona.android.di

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import kotlinx.coroutines.flow.first
import kotlinx.serialization.encodeToString
import persona.core.contracts.JsonCodec
import persona.feature.chat.ForwardMemory

/**
 * Forward to's Recent (#3901, iOS `ForwardMemory`) in one pairing's preferences: the targets as ids, latest first, at most
 * [ForwardMemory.LIMIT]; never synced, and erased with the pairing's other data.
 */
internal class PreferencesForwardMemory(private val store: DataStore<Preferences>) : ForwardMemory {
    private val key = stringPreferencesKey(ForwardMemory.KEY)

    private fun decode(value: String?): List<String> =
        value?.let { runCatching { JsonCodec.decodeFromString<List<String>>(it) }.getOrNull() }.orEmpty()

    override suspend fun targets(): List<String> = decode(store.data.first()[key])

    override suspend fun remember(target: String) {
        store.edit { it[key] = JsonCodec.encodeToString(ForwardMemory.next(decode(it[key]), target)) }
    }
}
