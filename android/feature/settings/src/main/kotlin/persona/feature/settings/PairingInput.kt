package persona.feature.settings

import persona.core.contracts.ProtocolCompatibility

import androidx.annotation.StringRes
import persona.core.contracts.api.*
import persona.core.domain.Pairing
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.hub.SpkiTrustManager
import persona.core.hub.LocalAddressPolicy
import java.net.*
import java.security.cert.CertificateException
import javax.net.ssl.SSLPeerUnverifiedException

object PairingInput {
    fun parse(link: String): PairingRequest {
        val uri = runCatching { URI(link.trim()) }.getOrNull()
        if (uri?.scheme !in ProtocolCompatibility.appSchemes || uri?.rawAuthority != "pair") invalid()
        val parsed = Pairing.parse(link) ?: invalid()
        val keys = uri.rawQuery.orEmpty().split('&').map { it.substringBefore('=') }
        if (keys.filter { it != "alt" }.groupingBy { it }.eachCount().any { it.value > 1 }) invalid()
        if ("tls" in keys && parsed.tls == null) invalid()
        return validate(PairingRequest(parsed.url, parsed.token, parsed.hub, parsed.fp, parsed.tls, parsed.alt))
    }

    /** A full pairing link (`persona://`) is parsed whole instead of being treated as a hub address. */
    fun isLink(value: String): Boolean = Pairing.isLink(value)

    /**
     * The manual form's request. An address without a scheme gets the one it would be reached with: `http://` on this
     * device and the local network (the hosts plaintext is allowed for), `https://` anywhere else. [metadata] is a
     * scanned link whose address was edited in the form; it keeps the link's hub id and fingerprint.
     */
    fun manual(address: String, token: String, metadata: PairingRequest? = null): PairingRequest {
        if (isLink(address)) return parse(address)
        val url = withScheme(address.trim())
        return validate(metadata?.copy(url = url, token = token.trim()) ?: PairingRequest(url, token.trim()))
    }

    // A string check, not URI's scheme: "persona:18660" is a host and port.
    private fun withScheme(address: String): String {
        if (address.isEmpty() || "://" in address) return address
        val host = runCatching { URI("http://$address").host }.getOrNull()
        return (if (host != null && localHost(host)) "http://" else "https://") + address
    }

    /**
     * Metadata after the manual address field changes to [value]. A pairing link replaces all metadata
     * (the caller parses it), so the raw link and the previous link's token, hub and fingerprint never survive.
     */
    fun editedAddress(metadata: PairingRequest?, value: String): PairingRequest? =
        if (isLink(value)) null else metadata?.copy(url = value)

    private fun validate(request: PairingRequest): PairingRequest {
        if (request.token.isBlank() || request.tls?.let { it !in 1..65535 } == true) invalid()
        // The same hub id and fingerprint checks as HubPairingClient.addresses, so a malformed link is Invalid pairing code, not Unknown error.
        if (request.hub?.matches(Regex("[A-Za-z0-9_-]{1,128}")) == false) invalid()
        if (request.fp != null && runCatching { SpkiTrustManager(request.fp) }.isFailure) invalid()
        (listOf(request.url) + request.alt).forEach { address ->
            val uri = runCatching { URI(address) }.getOrNull() ?: invalid()
            if (uri.scheme !in listOf("http", "https") || uri.host.isNullOrBlank() ||
                uri.userInfo != null || uri.query != null || uri.fragment != null || uri.port > 65535 || uri.port == 0 || uri.port < -1) invalid()
        }
        val primary = URI(request.url)
        if (primary.scheme == "http" && request.fp == null && !localHost(primary.host)) {
            throw PairingException(PairingFailure.TlsRequired)
        }
        return request
    }
    /** Address literals are judged by the policy `HubHttpClient` connects with, so a link it would refuse fails here. */
    private fun localHost(host: String): Boolean {
        val value = host.removePrefix("[").removeSuffix("]").lowercase()
        if (value == "localhost" || value.endsWith(".local")) return true
        if (':' in value) {
            val address = runCatching { InetAddress.getByName(value) }.getOrNull() ?: return false
            return LocalAddressPolicy().allows(address)
        }
        val bytes = value.split('.').map { it.toIntOrNull() ?: return false }
        if (bytes.size != 4 || bytes.any { it !in 0..255 }) return false
        return LocalAddressPolicy().allows(InetAddress.getByAddress(ByteArray(4) { bytes[it].toByte() }))
    }
    private fun invalid(): Nothing = throw PairingException(PairingFailure.InvalidCode)
}

private fun isCertificateRejection(error: Throwable): Boolean =
    error is SSLPeerUnverifiedException || error is CertificateException

