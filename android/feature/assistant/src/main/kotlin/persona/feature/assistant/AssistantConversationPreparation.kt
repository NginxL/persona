package persona.feature.assistant

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackAction
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.JToastState
import persona.core.designsystem.LocalHubFeatures

/** Conversation grouping, arranging and search preparation, kept outside AssistantScreen's oversized lambda. */
internal class AssistantConversationPreparation(
    val groups: ConversationGroups,
    val beginArrange: (() -> Unit)?,
    val knownChats: List<persona.core.contracts.Session>,
    val tagChoices: List<String>,
    val foundChats: List<FoundChat>?,
    val searchPartial: Boolean,
)

@Composable
internal fun rememberAssistantConversationPreparation(
    snapshot: AssistantSnapshot,
    pageManagement: AssistantManagement?,
    demo: Boolean,
    repository: AssistantRepository,
    conversationArranger: ConversationArranger?,
    conversationArrangeUi: ConversationArrangeUi,
    conversationWriteScope: CoroutineScope,
    history: ConversationHistory,
    ended: Set<SessionKey>,
    archived: Set<SessionKey>,
    query: String,
    onQuery: (String) -> Unit,
    words: ConversationWords,
    resources: android.content.res.Resources,
    feedback: AppFeedback?,
    toast: JToastState,
    managerKey: String,
): AssistantConversationPreparation {
    val orderedSnapshot = if (conversationArrangeUi.arranging || conversationArrangeUi.pending > 0) {
        snapshot.copy(conversationOrder = conversationArrangeUi.order)
    } else snapshot
    val groups = remember(orderedSnapshot, query, ended, archived, words, history.titles, history.tagEdits, history.activeTag) {
        conversationGroups(orderedSnapshot.copy(conversations = history.tagged(orderedSnapshot.conversations.filter { it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        })), query, words, activeTag = history.activeTag)
    }
    val latestArrangeActivityIds = rememberUpdatedState(groups.others.map { it.sessionId })
    val beginArrange: (() -> Unit)? = conversationArranger?.let { controller ->
        {
            onQuery("")
            history.activeTag?.let(history::selectTag)
            conversationWriteScope.launch {
                controller.start(if (conversationArrangeUi.pending > 0) conversationArrangeUi.order else snapshot.conversationOrder)
            }
        }
    }
    LaunchedEffect(conversationArranger, resources, feedback) {
        val controller = conversationArranger ?: return@LaunchedEffect
        controller.events.collect { event ->
            when (event) {
                is ConversationArrangeEvent.Failed -> {
                    if (feedback?.failure(event.error) != true) toast.show(FeedbackNotice(
                        resources.getString(R.string.assistant_arrange_save_failed),
                        style = FeedbackStyle.Error,
                    ))
                }
                is ConversationArrangeEvent.Reset -> {
                    val message = resources.getString(R.string.assistant_arrange_reset_done)
                    val undo = FeedbackAction(resources.getString(R.string.assistant_arrange_undo)) {
                        conversationWriteScope.launch { controller.undoReset(event.revision, latestArrangeActivityIds.value) }
                    }
                    if (feedback?.notice(message, undo) != true) {
                        toast.show(FeedbackNotice(message, undo))
                    }
                }
            }
        }
    }
    val searchOn = pageManagement != null && !demo && AssistantSearchFeature in LocalHubFeatures.current
    val knownChats = remember(snapshot.conversations, ended, archived, history.titles, history.tagEdits, history.archived, history.restoring) {
        val listed = snapshot.conversations.filter { it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        }
        history.tagged((listed + history.shownArchived(openKeys(listed))).distinctBy { it.sessionId })
    }
    val tagChoices = remember(knownChats) { inUseConversationTags(knownChats) }
    val (foundChats, searchPartial) = rememberFoundChats(
        query, remember(groups) { (groups.directions + groups.others).distinctBy { it.sessionId } },
        knownChats.withConversationTag(history.activeTag), searchOn, pageManagement, managerKey,
        activeTag = history.activeTag)
    return AssistantConversationPreparation(groups, beginArrange, knownChats, tagChoices, foundChats, searchPartial)
}
