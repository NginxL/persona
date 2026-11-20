package persona.feature.agents

import androidx.lifecycle.viewModelScope
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import java.io.IOException

/** Async starts, the first message sent once, "Follow model configuration" and prefilled forms (docs/protocol/protocol.md "New session"). */
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class NewAgentAsyncTest {
    private val machines = MutableStateFlow(listOf(Machine("first", online = true), Machine("second", online = true)))

    private fun test(block: suspend TestScope.() -> Unit) = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try { block() } finally { Dispatchers.resetMain() }
    }

    @Test fun anAsyncStartOpensAtOnceAndItsFirstMessageIsQueuedOnceAcrossARestart() = test {
        val store = MemoryStore()
        val sender = Sender()
        val adopted = mutableListOf<Session>()
        val service = Service().apply { result = AgentStartResult.Created(Session(machineId = "first", sessionId = "new", status = "idle", starting = true)) }
        val model = NewAgentViewModel(service, store, machines, sender, adopt = { adopted += it })
        try {
            advanceUntilIdle()
            model.prompt("  看下首页  ")
            sender.fail = true
            model.submit()
            advanceUntilIdle()
            val request = requireNotNull(service.request)
            assertEquals(true, request.async)
            assertEquals("看下首页", request.nameHint)
            assertNull(request.prompt)
            val pending = requireNotNull(store.operation)
            assertEquals("看下首页", pending.firstMessage)
            val firstId = requireNotNull(pending.firstMessageId)
            assertNotEquals(pending.id, firstId)
            // The message did not get into the queue: the start stays to be checked, nothing opens.
            assertNull(model.state.value.created)
            assertEquals(listOf("new"), adopted.map { it.sessionId })
            model.viewModelScope.cancel()

            sender.fail = false
            val restarted = NewAgentViewModel(service, store, machines, sender, adopt = { adopted += it })
            try {
                advanceUntilIdle()
                assertEquals("看下首页", restarted.state.value.prompt)
                restarted.submit()
                advanceUntilIdle()
                assertEquals(1, service.starts)
                assertEquals(pending.id, service.queried)
                assertEquals("new", restarted.state.value.created?.sessionId)
                restarted.submit()
                advanceUntilIdle()
                // One message, under the id saved with the start (the outbox and the hub both dedupe by it).
                assertEquals(listOf(Triple(SessionKey("first", "new"), "看下首页", firstId)), sender.sent)
                restarted.opened()
                advanceUntilIdle()
                assertNull(store.operation)
            } finally { restarted.viewModelScope.cancel() }
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun newAgentsFollowTheModelConfigurationUnlessALauncherIsPicked() = test {
        val store = MemoryStore().apply { saved["first"] = AgentDefaults("/code/old", "sonnetcc") }
        val service = Service().apply {
            model = ModelPurposeDetail(ModelPurpose("agent.new", effective = ModelBinding("launcher.claude", "model-chat")),
                listOf(ModelProvider("launcher.claude", "claude")))
            available = listOf(AgentLauncher("claude"), AgentLauncher("sonnetcc"), AgentLauncher("codex", agentType = "codex"))
        }
        val model = NewAgentViewModel(service, store, machines, Sender())
        try {
            advanceUntilIdle()
            val state = model.state.value
            // The remembered launcher no longer overrides the hub's configuration.
            assertEquals("", state.selection.launcher)
            assertTrue(state.following)
            assertEquals("claude", state.configuredLauncher?.name)
            assertEquals("/code/old", state.selection.cwd)
            model.submit()
            advanceUntilIdle()
            assertEquals("", service.request?.launcher)
            assertEquals(true, service.request?.async)
            // Picking one overrides it for this start only.
            model.viewModelScope.cancel()
            store.operation = null
            val picked = NewAgentViewModel(service, store, machines, Sender())
            try {
                advanceUntilIdle()
                picked.selection(picked.state.value.selection.copy(launcher = "sonnetcc"))
                assertFalse(picked.state.value.following)
                picked.submit()
                advanceUntilIdle()
                assertEquals("sonnetcc", service.request?.launcher)
            } finally { picked.viewModelScope.cancel() }
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun codexTakesItsFirstMessageWithTheStartAndUnreadableConfigurationsAskForALauncher() = test {
        val service = Service().apply { available = listOf(AgentLauncher("claude"), AgentLauncher("codex", agentType = "codex")) }
        val model = NewAgentViewModel(service, MemoryStore(), machines, Sender())
        try {
            advanceUntilIdle()
            // An older hub: the first launcher, as before.
            assertEquals(NewAgentModelConfig.Unsupported, model.state.value.modelConfig)
            assertEquals("claude", model.state.value.selection.launcher)
            model.selection(model.state.value.selection.copy(launcher = "codex"))
            model.prompt("修一下登录")
            model.submit()
            advanceUntilIdle()
            assertEquals("修一下登录", service.request?.prompt)
            assertNull(service.request?.async)
            assertNull(service.request?.nameHint)
            assertEquals("codex", service.request?.agentType)
        } finally { model.viewModelScope.cancel() }
        val failing = Service().apply { modelFailure = IOException("reset") }
        val unread = NewAgentViewModel(failing, MemoryStore(), machines, Sender())
        try {
            advanceUntilIdle()
            assertTrue(unread.state.value.modelConfig is NewAgentModelConfig.Failed)
            assertEquals("", unread.state.value.selection.launcher)
            assertEquals(R.string.agents_new_error_model_config, unread.state.value.error!!.id)
            unread.submit()
            advanceUntilIdle()
            assertEquals(0, failing.starts)
        } finally { unread.viewModelScope.cancel() }
    }

    @Test fun aPrefilledFormPicksItsMachineAndFolderWhenOnline() = test {
        val prefill = NewAgentPrefill("second", "/code/project", "项目背景……\n\n")
        val model = NewAgentViewModel(Service(), MemoryStore(), machines, Sender(), prefill = prefill)
        try {
            advanceUntilIdle()
            assertEquals("second", model.state.value.machineId)
            assertEquals("/code/project", model.state.value.selection.cwd)
            assertEquals("项目背景……\n\n", model.state.value.prompt)
            assertNull(model.state.value.prefillOffline)
            // Another machine keeps its own folder.
            model.selectMachine("first")
            advanceUntilIdle()
            assertEquals("/code", model.state.value.selection.cwd)
        } finally { model.viewModelScope.cancel() }
        val offline = MutableStateFlow(listOf(Machine("first", online = true), Machine("second", name = "Mac Studio", online = false)))
        val elsewhere = NewAgentViewModel(Service(), MemoryStore(), offline, Sender(), prefill = prefill)
        try {
            advanceUntilIdle()
            assertEquals("first", elsewhere.state.value.machineId)
            assertEquals("Mac Studio", elsewhere.state.value.prefillOffline)
            assertEquals("/code", elsewhere.state.value.selection.cwd)
        } finally { elsewhere.viewModelScope.cancel() }
    }

    /** A forwarded start whose answer was lost comes back with its card and what was typed after it, not a blank field. */
    @Test fun aPendingForwardStartIsRestoredWithItsCard() = test {
        val forward = "<persona-context kind=\"forward\" title=\"转发自 Persona · Persona\" url=\"persona://session/m/s?at=x%230\"" +
            " msg=\"x#0\" role=\"persona\" author=\"Persona\" chat=\"Persona\">\n原文\n</persona-context>"
        val store = MemoryStore().apply {
            operation = AgentStartOperation("op", AgentStartRequest("first", "/code", "claude", async = true),
                forward + "\n\n如有疑问告诉我", "first-id")
        }
        val model = NewAgentViewModel(Service(), store, machines, Sender())
        try {
            advanceUntilIdle()
            assertEquals(forward, model.state.value.context)
            assertEquals("如有疑问告诉我", model.state.value.prompt)
            assertNotNull(usableForward(model.state.value.context))
        } finally { model.viewModelScope.cancel() }
    }

    /** A pending start of something else keeps its first message as saved, blocks and spacing included. */
    @Test fun aPendingStartWithoutAForwardKeepsItsFirstMessage() = test {
        val context = "<persona-context kind=\"context\" title=\"修复登录\" url=\"\">\n原文\n</persona-context>"
        val saved = context + "\n\n  看这个  "
        val store = MemoryStore().apply {
            operation = AgentStartOperation("op", AgentStartRequest("first", "/code", "claude", async = true), saved, "first-id")
        }
        val model = NewAgentViewModel(Service(), store, machines, Sender())
        try {
            advanceUntilIdle()
            assertNull(model.state.value.context)
            assertEquals(saved, model.state.value.prompt)
        } finally { model.viewModelScope.cancel() }
    }

    /** A restored forward keeps the other block beside it and the words after them. */
    @Test fun aPendingForwardStartKeepsAllPickedMessagesAndOtherContexts() = test {
        val forward = "<persona-context kind=\"forward\" title=\"转发自 Persona · Persona\" url=\"persona://session/m/s?at=x%230\"" +
            " msg=\"x#0\" role=\"persona\" author=\"Persona\" chat=\"Persona\">\n原文\n</persona-context>"
        val context = "<persona-context kind=\"context\" title=\"修复登录\" url=\"\">\n页面原文\n</persona-context>"
        val forwards = listOf(forward, forward.replace("x%230", "y%231").replace("x#0", "y#1").replace("原文", "其他原文")).joinToString("\n")
        val saved = forwards + "\n" + context + "\n\n看这个"
        val store = MemoryStore().apply {
            operation = AgentStartOperation("op", AgentStartRequest("first", "/code", "claude", async = true), saved, "first-id")
        }
        val model = NewAgentViewModel(Service(), store, machines, Sender())
        try {
            advanceUntilIdle()
            model.forwardContext(null)
            assertEquals(saved, model.state.value.pending?.firstMessage)
            assertEquals(forwards, model.state.value.context)
            assertEquals(context + "\n\n看这个", model.state.value.prompt)
        } finally { model.viewModelScope.cancel() }
    }

    /** A context the card cannot read is no first message: a required prompt stays required. */
    @Test fun anUnreadableContextDoesNotStandInForARequiredFirstMessage() = test {
        val service = Service().apply { available = listOf(AgentLauncher("claude"), AgentLauncher("codex", agentType = "codex")) }
        val store = MemoryStore()
        val model = NewAgentViewModel(service, store, machines, Sender(), prefill = NewAgentPrefill(context = "不是转发的块"))
        try {
            advanceUntilIdle()
            model.selection(model.state.value.selection.copy(launcher = "codex"))
            assertTrue(model.state.value.promptRequired)
            assertNull(usableForward(model.state.value.context))
            assertFalse(startable(model.state.value, machines.value))
            model.submit()
            advanceUntilIdle()
            assertEquals(0, service.starts)
        } finally { model.viewModelScope.cancel() }
    }

    private class MemoryStore : AgentCreationStore {
        var operation: AgentStartOperation? = null
        val saved = mutableMapOf<String, AgentDefaults>()
        override suspend fun lastMachine(scope: String) = "first"
        override suspend fun defaults(scope: String, machine: String) = saved[machine] ?: AgentDefaults()
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) { saved[machine] = defaults }
        override suspend fun pending(scope: String) = operation
        override suspend fun pending(scope: String, value: AgentStartOperation?) { operation = value }
    }

    private class Sender : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<Triple<SessionKey, String, String>>()
        var fail = false
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            if (fail) throw IllegalStateException("Pairing scope is closed")
            sent += Triple(session, text, clientMessageId)
        }
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        var starts = 0
        var queried: String? = null
        var request: AgentStartRequest? = null
        var result: AgentStartResult = AgentStartResult.Created(Session(machineId = "first", sessionId = "created"))
        var available = listOf(AgentLauncher("claude"))
        var model: ModelPurposeDetail? = null
        var modelFailure: Exception? = null
        override suspend fun launchers(machineId: String) = available
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/code" })
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            starts++
            this.request = request
            return result.let { if (it is AgentStartResult.Created) it.copy(session = it.session.copy(machineId = request.machineId)) else it }
        }
        override suspend fun operation(operationId: String): AgentStartResult { queried = operationId; return result }
        override suspend fun newAgentModel(): ModelPurposeDetail? { modelFailure?.let { throw it }; return model }
    }
}

class AgentStartRecordTest {
    @Test fun theFirstMessageAndItsIdAreSavedWithTheStart() {
        val operation = AgentStartOperation("00000000-0000-4000-8000-000000000001",
            AgentStartRequest("mac", "/code", "", async = true, nameHint = "看下首页"), "看下首页", "00000000-0000-4000-8000-000000000002")
        assertEquals(operation, decodeOperation(encodeOperation(operation)))
        // Records saved before async starts keep their prompt and carry no first message.
        val older = decodeOperation("""{"id":"x","request":{"machine_id":"mac","cwd":"/code","launcher":"claude","prompt":"首句"}}""")
        assertEquals("首句", older.request.prompt)
        assertNull(older.firstMessage)
        assertNull(older.firstMessageId)
        assertTrue(runCatching { decodeOperation("""{"request":{}}""") }.isFailure)
    }
}
