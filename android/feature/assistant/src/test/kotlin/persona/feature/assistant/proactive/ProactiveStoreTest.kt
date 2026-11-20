package persona.feature.assistant.proactive

import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import java.io.IOException
import java.time.Instant
import java.time.ZoneId
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4

/** A hub that behaves like the real one for the schedule: it keeps its own fields by id and hands out ids. */
private class FakeHub(
    var table: List<ScheduledTask>,
    var runs: List<Run> = emptyList(),
) : ProactiveApi {
    val writes = mutableListOf<ScheduledTask>()
    val deleted = mutableListOf<String>()
    val started = mutableListOf<String>()
    var failWrite: Exception? = null
    /** The write lands but the answer never comes back. */
    var loseAnswer = false
    var failRun: Exception? = null
    /** When set, a run waits for it before the hub answers (a slow hub). */
    var runAnswer: CompletableDeferred<Unit>? = null
    var failDelete: Exception? = null
    var deleteLandsAnyway = false
    var reads = 0
    private var ids = 0

    override suspend fun tasks(): List<ScheduledTask> { reads++; return table }

    override suspend fun saveTask(task: ScheduledTask): ScheduledTask {
        writes += task
        failWrite?.let { throw it }
        val hub = table.firstOrNull { it.id == task.id }
        if (task.id.isNotBlank() && hub == null) throw IOException("404 no such task")
        val saved = task.copy(id = task.id.ifBlank { "task_new${ids++}" }, createdBy = hub?.createdBy, lastRun = hub?.lastRun,
            lastSessionId = hub?.lastSessionId, lastStatus = hub?.lastStatus, lastError = hub?.lastError,
            runMachineId = hub?.runMachineId, runSessionId = hub?.runSessionId, machineId = hub?.machineId,
            sessionId = task.sessionId?.takeIf { it.isNotBlank() && hub?.sessionId == it },
            inbox = task.inbox ?: hub?.inbox,
            nextRun = if (task.enabled) "2026-11-03T01:00:00Z" else null)
        table = if (hub == null) table + saved else table.map { if (it.id == saved.id) saved else it }
        if (loseAnswer) throw IOException("connection reset")
        return saved
    }

    override suspend fun deleteTask(id: String) {
        failDelete?.let {
            if (deleteLandsAnyway) table = table.filterNot { t -> t.id == id }
            throw it
        }
        deleted += id
        table = table.filterNot { it.id == id }
    }

    override suspend fun runTask(id: String): SessionKey {
        runAnswer?.await()
        failRun?.let { throw it }
        started += id
        val task = table.first { it.id == id }
        if (task.repeat == TaskRepeat.Once) table = table - task
        return task.origin ?: SessionKey("server", "run-$id")
    }

    override suspend fun freshTask(id: String): ScheduledTask {
        val task = table.first { it.id == id }.copy(runMachineId = null, runSessionId = null)
        table = table.map { if (it.id == id) task else it }
        return task
    }

    override suspend fun runs() = runs
    override suspend fun assistantMachine() = "server"
}

private val byPersona = ScheduledTask(
    id = "task_weekly", title = "周报", prompt = "起草周报", repeats = ScheduledTask.DAILY, time = "17:00", weekdays = listOf(5), enabled = true,
    createdBy = "assistant", lastRun = "2026-10-30T09:00:00Z", lastSessionId = "conversation-1", lastStatus = "failed",
    lastError = "assistant machine is offline", nextRun = "2026-11-06T09:00:00Z",
)
private val morning = ScheduledTask(
    id = "morning", title = "早报", prompt = "给我今天的早报", repeats = ScheduledTask.DAILY, time = "09:00", weekdays = listOf(1, 2, 3, 4, 5),
    enabled = true, lastRun = "2026-11-02T01:00:00Z", lastSessionId = "conversation-2", lastStatus = "ok", nextRun = "2026-11-03T01:00:00Z",
    runMachineId = "server", runSessionId = "own-morning",
)
private val patrol = ScheduledTask(id = "patrol", title = "巡检", prompt = "看看服务", repeats = ScheduledTask.INTERVAL, everyMinutes = 30,
    at = "2026-11-02T02:30:00Z", enabled = true, nextRun = "2026-11-02T02:30:00Z", permissionMode = "auto")
private val review = ScheduledTask(id = "fu_review", repeats = ScheduledTask.ONCE, at = "2026-11-02T03:00:00Z", title = "会前准备", prompt = "整理纪要",
    enabled = true, machineId = "server", sessionId = "conversation-3")
