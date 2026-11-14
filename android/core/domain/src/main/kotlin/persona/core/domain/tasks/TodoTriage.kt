package persona.core.domain.tasks

import java.time.ZoneId
import persona.core.contracts.AgentState
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.TodoAsk
import persona.core.contracts.parseTime

// The To-dos page's sections (#3156, #3206, docs/assistant/todo-status-sop.md): each to-do's place comes from real signals
// only — its agents' sessions (alive, running, asking), its PR / delivery state, the latest task_review verdict and when it
// last moved — never from what an agent says about itself. A port of the web's `web/src/lib/todo-triage.ts`; the rules are
// the SOP's table, in its order, and the first that holds wins. A change to the rules changes every client.

/**
 * Where a to-do shows, in the page's order (#5707): what the owner has to act on, the unfinished work, what just finished,
 * then the folded ones, Done last. A merged row is led by its member in the first of these.
 */
enum class TriageSection { Waiting, Working, Pr, Other, JustDone, Shelved, Done;

    /** Starts folded: Shelved and Done; the rest start open, unfinished to-dos included. */
    val folded: Boolean get() = this == Shelved || this == Done
}

/** Which SOP rule placed a to-do; the section follows from it. */
enum class TriageRule { FailedStart, Superseded, AgentAsks, OwnerDecides, AgentRunning, Closed, Accepted, Deployed, FinishedUnseen, PrOpen, Seen, Stale, Rest }

/** A to-do's section, the rule that placed it, and the last moment anything of it moved (ms, 0 when nothing says). */
data class Triage(val section: TriageSection, val rule: TriageRule, val last: Long)

/** No activity for this long, nothing of it running: shelved. */
const val SHELVE_AFTER_MS: Long = 48 * 3_600_000L

/** Finished this recently, and not seen since: "Just finished"; earlier, "Done". Seeing it (#5707) files it at once. */
const val JUST_DONE_MS: Long = 24 * 3_600_000L

private fun ms(at: String?): Long = parseTime(at)?.toEpochMilli() ?: 0L

/** The web's todo status (`web/src/lib/todos.ts` `todoStatus`), which the triage reads: an open ask does not make it Needs you. */
enum class TriageStatus { Failed, Done, NotStarted, NeedsYou, Interrupted, Unread, Working, FollowUp }

/** The status the triage reads from the to-do and its current agents' sessions, as the web computes it. */
fun Todo.triageStatus(lookup: (SessionKey) -> Session?): TriageStatus {
    if (!startError.isNullOrEmpty()) return TriageStatus.Failed
    if (isDone) return TriageStatus.Done
    val active = currentAgents
    if (active.isEmpty()) return TriageStatus.NotStarted
    var interrupted = false
    var unread = false
    var working = false
    for (a in active) {
        if (!a.startError.isNullOrEmpty()) continue // a failed start counts as stopped
        val s = lookup(a.key)
        val st = s?.agentState ?: AgentState.Offline
        if (st == AgentState.NeedsYou) return TriageStatus.NeedsYou
        when {
            s?.interrupted == true -> interrupted = true
            st == AgentState.Unread -> unread = true
            st == AgentState.Working || s?.starting == true -> working = true
        }
    }
    return when {
        interrupted -> TriageStatus.Interrupted
        unread -> TriageStatus.Unread
        working -> TriageStatus.Working
        else -> TriageStatus.FollowUp
    }
}

/** The ask that is open now: one with a verdict and the moment it was asked (web `openAsk`). */
val Todo.openAsk: TodoAsk? get() = ask?.takeIf { it.verdict.isNotEmpty() && it.askedAt.isNotEmpty() }

/**
 * The last moment the to-do or any of its agents moved: its own times, its supervision's (reviews, reports), its
 * delivery and ask, and each linked agent's session. A restore from Shelved counts as a move ([restoredAt]).
 */
