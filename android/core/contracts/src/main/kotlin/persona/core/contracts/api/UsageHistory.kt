package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** The four token kinds the hub reports for a day, an hour, or a computer. */
interface UsageTokenValues {
    val input: Long
    val cacheWrite: Long
    val cacheRead: Long
    val output: Long

    /** Every token kind, cache reads included; request counts are not tokens. */
    val total: Long get() = input + cacheWrite + cacheRead + output
}

@Serializable
data class UsageTokens(
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageDay(
    val date: String = "",
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageDays(
    val zone: String = "",
    val days: List<UsageDay> = emptyList(),
    val stored: Boolean = false,
)

@Serializable
data class UsageHour(
    val t: Long = 0,
    val date: String = "",
    val hour: Int = 0,
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageMachineShare(
    val id: String = "",
    val name: String = "",
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageHours(
    val zone: String = "",
    val hours: List<UsageHour> = emptyList(),
    val machines: List<UsageMachineShare> = emptyList(),
    val total: UsageTokens = UsageTokens(),
    val stored: Boolean = false,
)

/** Token history for the selected assistant's manager. */
interface UsageHistoryApi {
    /** GET /api/v1/usage/days: at most 371 complete daily buckets, oldest first and today last. */
    suspend fun usageDays(days: Int = 371): UsageDays

    /** GET /api/v1/usage/hours: one to seven days by the clock, including repeated clock-change hours. */
    suspend fun usageHours(days: Int = 7): UsageHours
}