private val ci = ScheduledTask(id = "fu_ci", repeats = ScheduledTask.ONCE, at = "2026-11-02T05:00:00Z", prompt = "看看 CI 跑完没有\n跑完告诉我",
    enabled = true)

/** The page's words in the tests' language (Simplified Chinese, robolectric.properties). */
private fun pageText() = ProactiveText(ApplicationProvider.getApplicationContext())

@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ProactiveStoreTest {
    private val say by lazy { pageText() }

    private fun TestScope.messagesOf(store: ProactiveStore): List<String> {
        val messages = mutableListOf<String>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { store.messages.toList(messages) }
        return messages
    }

    @Test fun pairingAndCompatibilityFailuresUseTheProactivePagesLanguage() {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        for (language in listOf("zh", "en", "ja")) {
            val config = android.content.res.Configuration(context.resources.configuration).apply {
                setLocale(java.util.Locale.forLanguageTag(language))
            }
            val localized = context.createConfigurationContext(config)
            val words = ProactiveText(localized)
            for ((kind, id) in listOf(
                persona.core.hub.HubError.PairingExpired to persona.core.designsystem.R.string.designsystem_feedback_pairing_expired,
                persona.core.hub.HubError.Incompatible to persona.core.designsystem.R.string.designsystem_feedback_incompatible,
            )) {
                val error = persona.core.hub.HubFailure(kind, "raw detail")
                val expected = localized.getString(id)
                assertEquals(language, expected, loadFailure(words, error))
                assertEquals(language, expected.trimEnd('。', '.'), failureReason(words, error))
            }
        }
    }

    @Test fun repeatingAndOneOffTasksAreTheirOwnSections() = runTest {
        val store = ProactiveStore(FakeHub(listOf(ci, byPersona, review, morning, patrol)), this, say)
        store.refresh()
        assertEquals(listOf("task_weekly", "morning", "patrol"), store.state.value.repeating!!.map { it.id })
        assertEquals(listOf("fu_review", "fu_ci"), store.state.value.oneOff!!.map { it.id })
    }

    @Test fun aSwitchWritesOnlyThatTaskAndOnlyItsSwitch() = runTest {
        val hub = FakeHub(listOf(byPersona, morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        store.toggle("morning", false)
        advanceUntilIdle()
        val written = hub.writes.single()
        assertEquals(morning.copy(enabled = false), written)
        assertEquals(listOf("morning", "task_weekly"), listOf(written.id) + hub.table.filter { it.enabled }.map { it.id })
        assertFalse(store.state.value.task("morning")!!.enabled)
        assertEquals("assistant", store.state.value.task("task_weekly")!!.createdBy)
    }

    /** The prompt changed elsewhere after the page loaded (the page does not hear of it); the switch keeps that change. */
    @Test fun aSwitchWritesOverWhatTheHubHasNowNotWhatThePageLoaded() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        hub.table = listOf(morning.copy(prompt = "new"))
        store.toggle("morning", false)
        advanceUntilIdle()
        assertEquals(morning.copy(prompt = "new", enabled = false), hub.writes.single())
    }

    @Test fun switchMovesAtOnceAndAFailedWriteShowsTheHubsValue() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        val messages = messagesOf(store)
        store.refresh()
        hub.failWrite = IOException("no route")
        store.toggle("morning", false)
        assertFalse(store.state.value.shownTasks!!.single().enabled)
        advanceUntilIdle()
        assertTrue(store.state.value.pending.isEmpty())
        assertTrue(store.state.value.shownTasks!!.single().enabled)
        assertEquals(listOf("没保存上：连不上 hub"), messages)
    }

    @Test fun aFailureAfterThePageIsGoneGoesToTheAppsFeedback() = runTest {
        val hub = FakeHub(listOf(morning))
        val unheard = mutableListOf<String>()
        val store = ProactiveStore(hub, this, say, unheard = { unheard += it })
        val page = backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { store.messages.collect {} }
        store.refresh()
        hub.failWrite = IOException("no route")
        // The switch is flipped and the page is left before the write fails.
        store.toggle("morning", false)
        page.cancel()
        advanceUntilIdle()
        assertEquals(listOf("没保存上：连不上 hub"), unheard)
    }

    @Test fun aPageThatIsListeningSaysItItself() = runTest {
        val hub = FakeHub(listOf(morning))
        val unheard = mutableListOf<String>()
        val store = ProactiveStore(hub, this, say, unheard = { unheard += it })
        val messages = messagesOf(store)
        store.refresh()
        hub.failWrite = IOException("no route")
        store.toggle("morning", false)
        advanceUntilIdle()
        assertEquals(listOf("没保存上：连不上 hub"), messages)
        assertTrue(unheard.isEmpty())
    }

    @Test fun aLostAnswerIsNotAFailureWhenTheHubHasTheChange() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        val messages = messagesOf(store)
        store.refresh()
        hub.loseAnswer = true
        store.toggle("morning", false)
        advanceUntilIdle()
        assertFalse(store.state.value.shownTasks!!.single().enabled)
        assertTrue(messages.isEmpty())
    }

    @Test fun editWritesTheUserFieldsAndKeepsTheRest() = runTest {
        val hub = FakeHub(listOf(byPersona, morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val draft = byPersona.draft().copy(title = "  周报（草稿）  ", prompt = " 先给我草稿 ", time = "16:30", weekdays = (1..7).toList(),
            enabled = false, permissionMode = "plan")
        assertTrue(store.save(TaskChange.Edit("task_weekly", byPersona.draft(), draft)).isSuccess)
        assertEquals(byPersona.copy(title = "周报（草稿）", prompt = "先给我草稿", time = "16:30", weekdays = emptyList(), enabled = false,
            permissionMode = "plan"), hub.writes.single())
        // The hub kept its own fields; the page shows what it answered.
        val shown = store.state.value.task("task_weekly")!!
        assertEquals("assistant", shown.createdBy)
        assertEquals("failed", shown.lastStatus)
        assertNull(shown.nextRun)
    }

    @Test fun switchingTheRuleWritesTheNewRulesFields() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val zone = ZoneId.of("Asia/Shanghai")
        val now = Instant.parse("2026-11-02T02:10:00Z")
        val interval = morning.draft().repeating(TaskRepeat.Interval, now, zone)
        assertEquals(DefaultInterval, interval.everyMinutes)
        assertTrue(store.save(TaskChange.Edit("morning", morning.draft(), interval.copy(everyMinutes = 120))).isSuccess)
        val written = hub.writes.last()
        assertEquals(ScheduledTask.INTERVAL, written.repeats)
        assertEquals(120, written.everyMinutes)
        assertNull(written.time)
        assertTrue(written.weekdays.isEmpty())
        // "Once" picks the next full hour an hour away.
        val once = morning.draft().repeating(TaskRepeat.Once, now, zone)
        assertEquals(Instant.parse("2026-11-02T03:00:00Z"), once.at)
        assertTrue(store.save(TaskChange.Edit("morning", store.state.value.task("morning")!!.draft(), once)).isSuccess)
        assertEquals("2026-11-02T03:00:00Z", hub.writes.last().at)
        assertEquals(ScheduledTask.ONCE, hub.writes.last().repeats)
    }

    @Test fun addPostsANewTaskWithoutAnId() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val saved = store.save(TaskChange.Add(RoutineTemplates.first().draft(say))).getOrThrow()!!
        val written = hub.writes.single()
        assertEquals("", written.id)
        assertEquals("会前准备", written.title)
        assertEquals("08:45", written.time)
        assertEquals(listOf(1, 2, 3, 4, 5), written.weekdays)
        assertEquals(ScheduledTask.DAILY, written.repeats)
        assertNull(written.createdBy)
        assertEquals(listOf("morning", saved.id), store.state.value.shownTasks!!.map { it.id })
    }

    @Test fun anEditWritesOnlyWhatThePersonChanged() = runTest {
        val hub = FakeHub(listOf(byPersona, morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val opened = morning.draft()
        // While the page is open Persona moves the task to 08:00 from a conversation; the person edits the prompt.
        hub.table = listOf(byPersona, morning.copy(time = "8:00"))
        store.loadTasks()
        assertTrue(store.save(TaskChange.Edit("morning", opened, opened.copy(prompt = "早报，三条以内"))).isSuccess)
        val written = hub.writes.single()
        assertEquals("08:00", written.time)
        assertEquals("早报，三条以内", written.prompt)
        assertEquals("09:00", morning.copy(time = "9:00").draft().time)
    }

    @Test fun turningContinueOffClearsTheConversationAndOnlyThat() = runTest {
        val hub = FakeHub(listOf(review))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val opened = review.draft()
        assertTrue(opened.continues)
        assertTrue(store.save(TaskChange.Edit("fu_review", opened, opened.copy(continues = false))).isSuccess)
        assertNull(hub.writes.single().sessionId)
        assertEquals("", hub.writes.single().editable()["session_id"].toString().trim('"'))
    }

    @Test fun inboxValuesRoundTripWithoutChangingPush() {
        for (mode in listOf("", "worthy", "every")) {
            val task = review.copy(push = ScheduledTask.PUSH_ASKED, inbox = mode)
            val opened = task.draft()
            assertEquals(mode, opened.inbox)
            val saved = task.applying(opened)
            assertEquals(mode, saved.editable()["inbox"].toString().trim('"'))
            assertEquals(ScheduledTask.PUSH_ASKED, saved.push)
        }
    }

    @Test fun inboxChangesArePatchedAndCanBeTurnedOff() {
        val opened = morning.draft()
        val changed = morning.patched(opened, opened.copy(inbox = "worthy"))
        assertEquals("worthy", changed.inbox)
        assertEquals(morning.push, changed.push)
        assertEquals(morning.sessionId, changed.sessionId)
        val turnedOff = changed.patched(changed.draft(), changed.draft().copy(inbox = ""))
        assertEquals("", turnedOff.editable()["inbox"].toString().trim('"'))
    }

    @Test fun aSwitchLeavesAnUnchangedInboxOutOfThePut() = runTest {
        val task = morning.copy(inbox = "every")
        val hub = FakeHub(listOf(task))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        store.toggle("morning", false)
        advanceUntilIdle()
        val written = hub.writes.single()
        assertNull(written.inbox)
        assertFalse(written.editable().containsKey("inbox"))
        assertEquals("every", hub.table.single().inbox)
        assertEquals("every", store.state.value.task("morning")?.inbox)
    }

    @Test fun inboxStaysUnsetForAnOldHubAndKeepsANewerSettingDuringAnotherEdit() {
        val opened = morning.draft()
        val untouched = morning.patched(opened, opened.copy(prompt = "Changed"))
        assertNull(untouched.inbox)
        assertFalse(untouched.editable().containsKey("inbox"))
        val changedMeanwhile = morning.copy(inbox = "every")
        val saved = changedMeanwhile.patched(opened, opened.copy(prompt = "Changed again"))
        assertNull(saved.inbox)
        assertFalse(saved.editable().containsKey("inbox"))
        assertEquals("Changed again", saved.prompt)
    }

    @Test fun unchangedInboxStillCountsAsSavedWhenTheAnswerIsLost() = runTest {
        val task = morning.copy(inbox = "every")
        val hub = FakeHub(listOf(task))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val opened = task.draft()
        hub.loseAnswer = true
        assertTrue(store.save(TaskChange.Edit("morning", opened, opened.copy(prompt = "Changed"))).isSuccess)
        assertNull(hub.writes.single().inbox)
        assertFalse(hub.writes.single().editable().containsKey("inbox"))
        assertEquals("every", store.state.value.task("morning")?.inbox)
    }

    /** "Only when it alerts" is written, leaves "Continue the original chat" (the hub runs it in its own conversation), and can be turned back. */
    @Test fun pushOnlyAskedIsWrittenAndLeavesTheOriginalConversation() = runTest {
        val hub = FakeHub(listOf(review))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val opened = review.draft()
        assertEquals("", opened.push)
        val asked = opened.pushing(ScheduledTask.PUSH_ASKED)
        assertFalse(asked.continues)
        assertFalse(asked.sameAs(opened))
        assertTrue(store.save(TaskChange.Edit("fu_review", opened, asked)).isSuccess)
        val written = hub.writes.single()
        assertEquals(ScheduledTask.PUSH_ASKED, written.push)
        assertNull(written.sessionId)
        assertEquals("asked", written.editable()["push"].toString().trim('"'))
        val saved = store.state.value.task("fu_review")!!
        assertTrue(saved.pushesOnlyAsked)
        assertTrue(store.save(TaskChange.Edit("fu_review", saved.draft(), saved.draft().pushing(""))).isSuccess)
        assertEquals("", hub.writes.last().push)
    }

    /** A task that never had "Push when done" does not send it, so a hub without `task_push` is never sent one. */
    @Test fun pushStaysUnsetUntilItIsChosen() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val opened = morning.draft()
        assertTrue(store.save(TaskChange.Edit("morning", opened, opened.copy(prompt = "早报，三条以内"))).isSuccess)
        assertNull(hub.writes.single().push)
        assertFalse(hub.writes.single().editable().containsKey("push"))
    }

    /** Persona set "Only when it alerts" from a conversation while the page was open; the person's other edit keeps it. */
    @Test fun pushSetMeanwhileStaysWhenThePersonEditsSomethingElse() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        val opened = morning.draft()
        hub.table = listOf(morning.copy(push = ScheduledTask.PUSH_ASKED))
        store.loadTasks()
        assertTrue(store.save(TaskChange.Edit("morning", opened, opened.copy(prompt = "早报，三条以内"))).isSuccess)
        assertEquals(ScheduledTask.PUSH_ASKED, hub.writes.single().push)
    }

    @Test fun editingATaskDeletedMeanwhileDoesNotBringItBack() = runTest {
        val hub = FakeHub(listOf(byPersona, morning))
        val store = ProactiveStore(hub, this, say)
        val messages = messagesOf(store)
        store.refresh()
        hub.table = listOf(morning)
        val result = store.save(TaskChange.Edit("task_weekly", byPersona.draft(), byPersona.draft().copy(title = "改个名")))
        assertTrue(result.isFailure)
        assertEquals(listOf("morning"), hub.table.map { it.id })
        assertEquals(listOf("morning"), store.state.value.shownTasks!!.map { it.id })
        assertEquals(1, messages.size)
    }

    @Test fun deleteTakesOutOnlyThatTaskAndALostAnswerStillCounts() = runTest {
        val hub = FakeHub(listOf(byPersona, morning, ci))
        val store = ProactiveStore(hub, this, say)
        val messages = messagesOf(store)
        store.refresh()
        assertTrue(store.save(TaskChange.Delete("task_weekly")).isSuccess)
        assertEquals(listOf("task_weekly"), hub.deleted)
        assertEquals(listOf("morning", "fu_ci"), store.state.value.shownTasks!!.map { it.id })
        assertTrue(store.state.value.busy.isEmpty())

        hub.failDelete = IOException("timeout")
        assertTrue(store.save(TaskChange.Delete("fu_ci")).isFailure)
        assertEquals(listOf("没删掉：连不上 hub"), messages)
        hub.deleteLandsAnyway = true
        assertTrue(store.save(TaskChange.Delete("fu_ci")).isSuccess)
        assertEquals(listOf("morning"), store.state.value.shownTasks!!.map { it.id })
        assertEquals(1, messages.size)
    }

    @Test fun runningNowReturnsTheConversationOrSaysWhy() = runTest {
        val hub = FakeHub(listOf(morning, review))
        val store = ProactiveStore(hub, this, say)
        val messages = messagesOf(store)
        store.refresh()
        assertEquals(SessionKey("server", "run-morning"), store.run("morning").getOrThrow())
        assertFalse(store.state.value.busy("morning"))
        assertEquals(SessionKey("server", "conversation-3"), store.run("fu_review").getOrThrow())
        advanceUntilIdle()
        // A one-off that ran is done.
        assertEquals(listOf("morning"), store.state.value.shownTasks!!.map { it.id })
        hub.failRun = IOException("refused")
        assertTrue(store.run("morning").isFailure)
        assertEquals(listOf("没跑起来：连不上 hub"), messages)
    }

    @Test fun runNowOpensTheConversationOnlyForAPageThatIsStillThere() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        val opened = mutableListOf<SessionKey>()
        val page = backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { store.runsToOpen.collect { opened += it } }
        store.runNow("morning")
        advanceUntilIdle()
        assertEquals(listOf(SessionKey("server", "run-morning")), opened)

        // Run now, then the page is left before the hub answers: the run still happens, nothing opens.
        hub.runAnswer = CompletableDeferred()
        store.runNow("morning")
        advanceUntilIdle()
        page.cancel()
        hub.runAnswer!!.complete(Unit)
        advanceUntilIdle()
        assertEquals(listOf("morning", "morning"), hub.started)
        assertEquals(listOf(SessionKey("server", "run-morning")), opened)

        // Nor is it kept for the next time the page opens.
        val again = mutableListOf<SessionKey>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { store.runsToOpen.collect { again += it } }
        advanceUntilIdle()
        assertTrue(again.isEmpty())
    }

    @Test fun freshForgetsTheTasksOwnConversation() = runTest {
        val hub = FakeHub(listOf(morning))
        val store = ProactiveStore(hub, this, say)
        val messages = messagesOf(store)
        store.refresh()
        assertEquals(SessionKey("server", "own-morning"), store.state.value.task("morning")!!.ownConversation)
        assertTrue(store.fresh("morning").isSuccess)
        assertNull(store.state.value.task("morning")!!.ownConversation)
        assertFalse(store.state.value.freshening("morning"))
        assertEquals(listOf("下次运行会开一段新对话"), messages)
    }

    @Test fun refreshKeepsWhatWasReadWhenAListFails() = runTest {
        val hub = object : ProactiveApi by FakeHub(listOf(morning)) {
            var fail = false
            override suspend fun tasks(): List<ScheduledTask> = if (fail) throw IOException("down") else listOf(morning)
        }
        val store = ProactiveStore(hub, this, say)
        store.refresh()
        hub.fail = true
        store.refresh()
        val state = store.state.value
        assertEquals(listOf(morning), state.tasks.value)
        assertEquals("连不上 hub，连上后下拉刷新。", state.tasks.error)
        assertFalse(state.refreshing)
        assertEquals("server", state.assistantMachine)
    }

    @Test fun theTransportsOwnCancellationIsAFailureNotAStuckPage() = runTest {
        val hub = object : ProactiveApi by FakeHub(listOf(morning)) {
            override suspend fun deleteTask(id: String) = throw kotlinx.coroutines.CancellationException("Pairing changed")
        }
        val store = ProactiveStore(hub, this, say)
        val messages = messagesOf(store)
        store.refresh()
        val result = store.save(TaskChange.Delete("morning"))
        assertTrue(result.isFailure)
        assertTrue(store.state.value.busy.isEmpty())
        assertEquals(listOf("没删掉：连接刚换过，请再试一次"), messages)
    }

    @Test fun runsAreNewestFirstAndOneTasksHistoryIsItsOwn() = runTest {
        val runs = listOf(
            Run(id = "1", kind = "routine", refId = "morning", at = "2026-11-01T01:00:00Z", status = "ok"),
            Run(id = "2", kind = "followup", refId = "fu_lunch", at = "2026-11-02T04:00:00Z", status = "ok"),
            Run(id = "3", kind = "routine", refId = "morning", at = "2026-11-02T01:00:00Z", status = "ok"),
        )
        val store = ProactiveStore(FakeHub(listOf(morning), runs = runs), this, say)
        store.refresh()
        assertEquals(listOf("2", "3", "1"), store.state.value.runList!!.map { it.id })
        assertEquals(listOf("3", "1"), store.state.value.runsOf("morning")!!.map { it.id })
    }
}

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ProactiveTextTest {
    private val say by lazy { pageText() }
    private val zone = java.time.ZoneId.of("Asia/Shanghai")
    private val now = java.time.Instant.parse("2026-11-02T02:00:00Z") // Monday 10:00 in Shanghai

    @Test fun rulesReadLikeReminders() {
        assertEquals("每个工作日 上午 9:00", say.sentence("09:00", listOf(5, 4, 3, 2, 1)))
        assertEquals("每天 晚上 9:30", say.sentence("21:30", (1..7).toList()))
        assertEquals("每天 早上 7:05", say.sentence("07:05", emptyList()))
        assertEquals("每个周末 中午 12:00", say.sentence("12:00", listOf(6, 7)))
        assertEquals("每周一、三、五 下午 5:00", say.sentence("17:00", listOf(5, 1, 3, 3)))
        assertEquals("凌晨 12:15", say.clock("00:15"))
        assertEquals("工作日", say.repeats(listOf(1, 2, 3, 4, 5)))
        assertEquals(emptyList<Int>(), normalizeDays(listOf(7, 6, 5, 4, 3, 2, 1, 9)))
        assertEquals(listOf(1, 3), normalizeDays(listOf(3, 1, 3, 0)))
        assertTrue(validTime("23:59"))
        assertFalse(validTime("24:00"))
        assertFalse(validTime("9:00"))
    }

    @Test fun hubTimesWithAnOffsetOrZoneZero() {
        assertEquals(java.time.Instant.parse("2026-11-03T01:00:00Z"), parseTime("2026-11-03T09:00:00+08:00"))
        assertEquals(java.time.Instant.parse("2026-11-03T01:00:00Z"), parseTime("2026-11-03T01:00:00Z"))
        assertEquals(java.time.Instant.parse("2026-11-03T01:00:00.5Z"), parseTime("2026-11-03T09:00:00.5+08:00"))
        assertNull(parseTime("0001-01-01T00:00:00Z"))
        assertNull(parseTime("not a time"))
        assertNull(parseTime(null))
        assertEquals("09:05", normalizeTime("9:05"))
        assertEquals("21:30", normalizeTime("21:30"))
        assertTrue(TaskDraft("早报", "做", time = "9:00").valid)
    }

    @Test fun momentsAndCountdowns() {
        val at = { text: String -> java.time.Instant.parse(text) }
        assertEquals("今天 15:30", say.moment(at("2026-11-02T07:30:00Z"), now, zone))
        assertEquals("明天 09:00", say.moment(at("2026-11-03T01:00:00Z"), now, zone))
        assertEquals("昨天 21:30", say.moment(at("2026-11-01T13:30:00Z"), now, zone))
        assertEquals("周五 17:00", say.moment(at("2026-11-06T09:00:00Z"), now, zone))
        assertEquals("11月20日 09:00", say.moment(at("2026-11-20T01:00:00Z"), now, zone))
        assertEquals("马上", say.until(at("2026-11-02T02:00:30Z"), now, zone))
        assertEquals("40 分钟后", say.until(at("2026-11-02T02:40:00Z"), now, zone))
        assertEquals("2 小时 30 分钟后", say.until(at("2026-11-02T04:30:00Z"), now, zone))
        assertEquals("7 小时后", say.until(at("2026-11-02T09:05:00Z"), now, zone))
        assertEquals("4 天后", say.until(at("2026-11-06T09:00:00Z"), now, zone))
        assertEquals("已过时间", say.until(at("2026-11-02T01:00:00Z"), now, zone))
        assertEquals("准时", say.lateness(at("2026-11-02T01:00:00Z"), at("2026-11-02T01:00:01Z")))
        assertEquals("晚了 21 秒", say.lateness(at("2026-11-02T01:00:00Z"), at("2026-11-02T01:00:21Z")))
        assertEquals("晚了 45 分钟", say.lateness(at("2026-11-02T01:00:00Z"), at("2026-11-02T01:45:00Z")))
        assertNull(say.lateness(null, now))
    }

    @Test fun runsAndRoutinesSayWhatHappened() {
        assertEquals("准时", Run(status = "ok", due = "2026-11-02T01:00:00Z", at = "2026-11-02T01:00:02Z").detail(say))
        assertEquals("手动运行", Run(status = "ok", manual = true).detail(say))
        assertEquals("没跑起来：助手所在的机器离线（手动运行）", Run(status = "failed", error = "assistant machine is offline", manual = true).detail(say))
        assertEquals("跳过了：Persona 停机，错过了时间", Run(status = "skipped", error = "Persona 停机，错过了时间").detail(say))
        assertEquals("已跳过", Run(status = "skipped").detail(say))
        assertEquals(SessionKey("server", "s"), Run(sessionId = "s").conversation("server"))
        assertNull(Run(sessionId = "s").conversation(null))
        assertEquals("跟进", Run(kind = "followup").displayTitle(say))
        val noHistory = ProactiveState(assistantMachine = "server")
        assertEquals("上次 周五 17:00 没跑起来：助手所在的机器离线", noHistory.lastRunOf(byPersona)!!.line(say, now, zone))
        assertEquals("上次 今天 09:00 已运行", noHistory.lastRunOf(morning)!!.line(say, now, zone))
        assertEquals(SessionKey("server", "conversation-2"), noHistory.lastRunOf(morning)!!.conversation)
        assertNull(noHistory.lastRunOf(ScheduledTask()))
        // "Run now" does not move the hub's last_run: the history knows better.
        val manual = Run(id = "m", kind = "routine", refId = "morning", at = "2026-11-02T01:30:00Z", status = "failed",
            error = "assistant machine is offline", manual = true, machineId = "laptop", sessionId = "")
        val withHistory = ProactiveState(runs = Loadable(listOf(manual)), assistantMachine = "server")
        assertEquals("上次 今天 09:30 没跑起来：助手所在的机器离线", withHistory.lastRunOf(morning)!!.line(say, now, zone))
        assertTrue(withHistory.lastRunOf(morning)!!.failed)
        assertNull(withHistory.lastRunOf(morning)!!.conversation)
        val ran = ProactiveState(runs = Loadable(listOf(manual.copy(status = "ok", sessionId = "s1"))))
        assertEquals("上次 今天 09:30 手动运行", ran.lastRunOf(morning)!!.line(say, now, zone))
        assertEquals(SessionKey("laptop", "s1"), ran.lastRunOf(morning)!!.conversation)
        assertEquals("下次 明天 09:00，23 小时后", morning.nextLine(say, now, zone))
        assertEquals("已关闭，不会自动运行", morning.copy(enabled = false).nextLine(say, now, zone))
        assertEquals("这个跟进已经不在了", hubErrorText(say, " no such follow-up "))
        assertEquals("routine \"x\" needs a title", hubErrorText(say, "routine \"x\" needs a title"))
    }

    @Test fun oneOffsSayWhereTheyGo() {
        val machines = listOf(Machine("server", name = "服务器"))
        assertEquals("会前准备", review.displayTitle(say))
        assertEquals("看看 CI 跑完没有", ci.displayTitle(say))
        assertEquals("在 服务器 上接着原来的对话", review.whereLine(say, machines))
        assertEquals("开一段新对话", ci.whereLine(say, machines))
        assertEquals("在同一个对话里运行", morning.whereLine(say, machines))
        assertEquals(SessionKey("server", "conversation-3"), review.origin)
        assertNull(ci.origin)
    }

    @Test fun rulesForEveryKindOfTask() {
        assertEquals("每个工作日 上午 9:00", say.rule(morning, now, zone))
        assertEquals("每 30 分钟", say.rule(patrol, now, zone))
        assertEquals("每 2 小时", say.every(120))
        assertEquals("每 1 小时 30 分钟", say.every(90))
        assertEquals("1 小时 30 分钟", say.interval(90))
        assertEquals("今天 11:00", say.rule(review, now, zone))
        assertEquals("自动审批", patrol.runsWith(say))
        assertNull(morning.runsWith(say))
        assertEquals("完全访问", morning.copy(permissionMode = BypassMode).runsWith(say))
    }

    @Test fun timelineAndSummary() {
        val off = morning.copy(id = "evening", title = "晚间回顾", enabled = false, nextRun = "2026-11-02T13:30:00Z")
        val list = upcoming(say, listOf(byPersona, morning, off, ci, review, patrol), now, zone)
        assertEquals(listOf("patrol", "fu_review", "fu_ci", "morning", "task_weekly"), list.map { it.id })
        assertEquals("在 server 上接着原来的对话", list[1].detail)
        assertEquals("每个工作日 上午 9:00", list[3].detail)
        assertEquals("2 个定时任务开着，下一件在今天 11:00。", summaryLine(say, listOf(byPersona, morning, off, review, ci), now, zone))
        assertEquals("定时任务都关着，下一件在今天 11:00。", summaryLine(say, listOf(off, review), now, zone))
        assertEquals("还没有安排，Persona 只在你开口时做事。", summaryLine(say, emptyList(), now, zone))
        assertNull(summaryLine(say, null, now, zone))
    }

    @Test fun pagesSurviveAsKeys() {
        assertEquals(listOf(ProactivePage.Overview), startPages(null))
        assertEquals(listOf(ProactivePage.Overview, ProactivePage.EditRoutine("morning")), startPages("routine:morning"))
        assertEquals(listOf(ProactivePage.Overview, ProactivePage.AddRoutine(-1)), startPages("new"))
        assertEquals(listOf(ProactivePage.Overview, ProactivePage.RunList("morning")), startPages("runs:morning"))
        assertEquals(listOf(ProactivePage.Overview), startPages("nonsense"))
        listOf(ProactivePage.Overview, ProactivePage.EditRoutine("a:b"), ProactivePage.AddRoutine(2), ProactivePage.FollowupList,
            ProactivePage.RunList(null), ProactivePage.RunList("x")).forEach { assertEquals(it, ProactivePage.parse(it.key)) }
    }

    @Test fun draftsCompareByWhatTheHubStores() {
        assertTrue(TaskDraft(" 早报 ", "做", time = "09:00", weekdays = (1..7).toList()).sameAs(TaskDraft("早报", "做 ", time = "09:00", weekdays = emptyList())))
        assertFalse(TaskDraft("早报", "做", time = "09:00").sameAs(TaskDraft("早报", "做", time = "09:30")))
        // Another rule's leftovers do not count.
        assertTrue(TaskDraft("巡检", "看", TaskRepeat.Interval, time = "09:00").sameAs(TaskDraft("巡检", "看", TaskRepeat.Interval, time = "10:00")))
        assertFalse(TaskDraft(" ", "做").valid)
        assertTrue(TaskDraft("早报", "做").valid)
        // A one-off may go without a title but needs a time; an interval needs one the hub takes.
        assertTrue(TaskDraft("", "做", TaskRepeat.Once, at = now).valid)
        assertFalse(TaskDraft("", "做", TaskRepeat.Once).valid)
        assertFalse(TaskDraft("巡检", "看", TaskRepeat.Interval, everyMinutes = 2).valid)
        assertEquals(TaskDraft("周报", "起草周报", TaskRepeat.Daily, null, "17:00", listOf(5), DefaultInterval, true), byPersona.draft())
        assertEquals(TaskDraft("巡检", "看看服务", TaskRepeat.Interval, parseTime(patrol.at), "09:00", emptyList(), 30, true, permissionMode = "auto"), patrol.draft())
    }
}
