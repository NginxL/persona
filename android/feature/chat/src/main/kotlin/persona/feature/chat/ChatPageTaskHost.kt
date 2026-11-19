package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.Interaction
import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageSender
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
    val taskHost = tasks?.let { store ->
        val namespace = GroupKeys.namespaceOf(key.machineId)
        val localSessions = sessionList.filter { GroupKeys.namespaceOf(it.machineId) == namespace }
            .map { it.copy(machineId = GroupKeys.raw(it.machineId)) }
            .map { base -> base.copy(plan = listOfNotNull(base.plan, taskAgentSessions[base.key]?.plan)
                .maxByOrNull { parseTime(it.at) ?: java.time.Instant.MIN }) }
        AssistantTaskHost(store, key.sessionId, (localSessions + taskAgentSessions.values).distinctBy { it.key },
            { id -> machines.firstOrNull { it.id == GroupKeys.machine(id, namespace) }?.displayName ?: id },
            onOpen = { k -> chrome.onOpenSession?.invoke(GroupKeys.key(k, namespace)) }, onError = { onNotice(operationFeedback(it).text.resolve(res)) },
            say = { text -> scope.launch { runCatching { sender.send(key, text, UUID.randomUUID().toString()) } } },
            sheet = taskSheet, machines = machines.filter { GroupKeys.namespaceOf(it.id) == namespace }.map { it.copy(id = GroupKeys.raw(it.id)) },
            pending = taskCards.filter { GroupKeys.namespaceOf(it.machineId) == namespace }.map { it.copy(machineId = GroupKeys.raw(it.machineId)) },
            manager = session?.manager,
            backgroundRecords = assistantPresentation.background, onOpenBackground = { backgroundOpenState.value = true },
            onOpenSetup = chrome.onOpenSetup?.let { open -> { page: persona.core.contracts.BriefSetupPage -> open(page, session?.manager) } })
    }
    return ChatPageTaskHost(taskHost, backgroundOpenState)
}

internal class ChatPageTaskHost(
    val host: AssistantTaskHost?,
    val backgroundOpen: MutableState<Boolean>,
)
