package persona.feature.settings

import android.content.res.Resources
import androidx.annotation.StringRes
import persona.core.contracts.api.DeviceSettings
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.HubManagers
import persona.core.contracts.api.ManagerCode
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.api.ManagersUnsupported
import persona.core.contracts.api.hubCodedFailure
import persona.core.contracts.parseTime
import persona.core.designsystem.Status
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import java.time.Duration
import java.time.Instant
import java.time.ZoneId

// Settings › Managers (docs/assistant/managers.md): the words of the list and a manager's page (the same as the web's), and the model
// both pages share. The pages are in ManagersScreen.kt.

/** What the roster says about a manager, in the words the web uses too. */
internal object ManagerText {
    /** The OS the manager said it runs on, by its usual name. */
    fun osName(os: String?): String? = when (val value = os?.trim()?.takeIf { it.isNotEmpty() }) {
        null -> null
        "darwin" -> "macOS"
        "linux" -> "Linux"
        "windows" -> "Windows"
        else -> value
    }

    /** How it runs, in words; the hub's own assistant is "the hub's own" whatever it said. */
    fun deploy(res: Resources, m: HubManager): String? {
        val value = m.deploy?.trim()?.takeIf { it.isNotEmpty() }
        if (m.builtin) return res.getString(R.string.settings_managers_deploy_hub)
        return when (value?.lowercase()) {
            null -> null
            "child" -> res.getString(R.string.settings_managers_deploy_hub)
            "service", "systemd", "launchd" -> res.getString(R.string.settings_managers_deploy_service)
            "docker", "container" -> res.getString(R.string.settings_managers_deploy_container)
            "external" -> res.getString(R.string.settings_managers_deploy_external)
            else -> value
        }
    }

    /** Where it runs: host · OS · how, leaving out what it did not say; null when it said nothing (still installing). */
    fun where(res: Resources, m: HubManager): String? =
        listOfNotNull(m.host?.trim()?.takeIf { it.isNotEmpty() }, osName(m.os), deploy(res, m)).joinToString(" · ").ifEmpty { null }

    /** The dot beside the status: green online, the waiting dot while installing, grey offline. */
    fun status(m: HubManager): Status = when {
        m.isInstalling -> Status.Waiting
        m.online -> Status.Unread
        else -> Status.Offline
    }

    /** Whole minutes left on an install code, rounded up; null once it ran out (or the time is unreadable). */
    fun minutesLeft(until: String?, now: Instant): Int? {
        val end = parseTime(until) ?: return null
        val left = Duration.between(now, end)
        if (left.isNegative || left.isZero) return null
        return ((left.seconds + 59) / 60).toInt().coerceAtLeast(1)
    }

