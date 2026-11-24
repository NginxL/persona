package persona.core.data

import java.io.File
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.contracts.Endpoint
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.ItemsQuery
import persona.core.hub.HttpChatComposerService
import persona.core.hub.HubConnectionConfig
import persona.core.inbox.InboxHandOffRequest

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class DefaultManagerEngineInboxHandoffLiveTest {
    @get:Rule val temporary = TemporaryFolder()

    @Test fun retriesTheRefusedHandOffAndReadsTheEngineReplyFromHistory() {
        val path = System.getenv("PERSONA_ANDROID_ENGINE_FIXTURE")
        assumeTrue("Requires the isolated engine handoff fixture", !path.isNullOrBlank())
        val file = File(requireNotNull(path))
        val fixture = JsonCodec.parseToJsonElement(file.readText()).jsonObject
        fun field(name: String) = fixture.getValue(name).jsonPrimitive.content
        val base = field("base_url").toHttpUrl()
        require(base.host == "127.0.0.1")
        val config = HubConnectionConfig(field("hub_id"), null, field("token"), listOf(Endpoint(base.toString(), "lan")), "handoff-test")
        val target = SessionKey(field("machine_id"), field("session_id"))
        val client = field("client_id")
        runBlocking {
            withTimeout(180_000) {
                withLiveInboxHandoff(config, "engine", temporary.newFolder("engine")) {
                    val item = repository.snapshot.value.records.single { it.hubItemId == field("item_id") }
                    val composer = sender.connectComposer { transport ->
                        HttpChatComposerService(pairing, transport, connection, config.token,
                            onRetryStart = sender::beginComposerRetry,
                            onRetryDelivery = sender::recordComposerDelivery, onPairingExpired = {})
                    }
                    assertEquals(target, repository.handOff(InboxHandOffRequest(item.item.id, field("instruction"),
                        target.machineId, target.sessionId, client, targetTitle = "Fixture agent")))
                    store.entries.first { entries -> entries.any { it.clientMessageId == client && it.state == OutboxState.Refused } }
                    assertFalse(recorded.isCompleted)
                    file.resolveSibling("refused").createNewFile()
                    // The driver first inspects the real transcript, wire and database at the refusal boundary.
                    while (!file.resolveSibling("allow-retry").exists()) delay(50)
                    assertEquals(DeliveryState.Sent, composer.retryRefused(client, clearDraft = true).state)
                    assertEquals(target.sessionId, recorded.await().getValue("session").jsonObject.getValue("session_id").jsonPrimitive.content)
                    while (true) {
                        val items = chat.items(target, ItemsQuery()).items
                        val replies = items.filter { it.role == "assistant" && it.kind == "text" && it.text?.trim() == field("expected") }
                        if (replies.isNotEmpty()) {
                            assertEquals(1, replies.size)
                            assertEquals(1, items.count { it.role == "user" && it.text?.trim() == field("instruction") })
                            file.resolveSibling("android-result-seq").writeText(replies.single().seq.toString())
                            break
                        }
                        delay(200)
                    }
                }
            }
        }
    }
}
