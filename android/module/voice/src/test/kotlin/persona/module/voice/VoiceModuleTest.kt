package persona.module.voice

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.navigation3.runtime.NavEntry
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.icons.PersonaIcons
import persona.core.moduleapi.MenuSlot
import persona.core.moduleapi.ModuleMenuAction
import persona.core.moduleapi.ModuleNavContext
import persona.core.moduleapi.ModuleUiContext
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** The voice module as the shell sees it (#13433): its page and its "Voice chat" item come only through PersonaModule. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class VoiceModuleTest {
    @get:Rule val rule = createComposeRule()
    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private val services = VoiceServices("pairing", FakeExtensions())

    private fun actions(slot: MenuSlot, services: VoiceServices?, active: Boolean): List<ModuleMenuAction> {
        var shown = emptyList<ModuleMenuAction>()
        rule.setContent { shown = VoiceModule.menuActions(slot, ModuleUiContext(navigator, services, active)) }
        rule.waitForIdle()
        return shown
    }

    @Test fun voiceChatIsOfferedWhileTheHubRunsAVoiceExtension() {
        val action = actions(MenuSlot.AssistantTitle, services, active = true).single()
        val words = ApplicationProvider.getApplicationContext<android.content.Context>().getString(R.string.voice_menu_chat)
        assertEquals(words, action.label)
        assertTrue(action.icon === PersonaIcons.voiceChannel)
        action.onClick()
        assertEquals(listOf<NavKey>(VoiceRoute("voice", "语音")), opened)
    }

    @Test fun nothingInDemoMode() {
        assertTrue(actions(MenuSlot.AssistantTitle, null, active = true).isEmpty())
    }

    @Test fun nothingWhileTheSurfaceIsAway() {
        assertTrue(actions(MenuSlot.AssistantTitle, services, active = false).isEmpty())
    }

    @Test fun nothingInSlotsItDoesNotServe() {
        assertTrue(actions(MenuSlot.ComposerActions, services, active = true).isEmpty())
    }

    @Test fun itsPageIsRegisteredWithAndWithoutAHub() {
        for (hub in listOf(services, null)) {
            val missing = mutableListOf<NavKey>()
            val provider = entryProvider<NavKey>(fallback = { key -> missing += key; NavEntry(key) {} }) {
                VoiceModule.navEntries(this, ModuleNavContext(navigator, hub, unavailable = {}))
            }
            val _ = provider(VoiceRoute("voice"))
            assertTrue(missing.isEmpty())
        }
        assertEquals(mapOf<String, NavKey>("voice" to VoiceRoute("voice", "语音")), VoiceModule.debugPages())
    }

    @Test fun debugArgumentsTakeOnlyItsOwn() {
        val arguments = mutableMapOf("debug.voiceAudio" to "none", "debug.theme" to "dark")
        VoiceModule.debugArguments(arguments)
        assertEquals(mapOf("debug.theme" to "dark"), arguments)
    }
}
