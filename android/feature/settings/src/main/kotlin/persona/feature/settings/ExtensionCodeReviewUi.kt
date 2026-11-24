package persona.feature.settings

import android.content.SharedPreferences
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import persona.core.contracts.api.ExtensionDiffThread
import persona.core.contracts.api.ExtensionUiResult
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewComment
import persona.core.contracts.api.ExtensionViewItem
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

// A file's diff for reviewing (UI version 4's diff with version 5's code review, docs/extensions/extensions.md, #3136), as the
// web's DiffReview.tsx: line numbers, the words that changed marked, hidden lines shown 20 at a time or all (asked of the
// extension as lines, not a new page), viewed marks, generated and big files folded at first, comments on a line or a
// range (tap a line, then another on the same side), alone or as a draft of the pending review, with a suggestion;
// threads with drafts, resolving and suggestions drawn as a small diff; and around the diffs a toolbar (viewed so far,
// open threads, whitespace) and the pending review's bar. Logic in ExtensionCodeReview.kt.

/**
 * What a page of diffs shares: viewed marks (the extension's, changed at once and sent with mark_viewed; or kept on
 * this device by path and revision), whitespace (kept on the device), the lines of files that came, quiet actions.
 */
internal class CodeReviewMemory(val whitespace: androidx.compose.runtime.MutableState<Boolean> = mutableStateOf(false)) {
    val over = mutableStateMapOf<String, Boolean>()
    val viewedAttempts = mutableMapOf<String, Any>()
    val here = mutableStateOf(emptySet<String>())
    val caches = HashMap<String, LineCache>()
}

@Stable
internal class CodeReviewState(
    private val prefs: SharedPreferences?,
    private val memory: CodeReviewMemory? = null,
    private val onlyKey: String? = null,
    private val nodeMemory: ((String) -> CodeReviewMemory)? = null,
    private val send: (ExtensionViewAction, Map<String, String>, (ExtensionUiResult?) -> Unit) -> Unit,
) {
    private val over = memory?.over ?: mutableStateMapOf<String, Boolean>()
    private val viewedAttempts = memory?.viewedAttempts ?: mutableMapOf<String, Any>()
    private val hereState: androidx.compose.runtime.MutableState<Set<String>> = memory?.here ?: mutableStateOf(prefs?.getStringSet(ViewedKey, emptySet())?.toSet().orEmpty())
    private var here by hereState
    private val whitespaceState: androidx.compose.runtime.MutableState<Boolean> = memory?.whitespace ?: mutableStateOf(prefs?.getBoolean(WhitespaceKey, false) ?: false)
    var whitespace by whitespaceState
        private set
    private val caches = memory?.caches ?: HashMap<String, LineCache>()
    /** Bumped when lines come: what draws them reads it. */
    var linesCame by mutableIntStateOf(0)
        private set
    private var indexed: ExtensionView? = null
    private var diffs: Map<String, ExtensionViewItem> = emptyMap()
    private var entries: Map<String, Boolean> = emptyMap()

    private fun index(view: ExtensionView?) {
        if (view === indexed) return
        indexed = view
        val items = view?.sections.orEmpty().flatMap { it.items }.filter { onlyKey == null || it.key == onlyKey }
        diffs = items.filter { it.type == "diff" && !it.path.isNullOrEmpty() }.associateBy { it.path!! }
        entries = items.filter { it.type == "file_tree" }.flatMap { it.entries.orEmpty() }.filter { it.kind != "dir" }.associate { it.path to (it.viewed == true) }
    }

    private fun localId(d: ExtensionViewItem) = ExtensionCodeReview.changeId(d.path.orEmpty(), d.revision, d.hunks)

    fun viewed(path: String, view: ExtensionView?): Boolean {
        if (memory == null) over[path]?.let { return it }
        index(view)
        val d = diffs[path]
        val native = d?.key?.let { nodeMemory?.invoke(it) } ?: memory
        if (d != null && native != null) {
            native.over[localId(d)]?.let { return it }
            if (d.markViewed == null) return localId(d) in native.here.value
        }
        return when {
            d != null && d.markViewed == null -> localId(d) in here
            d != null -> d.viewed == true
            else -> entries[path] == true
        }
    }

    fun setViewed(path: String, view: ExtensionView?, viewed: Boolean) {
        index(view)
        val d = diffs[path] ?: run { if (memory == null) over[path] = viewed; return }
        val overlayKey = if (memory == null) path else localId(d)
        val previous = over[overlayKey]
        val attempt = Any()
        viewedAttempts[overlayKey] = attempt
        over[overlayKey] = viewed
        val mark = d.markViewed?.takeIf { it.id.isNotEmpty() }
        if (mark != null) send(mark, mapOf("path" to path, "viewed" to "$viewed")) { result ->
            if (viewedAttempts[overlayKey] === attempt) {
                viewedAttempts.remove(overlayKey)
                if (result == null) {
                    if (previous == null) over.remove(overlayKey) else over[overlayKey] = previous
                }
            }
        }
        else {
            viewedAttempts.remove(overlayKey)
            val id = localId(d)
            here = (if (viewed) here + id else here - id).let { if (it.size > ViewedMost) it.drop(it.size - ViewedMost).toSet() else it }
            prefs?.edit()?.putStringSet(ViewedKey, here)?.apply()
        }
    }

    fun toggleWhitespace() {
        whitespace = !whitespace
        prefs?.edit()?.putBoolean(WhitespaceKey, whitespace)?.apply()
    }

    fun lines(path: String): LineCache = caches.getOrPut(path) { LineCache() }

    /** An action that does not hold the page; [done] gets its answer (null when it failed), lines going into the cache. */
    fun fetch(action: ExtensionViewAction, values: Map<String, String>, done: (ExtensionUiResult?) -> Unit = {}) = send(action, values) { result ->
        result?.lines?.let { l -> lines(l.path).put(l.start, l.lines, l.total); linesCame++ }
        done(result)
    }

    companion object {
        const val Prefs = "persona.review"
        private const val ViewedKey = "viewed"
        private const val WhitespaceKey = "whitespace"
        private const val ViewedMost = 5000
    }
}

