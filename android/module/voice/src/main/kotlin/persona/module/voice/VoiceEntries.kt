package persona.module.voice

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.contracts.api.HubExtension
import persona.core.navigation.Navigator
import kotlinx.coroutines.CancellationException

/**
 * Registers the voice page ([VoiceRoute]) on the paired hub's [extensions]. [identity] is the pairing's (its scope path):
 * the picked voices are kept with it. [onModelConfiguration] opens the model configuration of one purpose, given its
 * id (`extension.<extension id>`, [voicePurposeId]); without it the page offers no way there and only says why the model
 * cannot be used.
 */
fun EntryProviderScope<NavKey>.voiceEntries(
    navigator: Navigator,
    extensions: VoiceExtensions,
    identity: String,
    onModelConfiguration: ((String) -> Unit)? = null,
) {
    entry<VoiceRoute> { route ->
        // A new pairing is a new page: nothing of the old one's conversation carries over.
        key(extensions, identity) { VoiceScreen(route, navigator, extensions, identity, onModelConfiguration) }
    }
}

/** The model configuration purpose of a voice extension (docs/assistant/model-config.md): `extension.<extension id>`. */
fun voicePurposeId(extensionId: String): String = "plugin.$extensionId"

/**
 * The hub's running voice extension, for an entry to the voice page: read once each time [active] turns true (and again
 * when the pairing changes, a new [extensions]); null without one, on a hub without extensions, or when it cannot be read.
 */
@Composable
fun rememberVoiceExtension(extensions: VoiceExtensions?, active: Boolean): HubExtension? {
    var extension by remember(extensions) { mutableStateOf<HubExtension?>(null) }
    LaunchedEffect(extensions, active) {
        if (extensions == null || !active) return@LaunchedEffect
        extension = try {
            extensions.voiceExtension()
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            null
        }
    }
    return extension
}