fun pairingFailure(error: Throwable): PairingFailure = when (error) {
    is PairingException -> error.reason
    is UnknownHostException -> PairingFailure.Dns
    is ConnectException -> PairingFailure.Refused
    is NoRouteToHostException -> PairingFailure.Unreachable
    is SocketTimeoutException -> PairingFailure.Timeout
    is SSLPeerUnverifiedException, is CertificateException -> PairingFailure.Certificate
    is HubFailure -> when (error.kind) {
        HubError.CertificateMismatch ->
            if (error.code == persona.core.hub.PROXY_CERTIFICATE) PairingFailure.ProxyCertificate else PairingFailure.Certificate
        HubError.HubMismatch -> PairingFailure.Certificate
        HubError.AddressNotFound -> PairingFailure.Dns
        HubError.ConnectionRefused -> PairingFailure.Refused
        HubError.Unreachable, HubError.NoNetwork -> PairingFailure.Unreachable
        HubError.Timeout -> PairingFailure.Timeout
        HubError.PairingExpired -> PairingFailure.Expired
        HubError.Incompatible -> PairingFailure.Incompatible
        HubError.TlsRequired -> PairingFailure.TlsRequired
        HubError.LocalNetworkPermission -> PairingFailure.LocalPermission
        HubError.AccessRequired -> PairingFailure.AccessRequired
        HubError.AccessRejected -> PairingFailure.AccessRejected
        else -> error.cause?.takeIf { it !== error }?.let(::pairingFailure) ?: PairingFailure.Unknown
    }
    is SecurityException -> PairingFailure.LocalPermission
    else -> error.cause?.takeIf { it !== error }?.let(::pairingFailure) ?: PairingFailure.Unknown
}

/** The address a failure names (a proxied address whose certificate was refused, #2622), from anywhere in [error]'s chain. */
fun pairingFailureAddress(error: Throwable): String? =
    generateSequence(error) { it.cause }.take(16).filterIsInstance<PairingException>().firstOrNull()?.address

/** What went wrong and what to do, naming [address] where the message has room for one ([PairingFailure.ProxyCertificate]). */
@androidx.compose.runtime.Composable
fun pairingExplanation(reason: PairingFailure, address: String?): String =
    if (reason == PairingFailure.ProxyCertificate) androidx.compose.ui.res.stringResource(R.string.settings_pairing_failure_proxy_certificate,
        address ?: androidx.compose.ui.res.stringResource(R.string.settings_pairing_failure_proxy_this_address))
    else androidx.compose.ui.res.stringResource(reason.explanation())

/** What went wrong and what to do, as a string resource. */
@StringRes
fun PairingFailure.explanation(): Int = when (this) {
    PairingFailure.InvalidCode -> R.string.settings_pairing_failure_invalid
    PairingFailure.Dns -> R.string.settings_pairing_failure_dns
    PairingFailure.Refused -> R.string.settings_pairing_failure_refused
    PairingFailure.Unreachable -> R.string.settings_pairing_failure_unreachable
    PairingFailure.Timeout -> R.string.settings_pairing_failure_timeout
    PairingFailure.LocalPermission -> R.string.settings_pairing_failure_local
    PairingFailure.Certificate -> R.string.settings_pairing_failure_certificate
    PairingFailure.Expired -> R.string.settings_pairing_failure_expired
    PairingFailure.Incompatible -> R.string.settings_pairing_failure_incompatible
    PairingFailure.TlsRequired -> R.string.settings_pairing_failure_tls_required
    PairingFailure.AccessRequired -> R.string.settings_pairing_failure_access_required
    PairingFailure.AccessRejected -> R.string.settings_pairing_failure_access_rejected
    PairingFailure.ProxyCertificate -> R.string.settings_pairing_failure_proxy_certificate
    PairingFailure.Unknown -> R.string.settings_pairing_failure_unknown
}

internal enum class PairingRecovery(@StringRes val label: Int) {
    Retry(R.string.settings_pairing_recovery_retry), Scan(R.string.settings_pairing_rescan),
    Permission(R.string.settings_pairing_recovery_permission), Update(R.string.settings_pairing_recovery_update),
    /** Back to the confirm page with the Cloudflare Access fields open. */
    Access(R.string.settings_pairing_recovery_access),
}

internal fun PairingFailure.recovery(): PairingRecovery = when (this) {
    PairingFailure.LocalPermission -> PairingRecovery.Permission
    PairingFailure.InvalidCode, PairingFailure.Certificate, PairingFailure.Expired,
    PairingFailure.TlsRequired -> PairingRecovery.Scan
    PairingFailure.Incompatible -> PairingRecovery.Update
    PairingFailure.AccessRequired, PairingFailure.AccessRejected -> PairingRecovery.Access
    PairingFailure.Dns, PairingFailure.Refused, PairingFailure.Unreachable,
    PairingFailure.Timeout, PairingFailure.Unknown -> PairingRecovery.Retry
    // the proxy's certificate is fixed on the server; the same link works once it is
    PairingFailure.ProxyCertificate -> PairingRecovery.Retry
}

