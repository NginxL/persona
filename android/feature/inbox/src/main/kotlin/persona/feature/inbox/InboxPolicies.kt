package persona.feature.inbox

import android.content.res.Resources
import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.parseTime
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxItem
import persona.core.inbox.*

// Inbox policies (docs/assistant/inbox.md "Policies"), as the web's PolicyDialog: "Rule for this conversation / sender /
// repository" in an item's menu opens InboxPolicySheet (where its items go, what the AI twin does with them); "Rules" in
// the Inbox's ⋯ menu lists them (InboxRulesSheet) with Edit and Remove. The hub decides in code; this only sets and lists.

/** The menu's words for setting a rule on an option: "Rule for this repository / conversation / sender". */
@get:StringRes val InboxMuteOption.policyTitle: Int get() = when (tag) {
    "repo" -> R.string.inbox_policy_repo
    "chat" -> R.string.inbox_policy_chat
    else -> R.string.inbox_policy_sender
}

private fun List<InboxPolicy>.newestFirst() = sortedByDescending { parseTime(it.createdAt)?.toEpochMilli() ?: 0L }

/** The tags a rule can name, the most specific first: a sender's rule beats its chat's (the hub's rule). */
private val POLICY_TAGS = listOf("sender", "chat", "repo")

/** The rule that applies to an item, as the hub picks it: the same source, one of its mute options, sender before chat before repository. */
fun policyFor(record: InboxRecord, policies: List<InboxPolicy>): InboxPolicy? =
    record.mutes.filter { it.tag in POLICY_TAGS }.sortedBy { POLICY_TAGS.indexOf(it.tag) }
        .firstNotNullOfOrNull { o -> policies.firstOrNull { it.covers(record.source, o) } }

/**
 * An item's menu entries for its mute options, then a rule for each, each group under a line (the row's long press),
 * with the name after the words ("Mute this repository · example-repo").
 */
internal fun muteAndRuleActions(
    res: Resources, record: InboxRecord, onMute: (InboxMuteOption) -> Unit, onRule: (InboxMuteOption) -> Unit,
): List<JAction> =
    record.mutes.mapIndexed { i, option ->
        val name = if (option.tag == "chat") record.event.where?.takeIf { it.isNotBlank() } ?: option.shortName else option.shortName
        JAction(res.getString(R.string.inbox_mute_option, res.getString(option.title), name), PhIcons.Prohibit,
            separatorBefore = i == 0) { onMute(option) }
    } + record.mutes.mapIndexed { i, option ->
        JAction(res.getString(R.string.inbox_mute_option, res.getString(option.policyTitle), option.shortName), PhIcons.Target,
            separatorBefore = i == 0) { onRule(option) }
    }

/** The rule sheet's two buttons that send something. */
enum class InboxPolicySend { Save, Remove }

/** The rule sheet's state (the web's PolicyDialog): the rule the hub has, prefilled; the choice; what is being sent. */
@Stable
class InboxPolicyEditor(private val repository: InboxRepository, val target: InboxPolicyTarget) {
    var policy by mutableStateOf(target.policy); private set
    var loaded by mutableStateOf(target.policy != null); private set
    var tier by mutableStateOf(target.policy?.tier ?: InboxPolicyTier.None)
    var twin by mutableStateOf(target.policy?.twin ?: InboxPolicyTwin.None)
    var sending by mutableStateOf<InboxPolicySend?>(null); private set
    val busy: Boolean get() = sending != null

    /** Save needs a choice: a new rule one of its two parts (an existing one may clear both, which removes it). */
    val canSave: Boolean get() = loaded && (policy != null || tier != InboxPolicyTier.None || twin != InboxPolicyTwin.None)

    /** Prefills from the hub's list when opened from an item; an older hub without the list starts from nothing. */
    suspend fun load() {
        if (loaded) return
        val same = try { repository.inboxPolicies(target.record).firstOrNull { it.covers(target.source, target.option) } }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { null }
        policy = same
        tier = same?.tier ?: InboxPolicyTier.None
        twin = same?.twin ?: InboxPolicyTwin.None
        loaded = true
    }

