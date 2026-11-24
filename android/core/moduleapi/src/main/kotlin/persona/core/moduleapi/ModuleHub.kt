package persona.core.moduleapi

import kotlinx.coroutines.flow.StateFlow
import okhttp3.OkHttpClient

/**
 * The paired hub as a module's [PersonaModule.services] sees it: its own calls follow the live route with the pairing's
 * pinned client and token, and stop once the pairing changed ([isCurrent]).
 */
interface ModuleHub {
    /** The pairing's identity (its scope path): what a module keeps per pairing goes under it. */
    val identity: String
    /** The hub's hello features (`Hello.features`). */
    val features: StateFlow<Set<String>>
    /** The hub's bearer token. */
    val token: String
    /** The pairing's pinned HTTP client. */
    val http: OkHttpClient

    /** The hub's address on its current route; null while there is none (reconnecting). */
    fun address(): String?

    /** False once the pairing changed: calls from then on must fail with a cancellation. */
    fun isCurrent(): Boolean
}
