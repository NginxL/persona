package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/**
 * Another reply to the same place changing state makes the composer read its delivery again; what the user did while
 * that read was on its way (typing, sending) stays (#9907).
 */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ReplyComposerUpdatesTest {
    /** The shared fake, with outbox updates the test sends and a status read that answers when the test says. */
    private class SlowStatus(val base: FakeInboxRepository = FakeInboxRepository()) : InboxRepository by base {
        val updates = MutableSharedFlow<Unit>(extraBufferCapacity = 1)
        var answer: CompletableDeferred<ReplyDelivery>? = null
        override fun replyUpdates(target: ReplyTarget): Flow<Unit> = updates
        override suspend fun replyStatus(target: ReplyTarget, clientId: String): ReplyDelivery = answer?.await() ?: ReplyDelivery.Ready
    }

    @Test fun textTypedWhileTheStatusIsReadStays() = runTest {
        val repository = SlowStatus()
        val composer = ReplyComposer(repository, ReplyTarget.Lark("feed"), backgroundScope)
        runCurrent()
        composer.update("Hel")
        val answer = CompletableDeferred<ReplyDelivery>()
        repository.answer = answer
        // The message sent before this one moves on in the outbox: the composer reads its own delivery again.
        repository.updates.tryEmit(Unit)
        runCurrent()
        composer.update("Hello")
        answer.complete(ReplyDelivery.Ready)
        runCurrent()
        assertEquals("Hello", composer.state.value.draft.text)
        assertTrue(composer.state.value.canEdit)
    }

    @Test fun aSendStartedWhileTheStatusIsReadStaysOnItsWay() = runTest {
        val repository = SlowStatus()
        val composer = ReplyComposer(repository, ReplyTarget.Lark("feed"), backgroundScope)
        runCurrent()
        composer.update("Hello")
        runCurrent()
        val answer = CompletableDeferred<ReplyDelivery>()
        repository.answer = answer
        repository.updates.tryEmit(Unit)
        runCurrent()
        // The send waits on its draft write, so it is still on its way when the late answer comes.
        repository.base.draftGate = CompletableDeferred()
        composer.send()
        runCurrent()
        assertTrue(composer.state.value.sending)
        answer.complete(ReplyDelivery.Ready)
        runCurrent()
        // The late answer is for the moment before the send: it does not take the send back.
        assertTrue(composer.state.value.sending)
        assertEquals(ReplyDelivery.Sending, composer.state.value.delivery)
        assertEquals("Hello", composer.state.value.draft.text)
    }
}
