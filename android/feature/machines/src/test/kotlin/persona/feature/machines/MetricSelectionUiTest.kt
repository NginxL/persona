package persona.feature.machines

import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.MachineMetrics
import persona.core.contracts.MachineSystem
import persona.core.contracts.MetricPoint
import persona.core.contracts.MetricRange
import persona.core.contracts.MetricSample
import persona.core.contracts.MetricsDashboard
import persona.core.contracts.MetricsHistory
import persona.core.contracts.Session
import persona.core.contracts.api.MachineMetricsApi
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MetricSelectionUiTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val initialNow = 1_793_600_000L
    private var now = initialNow
    private var historyPoints = points(initialNow)
    private val system = MachineSystem(os = "linux", name = "Linux", cores = 4, memory = 64L shl 30)
    private val machine = Machine("machine", name = "Workstation", online = true, system = system)
    private val api = object : MachineMetricsApi {
        override suspend fun dashboard() = MetricsDashboard(listOf(currentMetrics()), step = 10)
        override suspend fun history(machineId: String, range: MetricRange) =
            MetricsHistory(currentMetrics(), range.wire, range.step)
    }
    private val repository = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(machine))
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h5200dp-xhdpi")
    @Test
    fun sharedSelectionSurvivesRefreshRangeResizeAndDetailReturn() {
        val model = MachinesViewModel(api, repository, "machine") { now }
        runBlocking { model.load(MetricRange.Hour) }
        var width by mutableStateOf(400.dp)
        var showingDetail by mutableStateOf(true)
        rule.setContent {
            val holder = rememberSaveableStateHolder()
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                Box(Modifier.width(width).height(5200.dp).background(PersonaTheme.palette.background)) {
                    if (showingDetail) holder.SaveableStateProvider("machine-detail") {
                        MachinesScreen("machine", model, navigator)
                    } else Text("Elsewhere")
                }
            }
        }

        rule.onNodeWithTag("metric-plot-Cpu").performScrollTo().performTouchInput {
            down(center)
            advanceEventTime(600)
            moveBy(Offset(-60f, 0f))
            up()
        }
        rule.waitForIdle()
        val selected = selectionText(MetricKind.Cpu)
        assertNotEquals("Now", selected)
        assertSharedSelection(selected)
        rule.onNodeWithText("Load 5.6").assertExists()
        rule.onNodeWithText("1.4 per core").assertExists()

        now += 10
        historyPoints = historyPoints + MetricPoint(now, cpu = 45.0, mem = 30.0, rx = 1_200.0, tx = 2_200.0, load = 1.6, disk = 50.0)
        runBlocking { model.load(MetricRange.Hour) }
        rule.waitForIdle()
        assertSharedSelection(selected)

        rule.onNodeWithText("6h").performScrollTo().performClick()
        runBlocking { model.load(MetricRange.SixHours) }
        rule.waitForIdle()
        assertSharedSelection(selected)

        rule.runOnIdle { width = 700.dp }
        rule.waitForIdle()
        assertSharedSelection(selected)

        rule.runOnIdle { showingDetail = false }
        rule.onNodeWithText("Elsewhere").assertExists()
        rule.runOnIdle { showingDetail = true }
        rule.waitForIdle()
        assertSharedSelection(selected)

        rule.onNodeWithText("Back to now").performScrollTo().performClick()
        rule.waitForIdle()
        assertSharedSelection("Now")
        rule.onNodeWithText("Load 1.5").assertExists()
        rule.onNodeWithText("1.4 per core").assertDoesNotExist()
    }

    private fun assertSharedSelection(expected: String) {
        for (kind in listOf(MetricKind.Cpu, MetricKind.Memory, MetricKind.Rx, MetricKind.Load, MetricKind.Disk)) {
            assertEquals(kind.name, expected, selectionText(kind))
        }
    }

    private fun selectionText(kind: MetricKind): String =
        rule.onNodeWithTag("metric-selection-${kind.name}").fetchSemanticsNode().config[SemanticsProperties.Text]
            .joinToString(separator = "") { it.text }

    private fun currentMetrics() = MachineMetrics(
        "machine", true, system,
        MetricSample(now - 5, cpu = 42.0, memUsed = 30, memTotal = 64, netRx = 1_000.0, netTx = 2_000.0,
            load = 1.5, diskUsed = 50, diskTotal = 100),
        historyPoints,
    )

    private companion object {
        fun points(end: Long): List<MetricPoint> = (0..360).map { index ->
            MetricPoint(end - 3_600 + index * 10, cpu = 40.0, mem = 30.0, rx = 1_000.0, tx = 2_000.0,
                load = 5.6, disk = 50.0)
        }
    }
}
