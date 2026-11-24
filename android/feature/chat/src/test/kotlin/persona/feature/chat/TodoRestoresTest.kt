package persona.feature.chat

import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class TodoRestoresTest {
    @Test
    fun observeNotifiesOnRestoreAndStopsAfterUnregister() {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        val restores = TodoRestores(context)
        val seen = mutableListOf<Map<String, Long>>()
        val unregister = restores.observe { seen += it }

        val t1 = 1_700_000_000_000L
        val afterFirst = restores.restore("todo-1", t1)
        assertEquals(mapOf("todo-1" to t1), afterFirst)
        assertEquals(mapOf("todo-1" to t1), seen.last())

        unregister()
        val countBefore = seen.size
        val afterSecond = restores.restore("todo-2", t1 + 1_000L)
        assertEquals(mapOf("todo-1" to t1, "todo-2" to t1 + 1_000L), afterSecond)
        assertEquals(countBefore, seen.size)
    }
}
