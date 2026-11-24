package persona.feature.settings

import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ExtensionRowControl
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiResult
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewSection
import persona.core.contracts.api.ManagerPins
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.InboxItemUiRoute
import persona.core.inbox.InboxItemKey

/** An extension-drawn inbox item's body is drawn and worked by the item's manager, under the item's id on the hub. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxItemBodyManagerTest {
    @get:Rule val rule = createComposeRule()

    @Before fun choose() = AssistantManagerChoice.choose(null, null, "assistant")
    @After fun reset() { AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems() }

    private data class Call(val name: String, val item: String?, val pins: ManagerPins?)

    private class Recording : ExtensionApiStub() {
        val calls = mutableListOf<Call>()
        override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?): ExtensionView {
            calls += Call("render", context?.item, currentCoroutineContext()[ManagerPins])
            return ExtensionView(title = "Event", sections = listOf(ExtensionViewSection(items = listOf(ExtensionViewItem("row", title = "Lamp",
                control = ExtensionRowControl("switch", "lamp", on = false, action = ExtensionViewAction("toggle")))))))
        }
        override suspend fun uiAction(extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?,
            values: Map<String, String>?): ExtensionUiResult {
            calls += Call(action, context?.item, currentCoroutineContext()[ManagerPins])
            return ExtensionUiResult(toast = "Done")
        }
    }

    private val hubId = "lark:F1:1051"
    private val ui = listOf("extensions", "lark-events", "-", "ui", "event")

    private fun body(api: Recording, itemId: String, manager: () -> String?) {
        val slots = ExtensionSlots(api, flowOf(true), MutableSharedFlow())
        val route = InboxItemUiRoute(itemId, "lark-events", "event")
        rule.setContent { PersonaTheme(reduceMotion = true) {
            val body = rememberInboxItemBody(slots, route, manager = manager(), hubItemId = InboxItemKey.hubId(itemId), onSource = {})
            LazyColumn { body.items(this) }
        } }
        rule.waitUntil { api.calls.isNotEmpty() }
    }

    private fun flip(api: Recording) {
        val before = api.calls.size
        rule.onNode(hasText("Lamp") and hasClickAction()).performClick()
        rule.waitUntil { api.calls.drop(before).any { it.name == "toggle" } }
    }

    @Test fun anotherManagersItemIsDrawnAndWorkedByItsManagerUnderItsHubId() {
        val api = Recording()
        body(api, InboxItemKey.of(hubId, "m-3")) { "m-3" }
        flip(api)
        rule.runOnIdle {
            val render = api.calls.first { it.name == "render" }
            val toggle = api.calls.first { it.name == "toggle" }
            assertEquals(hubId, render.item)
            assertEquals("m-3", render.pins?.manager(ui))
            assertEquals(hubId, toggle.item)
            assertEquals("m-3", toggle.pins?.manager(ui))
        }
    }

    @Test fun theDefaultsItemNamesNoManager() {
        val api = Recording()
        var manager by mutableStateOf<String?>(null)
        body(api, hubId) { manager }
        flip(api)
        rule.runOnIdle {
            assertEquals(listOf(Call("render", hubId, null), Call("toggle", hubId, null)), api.calls.take(2))
            api.calls.clear()
            manager = "assistant"
        }
        rule.waitForIdle()
        rule.waitUntil { api.calls.isNotEmpty() }
        flip(api)
        rule.runOnIdle {
            // The default's stamp: an empty name, which the hub client drops (no header, no `?manager=`).
            api.calls.forEach { assertEquals(hubId, it.item); assertEquals("", it.pins?.manager(ui)) }
            assertNull(api.calls.first().pins?.manager(listOf("extensions", "notes")))
        }
    }
}
