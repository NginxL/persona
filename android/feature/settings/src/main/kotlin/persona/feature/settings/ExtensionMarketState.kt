package persona.feature.settings

import android.content.res.Resources
import persona.core.contracts.api.CurrentAssistant
import persona.core.contracts.api.CatalogExtension
import persona.core.contracts.api.ExtensionCatalog
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionHasDependents
import persona.core.contracts.api.ExtensionFailedMember
import persona.core.contracts.api.ExtensionPartialUninstall
import persona.core.contracts.api.ExtensionRemoval
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionMarket
import persona.core.contracts.api.ExtensionMarkets
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.ExtensionOperation
import persona.core.contracts.api.ExtensionPreview
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.onStart
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.withTimeoutOrNull
import java.time.Instant

// Settings › Extensions › Extension market (docs/extensions/extensions.md "Extension market"): the words, the catalog, one extension of it (preview, then install,
// upgrade or remove) and the markets. The pages are in ExtensionMarketScreen.kt.

/** Where a catalog extension stands on this hub. */
internal enum class CatalogStanding { Available, Installed, Upgradable, InstalledElsewhere, Unavailable }

/** What the page may do with a catalog extension. */
internal enum class CatalogAction { Install, Upgrade, Uninstall }

/**
 * A GitHub market read without a token: GitHub allows 60 reads an hour per address, shared by every device on the
 * same network, so offering it a read-only token is what gets the market read again (docs/extensions/extensions.md "Extension market").
 */
internal fun wantsGitHubToken(market: ExtensionMarket): Boolean = !market.hasToken && isGitHubMarket(market.spec)

/** A market read through GitHub's API (github:owner/repo), the kind a GitHub token is for. */
internal fun isGitHubMarket(spec: String): Boolean = spec.trim().startsWith("github:", ignoreCase = true)

/**
 * GitHub's page for a new fine-grained token, filled in for a market (docs/extensions/extensions.md "GitHub rate limits"): a name, what it
 * is for, a year. No permission: such a token always reads public repositories, which is what lifts the hourly limit.
 */
internal const val GitHubTokenUrl = "https://github.com/settings/personal-access-tokens/new" +
    "?name=Persona%20hub%20extension%20markets" +
    "&description=Lets%20a%20Persona%20hub%20read%20its%20GitHub%20extension%20markets%20%28read-only%29." +
    "&expires_in=365"

/** A successful uninstall with cleanup left for the user, held until its OK. */
internal data class ExtensionRemovalNotice(val title: String, val warnings: List<String>, val operation: String? = null)

/** Enough terminal operations to order delayed packets without retaining the hub's whole history in the page. */
private const val REMOVAL_HISTORY_LIMIT = 32

/** Only actionable hub warnings open the cleanup sheet; null, empty and blank results keep the ordinary success path. */
internal fun extensionRemovalNotice(
    res: Resources,
    name: String,
    members: Boolean,
    removal: ExtensionRemoval?,
    stepped: Boolean = false,
    operation: String? = null,
): ExtensionRemovalNotice? {
    val warnings = removal?.warnings.orEmpty().filter { it.isNotBlank() }
    if (warnings.isEmpty()) return null
    return ExtensionRemovalNotice(ExtensionText.uninstalled(res, name, members, removal, stepped), warnings, operation)
}

internal object CatalogText {
    fun standing(p: CatalogExtension): CatalogStanding = when {
        p.upgradable && p.problem.isNullOrBlank() -> CatalogStanding.Upgradable
        !p.installed.isNullOrBlank() && (p.fromThisMarket || p.installedSource == "installed") -> CatalogStanding.Installed
        !p.installed.isNullOrBlank() -> CatalogStanding.InstalledElsewhere
        !p.problem.isNullOrBlank() -> CatalogStanding.Unavailable
        else -> CatalogStanding.Available
    }

    /** The short word beside an extension in the catalog. */
    fun badge(res: Resources, p: CatalogExtension): String? = when (standing(p)) {
        CatalogStanding.Upgradable -> res.getString(R.string.settings_ext_badge_upgradable)
        CatalogStanding.Installed, CatalogStanding.InstalledElsewhere -> res.getString(R.string.settings_ext_source_installed)
        CatalogStanding.Unavailable -> res.getString(R.string.settings_ext_badge_unavailable)
        CatalogStanding.Available -> null
    }

    /** The line under it: the version (and the installed one), or why it cannot be installed. */
    fun line(res: Resources, p: CatalogExtension): String = when (standing(p)) {
        CatalogStanding.Upgradable -> "${p.installed} → ${p.version}"
        CatalogStanding.Installed -> res.getString(R.string.settings_ext_line_installed, p.version)
        CatalogStanding.InstalledElsewhere -> res.getString(R.string.settings_ext_line_installed_elsewhere, p.version, p.installed.orEmpty(),
            ExtensionText.source(res, p.installedSource) ?: res.getString(R.string.settings_ext_line_elsewhere))
        CatalogStanding.Unavailable -> p.problem.orEmpty()
        CatalogStanding.Available -> p.version
    }

