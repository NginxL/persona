package persona.feature.chat

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

class DictationSessionTest {
    private class Phone(scope: CoroutineScope, var allowed: Boolean = true) {
        var starts = 0
        var stops = 0
        var closed = 0
        var ask: ((Boolean) -> Unit)? = null
        val saved = mutableListOf<String>()
        val errors = mutableListOf<Boolean>()
        lateinit var listener: DictationSession.Listener
        val mic = DictationSession(scope, open = { callback ->
            listener = callback
            object : DictationSession.Input {
                override fun start() { starts++ }
                override fun stop() { stops++ }
                override fun close() { closed++ }
            }
        }, hasPermission = { allowed }, askPermission = { ask = it }, keep = saved::add, error = errors::add)
    }

    @Test fun tappingStopsAndUsesTheFinalCorrectionOnce() = runTest {
        val phone = Phone(this)
        phone.mic.tap(); phone.listener.partial("one"); phone.listener.partial("one note")
        assertTrue(phone.mic.listening); assertEquals("one note", phone.mic.heard); assertTrue(phone.saved.isEmpty())
        phone.mic.tap(); assertFalse(phone.mic.listening); assertEquals(1, phone.stops)
        phone.listener.result("one note."); phone.listener.result("late")
        assertEquals(listOf("one note."), phone.saved); assertEquals(1, phone.closed)
    }

    @Test fun releasingKeepsThePartialWhenTheServiceGivesNoFinal() = runTest {
        val phone = Phone(this)
        phone.mic.hold(); phone.listener.partial("revised words"); phone.mic.release(false)
        advanceTimeBy(1501)
        assertEquals(listOf("revised words"), phone.saved); assertFalse(phone.mic.active)
    }

    @Test fun anotherActivationWaitsForThePreviousFinalOrTimeout() = runTest {
        val phone = Phone(this)
        phone.mic.tap(); phone.mic.tap()
        val first = phone.listener
        phone.mic.tap(); phone.mic.hold()
        assertEquals(1, phone.starts)
        first.result("Final only"); first.result("Repeated")
        assertEquals(listOf("Final only"), phone.saved)
        phone.mic.tap(); phone.listener.partial("Second"); phone.mic.tap()
        phone.mic.tap(); phone.mic.cover(); advanceTimeBy(1501)
        assertEquals(listOf("Final only", "Second"), phone.saved)
        phone.mic.hold(); assertEquals(3, phone.starts); phone.mic.release(true)
    }

    @Test fun aPauseBetweenPhrasesKeepsListeningAndAReleaseKeepsBoth() = runTest {
        val phone = Phone(this)
        phone.mic.hold(); phone.listener.partial("first"); phone.listener.result("first phrase"); runCurrent()
        assertTrue(phone.mic.holding); assertEquals(2, phone.starts)
        phone.listener.partial("more"); phone.mic.release(false); phone.listener.result("more words")
        assertEquals(listOf("first phrase more words"), phone.saved)
    }

    @Test fun cancellationDropsAllPhrasesAndALateCallbackCannotReachAnotherDraft() = runTest {
        val phone = Phone(this)
        phone.mic.hold(); phone.listener.result("first phrase"); runCurrent()
        val old = phone.listener
        old.partial("second"); phone.mic.release(true); phone.mic.release(false)
        phone.mic.tap(); old.result("stale"); phone.listener.partial("new phrase"); phone.mic.finish(true)
        assertEquals(listOf("new phrase"), phone.saved); assertEquals(0, phone.stops)
    }

    @Test fun aReleasedHoldAndADeniedTapCannotOpenTheMicrophoneAfterPermissionReturns() = runTest {
        val phone = Phone(this, allowed = false)
        phone.mic.hold(); assertNotNull(phone.ask); phone.mic.release(false); phone.ask!!(true)
        assertEquals(0, phone.starts)
        phone.mic.tap(); phone.mic.cover(); phone.ask!!(true); assertEquals(0, phone.starts)
        phone.mic.tap(); phone.ask!!(false); assertEquals(listOf(true), phone.errors); assertEquals(0, phone.starts)
        phone.mic.tap(); phone.ask!!(true); assertTrue(phone.mic.listening); assertEquals(1, phone.starts)
        phone.listener.failed(true); advanceTimeBy(401); assertTrue(phone.mic.listening); assertEquals(2, phone.starts)
        phone.mic.tap(); advanceTimeBy(1501); assertFalse(phone.mic.active); assertTrue(phone.saved.isEmpty())
    }

    @Test fun disconnectionKeepsHeardWordsAndExplainsTheFailure() = runTest {
        val phone = Phone(this)
        phone.mic.tap(); phone.listener.partial("keep me"); phone.listener.failed(false)
        assertEquals(listOf("keep me"), phone.saved); assertEquals(listOf(false), phone.errors)
    }

    @Test fun holdAndReleaseThresholdsMatchTheOtherMics() {
        assertEquals(300L, DictationHoldMillis)
        assertFalse(cancelDictationHold(-150f, false)); assertTrue(cancelDictationHold(-151f, false))
        assertFalse(cancelDictationHold(-100f, true)); assertTrue(cancelDictationHold(-101f, true))
        assertFalse(cancelDictationHold(0f, true, -400f)); assertTrue(cancelDictationHold(-5f, true, -401f))
        assertFalse(cancelDictationHold(160f, true, 500f))
    }
}
