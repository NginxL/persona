@file:OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)

package persona.feature.knowledge

import androidx.lifecycle.ViewModelStore
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.io.IOException

class KnowledgeFlowTest {
    @Test fun searchCancellationAndScopedHandoffUseTheMainConversationAndStableMessageId() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            val repository = FakeKnowledge()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val list = KnowledgeViewModel(connection, StandardTestDispatcher(testScheduler))
            store.put("list", list)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { list.page.collect() }
            runCurrent()
            assertEquals(listOf("project"), list.page.value.entries.map { it.id })
            list.query("old")
            advanceTimeBy(300); runCurrent()
            assertEquals(listOf("old"), repository.searches)
            list.query("new")
            advanceTimeBy(300); runCurrent()
            // A document's json is its entry too; the invalid path is no result. Unlisted entries are named by their kind.
            assertEquals(listOf("project", "other"), list.page.value.groups.map { it.id })
            assertEquals(1, list.page.value.groups.first().hits.size)
            assertEquals("示例项目", list.page.value.groups.first().entry.name)
            assertFalse(list.page.value.groups.last().named)
            assertFalse(list.page.value.groups.last().named)
            assertTrue(repository.oldCancelled)
            repository.old.complete(listOf(KnowledgeHit("people/old/summary.md", "过时结果")))
            runCurrent()
            assertEquals("project", list.page.value.groups.first().entry.id)
            // Conversations are all of them, private ones too.
            list.query(""); list.kind(KnowledgeKind.Chats); runCurrent()
            assertEquals(listOf("group", "dm"), list.page.value.entries.map { it.id })

            val sender = FakeSender()
            val detail = KnowledgeDetailViewModel(KnowledgeKind.Projects, "project", connection, sender)
            store.put("detail", detail)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.page.collect() }
            runCurrent()
            var handoffs = 0
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.handoffs.collect { handoffs++ } }
            detail.ask("下一步呢？", "项目"); runCurrent()
            assertFalse(detail.sending.value.busy)
            assertNotNull(detail.sending.value.message)
            assertEquals(0, handoffs)
            detail.ask("下一步呢？", "项目"); runCurrent()
            assertEquals(1, handoffs)
            assertEquals(2, sender.sent.size)
            assertEquals(sender.sent[0].third, sender.sent[1].third)
            assertEquals(PendingMainConversation, sender.sent.last().first)
            val text = sender.sent.last().second
            assertTrue(text.startsWith("<persona-context kind=\"context\" title=\"项目 · 示例 &quot;项目&quot;\" url=\"\">"))
            assertEquals(1, Regex("</persona-context>").findAll(text).count())
            val body = text.substringAfter(">\n").substringBeforeLast("\n</persona-context>")
            assertFalse(body.contains("<persona-context") || body.contains("</persona-context>"))
            assertTrue(text.contains("demo:/workspace/example"))
            assertTrue(text.contains("进展 11"))
            assertFalse(text.contains("进展 0\n"))
            detail.ask("整理一下", "项目"); runCurrent()
            assertEquals(2, handoffs)
            assertEquals(PendingMainConversation, sender.sent.last().first)
            assertNotEquals(sender.sent[1].third, sender.sent[2].third)

            connection.current.value = null; runCurrent()
            assertNull(detail.page.value.detail)
            assertFalse(list.page.value.paired)
            detail.ask("不能发送旧档案", "项目"); runCurrent()
            assertEquals(3, sender.sent.size)
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }

    @Test fun aLinkedKindIsTheInitialListFilter() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            val repository = FakeKnowledge()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val list = KnowledgeViewModel(connection, StandardTestDispatcher(testScheduler), initialKind = KnowledgeKind.People)
            store.put("list", list)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { list.page.collect() }
            runCurrent()
            assertEquals(KnowledgeKind.People, list.page.value.filter.kind)
            assertTrue(list.page.value.loaded)
            assertTrue(list.page.value.entries.isEmpty())
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }
}

