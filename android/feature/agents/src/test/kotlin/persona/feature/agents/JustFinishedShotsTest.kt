package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onRoot
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentListDisplay
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * Just finished (#9786): a turn the hub's triage found done before anybody looked, beside a working, a seen and a muted
 * one. By Status (its own section after Working, before Seen) and By repository (its repository stays open, the row
 * says "Just finished" in Unread's color with the triage's "✓ conclusion"), on Glass as Two lines and State cards too. Every
 * style, light and dark. PNGs in `build/just-finished-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class JustFinishedShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun session(id: String, title: String, state: String, words: String, cwd: String = "/code/app", work: WorkState? = null) =
        Session("mac", id, title = title, cwd = cwd, state = state, agent = "claude", spaceId = "w1", spaceName = "app",
            slotName = id, paneId = "w1:$id", lastText = words, work = work, updatedAt = "2026-01-01T00:00:00Z")

    private val sessions = listOf(
        session("working", "Fix the login bug", "working", "Updating the retry logic."),
        session("finished", "Ship the release notes", "idle", "Done.",
            work = WorkState("done", line = "Release notes merged", justFinished = true)),
        session("quiet", "Tidy the changelog", "idle", "Done.", work = WorkState("done", line = "Changelog tidied", justFinished = true)),
        session("seen", "Update the docs", "idle", "The guide is updated.", cwd = "/code/docs"),
    )

    @Test fun everyStyle() {
        val model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(this@JustFinishedShotsTest.sessions)
            override val machines = MutableStateFlow(listOf(Machine("mac", "MacBook Pro", online = true, icon = "laptop")))
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
            // The second one is muted: plain Seen.
            override val preferences = MutableStateFlow(HubPreferencesSnapshot(mutes = HubPreferenceSet(sessions = listOf("mac/quiet"))))
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "Agents", onSelect = {}, leading = {})) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
        val out = File(System.getProperty("persona.shots") ?: "build/just-finished-shots").also { it.mkdirs() }
        fun save(name: String) {
            rule.waitForIdle()
            val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
            File(out, "$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d; model.grouping(AgentGrouping.State) }
            rule.waitForIdle()
            assertTrue("$s: the section", rule.onAllNodesWithText("Just finished").fetchSemanticsNodes().isNotEmpty())
            save("by-status")
            rule.runOnIdle { model.grouping(AgentGrouping.Repo) }
            rule.waitForIdle()
            assertTrue("$s: the row's word", rule.onAllNodesWithText("Just finished", substring = true).fetchSemanticsNodes().isNotEmpty())
            save("by-repository")
            if (s == AppStyle.Glass) {
                rule.runOnIdle { model.display(AgentListDisplay.TwoLines) }
                save("by-repository-two-lines")
                rule.runOnIdle { model.display(AgentListDisplay.Cards) }
                save("by-repository-cards")
                rule.runOnIdle { model.display(AgentListDisplay.Avatar) }
            }
        }
    }

    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
}