    /** The list's status line: Online; Offline · Last synced at …; Installing · Code expires in n minutes / Code expired. */
    fun statusLine(res: Resources, m: HubManager, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String = when {
        m.isInstalling -> minutesLeft(m.installing, now)?.let { res.getQuantityString(R.plurals.settings_managers_installing, it, it) }
            ?: res.getString(R.string.settings_managers_installing_expired)
        m.online -> res.getString(R.string.settings_managers_online)
        else -> parseTime(m.lastSeen)?.let { res.getString(R.string.settings_managers_offline_since, agoText(res, it, now, zone)) }
            ?: res.getString(R.string.settings_managers_offline)
    }

    /** A manager's page: Connected at … (online) or Last synced at … (offline), as a label and its value; null without a time. */
    fun seen(res: Resources, m: HubManager, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): Pair<String, String>? = when {
        m.isInstalling -> null
        m.online -> parseTime(m.since)?.let { res.getString(R.string.settings_managers_row_since) to agoText(res, it, now, zone) }
        else -> parseTime(m.lastSeen)?.let { res.getString(R.string.settings_managers_row_last_seen) to agoText(res, it, now, zone) }
    }

    /** An install code's countdown, m:ss; null once it ran out. */
    fun countdown(until: String?, now: Instant): String? {
        val end = parseTime(until) ?: return null
        val left = Duration.between(now, end).seconds
        if (left <= 0) return null
        return String.format(java.util.Locale.ROOT, "%d:%02d", left / 60, left % 60)
    }

    /** A name the hub takes: 1 to 40 characters once trimmed. */
    fun validName(name: String): Boolean = persona.core.contracts.api.validManagerName(name)

    /** Why deleting is not offered, for the note under the button; null when it can be deleted. */
    @StringRes fun whyNoDelete(m: HubManager): Int? = when {
        m.builtin -> R.string.settings_managers_delete_builtin
        m.isDefault -> R.string.settings_managers_delete_default
        else -> null
    }

    /**
     * How a refused change reads: the roster's own codes and 400 / 403 in their words, else what did not happen
     * ([verb]) and the app's usual reason (the network, the hub's word).
     */
    fun failure(error: Throwable, @StringRes verb: Int = R.string.settings_src_channel_not_changed): ResText {
        val coded = hubCodedFailure(error)
        return when {
            coded?.code == "default_manager" -> ResText.of(R.string.settings_managers_err_default)
            coded?.code == "builtin_manager" -> ResText.of(R.string.settings_managers_err_builtin)
            coded?.code == "builtin_in_use" -> ResText.of(R.string.settings_managers_switcher_in_use)
            error is HideUnsupported -> ResText.of(R.string.settings_managers_switcher_unsupported)
            coded?.code == "no_manager" -> ResText.of(R.string.settings_managers_err_gone)
            coded?.code == "installing" -> ResText.of(R.string.settings_managers_err_installing)
            coded?.status == 400 -> ResText.of(R.string.settings_managers_err_name)
            coded?.status == 403 -> ResText.of(R.string.settings_managers_err_forbidden)
            else -> ResText.of(verb, failureReasonText(error))
        }
    }

    const val MaxName = persona.core.contracts.api.MANAGER_NAME_MAX
}

/** The hub took `{"hide": true}` and kept showing it: a hub from before the choice. */
internal class HideUnsupported : Exception("This hub cannot hide its own assistant")

/** An install code on screen, for [managerId] (named [name]). */
internal data class ManagerCodeSheet(
    val managerId: String, val name: String, val code: ManagerCode,
    /** Never installed (just made, or still installing): its code is all there is of it. */
    val fresh: Boolean = false,
)

internal data class ManagersState(
    val roster: ManagerRoster? = null,
    val loading: Boolean = false,
    /** The hub predates the roster (404). */
    val unsupported: Boolean = false,
    /** Reading the list went wrong. */
    val error: ResText? = null,
    /** A change is on its way (one at a time). */
    val busy: Boolean = false,
    /** Why the last change did not go. */
    val failure: ResText? = null,
    val code: ManagerCodeSheet? = null,
) {
    val managers: List<HubManager> get() = roster?.managers.orEmpty()
    fun manager(id: String): HubManager? = managers.firstOrNull { it.id == id }
}

/** Settings › Managers: the roster, and making, renaming, moving and deleting managers. One model per page. */
internal class ManagersModel(private val api: HubManagers) {
    private val mutable = MutableStateFlow(ManagersState())
    val state = mutable.asStateFlow()
    private var again = false

    /** Reads the list; asked again while reading (a push), it reads once more after. */
    suspend fun load() {
        if (state.value.loading) { again = true; return }
        mutable.update { it.copy(loading = true) }
        try {
            do {
                again = false
                try {
                    val roster = api.roster()
                    mutable.update { it.copy(roster = roster, error = null, unsupported = false) }
                } catch (cancelled: CancellationException) { throw cancelled }
                catch (_: ManagersUnsupported) { mutable.update { it.copy(unsupported = true, error = null) } }
                catch (error: Exception) { mutable.update { it.copy(error = ManagerText.failure(error, R.string.settings_src_read_failed), unsupported = false) } }
            } while (again)
        } finally { mutable.update { it.copy(loading = false) } }
    }

    /** New manager: the code sheet opens on success. */
    suspend fun create(name: String): Boolean {
        if (!ManagerText.validName(name)) { mutable.update { it.copy(failure = ResText.of(R.string.settings_managers_err_name)) }; return false }
        return change {
            val made = api.create(name.trim())
            val manager = made.manager
            mutable.update { it.copy(code = ManagerCodeSheet(manager?.id.orEmpty(), manager?.name ?: name.trim(), made, fresh = true)) }
            // The hub pushes the change too; reading now does not wait on it.
            load()
        }
    }

    /** Install elsewhere / New installation code: a new code, on the sheet. */
    suspend fun newCode(id: String): Boolean = change {
        val sheet = state.value.code?.takeIf { it.managerId == id }
        val name = state.value.manager(id)?.name ?: sheet?.name.orEmpty()
        // One never installed whose code ran out drops out of the hub's roster (404 no_manager): it is made again
        // under the same name, and the new one's code shows (its new id from then on). An installed one's 404 stays.
        val fresh = state.value.manager(id)?.isInstalling ?: (sheet?.fresh == true)
        val next = try { ManagerCodeSheet(id, name, api.newCode(id), fresh) }
        catch (failure: Exception) {
            if (!fresh || name.isBlank() || hubCodedFailure(failure)?.let { it.status == 404 && it.code == "no_manager" } != true) throw failure
            val made = api.create(name)
            ManagerCodeSheet(made.manager?.id.orEmpty(), made.manager?.name ?: name, made, fresh = true)
        }
        mutable.update { it.copy(code = next) }
        load()
    }

