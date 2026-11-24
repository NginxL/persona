package persona.module.voice

import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import kotlinx.coroutines.CancellationException
import persona.core.designsystem.icons.PersonaIcons
import persona.core.moduleapi.MenuSlot
import persona.core.moduleapi.ModuleHub
import persona.core.moduleapi.ModuleMenuAction
import persona.core.moduleapi.ModuleNavContext
import persona.core.moduleapi.ModuleRegistry
import persona.core.moduleapi.ModuleServices
import persona.core.moduleapi.ModuleUiContext
import persona.core.moduleapi.PersonaModule
import persona.core.navigation.ModelPurposeRoute

/**
 * Full-duplex voice chat with the hub's voice extension, as a module (#13431, #13433; docs/android/android.md "Voice chat"):
 * the voice page ([VoiceRoute]) and "Voice chat" in the assistant's title menu while the hub runs a voice extension. A hub
 * that publishes module flags switches it with `module.voice` (ModuleRegistry.active); on others it stays on and its menu
 * item follows the running extension.
 */
object VoiceModule : PersonaModule {
    override val id = "voice"
    override val feature = ModuleRegistry.moduleFeature(id)

    override fun services(hub: ModuleHub): ModuleServices = VoiceServices(hub.identity, object : VoiceExtensions {
        // Like machine metrics: each call follows the live route with the pairing's pinned client and token.
        private fun api(): VoiceExtensions {
            if (!hub.isCurrent()) throw CancellationException("Pairing changed")
            val address = hub.address() ?: throw persona.core.hub.HubFailure(persona.core.hub.HubError.NoNetwork)
            return HttpVoiceExtensions(address, hub.token, hub.http, hub::isCurrent,
                legacy = { "extensions" !in hub.features.value })
        }
        override suspend fun voiceExtension() = api().voiceExtension()
        override suspend fun voices(extensionId: String, model: String?) = api().voices(extensionId, model)
        override suspend fun models(extensionId: String) = api().models(extensionId)
        override suspend fun modelChoice(extensionId: String) = api().modelChoice(extensionId)
        override fun connect(extensionId: String, listener: VoiceSocketListener) = api().connect(extensionId, listener)
    })

    override fun navEntries(entries: EntryProviderScope<NavKey>, context: ModuleNavContext) {
        val services = context.services as? VoiceServices
        if (services == null) {
            // Demo mode: the page is there, saying why it cannot work.
            entries.entry<VoiceRoute> { context.unavailable(stringResource(R.string.voice_demo_unavailable)) }
        } else {
            entries.voiceEntries(context.navigator, services.extensions, services.identity,
                onModelConfiguration = { purpose -> context.navigator.navigate(ModelPurposeRoute(purpose)) })
        }
    }

    @Composable
    override fun menuActions(slot: MenuSlot, context: ModuleUiContext): List<ModuleMenuAction> {
        if (slot != MenuSlot.AssistantTitle) return emptyList()
        // Offered while the hub runs a voice extension: asked each time the surface shows while connected.
        val extension = rememberVoiceExtension((context.services as? VoiceServices)?.extensions, context.active)
            ?: return emptyList()
        return listOf(ModuleMenuAction("voice.chat", stringResource(R.string.voice_menu_chat), PersonaIcons.voiceChannel) {
            context.navigator.navigate(VoiceRoute(extension.id, extension.name))
        })
    }

    override fun debugPages(): Map<String, NavKey> =
        mapOf("voice" to VoiceRoute("voice", "语音"))  // i18n-ignore: debug launch arguments, developer-only

    override fun debugArguments(arguments: MutableMap<String, String>) {
        // "Voice chat" on an emulator without audio (-no-audio): silence up, nothing played, a made-up level for the orb.
        voiceDebugAudio(arguments.remove("debug.voiceAudio"))
    }
}

/** The voice module's part of one pairing: its hub's voice extensions, and the pairing's identity the picked voices go with. */
internal class VoiceServices(val identity: String, val extensions: VoiceExtensions) : ModuleServices
