@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts

import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@KeepGeneratedSerializer
@Serializable(with = MetricSampleSerializer::class)
data class MetricSample(
    val t: Long,
    val cpu: Double? = null,
    val cores: List<Double>? = null,
    val load: Double? = null,
    @SerialName("mem_used") val memUsed: Long? = null,
    @SerialName("mem_total") val memTotal: Long? = null,
    @SerialName("swap_used") val swapUsed: Long? = null,
    @SerialName("swap_total") val swapTotal: Long? = null,
    @SerialName("net_rx") val netRx: Double? = null,
    @SerialName("net_tx") val netTx: Double? = null,
    @SerialName("disk_used") val diskUsed: Long? = null,
    @SerialName("disk_total") val diskTotal: Long? = null,
    @SerialName("battery_percent") val batteryPercent: Double? = null,
    @SerialName("battery_charging") val batteryCharging: Boolean? = null,
    @SerialName("temp_c") val tempC: Double? = null,
)

/** History memory, disk and swap values are bytes, not percentages. */
@KeepGeneratedSerializer
@Serializable(with = MetricPointSerializer::class)
data class MetricPoint(
    val t: Long,
    val gap: Boolean = false,
    val cpu: Double? = null,
    val load: Double? = null,
    val mem: Double? = null,
    val swap: Double? = null,
    val rx: Double? = null,
    val tx: Double? = null,
    val disk: Double? = null,
    @SerialName("battery_percent") val batteryPercent: Double? = null,
    @SerialName("temp_c") val tempC: Double? = null,
)

@KeepGeneratedSerializer
@Serializable(with = MachineMetricsSerializer::class)
data class MachineMetrics(
    @SerialName("machine_id") val machineId: String,
    val online: Boolean = false,
    val system: MachineSystem? = null,
    val latest: MetricSample? = null,
    val points: List<MetricPoint> = emptyList(),
)

@KeepGeneratedSerializer
@Serializable(with = MetricsDashboardSerializer::class)
data class MetricsDashboard(val machines: List<MachineMetrics> = emptyList(), val step: Int = 60)

@KeepGeneratedSerializer
@Serializable(with = MetricsHistorySerializer::class)
data class MetricsHistory(val metrics: MachineMetrics, val range: String = "1h", val step: Int = 10)

enum class MetricRange(val wire: String, val seconds: Long, val step: Int, val pollMillis: Long) {
    Hour("1h", 3600, 10, 10_000),
    SixHours("6h", 21600, 60, 60_000),
    Day("24h", 86400, 240, 60_000),
}

object MetricSampleSerializer : WireSerializer<MetricSample>(MetricSample.generatedSerializer())
object MetricPointSerializer : WireSerializer<MetricPoint>(MetricPoint.generatedSerializer())
object MachineMetricsSerializer : WireSerializer<MachineMetrics>(MachineMetrics.generatedSerializer())
object MetricsDashboardSerializer : WireSerializer<MetricsDashboard>(MetricsDashboard.generatedSerializer())
object MetricsHistorySerializer : WireSerializer<MetricsHistory>(MetricsHistory.generatedSerializer())
