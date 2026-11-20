package persona.core.domain.sort

import persona.core.contracts.AgentState
import persona.core.contracts.Closeout
import persona.core.contracts.CoreText
import persona.core.contracts.CoreText.Key
import persona.core.contracts.Session

// An agent's closeout (docs/assistant/agent-work.md "Closeout", #3612; the web's lib/closeout.ts): the record it ends a
// turn with, saying whether the work is done, what changed, whether it is merged and deployed, what was verified and
// what is missing. It is the agent's own claim. Where the hub looked itself (`checked`), its finding stands beside the
// claim: confirmed, contradicted, or not checked. The session's page draws it as a card, list rows as a short suffix of
// fact under their line (Session.closeoutFacts).

/**
 * The closeout to show for a session: one with a status this app knows, while the session is not working (a new turn
 * has no closeout yet, and the previous turn's no longer says where the work stands).
 */
val Session.shownCloseout: Closeout? get() {
    val w = work ?: return null
    val closeout = w.closeout?.takeIf { it.known } ?: return null
    if (w.phase == "working" || agentState == AgentState.Working) return null
    return closeout
}

/** What a closeout claims of: merging, or deploying. */
enum class CloseoutSubject { Merged, Deployed }

/** How a claim stands against the hub's finding. [Found]: the agent did not say, the hub's finding stands alone. */
enum class ClaimState { Confirmed, Contradicted, Unchecked, Found }

/**
 * A claim with the hub's finding: [said] what the agent said (null when it did not say), [found] what the hub found
 * (null when it did not look or could not tell).
 */
data class CloseoutClaim(val said: Boolean?, val found: Boolean?, val state: ClaimState)

private fun yesNo(value: String?): Boolean? = when (value) { "yes" -> true; "no" -> false; else -> null }

/** The claim on [subject] with the hub's finding; null when neither the agent said nor the hub found. */
fun Closeout.claim(subject: CloseoutSubject): CloseoutClaim? {
    val said = yesNo(if (subject == CloseoutSubject.Merged) merged else deployed)
    val found = yesNo(if (subject == CloseoutSubject.Merged) checked?.merged else checked?.deployed)
    return when {
        said == null && found == null -> null
        said == null -> CloseoutClaim(null, found, ClaimState.Found)
        found == null -> CloseoutClaim(said, null, ClaimState.Unchecked)
        else -> CloseoutClaim(said, found, if (said == found) ClaimState.Confirmed else ClaimState.Contradicted)
    }
}

/** The hub found a claim of the closeout to be untrue. */
val Closeout.contradicted: Boolean
    get() = CloseoutSubject.entries.any { claim(it)?.state == ClaimState.Contradicted }

/**
 * What a list row marks beside a stopped session's line: the hub found its closeout's claim untrue, or its turn is not
 * finished (the hub publishes that as an empty phase with a line, "Closeout"); null for every other session.
 */
enum class CloseoutMark { Contradicted, NotFinished }

val Session.closeoutMark: CloseoutMark? get() {
    val closeout = shownCloseout ?: return null
    if (closeout.contradicted) return CloseoutMark.Contradicted
    val w = work ?: return null
    return if (w.phase.isEmpty() && !w.line.isNullOrBlank()) CloseoutMark.NotFinished else null
}

/** One claim in a few words for a row; [contradicted]: the agent said yes and the hub found it is not so. */
data class CloseoutFact(val text: CoreText, val contradicted: Boolean = false)

// The hub's finding when it has one ("merged ✓"), else the agent's word as it said it ("merged"); a "yes" the hub
// found untrue is marked ("✗ says merged, isn't").
private fun Closeout.fact(subject: CloseoutSubject): CloseoutFact? {
    val claim = claim(subject) ?: return null
    val merged = subject == CloseoutSubject.Merged
    val yes = if (merged) Key.CloseoutMergedYes else Key.CloseoutDeployedYes
    val no = if (merged) Key.CloseoutMergedNo else Key.CloseoutDeployedNo
    val confirmed = if (merged) Key.CloseoutMergedConfirmed else Key.CloseoutDeployedConfirmed
    val overclaimed = claim.state == ClaimState.Contradicted && claim.said == true
    return CloseoutFact(CoreText.of(when {
        overclaimed -> if (merged) Key.CloseoutMergedContradicted else Key.CloseoutDeployedContradicted
        claim.found != null -> if (claim.found) confirmed else no
        else -> if (claim.said == true) yes else no
    }), overclaimed)
}

