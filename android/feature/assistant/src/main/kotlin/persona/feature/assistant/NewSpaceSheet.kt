package persona.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
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
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.Track
import persona.core.contracts.api.TrackCreate
import persona.core.contracts.api.TrackKind
import persona.core.contracts.api.Tracks
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JChip
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JNotice
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTextField
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SpaceIcons
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.infoCategoryLabel
import persona.core.designsystem.operationFeedback

/** What the new space form holds, apart from the screen: its rules are tested on their own. */
internal class NewSpaceDraft(private val taken: () -> Collection<String>) {
    var title by mutableStateOf("")
    var description by mutableStateOf("")
    var icon by mutableStateOf(SpaceIcons.choices.first())
    /** None by default: the user picks what the space holds. */
    var categories by mutableStateOf(emptyList<String>())
    var kinds by mutableStateOf(emptyList<TrackKind>())
        private set
    var kind by mutableStateOf(Tracks.DEFAULT_KIND)
    private var typedSlug by mutableStateOf<String?>(null)

    /** The address: the one the user wrote, else one suggested from the name; worked out once per change, not per read. */
    val slug: String by derivedStateOf {
        typedSlug ?: title.trim().takeIf { it.isNotEmpty() }?.let { Tracks.suggestSlug(it, taken()) }.orEmpty()
    }
    val slugTaken: Boolean get() = slug.isNotEmpty() && slug in taken()
    val slugOk: Boolean get() = Tracks.formSlug(slug) && !slugTaken
    val ready: Boolean get() = title.isNotBlank() && slugOk

    fun typeSlug(value: String) { typedSlug = value.lowercase() }
    fun toggle(category: String) { categories = if (category in categories) categories - category else categories + category }

    /** The kinds this assistant offers; the default kind may be off or missing there, so the form starts on one it offers. */
    fun kindsLoaded(loaded: List<TrackKind>) {
        kinds = Tracks.spaceKinds(loaded)
        kind = Tracks.reconcileKind(kind, kinds)
    }

    fun create() = TrackCreate(slug = slug, kind = kind, title = title.trim(), icon = icon, description = description.trim(),
        defaultCategories = categories)
}

