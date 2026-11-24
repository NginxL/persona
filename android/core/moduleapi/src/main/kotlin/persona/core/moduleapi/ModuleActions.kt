package persona.core.moduleapi

import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf

/**
 * What an action in [MenuSlot.ComposerActions], [MenuSlot.SessionActions] or [MenuSlot.MessageActions] is about: the
 * conversation ([machineId] and [sessionId]; both null for the assistant's main chat where it has none), and for a
 * message's menu the message ([messageId], [text]).
 */
@Immutable
data class ModuleActionTarget(
    val machineId: String? = null,
    val sessionId: String? = null,
    val messageId: String? = null,
    val text: String? = null,
)

/**
 * The active modules' actions for a slot, as the shell serves them to the surfaces that have one (the composer's ⊕ menu,
 * a conversation's ⋯ menu, a message's menu) through [LocalModuleActions]. A surface asks only while its menu is open.
 */
interface ModuleActions {
    @Composable
    fun actions(slot: MenuSlot, target: ModuleActionTarget): List<ModuleMenuAction>

    companion object {
        /** No module actions: a surface outside the shell (previews, tests), or a build without modules. */
        val None: ModuleActions = object : ModuleActions {
            @Composable
            override fun actions(slot: MenuSlot, target: ModuleActionTarget): List<ModuleMenuAction> = emptyList()
        }
    }
}

/** The shell's [ModuleActions]; [ModuleActions.None] where no shell provides one. */
val LocalModuleActions = staticCompositionLocalOf { ModuleActions.None }

/** The active modules' Settings pages ([PersonaModule.settingsPages]), as the shell serves them to Settings. */
val LocalModuleSettingsPages = staticCompositionLocalOf { emptyList<ModuleSettingsPage>() }

/** [actions] as menu entries after a surface's own, under a rule (a group of their own); [onPick] runs first (closing the menu). */
fun List<ModuleMenuAction>.asMenuActions(onPick: () -> Unit = {}): List<persona.core.designsystem.JAction> =
    mapIndexed { index, action ->
        persona.core.designsystem.JAction(action.label, action.icon, separatorBefore = index == 0) { onPick(); action.onClick() }
    }

/**
 * The places the active modules serve ([PersonaModule.places]), as the shell provides them; null where no shell says
 * (previews, tests), which shows every way in as before.
 */
val LocalModulePlaces = staticCompositionLocalOf<Set<kotlin.reflect.KClass<out androidx.navigation3.runtime.NavKey>>?> { null }

/** Whether a way into [place] should show: some active module serves it, or no shell says (null). */
fun Set<kotlin.reflect.KClass<out androidx.navigation3.runtime.NavKey>>?.serves(place: kotlin.reflect.KClass<out androidx.navigation3.runtime.NavKey>): Boolean =
    this == null || place in this