/** Whether a view is a page of diffs (the toolbar and the review bar are drawn for it). */
internal fun reviewing(view: ExtensionView?): Boolean = view?.sections?.any { s -> s.items.any { it.type == "diff" } } == true

/** A run of code: its highlighting and the words that changed, on the add or delete colour. */
@Composable
private fun pieces(tokens: List<CodeToken>?, text: String, spans: List<CharSpan>?, kind: DiffKind): AnnotatedString {
    if (text.isEmpty()) return AnnotatedString(" ")
    val palette = PersonaTheme.palette
    val colors = TokenKind.entries.associateWith { tokenColor(it) }
    val word = if (kind == DiffKind.Add) palette.diffAddWord else palette.diffDelWord
    return buildAnnotatedString {
        ExtensionCodeReview.overlay(tokens, text, spans).forEach { p ->
            withStyle(SpanStyle(color = colors.getValue(p.kind), fontStyle = if (p.kind == TokenKind.Comment) FontStyle.Italic else null,
                background = if (p.changed) word else androidx.compose.ui.graphics.Color.Unspecified)) { append(p.text) }
        }
    }
}
// ── around the diffs ──

/** Over a page of diffs: viewed so far, the threads still open, whitespace, and the pending review's drafts. */
@Composable
internal fun ReviewToolbar(view: ExtensionView, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier) {
    val review = handle.review ?: return
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val diffs = view.sections.flatMap { it.items }.filter { it.type == "diff" }
    val seen = diffs.count { (handle.forNode?.invoke(it.key.orEmpty())?.review ?: review).viewed(it.path.orEmpty(), view) }
    val open = diffs.sumOf { d -> d.threads.orEmpty().count { !it.resolved && !it.pending } }
    Row(modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = Tokens.Space.xs), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically) {
        if (diffs.size > 1) Text(stringResource(R.string.settings_ext_review_viewed_of, seen, diffs.size), style = type.caption, color = palette.ink2, maxLines = 1)
        if (open > 0) JTag(pluralStringResource(R.plurals.settings_ext_review_open_threads, open, open), Tone.Caution)
        JChip(stringResource(R.string.settings_ext_review_ignore_whitespace), selected = review.whitespace, onClick = { review.toggleWhitespace() })
        view.review?.takeIf { it.buttons.isNotEmpty() }?.let { ReviewSubmit(view, state, handle) }
    }
}

