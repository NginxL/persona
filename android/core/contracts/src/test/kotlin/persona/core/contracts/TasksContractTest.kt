package persona.core.contracts

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** The unified entry's wire shapes (docs/protocol/protocol.md "Tasks and the chat feed") decode, and its events enter the stream. */
class TasksContractTest {
    @Test fun eventsDecode() {
        assertTrue(ServerEvent.decode("""{"type":"todos"}""") is TodosEvent)
        val feed = ServerEvent.decode("""{"type":"assistant_feed","conversation":"c1","item":{"id":"f1","conversation":"c1",
            "at":"2026-11-03T10:00:00Z","kind":"receipt","ref":"t1","body":{"title":"修登录崩溃","state":"done","pr":"https://github.com/o/r/pull/9"}}}""")
        val item = (feed as AssistantFeedEvent).item
        assertEquals("receipt", (item.kind).zhAny()); assertEquals("修登录崩溃", (item.field("title")).zhAny()); assertEquals("t1", (item.ref).zhAny())
        val plan = ServerEvent.decode("""{"type":"task_plan","plan":{"id":"p1","conversation":"c1","state":"open",
            "tasks":[{"title":"t","prompt":"p","machine":"box","cwd":"/code/app","readonly":true}]}}""")
        assertEquals("app", ((plan as TaskPlanEvent).plan.tasks.single().project).zhAny())
    }

    // The cards' snapshot (docs/assistant/assistant-views.md): every live card, answer and registered block, each read on its own.
    @Test fun theCardsSnapshotDecodes() {
        val res = JsonCodec.decodeFromString<FeedResponse>("""{"items":[{"id":"f","conversation":"c","kind":"view","ref":"view_1"}],
            "views":{"view_1":{"id":"view_1","version":2,"view":{"fallback":"f","sections":[]}},"view_2":"not a card"},
            "view_actions":{"i/0":{"key":"i/0","action":"a","client_msg_id":"m","status":"sent","revision":3,"at":"t"}},
            "view_blocks":["i/0","i/1"]}""")
        assertEquals("view", res.items.single().kind)
        assertEquals(setOf("view_1", "view_2"), res.views.keys)
        assertEquals(setOf("i/0"), res.viewActions.keys)
        assertEquals(listOf("i/0", "i/1"), res.viewBlocks)
        // An older hub has none of them.
        assertTrue(JsonCodec.decodeFromString<FeedResponse>("""{"items":[]}""").views.isEmpty())
        assertTrue(ServerEvent.decode("""{"type":"assistant_view_block","conversation":"","key":"k"}""") == null)
        assertTrue(ServerEvent.decode("""{"type":"assistant_view","conversation":"c","view":{"id":"v"}}""") is AssistantViewEvent)
    }

    // A supervised task's verdict (#5190): kept as "review", not folded into "unknown", so the conversation draws it.
    @Test fun aReviewKeepsItsKind() {
        val item = JsonCodec.decodeFromString<FeedItem>("""{"id":"f1","kind":"review","ref":"t1","body":{"verdict":"accepted"}}""")
        assertEquals("review", item.kind)
        assertEquals("accepted", item.field("verdict"))
    }

    /** A scheduled briefing must reach the feed renderer instead of decoding as an unknown item (#4053). */
    @Test fun aBriefKeepsItsKind() {
        val item = JsonCodec.decodeFromString<FeedItem>("""{"id":"brief1","kind":"brief","ref":"routine1",
            "body":{"title":"Morning briefing","hours":12,"waiting":[],"done":[],"accept":[],"progress":[]}}""")
        assertEquals("brief", item.kind)
        assertEquals("Morning briefing", item.field("title"))
        assertEquals(12, item.number("hours"))
    }

    @Test fun briefRowsDecodeIndependently() {
        val item = JsonCodec.decodeFromString<FeedItem>("""{"id":"brief2","kind":"brief","body":{
            "title":"Evening briefing","hours":12.0,"waiting_unknown":true,
            "waiting":[{"id":"w1","title":"Answer?","waited_s":1.5,"machine_id":"box","session_id":"agent"},{"title":"missing id"},"bad"],
            "done":[{"id":"d1","title":"Done"}],"done_total":0,
            "accept":[{"id":"a1","outcome":"decide","recommend":"Canary"},{"id":"a2","outcome":"decide"},{"id":"a3","outcome":"done"}],
            "later":[{"id":"l1","title":"Later"}],"later_total":3,
            "progress":[{"id":"p1","stage":"future-stage","stage_kind":"code"}],"progress_total":2}}""")
        val brief = item.brief ?: error("brief body missing")
        assertEquals("Evening briefing", brief.title)
        assertEquals(12, brief.hours)
        assertTrue(brief.waitingUnknown)
        assertEquals(listOf("w1"), brief.waiting.map { it.id })
        assertEquals(2, brief.waiting.single().waitedS)
        assertEquals(SessionKey("box", "agent"), brief.waiting.single().chat(waiting = true, pending = emptyList()))
        assertEquals(1, brief.doneTotal)
        assertEquals(listOf(BriefTake.Recommendation, null, BriefTake.GotIt), brief.accept.map { it.take })
        assertEquals(2, brief.laterMore)
        assertEquals("future-stage", brief.progress.single().stage)
        assertEquals("code", brief.progress.single().stageKind)
        assertEquals(1, brief.progressMore)
    }