    /**
     * Install what is not installed and can be; upgrade what is older here (a built-in or hub.json extension the hub
     * marks not upgradable); remove only what came from this market (the hub refuses the rest from a phone).
     */
    fun actions(p: CatalogExtension): List<CatalogAction> = buildList {
        val usable = p.problem.isNullOrBlank()
        if (usable && p.installed.isNullOrBlank()) add(CatalogAction.Install)
        if (usable && p.upgradable) add(CatalogAction.Upgrade)
        if (!p.installed.isNullOrBlank() && p.fromThisMarket) add(CatalogAction.Uninstall)
    }

    /** Why the page offers nothing: installed some other way (only the hub's machine changes it). */
    fun noActionReason(res: Resources, p: CatalogExtension): String? = when {
        actions(p).isNotEmpty() -> null
        !p.problem.isNullOrBlank() -> p.problem
        !p.installed.isNullOrBlank() && !p.fromThisMarket ->
            res.getString(R.string.settings_ext_not_from_market)
        else -> null
    }

    /** The confirm button of a preview: red when an upgrade asks for more. */
    fun confirmLabel(res: Resources, preview: ExtensionPreview): String = when {
        !preview.upgrade -> res.getString(R.string.settings_ext_confirm_install)
        preview.morePermissions || !preview.addedPermissions.isNullOrEmpty() -> res.getString(R.string.settings_ext_confirm_upgrade_more)
        else -> res.getString(R.string.settings_ext_upgrade)
    }

    fun asksForMore(preview: ExtensionPreview): Boolean = preview.upgrade && (preview.morePermissions || !preview.addedPermissions.isNullOrEmpty())

    /** What the page says once it is in. */
    fun installed(res: Resources, detail: ExtensionDetail, upgrade: Boolean): String {
        val missing = detail.extension.needsSetup.orEmpty()
        val done = if (upgrade) res.getString(R.string.settings_ext_upgraded_to, detail.extension.version.orEmpty()).trim() else res.getString(R.string.settings_ext_source_installed)
        return if (missing.isEmpty()) done else res.getString(R.string.settings_ext_installed_missing, done, missing.joinToString(res.getString(R.string.settings_ext_list_separator)))
    }

    /** A market's line in the list: how many extensions, signed or not, a token, when it was read. */
    fun marketLine(res: Resources, m: ExtensionMarket): String = listOfNotNull(
        res.getQuantityString(R.plurals.settings_ext_market_count, m.extensions, m.extensions),
        m.signature?.let { if (it.verified) res.getString(R.string.settings_ext_market_signed) else if (it.unsigned) res.getString(R.string.settings_ext_market_unsigned) else null },
        res.getString(R.string.settings_ext_market_token).takeIf { m.hasToken },
        m.fetchedAt?.let(::time)?.let { res.getString(R.string.settings_ext_market_read_at, it) },
    ).joinToString(res.getString(R.string.settings_ext_dot_separator))

    /** A market whose signature fails or whose key changed: what the page says under the list (null when it checks out). */
    fun signatureProblem(res: Resources, m: ExtensionMarket): String? = m.signature?.takeIf { it.failed }?.let { s ->
        s.detail?.takeIf { it.isNotBlank() } ?: res.getString(R.string.settings_ext_market_signature_failed)
    }

    /** `2026-11-04T06:00:00Z` → `2026-11-04 06:00` (UTC as the hub wrote it; enough to tell old from fresh). */
    fun time(iso: String): String? = Regex("^(\\d{4}-\\d{2}-\\d{2})T(\\d{2}:\\d{2})").find(iso)?.let { "${it.groupValues[1]} ${it.groupValues[2]}" }
}

internal data class CatalogState(
    val catalog: ExtensionCatalog? = null,
    val query: String = "",
    /** The market the list is narrowed to; null is all. */
    val market: String? = null,
    val loading: Boolean = false,
    val error: String? = null,
    /** The market a token is on its way for. */
    val tokenWorking: String? = null,
    /** Why the last token change did not happen. */
    val tokenError: String? = null,
) {
    val markets: List<ExtensionMarket> get() = catalog?.markets.orEmpty()
    val extensions: List<CatalogExtension> get() = catalog?.extensions.orEmpty()
}

/**
 * The official market reads "Official extensions" in the app's language (#5017), whatever its index (or an older hub) calls it; its
 * catalog extensions say so as their market too. Other markets keep the names their indexes give.
 */
internal object OfficialMarket {
    fun named(res: Resources, m: ExtensionMarket): ExtensionMarket =
        if (m.official) m.copy(name = res.getString(R.string.settings_ext_official_market_name)) else m

    fun named(res: Resources, c: ExtensionCatalog): ExtensionCatalog {
        val official = c.markets.filter { it.official }.mapTo(HashSet()) { it.id }
        if (official.isEmpty()) return c
        val name = res.getString(R.string.settings_ext_official_market_name)
        return c.copy(markets = c.markets.map { named(res, it) },
            extensions = c.extensions.map { if (it.market in official) it.copy(marketName = name) else it })
    }
}

/** "Extension market": the catalog, searched and narrowed to a market by the hub. */
internal class CatalogModel(private val api: ExtensionMarkets, private val res: Resources) {
    private val mutable = MutableStateFlow(CatalogState())
    val state = mutable.asStateFlow()
    private var generation = 0

