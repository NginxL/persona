package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.AgentMarkSpec
import persona.core.designsystem.AgentRow
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRow
import persona.core.designsystem.JSearchField
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSheet
import persona.core.designsystem.JSpinner
import persona.core.designsystem.LocalPersonaName
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.RowFact
import persona.core.designsystem.Tokens
import persona.core.designsystem.agentMark
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.operationFeedback
import persona.core.designsystem.resolve

internal const val FORWARD_SHEET_TAG = "forward-sheet"

/**
 * Forward to (#3901; iOS `ForwardSheet`, web `ForwardDialog`): the message being forwarded, then Persona (the main chat,
 * unless that is where it comes from) and New agent…, the recent targets, and each computer's live Agents, with search, which
 * also finds ended Agents and Persona's other conversations ([ForwardTargets]). The lists are the sessions the app holds,
 * read once as the sheet opens. A pick reads the whole message when the chat holds it cut ([complete]), remembers the target
 * and hands both to [onPicked] once the sheet has gone; nothing is sent until the user sends there.
 */
@Composable
internal fun ForwardSheet(
    draft: ForwardDraft,
    forwarding: ChatForwarding,
    complete: suspend () -> ChatLinkContext,
    onDismiss: () -> Unit,
    onPicked: (ForwardTarget, ChatLinkContext) -> Unit,
) {
    val resources = LocalResources.current
    val scope = rememberCoroutineScope()
    val from = draft.source.key
    // What the app holds as the sheet opens (iOS reads the list once): rows do not move under the finger while it is open.
    val held = remember(draft) {
        val persona = forwarding.personaGroupOf(from)
        ForwardHeld(forwarding.agents.value, forwarding.assistant.value.conversations.filterNot { it.archived }, forwarding.machines.value,
            forwarding.assistant.value.mainSession?.takeIf { persona }, persona)
    }
    val memory = remember(draft) { forwarding.memory(from) }
    var recents by remember(draft) { mutableStateOf(emptyList<SessionKey>()) }
    LaunchedEffect(memory) {
        recents = try { memory?.targets()?.let(ForwardMemory::sessions).orEmpty() }
            catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { emptyList() }
    }
    var query by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf<ForwardTarget?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    val preview = remember(draft, resources) { ForwardMessage.ref(draft.raw, draft.source, resources) }
    val online: (String) -> Boolean = { id -> held.machines.firstOrNull { it.id == id }?.online == true }
    val sections = remember(held, recents, query) {
        ForwardTargets.sections(held.agents, held.conversations, from, recents, held.main, held.machines, query, online,
            forwarding.conversationTitle)
    }
    val machineName: (String) -> String = { id -> held.machines.firstOrNull { it.id == id }?.displayName ?: id }

    fun pick(target: ForwardTarget) {
        if (busy != null) return
        busy = target
        error = null
        scope.launch {
            val ref = try { complete() } catch (cancelled: CancellationException) { throw cancelled }
            catch (e: Exception) {
                busy = null
                error = resources.getString(R.string.chat_forward_failed, operationFeedback(e).text.resolve(resources))
                return@launch
            }
            target.stored?.let { stored ->
                try { memory?.remember(stored) } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
            }
            onDismiss()
            onPicked(target, ref)
        }
    }

    JSheet(onDismiss, Modifier.testTag(FORWARD_SHEET_TAG), title = stringResource(R.string.chat_forward_title), dismissible = busy == null) {
        JSearchField(query, { query = it }, stringResource(R.string.chat_forward_search), Modifier.fillMaxWidth())
        LazyColumn(Modifier.fillMaxWidth().weight(1f, fill = false)) {
            item("preview") {
                val label = stringResource(R.string.chat_forward_preview)
                Column(Modifier.padding(top = Tokens.Space.m, bottom = Tokens.Space.s).semantics { contentDescription = label }) {
                    ChatContextCard(preview)
                }
            }
            if (query.isBlank()) {
                val fixed = buildList {
                    // Persona is offered on its own hub group only (a forward stays on its hub), and never to itself.
                    if (held.persona && from != held.main) add(ForwardTarget.Persona)
                    add(ForwardTarget.NewAgent)
                }
                itemsIndexed(fixed, key = { _, t -> "fixed:$t" }) { index, target ->
                    JGroupItem(GroupPosition.of(index, fixed.size)) {
                        val persona = target == ForwardTarget.Persona
                        JRow(if (persona) LocalPersonaName.current else stringResource(R.string.chat_forward_new_agent),
                            Modifier.testTag(if (persona) "forward-persona" else "forward-new-agent"),
                            subtitle = stringResource(if (persona) R.string.chat_forward_main_chat else R.string.chat_forward_new_agent_detail),
                            leadingIcon = if (persona) PersonaIcons.persona else PersonaIcons.add, subtitleLines = 1,
                            trailing = if (busy == target) ({ JSpinner() }) else null, chevron = false,
                            enabled = busy == null || busy == target, onClick = { pick(target) })
                    }
                }
            }
            sections.forEach { section ->
                when (section.kind) {
                    ForwardTargets.Kind.Recent -> item("head:${section.id}") { JSectionHeader(stringResource(R.string.chat_forward_recent)) }
                    ForwardTargets.Kind.Machine -> item("head:${section.id}") { JSectionHeader(section.title) }
                    ForwardTargets.Kind.Results -> Unit
                }
                itemsIndexed(section.sessions, key = { _, s -> "${section.id}/${s.key.path}" }) { index, s ->
                    val target = ForwardTarget.Chat(s.key)
                    TargetRow(s, GroupPosition.of(index, section.sessions.size),
                        machine = if (section.kind == ForwardTargets.Kind.Machine || s.assistant) null else machineName(s.machineId),
                        title = if (s.assistant) forwarding.conversationTitle(s) else s.displayTitle,
                        busy = busy == target, enabled = busy == null || busy == target) { pick(target) }
                }
            }
            if (query.isNotBlank() && sections.isEmpty()) item("none") {
                Text(stringResource(R.string.chat_forward_no_match), Modifier.fillMaxWidth().padding(vertical = Tokens.Space.xxl),
                    style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, textAlign = TextAlign.Center)
            }
            error?.let { message ->
                item("error") {
                    Text(message, Modifier.padding(vertical = Tokens.Space.s), style = PersonaTheme.type.caption,
                        color = PersonaTheme.palette.danger.text)
                }
            }
        }
    }
}

