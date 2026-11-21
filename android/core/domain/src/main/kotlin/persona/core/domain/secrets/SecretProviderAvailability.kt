package persona.core.domain.secrets

import kotlinx.coroutines.CancellationException
import persona.core.contracts.api.ExtensionManagement

/**
 * Whether an enabled, compatible extension has an enabled `secret_providers` contribution. The list's `contributes`
 * comes from the full manifest, so its detail is needed to see a contribution the owner switched off. Unknown state
 * fails closed; a stopped provider is still enabled and can be checked when a reference is saved.
 */
suspend fun hasEnabledSecretProvider(api: ExtensionManagement): Boolean = try {
    val candidates = api.extensions().filter {
        it.enabled == true && it.status != "incompatible" && it.contributes?.get("secret_providers")?.isNotEmpty() == true
    }
    candidates.any { candidate ->
        api.extension(candidate.id).contributions.orEmpty().any { it.point == "secret_providers" && it.enabled }
    }
} catch (cancelled: CancellationException) {
    throw cancelled
} catch (_: Exception) {
    false
}
