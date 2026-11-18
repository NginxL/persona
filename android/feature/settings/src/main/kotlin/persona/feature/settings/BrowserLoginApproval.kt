package persona.feature.settings

import androidx.compose.runtime.*
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.api.*
import persona.core.hub.HubFailure
import persona.core.designsystem.JDialog
import persona.core.designsystem.LocalAppFeedback

/** Dismissal does not depend on a denial reaching the hub. A stale decision is already terminal. */
internal suspend fun decideBrowserLogin(api: DeviceSettings, request: BrowserLoginRequest, allow: Boolean, dismiss: () -> Unit) {
    if (!allow) dismiss()
    try { api.browserLogin(request.id, request.hub, allow) }
    catch (failure: HubFailure) {
        if (failure.status != 409 && failure.status != 410) throw failure
    }
    if (allow) dismiss()
}

/** Only an intentional scan in Devices can present this confirmation. */
@Composable
internal fun BrowserLoginApproval(api: DeviceSettings, request: BrowserLoginRequest, scope: CoroutineScope, onDismiss: () -> Unit) {
    var details by remember(request) { mutableStateOf<BrowserLoginDetails?>(null) }
    var failed by remember(request) { mutableStateOf(false) }
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
            try { decideBrowserLogin(api, request, allow, onDismiss) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { failed = true; feedback?.failure(error) }
            finally { busy = false }
        }
    }
    val current = details
    JDialog(
        onDismiss = { decide(false) },
        title = stringResource(R.string.settings_browser_login_title),
        text = when {
            failed -> stringResource(R.string.settings_browser_login_failed)
            current == null -> stringResource(R.string.settings_browser_login_loading)
            else -> stringResource(R.string.settings_browser_login_details, current.name) + "\n" +
                stringResource(R.string.settings_browser_login_source, current.source, current.ageSeconds)
        },
        confirm = stringResource(R.string.settings_browser_login_allow) to { decide(true) },
        dismiss = stringResource(R.string.settings_browser_login_cancel) to { decide(false) },
        confirmEnabled = current != null && !busy && !failed,
    )
}
