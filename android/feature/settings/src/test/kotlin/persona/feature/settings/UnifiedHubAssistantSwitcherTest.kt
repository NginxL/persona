package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubGroupInfo
import persona.core.contracts.api.HubGroups
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.HubManagers
import persona.core.contracts.api.HubMemberInfo
import persona.core.contracts.api.ManagerCode
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class UnifiedHubAssistantSwitcherTest {
    @get:Rule val rule = createComposeRule()

    @After fun tearDown() { AssistantManagerChoice.reset() }

    private val managersList = listOf(
        HubManager("assistant", "Assistant", online = true, isDefault = true, builtin = true, host = "server"),
        HubManager("m-work", "Work", online = true, host = "Mac Studio"),
    )

    private val fakeManagers = object : HubManagers {
        override suspend fun roster() = ManagerRoster(managersList, "assistant")
        override suspend fun create(name: String) = error("unused")
        override suspend fun newCode(id: String): ManagerCode = error("unused")
        override suspend fun rename(id: String, name: String) = error("unused")
        override suspend fun makeDefault(id: String) = error("unused")
        override suspend fun setHide(id: String, hide: Boolean) = error("unused")
        override suspend fun delete(id: String) = error("unused")
    }

    private class FakeHubGroups(initial: List<HubGroupInfo>) : HubGroups {
        override val groups = MutableStateFlow(initial)
        val selectedIds = mutableListOf<String>()
        override suspend fun select(groupId: String) {
            selectedIds += groupId
            groups.value = groups.value.map { it.copy(selected = it.id == groupId) }
        }
        override suspend fun rename(groupId: String, name: String) = Unit
        override suspend fun removeGroup(groupId: String) = Unit
        override suspend fun removeHub(groupId: String, hubId: String) = Unit
    }

    private fun group(id: String, name: String, selected: Boolean, state: ConnectionState = ConnectionState.Online) =
        HubGroupInfo(id, name, namespace = if (id == "home") null else id, selected = selected, state = state,
            hubs = listOf(HubMemberInfo("h-$id", name, paired = true, connected = state == ConnectionState.Online)))

    @Test fun singleHubKeepsMenuAssistantsOnly() {
        val hubs = FakeHubGroups(listOf(group("home", "Home Hub", selected = true)))
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides ManagerRoster(managersList, "assistant")) {
                    AssistantSwitcher(
                        managers = fakeManagers,
                        waiting = emptyMap(),
                        hubOnline = true,
                        onManage = {},
                        ticking = false,
                        hubs = hubs,
                    )
                }
            }
        }
        rule.onNodeWithText("Assistant").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Work").assertIsDisplayed()
        assertEquals(0, rule.onAllNodesWithText("Hub").fetchSemanticsNodes().size)
        assertEquals(0, rule.onAllNodesWithText("Assistants on Home Hub").fetchSemanticsNodes().size)
    }

    @Test fun multiHubListsHubsAboveCurrentHubAssistantsAndSwitchesHub() {
        val hubs = FakeHubGroups(listOf(
            group("home", "Home Hub", selected = true, state = ConnectionState.Online),
            group("office", "Office Hub", selected = false, state = ConnectionState.Online),
        ))
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides ManagerRoster(managersList, "assistant")) {
                    AssistantSwitcher(
                        managers = fakeManagers,
                        waiting = mapOf("m-work" to 2),
                        hubOnline = true,
                        onManage = {},
                        ticking = false,
                        hubs = hubs,
                    )
                }
            }
        }
        rule.onNodeWithText("Assistant").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Hub").assertIsDisplayed()
        rule.onNodeWithTag("assistant-switcher-hub-home").assertIsDisplayed()
        rule.onNodeWithTag("assistant-switcher-hub-office").assertIsDisplayed()
        rule.onNodeWithText("Assistants on Home Hub").assertIsDisplayed()
        rule.onNodeWithText("Work").assertIsDisplayed()

        rule.onNodeWithTag("assistant-switcher-hub-office").performClick()
        rule.waitForIdle()
        assertEquals(listOf("office"), hubs.selectedIds)
    }

    @Test fun rosterlessMultiHubRendersFallbackHubSwitcher() {
        val hubs = FakeHubGroups(listOf(
            group("home", "Home Hub", selected = true, state = ConnectionState.Online),
            group("office", "Office Hub", selected = false, state = ConnectionState.WaitingForNetwork),
        ))
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides null) {
                    AssistantSwitcher(
                        managers = fakeManagers,
                        waiting = emptyMap(),
                        hubOnline = true,
                        onManage = {},
                        ticking = false,
                        hubs = hubs,
                    )
                }
            }
        }
        rule.onNodeWithText("Home Hub").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-switcher-hub-office").assertIsDisplayed().performClick()
        rule.waitForIdle()
        assertEquals(listOf("office"), hubs.selectedIds)
    }

    @Test fun capturesMultiHubSwitcherLightAndDarkShots() {
        val hubs = listOf(
            group("home", "Home Hub", selected = true, state = ConnectionState.Online),
            group("office", "Office Hub", selected = false, state = ConnectionState.Online),
            group("lab", "Lab Hub", selected = false, state = ConnectionState.WaitingForNetwork),
        )
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = dark, reduceMotion = true) {
                Box(
                    Modifier
                        .testTag("shot")
                        .width(340.dp)
                        .background(PersonaTheme.palette.background)
                        .padding(16.dp),
                ) {
                    androidx.compose.foundation.layout.Column(
                        Modifier
                            .background(PersonaTheme.palette.surfaceOpaque, PersonaTheme.shapes.panelShape)
                            .padding(vertical = 8.dp),
                    ) {
                        HubSwitcherSection(groups = hubs, onPickHub = {})
                        managersList.forEach { manager ->
                            persona.core.designsystem.JAssistantSwitcherRow(
                                manager = manager,
                                isDefault = manager.id == "assistant",
                                status = if (manager.online) "Online · ${manager.host}" else "Offline",
                                current = manager.id == "assistant",
                                badge = if (manager.id == "m-work") 2 else 0,
                                onClick = {},
                                defaultTag = if (manager.id == "assistant") "Default" else null,
                            )
                        }
                    }
                }
            }
        }
        val out = File("build/hub-switcher-shots").also { it.mkdirs() }
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            val bmp = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out, "multi-hub-switcher-${if (d) "dark" else "light"}.png").outputStream().use {
                bmp.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
