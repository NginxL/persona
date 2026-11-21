package persona.feature.settings

import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.TraeCliDisplayName
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.isTraeCliId

// A Persona-managed provider's device scope (docs/integrations/launchers.md "Device scope and on-demand delivery", the web's
// lib/provider-scope.ts): which computers it runs on, when its key travels, whether it stands back where the harness has a
// service of its own, and keys for single computers. The form, its check, the summary, the fields of POST / PUT and the
// list's grouping by harness. Pure: ProviderComputersPage only draws it.

internal object Share {
    const val All = "all"
    const val Selected = "selected"
    const val None = "none"
}

internal object Delivery {
    const val OnDemand = "on_demand"
    const val Push = "push"
}

internal object OwnService {
    const val Skip = "skip"
    const val Use = "use"
}

/**
 * The "Computers" page's form. [machines] with `selected`, [work] (work computers added) with `all`; [ownService] null:
 * not chosen, so it follows the hub's default for a new provider. [keys] typed for single computers (never prefilled),
 * [remove] the computers whose stored key goes.
 */
internal data class ScopeForm(
    val share: String = Share.All,
    val machines: List<String> = emptyList(),
    val work: List<String> = emptyList(),
    val delivery: String = Delivery.OnDemand,
    val ownService: String? = null,
    val keys: Map<String, String> = emptyMap(),
    val remove: Set<String> = emptySet(),
    /** Per-computer explicit reference modes, restored from machine_key_references. */
    val keyRefs: Set<String> = emptySet(),
) {
    /** The ids `scope_machines` carries for the share chosen. */
    val scopeMachines: List<String> get() = when (share) {
        Share.Selected -> machines
        Share.All -> work
        else -> emptyList()
    }

    /** The own-service rule it saves: chosen, else the hub's default (use with assign or a direct API, else skip). */
    fun ownServiceOf(defaultUse: Boolean = false): String = ownService ?: if (defaultUse) OwnService.Use else OwnService.Skip

    /** "Chosen computers" with none: the hub would refuse it. */
    val problem: ResText? get() = if (share == Share.Selected && machines.isEmpty()) ResText.of(R.string.settings_pscope_pick_machine) else null

    /** How many computers have a key of their own once saved: the stored ones not removed, plus those typed for others. */
    fun keyCount(stored: List<String>): Int =
        (stored.filterNot { it in remove } + keys.filter { it.value.isNotBlank() }.keys).distinct().size

    fun toggle(id: String): ScopeForm = copy(machines = if (id in machines) machines - id else machines + id)
    fun toggleWork(id: String): ScopeForm = copy(work = if (id in work) work - id else work + id)
    fun typeKey(id: String, key: String): ScopeForm = copy(keys = keys + (id to key))
    fun referenceKey(id: String, byReference: Boolean): ScopeForm = copy(
        keys = keys - id, keyRefs = if (byReference) keyRefs + id else keyRefs - id)
    fun removeKey(id: String): ScopeForm = copy(remove = remove + id, keys = keys - id)
    fun keepKey(id: String): ScopeForm = copy(remove = remove - id)
}

/** The scope an existing provider opens with; null when its hub does not know scopes. */
internal fun scopeFrom(l: HubLauncher): ScopeForm? {
    val share = l.share?.takeIf { it.isNotBlank() } ?: return null
    return ScopeForm(
        share = share,
        machines = if (share == Share.Selected) l.scopeMachines else emptyList(),
        work = if (share == Share.All) l.scopeMachines else emptyList(),
        delivery = l.delivery?.takeIf { it.isNotBlank() } ?: Delivery.OnDemand,
        ownService = l.ownService?.takeIf { it.isNotBlank() } ?: OwnService.Skip,
        keyRefs = l.machineKeyReferences.keys,
    )
}

/** The hub's id of a machine in the merged list, and whether it is this hub's (the merged list holds other groups' too). */
private fun Machine.hubId(): String = GroupKeys.raw(id)
private fun Machine.ownHub(): Boolean = GroupKeys.namespaceOf(id) == null

/** A work computer, when the hub says so (`trust: "work"`); hubs that only mark pending ones make none. */
internal fun Machine.work(): Boolean = trust == "work"

/** The computers "Chosen computers" offers: this hub's confirmed ones, plus whatever is already chosen (kept to uncheck). */
internal fun choosable(machines: List<Machine>, chosen: List<String>): List<String> {
    val out = machines.filter { it.ownHub() && (!it.pending || it.hubId() in chosen) }.map { it.hubId() }.toMutableList()
    chosen.forEach { if (it !in out) out += it }
    return out.distinct()
}

/** The work computers "All my computers" can add: the known ones, plus those already added. */
internal fun addableWork(machines: List<Machine>, added: List<String>): List<String> {
    val out = machines.filter { it.ownHub() && it.work() && !it.pending }.map { it.hubId() }.toMutableList()
    added.forEach { if (it !in out) out += it }
    return out.distinct()
}

/** The computers that may get the provider, whose own key can be set: none for `none`. */
internal fun keyMachines(f: ScopeForm, machines: List<Machine>): List<String> = when (f.share) {
    Share.None -> emptyList()
    Share.Selected -> f.machines
    else -> (machines.filter { it.ownHub() && !it.pending && !it.work() }.map { it.hubId() } + f.work).distinct()
}

/** The scope's fields of POST / PUT; each null when it does not go. */
internal data class ScopeFields(
    val share: String? = null,
    val scopeMachines: List<String>? = null,
    val delivery: String? = null,
    val ownService: String? = null,
    val machineKeys: Map<String, String>? = null,
    val machineKeyReferences: Map<String, String>? = null,
)

