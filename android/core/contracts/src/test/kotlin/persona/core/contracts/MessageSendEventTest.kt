package persona.core.contracts

import org.junit.Assert.*
import org.junit.Test

class MessageSendEventTest {
    private fun frame(state: String, at: String = "2026-11-12T10:00:00.123456789Z") =
        """{"type":"message_send_state","machine_id":"computer","session_id":"chat","client_msg_id":"attempt","state":"$state","at":"$at"}"""

    @Test fun receiptsDecodeAndRoundTripWithNanosecondTime() {
        for (state in listOf("processing", "delivered", "failed", "unknown", "kept")) {
            val event = ServerEvent.decode(frame(state)) as MessageSendStateEvent
            assertEquals("computer", event.machineId)
            assertEquals("chat", event.sessionId)
            assertEquals("attempt", event.clientMessageId)
            assertEquals(state, event.state)
            assertEquals("2026-11-12T10:00:00.123456789Z", event.at)
            assertEquals(event, ServerEvent.decode(JsonCodec.encodeToString<ServerEvent>(event)))
        }
    }

    @Test fun helloHasAHubOwnedVersionAndWireFramesCannotSupplySocketIdentity() {
        val hello = ServerEvent.decode("""{"type":"hello","message_send_state":1,"features":["message_send_state"]}""") as Hello
        assertEquals(1, hello.messageSendState)
        val old = ServerEvent.decode("""{"type":"hello","features":["message_send_state"]}""") as Hello
        assertNull(old.messageSendState)
        val forged = ServerEvent.decode(frame("processing").dropLast(1) + ",\"socketGeneration\":99}") as MessageSendStateEvent
        assertNull(forged.socketGeneration)
        assertNull((ServerEvent.decode(JsonCodec.encodeToString<ServerEvent>(forged.copy(socketGeneration = 99))) as MessageSendStateEvent).socketGeneration)
    }

    @Test fun malformedOrUnknownReceiptsNeverEnterTheStream() {
        assertNull(ServerEvent.decode(frame("working")))
        assertNull(ServerEvent.decode(frame("processing", "invalid")))
        assertNull(ServerEvent.decode(frame("processing").replace("\"attempt\"", "\"\"")))
        assertNull(ServerEvent.decode(frame("processing").replace("\"computer\"", "\"\"")))
        assertNull(ServerEvent.decode(frame("processing").replace("\"chat\"", "\"\"")))
    }
}
