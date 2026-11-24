package persona.feature.assistant

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf

/** Navigation requests the sidebar hands to the Assistant page, consumed when it shows. */
enum class AssistantSidebarAction { NewConversation, MainConversation, ScheduledTasks, SessionInfo, Launcher, Health,
    /** A domain space's row or New space (#13500): the page opens [AssistantSidebarHost.openSpace]. */
    Space }

/** The shell owns one history sidebar, independent of the Assistant page's navigation lifetime. */
class AssistantSidebarHost {
    val opened = mutableStateOf(false)
    var personaState by mutableStateOf(persona.core.designsystem.PersonaState.Idle)
    var active by mutableStateOf<persona.core.contracts.SessionKey?>(null)
    var healthInFront by mutableStateOf(false)
    /** The domain space whose chat or home is in front (#13500), shading its Spaces row; null: none. */
    var spaceInFront by mutableStateOf<String?>(null)
    /** The space a [AssistantSidebarAction.Space] opens: its slug, or [SpacesModel.NEW] for the new space form. */
    var openSpace by mutableStateOf<String?>(null)
    private val spaces = mutableMapOf<String?, SpacesModel>()
    /** The spaces as last read for [hub], one list for the sidebar and the page. */
    fun spaces(hub: String?): SpacesModel = spaces.getOrPut(hub) { SpacesModel() }
    var action by mutableStateOf<AssistantSidebarAction?>(null)
    var retired by mutableStateOf(emptySet<persona.core.contracts.SessionKey>())
}

val LocalAssistantSidebarHost = staticCompositionLocalOf<AssistantSidebarHost?> { null }

/**
 * The sidebar's head with the assistant switcher, drawn by the shell (docs/assistant/managers.md "Clients"): the head's own
 * padding, the head's other button (the compact sidebar's search), and the head as it was for where no switcher shows
 * (demo mode, a hub without a roster).
 */
typealias AssistantSwitcherSlot = @androidx.compose.runtime.Composable (
    head: androidx.compose.ui.Modifier,
    trailing: (@androidx.compose.runtime.Composable () -> Unit)?,
    fallback: @androidx.compose.runtime.Composable () -> Unit,
) -> Unit
