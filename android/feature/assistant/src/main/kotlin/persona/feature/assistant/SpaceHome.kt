package persona.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.isSpecified
import kotlinx.coroutines.CancellationException
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.SpaceMd
import persona.core.contracts.api.SpaceMdText
import persona.core.contracts.api.Track
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JCard
import persona.core.designsystem.JDivider
import persona.core.designsystem.JEmptyState
import persona.core.designsystem.JIcon
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JTag
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SpaceIcons
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.infoCategoryLabel
import persona.core.designsystem.pagePadding
import persona.feature.chat.DocumentMarkdown

/** How many lines of "About this space" show before Show more (the web's six). */
private const val AboutFoldLines = 6

/**
 * A domain space's home (#13500, the web's `views/spaces/Space.tsx` SpaceHome): its icon, name and one line about it, a
 * plain line on how what is here is used, its conversations (newest first), About this space (what the user wrote in
 * SPACE.md as formatted text, Persona's kept blocks left out, and where things stand), and the personal information it
 * holds. The composer under it starts a conversation in the space, as the health space's page does.
 */
@Composable
internal fun SpaceHome(
    track: Track?, slug: String, chats: List<Session>?, loadFailed: Boolean,
    api: AssistantManagement?, latestMd: SpaceMd?, contentPadding: PaddingValues,
    onOpen: (SessionKey) -> Unit, onCopy: (SessionKey) -> Unit, onEdit: (() -> Unit)?,
) {
    val palette = PersonaTheme.palette
    val name = track?.name ?: slug
    val icon = SpaceIcons.of(track?.iconToken)
    val history = remember(chats) { HealthEntry.history(chats.orEmpty()) }
    var readMd by remember(slug, api) { mutableStateOf<SpaceMd?>(null) }
    var mdFailed by remember(slug, api) { mutableStateOf(false) }
    LaunchedEffect(slug, api) {
        val source = api ?: return@LaunchedEffect
        try { readMd = source.spaceMd(slug) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { mdFailed = true }
    }
    val md = latestMd ?: readMd
    LazyColumn(Modifier.fillMaxSize().testTag("space-home"), contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
        top = contentPadding.calculateTopPadding() + Tokens.Space.l, bottom = contentPadding.calculateBottomPadding() + Tokens.Space.xl)) {
        item(key = "space-lead") {
            Column(Modifier.padding(vertical = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                JIcon(icon, null, size = Tokens.Size.icon, tint = palette.accent, decorative = true)
                Text(name, style = PersonaTheme.type.largeTitle, color = palette.ink)
                track?.description?.takeIf { it.isNotBlank() }?.let {
                    Text(it, style = PersonaTheme.type.voice, color = palette.ink2)
                }
                Row(Modifier.testTag("space-privacy"), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    JIcon(PersonaIcons.locked, null, Modifier.padding(top = Tokens.Space.xxs), size = Tokens.Size.markS,
                        tint = palette.ink3, decorative = true)
                    Text(stringResource(R.string.assistant_spaces_privacy_line), style = PersonaTheme.type.caption, color = palette.ink3)
                }
            }
        }
        item(key = "space-history-header") {
            JSectionHeader(stringResource(R.string.assistant_spaces_recent), count = history.size.takeIf { it > 0 })
        }
        when {
            chats == null && loadFailed -> item(key = "space-history-failed") {
                JEmptyState(stringResource(R.string.assistant_spaces_load_failed), "", icon = icon)
            }
            chats == null -> item(key = "space-history-loading") {
                Text(stringResource(R.string.assistant_spaces_loading), Modifier.testTag("space-history-loading"),
                    style = PersonaTheme.type.callout, color = palette.ink3)
            }
            history.isEmpty() -> item(key = "space-history-empty") {
                JEmptyState(stringResource(R.string.assistant_spaces_empty_title), stringResource(R.string.assistant_spaces_empty_body), icon = icon)
            }
            else -> itemsIndexed(history, key = { _, session -> "space/" + session.key.path }) { index, session ->
                ConversationRow(session, selected = false, position = GroupPosition.of(index, history.size),
                    onOpen = { onOpen(session.key) }, onEnd = null, onCopy = { onCopy(session.key) })
            }
        }
        item(key = "space-md-header") { JSectionHeader(stringResource(R.string.assistant_spaces_md_section)) }
        item(key = "space-md") {
            JCard(Modifier.testTag("space-md")) {
                if (md == null) {
                    Text(stringResource(if (mdFailed) R.string.assistant_spaces_md_unavailable else R.string.assistant_spaces_md_loading),
                        style = PersonaTheme.type.callout, color = palette.ink3)
                } else {
                    val written = remember(md, name) { SpaceMdText.body(md.content, name) }
                    if (written.isNotBlank()) FoldedMarkdown(written, "space-md/$slug")
                    else Text(stringResource(R.string.assistant_spaces_md_empty), style = PersonaTheme.type.callout, color = palette.ink3)
                    md.state.trim().takeIf { it.isNotEmpty() }?.let { state ->
                        JDivider(Modifier.padding(vertical = Tokens.Space.m))
                        Text(stringResource(R.string.assistant_spaces_md_state) + " " + state,
                            style = PersonaTheme.type.caption, color = palette.ink3)
                    }
                    if (onEdit != null) Box(Modifier.fillMaxWidth().padding(top = Tokens.Space.s)) {
                        JButton(stringResource(R.string.assistant_spaces_md_edit), onEdit, Modifier.align(Alignment.CenterEnd),
                            kind = JButtonKind.Quiet, size = JButtonSize.Small, icon = PersonaIcons.edit)
                    }
                }
            }
        }
        item(key = "space-categories-header") { JSectionHeader(stringResource(R.string.assistant_spaces_categories)) }
        item(key = "space-categories") { SpaceCategoriesCard(track?.defaultCategories.orEmpty()) }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun SpaceCategoriesCard(categories: List<String>) {
    val palette = PersonaTheme.palette
    JCard {
        if (categories.isEmpty()) Text(stringResource(R.string.assistant_spaces_categories_none), style = PersonaTheme.type.callout, color = palette.ink3)
        else FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            categories.forEach { JTag(infoCategoryLabel(it)) }
        }
        Text(stringResource(R.string.assistant_spaces_categories_body), Modifier.padding(top = Tokens.Space.s),
            style = PersonaTheme.type.caption, color = palette.ink3)
    }
}

/** Formatted text cut at [AboutFoldLines] lines, with Show more / Show less once it runs longer than that. */
@Composable
private fun FoldedMarkdown(text: String, id: String) {
    val style = PersonaTheme.type.callout
    val lineHeight = foldLineHeight(style)
    var open by rememberSaveable(id) { mutableStateOf(false) }
    var full by remember(text) { mutableIntStateOf(0) }
    var shown by remember(text) { mutableIntStateOf(0) }
    val long = full > shown + 1
    Column {
        Box(if (open) Modifier else Modifier.heightIn(max = lineHeight * AboutFoldLines).clipToBounds().onSizeChanged { shown = it.height }) {
            DocumentMarkdown(text, id,
                Modifier.wrapContentHeight(align = Alignment.Top, unbounded = true).onSizeChanged { full = it.height },
                bodyStyle = style)
        }
        if (long || open) JTextButton(stringResource(if (open) R.string.assistant_spaces_md_show_less else R.string.assistant_spaces_md_show_more),
            { open = !open })
    }
}

@Composable
private fun foldLineHeight(style: TextStyle): Dp = with(LocalDensity.current) {
    (if (style.lineHeight.isSpecified) style.lineHeight else style.fontSize).toDp()
}