/**
 * New space (#13500, the web's `NewSpace.tsx`): a name, one line about it, an icon, which kinds of personal information it
 * holds, and (under More options) its type and address. Created, it opens ([onCreated]).
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun NewSpaceSheet(api: AssistantManagement, taken: List<String>, onDismiss: () -> Unit, onCreated: (Track) -> Unit) {
    val resources = LocalResources.current
    val scope = rememberCoroutineScope()
    val latestTaken by androidx.compose.runtime.rememberUpdatedState(taken)
    val draft = remember { NewSpaceDraft { latestTaken } }
    var more by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var failure by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(api) {
        try { draft.kindsLoaded(api.trackKinds()) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* The default kind stays; the hub says if it does not take it. */ }
    }
    fun create() {
        if (!draft.ready || busy) return
        busy = true
        failure = null
        scope.launch {
            try { onCreated(api.createTrack(draft.create())) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                failure = resources.getString(R.string.assistant_spaces_create_failed, operationFeedback(error).text.resolve(resources))
            } finally { busy = false }
        }
    }
    val palette = PersonaTheme.palette
    JSheet(onDismiss, title = stringResource(R.string.assistant_spaces_new_title), scrollable = true, dismissible = !busy) {
        Column(Modifier.fillMaxWidth().testTag("new-space"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Text(stringResource(R.string.assistant_spaces_new_intro), style = PersonaTheme.type.callout, color = palette.ink2)
            JTextField(draft.title, { draft.title = it.take(80) }, Modifier.fillMaxWidth().testTag("new-space-title"),
                label = stringResource(R.string.assistant_spaces_field_title),
                placeholder = stringResource(R.string.assistant_spaces_field_title_placeholder), singleLine = true, enabled = !busy)
            JTextField(draft.description, { draft.description = it.take(200) }, Modifier.fillMaxWidth(),
                label = stringResource(R.string.assistant_spaces_field_description),
                placeholder = stringResource(R.string.assistant_spaces_field_description_placeholder), singleLine = true, enabled = !busy)
            FieldLabel(stringResource(R.string.assistant_spaces_field_icon))
            FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                SpaceIcons.choices.forEach { token ->
                    JIconButton(SpaceIcons.of(token), stringResource(spaceIconLabel(token)), { draft.icon = token },
                        Modifier.testTag("new-space-icon-$token"), enabled = !busy, raised = true, selected = draft.icon == token)
                }
            }
            FieldLabel(stringResource(R.string.assistant_spaces_field_categories))
            FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Tracks.INFO_CATEGORIES.forEach { category ->
                    val on = category in draft.categories
                    JChip(infoCategoryLabel(category), Modifier.testTag("new-space-category-$category"), selected = on,
                        onClick = { draft.toggle(category) }, icon = PersonaIcons.done.takeIf { on })
                }
            }
            Text(stringResource(R.string.assistant_spaces_field_categories_hint), style = PersonaTheme.type.caption, color = palette.ink3)
            JButton(stringResource(R.string.assistant_spaces_more_options), { more = !more }, kind = JButtonKind.Quiet,
                size = JButtonSize.Small, icon = if (more) PersonaIcons.collapse else PersonaIcons.expand)
            if (more) {
                FieldLabel(stringResource(R.string.assistant_spaces_field_kind))
                val kinds = draft.kinds.ifEmpty { listOf(TrackKind(slug = Tracks.DEFAULT_KIND)) }
                FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    kinds.forEach { kind ->
                        JChip(if (kind.slug == Tracks.DEFAULT_KIND) stringResource(R.string.assistant_spaces_kind_space) else kind.title.ifBlank { kind.slug },
                            Modifier.testTag("new-space-kind-${kind.slug}"), selected = draft.kind == kind.slug,
                            onClick = { draft.kind = kind.slug }, role = Role.RadioButton)
                    }
                }
                JTextField(draft.slug, draft::typeSlug, Modifier.fillMaxWidth().testTag("new-space-slug"),
                    label = stringResource(R.string.assistant_spaces_field_slug),
                    supportingText = stringResource(if (draft.slugTaken) R.string.assistant_spaces_field_slug_taken else R.string.assistant_spaces_field_slug_hint),
                    isError = draft.slug.isNotEmpty() && !draft.slugOk, singleLine = true, enabled = !busy, autofill = false)
            }
            failure?.let { JNotice(it, status = Status.NeedsYou) }
            JButton(stringResource(R.string.assistant_spaces_create), ::create, Modifier.fillMaxWidth().testTag("new-space-create"),
                kind = JButtonKind.Primary, size = JButtonSize.Large, loading = busy, enabled = draft.ready && !busy)
        }
    }
}

@Composable
private fun FieldLabel(text: String) =
    Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)

private fun spaceIconLabel(token: String): Int = when (token) {
    "book" -> R.string.assistant_spaces_icon_book
    "wallet" -> R.string.assistant_spaces_icon_wallet
    "home" -> R.string.assistant_spaces_icon_home
    "leaf" -> R.string.assistant_spaces_icon_leaf
    "car" -> R.string.assistant_spaces_icon_car
    "calendar" -> R.string.assistant_spaces_icon_calendar
    "star" -> R.string.assistant_spaces_icon_star
    "flag" -> R.string.assistant_spaces_icon_flag
    "music" -> R.string.assistant_spaces_icon_music
    "camera" -> R.string.assistant_spaces_icon_camera
    "chart" -> R.string.assistant_spaces_icon_chart
    "target" -> R.string.assistant_spaces_icon_target
    "sun" -> R.string.assistant_spaces_icon_sun
    else -> R.string.assistant_spaces_icon_layers
}
