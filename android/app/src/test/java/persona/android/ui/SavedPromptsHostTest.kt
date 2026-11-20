package persona.android.ui

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
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
import persona.core.contracts.SavedPromptsEvent
import persona.core.contracts.ServerEvent
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.SavedPrompt
import persona.core.contracts.api.SavedPromptsApi
import persona.core.contracts.api.SocketHello
import persona.feature.chat.LocalSavedPrompts

@RunWith(AndroidJUnit4::class)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SavedPromptsHostTest {
    @get:Rule val compose = createEmptyComposeRule()
    @After fun resetChoice() { AssistantManagerChoice.reset() }

    private class Hub(val id: String) : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>(extraBufferCapacity = 16)
        override val currentHello = MutableStateFlow<SocketHello?>(SocketHello(1, Hello(hubId = id)))
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = false
    }

    private class Prompts(var name: String) : SavedPromptsApi {
        @Volatile var reads = 0
        var pending: CompletableDeferred<List<SavedPrompt>>? = null
        override suspend fun list(): List<SavedPrompt> {
            reads++
            return pending?.await() ?: listOf(SavedPrompt("fixture", name, "Prompt text"))
        }
        override suspend fun save(id: String?, name: String, text: String): SavedPrompt = error("Unexpected save")
        override suspend fun reorder(ids: List<String>): List<SavedPrompt> = error("Unexpected reorder")
        override suspend fun delete(id: String) = error("Unexpected delete")
    }

    @Test fun theListFollowsTheSelectedHubAndAssistantAndOnlyTheirEvents() {
        val first = Hub("first")
        val next = Hub("next")
        val hub = mutableStateOf(first)
        val home = Prompts("Home list")
        val work = Prompts("Work list")
        val other = Prompts("Next hub list")
        AssistantManagerChoice.choose(first.id, null, "home")
        val app = ApplicationProvider.getApplicationContext<Application>()
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        ActivityScenario.launch(ComponentActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                activity.setContent {
                    val active = hub.value
                    SavedPromptsHost(active, active.id, { manager ->
                        if (active === next) other else if (manager == "work") work else home
                    }) {
                        Column { LocalSavedPrompts.current.forEach { Text(it.name) } }
                    }
                }
            }
            compose.onNodeWithText("Home list").assertIsDisplayed()
            val late = CompletableDeferred<List<SavedPrompt>>()
            compose.runOnIdle {
                home.pending = late
                first.events.tryEmit(SavedPromptsEvent())
            }
            compose.waitUntil { home.reads == 2 }
            compose.runOnIdle { AssistantManagerChoice.pick("work") }
            compose.onNodeWithText("Work list").assertIsDisplayed()
            compose.runOnIdle {
                late.complete(listOf(SavedPrompt("late", "Late home list", "Body")))
                first.events.tryEmit(SavedPromptsEvent("home"))
            }
            compose.onNodeWithText("Late home list").assertDoesNotExist()
            compose.runOnIdle {
                assertEquals(1, work.reads)
                work.name = "Updated work list"
                first.events.tryEmit(SavedPromptsEvent("work"))
            }
            compose.onNodeWithText("Updated work list").assertIsDisplayed()
            compose.runOnIdle {
                work.name = "Reconnected work list"
                first.currentHello.value = SocketHello(2, Hello(hubId = first.id))
            }
            compose.onNodeWithText("Reconnected work list").assertIsDisplayed()
            compose.runOnIdle { hub.value = next }
            compose.onNodeWithText("Reconnected work list").assertDoesNotExist()
            compose.runOnIdle { AssistantManagerChoice.choose(next.id, null, "home") }
            compose.onNodeWithText("Next hub list").assertIsDisplayed()
        }
    }
}
