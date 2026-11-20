package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import java.time.Instant
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.api.BrowserLoginDetails
import persona.core.contracts.api.DeviceSettings
import persona.core.designsystem.InlineTone
import persona.core.designsystem.JDialog
import persona.core.designsystem.JInlineStatus
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.hub.HubFailure

/** Dismissal does not depend on a denial reaching the hub. A stale decision is already terminal. */
internal suspend fun decideBrowserLogin(
    api: DeviceSettings,
    request: BrowserLoginRequest,
    allow: Boolean,
    dismiss: () -> Unit,
): BrowserLoginDetails? {
    if (!allow) dismiss()
    return try { api.browserLogin(request.id, request.hub, allow) }
    catch (failure: HubFailure) {
        if (failure.status != 409 && failure.status != 410) throw failure
        if (allow) dismiss()
        null
    }
}

private enum class BrowserLoginPhase { Loading, Asking, Number, SignedIn, Voided, Denied, Expired, Decided, Failed }

private fun browserLoginPhase(details: BrowserLoginDetails?, failed: Boolean, unavailable: Boolean, expired: Boolean): BrowserLoginPhase = when {
    expired -> BrowserLoginPhase.Expired
    failed -> BrowserLoginPhase.Failed
    unavailable -> BrowserLoginPhase.Decided
    details == null -> BrowserLoginPhase.Loading
    else -> when (details.state) {
        "pending", "scanned", null -> BrowserLoginPhase.Asking
        "confirm" -> if (details.number == null) BrowserLoginPhase.Decided else BrowserLoginPhase.Number
        "approved" -> BrowserLoginPhase.SignedIn
        "voided" -> BrowserLoginPhase.Voided
        "denied" -> BrowserLoginPhase.Denied
        else -> BrowserLoginPhase.Decided
    }
}

