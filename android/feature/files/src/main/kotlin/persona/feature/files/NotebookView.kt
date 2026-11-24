package persona.feature.files

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.annotation.StringRes
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import coil3.compose.AsyncImage
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import persona.core.designsystem.*
import java.util.Base64

private sealed interface NotebookLoad {
    data object Reading : NotebookLoad
    class Failed(@StringRes val reason: Int) : NotebookLoad
    class Ready(val notebook: Notebooks.Notebook) : NotebookLoad
}

/**
 * A Jupyter notebook as its cells (docs/android/android.md "Files and Git in chat", iOS `NotebookFileView`): Markdown cells rendered,
 * code cells with their run count and colored source, then each output: text and streams in the mono face (stderr in
 * neutral secondary ink), pictures, HTML as its text, errors with the traceback. ⋯ shows the JSON source instead.
 */
@Composable
internal fun NotebookFile(text: String, base: String, contentPadding: PaddingValues, header: LazyListScope.() -> Unit,
    onOpenPath: (String) -> Unit, loadImage: suspend (String) -> ByteArray?, truncated: Boolean = false) {
    val resources = LocalResources.current
    val load by produceState<NotebookLoad>(NotebookLoad.Reading, text, resources) {
        value = withContext(Dispatchers.Default) {
            val more = { lines: Int -> resources.getQuantityString(R.plurals.files_notebook_lines_hidden, lines, lines) }
            try { NotebookLoad.Ready(Notebooks.parse(text, more)) } catch (e: Notebooks.Invalid) { NotebookLoad.Failed(e.reason) }
        }
    }
    val side = pagePadding
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(top = contentPadding.calculateTopPadding(),
        bottom = contentPadding.calculateBottomPadding() + Tokens.Space.xl)) {
        header()
        when (val state = load) {
            NotebookLoad.Reading -> item { JLoading() }
            is NotebookLoad.Failed -> item {
                // A notebook cut at 2 MB is JSON cut in half: say that, not just that it doesn't parse.
                JEmptyState(stringResource(R.string.files_notebook_failed), if (truncated) stringResource(R.string.files_notebook_truncated) else stringResource(state.reason))
            }
            is NotebookLoad.Ready -> {
                val nb = state.notebook
                item("about") {
                    val cells = pluralStringResource(R.plurals.files_notebook_cells, nb.cells.size, nb.cells.size)
                    Notice(listOf(nb.kernel ?: nb.language, cells, "nbformat ${nb.format}").joinToString(" · "))
                }
                if (nb.cells.isEmpty()) item("empty") { JEmptyState(stringResource(R.string.files_notebook_empty), stringResource(R.string.files_notebook_empty_message)) }
                itemsIndexed(nb.cells, key = { i, _ -> "cell:$i" }, contentType = { _, c -> c.type }) { _, cell ->
                    NotebookCell(cell, nb.language, base, onOpenPath, loadImage, Modifier.padding(horizontal = side, vertical = Tokens.Space.s))
                }
            }
        }
    }
}

@Composable
private fun NotebookCell(cell: Notebooks.Cell, language: String, base: String, onOpenPath: (String) -> Unit,
    loadImage: suspend (String) -> ByteArray?, modifier: Modifier) {
    val palette = PersonaTheme.palette
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        when (cell.type) {
            Notebooks.CellType.Markdown -> if (cell.source.isNotBlank()) ReadingContent {
                MarkdownText(cell.source, base, onOpenPath, loadImage, attachments = cell.attachments)
            }
            Notebooks.CellType.Raw -> if (cell.source.isNotEmpty()) JCodeBlock(cell.source, small = true)
            Notebooks.CellType.Code -> {
                Text(if (cell.count != null) "In [${cell.count}]" else "In [ ]", style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
                val keyword = palette.accent
                val string = palette.done.text
                val comment = palette.ink3
                val code = remember(cell.source, keyword, string, comment) { highlighted(cell.source, keyword, string, comment) }
                JCodeBlock(code, small = true)
                cell.outputs.forEach { NotebookOutput(it, base, onOpenPath, loadImage) }
            }
        }
    }
}

@Composable
private fun NotebookOutput(output: Notebooks.Output, base: String, onOpenPath: (String) -> Unit, loadImage: suspend (String) -> ByteArray?) {
    val palette = PersonaTheme.palette
    when (output) {
        is Notebooks.Output.Stream -> OutputText(AnnotatedString(output.text.removeSuffix("\n")), if (output.stderr) palette.ink2 else palette.ink,
            palette.statusSurface)
        is Notebooks.Output.Text -> OutputText(AnnotatedString(output.text), palette.ink)
        is Notebooks.Output.Html -> OutputText(AnnotatedString(output.text), palette.ink)
        is Notebooks.Output.Markdown -> MarkdownText(output.text, base, onOpenPath, loadImage)
        is Notebooks.Output.Image -> OutputImage(output)
        is Notebooks.Output.Error -> {
            val danger = palette.danger.text
            val text = remember(output, danger) {
                buildAnnotatedString {
                    withStyle(SpanStyle(color = danger)) { append(listOf(output.name, output.value).filter { it.isNotEmpty() }.joinToString(": ")) }
                    if (output.traceback.isNotBlank()) append("\n" + output.traceback)
                }
            }
            OutputText(text, palette.ink, palette.statusSurface)
        }
    }
}

/** Output text in the mono face, one line per line (wide tables scroll sideways), selectable. */
@Composable
private fun OutputText(text: AnnotatedString, color: Color, fill: Color? = null) {
    val shape = RoundedCornerShape(Tokens.Radius.badge)
    SelectionContainer {
        Text(text, Modifier.fillMaxWidth().clip(shape).then(if (fill != null) Modifier.background(fill) else Modifier).horizontalScroll(rememberScrollState())
            .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
            style = PersonaTheme.type.codeSmall, color = color, softWrap = false)
    }
}

@Composable
private fun OutputImage(image: Notebooks.Output.Image) {
    val bytes by produceState<ByteArray?>(null, image.base64) {
        value = withContext(Dispatchers.Default) { runCatching { Base64.getMimeDecoder().decode(image.base64) }.getOrNull() }
    }
    val data = bytes ?: return
    AsyncImage(model = data, contentDescription = stringResource(R.string.files_notebook_output_image), contentScale = ContentScale.FillWidth,
        modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.badge)))
}
