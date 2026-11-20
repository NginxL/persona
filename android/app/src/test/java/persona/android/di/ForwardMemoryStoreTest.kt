package persona.android.di

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.datastore.preferences.core.stringPreferencesKey
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.feature.chat.ForwardMemory

/** Forward to's Recent in a pairing's preferences (#3901): ids only, latest first, at most eight, per pairing. */
class ForwardMemoryStoreTest {
    @Test fun keepsTheLatestTargetsAsIds() = runTest {
        val preferences = MemoryPreferences()
        val memory = PreferencesForwardMemory(preferences)
        assertEquals(emptyList<String>(), memory.targets())
        memory.remember("session:studio/photos")
        memory.remember("persona")
        memory.remember("session:studio/photos")
        assertEquals(listOf("session:studio/photos", "persona"), memory.targets())
        (1..10).forEach { memory.remember("session:mbp/s$it") }
        assertEquals(ForwardMemory.LIMIT, memory.targets().size)
        assertEquals("session:mbp/s10", memory.targets().first())
        // Another pairing's preferences know nothing of them.
        assertEquals(emptyList<String>(), PreferencesForwardMemory(MemoryPreferences()).targets())
        // What does not read is an empty list, not a failure.
        preferences.updateData { it.toMutablePreferences().apply { this[stringPreferencesKey(ForwardMemory.KEY)] = "{" } }
        assertEquals(emptyList<String>(), memory.targets())
    }

    private class MemoryPreferences : DataStore<Preferences> {
        private val state = MutableStateFlow(emptyPreferences())
        override val data = state
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
            transform(state.value).also { state.value = it }
    }
}
