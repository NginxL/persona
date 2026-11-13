package persona.feature.assistant

import persona.core.contracts.Session
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.Direction
import persona.core.contracts.DirectionCounts
import persona.core.contracts.DirectionTree
import persona.core.contracts.api.LeaderCounts
import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class AssistantTest {
    /** The app's own words, in Chinese (Robolectric runs in zh, docs/android/android.md). */
    private val resources get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val words get() = ConversationWords(resources.getString(R.string.assistant_untitled),
        resources.getString(R.string.assistant_handoff), resources.getString(R.string.assistant_handoff_named))

    @Test fun searchFindsEveryChatAndKeepsTheMainOneOnce() {
        val main = Session(machineId = "demo", sessionId = "main", title = "主对话")
        val chat = Session(machineId = "demo", sessionId = "chat", title = "工作安排", lastText = "周报")
        val other = Session(machineId = "demo", sessionId = "other", title = "别的")
        val result = conversationGroups(AssistantSnapshot(main.key, listOf(main, chat, other, chat)), " 周报 ", words)
        assertEquals(listOf(chat), result.others)
        assertTrue(result.directions.isEmpty())
        val mainOnly = conversationGroups(AssistantSnapshot(main.key, listOf(main, main)), "主对话", words)
        assertEquals(main, mainOnly.main)
        assertTrue(mainOnly.others.isEmpty())
        // The main conversation is the drawer's first row, above the search: a search that misses it keeps it.
        assertEquals(main, conversationGroups(AssistantSnapshot(main.key, listOf(main)), "周报", words).main)
        assertNull(conversationGroups(AssistantSnapshot(main.key, listOf(main.copy(archived = true))), "", words).main)
    }

    @Test fun historyFormatting() {
        val session = Session(machineId = "demo", sessionId = "history", title = "Claude Code",
            status = "offline", lastText = "\n继续整理安排")
        assertEquals("继续整理安排", session.conversationTitle(words))
        assertEquals(persona.core.designsystem.Status.Offline, session.conversationStatus())
        assertEquals("10月31日 17:30", conversationTime(Instant.parse("2026-10-31T09:30:00Z"), Locale.forLanguageTag("zh-Hans"), ZoneId.of("Asia/Shanghai")))
        assertEquals("31 Oct, 17:30", conversationTime(Instant.parse("2026-10-31T09:30:00Z"), Locale.forLanguageTag("en-GB"), ZoneId.of("Asia/Shanghai")))
        assertNull(conversationTime(null, Locale.ENGLISH))
    }

    // The history's times as iOS writes them (`Fmt.relative`, #4339 C12): just now, minutes, today's time, yesterday, the
    // weekday within six days, else the date.
    @Test fun historyTimesReadAsIOSWritesThem() {
        val zone = ZoneId.of("Asia/Shanghai")
        val now = Instant.parse("2026-11-12T06:30:00Z") // Thursday 14:30
        val locale = resources.configuration.locales[0]
        fun at(time: String) = historyTime(Instant.parse(time), resources, now, zone)
        fun shown(time: String, skeleton: String) = persona.core.designsystem.LocalizedFormats.format(Instant.parse(time), skeleton, locale, zone)
        assertEquals("刚刚", historyTime(now.minusSeconds(44), resources, now, zone))
        assertEquals("1 分钟前", historyTime(now.minusSeconds(45), resources, now, zone))
        assertEquals("5 分钟前", historyTime(now.minusSeconds(300), resources, now, zone))
        assertEquals("59 分钟前", historyTime(now.minusSeconds(3599), resources, now, zone))
        assertEquals("a time ahead of the clock is just now", "刚刚", historyTime(now.plusSeconds(30), resources, now, zone))
        assertEquals("earlier today: its time", shown("2026-11-12T01:00:00Z", persona.core.designsystem.LocalizedFormats.TIME), at("2026-11-12T01:00:00Z"))
        assertEquals("昨天", at("2026-11-11T14:00:00Z"))
        assertEquals("昨天", at("2026-11-11T00:30:00Z"))
        assertEquals("星期一", at("2026-11-09T06:00:00Z"))
        assertEquals(shown("2026-11-07T06:30:00Z", persona.core.designsystem.LocalizedFormats.WEEKDAY), at("2026-11-07T06:30:00Z"))
        assertEquals("six days is the date", "11月6日", at("2026-11-06T06:30:00Z"))
        assertEquals("11月5日", at("2026-11-05T06:00:00Z"))
        assertNull(historyTime(null, resources, now, zone))
    }

    // The blank page's lead is iOS's (`ShellHome`): the main chat's page, a side chat's page, and a step still to finish.
    @Test fun theBlankPagesLeadIsIOSs() {
        assertEquals(R.string.assistant_home_lead_main, homeLead(setup = false, startsMain = true))
        assertEquals(R.string.assistant_home_lead_side, homeLead(setup = false, startsMain = false))
        assertEquals(R.string.assistant_home_setup_lead, homeLead(setup = true, startsMain = true))
        assertEquals(R.string.assistant_home_setup_lead, homeLead(setup = true, startsMain = false))
        assertEquals("这是你的主对话：我会记住这里聊过的事。我能看你的电脑、手机、邮箱、飞书和整理好的项目，也能替你盯着事、到点提醒。",
            resources.getString(R.string.assistant_home_lead_main))
        assertEquals("开一段新的旁支对话。我能看你的电脑、手机、邮箱、飞书和整理好的项目，也能替你盯着事、到点提醒。",
            resources.getString(R.string.assistant_home_lead_side))
    }

    @Test fun handoffConversationTitle() {
        // From the old app's InboxTest: the hub names a hand-off after its first message, a <persona-context> tag.
        val s = Session(machineId = "m", sessionId = "x", title = "<persona-context kind=\"context\" title=\"飞书 · 发布群\">(feed_id=f2)")
        assertEquals("交给 Persona：飞书 · 发布群", s.conversationTitle(words))
        val escaped = Session(machineId = "m", sessionId = "z", title = "<persona-context kind=\"context\" title=\"飞书 · a&quot;b &lt;c&gt; &amp;amp;\" url=\"\">")
        assertEquals("交给 Persona：飞书 · a\"b <c> &amp;", escaped.conversationTitle(words))
        assertEquals("早报", Session(machineId = "m", sessionId = "y", title = "早报").conversationTitle(words))
        // No title on the tag, or an empty one: still a hand-off, not the raw tag.
        assertEquals("交给 Persona", Session(machineId = "m", sessionId = "a", title = "<persona-context kind=\"context\">").conversationTitle(words))
        assertEquals("交给 Persona", Session(machineId = "m", sessionId = "b", title = "<persona-context title=\"\" url=\"\">").conversationTitle(words))
        // Untitled: the last message's first line, read before it is cut to 30 characters.
        val untitled = Session(machineId = "m", sessionId = "c", title = "Claude Code",
            lastText = "\n<persona-context kind=\"context\" title=\"项目 · 一个名字很长很长很长很长很长很长的项目\" url=\"\">\n正文")
        assertEquals("交给 Persona：项目 · 一个名字很长很长很长很长很长很长的项目", untitled.conversationTitle(words))
        assertEquals("a".repeat(30), Session(machineId = "m", sessionId = "d", lastText = "a".repeat(40)).conversationTitle(words))
        assertNull(handoffTitle("早报 <persona-context", words))
        // Bare canonical tags use the same handoff title.
        assertEquals("交给 Persona", handoffTitle(" <persona-context kind=\"context\">", words))
    }

    // The leader conversations are the drawer's 常驻 group: first, in the hub's order, under their names and open tasks;
    // every other chat waits under "All chats" (out of the group, never listed twice).
    @Test fun leadersAreTheDirectionsGroupWithTheirCounts() {
        val main = Session(machineId = "demo", sessionId = "main", title = "主对话")
        val mine = Session(machineId = "demo", sessionId = "mine", title = "周报", origin = "user")
        val leadA = Session(machineId = "demo", sessionId = "lead-a", title = "1110 | 探索 | 增长", origin = "user")
        val leadB = Session(machineId = "demo", sessionId = "lead-b", title = "1110 | 探索 | 支付", origin = "user")
        val snapshot = AssistantSnapshot(main.key, listOf(main, mine, leadB, leadA),
            leaders = listOf("lead-a", "gone", "lead-b"), directionNames = mapOf("lead-a" to "Growth"),
            directionTree = DirectionTree(listOf(Direction("lead-a", counts = DirectionCounts(working = 1, needsYou = 2, stuck = 4)))))
        val result = conversationGroups(snapshot, "", words)
        assertEquals(listOf(leadA, leadB), result.directions)
        assertEquals("Growth", result.names["lead-a"])
        assertEquals(LeaderCounts(working = 1, needsYou = 2), result.counts["lead-a"])
        assertEquals(listOf(mine), result.others)
        // a search finds a direction by its name
        assertEquals(listOf(leadA), conversationGroups(snapshot, "growth", words).directions)
        // no leaders: no group, as before
        assertTrue(conversationGroups(AssistantSnapshot(main.key, listOf(main, mine)), "", words).directions.isEmpty())
    }

    // #5084: the background supervisor is built in. It waits under "All chats" with the chats Persona opened itself,
    // never among 常驻 or Recent, and is found by the name the app gives it.
    @Test fun theBackgroundSupervisorIsListedUnderAllChatsOnly() {
        val main = Session(machineId = "demo", sessionId = "main", title = "主对话")
        val mine = Session(machineId = "demo", sessionId = "mine", title = "周报", origin = "user", updatedAt = "2026-11-12T08:00:00Z")
        val scheduled = Session(machineId = "demo", sessionId = "scheduled", title = "早报", origin = "task", originRef = "t1",
            updatedAt = "2026-11-12T09:00:00Z")
        val background = Session(machineId = "demo", sessionId = "bg", title = "Background supervisor", origin = "task",
            updatedAt = "2026-11-12T10:00:00Z")
        val named = words.copy(background = resources.getString(R.string.assistant_background_supervisor))
        val snapshot = AssistantSnapshot(main.key, listOf(main, mine, scheduled, background), backgroundSupervisor = "bg")
        val result = conversationGroups(snapshot, "", named)
        assertEquals("bg", result.background)
        assertEquals(listOf(background, scheduled, mine), result.others)
        assertEquals(listOf(scheduled, mine), result.recent)
        assertTrue(result.directions.isEmpty())
        // whatever the leaders or its origin say, it is no direction and no recent chat of the user's
        val odd = snapshot.copy(leaders = listOf("bg"), conversations = listOf(main, mine, background.copy(origin = null)))
        assertTrue(conversationGroups(odd, "", named).directions.isEmpty())
        assertEquals(listOf(mine), conversationGroups(odd, "", named).recent)
        assertEquals(listOf(background.copy(origin = null), mine), conversationGroups(odd, "", named).others)
        // found by the app's name for it, in the app's language, whatever language the hub titled it in
        assertEquals("后台督办", named.background)
        assertEquals(listOf(background), conversationGroups(snapshot, "后台督办", named).others)
        // an older hub names none: the conversation list is as before
        val old = conversationGroups(snapshot.copy(backgroundSupervisor = null), "", named)
        assertNull(old.background)
        assertEquals(listOf(background, scheduled, mine), old.others)
        assertTrue(conversationGroups(snapshot.copy(backgroundSupervisor = null), "后台督办", named).others.isEmpty())
    }

    @Test fun archivedConversationsLeaveTheHistory() {
        val kept = Session(machineId = "demo", sessionId = "kept", title = "留着")
        val archived = Session(machineId = "demo", sessionId = "gone", title = "归档了", archived = true)
        val result = conversationGroups(AssistantSnapshot(null, listOf(kept, archived)), "", words)
        assertEquals(emptyList<Session>(), result.directions)
        assertEquals(listOf(kept), result.others)
    }

    // #4331: a leader's row says what it has open, only the parts that are not zero.
    @Test fun leaderCountsSayWhatIsOpen() {
        assertEquals("在做 1 · 等你 2", leaderCountsLabel(LeaderCounts(working = 1, needsYou = 2), resources))
        assertEquals("在做 3", leaderCountsLabel(LeaderCounts(working = 3), resources))
        assertEquals("等你 1", leaderCountsLabel(LeaderCounts(needsYou = 1), resources))
        assertNull("nothing open: no line", leaderCountsLabel(LeaderCounts(), resources))
        assertNull(leaderCountsLabel(null, resources))
    }

    @Test fun theHistoryRowMenu() {
        fun labels(leader: Boolean, live: Boolean, act: Boolean, end: Boolean) =
            conversationActions(resources, leader, live, if (act) ({}) else null, {}, if (end) ({}) else null).map { it.label }
        assertEquals(listOf("设为常驻", "拷贝会话 ID", "结束…"), labels(leader = false, live = true, act = true, end = true))
        assertEquals(listOf("取消常驻", "拷贝会话 ID"), labels(leader = true, live = false, act = true, end = true))
        assertEquals("the main conversation is no leader", listOf("拷贝会话 ID", "结束…"), labels(leader = false, live = true, act = false, end = true))
        assertEquals(listOf("拷贝会话 ID"), labels(leader = false, live = true, act = false, end = false))
        assertTrue(conversationActions(resources, false, true, null, {}, {}).last().destructive)
        // C12 (#4339): iOS's items in iOS's order (`AssistantSidebar.row`), End… last, apart and red.
        val all = conversationActions(resources, false, true, {}, {}, {}, onArchive = {}, onRename = {}, onDiagnostics = {})
        assertEquals(listOf("设为常驻", "重命名…", "归档", "拷贝会话 ID", "拷贝诊断", "结束…"), all.map { it.label })
        assertEquals(listOf(false, false, false, false, false, true), all.map { it.destructive })
        assertTrue(all.last().separatorBefore)
        // Each action is the one passed for it.
        val ran = mutableListOf<String>()
        conversationActions(resources, true, true, { ran += "pin" }, { ran += "copy" }, { ran += "end" }, onArchive = { ran += "archive" },
            onRename = { ran += "rename" }, onDiagnostics = { ran += "diagnostics" }).forEach { it.onClick() }
        assertEquals(listOf("pin", "rename", "archive", "copy", "diagnostics", "end"), ran)
    }

    /** Copy diagnostics (C12): the conversation's structure, never its words. */
    @Test fun diagnosticsCarryTheStructureAndNoWords() {
        val session = Session(machineId = "hub", sessionId = "s1", title = "周末去哪儿", status = "idle", state = "working",
            lastText = "去海边吧", updatedAt = "2026-11-12T09:00:00Z", origin = "user", pendingInteractions = 2,
            queued = listOf("先订酒店"), turnErrorCode = "auth_failed")
        val text = conversationDiagnostics(session)
        assertTrue(text, "session=hub/s1" in text && "state=working" in text && "pending=2 queued=1" in text && "turn_error_code=auth_failed" in text)
        listOf("周末去哪儿", "去海边吧", "先订酒店").forEach { assertFalse(it, it in text) }
    }

    @Test fun firstConversationBecomesMainAndSendUsesOutbox() = runTest {
        val repository = DemoAssistantRepository(DemoWords("周报素材", "整理这周完成的工作", "下周出差的行程", "文档的要点", "新对话"))
        val main = repository.start()
        val branch = repository.start()
        assertEquals(main, repository.assistant.value.mainSession)
        assertNotEquals(main, branch)
        repository.send(main, "整理日程", "message", null)
        repository.send(main, "整理日程", "message", null)
        assertEquals(1, repository.outbox.value.size)
        assertEquals(main, repository.outbox.value.single().session)
        assertEquals("整理日程", repository.assistant.value.conversations.single { it.key == main }.lastText)
    }

    /** #5196 (web `recentConversations`, iOS `AssistantSidebarGroups.recent`): every eligible chat in the drawer's Recent,
     *  except the scheduled tasks' own chats (#6228: those have their own Scheduled Tasks entry and stay in All chats). */
    @Test fun recentIsEveryNewestFiveWithoutMainLeadersArchivedBackgroundOrTasks() {
        fun chat(id: String, minutes: Long, origin: String? = null, archived: Boolean = false) = Session(machineId = "demo", sessionId = id,
            updatedAt = Instant.parse("2026-11-11T12:00:00Z").minusSeconds(minutes * 60).toString(), origin = origin, archived = archived)
        val main = chat("main", 0)
        val chats = listOf(main, chat("lead", 1), chat("background", 0, "task"), chat("inbox", 2, "inbox"), chat("task", 2, "task"), chat("lark", 2, "lark_agent"),
            chat("spawned", 2, "spawned"), chat("ext", 2, "extension"), chat("future", 2, "something_new"), chat("gone", 2, archived = true),
            chat("b", 3, "user"), chat("a", 3, "channel"), chat("c", 4, ""), chat("d", 5), chat("e", 6), chat("f", 7))
        val snapshot = AssistantSnapshot(main.key, chats + chats.last(), leaders = listOf("lead"), backgroundSupervisor = "background")
        assertEquals("automatic chats included; newest first, equal times by session id, at most 5",
            listOf("ext", "future", "inbox", "lark", "spawned"), recentConversations(snapshot).map { it.sessionId })
        assertFalse("a scheduled task's chat never takes a Recent row", recentConversations(snapshot).map { it.sessionId }.contains("task"))
        // The task chat would be the newest one: it still leaves Recent, and the next eligible chat takes its place.
        val taskFirst = AssistantSnapshot(main.key, listOf(main, chat("task", 1, "task"), chat("mine", 2, "user"), chat("inbox", 3, "inbox")))
        assertEquals(listOf("mine", "inbox"), recentConversations(taskFirst).map { it.sessionId })
        assertEquals("ending the leader lets it back in", "lead", recentConversations(snapshot.copy(leaders = emptyList())).first().sessionId)
        val groups = conversationGroups(snapshot, "", words)
        assertEquals(listOf("ext", "future", "inbox", "lark", "spawned"), groups.recent.map { it.sessionId })
        assertTrue("All chats still lists the task chats and everyone else", groups.others.map { it.sessionId }.containsAll(listOf("a", "b", "inbox", "task", "background")))
        assertTrue("no Recent while searching", conversationGroups(snapshot, "x", words).recent.isEmpty())
        assertTrue("only the main chat: no Recent", recentConversations(AssistantSnapshot(main.key, listOf(main))).isEmpty())
    }

    @Test fun automaticConversationOriginsHaveOneAccessibleMapping() {
        listOf(null, "", "user", "channel", " user ").forEach { origin ->
            assertNull("the user's own origin has no source mark: $origin", automaticConversationOrigin(origin))
        }
        val origins = listOf("inbox", "task", "lark_agent", "spawned", "extension", "something_new")
        assertEquals(AssistantConversationOrigin.entries.toList(), origins.map { automaticConversationOrigin(it) })
        assertEquals(listOf("收件箱", "定时任务", "来自 飞书 /agent", "对话派生", "来自扩展", "其他"),
            AssistantConversationOrigin.entries.map { it.accessibilityLabel(resources) })
    }
}