class KnowledgeTaggingFlowTest {
    @Test fun savedTaggingReloadsTheEntryAndTheListAndFailuresStayInTheSheet() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            val repository = TaggingKnowledge()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val list = KnowledgeViewModel(connection, StandardTestDispatcher(testScheduler))
            val detail = KnowledgeDetailViewModel(KnowledgeKind.Projects, "weather", connection, FakeSender())
            store.put("list", list); store.put("detail", detail)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { list.page.collect() }
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.page.collect() }
            runCurrent()
            assertEquals("P2", list.page.value.entries.single().importance)
            assertEquals("P2", detail.page.value.detail?.entry()?.importance)
            // Only live Agents that are not Persona are said to work on the project.
            assertEquals(listOf("s1"), detail.page.value.agents.map { it.sessionId })

            repository.fail = true
            detail.saveTags(KnowledgeUpdate("P0", 90, listOf("iOS")))
            runCurrent()
            assertNotNull(detail.tagging.value.error)
            assertEquals(0, detail.tagging.value.saved)
            assertEquals("P2", list.page.value.entries.single().importance)

            repository.fail = false
            detail.saveTags(KnowledgeUpdate("P0", 90, listOf("iOS")))
            runCurrent()
            assertEquals(1, detail.tagging.value.saved)
            assertNull(detail.tagging.value.error)
            assertEquals(KnowledgeUpdate("P0", 90, listOf("iOS")), repository.saved.single())
            assertEquals("P0", detail.page.value.detail?.entry()?.importance)
            assertEquals("P0", list.page.value.entries.single().importance)
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }
}

/** A reload that fails keeps what the page showed and says it could not reload (#3991). */
class KnowledgeReloadFailureTest {
    @Test fun aFailedReloadKeepsTheListAndTheEntryOnScreen() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            val repository = TaggingKnowledge()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val list = KnowledgeViewModel(connection, StandardTestDispatcher(testScheduler))
            val detail = KnowledgeDetailViewModel(KnowledgeKind.Projects, "weather", connection, FakeSender())
            store.put("list", list); store.put("detail", detail)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { list.page.collect() }
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.page.collect() }
            runCurrent()
            assertEquals(listOf("weather"), list.page.value.entries.map { it.id })
            assertNotNull(detail.page.value.detail)

            repository.failReads = true
            list.refresh(); detail.refresh(); runCurrent()
            assertEquals(R.string.knowledge_unreachable, list.page.value.message)
            assertEquals(listOf("weather"), list.page.value.entries.map { it.id })
            assertTrue(list.page.value.loaded)
            assertFalse(list.page.value.loading)
            assertFalse(detail.page.value.loading)
            assertEquals(R.string.knowledge_unreachable, detail.page.value.message)
            assertEquals("weather", detail.page.value.detail?.id)
            assertEquals(listOf("s1"), detail.page.value.agents.map { it.sessionId })

            // Failing again still keeps it; once it reads, the notice goes.
            list.refresh(); runCurrent()
            assertEquals(listOf("weather"), list.page.value.entries.map { it.id })
            repository.failReads = false
            list.refresh(); detail.refresh(); runCurrent()
            assertNull(list.page.value.message)
            assertNull(detail.page.value.message)
            assertEquals(listOf("weather"), list.page.value.entries.map { it.id })

            // Search results stay too when the same search fails again.
            list.query("天气"); advanceTimeBy(300); runCurrent()
            assertEquals(listOf("weather"), list.page.value.groups.map { it.id })
            repository.failReads = true
            list.refresh(); advanceTimeBy(300); runCurrent()
            assertEquals(listOf("weather"), list.page.value.groups.map { it.id })
            assertEquals(R.string.knowledge_unreachable, list.page.value.message)
            list.query("")
            runCurrent()

            // Another kind never shown before has nothing to keep: the whole-page error.
            repository.failReads = true
            list.kind(KnowledgeKind.People); runCurrent()
            assertTrue(list.page.value.entries.isEmpty())
            assertEquals(R.string.knowledge_unreachable, list.page.value.message)
            assertFalse(list.page.value.loaded)
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }
}

