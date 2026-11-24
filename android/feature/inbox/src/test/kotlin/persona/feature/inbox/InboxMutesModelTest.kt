package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import persona.core.inbox.*

/**
 * "Muted" (docs/assistant/inbox.md "Muting") with several managers (docs/assistant/managers.md "Clients"): the list is the
 * current assistant's, read with it named and each entry stamped with it; one that can't be read throws; Unmute goes to
 * the manager whose mute it is and, failing, puts it back there; another assistant's list never stays under this one.
 */
class InboxMutesModelTest {
    private class Mutes : InboxRepository by FakeInboxRepository() {
        val unmuted = mutableListOf<String>()
        var failUnmute: Exception? = null
        override suspend fun inboxMutes(manager: String?): List<InboxMute> = when (manager) {
            "m-3" -> throw IllegalStateException("503")
            "m-2" -> listOf(InboxMute("u1", "github", tag = "repo", value = "o/two", name = "o/two", createdAt = "2026-02-01T00:00:00Z"))
            else -> listOf(InboxMute("u1", "github", tag = "repo", value = "o/one", name = "o/one", createdAt = "2026-01-01T00:00:00Z"))
        }
        override suspend fun unmute(id: String, manager: String?) {
            unmuted += "$manager:$id"
            failUnmute?.let { throw it }
        }
    }

    @Test fun theCurrentAssistantsMutesAreReadUnderItsNameAndTakenBackOnIt() = runBlocking {
        val repository = Mutes()
        val model = InboxMutesModel(repository)
        model.load("m-2")
        assertEquals(listOf("o/two"), model.mutes?.map { it.name })
        assertEquals(listOf<String?>("m-2"), model.mutes?.map { it.manager })

        // Another assistant's list replaces it: the one before is gone, and a manager that can't be read throws with nothing shown.
        model.load("assistant")
        assertEquals(listOf("o/one"), model.mutes?.map { it.name })
        assertEquals(listOf<String?>("assistant"), model.mutes?.map { it.manager })
        try { model.load("m-3"); fail("a manager that can't be read throws") } catch (e: IllegalStateException) { assertEquals("503", e.message) }
        assertNull(model.mutes)

        model.load("m-2")
        model.unmute(model.mutes!!.single())
        assertEquals(emptyList<InboxMute>(), model.mutes)
        assertTrue(model.isEmpty)

        model.load("assistant")
        repository.failUnmute = IllegalStateException("offline")
        val left = model.mutes!!.single()
        try { model.unmute(left); fail("a failed unmute throws") } catch (e: IllegalStateException) { assertEquals("offline", e.message) }
        assertEquals(listOf(left), model.mutes)
        assertEquals(listOf("m-2:u1", "assistant:u1"), repository.unmuted)
    }

    @Test fun aReadOverTakenByANewerOneDoesntLand() = runBlocking {
        val first = CompletableDeferred<Unit>()
        var calls = 0
        val repository = object : InboxRepository by Mutes() {
            override suspend fun inboxMutes(manager: String?): List<InboxMute> {
                if (calls++ == 0) {
                    first.await()
                    return listOf(InboxMute("old", "github", tag = "repo", value = "o/old", name = "o/old"))
                }
                return listOf(InboxMute("new", "github", tag = "repo", value = "o/new", name = "o/new"))
            }
        }
        val model = InboxMutesModel(repository)
        val before = launch { model.load(null) } // the managers changed while it was on its way
        yield()
        model.load(null)
        first.complete(Unit)
        before.join()
        assertEquals(listOf("new"), model.mutes?.map { it.id })
    }

    @Test fun aFailedReadOverTakenByANewerOneSaysNothing() = runBlocking {
        val first = CompletableDeferred<Unit>()
        var calls = 0
        val repository = object : InboxRepository by Mutes() {
            override suspend fun inboxMutes(manager: String?): List<InboxMute> {
                if (calls++ == 0) {
                    first.await()
                    throw IllegalStateException("timeout")
                }
                return listOf(InboxMute("new", "github", tag = "repo", value = "o/new", name = "o/new"))
            }
        }
        val model = InboxMutesModel(repository)
        var thrown: Throwable? = null
        val before = launch { try { model.load(null) } catch (e: IllegalStateException) { thrown = e } }
        yield()
        model.load(null)
        first.complete(Unit)
        before.join()
        assertNull(thrown)
        assertEquals(listOf("new"), model.mutes?.map { it.id })
    }

    @Test fun anUnmuteStaysGoneWhenAReloadReadBeforeItLands() = runBlocking {
        val answer = CompletableDeferred<Unit>()
        val repository = object : InboxRepository by Mutes() {
            override suspend fun unmute(id: String, manager: String?) = answer.await()
        }
        val model = InboxMutesModel(repository)
        model.load("m-2")
        val gone = model.mutes!!.single()
        val unmuting = launch { model.unmute(gone) }
        yield()
        model.load("m-2") // read while the unmute was on its way: it still has the mute
        assertEquals(listOf("o/two"), model.mutes?.map { it.name })
        answer.complete(Unit)
        unmuting.join()
        assertEquals(emptyList<InboxMute>(), model.mutes)
    }

    @Test fun withOneManagerTheMutesAreOneListAsBefore() = runBlocking {
        val model = InboxMutesModel(Mutes())
        model.load(null) // none named
        assertEquals(listOf("o/one"), model.mutes?.map { it.name })
        assertEquals(listOf<String?>(null), model.mutes?.map { it.manager })
    }
}