/**
 * "merged ✓", "not deployed": where the closeout stands on merged and deployed, as short facts for a row; none when
 * it says nothing on either and the hub found nothing.
 */
val Closeout.facts: List<CloseoutFact> get() = CloseoutSubject.entries.mapNotNull(::fact)

/** [facts] of the closeout the session shows ([shownCloseout]); none without one. */
val Session.closeoutFacts: List<CloseoutFact> get() = shownCloseout?.facts.orEmpty()

/**
 * A row's line from the closeout alone, for a turn the hub gave no line of its own: what a blocked one waits for, what
 * a not-done one still needs, "Reported done" for a done one.
 */
fun Closeout.line(): CoreText = when (status) {
    "blocked" -> waiting?.takeIf(String::isNotBlank)?.let { CoreText.of(Key.CloseoutBlockedOn, it) } ?: CoreText.of(Key.CloseoutBlocked)
    "not_done" -> missing?.takeIf(String::isNotBlank)?.let { CoreText.of(Key.CloseoutNotDoneMissing, it) } ?: CoreText.of(Key.CloseoutNotDone)
    else -> CoreText.of(Key.CloseoutDone)
}

// Markdown work reports have a dedicated heading, three required facts and a terminating rule.
private val WorkReport = Regex("""(?:^|\n)(### Persona (?:work report|工作报告|作業報告)\r?\n(?:\r?\n)?- Status: (?:done|not_done|blocked)\r?\n- Merged: (?:yes|no|n/a)\r?\n- Deployed: (?:yes|no|n/a)\r?\n(?:- (?:Did|Artifact|Delivery|Unblock|Verified|Missing|Waiting|Next): [^\r\n]+\r?\n)*(?:\r?\n)?---)(?=[\t \r\n]*(?:\n<oai-mem-citation>[\s\S]*?</oai-mem-citation>[\t \r\n]*)?$)""") // i18n-ignore: the report heading as Persona writes it in each language
private val WorkReportFence = Regex("""^[\t ]*(`{3,}|~{3,})([^\r\n]*)""", RegexOption.MULTILINE)

private fun insideWorkReportFence(before: String): Boolean {
    var open = ""
    for (match in WorkReportFence.findAll(before)) {
        val fence = match.groupValues[1]
        if (open.isEmpty()) open = fence
        else if (fence.startsWith(open) && match.groupValues[2].isBlank()) open = ""
    }
    return open.isNotEmpty()
}

private val CloseoutBlock = Regex("<closeout\\b[^>]*>[\\s\\S]*?</closeout>")
private val FenceBefore = Regex("(^|\\n)[ \\t]*(`{3,}|~{3,})[^\\n`]*\\n[ \\t]*$")
private val FenceAfter = Regex("^[ \\t]*\\n[ \\t]*(`{3,}|~{3,})[ \\t]*(?=\\n|$)")

/**
 * [text] without its last closeout block, the one the hub read (it takes the last): the card above the transcript says
 * it, so the reply does not say it again as raw text. Only the block goes, with the blank lines it leaves and a code
 * fence that held nothing else; text without a block comes back as it is.
 */
fun stripCloseout(text: String, complete: Boolean = true): String {
    if (complete && text.contains("### Persona ")) {
        WorkReport.find(text)?.let { report ->
            val before = text.substring(0, report.range.first)
            if (!insideWorkReportFence(before)) {
                return listOf(before.trimEnd(), text.substring(report.range.last + 1).trimStart())
                    .filter(String::isNotEmpty).joinToString("\n\n")
            }
        }
    }
    val last = CloseoutBlock.findAll(text).lastOrNull() ?: return text
    var before = text.substring(0, last.range.first)
    var after = text.substring(last.range.last + 1)
    // a fence around the block alone goes with it: an empty code box says nothing
    val open = FenceBefore.find(before)
    val close = FenceAfter.find(after)
    if (open != null && close != null && open.groupValues[2] == close.groupValues[1]) {
        before = before.substring(0, open.range.first)
        after = after.substring(close.range.last + 1)
    }
    before = before.trimEnd()
    after = after.trimStart()
    return if (before.isNotEmpty() && after.isNotEmpty()) "$before\n\n$after" else before.ifEmpty { after }
}
