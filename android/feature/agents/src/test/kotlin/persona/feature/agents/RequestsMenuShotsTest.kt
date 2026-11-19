package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * Where "Requests and history" is (#4338 A1): on Glass a page in list settings after Machines; Brief and Board keep
 * the list row (decision 8). The page and its list settings sheet in every style,
 * light and dark, written to `build/requests-menu-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
class RequestsMenuShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/requests-menu-shots").also { it.mkdirs() }

    private val sessions = listOf(
        Session("mac", "s1", title = "Fix login page", cwd = "/code/app", state = "working", lastText = "Updating the retry logic"),
        Session("mac", "s2", title = "Release checklist", cwd = "/code/app", state = "unread", lastText = "Summary is ready"),
    )

    @Test fun page() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        show({ style }, { dark })
        // The list first, then its settings sheet in a separate window.
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            save(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "list", s, d)
        }
        rule.runOnIdle { style = AppStyle.Glass }
        rule.reselectAgentsTab()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            save(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "menu", s, d)
            // One way there in every style: the item in the sheet on Glass, the row under it on Brief and Board.
            assertEquals("$s", 1, rule.onAllNodesWithText("Requests and history").fetchSemanticsNodes().size)
        }
    }

    private fun save(bitmap: Bitmap, name: String, style: AppStyle, dark: Boolean) =
        File(out(), "requests-$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }

    private fun show(style: () -> AppStyle, dark: () -> Boolean) {
        val model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(this@RequestsMenuShotsTest.sessions)
            override val machines = MutableStateFlow(listOf(Machine("mac", "MacBook Pro", online = true)))
            override suspend fun refresh() = Unit
        }, object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }, object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }, object : MessageSender {
            override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }, AgentsPreferences(object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences) =
                transform(data.value).also { data.value = it }
        }, object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }))
        rule.setContent {
            PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                CompositionLocalProvider(LocalHomeChrome provides agentsHomeChrome) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        AgentsScreen(model, Nowhere, AgentsMetadata())
                    }
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
    }

    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
}