    /** Sends the choice ([InboxPolicySend.Remove]: neither part); true when that removed the rule. Throws what the hub said. */
    suspend fun send(what: InboxPolicySend): Boolean {
        val (tier, twin) = if (what == InboxPolicySend.Remove) InboxPolicyTier.None to InboxPolicyTwin.None else tier to twin
        sending = what
        try { policy = repository.savePolicy(target.copy(policy = policy), tier, twin) }
        finally { sending = null }
        return tier == InboxPolicyTier.None && twin == InboxPolicyTwin.None
    }
}

/** "Rule for this conversation / sender / repository": where its items go and what the AI twin does with them. */
@Composable
internal fun InboxPolicySheet(repository: InboxRepository, target: InboxPolicyTarget, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val palette = PersonaTheme.palette
    val editor = remember(repository, target) { InboxPolicyEditor(repository, target) }
    // The sheet stays open on a failure, over the page where a notice would show: it says so itself.
    var failure by remember(editor) { mutableStateOf<String?>(null) }
    LaunchedEffect(editor) { editor.load() }
    fun send(what: InboxPolicySend) {
        failure = null
        scope.launch {
            try {
                val removed = editor.send(what)
                feedback?.notice(res.getString(if (removed) R.string.inbox_policy_removed else R.string.inbox_policy_saved, target.option.name))
                onDismiss()
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { failure = res.getString(R.string.inbox_policy_failed, failureReasonText(e).resolve(res)) }
        }
    }
    JSheet(onDismiss, title = stringResource(R.string.inbox_policy_title, target.option.shortName), dismissible = !editor.busy, scrollable = true) {
        if (!editor.loaded) { JLoading(); return@JSheet }
        val kind = stringResource(muteTagName(target.option.tag))
        Text(stringResource(R.string.inbox_policy_intro, "${target.sourceName} · $kind"), style = PersonaTheme.type.callout, color = palette.ink2)
        JSectionHeader(stringResource(R.string.inbox_policy_tier))
        InboxPolicyTier.entries.forEachIndexed { i, t ->
            JGroupItem(GroupPosition.of(i, InboxPolicyTier.entries.size)) {
                PolicyChoice(stringResource(t.title), editor.tier == t, enabled = !editor.busy) { editor.tier = t }
            }
        }
        JSectionHeader(stringResource(R.string.inbox_policy_twin))
        InboxPolicyTwin.entries.forEachIndexed { i, t ->
            JGroupItem(GroupPosition.of(i, InboxPolicyTwin.entries.size)) {
                PolicyChoice(stringResource(t.title), editor.twin == t, enabled = !editor.busy) { editor.twin = t }
            }
        }
        Spacer(Modifier.height(Tokens.Space.s))
        Text(stringResource(R.string.inbox_policy_twin_note), Modifier.padding(horizontal = rowPadding),
            style = PersonaTheme.type.caption, color = palette.ink3)
        failure?.let { JNotice(it, Modifier.padding(top = Tokens.Space.l), status = Status.NeedsYou) }
        Spacer(Modifier.height(Tokens.Space.section))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            if (editor.policy != null) JButton(stringResource(R.string.inbox_policy_remove), { send(InboxPolicySend.Remove) },
                kind = JButtonKind.Quiet, size = JButtonSize.Medium, enabled = !editor.busy, loading = editor.sending == InboxPolicySend.Remove)
            Spacer(Modifier.weight(1f))
            JButton(stringResource(R.string.inbox_policy_cancel), onDismiss, kind = JButtonKind.Secondary, size = JButtonSize.Medium, enabled = !editor.busy)
            JButton(stringResource(R.string.inbox_policy_save), { send(InboxPolicySend.Save) }, size = JButtonSize.Medium,
                enabled = editor.canSave && !editor.busy, loading = editor.sending == InboxPolicySend.Save)
        }
    }
}

/** One choice of a group: its words and a radio, the whole row tapping it. */
@Composable
private fun PolicyChoice(title: String, selected: Boolean, enabled: Boolean, onSelect: () -> Unit) =
    JRow(title, onClick = onSelect, chevron = false, enabled = enabled, role = Role.RadioButton, trailing = { JRadio(selected, null) })

/**
 * The "Rules" list's state: every source's rules, newest first, and with several managers each one's (read and removed
 * with it named); Remove takes one off at once and puts it back on failure.
 */
