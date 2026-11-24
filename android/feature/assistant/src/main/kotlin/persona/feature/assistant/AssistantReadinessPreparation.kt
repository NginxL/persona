package persona.feature.assistant

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.lifecycle.compose.LifecycleEventEffect
import androidx.lifecycle.Lifecycle
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ReadinessStep
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.AssistantReadinessFeature
import persona.core.designsystem.LocalHubFeatures
import persona.core.navigation.Navigator

/** The main conversation's readiness preparation, kept outside AssistantScreen's oversized Compose lambda. */
internal class AssistantReadinessPreparation(
    val enabled: Boolean,
    val controller: MainReadiness,
    val readiness: persona.core.contracts.api.AssistantReadiness?,
    val blocking: ReadinessStep?,
    val guidance: (@Composable () -> Unit)?,
    val onAction: (persona.core.designsystem.ReadinessActionId, String?) -> Unit,
)

@Composable
internal fun rememberAssistantReadinessPreparation(
    pageManagement: AssistantManagement?,
    demo: Boolean,
    snapshot: AssistantSnapshot,
    active: SessionKey?,
    machines: List<Machine>,
    scope: CoroutineScope,
    navigator: Navigator,
    mainSession: Session?,
    mainMessages: List<OutboxMessage>,
    managerOffline: Boolean,
    managerId: String,
    readinessHelp: persona.core.designsystem.ReadinessHelp?,
    onHelp: (persona.core.designsystem.ReadinessHelp?) -> Unit,
    onResume: () -> Unit,
): AssistantReadinessPreparation {
    val onMain = active != null && active == snapshot.mainSession
    val readinessOn = pageManagement != null && !demo && AssistantReadinessFeature in LocalHubFeatures.current
    val mainReadiness = remember(pageManagement) { MainReadiness { probe -> requireNotNull(pageManagement).readiness(probe) } }
    val machinesSeen = machines.joinToString(",") { "${it.id}:${it.online}" }
    // Reopening main also checks for setup changes that arrive without a turn update.
    LaunchedEffect(mainReadiness, readinessOn, machinesSeen, onMain,
        snapshot.mainSession, mainSession?.state, mainSession?.turnErrorCode, mainSession?.modelPick) {
        if (readinessOn) mainReadiness.refresh()
    }
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) {
        if (readinessOn) scope.launch { mainReadiness.refresh() }
        onResume()
    }
    val refusedMain = mainMessages.filter { it.setupRefusal() != null }.map { it.clientMessageId }
    LaunchedEffect(mainReadiness, readinessOn, refusedMain) { if (readinessOn && refusedMain.isNotEmpty()) mainReadiness.refresh() }
    val readiness = mainReadiness.readiness.takeIf { readinessOn }
    val blocking = mainReadiness.blocking.takeIf { readinessOn }
    val onAction = run@{ action: persona.core.designsystem.ReadinessActionId, launcher: String? ->
        runAssistantReadinessAction(action, launcher, readiness, managerId,
            scope, mainReadiness, navigator, onHelp = onHelp)
    }
    // The hub checks the assistant's default model and computer. An existing chat can use a different one.
    val modelProblem = blocking?.takeIf {
        onMain && it.id == ReadinessStep.MODEL && !managerOffline && mainSession?.modelPick == null &&
            readiness?.machine?.id?.takeIf(String::isNotBlank)?.let { id -> id == active?.machineId } == true
    }
    val guidance: (@Composable () -> Unit)? = modelProblem?.let { step ->
        {
            val machine = readiness?.machine?.name?.takeIf(String::isNotBlank)
            AssistantModelGuidanceCard(step = step, machine = machine, onAction = { action -> onAction(action, blocking?.launcher ?: readiness?.launcher) })
        }
    }
    return AssistantReadinessPreparation(readinessOn, mainReadiness, readiness, blocking, guidance, onAction)
}