fun Todo.lastActivity(lookup: (SessionKey) -> Session?, restoredAt: Long = 0): Long {
    val sup = supervision
    var last = maxOf(
        maxOf(ms(updatedAt), ms(createdAt), ms(reopenedAt), ms(doneAt)),
        maxOf(ms(sup?.updatedAt), ms(sup?.progress?.at), ms(sup?.latestReview?.at)),
        maxOf(ms(delivery?.at), ms(ask?.askedAt?.takeIf(String::isNotEmpty)), restoredAt),
    )
    for (a in agents) {
        if (a.releasedAt != null) continue
        val s = lookup(a.key) ?: continue
        last = maxOf(last, ms(s.updatedAt), ms(s.stateSince))
    }
    return last
}

/** The PR its work stopped at, still to merge: the delivery stopped at a PR, or a PR is known and no delivery state is past it. */
val Todo.hasOpenPR: Boolean
    get() {
        val d = delivery?.state
        if (d == "pr") return true
        return !prUrl.isNullOrBlank() && (d.isNullOrEmpty() || d == "none")
    }

/** A task (an agent was linked, or a conversation handed it out) with no due date ahead: the user's own reminders never shelve. */
private fun Todo.shelvable(now: Long, zone: ZoneId): Boolean {
    if (kind == "concern") return false
    val due = dueBy(zone)?.toEpochMilli()
    if (due != null && due >= now) return false
    return agents.isNotEmpty() || !origin.isNullOrEmpty()
}

/**
 * A to-do's section by the SOP's decision table, first match wins:
 *
 *  1. FailedStart    the task's agent never started                     → Waiting (retry or remove), Done after 48 h;
 *     Superseded     … and a related to-do (its row) really finished    → Just finished (24 h), then Done (#3713)
 *  2. AgentAsks      an agent asks (question, approval) or was cut off  → Waiting
 *  3. OwnerDecides   review says needs_user, or a decision / "cannot" ask → Waiting
 *  4. AgentRunning   an agent is working or starting                    → Working
 *  5. Closed         done_at set                                        → Just finished (24 h), then Done
 *  6. Accepted       the latest review accepted it (even reopened)      → Just finished (24 h), then Done
 *  7. Deployed       its delivery is live                               → Just finished (24 h), then Done
 *  8. FinishedUnseen an agent finished unseen, awaits review, or a done ask → Just finished
 *  9. PrOpen         its PR is open, nothing running                    → PR to merge (however old)
 *  9a. Seen          rule 8, seen since it last moved, no PR open       → Done
 * 10. Stale          a task with no activity for 48 h                   → Shelved
 * 11. Rest           anything else                                      → Other open
 *
 * An open concern keeps rules 1–4, 8 and 9, never uses 6, 7, 9a or 10, and an old failed start is Rest.
 * A closed concern follows rule 5 like any closed to-do (#3582).
 */