@Stable
class InboxRulesModel(private val repository: InboxRepository) {
    var rules by mutableStateOf<List<InboxPolicy>?>(null); private set
    /** Why the first read failed; a later one leaves the shown list and throws. */
    var failed by mutableStateOf<Throwable?>(null); private set

    /** Nothing to show: no rules. */
    val isEmpty: Boolean get() = rules?.isEmpty() == true

    private var reads = 0
    private var shownOf: String? = null

    /** Reads [manager]'s list (null: one manager, none named); only the newest read lands. */
    suspend fun load(manager: String? = null) {
        val read = ++reads
        failed = null
        // Another assistant's rules never stay under this one's name.
        if (manager != shownOf) { rules = null; shownOf = manager }
        try {
            val list = repository.inboxPolicies(manager = manager)
            if (read != reads) return
            rules = if (manager == null) list else list.map { it.copy(manager = manager) }
        }
        catch (e: CancellationException) { throw e }
        catch (e: Exception) {
            if (read != reads) return
            if (rules == null) failed = e else throw e
        }
    }

    /** Removes [policy] (on its manager) at once; throws what the hub said, with the row back. */
    suspend fun remove(policy: InboxPolicy) {
        val same = { p: InboxPolicy -> p.id == policy.id && p.manager == policy.manager }
        rules = rules?.filterNot(same)
        try {
            repository.removePolicy(policy.id, policy.manager)
            // A reload read before the removal reached the hub may have brought it back.
            rules = rules?.filterNot(same)
        }
        catch (e: Exception) {
            // Back only this one: another remove that went through meanwhile stays gone.
            rules = rules?.let { list -> (list.filterNot(same) + policy).newestFirst() }
            throw e
        }
    }
}

/** "Rules" (the Inbox's ⋯ menu): each rule's name, "source · kind · what it does", Edit and Remove. */
@Composable
internal fun InboxRulesSheet(repository: InboxRepository, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val owner = currentAssistant().listOwner()
    val model = remember(repository) { InboxRulesModel(repository) }
    var editing by remember { mutableStateOf<InboxPolicyTarget?>(null) }
    var failure by remember { mutableStateOf<String?>(null) }
    fun load() {
        scope.launch {
            try { model.load(owner.id) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { feedback?.failure(e) }
        }
    }
    fun remove(p: InboxPolicy) {
        failure = null
        scope.launch {
            try {
                model.remove(p)
                feedback?.notice(res.getString(R.string.inbox_policy_removed, p.name))
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { failure = res.getString(R.string.inbox_rules_remove_failed, failureReasonText(e).resolve(res)) }
        }
    }
    LaunchedEffect(model, owner) { load() } // another assistant, or this one back online, reads it again
    // The rule's own sheet takes the list's place; the list reads again when it closes.
    editing?.let { target ->
        InboxPolicySheet(repository, target, onDismiss = { editing = null; load() })
        return
    }
    JSheet(onDismiss, title = stringResource(R.string.inbox_rules_title), scrollable = true) {
        val list = model.rules
        val failed = model.failed
        @Composable fun rule(p: InboxPolicy) =
            JRow(p.name, subtitle = (listOf(p.sourceName ?: p.source, stringResource(muteTagName(p.tag))) + p.summary.map { stringResource(it) })
                .filter { it.isNotBlank() }.joinToString(" · "),
                leadingIcon = PhIcons.Target,
                trailing = {
                    Row {
                        JTextButton(stringResource(R.string.inbox_policy_edit), { editing = InboxPolicyTarget.of(p) })
                        JTextButton(stringResource(R.string.inbox_policy_remove), { remove(p) })
                    }
                })
        when {
            failed != null -> JNotice(if (owner.offline) stringResource(R.string.inbox_rules_offline)
                else stringResource(R.string.inbox_rules_failed, failureReasonText(failed).text()), status = Status.NeedsYou,
                actionLabel = stringResource(R.string.inbox_retry), onAction = ::load)
            list == null -> JLoading()
            model.isEmpty -> JEmptyState(stringResource(R.string.inbox_rules_empty_title), stringResource(R.string.inbox_rules_empty_text), icon = PhIcons.Target)
            else -> JGroup(rows = list.map { p -> { rule(p) } })
        }
        failure?.let { JNotice(it, Modifier.padding(top = Tokens.Space.l), status = Status.NeedsYou) }
    }
}
