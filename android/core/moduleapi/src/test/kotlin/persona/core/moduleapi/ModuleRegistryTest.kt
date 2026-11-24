package persona.core.moduleapi

import androidx.navigation3.runtime.NavKey
import kotlinx.coroutines.flow.MutableStateFlow
import okhttp3.OkHttpClient
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class ModuleRegistryTest {
    private object Page : NavKey

    private class Example(override val id: String, override val feature: String? = null, private val keeps: Boolean = true) : PersonaModule {
        var built = 0
        override fun services(hub: ModuleHub): ModuleServices? = if (keeps) object : ModuleServices {}.also { built++ } else null
        override fun debugPages(): Map<String, NavKey> = mapOf(id to Page)
    }

    private val hub = object : ModuleHub {
        override val identity = "pairing"
        override val features = MutableStateFlow(emptySet<String>())
        override val token = "fixture-token"
        override val http = OkHttpClient()
        override fun address(): String? = null
        override fun isCurrent() = true
    }

    private val plain = Example("plain")
    private val gated = Example("knowledge-ui", feature = ModuleRegistry.moduleFeature("knowledge-ui"))
    private val registry = ModuleRegistry(listOf(plain, gated))

    @Test fun theFlagNamesAreTheOnesEveryClientUses() {
        assertEquals("modules", ModuleRegistry.MODULES_MARKER)
        assertEquals("module.knowledge-ui", ModuleRegistry.moduleFeature("knowledge-ui"))
    }

    @Test fun beforeAHelloAndInDemoModeEveryModuleIsOn() {
        // No hello yet and demo mode both read an empty feature set.
        assertEquals(listOf(plain, gated), registry.active(emptySet()))
    }

    @Test fun anOlderHubWithoutTheMarkerLeavesEveryModuleOn() {
        assertEquals(listOf(plain, gated), registry.active(setOf("extensions", "inbox_sources")))
    }

    @Test fun aHubThatPublishesModuleFlagsTurnsAModuleOnWithItsFlag() {
        assertEquals(listOf(plain, gated), registry.active(setOf("modules", "module.knowledge-ui", "extensions")))
    }

    @Test fun aHubThatPublishesModuleFlagsTurnsAModuleOffWithoutItsFlag() {
        assertEquals(listOf(plain), registry.active(setOf("modules", "module.voice")))
        assertFalse(ModuleRegistry.isActive(gated, setOf("modules")))
        // A module that needs no feature stays on wherever it is carried.
        assertTrue(ModuleRegistry.isActive(plain, setOf("modules")))
    }

    @Test fun aFlagWithoutTheMarkerChangesNothing() {
        assertEquals(listOf(plain, gated), registry.active(setOf("module.voice")))
    }

    @Test fun settingsPagesComeOnlyFromActiveModules() {
        val page = ModuleSettingsPage("knowledge-ui.settings", 0, persona.core.designsystem.icons.PersonaIcons.options, Page)
        val withPage = object : PersonaModule {
            override val id = "knowledge-ui"
            override val feature = ModuleRegistry.moduleFeature(id)
            override fun settingsPages() = listOf(page)
        }
        val registry = ModuleRegistry(listOf(plain, withPage))
        assertEquals(listOf(page), registry.settingsPages(emptySet()))
        assertEquals(listOf(page), registry.settingsPages(setOf("modules", "module.knowledge-ui")))
        assertTrue(registry.settingsPages(setOf("modules")).isEmpty())
        assertTrue(ModuleRegistry.Empty.settingsPages(emptySet()).isEmpty())
    }

    @Test fun anEmptyListIsPersonaCoreAlone() {
        assertTrue(ModuleRegistry.Empty.active(setOf("anything")).isEmpty())
        assertTrue(ModuleRegistry.Empty.services(hub).isEmpty())
        assertTrue(ModuleRegistry.Empty.debugPages().isEmpty())
    }

    @Test fun servicesAreBuiltOncePerPairingAndOnlyForModulesThatKeepSome() {
        val keeps = Example("voice")
        val none = Example("glasses", keeps = false)
        val services = ModuleRegistry(listOf(keeps, none)).services(hub)
        assertEquals(setOf("voice"), services.keys)
        assertEquals(1, keeps.built)
    }

    @Test fun debugPagesComeFromEveryCarriedModule() {
        assertEquals(mapOf("voice" to Page, "mail" to Page), ModuleRegistry(listOf(Example("voice"), Example("mail"))).debugPages())
    }

    @Test fun idsAreLowerCaseHyphenatedAndUnique() {
        for (bad in listOf("Voice", "voice_chat", "-voice", "voice-", "", "phone extra")) {
            try { ModuleRegistry(listOf(Example(bad))); fail("'$bad' was accepted") } catch (_: IllegalArgumentException) {}
        }
        ModuleRegistry(listOf(Example("phone-extra"), Example("engines-extra2")))
        try { ModuleRegistry(listOf(Example("voice"), Example("voice"))); fail("A module listed twice was accepted") }
        catch (_: IllegalArgumentException) {}
    }

    @Test fun theContractsDefaultsAddNothing() {
        val bare = object : PersonaModule { override val id = "bare" }
        assertEquals(null, bare.feature)
        assertEquals(null, bare.services(hub))
        assertTrue(bare.settingsPages().isEmpty())
        assertTrue(bare.audioUses.isEmpty())
        assertTrue(bare.debugPages().isEmpty())
        val arguments = mutableMapOf("debug.voiceAudio" to "fake")
        bare.debugArguments(arguments)
        assertEquals(mapOf("debug.voiceAudio" to "fake"), arguments)
    }

    @Test fun audioUsesRankCallConversationRecordingPlayback() {
        assertEquals(listOf(AudioUse.Call, AudioUse.Conversation, AudioUse.Recording, AudioUse.Playback), AudioUse.entries)
        assertTrue(AudioUse.Call.outranks(AudioUse.Conversation))
        assertTrue(AudioUse.Conversation.outranks(AudioUse.Recording))
        assertFalse(AudioUse.Playback.outranks(AudioUse.Recording))
        assertFalse(AudioUse.Call.outranks(AudioUse.Call))
    }

    @Test fun theRegistryKeepsTheListsOrder() {
        val first = Example("b"); val second = Example("a")
        val registry = ModuleRegistry(listOf(first, second))
        assertSame(first, registry.modules[0])
        assertSame(second, registry.active(emptySet())[1])
    }
}
