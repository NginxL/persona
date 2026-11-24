package persona.android.ui

import android.os.Bundle
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavBackStack
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.navigation3.runtime.serialization.NavBackStackSerializer
import androidx.navigation3.runtime.serialization.NavKeySerializer
import androidx.savedstate.serialization.decodeFromSavedState
import androidx.savedstate.serialization.encodeToSavedState
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.Serializable
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.moduleapi.ModuleNavContext
import persona.core.moduleapi.ModuleRegistry
import persona.core.moduleapi.ModuleSettingsPage
import persona.core.moduleapi.AudioUse
import persona.core.moduleapi.PersonaModule
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.Navigator
import persona.core.navigation.RestorableBackStackSerializer

/**
 * Module pages can never crash the shell (#13433): a module page left open when the hub stops offering the module's
 * feature falls through to the shell's unavailable page, and a saved stack naming a class this build lacks (a module left
 * out, an older route) restores without it.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en", application = android.app.Application::class)
class ModuleNavigationSafetyTest {
    @Serializable data object GatedRoute : NavKey
    @Serializable data class KeptRoute(val id: String) : NavKey

    private class Gated(override val id: String = "gated") : PersonaModule {
        override val feature = ModuleRegistry.moduleFeature(id)
        override fun navEntries(entries: EntryProviderScope<NavKey>, context: ModuleNavContext) {
            entries.entry<GatedRoute> {}
        }
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private val logged = mutableListOf<String>()

    private fun provider(registry: ModuleRegistry, features: Set<String>) =
        entryProvider<NavKey>(fallback = { key -> shellFallbackEntry(key, navigator, logged::add) }) {
            moduleEntries(registry.active(features)) { ModuleNavContext(navigator, null, unavailable = {}) }
        }

    @Test fun aModulePageLeftOpenWhenItsFeatureGoesAwayShowsTheUnavailablePage() {
        val registry = ModuleRegistry(listOf(Gated()))
        val _ = provider(registry, setOf(ModuleRegistry.MODULES_MARKER, ModuleRegistry.moduleFeature("gated")))(GatedRoute)
        assertTrue("registered while the hub offers the feature", logged.isEmpty())
        // The hub's hello drops the feature while the page is on the stack: no crash, the shell's page, a loud log.
        val _ = provider(registry, setOf(ModuleRegistry.MODULES_MARKER))(GatedRoute)
        assertEquals(1, logged.size)
        assertTrue(logged.single(), "GatedRoute" in logged.single())
        // What that page draws: ModuleFallbackPageTest (debug unit tests, which have a test activity).
    }

    @Test fun twoModulesRegisteringOneRouteAreRejected() {
        val registry = ModuleRegistry(listOf(Gated("first"), Gated("second")))
        try {
            val _ = provider(registry, emptySet())
            fail("A route registered twice was accepted")
        } catch (_: IllegalArgumentException) {
        }
    }

    @Test fun contributionsTheShellDoesNotServeAreSaidLoudly() {
        val warnings = mutableListOf<String>()
        ModuleRegistry(listOf(object : PersonaModule {
            override val id = "unserved"
            override val audioUses = setOf(AudioUse.Playback)
            // Settings pages are served (#13847): declaring some is not a warning.
            override fun settingsPages() = listOf(ModuleSettingsPage("unserved.page", 0, persona.core.designsystem.icons.PersonaIcons.options, GatedRoute))
        }), warnings::add)
        assertEquals(1, warnings.size)
        assertTrue(warnings.all { "unserved" in it })
    }

    /** Replaces every string [from] in a saved state, as a saved stack from a build with a class this one lacks would read. */
    @Suppress("DEPRECATION")
    private fun Bundle.rename(from: String, to: String) {
        for (key in keySet().toList()) when (val value = get(key)) {
            from -> putString(key, to)
            is Bundle -> value.rename(from, to)
            is ArrayList<*> -> value.filterIsInstance<Bundle>().forEach { it.rename(from, to) }
            is Array<*> -> value.filterIsInstance<Bundle>().forEach { it.rename(from, to) }
        }
    }

    private fun savedWithMissing(vararg keys: NavKey, missing: NavKey): Bundle {
        val serializer = RestorableBackStackSerializer(listOf(AssistantRoute))
        return encodeToSavedState(serializer, NavBackStack(*keys, missing)).apply {
            // The class name Navigation 3 saved, turned into one this build lacks (a module left out, a route that moved).
            rename(missing::class.java.name, "persona.example.gone.GoneRoute")
        }
    }

    @Test fun aSavedRouteWhoseClassIsMissingIsDroppedNotACrash() {
        val saved = savedWithMissing(AssistantRoute, KeptRoute("a"), missing = GatedRoute)
        // Navigation 3's own serializer (what rememberNavBackStack uses) cannot restore it.
        try {
            val _ = decodeFromSavedState(NavBackStackSerializer(NavKeySerializer<NavKey>()), saved)
            fail("Navigation 3 restored a class that does not exist")
        } catch (_: Exception) {
        }
        val dropped = mutableListOf<String>()
        val restored = decodeFromSavedState(RestorableBackStackSerializer(listOf(AssistantRoute), dropped::add), saved)
        assertEquals(listOf(AssistantRoute, KeptRoute("a")), restored.toList())
        assertEquals(listOf("persona.example.gone.GoneRoute"), dropped)
    }

    @Test fun aStackOfOnlyMissingRoutesStartsAgainFromItsRoot() {
        val saved = savedWithMissing(missing = GatedRoute)
        val restored = decodeFromSavedState(RestorableBackStackSerializer(listOf(AssistantRoute)), saved)
        assertEquals(listOf<NavKey>(AssistantRoute), restored.toList())
    }

    @Test fun aStackWithEveryClassPresentRestoresAsItWas() {
        val serializer = RestorableBackStackSerializer(listOf(AssistantRoute))
        val stack = NavBackStack(AssistantRoute, KeptRoute("a"), GatedRoute)
        assertEquals(stack.toList(), decodeFromSavedState(serializer, encodeToSavedState(serializer, stack)).toList())
        // The saved form is Navigation 3's own: its serializer reads what this one wrote.
        assertEquals(stack.toList(),
            decodeFromSavedState(NavBackStackSerializer(NavKeySerializer<NavKey>()), encodeToSavedState(serializer, stack)).toList())
    }
}
