package persona.android.ui

import android.app.Application
import android.content.ComponentName
import android.content.Context
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.LarkFeatures
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantConversations
import persona.core.contracts.api.AssistantSnapshot
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.NavItem
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SidebarLayout
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.feature.assistant.AssistantSidebarContent
import persona.feature.assistant.AssistantSidebarShade
import persona.feature.assistant.MergedSidebarContent

/** The real Separate and Merged sidebars with the device preference off and on in every appearance. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class LarkMessagesShellShotsTest {
    @get:Rule val rule = createEmptyComposeRule()

    @Test fun messagesPlaceOffAndOnInEveryStyleThemeAndTextSize() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        app.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE).edit().clear().commit()
        val store = AppStyleStore(app)
        assertFalse(store.larkHomeMessages.value)
        store.setLarkHomeMessages(true)
        assertFalse(larkMessagesVisible(true, LarkFeatures.None, available = true))
        assertFalse(larkMessagesVisible(true, LarkFeatures.All, available = false))
        assertTrue(larkMessagesVisible(true, LarkFeatures.All, available = false, loading = true))
        assertTrue(store.larkHomeMessages.value)
        store.setLarkHomeMessages(false)

        val operations = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
        val snapshot = AssistantSnapshot()
        val repository = object : AssistantConversations {
            override val assistant = MutableStateFlow(snapshot)
            override val pinned = MutableStateFlow(emptySet<SessionKey>())
            override suspend fun refresh() = Unit
            override suspend fun startConversation() = SessionKey("computer", "assistant")
        }
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableStateOf(1f)
        var layout by mutableStateOf(SidebarLayout.Separate)
        val opened = mutableListOf<String>()
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        try {
            ActivityScenario.launch(ComponentActivity::class.java).use { scenario ->
                scenario.onActivity { activity -> activity.setContent {
                    val enabled by store.larkHomeMessages.collectAsState()
                    val visible = larkMessagesVisible(enabled, LarkFeatures.All, available = true)
                    val pages = buildList {
                        if (visible) add(NavItem("messages", "Messages", PersonaIcons.messagesApp))
                        add(NavItem("machines", "Computers", PhIcons.Desktop))
                    }
                    CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                        PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                            Box(Modifier.testTag("shot").width(400.dp).height(1200.dp).background(PersonaTheme.palette.background)) {
                                val merged = if (layout == SidebarLayout.Merged) MergedSidebarContent(
                                    list = { listState, prefix, footer, _ ->
                                        LazyColumn(Modifier.fillMaxSize().testTag("merged-sidebar-list"), state = listState) {
                                            prefix()
                                            item { footer() }
                                        }
                                    },
                                    usage = {},
                                    agents = emptyList(),
                                    agentMachines = emptyMap(),
                                    pinnedAgents = emptySet(),
                                    onSearchAgent = {},
                                    agentsInFront = false,
                                    showKnowledge = true,
                                ) else null
                                AssistantSidebarContent(
                                    snapshot, repository, null, operations, demo = false,
                                    onOpen = {}, onAction = {}, onSettings = {}, onInbox = {}, onKnowledge = {},
                                    onTodos = null, todoCount = 0, showTodos = false, inboxCount = 0,
                                    settingsCount = 0, settingsDot = false, settingsBadge = null,
                                    pages = pages, onPage = { opened += it }, onRetired = {},
                                    shadedPlace = AssistantSidebarShade.None,
                                    merged = merged,
                                )
                            }
                        }
                    }
                } }
                val clickChecked = mutableSetOf<SidebarLayout>()
                for (sidebarLayout in SidebarLayout.entries) {
                    for (enabled in listOf(false, true)) for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
                        for ((textSize, fontScale) in listOf("default" to 1f, "text-200" to 2f)) {
                            rule.runOnIdle {
                                store.setLarkHomeMessages(enabled)
                                style = appearance
                                dark = night
                                scale = fontScale
                                layout = sidebarLayout
                            }
                            rule.waitForIdle()
                            if (sidebarLayout == SidebarLayout.Merged) {
                                rule.onNodeWithTag("merged-sidebar-list").performScrollToNode(hasText("Computers"))
                            }
                            rule.onNodeWithText("Computers").assertExists()
                            if (enabled) {
                                rule.onNodeWithText("Messages").assertExists()
                                val healthTop = rule.onNodeWithText("Health").fetchSemanticsNode().boundsInRoot.top
                                val messagesTop = rule.onNodeWithText("Messages").fetchSemanticsNode().boundsInRoot.top
                                val computersTop = rule.onNodeWithText("Computers").fetchSemanticsNode().boundsInRoot.top
                                assertTrue("Messages must follow Health", healthTop < messagesTop)
                                assertTrue("Messages must precede Computers", messagesTop < computersTop)
                            } else {
                                rule.onNodeWithText("Messages").assertDoesNotExist()
                            }
                            val state = if (enabled) "on" else "off"
                            capture("lark-shell-${sidebarLayout.key}-$state-${appearance.name.lowercase()}-${if (night) "dark" else "light"}-$textSize")
                            if (enabled && clickChecked.add(sidebarLayout)) {
                                val previous = opened.size
                                rule.onNodeWithText("Messages").performClick()
                                assertEquals(listOf("messages"), opened.drop(previous))
                            }
                        }
                    }
                }
                assertEquals(SidebarLayout.entries.toSet(), clickChecked)
            }
        } finally {
            operations.cancel()
        }
    }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
            if (bitmap.getPixel(x, y) != background) other++
        }
        assertTrue("$name drew nothing", other > 100)
        val out = File(System.getProperty("persona.shots") ?: "build/lark-shell-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use {
            assertTrue("$name could not be written", bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }
}
