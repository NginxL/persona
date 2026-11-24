package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.background
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.buildAnnotatedString
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * The opening value is retained until dismissal, independently of streaming updates. [plain] sets it as body text
 * rather than code (a message to select from: the long-press menu's "Select").
 */
@Composable
internal fun FullTextReader(text: String, onDismiss: () -> Unit, title: String = stringResource(R.string.chat_reader_full_text), plain: Boolean = false) {
    val frozen = remember { text }
    // Small handoffs must not depend on an asynchronous Dialog frame merely to show one short paragraph.
    // Large documents retain background pagination instead of blocking composition.
    val snapshot by if (frozen.length <= 8192) {
        remember(frozen) { mutableStateOf(ReaderSnapshot(frozen)) }
    } else {
        remember {
            kotlinx.coroutines.flow.flow {
                emit(withContext(Dispatchers.Default) { ReaderSnapshot(frozen) })
            }
        }.collectAsState(initial = null)
    }
    val copyText = rememberCopy()
    JFullScreen(onDismiss) {
        Box(Modifier.fillMaxSize().hardwareShortcutDismiss(onDismiss)) {
            val ready = snapshot
            val copy: () -> Unit = { copyText(frozen, null) }
            if (ready == null) InterfaceContent {
                JPage(title, onBack = onDismiss) { padding ->
                    LazyColumn(contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
                        top = padding.calculateTopPadding(), bottom = padding.calculateBottomPadding())) { item { JLoading() } }
                }
            } else ReaderPages(ready, Modifier.fillMaxSize().imePadding(), title, onDismiss, copy, plain)
        }
    }
}

@Composable
private fun ReaderPages(snapshot: ReaderSnapshot, modifier: Modifier, title: String, onBack: () -> Unit, onCopy: () -> Unit, plain: Boolean = false) {
    var page by rememberSaveable { mutableIntStateOf(0) }
    var query by rememberSaveable { mutableStateOf("") }
    var onlyPage by rememberSaveable { mutableStateOf(true) }
    val searchPage = page.takeIf { onlyPage }
    val matches by produceState(emptyList<ReaderRange>(), snapshot, query, searchPage) {
        value = emptyList()
        value = withContext(Dispatchers.Default) { snapshot.search(query, searchPage) }
    }
    var selected by remember(query, searchPage) { mutableIntStateOf(-1) }
    var target by remember { mutableStateOf<ReaderRange?>(null) }
    val bounds = snapshot.pages[page]
    val body = remember(snapshot, page) { snapshot.text.substring(bounds.start, bounds.end) }
    val scroll = rememberLazyListState()
    var layout by remember(body) { mutableStateOf<TextLayoutResult?>(null) }
    val active = matches.getOrNull(selected)
    val palette = PersonaTheme.palette
    val annotated = remember(body, matches, active, bounds, palette) {
        buildAnnotatedString {
            append(body)
            matches.forEach { match ->
                val start = maxOf(match.start, bounds.start) - bounds.start
                val end = minOf(match.end, bounds.end) - bounds.start
                if (start < end) addStyle(SpanStyle(
                    background = palette.marker,
                    color = palette.ink,
                    textDecoration = if (match == active) androidx.compose.ui.text.style.TextDecoration.Underline else null,
                ), start, end)
            }
        }
    }
    LaunchedEffect(page) { scroll.scrollToItem(0) }
    LaunchedEffect(target, page, layout) {
        val match = target ?: return@LaunchedEffect
        val result = layout ?: return@LaunchedEffect
        if (match.start in bounds.start until bounds.end && body.isNotEmpty()) {
            scroll.scrollToItem(0, result.getLineTop(result.getLineForOffset(match.start - bounds.start)).toInt())
        }
    }
    fun moveMatch(delta: Int) {
        if (matches.isEmpty()) return
        selected = if (selected < 0) (if (delta > 0) 0 else matches.lastIndex)
            else Math.floorMod(selected + delta, matches.size)
        target = matches[selected]
        page = snapshot.pageAt(matches[selected].start)
    }
    fun movePage(index: Int) {
        target = null
        selected = -1
        page = index.coerceIn(snapshot.pages.indices)
    }
    JPage(title = title, onBack = onBack, modifier = modifier,
        topBar = {
            InterfaceContent {
                Column {
                    JTopBar(title, onBack = onBack, actions = { JIconButton(PhIcons.Copy, stringResource(R.string.chat_reader_copy_all), onCopy) })
                    JSearchField(query, { query = it; target = null }, stringResource(if (onlyPage) R.string.chat_reader_search_page else R.string.chat_reader_search_all),
                        Modifier.padding(horizontal = pagePadding))
                    Row(Modifier.fillMaxWidth().padding(horizontal = pagePadding), verticalAlignment = Alignment.CenterVertically) {
                        JTextButton(stringResource(if (onlyPage) R.string.chat_reader_scope_page else R.string.chat_reader_scope_all), { onlyPage = !onlyPage; target = null })
                        Text(if (query.isEmpty()) "" else if (matches.isEmpty()) stringResource(R.string.chat_reader_not_found) else "${(selected + 1).coerceAtLeast(0)}/${matches.size}",
                            Modifier.weight(1f), style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1)
                        JIconButton(PhIcons.CaretUp, stringResource(R.string.chat_reader_previous_match), { moveMatch(-1) }, enabled = matches.isNotEmpty())
                        JIconButton(PhIcons.CaretDown, stringResource(R.string.chat_reader_next_match), { moveMatch(1) }, enabled = matches.isNotEmpty())
                    }
                }
            }
        },
        bottomBar = {
            InterfaceContent {
                JBottomBar { Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceEvenly, verticalAlignment = Alignment.CenterVertically) {
                    JIconButton(PhIcons.ArrowLeft, stringResource(R.string.chat_reader_first_page), { movePage(0) }, enabled = page > 0)
                    JIconButton(PhIcons.CaretLeft, stringResource(R.string.chat_reader_previous_page), { movePage(page - 1) }, enabled = page > 0)
                    Text("${page + 1}/${snapshot.pages.size}", style = PersonaTheme.type.caption, color = palette.ink, maxLines = 1)
                    JIconButton(PhIcons.CaretRight, stringResource(R.string.chat_reader_next_page), { movePage(page + 1) }, enabled = page < snapshot.pages.lastIndex)
                    JIconButton(PhIcons.ArrowLeft, stringResource(R.string.chat_reader_last_page), { movePage(snapshot.pages.lastIndex) }, modifier = Modifier.rotate(180f), enabled = page < snapshot.pages.lastIndex)
                } }
            }
        }) { padding ->
        LazyColumn(Modifier.fillMaxSize(), state = scroll, contentPadding = PaddingValues(
            start = pagePadding, end = pagePadding, top = padding.calculateTopPadding(),
            bottom = padding.calculateBottomPadding() + Tokens.Space.m)) {
            item {
                SelectionContainer {
                    Text(if (body.isEmpty()) AnnotatedString(stringResource(R.string.chat_reader_empty)) else annotated,
                        style = if (plain) PersonaTheme.type.body else PersonaTheme.type.code, color = palette.ink, onTextLayout = { layout = it },
                        modifier = if (plain) Modifier.fillMaxWidth().padding(vertical = Tokens.Space.m)
                            else Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).background(palette.sunken).padding(Tokens.Space.m))
                }
            }
        }
    }
}