/** The pending review's bar at the page's foot: how many drafts, and "Submit". */
@Composable
internal fun ReviewBar(view: ExtensionView, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier) {
    val r = view.review?.takeIf { it.count > 0 && it.buttons.isNotEmpty() } ?: return
    JGroupItem(GroupPosition.Single, modifier) {
        Row(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalAlignment = Alignment.CenterVertically) {
            JIcon(PhIcons.ChatCircle, null, size = Tokens.Space.l, tint = PersonaTheme.palette.cautionText)
            Spacer(Modifier.width(Tokens.Space.s))
            Text(pluralStringResource(R.plurals.settings_ext_review_drafts, r.count, r.count), Modifier.weight(1f), style = PersonaTheme.type.callout,
                color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            ReviewSubmit(view, state, handle)
        }
    }
}

/** "Submit" and its sheet: a summary, the review's buttons, "Discard drafts". */
@Composable
private fun ReviewSubmit(view: ExtensionView, state: ExtensionViewState, handle: ExtensionViewHandle) {
    val r = view.review ?: return
    var open by remember { mutableStateOf(false) }
    val summaryState: androidx.compose.runtime.MutableState<String> = handle.nativeLocal?.slot(handle.nodeKey, "review-body") { "" } ?: rememberSaveable { mutableStateOf("") }
    var body by summaryState
    JButton(if (r.count > 0) pluralStringResource(R.plurals.settings_ext_review_submit_drafts, r.count, r.count) else stringResource(R.string.settings_ext_review_review),
        { open = true }, size = JButtonSize.Small, kind = if (r.count > 0) JButtonKind.Primary else JButtonKind.Secondary)
    if (open) JSheet({ open = false }, title = if (r.count > 0) pluralStringResource(R.plurals.settings_ext_review_drafts, r.count, r.count) else stringResource(R.string.settings_ext_review_review)) {
        Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            JTextField(body, { body = it }, Modifier.fillMaxWidth().nativeInputFocus(handle.nativeLocal, handle.nodeKey, "summary"), placeholder = r.placeholder?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_ext_review_summary),
                singleLine = false, enabled = !state.busy)
            Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s, Alignment.End)) {
                r.discard?.takeIf { it.id.isNotEmpty() }?.let { d ->
                    JButton(stringResource(R.string.settings_ext_review_discard), { open = false; handle.act(d, emptyMap()) }, size = JButtonSize.Small, kind = JButtonKind.Secondary, enabled = !state.busy)
                }
                r.buttons.filter { it.action.id.isNotEmpty() }.forEachIndexed { i, b ->
                    JButton(b.title, { open = false; handle.act(b.action, mapOf(r.name to body)); if (handle.nativeLocal == null) body = "" }, size = JButtonSize.Small,
                        kind = if (i == 0) JButtonKind.Primary else JButtonKind.Secondary,
                        loading = state.acting == b.action.id, enabled = !state.busy)
                }
            }
        }
    }
}

// ── threads ──

/** A suggestion in a comment: the lines it is on, and what it would make them. */
@Composable
private fun Suggestion(from: List<String>, to: List<String>) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(Modifier.fillMaxWidth().padding(vertical = Tokens.Space.xs).clip(RoundedCornerShape(Tokens.Radius.control)).background(palette.sunken)) {
        Row(Modifier.padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
            JIcon(PhIcons.PencilSimple, null, size = Tokens.Space.m, tint = palette.ink3)
            Spacer(Modifier.width(Tokens.Space.xs))
            Text(stringResource(R.string.settings_ext_review_suggested_change), style = type.micro, color = palette.ink3, maxLines = 1)
        }
        Box(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState())) { Column(Modifier.width(IntrinsicSize.Max)) {
            from.forEach { Text("− $it", Modifier.fillMaxWidth().background(palette.sunken).padding(horizontal = Tokens.Space.s), style = type.codeSmall, color = palette.ink, maxLines = 1, softWrap = false) }
            to.forEach { Text("+ $it", Modifier.fillMaxWidth().background(palette.sunken).padding(horizontal = Tokens.Space.s), style = type.codeSmall, color = palette.ink, maxLines = 1, softWrap = false) }
            if (to.isEmpty()) Text(stringResource(R.string.settings_ext_review_suggest_delete), Modifier.padding(horizontal = Tokens.Space.s), style = type.micro, color = palette.ink3)
        } }
    }
}

@Composable
private fun ReviewComment(c: ExtensionViewComment, original: List<String>) {
    val parts = remember(c.body) { ExtensionCodeReview.splitSuggestions(c.body.orEmpty()) }
    if (parts.none { it is BodyPart.Suggestion }) return CommentLine(c)
    CommentLine(c.copy(body = null))
    Column(Modifier.fillMaxWidth().padding(start = rowPadding + Tokens.Space.xl + Tokens.Space.m, end = rowPadding, bottom = Tokens.Space.s)) {
        parts.forEach { p ->
            when (p) {
                is BodyPart.Text -> ReadingContent { ExtensionMarkdown(p.text) }
                is BodyPart.Suggestion -> Suggestion(original, p.lines)
            }
        }
    }
}