/**
 * What "Connection diagnostics" keeps of a pairing attempt that stopped with [error] (null: it succeeded). [stage] is the last stage the
 * pairing reported, [candidates] each address's probe result. The error text never carries the pairing code.
 */
fun pairingAttempt(
    request: PairingRequest?, error: Throwable?, stage: PairingStage? = null,
    candidates: List<ConnectionCandidate> = emptyList(), now: java.time.Instant = java.time.Instant.now(),
): PairingAttempt = PairingAttempt(
    address = request?.url?.takeIf(String::isNotBlank),
    step = error?.let { pairingStep(it, stage) },
    error = error?.let { describePairingError(it, request?.token) },
    at = now.toString(),
    candidates = candidates,
)

/** Which step a pairing failed at: parsing the link, reaching the address, TLS, an HTTP status, the code, the answer. */
fun pairingStep(error: Throwable, stage: PairingStage? = null): PairingAttemptStep {
    val causes = generateSequence(error) { it.cause }.take(16).toList()
    val hub = causes.filterIsInstance<HubFailure>().firstOrNull()
    val reason = causes.filterIsInstance<PairingException>().firstOrNull()?.reason
    val beforeNetwork = stage == null || stage == PairingStage.Parse
    return when {
        causes.any(::isCertificateRejection) -> PairingAttemptStep.Tls
        hub != null -> when (hub.kind) {
            HubError.CertificateMismatch -> PairingAttemptStep.Tls
            HubError.PairingExpired -> PairingAttemptStep.Auth
            HubError.Protocol, HubError.Incompatible, HubError.HubMismatch -> PairingAttemptStep.Protocol
            HubError.Server, HubError.RateLimited, HubError.AccessRequired, HubError.AccessRejected -> PairingAttemptStep.Http
            HubError.TlsRequired -> if (hub.status != null) PairingAttemptStep.Http else PairingAttemptStep.Network
            else -> PairingAttemptStep.Network
        }
        reason == PairingFailure.Certificate || reason == PairingFailure.ProxyCertificate -> PairingAttemptStep.Tls
        reason == PairingFailure.Expired -> PairingAttemptStep.Auth
        reason == PairingFailure.Incompatible -> PairingAttemptStep.Protocol
        reason == PairingFailure.InvalidCode || reason == PairingFailure.TlsRequired ->
            if (beforeNetwork) PairingAttemptStep.Parse else PairingAttemptStep.Protocol
        causes.any { it is IllegalArgumentException } -> if (beforeNetwork) PairingAttemptStep.Parse else PairingAttemptStep.Protocol
        else -> PairingAttemptStep.Network
    }
}

/** The error chain as text (type, HTTP status, message), with [token] replaced, at most a few hundred characters. */
internal fun describePairingError(error: Throwable, token: String?): String {
    val parts = generateSequence(error) { it.cause }.take(6).mapNotNull { cause ->
        when (cause) {
            is PairingException -> null
            is HubFailure -> listOfNotNull(cause.kind.name, cause.status?.let { "HTTP $it" }).joinToString(" · ") +
                cause.message.takeIf { it != cause.kind.name }?.let { "：$it" }.orEmpty() // i18n-ignore: technical error chain for diagnostics
            else -> cause.javaClass.simpleName + (cause.message?.let { "：$it" } ?: "") // i18n-ignore: technical error chain for diagnostics
        }
    }.distinct().toList()
    val text = parts.ifEmpty { listOf((error as? PairingException)?.reason?.name ?: error.javaClass.simpleName) }.joinToString(" ← ")
    val clean = token?.takeIf { it.length >= 4 }?.let { text.replace(it, "…") } ?: text
    return if (clean.length > 400) clean.take(400) + "…" else clean
}

/** Where a pairing stopped, as a string resource. */
@StringRes
fun PairingAttemptStep.labelRes(): Int = when (this) {
    PairingAttemptStep.Parse -> R.string.settings_pairing_step_parse
    PairingAttemptStep.Network -> R.string.settings_pairing_step_network
    PairingAttemptStep.Tls -> R.string.settings_pairing_step_tls
    PairingAttemptStep.Http -> R.string.settings_pairing_step_http
    PairingAttemptStep.Auth -> R.string.settings_pairing_step_auth
    PairingAttemptStep.Protocol -> R.string.settings_pairing_step_protocol
}