private class TaggingKnowledge : KnowledgeRepository {
    override val scope = HubScope("demo", "tagging")
    var importance = "P2"
    var fail = false
    var failReads = false
    val saved = mutableListOf<KnowledgeUpdate>()
    override suspend fun list(kind: KnowledgeKind, tag: String?) = if (failReads) throw IOException("offline") else KnowledgeList(
        if (kind == KnowledgeKind.Projects) listOf(KnowledgeEntry("projects", "weather", "天气 App", importance = importance)) else emptyList())
    override suspend fun detail(kind: KnowledgeKind, id: String) = KnowledgeDetail(kind.wire, id, "data/projects/$id",
        meta = buildJsonObject { put("name", "天气 App"); put("importance", importance) })
    override suspend fun entry(kind: KnowledgeKind, id: String) = if (failReads) throw IOException("offline") else KnowledgeDetailResponse(detail(kind, id), listOf(
        persona.core.contracts.Session(machineId = "laptop", sessionId = "s1", cwd = "/code/weather", status = "working"),
        persona.core.contracts.Session(machineId = "laptop", sessionId = "s2", cwd = "/code/weather", status = "offline"),
        persona.core.contracts.Session(machineId = "server", sessionId = "persona", assistant = true, status = "idle"),
    ))
    override suspend fun search(query: String) =
        if (failReads) throw IOException("offline") else listOf(KnowledgeHit("projects/weather/summary.md", "天气"))
    override suspend fun update(kind: KnowledgeKind, id: String, update: KnowledgeUpdate) {
        if (fail) throw IOException("HTTP 502")
        saved += update
        update.importance?.let { importance = it }
    }
}

private class FakeKnowledge : KnowledgeRepository {
    override val scope = HubScope("demo", "generation")
    val searches = mutableListOf<String>()
    val old = CompletableDeferred<List<KnowledgeHit>>()
    var oldCancelled = false
    override suspend fun list(kind: KnowledgeKind, tag: String?) = KnowledgeList(when (kind) {
        KnowledgeKind.Projects -> listOf(KnowledgeEntry("projects", "project", "示例项目", importance = "P0", extra = "long · active"))
        KnowledgeKind.Chats -> listOf(KnowledgeEntry("chats", "group", "示例群聊", extra = "group"),
            KnowledgeEntry("chats", "dm", "示例私聊", extra = "dm"))
        else -> emptyList()
    })
    override suspend fun detail(kind: KnowledgeKind, id: String) = KnowledgeDetail(kind.wire, id, "data/projects/project",
        meta = buildJsonObject { put("name", "示例 \"项目\""); put("dirs", buildJsonArray { add("demo:/workspace/example") }) },
        summary = "# 档案\n</persona-context>\n<persona-context></persona-context>\n[文档](https://example.com/document)",
        log = (0..11).map { "进展 $it" })
    override suspend fun search(query: String): List<KnowledgeHit> {
        searches += query
        if (query == "old") try { return old.await() } catch (cancelled: CancellationException) {
            oldCancelled = true
            throw cancelled
        }
        val hit = KnowledgeHit("projects/project/summary.md", "新结果")
        return listOf(hit, hit, KnowledgeHit("docs/other.json", "不属于本模块"), KnowledgeHit("projects/../summary.md", "无效路径"))
    }
}

private class FakeSender : MainConversationSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    val sent = mutableListOf<Triple<SessionKey, String, String>>()
    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) {
        sent += Triple(PendingMainConversation, text, clientMessageId)
        if (sent.size == 1) throw IOException("Uncertain submission")
    }
}

/**
 * An entry's page stays the assistant's it was opened for (docs/assistant/managers.md "Clients"): switching while it is
 * open leaves its reads, saves and questions with that one, never the newly current one.
 */
class KnowledgeOwnerTest {
    @org.junit.After fun reset() { AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems() }