/** Inline comments at a line: folded to its first one when resolved; a draft says so; a reply, resolve or reopen. */
@Composable
private fun ReviewThread(thread: ExtensionDiffThread, original: List<String>, state: ExtensionViewState, handle: ExtensionViewHandle) {
    val palette = PersonaTheme.palette
    var open by rememberSaveable(thread.id) { mutableStateOf(!thread.resolved) }
    val range = thread.startLine?.takeIf { it < thread.line }?.let { "$it–${thread.line}" }
    Column(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.s).clip(RoundedCornerShape(Tokens.Radius.control))
        .background(palette.surfaceOpaque)) {
        Row(Modifier.fillMaxWidth().clickable { open = !open }.padding(horizontal = rowPadding, vertical = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JIcon(if (open) PhIcons.CaretDown else PhIcons.CaretRight, null, size = Tokens.Space.m, tint = palette.ink3)
            val first = thread.comments.firstOrNull()
            Text(listOfNotNull(range?.let { stringResource(R.string.settings_ext_review_lines_range, it) },
                if (open) pluralStringResource(R.plurals.settings_ext_code_comments, thread.comments.size, thread.comments.size) else "${first?.author.orEmpty()}: ${first?.body.orEmpty()}").joinToString(" · "),
                Modifier.weight(1f), style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (thread.pending) JTag(stringResource(R.string.settings_ext_review_draft), Tone.Caution)
            if (thread.outdated) JTag(stringResource(R.string.settings_ext_code_outdated), Tone.Caution)
            if (thread.resolved) JTag(stringResource(R.string.settings_ext_code_resolved), Tone.Positive)
            val resolve = thread.resolve?.takeIf { it.id.isNotEmpty() && thread.id != null }
            if (resolve != null) Text(stringResource(if (thread.resolved) R.string.settings_ext_review_reopen else R.string.settings_ext_review_resolve),
                Modifier.clip(RoundedCornerShape(Tokens.Radius.badge)).clickable(enabled = !state.busy) {
                    handle.act(resolve, mapOf("thread" to thread.id!!, "resolved" to "${!thread.resolved}"))
                }.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xxs), style = PersonaTheme.type.caption, color = palette.accent, maxLines = 1)
        }
        if (open) {
            thread.comments.forEach { JDivider(); ReviewComment(it, original) }
            thread.reply?.takeIf { it.id.isNotEmpty() }?.let { JDivider(); ReplyBox(it, thread.id?.let { id -> mapOf("thread" to id) }.orEmpty(), state, handle) }
        }
    }
}

/**
 * The box for a comment on the lines picked: alone (comment), or as a draft of the review (draft; first once a review
 * is under way), or a suggestion started with the lines.
 */
@Composable
private fun LineComposer(pick: LinePick, body: String, setBody: (String) -> Unit, item: ExtensionViewItem, original: List<String>, drafts: Int,
                         state: ExtensionViewState, handle: ExtensionViewHandle, onCancel: () -> Unit) {
    val comment = item.comment?.takeIf { it.id.isNotEmpty() }
    val draft = item.draft?.takeIf { it.id.isNotEmpty() }
    val draftFirst = draft != null && drafts > 0
    fun go(a: ExtensionViewAction) {
        if (body.isBlank()) return
        handle.act(a, buildMap {
            put("path", item.path.orEmpty()); put("side", pick.side); put("line", "${pick.line}")
            pick.startLine?.let { put("start_line", "$it") }
            put("body", body.trim())
        })
        if (handle.nativeLocal == null) setBody("")
        onCancel()
    }
    val range = pick.startLine?.let { "$it–${pick.line}" } ?: "${pick.line}"
    Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JTextField(body, setBody, Modifier.fillMaxWidth().nativeInputFocus(handle.nativeLocal, handle.nodeKey, "diff-body"), placeholder = stringResource(R.string.settings_ext_review_comment_on_lines, range), singleLine = false, enabled = !state.busy)
        Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s, Alignment.End)) {
            if (item.suggestions == true && pick.side == "new" && original.isNotEmpty())
                JButton(stringResource(R.string.settings_ext_review_suggest), { setBody((if (body.isBlank()) "" else body.trimEnd('\n') + "\n\n") + ExtensionCodeReview.suggestionBlock(original)) },
                    size = JButtonSize.Small, kind = JButtonKind.Secondary, icon = PhIcons.PencilSimple)
            JButton(stringResource(R.string.settings_ext_code_cancel), onCancel, size = JButtonSize.Small, kind = JButtonKind.Secondary)
            draft?.let { d ->
                JButton(stringResource(if (draftFirst) R.string.settings_ext_review_add_to_review else R.string.settings_ext_review_start_review), { go(d) }, size = JButtonSize.Small,
                    kind = if (draftFirst) JButtonKind.Primary else JButtonKind.Secondary, loading = state.acting == d.id, enabled = !state.busy && body.isNotBlank())
            }
            comment?.let { c ->
                JButton(stringResource(if (draft != null) R.string.settings_ext_review_comment_now else R.string.settings_ext_code_comment), { go(c) }, size = JButtonSize.Small,
                    kind = if (draftFirst) JButtonKind.Secondary else JButtonKind.Primary, loading = state.acting == c.id, enabled = !state.busy && body.isNotBlank())
            }
        }
    }
}

