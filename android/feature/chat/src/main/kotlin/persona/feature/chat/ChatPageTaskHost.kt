package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.Interaction
import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.MessageVia
import persona.core.contracts.parseTime
import persona.core.domain.ChatTaps
import persona.core.domain.tasks.AssistantConversation
import persona.core.domain.tasks.TasksStore
import persona.core.designsystem.operationFeedback
import java.util.UUID

/** The task host's construction and watched-agent lifecycle, kept in a value-returning composable so ChatPage retains
 * the same recomposition scope and effect order without carrying this whole setup in its method. */
@Composable
internal fun rememberChatPageTaskHost(
    key: SessionKey,
    chrome: ChatChromeBindings,
    tasks: TasksStore?,
    taskKeys: Set<SessionKey>,
    taskTodos: List<persona.core.contracts.Todo>,
    taskAgentSessions: Map<SessionKey, Session>,
    sessionList: List<Session>,
    machines: List<Machine>,
    allRequests: List<Interaction>,
    taps: ChatTaps,
    assistantPresentation: AssistantConversation,
    session: Session?,
    scope: CoroutineScope,
    res: Resources,
    sender: MessageSender,
    onNotice: (String) -> Unit,
): ChatPageTaskHost {
    val backgroundOpenState = rememberSaveable(key.path) { mutableStateOf(false) }
    // A task's detail or the dock's "All", open over the conversation; kept across the host being made again.
    val taskSheet = remember(key) { mutableStateOf<TaskSheet?>(null) }
    LaunchedEffect(tasks, taskKeys, taskTodos, taskSheet.value) {
        val selected = (taskSheet.value as? TaskSheet.Task)?.todoId
        val detailKeys = taskTodos.firstOrNull { it.id == selected }?.activeAgents.orEmpty().map { it.key }
        tasks?.watchAgents(taskKeys.map(GroupKeys::rawKey).toSet() + detailKeys)
    }
    DisposableEffect(tasks, key) { onDispose { tasks?.watchAgents(emptySet()) } }
    // the cards still to answer: one just answered here no longer says what it asked
    val taskCards = remember(allRequests, taps.resolved) { taps.shown(allRequests) }
    val currentOnNotice by rememberUpdatedState(onNotice)
    val currentOnOpenSession by rememberUpdatedState(chrome.onOpenSession)
    val currentOnOpenSetup by rememberUpdatedState(chrome.onOpenSetup)
    val hasOpenSetup = chrome.onOpenSetup != null
    val namespace = GroupKeys.namespaceOf(key.machineId)
    val hostSessions = remember(tasks != null, namespace, sessionList, taskAgentSessions) {
        if (tasks == null) emptyList()
        else {
            val localSessions = sessionList.filter { GroupKeys.namespaceOf(it.machineId) == namespace }
                .map { it.copy(machineId = GroupKeys.raw(it.machineId)) }
                .map { base ->
                    val extraPlan = taskAgentSessions[base.key]?.plan
                    if (extraPlan == null) base
                    else base.copy(plan = listOfNotNull(base.plan, extraPlan)
                        .maxByOrNull { parseTime(it.at) ?: java.time.Instant.MIN })
                }
            (localSessions + taskAgentSessions.values).distinctBy { it.key }
        }
    }
    val hostMachines = remember(tasks != null, namespace, machines) {
        if (tasks == null) emptyList()
        else machines.filter { GroupKeys.namespaceOf(it.id) == namespace }.map { it.copy(id = GroupKeys.raw(it.id)) }
    }
    val machineDisplayNames = remember(hostMachines) { hostMachines.associate { it.id to it.displayName } }
    val hostPending = remember(tasks != null, namespace, taskCards) {
        if (tasks == null) emptyList()
        else taskCards.filter { GroupKeys.namespaceOf(it.machineId) == namespace }.map { it.copy(machineId = GroupKeys.raw(it.machineId)) }
    }
    val manager = session?.manager
    val backgroundRecords = assistantPresentation.background
    val taskHost = remember(
        tasks, key, namespace, hostSessions, hostMachines, machineDisplayNames, hostPending,
        manager, backgroundRecords, hasOpenSetup, scope, res, sender, taskSheet, backgroundOpenState,
    ) {
        tasks?.let { store ->
            AssistantTaskHost(
                store, key.sessionId, hostSessions,
                { id -> machineDisplayNames[id] ?: id },
                onOpen = { k -> currentOnOpenSession?.invoke(GroupKeys.key(k, namespace)) },
                onError = { currentOnNotice(operationFeedback(it).text.resolve(res)) },
                say = { text -> scope.launch { val _ = runCatching { sendTodayCardMessage(sender, key, text, UUID.randomUUID().toString()) } } },
                sheet = taskSheet, machines = hostMachines,
                pending = hostPending,
                manager = manager,
                backgroundRecords = backgroundRecords, onOpenBackground = { backgroundOpenState.value = true },
                onOpenSetup = if (hasOpenSetup) { page: persona.core.contracts.BriefSetupPage -> currentOnOpenSetup?.invoke(page, manager) } else null,
            )
        }
    }
    return ChatPageTaskHost(taskHost, backgroundOpenState)
}

internal class ChatPageTaskHost(
    val host: AssistantTaskHost?,
    val backgroundOpen: MutableState<Boolean>,
)

/** A Today-card button writes from data the app read; it is not the user's typed words. */
internal suspend fun sendTodayCardMessage(sender: MessageSender, key: SessionKey, text: String, clientMessageId: String) =
    sender.send(key, text, clientMessageId, null, null, null, MessageVia.Card)
