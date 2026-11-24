package persona.android.ui

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.material3.Text
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import persona.core.contracts.Hello
import persona.core.contracts.Item
import persona.core.contracts.ServerEvent
import persona.core.contracts.SessionKey
import persona.core.contracts.StarredMessagesEvent
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.STARRED_MESSAGES_FEATURE
import persona.core.contracts.api.SocketHello
import persona.core.contracts.api.StarredConversation
import persona.core.contracts.api.StarredIndex
import persona.core.contracts.api.StarredList
import persona.core.contracts.api.StarredMessagesApi
import persona.core.contracts.api.StarredReference
import persona.core.contracts.api.StarredSnapshot
import persona.feature.chat.LocalStarredMessagesStore

@RunWith(AndroidJUnit4::class)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class StarredMessagesHostTest {
    @get:Rule val compose = createEmptyComposeRule()
    @After fun resetChoice() { AssistantManagerChoice.reset() }

    private class Hub(val id: String, features: List<String> = listOf(STARRED_MESSAGES_FEATURE)) : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>(extraBufferCapacity = 16)
        override val currentHello = MutableStateFlow<SocketHello?>(SocketHello(1, Hello(hubId = id, features = features)))
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = false
    }

    private class FakeStarred(var label: String, var refsCount: Int) : StarredMessagesApi {
        @Volatile var reads = 0
        override suspend fun index(): StarredIndex {
            reads++
            val refs = List(refsCount) { i ->
                StarredReference("sm-$label-$i", "server", "s1", "m-$i", StarredConversation.Assistant)
            }
            return StarredIndex(refs, canWrite = true)
        }
        override suspend fun list() = StarredList(emptyList(), canWrite = true)
        override suspend fun add(star: StarredSnapshot): StarredSnapshot = error("unused")
        override suspend fun remove(id: String): StarredSnapshot? = error("unused")
        override suspend fun fullItem(session: SessionKey, itemId: String): Item = error("unused")
    }

    @Test fun hostFollowsSelectedAssistantAndFiltersEvents() {
        val hub = Hub("hub-1")
        val home = FakeStarred("home", 1)
        val work = FakeStarred("work", 3)
        val activeHub = mutableStateOf(hub)
        AssistantManagerChoice.choose(hub.id, null, "home")

        val app = ApplicationProvider.getApplicationContext<Application>()
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        ActivityScenario.launch(ComponentActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                activity.setContent {
                    val active = activeHub.value
                    StarredMessagesHost(active, active.id, { manager -> if (manager == "work") work else home }) {
                        val count = LocalStarredMessagesStore.current?.state?.collectAsState()?.value?.count
                        Text("count=${count ?: -1}")
                    }
                }
            }
            compose.onNodeWithText("count=1").assertIsDisplayed()

            compose.runOnIdle {
                home.refsCount = 2
                hub.events.tryEmit(StarredMessagesEvent())
            }
            compose.onNodeWithText("count=2").assertIsDisplayed()

            compose.runOnIdle { AssistantManagerChoice.pick("work") }
            compose.onNodeWithText("count=3").assertIsDisplayed()

            compose.runOnIdle {
                hub.events.tryEmit(StarredMessagesEvent("home"))
            }
            compose.waitForIdle()
            assertEquals(1, work.reads)

            compose.runOnIdle {
                work.refsCount = 4
                hub.events.tryEmit(StarredMessagesEvent("work"))
            }
            compose.onNodeWithText("count=4").assertIsDisplayed()
        }
    }
}