/** Over lines hidden between hunks: 20 more below the hunk before, above the one after, or all. */
@Composable
private fun GapBar(hidden: Int, first: Boolean, last: Boolean, header: String?, loading: Boolean, onShow: (String) -> Unit) {
    val palette = PersonaTheme.palette
    val few = hidden <= ExtensionCodeReview.Step
    Row(Modifier.width(screenWidth()).background(palette.statusSurface).padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xxs),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        if (!few && !first) JIconButton(PhIcons.ArrowDown, stringResource(R.string.settings_ext_code_expand_down), { onShow("down") }, enabled = !loading, raised = false, tint = palette.accent)
        if (!few && !last) JIconButton(PhIcons.ArrowUp, stringResource(R.string.settings_ext_review_expand_up), { onShow("up") }, enabled = !loading, raised = false, tint = palette.accent)
        Text(stringResource(R.string.settings_ext_review_expand_all), Modifier.clip(RoundedCornerShape(Tokens.Radius.badge)).clickable(enabled = !loading) { onShow("all") }
            .padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xs), style = PersonaTheme.type.caption, color = palette.accent, maxLines = 1)
        if (loading) JSpinner()
        Text(if (hidden == Int.MAX_VALUE) stringResource(R.string.settings_ext_review_rest_of_file) else pluralStringResource(R.plurals.settings_ext_code_hidden_lines, hidden, hidden),
            style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
        header?.takeIf { it.isNotBlank() }?.let { Text(it, Modifier.weight(1f, fill = false), style = PersonaTheme.type.codeSmall, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
    }
}
// ── a file's diff ──

/** One line of a diff: both numbers, its sign, its text with the words that changed; tapping picks it for a comment. */
@Composable
private fun DiffLine(r: DiffRow, tokens: List<CodeToken>?, spans: List<CharSpan>?, picked: Boolean, onTap: (() -> Unit)?) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val bg = when {
        picked -> palette.statusSurface
        r.kind == DiffKind.Add || r.kind == DiffKind.Del -> palette.sunken
        else -> null
    }
    Row(Modifier.fillMaxWidth().then(if (bg != null) Modifier.background(bg) else Modifier).then(if (onTap != null) Modifier.clickable(onClick = onTap) else Modifier)) {
        Text(r.old?.toString().orEmpty(), Modifier.widthIn(min = Tokens.Space.xxl).padding(horizontal = Tokens.Space.xs), style = type.codeSmall, color = palette.ink3,
            maxLines = 1, textAlign = TextAlign.End)
        Text(r.new?.toString().orEmpty(), Modifier.widthIn(min = Tokens.Space.xxl).padding(horizontal = Tokens.Space.xs), style = type.codeSmall, color = palette.ink3,
            maxLines = 1, textAlign = TextAlign.End)
        Text(when (r.kind) { DiffKind.Add -> "+"; DiffKind.Del -> "−"; else -> " " }, Modifier.padding(horizontal = Tokens.Space.xs), style = type.codeSmall,
            color = when (r.kind) { DiffKind.Add -> palette.diffAdd; DiffKind.Del -> palette.diffDel; else -> palette.ink3 })
        Text(pieces(tokens, r.text, spans, r.kind), Modifier.padding(end = Tokens.Space.l), style = type.codeSmall, maxLines = 1, softWrap = false)
    }
}