fun Todo.triage(
    lookup: (SessionKey) -> Session?,
    now: Long,
    restoredAt: Long = 0,
    zone: ZoneId = ZoneId.systemDefault(),
    superseded: Boolean = false,
): Triage {
    val last = lastActivity(lookup, restoredAt)
    val status = triageStatus(lookup)
    // seen: the owner looked at it (`seen_at`, #5707) at or after `at`; fresh: it got to Just finished at `at`, under a day
    // ago, and is not seen since
    val seenMs = ms(this.seenAt)
    fun seen(at: Long) = seenMs > 0 && seenMs >= at
    fun fresh(at: Long) = now - at < JUST_DONE_MS && !seen(at)
    val ask = openAsk
    fun at(section: TriageSection, rule: TriageRule) = Triage(section, rule, last)
    val review = reviewState
    // Only its report closes a concern: no other signal files an open one with finished work or shelves it.
    val concern = kind == "concern" && !isDone

    if (status == TriageStatus.Failed) {
        // the same piece of work finished in a related to-do ([supersededFailedStarts]): history, nothing to retry
        if (superseded) return at(if (fresh(ms(doneAt).takeIf { it > 0 } ?: last)) TriageSection.JustDone else TriageSection.Done, TriageRule.Superseded)
        if (now - last < SHELVE_AFTER_MS) return at(TriageSection.Waiting, TriageRule.FailedStart)
        if (concern) return at(TriageSection.Other, TriageRule.Rest)
        return at(TriageSection.Done, TriageRule.FailedStart)
    }
    if (status == TriageStatus.NeedsYou || status == TriageStatus.Interrupted) return at(TriageSection.Waiting, TriageRule.AgentAsks)
    if (!isDone && (review == "needs_user" || ask?.outcome == "decide" || ask?.outcome == "cannot")) return at(TriageSection.Waiting, TriageRule.OwnerDecides)
    if (status == TriageStatus.Working) return at(TriageSection.Working, TriageRule.AgentRunning)
    if (isDone) return at(if (fresh(ms(doneAt))) TriageSection.JustDone else TriageSection.Done, TriageRule.Closed)
    if (!concern && acceptanceStands) return at(if (fresh(ms(supervision?.latestReview?.at))) TriageSection.JustDone else TriageSection.Done, TriageRule.Accepted)
    if (!concern && delivery?.state == "live") return at(if (fresh(ms(delivery?.at).takeIf { it > 0 } ?: last)) TriageSection.JustDone else TriageSection.Done, TriageRule.Deployed)
    val finished = status == TriageStatus.Unread || review == "pending_review" || (ask != null && ask.outcome.isNullOrEmpty()) || ask?.outcome == "done"
    if (finished && !seen(last)) return at(TriageSection.JustDone, TriageRule.FinishedUnseen)
    if (hasOpenPR) return at(TriageSection.Pr, TriageRule.PrOpen)
    // seen since it last moved, and no PR waits on the owner; an open concern is never Done
    if (finished && !concern) return at(TriageSection.Done, TriageRule.Seen)
    if (shelvable(now, zone) && now - last >= SHELVE_AFTER_MS) return at(TriageSection.Shelved, TriageRule.Stale)
    return at(TriageSection.Other, TriageRule.Rest)
}

/**
 * Whether a to-do counts toward the header's open count: not closed, not accepted or deployed, not shelved. A finished
 * turn nobody looked at yet (Just finished, still open) counts: only a review or the user closes a to-do. One the owner
 * has seen since (rule 9a, in Done) does not.
 */
fun Todo.countsAsOpen(t: Triage): Boolean {
    // a failed start has done_at too (the hub ends it), yet while it waits on the owner it is theirs to retry: open
    if (t.rule == TriageRule.FailedStart) return t.section == TriageSection.Waiting
    if (isDone || t.section == TriageSection.Shelved || t.section == TriageSection.Done) return false
    return t.rule != TriageRule.Accepted && t.rule != TriageRule.Deployed
}

/** Where a to-do stands for every count of open work (#3331): open counts, shelved folds, closed is finished. */
enum class TodoBucket { Open, Shelved, Closed }

/**
 * The one rule the To-dos page's "Not done" and the assistant's Progress entry both count by (#3331), so the two numbers
 * agree: [countsAsOpen] on [triage]; a to-do not open is shelved when its section is Shelved, else closed.
 */
fun Todo.bucket(lookup: (SessionKey) -> Session?, now: Long, restoredAt: Long = 0, superseded: Boolean = false): TodoBucket {
    val t = triage(lookup, now, restoredAt, superseded = superseded)
    return when {
        countsAsOpen(t) -> TodoBucket.Open
        t.section == TriageSection.Shelved -> TodoBucket.Shelved
        else -> TodoBucket.Closed
    }
}

/**
 * Whether a to-do's checkbox shows it open (the web's `checksAsOpen`): not closed, or a failed start still waiting on the
 * owner (rule 1), which the hub closed itself and the page counts as open (#3713). Ticking an open one closes it (done:
 * for a failed start the hub clears its start error, so it moves to Just finished); unticking a closed one reopens it.
 */
fun Todo.checksAsOpen(t: Triage?): Boolean =
    !isDone || !startError.isNullOrEmpty() && t?.rule == TriageRule.FailedStart && t.section == TriageSection.Waiting

/** An accepted task its hub reopened with more work, with nothing of it running or asking: it reads Accepted, not Follow up.
 * An open concern still waits for its report, so neither Accepted nor its done styling applies (#3582). */