@Composable
internal fun ReaderEntry(text: String, title: String = stringResource(R.string.chat_reader_full_text), always: Boolean = false) {
    var opened by remember { mutableStateOf<String?>(null) }
    if (always || text.length > 8192 || text.count { it == '\n' } >= 40) {
        InterfaceContent { JTextButton(stringResource(R.string.chat_reader_open), { opened = text }) }
    }
    opened?.let { FullTextReader(it, onDismiss = { opened = null }, title = title) }
}

@Composable
internal fun ToolReaderActions(input: String?, output: String?, onFullOutput: (() -> Unit)?, rawInput: Boolean = true) {
    var opened by remember { mutableStateOf<Pair<String, String>?>(null) }
    val copy = rememberCopy()
    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        val rawInputLabel = stringResource(R.string.chat_reader_raw_input)
        val rawOutput = stringResource(R.string.chat_reader_raw_output)
        // The step draws iOS's inline Raw input toggle itself (`drawsRawInput`): no second button for it here.
        if (rawInput) JTextButton(rawInputLabel, { opened = rawInputLabel to input.orEmpty() }, enabled = input != null)
        JTextButton(if (onFullOutput != null) stringResource(R.string.chat_reader_full_output) else rawOutput, { if (onFullOutput != null) onFullOutput() else opened = rawOutput to output.orEmpty() },
            enabled = output != null || onFullOutput != null)
        JTextButton(stringResource(R.string.chat_reader_copy_both), { copy(listOfNotNull(input, output).joinToString("\n\n"), null) },
            enabled = input != null || output != null)
    }
    opened?.let { (title, text) -> FullTextReader(text, onDismiss = { opened = null }, title = title) }
}
