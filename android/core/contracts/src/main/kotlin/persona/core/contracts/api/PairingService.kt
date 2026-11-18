package persona.core.contracts.api

import kotlinx.coroutines.flow.StateFlow

data class PairingRequest(
    val url: String,
    val token: String,
    val hub: String? = null,
    val fp: String? = null,
    val tls: Int? = null,
    val alt: List<String> = emptyList(),
    /** A Cloudflare Access service token entered before pairing: the pairing's own requests already carry it. */
    val access: HubAccess? = null,
    val deviceId: String? = null,
) {
    override fun toString() = "PairingRequest(credentials=redacted)"
}

enum class PairingStage { Parse, Probe, VerifyIdentity, ExchangeCredentials, LoadMachines, Complete }
data class PairingProgress(
    val stage: PairingStage,
    val timestamp: String? = null,
    val candidates: List<ConnectionCandidate> = emptyList(),
)
enum class PairingFailure { InvalidCode, Dns, Refused, Unreachable, Timeout, LocalPermission, Certificate, Expired, Incompatible, TlsRequired,
    /** Cloudflare Access stopped the request: no service token was set, or the one set was refused. */
    AccessRequired, AccessRejected,
    /**
     * The link's address behind a reverse proxy or tunnel presented a certificate this phone doesn't trust (#2622): the
     * proxy's, not the hub's key; [PairingException.address] names it.
     */
    ProxyCertificate,
    Unknown }
/** [address]: the address the failure is about, when the message names one ([PairingFailure.ProxyCertificate]). */
class PairingException(val reason: PairingFailure, val address: String? = null) : Exception(reason.name)

interface PairingService {
    val isPaired: StateFlow<Boolean>
    /** Waits for pairings restored at process start; implementations without asynchronous restore return immediately. */
    suspend fun restore() = Unit
    /** Opens the hub's HTTPS OPAQUE page; passwords never pass through the native API. */
    suspend fun passwordLogin(address: String, progress: (PairingProgress) -> Unit) {
        throw PairingException(PairingFailure.Incompatible)
    }
    suspend fun pair(request: PairingRequest)
    /** Implementations must report actual stages and verify identity before exchanging credentials. */
    suspend fun pair(request: PairingRequest, progress: (PairingProgress) -> Unit) {
        // Legacy adapters cannot safely process pinned pairing metadata.
        if (request.hub != null || request.fp != null || request.tls != null || request.alt.isNotEmpty()) {
            throw PairingException(PairingFailure.Incompatible)
        }
        progress(PairingProgress(PairingStage.ExchangeCredentials))
        pair(request)
    }
    suspend fun unpair()
}

/** Where a pairing attempt stopped, for "Connection diagnostics" (the running connection's probes only exist once paired). */
enum class PairingAttemptStep { Parse, Network, Tls, Http, Auth, Protocol }

/**
 * The last pairing attempt on this phone: the address tried, where it stopped ([step] null when it succeeded), the
 * error, when, and each address's probe result. Kept in memory only: it holds no credentials, and a restart forgets it.
 */
data class PairingAttempt(
    val address: String?,
    val step: PairingAttemptStep?,
    val error: String?,
    val at: String,
    val candidates: List<ConnectionCandidate> = emptyList(),
) {
    val failed: Boolean get() = step != null
}

/** The last pairing attempt, shared by the pairing page (which records it) and "Connection diagnostics" (which shows it). */
class PairingAttempts {
    private val mutable = kotlinx.coroutines.flow.MutableStateFlow<PairingAttempt?>(null)
    val last: StateFlow<PairingAttempt?> = mutable
    fun record(attempt: PairingAttempt?) { mutable.value = attempt }
}