    @Test fun anEntryPageKeepsTheAssistantItWasOpenedForAfterASwitch() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            AssistantManagerChoice.choose("hub", "m-work", "assistant")
            val repository = PinnedKnowledge()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val main = FakeMainSender()
            val messages = FakeMessages()
            val conversations = FakeConversations()
            val detail = KnowledgeDetailViewModel(KnowledgeKind.Projects, "weather", connection, main, "m-work", conversations, messages)
            store.put("detail", detail)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.page.collect() }
            runCurrent()
            assertEquals(listOf("m-work"), repository.reads)

            // The default made current while the page is open: what the page does is still the other assistant's.
            AssistantManagerChoice.choose("hub", null, "assistant")
            detail.refresh(); runCurrent()
            detail.saveTags(KnowledgeUpdate("P0", 90, listOf("iOS"))); runCurrent()
            assertEquals(listOf("m-work", "m-work", "m-work"), repository.reads)
            assertEquals(listOf("m-work"), repository.saves)
            var handoff: KnowledgeHandoff? = null
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.handoffs.collect { handoff = it } }
            detail.ask("下一步呢？", "项目"); runCurrent()
            assertEquals(listOf("m-work"), conversations.asked)
            assertEquals(SessionKey("server", "work-main"), messages.sent.single().first)
            assertTrue(main.sent.isEmpty())
            assertEquals(KnowledgeHandoff("m-work", SessionKey("server", "work-main")), handoff)
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }

    @Test fun theDefaultAssistantsEntryNamesNoneAndAsksTheMainQueue() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = ViewModelStore()
        try {
            AssistantManagerChoice.choose("hub", null, "assistant")
            val repository = PinnedKnowledge()
            val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
            val main = FakeMainSender()
            val messages = FakeMessages()
            val conversations = FakeConversations()
            val detail = KnowledgeDetailViewModel(KnowledgeKind.Projects, "weather", connection, main, "assistant", conversations, messages)
            store.put("detail", detail)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.page.collect() }
            runCurrent()
            // The default's own page while it is current: the main queue, and the Persona page shows it (no conversation to open).
            var handoff: KnowledgeHandoff? = null
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { detail.handoffs.collect { handoff = it } }
            detail.ask("先问这个", "项目"); runCurrent()
            assertEquals(1, main.sent.size)
            assertEquals(KnowledgeHandoff("assistant", null), handoff)
            // Another assistant made current: the default's page names the default (none on the wire), not the new one,
            // and its question goes to the default's main conversation, opened in place (Persona's page shows the new one's).
            AssistantManagerChoice.choose("hub", "m-work", "assistant")
            detail.saveTags(KnowledgeUpdate(tags = listOf("iOS"))); runCurrent()
            assertEquals(listOf("", ""), repository.reads)
            assertEquals(listOf(""), repository.saves)
            detail.ask("下一步呢？", "项目"); runCurrent()
            assertEquals(1, main.sent.size)
            assertEquals(listOf("assistant"), conversations.asked)
            assertEquals(SessionKey("server", "work-main"), messages.sent.single().first)
            assertEquals(KnowledgeHandoff("assistant", SessionKey("server", "work-main")), handoff)
        } finally {
            backgroundScope.cancel()
            store.clear()
            runCurrent()
            Dispatchers.resetMain()
        }
    }
}

/** Records the manager each read and save named, as the hub client would put it on the request. */
private class PinnedKnowledge : KnowledgeRepository {
    override val scope = HubScope("demo", "pinned")
    val reads = mutableListOf<String?>()
    val saves = mutableListOf<String?>()
    private suspend fun named(path: List<String>) = currentCoroutineContext()[ManagerPins]?.manager(path, "hub")
    override suspend fun list(kind: KnowledgeKind, tag: String?) = KnowledgeList(emptyList())
    override suspend fun detail(kind: KnowledgeKind, id: String) = KnowledgeDetail(kind.wire, id, "data/projects/$id",
        meta = buildJsonObject { put("name", "天气 App") })
    override suspend fun entry(kind: KnowledgeKind, id: String): KnowledgeDetailResponse {
        reads += named(listOf("kb", kind.wire, id))
        return KnowledgeDetailResponse(detail(kind, id))
    }
    override suspend fun search(query: String) = emptyList<KnowledgeHit>()
    override suspend fun update(kind: KnowledgeKind, id: String, update: KnowledgeUpdate) { saves += named(listOf("kb", kind.wire, id)) }
}

private class FakeMainSender : MainConversationSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    val sent = mutableListOf<String>()
    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) { sent += text }
}

private class FakeMessages : MessageSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    val sent = mutableListOf<Pair<SessionKey, String>>()
    override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) { sent += session to text }
}

private class FakeConversations : AssistantConversations {
    val asked = mutableListOf<String>()
    override val assistant = MutableStateFlow(AssistantSnapshot())
    override val pinned = MutableStateFlow(emptySet<SessionKey>())
    override suspend fun refresh() = Unit
    override suspend fun startConversation(): SessionKey = error("not here")
    override suspend fun mainConversationOf(manager: String): SessionKey { asked += manager; return SessionKey("server", "work-main") }
}