    fun setQuery(query: String) = mutable.update { it.copy(query = query) }

    fun setMarket(market: String?) = mutable.update { it.copy(market = market) }

    fun clearTokenError() = mutable.update { it.copy(tokenError = null) }

    /** A new token, or "" to remove it; the hub reads the market again with it (a rate-limited GitHub market too). */
    suspend fun setToken(id: String, token: String): Boolean {
        if (state.value.tokenWorking != null) return false
        mutable.update { it.copy(tokenWorking = id, tokenError = null) }
        return try {
            api.setMarketToken(id, token.trim())
            load()
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(tokenError = res.getString(R.string.settings_ext_market_token_not_changed, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(tokenWorking = null) } }
    }

    /** Reads the catalog for the query and market as they are; an older answer that comes in late is dropped. */
    suspend fun load(refresh: Boolean = false) {
        val mine = ++generation
        val (query, market) = state.value.let { it.query to it.market }
        mutable.update { it.copy(loading = true) }
        try {
            val catalog = OfficialMarket.named(res, api.catalog(query, market, refresh))
            if (mine == generation) mutable.update {
                // A market that went away is not a filter any more.
                val keep = it.market?.takeIf { id -> catalog.markets.any { m -> m.id == id } }
                it.copy(catalog = catalog, error = null, market = keep)
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { if (mine == generation) mutable.update { it.copy(error = res.getString(R.string.settings_ext_load_failed, failureReasonText(error).resolve(res))) } }
        finally { if (mine == generation) mutable.update { it.copy(loading = false) } }
    }
}

internal data class CatalogExtensionState(
    val extension: CatalogExtension? = null,
    val marketName: String? = null,
    val loading: Boolean = false,
    val error: String? = null,
    /** The preview waiting for the user's yes: what it is and may do. */
    val preview: ExtensionPreview? = null,
    /** Downloading and checking the package. */
    val previewing: Boolean = false,
    /** Installing, upgrading or removing. */
    val acting: Boolean = false,
    /** Why the last step did not happen. */
    val actionError: String? = null,
    /** It is gone from the catalog (the market dropped it, or the market was removed). */
    val missing: Boolean = false,
    /**
     * Its install, upgrade or removal in steps (a hub with `extension_operations`): going on, or stopped at a step; one
     * that ended well is gone again (the page says so once).
     */
    val operation: ExtensionOperation? = null,
    /** The operation whose preview the sheet shows: agreeing goes on with it, closing the sheet cancels it. */
    val consentFor: String? = null,
    /** The removal was refused for its enabled dependents (`has_dependents`, #5611): asked before going on with `cascade`. */
    val cascade: ExtensionCascade? = null,
    /** The pack's members were partly uninstalled (#1154): said in a dialog. */
    val partial: PackPartial? = null,
    /** Cleanup left after a successful uninstall: shown until the user acknowledges it, with no success toast. */
    val removalNotice: ExtensionRemovalNotice? = null,
) {
    val busy: Boolean get() = loading || previewing || acting || operation?.active == true
}

/**
 * One extension of a market: its permissions, preview → install / upgrade, remove. With a hub that has
 * `extension_operations` each runs in steps the page shows as they go ([watch]); with an older one the calls wait.
 * [now] is the clock a preview's expiry is read against.
 */
internal class CatalogExtensionModel(
    private val api: ExtensionMarkets, private val market: String, private val id: String, private val res: Resources,
    private val now: () -> Instant = Instant::now,
    manager: String? = null,
) {
    /** The owner captured when the page opens, even if the roster's default changes later. */
    val owner = ExtensionOwners.of(manager)
    private val mutable = MutableStateFlow(CatalogExtensionState())
    val state = mutable.asStateFlow()
    private var generation = 0
    /** Says an operation ended well (set by [watch]). */
    private var onDone: (String) -> Unit = {}
    /** Opens the installed extension's sign-in (an operation that succeeded with `next: auth`, #4507). */
    private var onSignIn: (String) -> Unit = {}
    /** The removal in steps this page started and its options, until it ends (kept when refused, for Retry). */
    private var removing: Removal? = null
    private data class Removal(val operation: String, val keepData: Boolean, val members: Boolean)
    /** Before its HTTP answer names an operation, matching pushes wait here instead of being mistaken for history. */
    private var startingRemoval = false
    private val earlyRemovalEvents = mutableMapOf<String, ExtensionOperation>()
    /** Warnings and terminal outcomes are cumulative: a late packet can add warnings but cannot revive an old operation. */
    private val removalWarnings = mutableMapOf<String, List<String>>()
    private val acknowledgedRemovalWarnings = mutableMapOf<String, List<String>>()
    private val removalNoticeQueue = ArrayDeque<ExtensionRemovalNotice>()
    private val finishedRemovals = linkedMapOf<String, FinishedRemoval>()
    private data class FinishedRemoval(
        val operation: ExtensionOperation,
        val keepData: Boolean,
        val members: Boolean,
        val notify: Boolean,
    )
    /** What the last uninstall that went through at once removed (its members, #1154). */
    var removal: ExtensionRemoval? = null
        private set

    /** The hub runs installs in steps; asked when something starts (a hub without the flag: no). */
    private suspend fun stepped(): Boolean = try {
        withTimeoutOrNull(1_000) { api.operationsAvailable.first() } ?: false
    } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { false }

    /**
     * While the page is open: follows this extension's operations as the hub pushes them, and lists them again after
     * each hello (opening, a reconnect), since what was missed is not replayed. [done] is what to tell the user.
     */
    suspend fun watch(signIn: (String) -> Unit = {}, done: (String) -> Unit) {
        onDone = done
        onSignIn = signIn
        api.operationUpdates.onStart { emit(null) }.collect { op -> if (op == null) relist() else track(op) }
    }

    private suspend fun relist() {
        if (!stepped()) return
        val list = try { api.operations() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { return }
        // The manager keeps operations only in memory: one it no longer lists was lost with a restart of it (#1832), and
        // no push will end it. The page lets go of it, so Install / Upgrade can start again.
        state.value.operation?.let { held -> if (list.none { it.id == held.id }) gone(held) }
        val mine = list.filter { mine(it, requestOwned = true) }
        val latest = mine.maxByOrNull { OperationText.instant(it.startedAt) }
        // Every listed terminal operation orders later packets, not just the one the page displays. Historical outcomes
        // are tombstones only: reconnecting must not replay a toast or a cleanup sheet.
        mine.filter { it.id != latest?.id && it.kind == "uninstall" && removalEnded(it) }.forEach { incoming ->
            val (op, _) = rememberRemovalWarnings(incoming)
            recordFinishedRemoval(op, keepData = true, members = false, notify = false)
        }
        latest?.let { track(it, requestOwned = true) }
    }

    /** An operation the manager does not know (any more): nothing to wait for, cancel or retry. */
    private fun gone(op: ExtensionOperation) {
        if (removing?.operation == op.id) removing = null
        mutable.update {
            if (it.operation?.id != op.id) it
            else it.copy(operation = null, consentFor = if (it.consentFor == op.id) null else it.consentFor,
                preview = if (it.consentFor == op.id) null else it.preview)
        }
    }

    /** HTTP receipts are scoped by the request; unlike broadcasts, older receipts omit manager metadata. */
    private fun mine(op: ExtensionOperation, requestOwned: Boolean = false) = op.extension == id && (op.market.isNullOrEmpty() || op.market == market) &&
        (requestOwned && op.manager.isNullOrBlank() || ExtensionOwners.of(op.manager) == owner)

    /** Accumulates cleanup lines independently of operation state: hubs can send them before or after the success packet. */
    private fun rememberRemovalWarnings(op: ExtensionOperation): Pair<ExtensionOperation, Boolean> {
        if (op.kind != "uninstall") return op to false
        val previous = removalWarnings[op.id].orEmpty()
        val merged = (previous + op.warnings.orEmpty().filter { it.isNotBlank() }).distinct()
        if (merged.isNotEmpty()) removalWarnings[op.id] = merged
        val warnings = if (merged.isEmpty()) op.warnings else merged
        return op.copy(warnings = warnings) to (merged.size > previous.size)
    }

    private fun removalEnded(op: ExtensionOperation): Boolean =
        op.state == ExtensionOperation.Succeeded || op.state == ExtensionOperation.Failed || op.state == ExtensionOperation.Canceled

    private fun recordFinishedRemoval(op: ExtensionOperation, keepData: Boolean, members: Boolean, notify: Boolean) {
        finishedRemovals.remove(op.id)
        finishedRemovals[op.id] = FinishedRemoval(op, keepData, members, notify)
        while (finishedRemovals.size > REMOVAL_HISTORY_LIMIT) finishedRemovals.remove(finishedRemovals.keys.first())
    }

    /** While the start request is in flight, only this extension's removal packets are held for its response's operation id. */
    private fun cacheEarlyRemoval(op: ExtensionOperation) {
        val previous = earlyRemovalEvents[op.id]
        val previousEnded = previous?.let(::removalEnded) == true
        val incomingEnded = removalEnded(op)
        earlyRemovalEvents[op.id] = when {
            previous == null -> op
            // Terminal states win over every running snapshot; only a newer packet of the same terminal state replaces one.
            previousEnded && (!incomingEnded || previous.state != op.state || !OperationText.newer(op, previous)) -> previous.copy(warnings = op.warnings)
            previousEnded || incomingEnded || OperationText.newer(op, previous) -> op
            else -> previous.copy(warnings = op.warnings)
        }
    }

    /** A terminal operation stays terminal. Only new cleanup lines change its notice; duplicates and old packets change nothing. */
    private fun updateFinishedRemoval(finished: FinishedRemoval, op: ExtensionOperation, hasNewWarnings: Boolean) {
        val previous = finished.operation
        val newer = OperationText.newer(op, previous)
        val updated = if (newer && op.state == previous.state) op.copy(warnings = removalWarnings[op.id].orEmpty())
        else previous.copy(warnings = removalWarnings[op.id].orEmpty())
        recordFinishedRemoval(updated, finished.keepData, finished.members, finished.notify)
        if (previous.state == ExtensionOperation.Failed && op.state == ExtensionOperation.Failed && newer && state.value.operation?.id == op.id) {
            mutable.update { it.copy(operation = updated) }
        }
        if (previous.state == ExtensionOperation.Succeeded && hasNewWarnings && finished.notify) queueRemovalNotice(updated, finished.members)
    }

    /** Queues each operation's unacknowledged cleanup lines; only OK consumes them, never their arrival or another sheet. */
    private fun queueRemovalNotice(op: ExtensionOperation, members: Boolean) {
        val acknowledged = acknowledgedRemovalWarnings[op.id].orEmpty()
        val pending = removalWarnings[op.id].orEmpty().filter { it !in acknowledged }
        if (pending.isEmpty()) return
        val name = state.value.extension?.label ?: op.name ?: op.extension
        removal = ExtensionRemoval(warnings = pending)
        val notice = extensionRemovalNotice(res, name, members, removal, stepped = true, operation = op.id) ?: return
        if (state.value.removalNotice?.operation == op.id) {
            mutable.update { it.copy(removalNotice = notice) }
            return
        }
        val queued = removalNoticeQueue.indexOfFirst { it.operation == op.id }
        if (queued >= 0) removalNoticeQueue[queued] = notice else removalNoticeQueue.addLast(notice)
        presentNextRemovalNotice()
    }

    private fun presentNextRemovalNotice() {
        if (state.value.removalNotice != null || state.value.operation?.active == true || removalNoticeQueue.isEmpty()) return
        val next = removalNoticeQueue.removeFirst()
        removal = ExtensionRemoval(warnings = next.warnings)
        mutable.update { it.copy(removalNotice = next) }
    }

    /** Keeps op when it is this extension's and newer than what the page has; what it waits for or how it ended follows. */
    private suspend fun track(incoming: ExtensionOperation, requestOwned: Boolean = false, startedHere: Boolean = false) {
        if (!mine(incoming, requestOwned)) return
        val (op, hasNewWarnings) = rememberRemovalWarnings(incoming)
        if (op.kind == "uninstall" && startingRemoval) {
            cacheEarlyRemoval(op)
            return
        }
        if (op.kind == "uninstall") {
            finishedRemovals[op.id]?.let { finished ->
                updateFinishedRemoval(finished, op, hasNewWarnings)
                return
            }
        }
        // While this page's removal is unresolved, an older or another client's operation cannot replace it.
        removing?.let { if (it.operation != op.id) return }
        val was = state.value.operation
        if (was != null && was.id == op.id && !OperationText.newer(op, was)) return
        // A historical success from relisting is not news; this start's own response and pushes still are.
        if (was?.id != op.id && !op.active && !op.failed && !startedHere && removing?.operation != op.id) {
            if (op.kind == "uninstall" && removalEnded(op)) recordFinishedRemoval(op, keepData = true, members = false, notify = false)
            return
        }
        val refused = op.failed && op.kind == "uninstall" && op.errorCode == "has_dependents"
        val asked = removing?.takeIf { it.operation == op.id }
        if (!op.active && !refused && asked != null) removing = null
        when (op.state) {
            ExtensionOperation.Succeeded -> {
                val name = state.value.extension?.label ?: op.name ?: op.extension
                val members = asked?.members == true
                if (op.kind == "uninstall") {
                    removal = ExtensionRemoval(warnings = op.warnings)
                    recordFinishedRemoval(op, asked?.keepData ?: true, members, notify = true)
                    queueRemovalNotice(op, members)
                }
                // A pack's removal with its members ends only once every member is gone (#1154).
                val message = if (op.kind == "uninstall" && members)
                    ExtensionText.uninstalled(res, name, members = true, stepped = true) else OperationText.done(res, op, name)
                mutable.update {
                    it.copy(operation = null, consentFor = null, preview = if (it.consentFor == op.id) null else it.preview)
                }
                presentNextRemovalNotice()
                val acknowledged = acknowledgedRemovalWarnings[op.id].orEmpty()
                val hasCleanup = op.kind == "uninstall" && removalWarnings[op.id].orEmpty().any { it !in acknowledged }
                // Cleanup warnings replace the success toast; each sheet waits for its own acknowledgement.
                if (op.kind != "uninstall" || !hasCleanup) onDone(message)
                // Installed but not signed in: straight on to its sign-in.
                if (op.next == "auth" && op.kind != "uninstall") onSignIn(op.extension.ifBlank { id })
                load()
            }
            ExtensionOperation.Canceled -> {
                if (op.kind == "uninstall") recordFinishedRemoval(op, asked?.keepData ?: true, asked?.members == true, notify = true)
                mutable.update { it.copy(operation = null, consentFor = null, preview = if (it.consentFor == op.id) null else it.preview) }
                presentNextRemovalNotice()
            }
            else -> {
                if (op.kind == "uninstall" && op.failed) recordFinishedRemoval(op, asked?.keepData ?: true, asked?.members == true, notify = true)
                mutable.update { it.copy(operation = op) }
                presentNextRemovalNotice()
                if (op.state == ExtensionOperation.NeedsConsent && state.value.consentFor != op.id) askConsent(op)
                // The operation carries only the code, not who depends on it.
                if (refused && asked != null) mutable.update { it.copy(cascade = ExtensionCascade(emptyList(), asked.keepData, asked.members)) }
            }
        }
    }

    /** The package is checked: its preview, for the user's yes. */
    private suspend fun askConsent(op: ExtensionOperation) {
        mutable.update { it.copy(consentFor = op.id) }
        try {
            val preview = op.preview ?: api.operation(op.id).preview ?: return
            if (state.value.consentFor == op.id) mutable.update { it.copy(preview = preview) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(consentFor = null, actionError = res.getString(R.string.settings_ext_no_package, failureReasonText(error).resolve(res))) }
        }
    }

    /** Shows the permissions an operation waits for again (the sheet could not be opened). */
    suspend fun review() {
        state.value.operation?.takeIf { it.state == ExtensionOperation.NeedsConsent }?.let { askConsent(it) }
    }

    /** A first removal and its Retry share the same in-flight pairing: early pushes join the response's new operation id. */
    private suspend fun startSteppedRemoval(
        keepData: Boolean,
        members: Boolean,
        start: suspend () -> ExtensionOperation,
    ): ExtensionOperation {
        removing = null
        startingRemoval = true
        earlyRemovalEvents.clear()
        try {
            val op = start()
            startingRemoval = false
            removing = Removal(op.id, keepData, members)
            val early = earlyRemovalEvents[op.id]
            earlyRemovalEvents.clear()
            track(op, requestOwned = true, startedHere = true)
            // A terminal push that beat the HTTP answer wins over its older running snapshot, and is applied once.
            if (early != null && early != op) track(early, requestOwned = true, startedHere = true)
            return op
        } finally {
            startingRemoval = false
            earlyRemovalEvents.clear()
        }
    }

    /** A failed operation again, from where it can. */
    suspend fun retry(): Boolean {
        val op = state.value.operation?.takeIf { it.failed && it.retryable } ?: return false
        if (state.value.acting) return false
        // The hub would run it again without cascade and be refused again: asked instead (no remembered keepData: kept).
        if (op.kind == "uninstall" && op.errorCode == "has_dependents") {
            val asked = removing?.takeIf { it.operation == op.id }
            mutable.update { it.copy(cascade = ExtensionCascade(emptyList(), asked?.keepData ?: true, asked?.members ?: false)) }
            return false
        }
        mutable.update { it.copy(acting = true, actionError = null) }
        return try {
            if (op.kind == "uninstall") {
                val previous = finishedRemovals[op.id]
                startSteppedRemoval(previous?.keepData ?: true, previous?.members == true) { api.retryOperation(op.id) }
            } else {
                track(api.retryOperation(op.id), requestOwned = true)
            }
            true
        }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(actionError = res.getString(R.string.settings_ext_op_not_retried, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(acting = false) } }
    }

    /**
     * Stops one that downloads or checks (the hub then says it is canceled), or puts away one that failed. One the manager
     * does not know (404: lost with a restart of it, #1832) is put away too.
     */
    suspend fun closeOperation() {
        val op = state.value.operation ?: return
        if (!op.active) mutable.update { it.copy(operation = null) }
        try { api.cancelOperation(op.id) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            if (hubCodedFailure(error)?.status == 404) gone(op)
            else if (op.active) mutable.update { it.copy(actionError = failureReasonText(error).resolve(res)) }
        }
    }

    suspend fun load() {
        val mine = ++generation
        mutable.update { it.copy(loading = true) }
        try {
            val catalog = OfficialMarket.named(res, api.catalog(market = market))
            // A link from before a rename names the old ID; the market lists the extension under its new one (#2511).
            val extension = catalog.extensions.firstOrNull { it.market == market && it.id == id }
                ?: catalog.extensions.firstOrNull { it.market == market && it.replaces == id }
            if (mine == generation) mutable.update {
                it.copy(extension = extension ?: it.extension, missing = extension == null, error = null,
                    marketName = catalog.markets.firstOrNull { m -> m.id == market }?.label ?: extension?.marketName)
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { if (mine == generation) mutable.update { it.copy(error = res.getString(R.string.settings_ext_load_failed, failureReasonText(error).resolve(res))) } }
        finally { if (mine == generation) mutable.update { it.copy(loading = false) } }
    }

    /** Downloads and checks the package; the page then shows what it may do and waits for a yes. */
    suspend fun preview(): Boolean {
        if (state.value.busy) return false
        mutable.update { it.copy(previewing = true, actionError = null) }
        return try {
            if (stepped()) track(api.startFromMarket(market, id), requestOwned = true)
            else mutable.update { it.copy(preview = api.previewFromMarket(market, id)) }
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(actionError = res.getString(R.string.settings_ext_no_package, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(previewing = false) } }
    }

    /** The user said no: an operation waiting for the yes is canceled. */
    suspend fun dismissPreview() {
        val waiting = state.value.consentFor
        mutable.update { it.copy(preview = null, consentFor = null, operation = if (waiting != null) null else it.operation) }
        if (waiting != null) try { api.cancelOperation(waiting) } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) {}
    }

    /**
     * The preview expired (#1316): it is put away (an operation waiting on it canceled) and the package previewed
     * afresh, so the user agrees to what the hub checks now, never to the old preview's grants.
     */
    suspend fun previewAgain(): Boolean {
        if (state.value.acting) return false
        dismissPreview()
        return preview()
    }

    /**
     * The user agreed: installs or upgrades with the preview's permissions. Returns what to tell them, or null (an
     * operation says it when it ends). An expired preview is not sent: the hub would refuse it ([previewAgain]).
     */
    suspend fun confirm(): String? {
        val preview = state.value.preview ?: return null
        if (state.value.acting || !preview.canConfirm(now())) return null
        mutable.update { it.copy(acting = true, actionError = null) }
        if (state.value.consentFor != null) return try {
            val op = api.agree(preview, if (preview.upgrade) id else null)
            mutable.update { it.copy(preview = null) }
            track(op, requestOwned = true)
            null
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(preview = null, consentFor = null, actionError = res.getString(if (preview.upgrade) R.string.settings_ext_not_upgraded else R.string.settings_ext_not_installed, failureReasonText(error).resolve(res))) }
            null
        } finally { mutable.update { it.copy(acting = false) } }
        return try {
            val detail = if (preview.upgrade) api.upgrade(id, preview) else api.install(preview)
            mutable.update { it.copy(preview = null, acting = false) }
            load()
            CatalogText.installed(res, detail, preview.upgrade)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(preview = null, actionError = res.getString(if (preview.upgrade) R.string.settings_ext_not_upgraded else R.string.settings_ext_not_installed, failureReasonText(error).resolve(res))) }
            null
        } finally { mutable.update { it.copy(acting = false) } }
    }

    /**
     * Removes it; true when it is gone now. In steps it returns false at once and says it when it ends. [members]: a
     * pack's installed members too (`members=1`, #1154), kept for the dependents question and its Retry.
     */
    suspend fun uninstall(keepData: Boolean, cascade: Boolean = false, members: Boolean = false): Boolean {
        if (state.value.busy) return false
        removal = null
        mutable.update { it.copy(acting = true, actionError = null, partial = null) }
        return try {
            if (stepped()) {
                startSteppedRemoval(keepData, members) { api.startUninstall(id, keepData, cascade, members) }
                return false
            }
            val completed = api.uninstall(id, keepData, cascade, members)
            removal = completed
            val name = state.value.extension?.label ?: id
            val notice = extensionRemovalNotice(res, name, members, completed)
            mutable.update { it.copy(acting = false, removalNotice = notice) }
            load()
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (refused: ExtensionHasDependents) {
            mutable.update { it.copy(cascade = ExtensionCascade(refused.dependents.map { d -> d.label }, keepData, members)) }
            false
        } catch (partial: ExtensionPartialUninstall) {
            mutable.update { it.copy(partial = PackPartial.of(partial)) }
            load()
            false
        } catch (error: Exception) {
            mutable.update { it.copy(actionError = res.getString(R.string.settings_ext_not_removed, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(acting = false) } }
    }

    /** Asked about the dependents: switches them off and removes it with `cascade`. */
    suspend fun confirmCascade(): Boolean {
        val ask = state.value.cascade ?: return false
        mutable.update { it.copy(cascade = null) }
        return uninstall(ask.keepData ?: true, cascade = true, members = ask.members)
    }

    fun dismissCascade() = mutable.update { it.copy(cascade = null) }

    fun dismissPartial() = mutable.update { it.copy(partial = null) }

    /** The cleanup sheet's OK: only now are its lines consumed and the next operation's cleanup shown. */
    fun dismissRemovalNotice() {
        val acknowledged = state.value.removalNotice
        acknowledged?.operation?.let { id ->
            acknowledgedRemovalWarnings[id] = (acknowledgedRemovalWarnings[id].orEmpty() + acknowledged.warnings).distinct()
            removalNoticeQueue.removeAll { it.operation == id }
        }
        removal = null
        mutable.update { it.copy(removalNotice = null) }
        presentNextRemovalNotice()
    }
}

/** A prepared market-removal confirmation. Null count: the hub could not provide an honest count. */
internal data class MarketRemoval(val marketId: String, val count: Int?)

/**
 * How many of the current assistant's installed extensions stop upgrading when [marketId] is removed.
 * Null when ownership or the hub's market provenance is not knowable; never turn that into a false zero.
 */
internal fun affectedExtensionCount(marketId: String, extensions: List<HubExtension>, assistant: CurrentAssistant): Int? {
    // Provenance alone is insufficient: configured, builtin, development and attached items never participate in market upgrades.
    val marketInstalled = extensions.filter { it.source == "installed" && it.attached == null }
    val own = when {
        // A known roster (or an explicit non-default choice) uses the same ownership rule as the rest of Settings.
        // In particular, an unstamped item remains the default assistant's; it never moves to the selected one.
        assistant.roster != null || assistant.choice.manager != null -> marketInstalled.filter { assistant.owns(it.manager) }
        marketInstalled.any { it.manager != null && (it.market == marketId || (it.autoUpgrade != null && it.market == null)) } -> return null
        else -> marketInstalled.filter { it.manager == null }
    }
    if (own.any { it.autoUpgrade != null && it.market == null }) return null
    return own.count { it.market == marketId }
}

/** The existing uncounted warning, a true zero, or a localized singular/plural affected count. */
internal fun marketRemovalText(res: Resources, count: Int?): String = when (count) {
    null -> res.getString(R.string.settings_ext_remove_market_text)
    0 -> res.getString(R.string.settings_ext_remove_market_unused)
    else -> res.getQuantityString(R.plurals.settings_ext_remove_market_affected, count, count)
}

internal data class MarketsState(
    val markets: List<ExtensionMarket>? = null,
    val loading: Boolean = false,
    val error: String? = null,
    /** The market a call is on its way for. */
    val working: String? = null,
    val adding: Boolean = false,
    /** Why the hub did not add the market (its own words: wrong address, private needs a token…). */
    val addError: String? = null,
    /** Why the last refresh, removal or token change did not happen. */
    val actionError: String? = null,
    /** The market whose installed extensions are being counted, then its prepared confirmation. */
    val preparingRemoval: String? = null,
    val removal: MarketRemoval? = null,
    /** The auto-upgrade switch for all market extensions (docs/extensions/extensions.md "Auto-upgrade"); null: a hub without it. */
    val autoUpgrade: Boolean? = null,
    val savingAuto: Boolean = false,
)

/** "Manage markets": the markets the hub reads. */
internal class MarketsModel(private val api: ExtensionManagement, private val res: Resources) {
    private val mutable = MutableStateFlow(MarketsState())
    val state = mutable.asStateFlow()
    private var removalRequest = 0

    suspend fun load() {
        if (state.value.loading) return
        mutable.update { it.copy(loading = true) }
        try {
            val markets = api.markets().map { OfficialMarket.named(res, it) }
            mutable.update { it.copy(markets = markets, error = null) }
            // The switch is extra: a hub that cannot say leaves it out, the markets still show.
            val auto = try { api.autoUpgrades() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { null }
            mutable.update { it.copy(autoUpgrade = auto) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(error = res.getString(R.string.settings_ext_load_failed, failureReasonText(error).resolve(res))) } }
        finally { mutable.update { it.copy(loading = false) } }
    }

    fun clearAddError() = mutable.update { it.copy(addError = null) }

    /** Puts away an error from an earlier action before the token sheet opens. */
    fun clearActionError() = mutable.update { it.copy(actionError = null) }

    /** Turns auto-upgrades for all market extensions on or off; the switch shows what the hub then says. */
    suspend fun setAutoUpgrade(on: Boolean): Boolean {
        if (state.value.savingAuto) return false
        mutable.update { it.copy(savingAuto = true, actionError = null) }
        return try {
            val auto = api.setAutoUpgrades(on)
            mutable.update { it.copy(autoUpgrade = auto) }
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(actionError = res.getString(R.string.settings_ext_auto_upgrades_failed, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(savingAuto = false) } }
    }

    /**
     * Adds a market (read once by the hub first); a token, when given, is sent once and never shown. The address goes
     * to the hub as typed: it knows the forms (https, github:, git: and the ones it adds later) and says what is wrong.
     */
    suspend fun add(spec: String, token: String): Boolean {
        val s = spec.trim()
        if (s.isEmpty() || state.value.adding) return false
        mutable.update { it.copy(adding = true, addError = null) }
        return try {
            val market = OfficialMarket.named(res, api.addMarket(s, token.trim().takeIf { it.isNotEmpty() }))
            mutable.update { it.copy(markets = it.markets.orEmpty().filter { m -> m.id != market.id } + market) }
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(addError = failureReasonText(error).resolve(res)) }
            false
        } finally { mutable.update { it.copy(adding = false) } }
    }

    /** Reads the installed list before the destructive confirmation. A newer request owns the dialog. */
    suspend fun prepareRemoval(id: String, assistant: CurrentAssistant) {
        val request = ++removalRequest
        mutable.update { it.copy(preparingRemoval = id, removal = null) }
        val count = try {
            affectedExtensionCount(id, api.extensions(), assistant)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { null }
        if (request == removalRequest) {
            mutable.update { it.copy(preparingRemoval = null, removal = MarketRemoval(id, count)) }
        }
    }

    /** Cancels the dialog and fences off a count that may still be arriving. */
    fun dismissRemoval() {
        removalRequest++
        mutable.update { it.copy(preparingRemoval = null, removal = null) }
    }

    suspend fun remove(id: String): Boolean = working(id, R.string.settings_ext_market_not_removed) {
        api.removeMarket(id)
        mutable.update { it.copy(markets = it.markets?.filter { m -> m.id != id }) }
    }

    suspend fun refresh(id: String): Boolean = working(id, R.string.settings_ext_market_not_refreshed) { replace(api.refreshMarket(id)) }

    /** A new token, or "" to remove it; the hub reads the market again with it. */
    suspend fun setToken(id: String, token: String): Boolean = working(id, R.string.settings_ext_market_token_not_changed) { replace(api.setMarketToken(id, token.trim())) }

    private fun replace(market: ExtensionMarket) = mutable.update {
        val named = OfficialMarket.named(res, market)
        it.copy(markets = it.markets?.map { m -> if (m.id == named.id) named else m })
    }

    private suspend fun working(id: String, failed: Int, block: suspend () -> Unit): Boolean {
        if (state.value.working != null) return false
        mutable.update { it.copy(working = id, actionError = null) }
        return try { block(); true }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(actionError = res.getString(failed, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(working = null) } }
    }
}
