package persona.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
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
import androidx.compose.ui.semantics.Role
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.ConversationCategories
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JChip
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.infoCategoryLabel
import persona.core.designsystem.operationFeedback

/** The chip's words: the first category, and how many more ("Money +1"). */
internal fun categoriesChipText(categories: ConversationCategories, label: (String) -> String): String? {
    val first = categories.categories.firstOrNull() ?: return null
    val more = categories.categories.size - 1
    return if (more > 0) "${label(first)} +$more" else label(first)
}

/**
 * A space conversation's information categories (#13500, the web's `ConversationCategories.tsx`): one neutral chip in the
 * top bar (a phone's bar holds no more), opening a sheet where one the space sets stays with a lock, any other can be
 * removed, and a removed one put back. Only the user loosens. Nothing shows when they cannot be read or there are none.
 */
@Composable
internal fun ConversationCategoriesChip(session: SessionKey, api: AssistantManagement) {
    val resources = LocalResources.current
    val feedback = LocalAppFeedback.current
    val scope = rememberCoroutineScope()
    var categories by remember(session, api) { mutableStateOf<ConversationCategories?>(null) }
    var open by remember(session) { mutableStateOf(false) }
    var busy by remember(session) { mutableStateOf<String?>(null) }
    LaunchedEffect(session, api) {
        categories = try { api.conversationCategories(session) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { null }
    }
    val shown = categories ?: return
    if (shown.categories.isEmpty() && shown.removed.isEmpty()) return
    fun change(category: String, add: Boolean) {
        if (busy != null) return
        busy = category
        scope.launch {
            val name = infoCategoryLabel(resources, category)
            try {
                categories = if (add) api.changeConversationCategories(session, add = listOf(category))
                    else api.changeConversationCategories(session, remove = listOf(category))
                feedback?.notice(resources.getString(if (add) R.string.assistant_spaces_category_added else R.string.assistant_spaces_category_removed, name))
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                feedback?.notice(resources.getString(R.string.assistant_spaces_category_failed, operationFeedback(error).text.resolve(resources)))
            } finally {
                busy = null
            }
        }
    }
    val text = categoriesChipText(shown) { infoCategoryLabel(resources, it) } ?: stringResource(R.string.assistant_spaces_categories)
    JChip(text, Modifier.testTag("conversation-categories"), onClick = { open = true }, role = Role.Button)
    if (open) JSheet({ open = false }, title = stringResource(R.string.assistant_spaces_categories), scrollable = true) {
        val palette = PersonaTheme.palette
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(stringResource(R.string.assistant_spaces_categories_body), style = PersonaTheme.type.callout, color = palette.ink2)
            shown.categories.forEachIndexed { index, category ->
                val name = infoCategoryLabel(category)
                val kept = category in shown.floor
                JGroupItem(GroupPosition.of(index, shown.categories.size)) {
                    JRow(name, subtitle = stringResource(R.string.assistant_spaces_category_kept).takeIf { kept },
                        trailing = {
                            if (kept) JIcon(PersonaIcons.locked, null, size = Tokens.Size.markS, tint = palette.ink3, decorative = true)
                            else JIconButton(PersonaIcons.close, stringResource(R.string.assistant_spaces_category_remove, name),
                                { change(category, add = false) }, Modifier.testTag("category-remove-$category"), enabled = busy == null)
                        })
                }
            }
            if (shown.removed.isNotEmpty()) {
                JSectionHeader(stringResource(R.string.assistant_spaces_category_restore))
                shown.removed.forEachIndexed { index, category ->
                    JGroupItem(GroupPosition.of(index, shown.removed.size)) {
                        JRow(infoCategoryLabel(category), trailing = {
                            JTextButton(stringResource(R.string.assistant_spaces_category_restore), { change(category, add = true) },
                                Modifier.testTag("category-restore-$category"), enabled = busy == null)
                        })
                    }
                }
            }
        }
    }
}