    suspend fun rename(id: String, name: String): Boolean {
        if (!ManagerText.validName(name)) { mutable.update { it.copy(failure = ResText.of(R.string.settings_managers_err_name)) }; return false }
        return change { replace(api.rename(id, name.trim())) }
    }

    suspend fun makeDefault(id: String): Boolean = change {
        api.makeDefault(id)
        // Every manager's Default changes: the whole list again.
        load()
    }

    /**
     * Show in the switcher (the hub's own assistant): off sends `{"hide": true}`, on `{"hide": false}`. The switch moves
     * at once and goes back if the hub refuses (409 `builtin_in_use` while it is the default or has a computer); then
     * the list is read again for what is in effect (`hidden`), and a hub that kept no choice says it cannot.
     */
    suspend fun showInSwitcher(id: String, show: Boolean): Boolean {
        val before = state.value.manager(id) ?: return false
        if (state.value.busy || before.hide == !show) return false
        markHide(id, !show)
        val done = change {
            api.setHide(id, !show)
            // Taken: a read that fails now leaves the switch where it is (the hub's push reads the list again).
            val roster = try { api.roster() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { null }
            if (roster != null) {
                mutable.update { it.copy(roster = roster, error = null, unsupported = false) }
                if (!show && roster.manager(id)?.hide != true) throw HideUnsupported()
            }
        }
        if (!done) markHide(id, before.hide)
        return done
    }

    suspend fun delete(id: String): Boolean = change {
        api.delete(id)
        mutable.update { s -> s.copy(roster = s.roster?.let { r -> r.copy(managers = r.managers.filterNot { it.id == id }) }) }
    }

    fun closeCode() = mutable.update { it.copy(code = null) }

    fun clearFailure() = mutable.update { it.copy(failure = null) }

    private fun markHide(id: String, hide: Boolean) = mutable.update { s ->
        s.copy(roster = s.roster?.let { r -> r.copy(managers = r.managers.map { if (it.id == id) it.copy(hide = hide) else it }) })
    }

    private fun replace(manager: HubManager) = mutable.update { s ->
        s.copy(roster = s.roster?.let { r -> r.copy(managers = r.managers.map { if (it.id == manager.id) manager.copy(isDefault = manager.isDefault || it.isDefault) else it }) })
    }

    private suspend fun change(block: suspend () -> Unit): Boolean {
        if (state.value.busy) return false
        mutable.update { it.copy(busy = true, failure = null) }
        return try { block(); true }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(failure = ManagerText.failure(error)) }; false }
        finally { mutable.update { it.copy(busy = false) } }
    }
}

/**
 * 设置 › 管理者 › one › 推送通知: whether this device gets a manager's pushes (the hub's `muted_managers` of this
 * device's prefs). [muted] null: not read yet, or a hub without the list (no switch then).
 */
internal data class ManagerPushState(
    val muted: Set<String>? = null,
    /** A change is on its way. */
    val busy: Boolean = false,
    /** Why the last change did not go (the switch is back where it was). */
    val failure: ResText? = null,
) {
    fun pushes(id: String): Boolean? = muted?.let { id !in it }
}

/** The 推送通知 switch: read from the device's prefs, written as `{"muted_managers": [...]}` alone, shown at once. */
internal class ManagerPushModel(private val devices: DeviceSettings) {
    private val mutable = MutableStateFlow(ManagerPushState())
    val state = mutable.asStateFlow()

    suspend fun load() {
        try {
            val muted = devices.preferences().mutedManagers
            mutable.update { it.copy(muted = muted?.toSet()) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* No switch until the prefs read: the rest of the page does not depend on it. */ }
    }

    /** Turns [id]'s pushes [on] or off: the switch moves now and goes back if the hub refuses. */
    suspend fun set(id: String, on: Boolean): Boolean {
        val before = state.value.muted ?: return false
        if (state.value.busy || (id !in before) == on) return false
        val wanted = if (on) before - id else before + id
        mutable.update { it.copy(muted = wanted, busy = true, failure = null) }
        return try {
            val saved = devices.saveMutedManagers(wanted.toList())
            mutable.update { it.copy(muted = saved.toSet()) }
            true
        } catch (cancelled: CancellationException) { mutable.update { it.copy(muted = before) }; throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(muted = before, failure = ManagerText.failure(error, R.string.settings_managers_push_failed)) }
            false
        } finally { mutable.update { it.copy(busy = false) } }
    }
}
