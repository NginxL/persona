package persona.feature.chat

import android.content.Context
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class SessionOverviewSheetTest {
    @get:Rule val rule = createComposeRule()

    private val actions = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun info(session: Session) = SessionInfo(
            sessionId = session.sessionId,
            cwd = "/repo/app",
            model = "claude-opus-5",
            version = "2.1.0",
            contextTokens = 48_000,
            turns = 4,
            outputTokens = 10_000,
        )
    }

    @Test fun rendersSectionsInOrderCollapsesEndedByDefaultAndOpensSubagentDetail() {
        val res = ApplicationProvider.getApplicationContext<Context>().resources
        val state = ChatChromeState(res, actions, CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate), {}, {})
        val parent = Session(
            machineId = "mac",
            sessionId = "parent-1",
            title = "Main session",
            cwd = "/repo/app",
            agent = "claude",
            state = "working",
            contextTokens = 48_000,
            contextWindow = 200_000,
            subagents = listOf(
                Subagent(
                    id = "emb-done",
                    sessionId = "parent-1@emb-done",
                    description = "Finished audit",
                    running = false,
                    startedAt = "2026-11-24T00:10:00Z",
                    updatedAt = "2026-11-24T00:20:00Z",
                ),
            ),
            background = listOf(
                BackgroundTask(id = "bg-1", kind = "shell", description = "Watch tests", startedAt = "2026-11-24T00:30:00Z"),
            ),
        )
        val crossChild = Session(
            machineId = "linux-box",
            sessionId = "remote-sub",
            title = "Cross-machine build",
            agent = "codex",
            state = "needs_you",
            attentionReason = "Approve release step",
            isSubagent = true,
            parentMachineId = "mac",
            parentSessionId = "parent-1",
            foldedUnder = "parent-1",
            foldedUnderMachineId = "mac",
            a2aTaskId = "a2a-7",
            subagentOrigin = "a2a",
            startedAt = "2026-11-24T00:40:00Z",
        )
        val opened = mutableListOf<SessionKey>()

        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                SessionOverviewSheet(
                    session = parent,
                    machine = Machine("mac", "MacBook", online = true),
                    machines = listOf(Machine("mac", "MacBook", online = true), Machine("linux-box", "87K-itx", online = true)),
                    childRows = listOf(crossChild),
                    state = state,
                    onOpenSubagent = { opened += it },
                    onDismiss = {},
                )
            }
        }
        rule.waitForIdle()

        // Verify key section headings exist and scroll to Subagents & Background tasks.
        rule.onNodeWithText("Session overview").assertExists()
        rule.onNodeWithText("Environment").assertExists()
        rule.onNodeWithTag("session-overview-list").performScrollToKey("section-context")
        rule.onAllNodesWithText("Context").onFirst().assertExists()

        rule.onNodeWithTag("session-overview-list").performScrollToKey("section-subagents")
        rule.onNodeWithText("Subagents").assertExists()
        rule.onNodeWithText("Running · 1").assertExists()
        rule.onNodeWithText("Cross-machine build").assertExists()
        rule.onNodeWithText("87K-itx · Codex").assertExists()

        // Ended subagent starts collapsed and expands on tap.
        rule.onNodeWithText("Finished audit").assertDoesNotExist()
        rule.onNodeWithTag("session-overview-list").performScrollToKey("subagents-ended-toggle")
        rule.onNodeWithTag("overview-ended-toggle").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("session-overview-list").performScrollToKey("sub-end:embedded:mac/parent-1:emb-done")
        rule.onNodeWithText("Finished audit").assertExists()

        // Tapping ended embedded subagent opens its parent@agent target key.
        rule.onNodeWithTag("overview-subagent-row:embedded:mac/parent-1:emb-done").performClick()
        // Tapping cross-machine A2A subagent opens its remote SessionKey.
        rule.onNodeWithTag("session-overview-list").performScrollToKey("sub-run:session:linux-box/remote-sub")
        rule.onNodeWithTag("overview-subagent-row:session:linux-box/remote-sub").performClick()
        assertEquals(listOf(SessionKey("mac", "parent-1@emb-done"), SessionKey("linux-box", "remote-sub")), opened)

        // Background tasks section appears after Subagents.
        rule.onNodeWithTag("session-overview-list").performScrollToKey("section-background")
        rule.onNodeWithText("Background tasks").assertExists()
    }

    @Test fun paginatedSummaryParentLoadsCompleteRowForEmbeddedSubagents() {
        val res = ApplicationProvider.getApplicationContext<Context>().resources
        val fullParent = Session(
            machineId = "mac",
            sessionId = "parent-summary",
            title = "Main session",
            cwd = "/repo/app",
            agent = "claude",
            state = "working",
            subagents = listOf(
                Subagent(
                    id = "emb-live",
                    sessionId = "parent-summary@emb-live",
                    description = "Embedded from completeRow",
                    running = true,
                    startedAt = "2026-11-24T00:40:00Z",
                ),
            ),
        )
        val pagedActions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun info(session: Session) = SessionInfo(sessionId = session.sessionId, cwd = "/repo/app")
            override suspend fun completeRow(session: Session): Session = fullParent
        }
        val state = ChatChromeState(res, pagedActions, CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate), {}, {})
        // Summary=1 parent has waiting != null, runningSubagents = 1, and omits subagents array.
        val summaryParent = fullParent.copy(subagents = null, waiting = true, runningSubagents = 1)

        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("sessions_lookup")) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    SessionOverviewSheet(
                        session = summaryParent,
                        machine = Machine("mac", "MacBook", online = true),
                        machines = listOf(Machine("mac", "MacBook", online = true)),
                        childRows = emptyList(),
                        state = state,
                        actions = pagedActions,
                        onDismiss = {},
                    )
                }
            }
        }
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()

        rule.onNodeWithTag("session-overview-list").performScrollToKey("section-subagents")
        rule.onNodeWithText("Embedded from completeRow").assertExists()
    }

    @Test fun scrollToSubagentsWaitsForAsyncChildrenAndHoistedEndedExpandedSurvivesReopen() {
        val res = ApplicationProvider.getApplicationContext<Context>().resources
        val state = ChatChromeState(res, actions, CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate), {}, {})
        val parent = Session(machineId = "mac", sessionId = "p-async", title = "Parent", cwd = "/repo/app", agent = "claude", state = "idle")
        val endedChild = Session(
            machineId = "linux-box", sessionId = "ended-1", title = "Completed remote task", agent = "codex", state = "idle",
            isSubagent = true, parentMachineId = "mac", parentSessionId = "p-async",
            foldedUnder = "p-async", foldedUnderMachineId = "mac",
        )
        var children by mutableStateOf<List<Session>>(emptyList())
        var scrollPending by mutableStateOf(true)
        var scrolledCount = 0
        var sheetOpen by mutableStateOf(true)
        var hoistedEndedExpanded by mutableStateOf(false)

        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                if (sheetOpen) {
                    SessionOverviewSheet(
                        session = parent,
                        machine = Machine("mac", "MacBook", online = true),
                        machines = listOf(Machine("mac", "MacBook", online = true), Machine("linux-box", "87K-itx", online = true)),
                        childRows = children,
                        state = state,
                        scrollToSubagents = scrollPending,
                        onSubagentsScrolled = { scrollPending = false; scrolledCount++ },
                        endedExpanded = hoistedEndedExpanded,
                        onEndedExpandedChange = { hoistedEndedExpanded = it },
                        onDismiss = {},
                    )
                }
            }
        }
        rule.waitForIdle()
        // Before async children arrive, scrollToSubagents stays pending and is not consumed prematurely.
        assertTrue(scrollPending)
        assertEquals(0, scrolledCount)

        // Once children arrive, the Subagents header mounts and consumes scrollToSubagents.
        children = listOf(endedChild)
        rule.waitForIdle()
        assertFalse(scrollPending)
        assertEquals(1, scrolledCount)

        // Expand ended group, simulate pushing detail (sheet closes) and returning (sheet reopens).
        rule.onNodeWithTag("session-overview-list").performScrollToKey("subagents-ended-toggle")
        rule.onNodeWithTag("overview-ended-toggle").performClick()
        rule.waitForIdle()
        assertTrue(hoistedEndedExpanded)
        rule.onNodeWithText("Completed remote task").assertExists()

        sheetOpen = false
        rule.waitForIdle()
        sheetOpen = true
        rule.waitForIdle()

        // Ended group stays expanded upon returning from subagent detail.
        rule.onNodeWithTag("session-overview-list").performScrollToKey("sub-end:session:linux-box/ended-1")
        rule.onNodeWithText("Completed remote task").assertExists()
    }

    @Test fun authoritativeCompleteRowClearsStoppedLoopAndFailureShowsSubagentsRetryForAssistant() {
        val res = ApplicationProvider.getApplicationContext<Context>().resources
        var shouldFail = true
        val fullAssistant = Session(
            machineId = "mac",
            sessionId = "asst-summary",
            title = "Assistant chat",
            cwd = "/repo/app",
            agent = "claude",
            assistant = true,
            state = "working",
            loop = null,
            subagents = listOf(
                Subagent(
                    id = "emb-recovered",
                    sessionId = "asst-summary@emb-recovered",
                    description = "Recovered embedded worker",
                    running = true,
                    startedAt = "2026-11-24T00:40:00Z",
                ),
            ),
        )
        val retryActions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun info(session: Session) = SessionInfo(sessionId = session.sessionId, cwd = "/repo/app")
            override suspend fun completeRow(session: Session): Session {
                if (shouldFail) throw IllegalStateException("offline")
                return fullAssistant
            }
        }
        val state = ChatChromeState(res, retryActions, CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate), {}, {})
        // Summary=1 assistant parent with stale loop + runningSubagents=1, omitting subagents array.
        val summaryAssistant = fullAssistant.copy(
            subagents = null,
            waiting = true,
            runningSubagents = 1,
            loop = LoopInfo(kind = "interval", intervalSeconds = 60),
        )

        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("sessions_lookup")) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    SessionOverviewSheet(
                        session = summaryAssistant,
                        machine = Machine("mac", "MacBook", online = true),
                        machines = listOf(Machine("mac", "MacBook", online = true)),
                        childRows = emptyList(),
                        state = state,
                        actions = retryActions,
                        onDismiss = {},
                    )
                }
            }
        }
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()

        // Complete-row failure keeps Subagents section mounted with inline Retry even for Assistant sessions.
        rule.onNodeWithTag("session-overview-list").performScrollToKey("section-subagents")
        rule.onNodeWithText("Subagents").assertExists()
        rule.onNodeWithTag("session-overview-list").performScrollToKey("subagents-error")

        // Retrying succeeds: authoritative complete row loads embedded subagent and does not resurrect stopped loop.
        shouldFail = false
        rule.onNodeWithText("Retry").performClick()
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()

        rule.onNodeWithTag("session-overview-list").performScrollToKey("sub-run:embedded:mac/asst-summary:emb-recovered")
        rule.onNodeWithText("Recovered embedded worker").assertExists()
    }

    @Test fun authoritativeCompleteRowClearsStoppedLoopInOverview() {
        val res = ApplicationProvider.getApplicationContext<Context>().resources
        val stoppedLoopFull = Session(
            machineId = "mac",
            sessionId = "agent-loop-stopped",
            title = "Agent chat",
            cwd = "/repo/app",
            agent = "claude",
            state = "idle",
            loop = null,
            background = emptyList(),
            workflows = emptyList(),
        )
        val staleSummary = stoppedLoopFull.copy(
            waiting = true,
            loop = LoopInfo(kind = "interval", intervalSeconds = 60, reason = "Stale loop"),
        )
        val fullActions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun info(session: Session) = SessionInfo(sessionId = session.sessionId, cwd = "/repo/app")
            override suspend fun completeRow(session: Session): Session = stoppedLoopFull
        }
        val state = ChatChromeState(res, fullActions, CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate), {}, {})

        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("sessions_lookup")) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    SessionOverviewSheet(
                        session = staleSummary,
                        machine = Machine("mac", "MacBook", online = true),
                        machines = listOf(Machine("mac", "MacBook", online = true)),
                        childRows = emptyList(),
                        state = state,
                        actions = fullActions,
                        onDismiss = {},
                    )
                }
            }
        }
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()

        // Complete parent row returned loop=null, so stale summary loop must not resurrect Background tasks section.
        rule.onNodeWithText("Background tasks").assertDoesNotExist()
        rule.onNodeWithText("Stale loop").assertDoesNotExist()
    }
}