/** Only an intentional scan in Devices can present this confirmation. */
@Composable
internal fun BrowserLoginApproval(api: DeviceSettings, request: BrowserLoginRequest, scope: CoroutineScope, onDismiss: () -> Unit) {
    var details by remember(request) { mutableStateOf<BrowserLoginDetails?>(null) }
    var failed by remember(request) { mutableStateOf(false) }
    var unavailable by remember(request) { mutableStateOf(false) }
    var expired by remember(request) { mutableStateOf(false) }
    var busy by remember(request) { mutableStateOf(false) }
    val feedback = LocalAppFeedback.current
    LaunchedEffect(api, request) {
        try { details = api.browserLogin(request.id, request.hub) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { failed = true; feedback?.failure(error) }
    }
    fun decide(allow: Boolean) {
        if (busy && allow) return
        busy = true
        scope.launch {
            try {
                val answer = decideBrowserLogin(api, request, allow, onDismiss)
                if (allow && answer != null) {
                    if (answer.state == null) onDismiss() else details = answer
                }
            }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { failed = true; feedback?.failure(error) }
            finally { busy = false }
        }
    }
    val current = details
    val phase = browserLoginPhase(current, failed, unavailable, expired)
    LaunchedEffect(api, request, phase) {
        if (phase != BrowserLoginPhase.Number) return@LaunchedEffect
        poll@ while (true) {
            delay(1_500)
            try { details = api.browserLogin(request.id, request.hub) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (failure: HubFailure) {
                when (failure.status) {
                    410 -> { expired = true; break@poll }
                    409 -> { unavailable = true; break@poll }
                    else -> Unit // A lost reply is asked for again.
                }
            }
            catch (_: Exception) { /* A lost reply is asked for again. */ }
        }
    }

    val title = when (phase) {
        BrowserLoginPhase.Number -> stringResource(R.string.settings_browser_login_number_title)
        BrowserLoginPhase.SignedIn -> stringResource(R.string.settings_browser_login_signed_in)
        BrowserLoginPhase.Voided -> stringResource(R.string.settings_browser_login_voided)
        BrowserLoginPhase.Denied -> stringResource(R.string.settings_browser_login_denied)
        BrowserLoginPhase.Expired -> stringResource(R.string.settings_browser_login_expired)
        BrowserLoginPhase.Decided -> stringResource(R.string.settings_browser_login_decided)
        else -> stringResource(R.string.settings_browser_login_title)
    }
    val text = when (phase) {
        BrowserLoginPhase.Loading -> stringResource(R.string.settings_browser_login_loading)
        BrowserLoginPhase.Failed -> stringResource(R.string.settings_browser_login_failed)
        BrowserLoginPhase.Decided -> stringResource(R.string.settings_browser_login_decided)
        BrowserLoginPhase.Asking -> if (current?.network == "internet") null else stringResource(R.string.settings_browser_login_only_yours)
        BrowserLoginPhase.SignedIn -> stringResource(R.string.settings_browser_login_signed_in_detail, current?.name.orEmpty())
        BrowserLoginPhase.Voided -> stringResource(R.string.settings_browser_login_voided_detail)
        BrowserLoginPhase.Expired -> stringResource(R.string.settings_browser_login_expired_detail)
        else -> null
    }
    val askingContent: @Composable ColumnScope.() -> Unit = { BrowserLoginRequestFacts(checkNotNull(current)) }
    val numberContent: @Composable ColumnScope.() -> Unit = { BrowserLoginNumber(checkNotNull(current?.number)) }
    val content: (@Composable ColumnScope.() -> Unit)? = when (phase) {
        BrowserLoginPhase.Asking -> askingContent
        BrowserLoginPhase.Number -> numberContent
        else -> null
    }
    val terminal = phase in setOf(BrowserLoginPhase.SignedIn, BrowserLoginPhase.Voided, BrowserLoginPhase.Denied,
        BrowserLoginPhase.Expired, BrowserLoginPhase.Decided)
    JDialog(
        onDismiss = { if (terminal) onDismiss() else decide(false) },
        title = title,
        text = text,
        confirm = if (phase == BrowserLoginPhase.Asking || phase == BrowserLoginPhase.Loading || phase == BrowserLoginPhase.Failed)
            stringResource(R.string.settings_browser_login_allow) to { decide(true) } else null,
        dismiss = if (terminal) stringResource(R.string.settings_done) to onDismiss
        else stringResource(R.string.settings_browser_login_cancel) to { decide(false) },
        confirmEnabled = current != null && !busy && !failed,
        content = content,
    )
}

@Composable
private fun BrowserLoginRequestFacts(details: BrowserLoginDetails) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        if (details.network == "internet") {
            JInlineStatus(stringResource(R.string.settings_browser_login_internet_warning), tone = InlineTone.Caution, maxLines = Int.MAX_VALUE)
        }
        BrowserLoginFact(stringResource(R.string.settings_browser_login_browser), details.name,
            stringResource(R.string.settings_browser_login_browser_note))
        val network = when (details.network) {
            "lan" -> R.string.settings_browser_login_network_lan
            "internet" -> R.string.settings_browser_login_network_internet
            else -> R.string.settings_browser_login_network_unknown
        }
        BrowserLoginFact(stringResource(R.string.settings_browser_login_from), stringResource(network),
            if (details.network == "lan" || details.network == "internet") null else stringResource(R.string.settings_browser_login_network_unknown_note),
            caution = details.network == "internet")
        val requestedAt = Instant.now().minusSeconds(details.ageSeconds.toLong())
        BrowserLoginFact(stringResource(R.string.settings_browser_login_requested),
            agoText(LocalResources.current, requestedAt, showSeconds = true))
        BrowserLoginFact(stringResource(R.string.settings_browser_login_gets), stringResource(R.string.settings_browser_login_full_access),
            stringResource(R.string.settings_browser_login_revoke))
    }
}

@Composable
private fun BrowserLoginFact(label: String, value: String, note: String? = null, caution: Boolean = false) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(label, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
        Text(value, style = PersonaTheme.type.callout, color = if (caution) PersonaTheme.palette.cautionText else PersonaTheme.palette.ink)
        note?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
    }
}

@Composable
private fun BrowserLoginNumber(number: String) {
    Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        Text(number, modifier = Modifier.fillMaxWidth().semantics { contentDescription = number.toList().joinToString(" ") },
            style = PersonaTheme.type.number, color = PersonaTheme.palette.ink, textAlign = TextAlign.Center)
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Text(stringResource(R.string.settings_browser_login_number_secret), modifier = Modifier.fillMaxWidth(),
                style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, textAlign = TextAlign.Center)
            Text(stringResource(R.string.settings_browser_login_number_one_try), modifier = Modifier.fillMaxWidth(),
                style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, textAlign = TextAlign.Center)
        }
        JInlineStatus(stringResource(R.string.settings_browser_login_number_waiting), tone = InlineTone.Busy)
    }
}
