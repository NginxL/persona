package persona.feature.assistant

import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CancellationException
import persona.core.contracts.api.AssistantReadiness
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.ReadinessStep
import persona.core.designsystem.ReadinessActionId
import persona.core.designsystem.hubAnswer
import persona.core.domain.SetupFailureCodes
import persona.core.navigation.ModelProviderRoute
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.AddComputerRoute
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute

/**
 * The main conversation's readiness on this screen (docs/assistant/onboarding.md 4.4): asked when the tab opens, when the machines
 * change and after a refused start; [probe] is "Try a message". An old hub's 404 leaves it null for good, and the page as before.
 */
@Stable
internal class MainReadiness(private val load: suspend (probe: Boolean) -> AssistantReadiness) {
    var readiness by mutableStateOf<AssistantReadiness?>(null)
        private set
    var probing by mutableStateOf(false)
        private set
    private var unsupported = false

    suspend fun refresh(probe: Boolean = false) {
        if (unsupported) return
        if (probe) probing = true
        try {
            readiness = load(probe)
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            // 404: a hub without it. 503 (the assistant restarting) and the rest: keep what was there, ask next time.
            if (hubAnswer(error)?.status == 404) { unsupported = true; readiness = null }
        } finally {
            if (probe) probing = false
        }
    }

    /** What blocks sending from the home composer, if anything. */
    val blocking: ReadinessStep? get() = readiness?.blockingStep()
    val chatOnly: Boolean get() = readiness?.mode == AssistantReadiness.CHAT_ONLY
}

/** Where a readiness button goes on the phone; null for one that opens help in place or runs here ("Try a message"). */
internal fun ReadinessActionId.destination(launcher: String?, machine: String? = null, manager: String? = null): NavKey? = when (this) {
    ReadinessActionId.AddComputer -> AddComputerRoute()
    ReadinessActionId.ConfirmComputer -> AddComputerRoute(confirm = machine.orEmpty())
    ReadinessActionId.ConnectModel -> ModelPurposeRoute("assistant", manager)
    ReadinessActionId.PickModel -> SettingsPageRoute(SettingsPage.ModelConfig)
    ReadinessActionId.EditProvider -> launcher?.takeIf(String::isNotBlank)?.let(::ModelProviderRoute)
        ?: SettingsPageRoute(SettingsPage.Providers)
    else -> null
}

/** A main message the hub refused to start for a missing step (no computer…): it waits for the user, with its reason. */
internal fun OutboxMessage.setupRefusal(): String? =
    error?.takeIf { state == DeliveryState.Refused && it in SetupFailureCodes }
