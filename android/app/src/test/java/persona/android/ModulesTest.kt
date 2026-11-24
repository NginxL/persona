package persona.android

import androidx.navigation3.runtime.NavEntry
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import persona.core.moduleapi.ModuleNavContext
import persona.core.moduleapi.ModuleRegistry
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * Modules.kt, the only list of modules (docs/android/android.md "Modules"). These tests name no module's code, so they
 * also run on a build without modules (`-Ppersona.modules=`).
 */
class ModulesTest {
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    /** The keys [registry]'s modules leave to the fallback: pages nobody registered. */
    private fun unregistered(registry: ModuleRegistry, keys: Collection<NavKey>): List<NavKey> {
        val missing = mutableListOf<NavKey>()
        val provider = entryProvider<NavKey>(fallback = { key -> missing += key; NavEntry(key) {} }) {
            registry.active(emptySet()).forEach { it.navEntries(this, ModuleNavContext(navigator, null, unavailable = {})) }
        }
        keys.forEach { val _ = provider(it) }
        return missing
    }

    @Test fun theBuildCarriesWhatPersonaModulesSays() {
        assertEquals(BuildConfig.PERSONA_MODULES.toSet(), Modules.carried.modules.map { it.id }.toSet())
    }

    @Test fun everyModuleProjectIsInTheListAndNothingElse() {
        val settings = generateSequence(File("").absoluteFile) { it.parentFile }
            .map { File(it, "settings.gradle.kts") }.first { it.isFile && File(it.parentFile, "module").isDirectory }
        val projects = Regex("\"(:module:[a-z0-9-]+)\"").findAll(settings.readText()).map { it.groupValues[1].removePrefix(":module:") }.toSet()
        assertEquals(projects, Modules.known.map { it.first }.toSet())
    }

    @Test fun withoutModulesThereIsNoModulePageAndNoMenuAction() {
        val none = Modules.carrying(emptySet())
        assertTrue(none.active(setOf("extensions")).isEmpty())
        // Nothing to add to the assistant's title menu, and no page: the default build's module pages fall through.
        val pages = if ("voice" in BuildConfig.PERSONA_MODULES) Modules.carrying(setOf("voice")).debugPages().values else emptyList()
        assertEquals(pages.toList(), unregistered(none, pages))
    }

    @Test fun theVoiceModuleRegistersItsPageOnlyThroughTheList() {
        assumeTrue("voice is carried by this build", "voice" in BuildConfig.PERSONA_MODULES)
        val voice = Modules.carrying(setOf("voice"))
        val page = voice.debugPages().getValue("voice")
        assertEquals(emptyList<NavKey>(), unregistered(voice, listOf(page)))
        assertEquals(listOf(page), unregistered(Modules.carrying(emptySet()), listOf(page)))
    }

    @Test fun anUnknownModuleIsABuildMistake() {
        try { Modules.carrying(setOf("not-a-module")); throw AssertionError("accepted") } catch (_: IllegalArgumentException) {}
    }
}
