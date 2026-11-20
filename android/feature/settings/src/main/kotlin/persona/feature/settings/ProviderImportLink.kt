package persona.feature.settings

import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute

/** The provider import link opened from outside the app (#5428), held until its providers page takes it. */
object ProviderImportRequests {
    /** Kept across a cold start and pairing, never logged or shown. */
    val pending = mutableStateOf<String?>(null)

    /** Distinguishes repeated opens of the same link after the previous wizard is still active. */
    val generation = mutableLongStateOf(0L)

    /** Takes the link exactly once and gives each external open a new identity. */
    fun take(): String? {
        val link = pending.value ?: return null
        pending.value = null
        generation.longValue += 1
        return link
    }

    /** A provider service that is not ready yet must not consume the only pending link. */
    fun takeIfReady(ready: Boolean): String? = if (ready) take() else null

    /** Pairing keeps the external providers destination so the paired shell can consume it. */
    fun retainProviderNavigationDuringPairing(route: NavKey): Boolean =
        route == SettingsPageRoute(SettingsPage.Providers) && pending.value != null

    /** Explicit cancellation and destination ownership end the link's lifetime. */
    fun cancel() { pending.value = null }
}
