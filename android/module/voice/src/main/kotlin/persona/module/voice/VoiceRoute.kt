package persona.module.voice

import androidx.navigation3.runtime.NavKey
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Voice chat with the hub's voice extension [extensionId] (docs/extensions/extensions.md "Voice"); [title] is the extension's name, if
 * known. It moved here from core:navigation with the module (#13433). Navigation 3 saves routes by JVM class name, not by
 * this serial name, so a stack saved under the old class name is not restored: the shell's restorable back stack drops
 * that entry (rememberRestorableNavBackStack) instead of crashing.
 */
@Serializable @SerialName("persona.core.navigation.VoiceRoute")
data class VoiceRoute(val extensionId: String, val title: String = "") : NavKey
