package persona.platform.notify

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.InteractionEvent
import persona.core.contracts.InteractionResolvedEvent
import persona.core.contracts.NotifyEvent
import persona.core.contracts.Pong
import persona.core.contracts.ServerEvent

/** A notification being handled holds up no other subscriber of the hub's events (#4038). */
class NotificationEventsTest {
    @Test fun currentInteractionSnapshotsReachNotificationDelivery() = runBlocking {
        val update = InteractionEvent(Interaction("request", kind = "permission"))
        val resolved = InteractionResolvedEvent(update.interaction.id)
        val events = flowOf<ServerEvent>(update, Pong("chat"), resolved).notificationEvents().toList()
        assertEquals(listOf(update, resolved), events)
    }

    @Test fun aNotificationBeingHandledHoldsUpNoOtherLiveEvent() = runBlocking {
        // Like ConnectionRuntime's merged events: no buffer, so emit waits for every subscriber to take the event.
        val events = MutableSharedFlow<ServerEvent>()
        val handling = CompletableDeferred<Unit>()
        val refreshed = CompletableDeferred<Unit>()
        val handled = Channel<ServerEvent>(Channel.UNLIMITED)
        val seen = Channel<ServerEvent>(Channel.UNLIMITED)
        val notifications = launch(Dispatchers.Default) {
            events.notificationEvents().collect { event ->
                handled.send(event)
                // A refresh over the network that has not returned yet.
                if (event is NotifyEvent) { handling.complete(Unit); refreshed.await() }
            }
        }
        val chat = launch(Dispatchers.Default) { events.collect { seen.send(it) } }
        try {
            withTimeout(5_000) {
                events.subscriptionCount.first { it == 2 }
                val card = NotifyEvent("interaction", "machine", "session", interactionId = "ix")
                events.emit(card)
                handling.await()
                // Streaming goes on while the card is handled: without the buffer this emit waits for the refresh.
                events.emit(Pong("delta"))
                val resolved = InteractionResolvedEvent("ix")
                events.emit(resolved)
                assertEquals(listOf(card, Pong("delta"), resolved), List(3) { seen.receive() })
                refreshed.complete(Unit)
                // The notifications still get theirs, in order, and only those.
                assertEquals(listOf<ServerEvent>(card, resolved), List(2) { handled.receive() })
                assertEquals(null, handled.tryReceive().getOrNull())
            }
        } finally {
            notifications.cancel()
            chat.cancel()
        }
    }
}