/**
 * The scope's fields of POST / PUT /api/v1/launchers (the web's scopeBody). A PUT ([old] has a share) sends a field only
 * when it changed (left out keeps what is stored, so a legacy provider stays as it was until one is changed); a POST
 * only what differs from the hub's defaults (all, on demand, skip; use with [defaultUse]). `scope_machines` goes with
 * the share whenever either changed. `machine_keys` carries typed keys for computers that may get the provider (or
 * already have a key of their own), "" for each removed one.
 */
internal fun scopeFields(f: ScopeForm, old: HubLauncher?, machines: List<Machine>, defaultUse: Boolean = false): ScopeFields {
    val ids = f.scopeMachines
    val own = f.ownServiceOf(defaultUse)
    val stored = old?.takeIf { it.hasScope }
    var out = if (stored != null) {
        val shareChanged = f.share != stored.share
        ScopeFields(
            share = f.share.takeIf { shareChanged },
            scopeMachines = ids.takeIf { shareChanged || ids.sorted() != stored.scopeMachines.sorted() },
            delivery = f.delivery.takeIf { it != (stored.delivery?.takeIf(String::isNotBlank) ?: Delivery.OnDemand) },
            ownService = own.takeIf { it != (stored.ownService?.takeIf(String::isNotBlank) ?: OwnService.Skip) },
        )
    } else {
        ScopeFields(
            share = f.share.takeIf { it != Share.All },
            scopeMachines = ids.takeIf { it.isNotEmpty() },
            delivery = f.delivery.takeIf { it != Delivery.OnDemand },
            ownService = own.takeIf { f.ownService != null && it != (if (defaultUse) OwnService.Use else OwnService.Skip) },
        )
    }
    val allowed = keyMachines(f, machines) + old?.keyMachines.orEmpty() + old?.machineKeyReferences.orEmpty().keys
    val keys = linkedMapOf<String, String>()
    val references = linkedMapOf<String, String>()
    f.keys.forEach { (id, key) ->
        if (key.isNotBlank() && id in allowed) {
            if (id in f.keyRefs) references[id] = key.trim() else keys[id] = key.trim()
        }
    }
    f.remove.forEach { id -> if (id !in keys && id !in references) keys[id] = "" }
    if (keys.isNotEmpty()) out = out.copy(machineKeys = keys)
    if (references.isNotEmpty()) out = out.copy(machineKeyReferences = references)
    return out
}

/** The own-service switch applies: the harnesses whose own sign-in the node can see (Claude Code, Codex). */
internal fun ownServiceDetected(kind: String, agent: String?): Boolean =
    kind != "api" && (agent.isNullOrBlank() || agent == "claude" || agent == "codex")

/** The scope as it would be saved, for the summary. */
internal fun ScopeForm.asView(defaultUse: Boolean = false): HubLauncher =
    HubLauncher("", source = "persona", share = share, scopeMachines = scopeMachines, delivery = delivery, ownService = ownServiceOf(defaultUse))

/**
 * The parts of one line for the detail page and the form's row: where, how the key travels, and whether it also runs
 * where the harness has a service of its own. [name] gives a machine's name, [separator] joins several.
 */
internal fun scopeSummary(v: HubLauncher, name: (String) -> String, separator: String): List<ResText> {
    if (v.share == Share.None) return listOf(ResText.of(R.string.settings_pscope_summary_none))
    val ids = v.scopeMachines
    val where = when {
        v.legacyScope -> ResText.of(R.string.settings_pscope_summary_legacy)
        v.share == Share.Selected && ids.isEmpty() -> ResText.of(R.string.settings_pscope_summary_selected_none)
        v.share == Share.Selected -> ResText.of(R.string.settings_pscope_summary_list, ids.joinToString(separator, transform = name))
        ids.isNotEmpty() -> ResText.plural(R.plurals.settings_pscope_summary_all_work, ids.size, ids.size)
        else -> ResText.of(R.string.settings_pscope_summary_all)
    }
    return listOfNotNull(where,
        ResText.of(if (v.delivery == Delivery.Push) R.string.settings_pscope_summary_push else R.string.settings_pscope_summary_on_demand),
        ResText.of(R.string.settings_pscope_summary_use).takeIf { v.ownService == OwnService.Use })
}

// ── The list, grouped by harness ─────────────────────────────

private val HarnessOrder = listOf("claude", "codex", "agy", "omp", "api")

/** Providers by harness: the known ones in a fixed order, any other after them; the list's order kept within each. */
internal fun harnessGroups(list: List<HubLauncher>): List<Pair<String, List<HubLauncher>>> {
    val groups = linkedMapOf<String, MutableList<HubLauncher>>()
    list.forEach { groups.getOrPut(it.harness) { mutableListOf() } += it }
    fun rank(h: String) = HarnessOrder.indexOf(h).takeIf { it >= 0 } ?: HarnessOrder.size
    return groups.entries.sortedBy { rank(it.key) }.map { it.key to it.value.toList() }
}

/** A harness's header: the products' own names; null for a direct API, which the page words in its language. */
internal fun harnessName(h: String): String? = when {
    h == "claude" -> "Claude Code"
    h == "codex" -> "Codex"
    isTraeCliId(h) -> TraeCliDisplayName
    h == "agy" -> "Antigravity"
    h == "omp" -> "Oh My Pi"
    h == "api" -> null
    else -> h
}
