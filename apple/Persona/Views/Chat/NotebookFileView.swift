import MarkdownUI
import PersonaKit
import SwiftUI

/// A Jupyter notebook in the file viewer (docs/apple/ios.md「File viewer」): Markdown cells in the chat's Markdown look,
/// code cells as code in the notebook's language with their run number, and under each what it printed: streams,
/// results, images, errors with their traceback. An HTML result shows its text version.
struct NotebookFileView: View {
    let notebook: Notebook

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Theme.Space.l) {
                ForEach(notebook.cells) { cell in
                    NotebookCellView(cell: cell, notebook: notebook)
                }
            }
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.m)
        }
        .accessibilityIdentifier("file-notebook")
    }
}

private struct NotebookCellView: View {
    let cell: Notebook.Cell
    let notebook: Notebook

    var body: some View {
        switch cell.kind {
        case .markdown:
            Markdown(MarkdownMemo.content(cell.source)).markdownTheme(.persona).readingContent()
        case .raw:
            Text(cell.source)
                .font(Theme.Font.code)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
        case .code:
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text("In [\(cell.count.map(String.init) ?? " ")]")
                    .font(Theme.Font.label)
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
                Markdown(MarkdownMemo.content(notebook.fenced(cell))).markdownTheme(.persona)
                ForEach(Array(cell.outputs.enumerated()), id: \.offset) { _, output in
                    NotebookOutputView(output: output)
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("notebook-cell-\(cell.id)")
        }
    }
}

private struct NotebookOutputView: View {
    let output: Notebook.Output

    /// Lines of one output shown before the rest is folded behind 「显示全部」.
    static let foldedLines = 60

    var body: some View {
        switch output {
        case .stream(let name, let text):
            OutputText(text: text, tint: name == "stderr" ? Theme.cautionText : nil)
        case .text(let text):
            OutputText(text: text, tint: nil)
        case .html(let html, let plain):
            NotebookHTMLOutput(html: html, text: plain ?? Self.strip(html))
        case .image(let mime, let base64):
            Base64Image(payload: ImagePayload(mime: mime, data: base64))
                .frame(maxWidth: .infinity, alignment: .leading)
        case .error(let name, let value, let traceback):
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(value.isEmpty ? name : "\(name): \(value)")
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(Theme.danger)
                if !traceback.isEmpty { OutputText(text: traceback, tint: Theme.danger) }
            }
        }
    }

    /// The text of an HTML result with no text version: tags out, entities decoded, blank lines squeezed.
    static func strip(_ html: String) -> String {
        var s = html.replacingOccurrences(of: "<br\\s*/?>|</(p|div|tr|h[1-6]|li)>", with: "\n", options: .regularExpression)
        s = s.replacingOccurrences(of: "</t[dh]>", with: "\t", options: .regularExpression)
        s = s.replacingOccurrences(of: "<[^>]+>", with: "", options: .regularExpression)
        for (e, c) in [("&lt;", "<"), ("&gt;", ">"), ("&quot;", "\""), ("&#39;", "'"), ("&nbsp;", " "), ("&amp;", "&")] {
            s = s.replacingOccurrences(of: e, with: c)
        }
        return s.replacingOccurrences(of: "\n{3,}", with: "\n\n", options: .regularExpression).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// What a cell printed: monospaced, selectable, sideways when long, the first lines of a long one.
private struct OutputText: View {
    let text: String
    let tint: Color?
    @State private var all = false

    var body: some View {
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        let folded = !all && lines.count > NotebookOutputView.foldedLines
        let shown = folded ? lines.prefix(NotebookOutputView.foldedLines).joined(separator: "\n") : text
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            // Long lines (a traceback's rule and file paths) scroll sideways; the fade says there is more.
            SidewaysScroll {
                Text(shown.hasSuffix("\n") ? String(shown.dropLast()) : shown)
                    .font(Theme.Font.code)
                    .foregroundStyle(tint ?? Color.primary)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: true, vertical: false)
                    .padding(Theme.Space.s)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityIdentifier("notebook-output")
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
            if folded {
                Button("Show All \(lines.count) Lines [Notebook File View 18a740]") { all = true }
                    .font(Theme.Font.label)
                    .lineLimit(1)
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.accent)
            }
        }
    }
}

/// An HTML result (a pandas table): rendered in isolation (`HTMLSnippetView`, scripts and the network off), with its
/// text version a tap away, and in its place when the HTML does not load.
private struct NotebookHTMLOutput: View {
    let html: String
    let text: String
    @State private var asText = false

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if asText {
                OutputText(text: text, tint: nil)
            } else {
                HTMLSnippetView(html: html, failed: { asText = true })
            }
            Button(asText ? String(localized: "Show Rendered") : String(localized: "Show Text")) { asText.toggle() }
                .font(Theme.Font.label)
                .lineLimit(1)
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
        }
    }
}
