package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable @SerialName("persona.core.navigation.MachinesRoute") data object MachinesRoute : NavKey
/**
 * Machines › "Add computer" (docs/assistant/onboarding.md 4.2); with [confirm], "Confirm computer" for that computer ("" for the first one waiting).
 */
@Serializable @SerialName("persona.core.navigation.AddComputerRoute") data class AddComputerRoute(val confirm: String? = null) : NavKey
@Serializable @SerialName("persona.core.navigation.MachineDetailRoute") data class MachineDetailRoute(
    val machineId: String,
    /** The history range named by a hub link; null keeps the screen's default and the route's old saved form. */
    val range: String? = null,
) : NavKey
/**
 * One computer's projects (iOS `MachineRoute`, `MachineSpacesView`, #4338 A11): its herdr spaces, the ones with something going
 * on first with their agents' counts and latest line, the idle ones below; a space opens its page ([SpaceRoute]). A wide
 * window shows it beside the Agents list, a phone pushes it; its toolbar opens [MachineDetailRoute].
 */
@Serializable @SerialName("persona.core.navigation.MachineSpacesRoute") data class MachineSpacesRoute(val machineId: String) : NavKey

/** Which terminal engine a machine uses (docs/integrations/engines.md), opened from its page. */
@Serializable @SerialName("persona.core.navigation.EngineRoute") data class EngineRoute(val machineId: String) : NavKey