fun Todo.acceptedAtRest(lookup: (SessionKey) -> Session?): Boolean =
    kind != "concern" && !isDone && acceptanceStands && triageStatus(lookup).let {
        it == TriageStatus.FollowUp || it == TriageStatus.Unread || it == TriageStatus.NotStarted
    }

// ── Related to-dos ────────────────────────────────────────────

private val ISSUE_REF = Regex("#(\\d{2,7})\\b")
private val TITLE_NOISE = Regex("[\\s\\p{P}\\p{S}]+")

/**
 * The keys that make two to-dos the same piece of work: the same PR (case and a trailing slash ignored), the same issue
 * number in the title, the same title once case, spaces and punctuation are dropped (six characters or more), or the
 * same plan (its subtasks under one request).
 */
fun Todo.relatedKeys(): List<String> {
    val keys = mutableListOf<String>()
    val pr = prUrl.orEmpty().trim().replace(Regex("[#?].*$"), "").trimEnd('/').lowercase()
    if (pr.isNotEmpty()) keys += "pr:$pr"
    ISSUE_REF.find(title)?.let { keys += "issue:" + it.groupValues[1] }
    val t = title.lowercase().replace(TITLE_NOISE, "")
    // long enough that two short generic titles ("test it") do not merge
    if (t.codePointCount(0, t.length) >= 6) keys += "title:$t"
    if (!planId.isNullOrEmpty()) keys += "plan:$planId"
    return keys
}

/** One row: related to-dos merged, led by the most urgent member (by section, then most recent). */
data class TriageGroup(
    val key: String,
    val section: TriageSection,
    val lead: Todo,
    /** Every member, the lead first, then by section and recency. */
    val members: List<Todo>,
    val triage: Map<String, Triage>,
    val last: Long,
)

data class TriageResult(
    val sections: Map<TriageSection, List<TriageGroup>>,
    /** To-dos (not rows) per section. */
    val todoCounts: Map<TriageSection, Int>,
    /** The header's "N open · N shelved · N done". */
    val open: Int,
    val shelved: Int,
    val finished: Int,
) {
    fun rows(section: TriageSection): List<TriageGroup> = sections[section].orEmpty()

    /**
     * This without the to-dos [ids] (another grouping shows them already, #4352): a row keeps its other members, led by
     * the first one left and in that one's section; a row left with none goes. The header totals stay as they are.
     */
    fun without(ids: Set<String>): TriageResult {
        if (ids.isEmpty()) return this
        val groups = sections.values.flatten().mapNotNull { group ->
            val members = group.members.filterNot { it.id in ids }
            when {
                members.isEmpty() -> null
                members.size == group.members.size -> group
                else -> group.copy(section = group.triage.getValue(members.first().id).section, lead = members.first(),
                    members = members, triage = members.associate { it.id to group.triage.getValue(it.id) },
                    last = members.maxOf { group.triage.getValue(it.id).last })
            }
        }
        val kept = groups.flatMap { group -> group.members.map { group.triage.getValue(it.id).section } }
        return copy(
            sections = TriageSection.entries.associateWith { section ->
                groups.filter { it.section == section }.sortedWith(compareByDescending<TriageGroup> { it.last }.thenBy { it.key })
            },
            todoCounts = TriageSection.entries.associateWith { section -> kept.count { it == section } },
        )
    }
}

/** The row each to-do is in: the root of its related to-dos (union of shared keys), open and closed kept apart.
 * An open concern always has its own row: it neither joins a related row nor lets another to-do join through its keys. */
fun relatedRoots(list: List<Todo>): Map<String, String> {
    val parent = HashMap<String, String>().apply { list.forEach { put(it.id, it.id) } }
    fun find(id: String): String {
        var r = id
        while (parent[r] != r) r = parent.getValue(r)
        parent[id] = r
        return r
    }
    val owner = HashMap<String, String>()
    for (td in list) {
        if (td.kind == "concern" && !td.isDone) continue
        val side = if (td.isDone) "c" else "o"
        for (k in td.relatedKeys()) {
            val key = "$side|$k"
            val other = owner[key]
            if (other == null) owner[key] = td.id else parent[find(td.id)] = find(other)
        }
    }
    return list.associate { it.id to find(it.id) }
}