@Composable
internal fun CodeDiff(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val review = handle.review
    val path = item.path.orEmpty()
    val key = item.key ?: path
    val hunks = item.hunks.orEmpty()
    val viewed = review?.viewed(path, state.view) ?: (item.viewed == true)
    val reason = remember(key) { ExtensionCodeReview.foldedAtFirst(item, viewed) }
    val openState: androidx.compose.runtime.MutableState<Boolean> = handle.nativeLocal?.slot(key, "diff-open") { reason == null } ?: rememberSaveable(key) { mutableStateOf(reason == null) }
    var open by openState
    val hadState: androidx.compose.runtime.MutableState<Boolean> = handle.nativeLocal?.slot(key, "diff-had") { hunks.isNotEmpty() } ?: remember(key) { mutableStateOf(hunks.isNotEmpty()) }
    var had by hadState
    LaunchedEffect(hunks.isNotEmpty()) { if (hunks.isNotEmpty() && !had) { had = true; open = true } }
    val allState: androidx.compose.runtime.MutableState<Boolean> = handle.nativeLocal?.slot(key, "diff-all") { false } ?: rememberSaveable(key) { mutableStateOf(false) }
    var all by allState
    val ws = review?.whitespace == true
    val rowsOf = remember(hunks, ws) { hunks.map { h -> ExtensionCodeText.rows(h).let { if (ws) ExtensionCodeReview.ignoreWhitespace(it) else it } } }
    val spansOf = remember(rowsOf) { rowsOf.map(ExtensionCodeReview::wordSpans) }
    val allWhitespace = remember(hunks, ws) { ws && ExtensionCodeReview.onlyWhitespace(hunks) }
    val language = ExtensionCodeText.language(item.language, path)
    val lit = remember(hunks, language) {
        hunks.map { h -> ExtensionCodeText.sides(h).let { (o, n) -> ExtensionCodeText.highlight(o, language) to ExtensionCodeText.highlight(n, language) } }
    }
    // hidden lines: what is shown of each gap, the file's lines as they come
    val context = item.context?.takeIf { it.id.isNotEmpty() }
    @Suppress("UNUSED_VARIABLE") val came = review?.linesCame
    val cache = review?.lines(path)
    var shown by remember(key) { mutableStateOf(mapOf<Int, Shown>()) }
    var gapBusy by remember(key) { mutableStateOf<Int?>(null) }
    // a new file is all in its diff: nothing hidden
    val gapList = if (context != null && review != null && item.status != "added") ExtensionCodeReview.gaps(hunks, cache?.total) else emptyList()
    fun showGap(g: Gap, direction: String) {
        val (next, need) = ExtensionCodeReview.reveal(g, shown[g.at] ?: Shown(), direction)
        if (cache != null && context != null && !cache.has(need.from, need.to)) {
            gapBusy = g.at
            review.fetch(context, mapOf("path" to path, "side" to "new", "from" to "${need.from}", "to" to (need.to?.toString() ?: "end"))) { r ->
                gapBusy = null
                if (r?.lines != null) shown = shown + (g.at to next)
            }
        } else shown = shown + (g.at to next)
    }
    // threads at their lines (and the hidden ones shown); the rest at the end
    val revealed = gapList.flatMap { g -> ExtensionCodeReview.gapRows(g, shown[g.at] ?: Shown(), cache).let { (t, b, _) -> t + b } }
        .flatMap { listOf(ExtensionCodeText.spot("new", it.new!!), ExtensionCodeText.spot("old", it.old!!)) }.toSet()
    val (at, rest) = remember(item.threads, hunks, revealed) {
        val (placed, left) = ExtensionCodeText.placeThreads(item.threads.orEmpty(), hunks)
        val more = placed.toMutableMap()
        val keep = left.filter { th ->
            val spot = ExtensionCodeText.spot(th.side, th.line)
            if (!th.outdated && spot in revealed) { more[spot] = more[spot].orEmpty() + th; false } else true
        }
        more to keep
    }
    // the lines picked for a comment: tap one, then another on the same side for a range
    val canComment = item.comment?.id?.isNotEmpty() == true || item.draft?.id?.isNotEmpty() == true
    var box by (handle.nativeLocal?.slot<LinePick?>(key, "diff-pick") { null } ?: remember(key) { mutableStateOf<LinePick?>(null) })
    handle.nativeLocal?.let { local ->
        // Retain the text, but an old line/range is not authorization to comment on different code.
        val anchor = Triple(path, item.revision, hunks)
        val selectedRevision = local.slot(key, "diff-pick-revision") { anchor }
        if (selectedRevision.value != anchor) { box = null; selectedRevision.value = anchor }
    }
    val bodyState: androidx.compose.runtime.MutableState<String> = handle.nativeLocal?.slot(key, "diff-body") { "" } ?: rememberSaveable(key) { mutableStateOf("") }
    var body by bodyState
    fun tap(side: String, n: Int) {
        val b = box
        box = when {
            b != null && b.side == side && b.startLine == null && b.line == n -> null
            b != null && b.side == side -> ExtensionCodeReview.pickRange(LinePick(side, b.startLine ?: b.line), LinePick(side, n))
            else -> LinePick(side, n)
        }
    }
    val newLines = remember(hunks) { hunks.flatMap(ExtensionCodeText::rows).mapNotNull { r -> r.new?.let { it to r.text } }.toMap() }
    fun original(side: String, line: Int, start: Int?): List<String> {
        if (side != "new") return emptyList()
        return ((start ?: line)..line).map { newLines[it] ?: cache?.get(it) ?: return emptyList() }
    }
    // a diff given without its lines loads them once it comes near (a generated one only when asked)
    val load = item.load?.takeIf { it.id.isNotEmpty() }
    var loadState by remember(key) { mutableStateOf("") }
    val autoLoad = hunks.isEmpty() && load != null && item.binary != true && reason != FoldReason.Generated && review != null
    LaunchedEffect(autoLoad) {
        if (autoLoad && loadState.isEmpty()) { loadState = "loading"; review.fetch(load, mapOf("path" to path)) { loadState = if (it == null) "failed" else "" } }
    }
    val mark = ExtensionCodeText.status(item.status)
    JGroupItem(GroupPosition.Single, modifier) {
        FileBar(path, lead = {
            JIcon(if (open) PhIcons.CaretDown else PhIcons.CaretRight, null, size = Tokens.Space.m, tint = palette.ink3)
            mark?.let { (letter, tone) -> Spacer(Modifier.width(Tokens.Space.s)); Text(letter, style = type.micro.copy(fontWeight = FontWeight.Bold), color = palette.tone(tone).text) }
        }, trailing = {
            if (reason == FoldReason.Generated) { JTag(stringResource(R.string.settings_ext_review_generated), Tone.Neutral); Spacer(Modifier.width(Tokens.Space.xs)) }
            item.threads?.size?.takeIf { it > 0 }?.let { JIcon(PhIcons.ChatCircle, null, size = Tokens.Space.m, tint = palette.ink3); Text(" $it  ", style = type.micro, color = palette.ink3) }
            Text("+${item.additions ?: 0}", style = type.micro, color = palette.diffAdd, maxLines = 1)
            Text(" −${item.deletions ?: 0}", style = type.micro, color = palette.diffDel, maxLines = 1)
            if (review != null) {
                Spacer(Modifier.width(Tokens.Space.s))
                val label = stringResource(R.string.settings_ext_review_viewed)
                JCheckbox(viewed, { v -> review.setViewed(path, state.view, v); if (v) open = false }, Modifier.semantics { contentDescription = label })
            }
        }, onClick = { open = !open })
        item.oldPath?.takeIf { it.isNotBlank() && it != path }?.let {
            val similar = item.similarity?.let { n -> " · " + stringResource(R.string.settings_ext_review_similar, n) }.orEmpty()
            Text(stringResource(R.string.settings_ext_code_renamed_from, it) + similar, Modifier.padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.s),
                style = type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.StartEllipsis)
        }
        when {
            item.binary == true -> { JDivider(); Text(stringResource(R.string.settings_ext_code_binary), Modifier.fillMaxWidth().padding(Tokens.Space.l), style = type.caption, color = palette.ink3) }
            hunks.isEmpty() && load != null -> if (loadState == "loading") {
                JDivider()
                Row(Modifier.fillMaxWidth().padding(Tokens.Space.m), horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.CenterVertically) {
                    JSpinner(); Spacer(Modifier.width(Tokens.Space.s))
                    Text(stringResource(R.string.settings_ext_review_loading_diff), style = type.caption, color = palette.ink3, maxLines = 1)
                }
            } else LoadLine(stringResource(if (reason == FoldReason.Generated) R.string.settings_ext_review_load_generated else R.string.settings_ext_code_load_diff),
                busy = state.acting == load.id, enabled = !state.busy) { handle.act(load, mapOf("path" to path)) }
            hunks.isEmpty() -> { JDivider(); Text(stringResource(if (item.status == "renamed") R.string.settings_ext_review_renamed_only else R.string.settings_ext_code_no_changes),
                Modifier.fillMaxWidth().padding(Tokens.Space.l), style = type.caption, color = palette.ink3) }
            !open && reason != null && reason != FoldReason.Collapsed -> {
                JDivider()
                Text(stringResource(when (reason) { FoldReason.Generated -> R.string.settings_ext_review_folded_generated; FoldReason.Big -> R.string.settings_ext_review_folded_big
                    else -> R.string.settings_ext_review_folded_viewed }) + " · " + stringResource(R.string.settings_ext_review_show_diff),
                    Modifier.fillMaxWidth().clickable { open = true }.padding(Tokens.Space.m), style = type.caption, color = palette.ink3, maxLines = 1, textAlign = TextAlign.Center)
            }
            open && allWhitespace -> { JDivider(); Text(stringResource(R.string.settings_ext_review_whitespace_only),
                Modifier.fillMaxWidth().padding(Tokens.Space.l), style = type.caption, color = palette.ink3) }
            open -> {
                JDivider()
                var drawn = 0
                @Composable
                fun under(spots: List<String>) {
                    // a thread's state (folded, its reply box) goes with its id when one is added before it (#4179)
                    spots.flatMap { at[it].orEmpty() }.forEach { th -> androidx.compose.runtime.key(th.id) {
                        Box(Modifier.width(screenWidth())) { ReviewThread(th, original(th.side, th.line, th.startLine), state, handle) }
                    } }
                    val b = box
                    if (b != null && ExtensionCodeText.spot(b.side, b.line) in spots) Box(Modifier.width(screenWidth()).background(palette.surfaceOpaque)) {
                        LineComposer(b, body, { body = it }, item, original(b.side, b.line, b.startLine), state.view?.review?.count ?: 0, state, handle) { box = null }
                    }
                }
                @Composable
                fun rows(rows: List<DiffRow>, tokens: (Int, DiffRow) -> List<CodeToken>?, spans: Map<Int, List<CharSpan>>) {
                    rows.forEachIndexed { i, r ->
                        if (!all && drawn >= LinesShown) return@forEachIndexed
                        drawn++
                        if (r.kind == DiffKind.Note) {
                            Text(r.text, Modifier.padding(horizontal = Tokens.Space.s), style = type.micro, color = palette.ink3)
                            return@forEachIndexed
                        }
                        val side = if (r.kind == DiffKind.Del) "old" else "new"
                        val n = if (r.kind == DiffKind.Del) r.old!! else r.new!!
                        DiffLine(r, tokens(i, r), spans[i], box?.has(side, n) == true, if (canComment) ({ tap(side, n) }) else null)
                        under(ExtensionCodeText.spotsOf(r))
                    }
                }
                @Composable
                fun gap(i: Int) {
                    val g = gapList.getOrNull(i) ?: return
                    val (top, bottom, hidden) = ExtensionCodeReview.gapRows(g, shown[g.at] ?: Shown(), cache)
                    val lines = remember(top, bottom, language) { ExtensionCodeText.highlight(top.map { it.text }, language) to ExtensionCodeText.highlight(bottom.map { it.text }, language) }
                    rows(top, { k, _ -> lines.first.getOrNull(k) }, emptyMap())
                    if (hidden > 0) GapBar(hidden, first = i == 0, last = i == hunks.size, header = hunks.getOrNull(i)?.header, loading = gapBusy == i) { showGap(g, it) }
                    rows(bottom, { k, _ -> lines.second.getOrNull(k) }, emptyMap())
                }
                val expand = item.expand?.takeIf { it.id.isNotEmpty() }
                Box(Modifier.fillMaxWidth().horizontalScroll(handle.nativeLocal?.scroll(key, "diff-horizontal") ?: rememberScrollState()).background(palette.sunken)) {
                    // as wide as the widest line (the screen at least): a line's colour runs the whole row
                    Column(Modifier.width(IntrinsicSize.Max).widthIn(min = screenWidth())) {
                        hunks.forEachIndexed { hi, h ->
                            if (!all && drawn >= LinesShown) return@forEachIndexed
                            if (gapList.isNotEmpty()) gap(hi)
                            else {
                                val hidden = ExtensionCodeText.gapBefore(hunks, hi)
                                if (expand != null && hidden > 0) GapLine(pluralStringResource(R.plurals.settings_ext_code_hidden_lines, hidden, hidden), !state.busy) {
                                    handle.act(expand, mapOf("path" to path, "hunk" to "$hi", "direction" to "up"))
                                } else h.header?.takeIf { it.isNotBlank() }?.let {
                                    Text(it, Modifier.padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs), style = type.codeSmall, color = palette.ink3, maxLines = 1, softWrap = false)
                                }
                            }
                            val (oldLit, newLit) = lit[hi]
                            // where each row's highlighting is: its place among the old (or new) side's lines
                            val places = remember(rowsOf[hi]) {
                                var oi = 0; var ni = 0
                                rowsOf[hi].map { r -> when (r.kind) { DiffKind.Del -> (oi++) to -1; DiffKind.Add -> -1 to (ni++); DiffKind.Context -> (oi++) to (ni++); DiffKind.Note -> -1 to -1 } }
                            }
                            rows(rowsOf[hi], { k, r -> places[k].let { (o, n) -> if (r.kind == DiffKind.Del) oldLit.getOrNull(o) else newLit.getOrNull(n) } }, spansOf[hi])
                        }
                        if (gapList.isNotEmpty()) { if (all || drawn < LinesShown) gap(hunks.size) }
                        else if (expand != null && (all || drawn < LinesShown)) GapLine(stringResource(R.string.settings_ext_code_expand_down), !state.busy, down = true) {
                            handle.act(expand, mapOf("path" to path, "hunk" to "${hunks.lastIndex}", "direction" to "down"))
                        }
                    }
                }
                if (!all && drawn >= LinesShown) LoadLine(stringResource(R.string.settings_ext_code_show_all_diff), busy = false, enabled = true) { all = true }
                if (rest.isNotEmpty()) {
                    JDivider()
                    Text(stringResource(R.string.settings_ext_code_elsewhere), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s), style = type.caption, color = palette.ink3)
                    rest.forEach { th -> androidx.compose.runtime.key(th.id) { ReviewThread(th, emptyList(), state, handle) } }
                }
            }
        }
    }
}

/** The bar an extension without context draws for hidden lines (its expand draws the page again, UI version 4). */
@Composable
private fun GapLine(title: String, enabled: Boolean, down: Boolean = false, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    Row(Modifier.width(screenWidth()).background(palette.statusSurface).clickable(enabled = enabled, onClick = onClick)
        .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
        JIcon(if (down) PhIcons.ArrowDown else PhIcons.ArrowUp, null, size = Tokens.Space.m, tint = palette.accent)
        Spacer(Modifier.width(Tokens.Space.s))
        Text(title, style = PersonaTheme.type.caption, color = palette.accent, maxLines = 1)
    }
}
