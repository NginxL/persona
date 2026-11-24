package persona.core.moduleapi

import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.designsystem.icons.PhIcon
import persona.core.navigation.Navigator
import kotlin.reflect.KClass

/**
 * One module of the app (#13430 "Persona Core and modules", docs/android/android.md "Modules"): a feature the owner can
 * go without while the assistant, Agents, to-dos, computers and connectors keep working. A module reaches the core only
 * through this contract and the other `:core:*` modules; the core names no module. The app's `Modules.kt` is the only
 * list of modules, and a module that is not in it, or not enabled on the paired hub ([feature]), is absent: no page, no
 * menu item, nothing left over.
 *
 * Names follow the shared module contract (#13430 section 4): [id] is `id`, [feature] is `feature`, [navEntries] serves
 * `shell_places`, [menuActions] serves `composer_actions` and the other action slots ([MenuSlot]), [settingsPages] is
 * `settings`, [services] is the client half of `hub_routes`. Declared but not yet served by the Android shell:
 * [audioUses] (the audio broker, #13431).
 */
interface PersonaModule {
    /** `id`: lower case and hyphenated, the same on every platform (`voice`, `knowledge-ui`). */
    val id: String

    /**
     * `feature`: the hub hello feature that switches this module, [ModuleRegistry.moduleFeature] (`module.<id>`). On a hub
     * that publishes module flags ([ModuleRegistry.MODULES_MARKER]) and lacks it, the module is inactive (absent, as if
     * not built in); before a hello, in demo mode and on an older hub it stays on ([ModuleRegistry.active]). Null: it
     * needs none and is active on every hub.
     */
    val feature: String? get() = null

    /**
     * The client half of `hub_routes`: what the module keeps for one pairing, built once per pairing (never in demo mode).
     * Null: it keeps nothing. The shell hands it back as [ModuleNavContext.services] and [ModuleUiContext.services].
     */
    fun services(hub: ModuleHub): ModuleServices? = null

    /**
     * `shell_places`: registers the module's pages. [ModuleNavContext.services] is null in demo mode, where a page that
     * needs the hub shows [ModuleNavContext.unavailable] instead.
     */
    fun navEntries(entries: EntryProviderScope<NavKey>, context: ModuleNavContext) {}

    /**
     * `shell_places`, as a list: the routes [navEntries] registers. The shell reads it to show its own way into a module's
     * place (a sidebar or menu item it keeps) only while some active module serves that place ([LocalModulePlaces]).
     */
    val places: Set<KClass<out NavKey>> get() = emptySet()

    /**
     * The module's actions in [slot], read while that surface is composed; none by default. [ModuleUiContext.target] says
     * what a composer, session or message action is about.
     */
    @Composable
    fun menuActions(slot: MenuSlot, context: ModuleUiContext): List<ModuleMenuAction> = emptyList()

    /**
     * `settings`: pages Settings lists for the module, at the end of the assistant's group, while the module is active.
     * Each opens its [ModuleSettingsPage.route], which the module registers in [navEntries].
     */
    fun settingsPages(): List<ModuleSettingsPage> = emptyList()

    /** What the module plays or records ([AudioFocus]). Declared; the Android shell has no broker yet. */
    val audioUses: Set<AudioUse> get() = emptySet()

    /** Debug builds: pages `debug.openPage` can open by name, for captures. */
    fun debugPages(): Map<String, NavKey> = emptyMap()

    /** Debug builds: takes (removes) the `debug.*` launch arguments the module owns from [arguments]. */
    fun debugArguments(arguments: MutableMap<String, String>) {}
}

/** What a module keeps for one pairing ([PersonaModule.services]); [close] runs when the pairing ends. */
interface ModuleServices {
    fun close() {}
}

/** The places a module can add actions to (#13433, #13847); the shell serves each through [LocalModuleActions] or Root. */
enum class MenuSlot {
    /** The assistant's title menu, after Details (a place the module opens: `shell_places`). */
    AssistantTitle,
    /** `composer_actions`: the composer's ⊕ menu, after the quick messages; the target is its conversation. */
    ComposerActions,
    /** `session_action` (#13400): a conversation's ⋯ menu, last; the target is the conversation. */
    SessionActions,
    /** `message_action` (#13400): a message's menu, last; the target is the message and its words. */
    MessageActions,
}

/**
 * One action a module adds to a [MenuSlot]: its words (resolved in [PersonaModule.menuActions], so they follow the
 * language), the icon Glass draws, and what it does.
 */
data class ModuleMenuAction(val id: String, val label: String, val icon: PhIcon, val onClick: () -> Unit)

/** A Settings page of a module ([PersonaModule.settingsPages]): its title, its row's icon and the page it opens. */
data class ModuleSettingsPage(val id: String, @StringRes val title: Int, val icon: PhIcon, val route: NavKey)

/** What [PersonaModule.navEntries] gets from the shell. */
class ModuleNavContext(
    val navigator: Navigator,
    /** The module's [PersonaModule.services] for the paired hub; null in demo mode or when it keeps none. */
    val services: ModuleServices?,
    /** The shell's page for a place that cannot work here (demo mode): the [message] and a way back. */
    val unavailable: @Composable (message: String) -> Unit,
    /** The core's repositories for the paired hub; null in demo mode. */
    val shell: ModuleShell? = null,
)

/** What [PersonaModule.menuActions] gets from the shell. */
class ModuleUiContext(
    val navigator: Navigator,
    /** The module's [PersonaModule.services] for the paired hub; null in demo mode or when it keeps none. */
    val services: ModuleServices?,
    /** The surface with the slot is on screen and the hub reachable: the time to ask the hub what to offer. */
    val active: Boolean,
    /** What a composer, session or message action is about; null for [MenuSlot.AssistantTitle]. */
    val target: ModuleActionTarget? = null,
)
