package persona.feature.machines

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.buildAgentList
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import java.util.Locale

@OptIn(ExperimentalCoroutinesApi::class)
class MachinesTest {
    @Test fun loadPerCoreMatchesTheWebRoundingAndOmitsMissingReadings() {
        val languages = listOf(Locale.ENGLISH, Locale.SIMPLIFIED_CHINESE, Locale.JAPANESE)
        for (language in languages) {
            assertEquals("1.4", MetricFormat.loadPerCore(loadPerCore(5.6, 4)!!, language))
            assertEquals("1.3", MetricFormat.loadPerCore(loadPerCore(10.0, 8)!!, language))
            assertEquals("1.1", MetricFormat.loadPerCore(loadPerCore(23.0, 20)!!, language))
            assertEquals("0.0", MetricFormat.loadPerCore(loadPerCore(0.0, 4)!!, language))
        }
        for (load in listOf(null, -1.0, Double.NaN, Double.POSITIVE_INFINITY)) assertNull(loadPerCore(load, 4))
        for (cores in listOf(null, 0, -1)) assertNull(loadPerCore(5.6, cores))
    }

    @Test fun loadAlertThresholdsUseTheUnroundedReading() {
        fun loadAlerts(load: Double, cores: Int?) = alerts(MetricSample(0, load = load), cores)
        assertTrue(loadAlerts(3.99, 4).isEmpty())
        assertEquals(listOf(MetricAlert(R.string.machines_alert_load, false)), loadAlerts(4.0, 4))
        assertEquals(listOf(MetricAlert(R.string.machines_alert_load, false)), loadAlerts(7.99, 4))
        assertEquals(listOf(MetricAlert(R.string.machines_alert_load, true)), loadAlerts(8.0, 4))
        assertTrue(loadAlerts(8.0, null).isEmpty())
    }

    @Test fun bytesPickTheUnitAfterRounding() {
        val en = java.util.Locale.ENGLISH
        assertEquals("1,023.0 B", MetricFormat.bytes(1023, en))
        assertEquals("1.0 KB", MetricFormat.bytes(1023.96, en))
        assertEquals("1.0 KB", MetricFormat.bytes(1024, en))
        assertEquals("1,023.9 KB", MetricFormat.bytes(1023.94 * 1024, en))
        assertEquals("1.0 MB", MetricFormat.bytes(1024.0 * 1024 - 20, en))
        assertEquals("1.0 GB/s", MetricFormat.rate(1023.97 * 1024 * 1024, en))
        assertEquals("16.0 GB", MetricFormat.bytes(16L shl 30, en))
        assertEquals("2,048.0 TB", MetricFormat.bytes(2048.0 * (1L shl 40), en))
    }

    /** A lead with agents it created (#4494): the card counts what the Agents list counts for the computer. */
    @Test fun theCardCountsTheSessionsTheAgentsListCounts() {
        fun runtime(id: String, parent: String? = null, state: String = "working") = Session("machine", id, state = state,
            status = if (state == "needs_you") "blocked" else state, sourceKind = "runtime", agent = "claude",
            isSubagent = parent != null, parentSessionId = parent)
        val codexSubagent = Session("machine", "thread", state = "working", status = "working", sourceKind = "codex_app_server",
            agent = "codex", parentThreadId = "parent-thread")
        val sessions = listOf(runtime("lead"), runtime("review", "lead"), runtime("check", "lead"), runtime("deploy", "lead", "needs_you"),
            codexSubagent, Session("machine", "plain", state = "working", status = "working"))
        assertEquals(listOf("deploy", "lead", "plain"), machineSessions(sessions, "machine").map { it.sessionId }.sorted())
        val list = buildAgentList(sessions.map(::AgentSession), listOf(Machine("machine", online = true)))
        val counted = machineSessions(sessions, "machine")
        val computer = list.machines.single()
        assertEquals(computer.sessionCount, counted.size)
        assertEquals(computer.stateCounts[AgentState.Working], counted.count { it.agentState == AgentState.Working })
        assertEquals(computer.stateCounts[AgentState.NeedsYou], counted.count { it.agentState == AgentState.NeedsYou })
    }

    @Test fun historyPreservesGapsAndPollingStopsWithThePage() = runTest {
        val points = listOf(MetricPoint(100, cpu = 20.0), MetricPoint(110, cpu = 30.0),
            MetricPoint(120, gap = true), MetricPoint(200, cpu = 95.0), MetricPoint(210), MetricPoint(220, cpu = 0.0))
        assertEquals(listOf(listOf(100L, 110L), listOf(200L), listOf(220L)), segments(points, 10) { it.cpu }.map { run -> run.map { it.t } })
        assertNull(selectedPoint(points, 180, 10))
        assertNull(selectedPoint(points, 120, 10))
        assertEquals(200L, selectedPoint(points, 200, 10)?.t)
        val sample = MetricSample(200, cpu = 95.0, memUsed = 80, memTotal = 100, load = 8.0)
        val metric = MachineMetrics("machine", true, latest = sample, points = points)
        assertNull(metric.current(231))
        assertNull(metric.copy(online = false).current(200))
        assertEquals(3, alerts(sample, 4).size)
        assertEquals(80.0, percent(sample.memUsed, sample.memTotal)!!, 0.0)
        assertNull(percent(1, 0))
        assertEquals(listOf("agent"), machineSessions(listOf(
            Session("machine", "assistant", assistant = true), Session("machine", "agent"),
            Session("other", "foreign"), Session("machine", "ended", status = "offline")), "machine").map { it.sessionId })

        val calls = mutableListOf<MetricRange>()
        val api = object : MachineMetricsApi {
            override suspend fun dashboard() = MetricsDashboard(listOf(metric))
            override suspend fun history(machineId: String, range: MetricRange): MetricsHistory {
                calls += range
                return MetricsHistory(metric, range.wire, range.step)
            }
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(emptyList<Machine>())
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() = Unit
        }
        val model = MachinesViewModel(api, sessions, "machine") { 200 }
        val job = backgroundScope.launch { model.poll() }
        runCurrent()
        assertEquals(listOf(MetricRange.Hour), calls)
        model.selectRange(MetricRange.Day)
        runCurrent()
        assertEquals(MetricRange.Day, calls.last())
        assertEquals("24h", model.state.value.history?.range)
        advanceTimeBy(MetricRange.Day.pollMillis)
        runCurrent()
        assertEquals(3, calls.size)
        job.cancelAndJoin()
        advanceTimeBy(MetricRange.Day.pollMillis * 2)
        runCurrent()
        assertEquals(3, calls.size)
    }
}
