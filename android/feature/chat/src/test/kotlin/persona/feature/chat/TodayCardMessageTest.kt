package persona.feature.chat

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.MessageVia
import persona.core.contracts.api.OutboxMessage

class TodayCardMessageTest {
    @Test fun aCardGeneratedMessageCarriesCardProvenance() = runTest {
        var sent: Triple<SessionKey, String, String?>? = null
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) =
                error("A card-generated send must retain its provenance")
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?,
                launcher: String?, model: String?, via: String?) {
                sent = Triple(session, text, via)
            }
        }
        val key = SessionKey("hub", "main")
        sendTodayCardMessage(sender, key, "Review the calendar event", "card-operation")
        assertEquals(Triple(key, "Review the calendar event", MessageVia.Card), sent)
    }
}