/** What the sheet lists, as the app held it when the sheet opened. */
private data class ForwardHeld(
    val agents: List<Session>, val conversations: List<Session>, val machines: List<persona.core.contracts.Machine>, val main: SessionKey?,
    /** Persona's page shows this chat's hub group. */
    val persona: Boolean,
)

/**
 * A chat to forward to, as the Agents list draws a session outside its tree: the Agent's mark and state, its title, its
 * project, and its computer where rows mix computers ([machine]); Persona's conversations carry Persona's mark and name
 * where an Agent's state and project are, as iOS and the web show them.
 */
@Composable
private fun TargetRow(s: Session, position: GroupPosition, machine: String?, title: String, busy: Boolean, enabled: Boolean,
    onClick: () -> Unit) {
    val status = s.agentState.designStatus
    val persona = LocalPersonaName.current
    AgentRow(title, status, onClick = { if (enabled) onClick() }, Modifier.testTag("forward-session-${s.sessionId}"),
        position = position, statusText = if (s.assistant) null else status.localizedLabel,
        preview = if (s.assistant) persona else s.spaceName?.takeIf { it.isNotBlank() } ?: s.folderName,
        agent = s.agentLabel.takeIf { !s.assistant && (s.isCodex || s.agent == "traex") },
        facts = machine?.let { listOf(RowFact(it)) }.orEmpty(),
        agentMark = if (s.assistant) AgentMarkSpec(null, persona, null) else s.agentMark, muted = !enabled,
        end = if (busy) ({ JSpinner() }) else null)
}
