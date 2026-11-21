package persona.feature.machines

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.delay
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.UsageAccount
import persona.core.designsystem.*
import persona.core.domain.usage.isWarn
import persona.core.domain.usage.liveAccounts
import java.time.Instant

/** The merged sidebar and Machines use the same subscription snapshot while both are on screen. */
val LocalSidebarUsage = compositionLocalOf<State<List<UsageAccount>>?> { null }

@Composable
fun rememberSidebarUsage(management: MachineManagement?, active: Boolean): State<List<UsageAccount>> {
    val accounts = remember(management) { mutableStateOf(emptyList<UsageAccount>()) }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(management, active, lifecycle) {
        if (active && management != null) lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            pollSubscriptionUsage(management) { accounts.value = it }
        }
    }
    return accounts
}

@Composable
fun SidebarUsageStrip(accounts: List<UsageAccount>, onOpenMachines: () -> Unit) {
    if (accounts.isEmpty()) return
    val now by produceState(Instant.now()) {
        while (true) {
            value = Instant.now()
            delay(USAGE_POLL_MILLIS)
        }
    }
    val top = remember(accounts, now) { orderedAccounts(liveAccounts(accounts, now)).take(2) }
    if (top.isEmpty()) return
    Row(
        Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onOpenMachines)
            .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.l),
    ) {
        for (account in top) {
            val used = fullest(account)
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    Text(account.name, Modifier.weight(1f), style = PersonaTheme.type.caption,
                        color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(usagePercent(used), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1)
                }
                JLinearProgress((used / 100).toFloat(), tone = Tone.Caution.takeIf { isWarn(used) })
            }
        }
    }
}
