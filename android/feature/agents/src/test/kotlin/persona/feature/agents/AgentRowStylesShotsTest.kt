package persona.feature.agents

import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentListDisplay
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * The Agents list's row styles on Glass (#4338 A9, iOS `AgentRowStyles.swift`): Avatar, Two Lines, State Cards and
 * Compact List in By Repository and By Status, light and dark, on the sessions of `AgentRowContentShotsTest` (two
 * computers; needs you, working, background waiting, muted, unread, awaiting reply, seen). Then Display in list settings, and
 * Brief's Compact / Detailed left as they were. PNGs in `build/row-styles-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h2000dp-xhdpi")
@org.robolectric.annotation.GraphicsMode(org.robolectric.annotation.GraphicsMode.Mode.NATIVE)
class AgentRowStylesShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val sessions = listOf(
        """"machine_id":"mac","session_id":"needs","title":"Approve the migration","state":"needs_you","cwd":"/code/app","pane_id":"w1:p1",
           "space_id":"s1","space_name":"app","slot_id":"t1","slot_index":1,"model":"claude-opus-4-6","last_text":"Run the database migration on staging?"""",
        """"machine_id":"mac","session_id":"working","title":"Fix the login bug","state":"working","cwd":"/code/app","pane_id":"w1:p2",
           "space_id":"s1","space_name":"app","slot_id":"t2","slot_name":"login","model":"claude-sonnet-4-5",
           "todo":{"id":"t1","title":"Ship 1.2"},"last_text":"Updating the retry logic"""",
        """"machine_id":"mac","session_id":"background","title":"Watch the CI run","state":"working","background_waiting":true,
           "background_working":true,"cwd":"/code/app/.worktrees/ci","pane_id":"w2:p1","space_id":"s2","space_name":"ci","slot_id":"t3",
           "slot_index":1,"agent":"codex","model":"gpt-5-codex","last_text":"Waiting for the test job"""",
        """"machine_id":"mac","session_id":"muted","title":"Side experiment","state":"working","cwd":"/code/notes","pane_id":"w3:p1",
           "space_id":"s3","space_name":"notes","slot_id":"t4","slot_index":1,"model":"claude-haiku-4-5","last_text":"Trying another layout"""",
        """"machine_id":"box","session_id":"unread","title":"Summarize the logs","state":"unread","cwd":"/srv/api","pane_id":"botmux:topic",
           "space_id":"s5","space_name":"api","slot_id":"t5","slot_index":2,"model":"claude-opus-4-6","last_text":"Found three timeouts in the gateway"""",
        """"machine_id":"box","session_id":"awaiting","title":"Pick a release name","state":"idle","awaits_reply":true,"cwd":"/srv/api",
           "pane_id":"w5:p1","space_id":"s5","space_name":"api","slot_id":"t6","slot_index":3,"todo":{"id":"t2","title":"Release 1.2"},
           "last_text":"Which name do you prefer?"""",
        """"machine_id":"box","session_id":"seen","title":"Update the docs","state":"idle","cwd":"/srv/api","pane_id":"w5:p2",
           "space_id":"s5","space_name":"api","slot_id":"t7","slot_name":"docs","agent":"pi","model":"pi-default","last_text":"The docs are updated"""",
    ).map { JsonCodec.decodeFromString(Session.serializer(), """{$it,"updated_at":"2026-01-01T00:00:00Z"}""") }

    private val store = object : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences) =
            transform(data.value).also { data.value = it }
    }
    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)

    @Test fun glassLight() = shoot(dark = false)
    @Test fun glassDark() = shoot(dark = true)

    /** Glass offers iOS's four under Display, one checked; Brief keeps Compact and Detailed (decision 8). */
    @Test fun displayInTheListSettings() {
        val model = open()
        val out = File(System.getProperty("persona.shots") ?: "build/row-styles-shots").also { it.mkdirs() }
        rule.reselectAgentsTab()
        rule.waitForIdle()
        for (d in listOf(false, true)) {
            rule.runOnIdle { dark = d }
            rule.waitForIdle()
            rule.onNode(isDialog()).captureToImage().asAndroidBitmap().let { bitmap ->
                out.resolve("menu-glass-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
        for (label in listOf("Display", "Avatar", "Two lines", "State cards", "Compact list")) {
            assertEquals(label, 1, rule.onAllNodesWithText(label).fetchSemanticsNodes().size)
        }
        assertEquals(0, rule.onAllNodesWithText("Detailed list").fetchSemanticsNodes().size)
        rule.onNodeWithText("State cards").performClick()
        rule.waitForIdle()
        rule.waitUntil(5_000) { model.page.value.preferences.display == AgentListDisplay.Cards }
        rule.runOnIdle { style = AppStyle.Brief }
        rule.onNodeWithContentDescription("List settings").performClick()
        rule.waitForIdle()
        assertEquals(1, rule.onAllNodesWithText("Detailed list").fetchSemanticsNodes().size)
        assertEquals(0, rule.onAllNodesWithText("State cards").fetchSemanticsNodes().size)
    }

    /** TalkBack hears each style's words: State Cards' state at the foot, Two Lines' source after its words. */
    @Test fun talkBackHearsWhatEachStyleShows() {
        val model = open()
        fun said(title: String) = rule.onNode(hasText(title) and SemanticsMatcher.keyIsDefined(SemanticsActions.OnLongClick))
            .fetchSemanticsNode().config.let { it.getOrNull(SemanticsProperties.ContentDescription).orEmpty() + it.getOrNull(SemanticsProperties.Text).orEmpty().map { t -> t.text } }
        rule.runOnIdle { model.grouping(AgentGrouping.Repo); model.display(AgentListDisplay.Cards) }
        rule.waitForIdle()
        val working = said("Fix the login bug")
        assertTrue(working.toString(), "Working" in working)
        assertTrue(working.toString(), "To-do: Ship 1.2" in working)
        assertTrue(working.toString(), "Sonnet 4.5" in working)
        assertTrue("Awaiting reply" in said("Pick a release name"))
        rule.runOnIdle { model.display(AgentListDisplay.TwoLines) }
        rule.waitForIdle()
        val needs = said("Approve the migration")
        assertTrue(needs.toString(), "Needs you  Run the database migration on staging?" in needs)
        assertTrue(needs.toString(), "Terminal" in needs)
        // Two Lines has no line for the model.
        assertTrue(said("Fix the login bug").none { "Sonnet" in it })
    }

    private fun open(): AgentsViewModel {
        val model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(this@AgentRowStylesShotsTest.sessions)
            override val machines = MutableStateFlow(listOf(Machine("mac", "MacBook", online = true), Machine("box", "Workstation", online = true, icon = "desktop")))
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
        }, AgentsPreferences(store, object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot(mutes = HubPreferenceSet(sessions = listOf("mac/muted"))))
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalHomeChrome provides agentsHomeChrome) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
        return model
    }

    private fun shoot(dark: Boolean) {
        this.dark = dark
        val model = open()
        val out = File(System.getProperty("persona.shots") ?: "build/row-styles-shots").also { it.mkdirs() }
        fun save(name: String) = rule.onRoot().captureToImage().asAndroidBitmap().let { bitmap ->
            out.resolve("$name-glass-${if (dark) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        for (display in AgentListDisplay.entries) {
            rule.runOnIdle { model.display(display) }
            rule.waitUntil(5_000) { model.page.value.preferences.display == display }
            for ((grouping, name) in listOf(AgentGrouping.Repo to "by-repository", AgentGrouping.State to "by-status")) {
                rule.runOnIdle { model.grouping(grouping) }
                rule.waitUntil(5_000) { model.page.value.preferences.grouping == grouping }
                rule.waitForIdle()
                save("${display.name.lowercase()}-$name")
            }
        }
    }

    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
}