/**
 * Failed starts (rule 1) whose row has a to-do that really finished: closed without a start error (#3713). The work got
 * done another way; the failed start goes to Just finished / Done and counts as closed, on the page and in the
 * assistant's entries alike (the web's `supersededFailedStarts`).
 */
fun supersededFailedStarts(todos: List<Todo>): Set<String> {
    val roots = relatedRoots(todos)
    val finished = todos.filter { it.isDone && it.startError.isNullOrEmpty() }.mapTo(HashSet()) { roots.getValue(it.id) }
    return todos.filter { !it.startError.isNullOrEmpty() && roots.getValue(it.id) in finished }.mapTo(HashSet()) { it.id }
}

/**
 * Every to-do placed in its section, related ones merged into one row (union of shared keys). Closed to-dos are not merged
 * into open ones' rows. A group takes its most urgent member's section (a superseded failed start never leads); rows in
 * a section go most recently active first.
 * [quiet] false leaves quiet tasks (#1731) out, as a badge does; the page lists them.
 */
fun triageTodos(
    todos: List<Todo>,
    lookup: (SessionKey) -> Session?,
    now: Long,
    restored: Map<String, Long> = emptyMap(),
    quiet: Boolean = true,
    zone: ZoneId = ZoneId.systemDefault(),
    // a caller that narrows the list (search, a filter) passes the set decided on every to-do (#3713)
    known: Set<String>? = null,
): TriageResult {
    val list = if (quiet) todos else todos.filter { !it.quiet }
    val superseded = known ?: supersededFailedStarts(list)
    val placed = list.associate { it.id to it.triage(lookup, now, restored[it.id] ?: 0, zone, superseded = it.id in superseded) }

    val roots = relatedRoots(list)
    val byRoot = LinkedHashMap<String, MutableList<Todo>>()
    for (td in list) byRoot.getOrPut(roots.getValue(td.id)) { mutableListOf() }.add(td)

    val counts = TriageSection.entries.associateWith { 0 }.toMutableMap()
    var open = 0
    for (td in list) {
        val tr = placed.getValue(td.id)
        counts[tr.section] = counts.getValue(tr.section) + 1
        if (td.countsAsOpen(tr)) open++
    }
    val shelved = counts.getValue(TriageSection.Shelved)
    val finished = list.size - open - shelved

    val sections = TriageSection.entries.associateWith { mutableListOf<TriageGroup>() }
    for (group in byRoot.values) {
        val members = group.sortedWith(
            compareBy<Todo> { placed.getValue(it.id).section.ordinal }
                // a superseded failed start never leads the row of the work that finished
                .thenBy { placed.getValue(it.id).rule == TriageRule.Superseded }
                .thenByDescending { placed.getValue(it.id).last }
                .thenBy { it.id }
        )
        val lead = members.first()
        val lt = placed.getValue(lead.id)
        sections.getValue(lt.section).add(TriageGroup(
            key = lead.id,
            section = lt.section,
            lead = lead,
            members = members,
            triage = members.associate { it.id to placed.getValue(it.id) },
            last = members.maxOf { placed.getValue(it.id).last },
        ))
    }
    val sorted = sections.mapValues { (_, rows) -> rows.sortedWith(compareByDescending<TriageGroup> { it.last }.thenBy { it.key }) }
    return TriageResult(sorted, counts, open, shelved, finished)
}

/**
 * Restores kept on this device (to-do id → when, ms), dropping ones that would have shelved again anyway, with [id]
 * restored at [now]. The hub keeps nothing about it (docs/assistant/todo-status-sop.md, "Shelving").
 */
fun withRestore(restored: Map<String, Long>, id: String, now: Long): Map<String, Long> =
    restored.filter { (_, at) -> now - at < SHELVE_AFTER_MS } + (id to now)
