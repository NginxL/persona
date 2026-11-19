package persona.feature.chat

import android.content.Context
import androidx.compose.runtime.saveable.SaverScope
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSnapshot
import persona.core.domain.parseContexts
import java.io.File
import java.time.Instant
import java.time.ZoneId
import java.time.ZoneOffset
import java.util.Locale

/**
 * Forwarding a message (#3901): the block as the shared cases write and read it (spec/fixtures/forward), what it carries,
 * the card's and the route's words, Forward to's lists and Recent, and where a pick puts the forward.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ForwardTest {
    private val res = ApplicationProvider.getApplicationContext<Context>().resources

    private fun JsonObject.text(name: String) = getValue(name).jsonPrimitive.content
    private fun JsonObject.strings(name: String) = getValue(name).jsonObject.mapValues { it.value.jsonPrimitive.content }

    /** Every shared case, byte for byte: the block Android writes, and what it reads back from a sent message. */
    @Test fun writesAndReadsTheSharedCases() {
        val file = File(requireNotNull(System.getProperty("persona.repo.root")), "spec/fixtures/forward/cases.json")
        val fixtures = Json.parseToJsonElement(file.readText()).jsonObject
        assertEquals(1, fixtures.getValue("version").jsonPrimitive.content.toInt())
        val cases = fixtures.getValue("cases").jsonArray.map { it.jsonObject }
        assertTrue(cases.size >= 3)
        var written = 0
        var read = 0
        for (c in cases) {
            val name = c.text("name")
            c["block"]?.jsonObject?.let { b ->
                val ref = ChatLinkContext(ForwardMessage.KIND, b.text("title"), b.text("url"), b.text("text"), msg = b.text("msg"),
                    extra = b.strings("attributes"))
                assertEquals(name, c.text("xml"), ref.xml())
                // Read back, it writes the same block again.
                val back = ChatLinkContext.parse(ref.xml()).first.single()
                assertEquals(name, b.strings("attributes"), back.extra)
                assertEquals(name, ref.xml(), back.xml())
                written++
            }
            c["message"]?.let { message ->
                val e = c.getValue("expected").jsonObject
                val (refs, rest) = ChatLinkContext.parse(message.jsonPrimitive.content)
                val r = refs.single()
                assertEquals(name, listOf(e.text("kind"), e.text("title"), e.text("url"), e.text("msg")), listOf(r.kind, r.title, r.url, r.msg))
                assertEquals(name, e.strings("attributes"), r.extra)
                assertEquals(name, e.text("text"), r.text)
                assertEquals(name, e.text("rest"), rest)
                val o = requireNotNull(ForwardMessage.origin(r))
                val origin = e.getValue("origin").jsonObject
                assertEquals(name, listOf(origin.text("machine"), origin.text("session"), origin.text("item")), listOf(o.machine, o.session, o.item))
                assertTrue(name, r.isForward)
                // The bubble's reader keeps the same attributes (unknown ones too) for its card.
                val context = parseContexts(message.jsonPrimitive.content).first.single()
                assertEquals(name, e.strings("attributes") + ("msg" to e.text("msg")), context.attributes)
                read++
            }
        }
        assertTrue(written >= 2 && read >= 1)
    }

    private val key = SessionKey("macbook", "a1f3c2d4-0000-4000-8000-000000000006")
    private fun source(role: ForwardMessage.Role = ForwardMessage.Role.Agent, time: Instant? = null) =
        ForwardMessage.Source(key, "e5f0#12", role, "Claude", "Network tests", "weather › tests", "MacBook Pro", time)

    @Test fun aForwardCarriesItsSourceAndALinkBack() {
        val t = Instant.parse("2026-11-11T02:24:00Z")
        val ref = ForwardMessage.ref("The answer", source(time = t), res, ZoneOffset.ofHours(8))
        assertEquals(listOf("forward", "The answer", "e5f0#12"), listOf(ref.kind, ref.text, ref.msg))
        assertEquals(mapOf("role" to "agent", "author" to "Claude", "chat" to "Network tests", "project" to "weather › tests",
            "machine" to "MacBook Pro", "time" to "2026-11-11T10:24:00+08:00"), ref.extra)
        assertEquals("转发自 Claude · Network tests", ref.title)
        assertEquals("persona://session/macbook/a1f3c2d4-0000-4000-8000-000000000006?at=e5f0%2312", ref.url)
        assertEquals(ForwardMessage.Origin("macbook", key.sessionId, "e5f0#12"), ForwardMessage.origin(ref))
        // Written in the order the protocol gives: kind, title, url, msg, then the metadata.
        assertTrue(ref.xml(), ref.xml().startsWith("<persona-context kind=\"forward\" title=\"转发自 Claude · Network tests\" url=\""))
        assertTrue(ref.xml(), ref.xml().contains("msg=\"e5f0#12\" role=\"agent\" author=\"Claude\" chat=\"Network tests\" project=\"weather › tests\" machine=\"MacBook Pro\" time="))
        assertEquals("2026-11-11T02:24:00Z", ForwardMessage.time(t, ZoneOffset.UTC))
        // Your own message is "Me" in the sender's language.
        val mine = ForwardMessage.ref("x", source(ForwardMessage.Role.User), res)
        assertEquals(listOf("user", "我"), listOf(mine.extra["role"], mine.extra["author"]))
        assertEquals("转发自 我 · Network tests", mine.title)
        // Persona's chats name no project or computer.
        val persona = ForwardMessage.ref("x", source(ForwardMessage.Role.Persona).copy(author = "Persona", project = null, machine = null), res)
        assertNull(persona.extra["project"])
        assertNull(persona.extra["machine"])
        // A linked group's chat links with its hub's own machine id, and opens again under the group's.
        val linked = ForwardMessage.url(SessionKey("studio~g0123abcd", "s-9"), "a#3")
        assertEquals("persona://session/studio/s-9?at=a%233", linked)
        assertEquals(SessionKey("studio~g0123abcd", "s-9"), ForwardMessage.origin(linked)?.key(SessionKey("mbp~g0123abcd", "x")))
        assertNull(ForwardMessage.origin("https://example.com/session/a/b"))
        assertNull(ForwardMessage.origin("persona://session/only-one"))
    }

    @Test fun theBodyIsWhatTheChatShows() {
        // The hub's provenance metadata and the photo note go; a quote it opened with stays, as `> ` lines.
        val quote = ChatLinkContext.quote("Earlier words", "Claude", "q1")
        val raw = "<persona-context kind=\"message-source\" title=\"\">\n{\"author\":\"user\"}\n</persona-context>\n" +
            quote.xml() + "\n\nMy words\n\n<attached_images>\n/Users/me/.persona/inbox/a.jpg\n</attached_images>"
        val body = ForwardMessage.body(raw, null, user = true, res)
        assertEquals("> 引用 Claude\n> Earlier words\n\nMy words", body)
        assertFalse(body.contains("message-source") || body.contains("attached_images"))
        // A forward it opened with is led by its own heading; a file sent with it is named on its computer.
        val nested = ForwardMessage.ref("Inner", source(), res).xml() + "\n\nLook" +
            "\n<persona-file kind=\"video\" path=\"/w/clip.mov\" name=\"clip.mov\" size=\"10\"/>"
        assertEquals("> 转发自 Claude · Network tests\n> Inner\n\nLook\n\n（在 MacBook Pro 上的文件：clip.mov）",
            ForwardMessage.body(nested, "MacBook Pro", user = true, res))
        // A very long one is cut, and says so.
        val long = "x".repeat(ForwardMessage.LIMIT + 500)
        val cut = ForwardMessage.body(long, null, user = false, res)
        assertTrue(cut.startsWith("x".repeat(ForwardMessage.LIMIT)))
        assertTrue(cut.length < long.length && !cut.endsWith("x"))
    }

    @Test fun theCardAndTheRouteNameTheComputers() {
        val zone = ZoneId.of("Asia/Shanghai")
        val now = Instant.parse("2026-11-11T02:24:00Z")
        val ref = ForwardMessage.ref("x", source(time = now.minusSeconds(60)), res, zone)
        val meta = ForwardMessage.meta(ref.extra, res, Locale.SIMPLIFIED_CHINESE, now, zone)
        assertTrue(meta, meta!!.startsWith("MacBook Pro · "))
        // Today's time alone; another day's carries its date.
        val old = ForwardMessage.ref("x", source(time = now.minusSeconds(3 * 86_400)), res, zone)
        assertTrue(ForwardMessage.meta(old.extra, res, Locale.SIMPLIFIED_CHINESE, now, zone)!!.length > meta.length)
        assertNull(ForwardMessage.meta(emptyMap(), res, Locale.SIMPLIFIED_CHINESE, now, zone))
        val across = ForwardMessage.route("MacBook Pro", ForwardMessage.RouteTarget("Codex", "Mac Studio"), res)
        assertEquals("从 MacBook Pro 转发 · 发到 Mac Studio 上的 Codex", across)
        val same = ForwardMessage.route("Mac Studio", ForwardMessage.RouteTarget("Codex", "Mac Studio"), res)
        assertEquals("发到 Mac Studio 上的 Codex", same)
        assertEquals("从 MacBook Pro 转发 · 发给 Persona", ForwardMessage.route("MacBook Pro", null, res))
        assertEquals("发给 Persona", ForwardMessage.route(null, null, res))
        assertEquals("没有内容", ForwardMessage.summary(" \n ", res))
        assertEquals("a\nb", ForwardMessage.summary("a\n\n  b  \n", res))
    }

    @Test fun aMessageIsForwardedOnceToTheSameBoxAndSaysSoWithoutANote() {
        val ref = ForwardMessage.ref("x", source(), res)
        val once = withQuote(emptyList(), ref)
        assertEquals(1, once.size)
        assertEquals(once, withQuote(once, ForwardMessage.ref("x", source(), res)))
        // A quote of the same message is another thing over the box.
        assertEquals(2, withQuote(once, ChatLinkContext.quote("x", "Claude", "e5f0#12")).size)
        assertEquals(ref.xml() + "\n\n请看上面转发的消息。", linkMessage(" ", once, res))
        assertEquals(ref.xml() + "\n\n顺便看下重试", linkMessage("顺便看下重试", once, res))
        // A forward over the box survives a configuration change with its metadata and route.
        val routed = ref.copy(route = "发给 Persona")
        val saved = with(ChatLinkContextsSaver) { object : SaverScope { override fun canBeSaved(value: Any) = true }.save(listOf(routed)) }
        assertEquals(listOf(routed), ChatLinkContextsSaver.restore(requireNotNull(saved)))
        assertEquals(listOf(ChatLinkContext("quote", "Claude", "", "x", "i", "m")),
            ChatLinkContextsSaver.restore(listOf("contexts-v2", "quote", "Claude", "", "x", "i", "m")))
    }

    // ---------------------------------------------------------------- targets

    private fun session(machine: String, id: String, state: String = "idle", updated: String, agent: String? = null,
        space: String? = null, assistant: Boolean = false) =
        Session(machineId = machine, sessionId = id, title = id, cwd = "/w/$id", status = state, state = state, updatedAt = updated,
            agent = agent, spaceName = space, assistant = assistant)

    private val agents = listOf(
        session("macbook", "here", updated = "2026-11-11T00:00:50Z"),
        session("macbook", "tests", updated = "2026-11-11T00:01:40Z", agent = "codex", space = "weather"),
        session("studio", "photos", state = "working", updated = "2026-11-11T00:03:20Z"),
        session("studio", "ended", state = "offline", updated = "2026-11-11T00:05:00Z"),
        session("nas", "away", updated = "2026-11-11T00:11:40Z"),
        // Another hub group's computer: never a target.
        session("studio~g0123abcd", "elsewhere", updated = "2026-11-11T00:12:00Z"),
    )
    private val conversations = listOf(
        session("server", "notes", updated = "2026-11-11T00:06:40Z"),
        session("server", "main", updated = "2026-11-11T00:08:20Z"),
    )
    private val machines = listOf(Machine("macbook", "MacBook Pro", online = true), Machine("studio", "Mac Studio", online = true),
        Machine("nas", "NAS", online = false), Machine("server", "Server", online = true))
    private val here = SessionKey("macbook", "here")
    private val main = SessionKey("server", "main")

    private fun sections(recents: List<String> = emptyList(), query: String = "") = ForwardTargets.sections(agents, conversations, here,
        recents.mapNotNull { id -> (agents + conversations).firstOrNull { it.sessionId == id }?.key }, main, machines, query,
        online = { id -> machines.firstOrNull { it.id == id }?.online == true }, title = { it.title })

    @Test fun theListIsRecentThenEachComputersLiveAgents() {
        val s = sections()
        assertEquals(listOf("MacBook Pro", "Mac Studio"), s.map { it.title })
        // Not this chat, not an ended Agent, not a computer that is offline, no Persona conversation (Persona has its row).
        assertEquals(listOf("tests", "photos"), s.flatMap { it.sessions }.map { it.sessionId })
        val r = sections(recents = listOf("photos", "main", "here", "notes", "gone"))
        assertEquals(ForwardTargets.Kind.Recent, r.first().kind)
        assertEquals(listOf("photos", "notes"), r.first().sessions.map { it.sessionId })
        assertEquals(listOf("tests"), r.drop(1).flatMap { it.sessions }.map { it.sessionId })
        // At most five recent ones.
        val many = (1..7).map { session("macbook", "r$it", updated = "2026-11-11T00:00:0${it}Z") }
        val shown = ForwardTargets.sections(many, emptyList(), here, many.map { it.key }, null, machines, "", { true }, { it.title })
        assertEquals(ForwardTargets.RECENT_LIMIT, shown.first().sessions.size)
    }

    @Test fun searchFindsEndedAgentsAndPersonasConversations() {
        assertEquals(listOf("ended", "photos"), sections(query = "mac studio").flatMap { it.sessions }.map { it.sessionId })
        assertEquals(listOf("notes"), sections(query = "notes").flatMap { it.sessions }.map { it.sessionId })
        assertEquals(listOf("tests"), sections(query = "codex weather").flatMap { it.sessions }.map { it.sessionId })
        listOf("main", "away", "elsewhere").forEach { assertTrue(it, sections(query = it).isEmpty()) }
        // An archived chat, or one that takes no messages, is never offered.
        val archived = conversations.first().copy(archived = true)
        assertFalse(ForwardTargets.takes(archived, here, ended = true, online = { true }))
        val history = agents[1].copy(sourceKind = "codex_history", capabilities = persona.core.contracts.SessionCapabilities(read = true))
        assertFalse(ForwardTargets.takes(history, here, ended = true, online = { true }))
    }

    @Test fun recentTargetsAreIdsKeptLatestFirst() {
        var list = emptyList<String>()
        listOf(ForwardTarget.Persona, ForwardTarget.Chat(SessionKey("m", "b")), ForwardTarget.Chat(SessionKey("m", "a")))
            .forEach { list = ForwardMemory.next(list, requireNotNull(it.stored)) }
        assertEquals(listOf("session:m/a", "session:m/b", "persona"), list)
        assertEquals(listOf(SessionKey("m", "a"), SessionKey("m", "b")), ForwardMemory.sessions(list))
        assertNull(ForwardTarget.NewAgent.stored)
        assertEquals(ForwardTarget.Persona, ForwardTarget.parse("persona"))
        assertNull(ForwardTarget.parse("session:nothing"))
        (1..12).forEach { list = ForwardMemory.next(list, "session:m/x$it") }
        assertEquals(ForwardMemory.LIMIT, list.size)
        assertEquals("session:m/x12", list.first())
    }

    // ---------------------------------------------------------------- picking

    private class Opened {
        val chats = mutableListOf<Pair<SessionKey, Boolean>>()
        var home = 0
        val agents = mutableListOf<String>()
    }

    private fun forwarding(opened: Opened, main: SessionKey?) = ChatForwarding(
        agents = MutableStateFlow(agents), machines = MutableStateFlow(machines),
        assistant = MutableStateFlow(AssistantSnapshot(main, conversations)), personaGroupOf = { true },
        conversationTitle = { it.title }, memory = { null },
        openChat = { key, assistant -> opened.chats += key to assistant }, openPersonaHome = { opened.home++ },
        openNewAgent = { opened.agents += it })

    @Test fun aPickPutsTheForwardOverTheTargetsComposerWithItsRoute() {
        val ref = ForwardMessage.ref("The answer", source(), res)
        val opened = Opened()
        val f = forwarding(opened, main)
        val photos = SessionKey("studio", "photos")
        deliverForward(ForwardTarget.Chat(photos), ref, key, f, res)
        assertEquals(listOf(photos to false), opened.chats)
        val placed = ForwardPlacements.take(photos).single()
        assertEquals("从 MacBook Pro 转发 · 发到 Mac Studio 上的 Claude", placed.route)
        // The route is shown, never sent.
        assertEquals(ref.xml(), placed.xml())
        deliverForward(ForwardTarget.Persona, ref, key, f, res)
        assertEquals(main to true, opened.chats.last())
        assertEquals("从 MacBook Pro 转发 · 发给 Persona", ForwardPlacements.take(main).single().route)
        // No main conversation yet: Persona's new conversation takes it.
        deliverForward(ForwardTarget.Persona, ref, key, forwarding(opened, null), res)
        assertEquals(1, opened.home)
        assertEquals(listOf(ref.id), ForwardPlacements.home.map { it.id })
        ForwardPlacements.home = emptyList()
        deliverForward(ForwardTarget.NewAgent, ref, key, f, res)
        assertEquals(listOf(ref.xml()), opened.agents)
        assertEquals(ref.copy(route = null).xml(), ForwardMessage.read(opened.agents.single())?.xml())
        assertNull(ForwardPlacements.waiting(SessionKey("macbook", "tests")))
    }

    @Test fun thePressedMessageSaysWhoWroteItAndWhere() {
        val agent = session("studio", "photos", updated = "2026-11-11T00:03:20Z", agent = "codex", space = "weather").copy(slotName = "2", slotIndex = 2)
        val studio = Machine("studio", "Mac Studio", online = true)
        val f = forwarding(Opened(), main)
        val reply = forwardDraftOf(MessageTarget("r#1", "答", user = false, raw = "**答**", time = "2026-11-11T00:03:20Z", cut = "r#1"),
            agent.key, agent, studio, ChatRenderStyle.Agent, f)
        assertEquals(ForwardMessage.Source(agent.key, "r#1", ForwardMessage.Role.Agent, "Codex", "photos", "weather › #2", "Mac Studio",
            Instant.parse("2026-11-11T00:03:20Z")), reply.source)
        assertEquals(listOf("**答**", "r#1"), listOf(reply.raw, reply.cut))
        val mine = forwardDraftOf(MessageTarget("u1", "问", user = true), agent.key, agent, studio, ChatRenderStyle.Agent, f)
        assertEquals(ForwardMessage.Role.User, mine.source.role)
        val relayed = forwardDraftOf(MessageTarget("u2", "通知", user = true, from = "@巡检"), agent.key, agent, studio, ChatRenderStyle.Agent, f)
        assertEquals(listOf(ForwardMessage.Role.Other, "@巡检"), listOf(relayed.source.role, relayed.source.author))
        val notes = conversations.first().copy(assistant = true)
        val persona = forwardDraftOf(MessageTarget("p1", "好", user = false), notes.key, notes, Machine("server"), ChatRenderStyle.Assistant, f)
        assertEquals(listOf(ForwardMessage.Role.Persona, "Persona", null, null),
            listOf(persona.source.role, persona.source.author, persona.source.project, persona.source.machine))
        assertNotEquals(persona.source.chat, "")
    }
}
