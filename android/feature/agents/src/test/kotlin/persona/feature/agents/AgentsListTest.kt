package persona.feature.agents

import persona.core.contracts.*
import persona.core.domain.answer.*
import persona.core.domain.sort.*
import org.junit.Assert.*
import org.junit.Test
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import java.time.Instant

/** Robolectric for the strings: Simplified Chinese, the language the expected text is in. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class AgentsListTest {
    private val res get() = RuntimeEnvironment.getApplication().resources

    @Test fun historyRangeKeepsRunningBehaviorAndSortsProjectsStably() {
        val working = Session(machineId = "machine", sessionId = "running", status = "working", cwd = "/code/project")
        val a = Session(machineId = "machine", sessionId = "a", status = "offline", cwd = "/code/project",
            title = "自定义名称", updatedAt = "2026-01-01T00:00:00Z")
        val b = a.copy(sessionId = "b")
        val other = a.copy(machineId = "other", sessionId = "c")
        val rows = listOf(b, working, other, a).map(::AgentSession)
        val machines = listOf(Machine("machine"), Machine("other"))
        val prefs = AgentsPreferencesSnapshot()
        val settings = AgentListSettings(grouping = AgentGrouping.State)
        val running = buildAgentList(rows, machines, settings = settings)
        assertEquals(running, scopedAgentList(rows, machines, prefs, settings))
        val history = scopedAgentList(rows, machines, prefs.copy(range = AgentRange.History), settings)
        assertEquals(2, history.groups.size)
        assertEquals(listOf("a", "b"), history.groups.first().rows.map { it.session.sessionId })
        assertEquals("自定义名称", history.groups.first().rows.first().session.displayTitle)
        assertEquals(history, scopedAgentList(rows.reversed(), machines, prefs.copy(range = AgentRange.History), settings))
        assertEquals(4, scopedAgentList(rows, machines, prefs.copy(range = AgentRange.All), settings).groups.sumOf { it.rows.size })
        assertEquals(1, scopedAgentList(rows, machines, prefs.copy(range = AgentRange.History), settings.copy(machineFilter = "other")).groups.size)
        assertEquals(running, scopedAgentList(rows, machines, prefs, settings))
        assertNotEquals(agentRangeKey("first/pairing"), agentRangeKey("second/pairing"))
    }

    @Test fun mixedOriginsUseEachHubsSettingForRowsAndCountsAcrossRanges() {
        val live = Session("machine", "live", state = "working", cwd = "/code/a", paneId = "botmux:1")
        val old = live.copy(sessionId = "old", status = "offline", state = "offline", paneId = "botmux:2")
        val other = live.copy(machineId = GroupKeys.machine("machine", "g11111111"), sessionId = "other")
        val pane = live.copy(sessionId = "pane", paneId = "w1:p1")
        val history = old.copy(sessionId = "history", paneId = "w1:p2")
        val sessions = listOf(live, old, other, pane, history)
        val machines = listOf(Machine(live.machineId), Machine(other.machineId))
        fun list(range: AgentRange, hidden: Set<String?>, grouping: AgentGrouping = AgentGrouping.Repo): Pair<Set<String>, Int> {
            val prefs = AgentsPreferencesSnapshot(range = range, hiddenBotmuxHubs = hidden)
            val result = scopedAgentList(sessions.filterNot(prefs::hides).map(::AgentSession), machines, prefs, AgentListSettings(grouping = grouping))
            return result.groups.flatMap { it.rows }.map { it.session.sessionId }.toSet() to (result.counts[AgentQuickFilter.Feishu] ?: 0)
        }
        assertEquals(setOf("other", "pane") to 1, list(AgentRange.Running, setOf(null)))
        assertEquals(setOf("history") to 0, list(AgentRange.History, setOf(null)))
        assertEquals(setOf("other", "pane", "history") to 1, list(AgentRange.All, setOf(null)))
        assertEquals(setOf("live", "other", "pane") to 2, list(AgentRange.Running, emptySet()))
        assertEquals(setOf("old", "history") to 1, list(AgentRange.History, emptySet()))
        // Recently Used includes past sessions in its flat timeline. Its origin count follows those rows too.
        assertEquals(sessions.map { it.sessionId }.toSet() to 3, list(AgentRange.Running, emptySet(), AgentGrouping.Recent))
        assertFalse(AgentQuickFilter.Feishu in AgentQuickFilter.shown(AgentQuickFilter.Feishu, emptyMap()))
    }

    @Test fun missingRequestSourceIsOmittedWhileKnownSourcesRemain() {
        val request = Interaction("request", kind = "permission")
        assertNull(request.sourceLabel(res, AgentsMetadata()))
        assertNull(request.sourceLabel(res, AgentsMetadata(requestSources = mapOf("request" to "  "))))
        assertEquals("子 Agent", request.sourceLabel(res, AgentsMetadata(requestSources = mapOf("request" to "子 Agent"))))
        assertEquals("终端", request.copy(kind = "screen").sourceLabel(res, AgentsMetadata()))
        assertNull(request.copy(toolName = "mcp__tools__read").sourceLabel(res, AgentsMetadata()))
        assertEquals("来自 MCP 服务器 example-service", request.copy(mcpServerName = "example-service").sourceLabel(res, AgentsMetadata()))
        assertEquals("来自 MCP 服务器 Persona", request.copy(mcpServerName = "persona").sourceLabel(res, AgentsMetadata()))
    }

    @Test fun aUsageLimitedUnreadSessionStaysUnreadInTheAgentsList() {
        val resets = Instant.now().plusSeconds(3600).toString()
        val limited = Session(machineId = "machine", sessionId = "limited", state = "unread", title = "Wait",
            turnError = "Weekly limit reached", turnErrorKind = "usage_limit", turnErrorResetsAt = resets)
        val plain = Session(machineId = "machine", sessionId = "plain", state = "unread", title = "Wait")
        val cut = Session(machineId = "machine", sessionId = "cut", state = "unread", title = "Wait", turnError = "stopped")
        assertTrue(limited.interrupted)
        assertTrue(limited.isUsageLimited())
        assertEquals(plain.agentState, limited.agentState)
        val settings = AgentListSettings(grouping = AgentGrouping.State, quick = AgentQuickFilter.Unread)
        fun listed(session: Session) = buildAgentList(listOf(AgentSession(session)), settings = settings).groups.single().visibleRows.single()
        val limitRow = listed(limited)
        assertEquals(AgentState.Unread, limitRow.state)
        assertEquals(listed(plain).state, limitRow.state)
        assertEquals(listed(cut).state, limitRow.state)
        assertEquals(listed(plain).rowContent(), limitRow.rowContent())
        assertEquals(listed(cut).rowContent(), limitRow.rowContent())
    }

    @Test fun idleRowsStayCollapsedUntilExpandedAndRemainHiddenInFoldedGroup() {
        val rows = listOf("working", "idle", "idle").mapIndexed { index, state ->
            AgentSession(Session(machineId = "machine", sessionId = "session-$index", state = state, spaceId = "space", spaceName = "空间"))
        }
        val settings = AgentListSettings(grouping = AgentGrouping.MachineSpace)
        val collapsed = buildAgentList(rows, settings = settings).groups.single()
        assertEquals(2, collapsed.rows.size - collapsed.visibleRows.size)
        assertEquals(AgentState.Working, collapsed.visibleRows.single().state)
        val expanded = settings.copy(expandedIdle = setOf(collapsed.id))
        assertEquals(3, buildAgentList(rows, settings = expanded).groups.single().visibleRows.size)
        assertTrue(buildAgentList(rows, settings = expanded.copy(folded = setOf(collapsed.id))).groups.single().visibleRows.isEmpty())
    }

    @Test fun openedUnreadRowKeepsItsGroupAndPositionAfterReadReceipt() {
        val first = Session(machineId = "machine", sessionId = "first", state = "unread", updatedAt = "2026-01-02T00:00:00Z")
        val second = Session(machineId = "machine", sessionId = "second", state = "unread", updatedAt = "2026-01-01T00:00:00Z")
        val settings = AgentListSettings(grouping = AgentGrouping.State, quick = AgentQuickFilter.Unread)
        val original = buildAgentList(listOf(AgentSession(first), AgentSession(second)), settings = settings)
        val next = buildAgentList(listOf(AgentSession(first.copy(state = "idle")), AgentSession(second)),
            settings = settings.copy(kept = setOf(first.key.path)))
        val retained = retainOpenedRows(next, original, setOf(first.key.path))
        assertEquals(listOf("state/unread"), retained.groups.map { it.id })
        assertEquals(listOf(first.key.path, second.key.path), retained.groups.single().visibleRows.map { it.id })
        assertEquals(AgentState.Idle, retained.groups.single().visibleRows.first().state)
        assertEquals(1, retained.counts[AgentQuickFilter.Unread])
        val reselected = buildAgentList(listOf(AgentSession(first.copy(state = "idle")), AgentSession(second)), settings = settings)
        assertEquals(listOf(second.key.path), reselected.groups.flatMap { it.visibleRows }.map { it.id })
    }

    @Test fun byRepositoryAnOpenedRowStaysInItsCheckoutWhenATreeAppearsOrGoes() {
        val opened = Session(machineId = "m", sessionId = "opened", state = "unread", cwd = "/code/app", updatedAt = "2026-01-02T00:00:00Z")
        val tree = Session(machineId = "m", sessionId = "tree", state = "unread", cwd = "/code/app/.worktrees/wt", updatedAt = "2026-01-01T00:00:00Z")
        val settings = AgentListSettings(grouping = AgentGrouping.Repo, quick = AgentQuickFilter.Unread)
        val kept = setOf(opened.key.path)
        fun list(vararg sessions: Session, keep: Set<String> = emptySet()) =
            buildAgentList(sessions.map(::AgentSession), listOf(Machine("m", online = true)), settings = settings.copy(kept = keep))
        fun assertInTree(result: AgentListResult) {
            val inTree = result.machines.flatMap { it.groups }.toSet()
            assertTrue("no stray section outside the computer", result.groups.all { it.id in inTree })
            assertTrue("a repository with trees holds no rows of its own", result.groups.filter { group -> result.groups.any { it.under == group.id } }.all { it.rows.isEmpty() })
            assertEquals("tree/m//code/app", result.groups.single { group -> group.rows.any { it.id == opened.key.path } }.id)
        }
        // A second working tree appears while the opened row is kept: it stays in the main checkout, not on the repository.
        val appeared = retainOpenedRows(list(opened.copy(state = "idle"), tree, keep = kept), list(opened), kept)
        assertInTree(appeared)
        assertEquals(listOf("repo/m//code/app", "tree/m//code/app", "tree/m//code/app/.worktrees/wt"), appeared.groups.map { it.id })
        // The other tree goes: the repository is one level again and no old tree group is brought back at the bottom.
        val gone = retainOpenedRows(list(opened.copy(state = "idle"), keep = kept), list(opened, tree), kept)
        assertInTree(gone)
        assertEquals(listOf("tree/m//code/app"), gone.groups.map { it.id })
    }

    @Test fun quickAnswersPreserveWireLabelsAndEnforceTheInlineBoundary() {
        val question = Question(question = "选择处理方式", options = listOf(QuestionOption("先备份（推荐）"), QuestionOption("保留")))
        val request = Interaction("request", kind = "question", questions = listOf(question), expiresAt = "2026-01-01T00:01:00Z")
        assertNotNull(request.quickQuestion())
        val answer = AnswerBuilder.build(request, AnswerAction.PickOption, AnswerDraft(optionIndex = 0)).resolution
        assertEquals(mapOf(question.question to "先备份（推荐）"), answer?.answers)
        assertNull(request.copy(questions = listOf(question.copy(multiSelect = true))).quickQuestion())
        assertNull(request.copy(questions = listOf(question.copy(options = List(5) { QuestionOption("选项 $it") }))).quickQuestion())
        assertNull(request.copy(secret = true).quickQuestion())
        assertEquals("即将过期", countdown(request.expiresAt, Instant.parse("2026-01-01T00:00:30Z")))
        assertTrue(request.expired(Instant.parse("2026-01-01T00:01:00Z")))
        assertEquals(RequestExpiry.expiredLabel.shown, countdown(request.expiresAt, Instant.parse("2026-01-01T00:01:01Z")))
        assertTrue(request.copy(expiresAt = "invalid").expired(Instant.parse("2026-01-01T00:00:30Z")))
        assertTrue(AgentRowMetadata(hasAttachments = true).hasDraft)
        assertFalse(AgentRowMetadata().hasDraft)
    }
}
