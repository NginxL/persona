package persona.feature.assistant

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.supervisorScope
import persona.core.contracts.api.UsageDays
import persona.core.contracts.api.UsageHistoryApi
import persona.core.contracts.api.UsageHours

internal data class UsageCalendarState(
    val days: UsageDays? = null,
    val hours: UsageHours? = null,
    val loading: Boolean = false,
    val failure: Boolean = false,
)

/** Reads only while the caller's page is resumed; a failed refresh never erases the last good answer. */
internal class UsageCalendarModel(private val api: UsageHistoryApi) {
    private val mutable = MutableStateFlow(UsageCalendarState())
    val state = mutable.asStateFlow()
    private var revision = 0L

    suspend fun loadDays() = load(details = false)

    suspend fun loadDetails() = load(details = true)

    private suspend fun load(details: Boolean) {
        val read = ++revision
        mutable.update { it.copy(loading = true, failure = false) }
        try {
            val (days, hours) = if (details) supervisorScope {
                val daysRead = async { api.usageDays() }
                val hoursRead = async { api.usageHours() }
                daysRead.await() to hoursRead.await()
            } else api.usageDays() to mutable.value.hours
            currentCoroutineContext().ensureActive()
            val validDays = days.validDates()
            val validHours = hours?.validDates()
            if (read == revision) mutable.update {
                val retainedDays = if (validDays.days.isEmpty() && it.days?.days?.isNotEmpty() == true) it.days else validDays
                it.copy(days = retainedDays, hours = validHours, loading = false, failure = false)
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            if (read == revision) mutable.update { it.copy(loading = false, failure = true) }
        }
    }
}