    /** Reports retain their original body and evidence metadata; presentation does not alter the wire record. */
    @Test fun aReportKeepsItsBodyAndEvidence() {
        val raw = """{"id":"report1","conversation":"main","kind":"report","ref":"task1","body":{
            "kind":"progress","text":"Verified the change.\nSee https://example.com/results and /tmp/example-check.log.",
            "evidence":["https://example.com/metadata-only","/tmp/example-evidence.png"]}}"""
        val item = JsonCodec.decodeFromString<FeedItem>(raw)
        assertEquals("report", item.kind)
        assertEquals("Verified the change.\nSee https://example.com/results and /tmp/example-check.log.", item.field("text"))
        assertEquals(JsonCodec.parseToJsonElement(raw).let { (it as kotlinx.serialization.json.JsonObject)["body"] }, item.body)
        val event = ServerEvent.decode("""{"type":"assistant_feed","conversation":"main","item":$raw}""") as AssistantFeedEvent
        assertEquals(item, event.item)
        assertEquals(item, JsonCodec.decodeFromString<FeedItem>(JsonCodec.encodeToString(FeedItem.serializer(), item)))
    }

    @Test fun todoKindDecodesAndAnOlderHubDefaultsToOrdinary() {
        assertEquals("concern", JsonCodec.decodeFromString<Todo>("""{"id":"c","kind":"concern"}""").kind)
        assertEquals("", JsonCodec.decodeFromString<Todo>("""{"id":"t"}""").kind)
        assertEquals("future-kind", JsonCodec.decodeFromString<Todo>("""{"id":"u","kind":"future-kind"}""").kind)
    }

    @Test fun unknownKindsAndTodosDecode() {
        val res = JsonCodec.decodeFromString<FeedResponse>("""{"items":[{"id":"f","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"new_kind"}],
            "plans":{"p":{"id":"p","state":"started","tasks":[]}}}""")
        assertEquals("unknown", (res.items.single().kind).zhAny())
        assertEquals("started", (res.plans.getValue("p").state).zhAny())
        val todos = JsonCodec.decodeFromString<TodosResponse>("""{"todos":[{"id":"t","title":"x","origin":"c","plan_id":"p",
            "agents":[{"machine_id":"box","session_id":"s1"},{"machine_id":"box","session_id":"s0","released_at":"2026-11-03T09:00:00Z"}]}]}""")
        assertEquals(listOf("s1"), (todos.todos.single().activeAgents.map { it.sessionId }).zhAny())
        // the completion check (#3180): kept as sent; a todo from an older hub has none
        val checked = JsonCodec.decodeFromString<Todo>("""{"id":"t","check":{"verdict":"waiting","reason":"Waiting for CI","at":"2026-11-10T08:00:00Z",
            "work_since":"2026-11-10T07:00:00Z","later":1}}""")
        assertEquals(TodoCheck("waiting", "Waiting for CI", "2026-11-10T08:00:00Z", "2026-11-10T07:00:00Z"), checked.check)
        assertEquals(null, JsonCodec.decodeFromString<Todo>("""{"id":"t"}""").check)
        val failed = JsonCodec.decodeFromString<Todo>("""{"id":"t","done_at":"2026-11-04T07:52:25Z","start_error":"claude did not become ready within 1m0s"}""")
        assertEquals("claude did not become ready within 1m0s", (failed.startError).zhAny())
        val ix = JsonCodec.decodeFromString<Interaction>("""{"id":"ix","machine_id":"box","session_id":"s1","kind":"permission","task_title":"修登录崩溃"}""")
        assertEquals("修登录崩溃", (ix.taskTitle).zhAny())
    }

    /** An answered record has every question with its answer in full (#1231); one from before it shows its line. */
    @Test fun answeredItemsInFullOrTheLine() {
        val res = JsonCodec.decodeFromString<FeedResponse>("""{"items":[
            {"id":"f1","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"answered","ref":"t",
             "body":{"question":"先补哪一块？","answer":"重试；假服务器","items":[{"question":"先补哪一块？","answer":"重试\n和超时"},
               {"question":"请求怎么处理？","answer":"假服务器"},{"question":"","answer":""},"x"]}},
            {"id":"f2","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"answered","body":{"question":"使用 Bash","answer":"批准了"}},
            {"id":"f3","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"answered","body":{"items":[]}}]}""")
        assertEquals(listOf(AnsweredItem("先补哪一块？", "重试\n和超时"), AnsweredItem("请求怎么处理？", "假服务器")), res.items[0].answeredItems())
        assertEquals("重试；假服务器", (res.items[0].field("answer")).zhAny())
        assertEquals(listOf(AnsweredItem("使用 Bash", "批准了")), res.items[1].answeredItems())
        assertTrue(res.items[2].answeredItems().isEmpty())
    }
}
