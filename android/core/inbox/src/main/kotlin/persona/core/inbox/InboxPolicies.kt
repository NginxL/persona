package persona.core.inbox

import persona.core.contracts.parseTime
import androidx.annotation.StringRes
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import persona.core.domain.inbox.InboxItem

/** A rule's tier: [None] leaves it to triage. */
enum class InboxPolicyTier(val wire: String, @get:StringRes val title: Int) {
    None("", R.string.inbox_policy_tier_none), Action("action", R.string.inbox_policy_tier_action),
    Fyi("fyi", R.string.inbox_policy_tier_fyi), Noise("noise", R.string.inbox_policy_tier_noise);

    companion object { fun of(wire: String?): InboxPolicyTier = entries.firstOrNull { it.wire == wire } ?: None }
}

/** What a rule does with the AI twin: [First] hands its items over first (even when not Needs you), [Never] keeps them away; [None] as set. */
enum class InboxPolicyTwin(val wire: String, @get:StringRes val title: Int) {
    None("", R.string.inbox_policy_twin_none), First("first", R.string.inbox_policy_twin_first), Never("never", R.string.inbox_policy_twin_never);

    companion object { fun of(wire: String?): InboxPolicyTwin = entries.firstOrNull { it.wire == wire } ?: None }
}

/** A rule the user set (`GET /api/v1/inbox/policies`): items of [source] with [value] under [tag] get its tier and twin. */
data class InboxPolicy(
    val id: String,
    val source: String,
    val sourceName: String? = null,
    val tag: String,
    val value: String,
    val name: String,
    val tier: InboxPolicyTier = InboxPolicyTier.None,
    val twin: InboxPolicyTwin = InboxPolicyTwin.None,
    val createdAt: String? = null,
    val updatedAt: String? = null,
    /** Whose rule it is (several managers: each keeps its own, and "Rules" stamps it); null: not known (one manager). */
    val manager: String? = null,
) {
    val option: InboxMuteOption get() = InboxMuteOption(tag, value, name)

    fun covers(source: String, option: InboxMuteOption) = this.source == source && tag == option.tag && value == option.value

    /** What it does, as the ids of its words: "Always Needs you", "AI twin first"; only the parts it sets. */
    val summary: List<Int> get() = listOfNotNull(
        tier.takeIf { it != InboxPolicyTier.None }?.title, twin.takeIf { it != InboxPolicyTwin.None }?.title)
}

/**
 * What the rule sheet is about: a new rule from one of [record]'s options, or an existing [policy] from "Rules". A rule
 * from an item is its manager's, as its mute is; one from "Rules" is the manager it was read from.
 */
data class InboxPolicyTarget(
    val source: String,
    val option: InboxMuteOption,
    val record: InboxRecord? = null,
    val policy: InboxPolicy? = null,
) {
    /** Whose rule it is: the item's manager ("" for the default's), else the rule's; null: not known (one manager). */
    val manager: String? get() = if (record != null) record.manager.orEmpty() else policy?.manager

    /** The source's name for people: the item's account, else the rule's source name, else the source itself. */
    val sourceName: String get() = record?.sourceLabel?.takeIf { it.isNotBlank() } ?: policy?.sourceName?.takeIf { it.isNotBlank() } ?: source

    companion object {
        fun of(record: InboxRecord, option: InboxMuteOption) = InboxPolicyTarget(record.source, option, record)
        fun of(policy: InboxPolicy) = InboxPolicyTarget(policy.source, policy.option, policy = policy)
    }
}

/** The hub's rules, newest first (by time: RFC 3339 drops trailing zeros, so the text does not sort). */
fun inboxPolicies(body: JsonObject): List<InboxPolicy> =
    (body["policies"] as? JsonArray).orEmpty().mapNotNull { (it as? JsonObject)?.let(::inboxPolicy) }.newestFirst()

/** One rule as the hub sends it (in the list, and as `policy` after a save); null without an id. */
fun inboxPolicy(p: JsonObject): InboxPolicy? {
    val id = p.text("id").takeIf { it.isNotBlank() } ?: return null
    return InboxPolicy(id, p.text("source"), p.cursor("source_name"), p.text("tag"), p.text("value"), p.text("name").ifBlank { p.text("value") },
        InboxPolicyTier.of(p.cursor("tier")), InboxPolicyTwin.of(p.cursor("twin")), p.cursor("created_at"), p.cursor("updated_at"))
}

/**
 * The records with a rule's [tier] applied at once, as the hub applies it: the ones it reaches ([mine]: the rule's
 * manager's) take the tier, a section that is not "working" follows it, and a tier other than Needs you clears urgent.
 * [InboxPolicyTier.None] leaves them all (the next triage of new content decides). Records left alone are the same objects.
 */
fun applyPolicyTier(
    records: List<InboxRecord>, source: String, option: InboxMuteOption, tier: InboxPolicyTier,
    mine: (InboxRecord) -> Boolean = { true },
): List<InboxRecord> {
    if (tier == InboxPolicyTier.None) return records
    return records.map { r ->
        if (!policyReaches(source, option, r) || !mine(r) || r.item.tier == tier.wire) return@map r
        fun section(status: String?) = status?.let { if (it != InboxItem.WORKING) tier.wire else it }
        val urgent = tier == InboxPolicyTier.Action && r.item.urgent
        r.copy(item = r.item.copy(tier = tier.wire, status = section(r.item.status), urgent = urgent),
            event = r.event.copy(status = section(r.event.status), urgent = tier == InboxPolicyTier.Action && r.event.urgent))
    }
}

/** Whether a rule's tier reaches an item at once (the hub's rule): the same thing, open, triaged, not being handled. */
fun policyReaches(source: String, option: InboxMuteOption, record: InboxRecord): Boolean =
    record.source == source && !record.item.isDone && record.item.isTriaged && !record.item.isWorking &&
        record.mutes.any { it.tag == option.tag && it.value == option.value }

private fun List<InboxPolicy>.newestFirst() = sortedByDescending { parseTime(it.createdAt)?.toEpochMilli() ?: 0L }
