import PersonaKit
import SwiftUI
import WebKit

/// An .html file previewed in isolation (docs/apple/ios.md「File viewer」): a web view of its own with no website data kept,
/// scripts off and every network load blocked unless the ⋯ menu turns them on; the page cannot navigate (a link is
/// opened the app's way instead). Files next to it (pictures, style sheets, fonts, scripts) are read from the machine
/// through the hub and put into the page first (`HTMLResources`), so they show with the network off.
struct HTMLFileView: View {
    let html: String
    /// The page's folder on the machine, and how to read a file there.
    let dir: String
    let fetch: HTMLResources.Fetch
    let scripts: Bool
    let network: Bool
    var onLink: (URL) -> Void
    @State private var page: HTMLResources.Result?

    var body: some View {
        VStack(spacing: 0) {
            Label(note, systemImage: scripts || network ? Theme.Icon.allowEverything : Theme.Icon.permissions)
                .font(Theme.Font.label)
                .foregroundStyle(scripts || network ? Theme.cautionText : Color.secondary)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, Theme.Space.page)
                .padding(.vertical, Theme.Space.s)
            if let page {
                IsolatedWebView(html: page.html, scripts: scripts, network: network, onLink: onLink)
                    .id([scripts, network]) // the settings apply when a web view is made
                    .accessibilityIdentifier("file-html")
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task(id: html) { page = await HTMLResources.inline(html, dir: dir, fetch: fetch) }
    }

    private var note: String {
        let base = switch (scripts, network) {
        case (false, false): String(localized: "Isolated preview: scripts and network are off. Turn them on in ⋯")
        case (true, false): String(localized: "Scripts on, network still off")
        case (false, true): String(localized: "Network on, scripts still off")
        case (true, true): String(localized: "Scripts and network are on")
        }
        guard let page, page.loaded + page.missing > 0 else { return base }
        return page.missing > 0 ? String(localized: "\(base) · \(page.loaded) files from the same folder loaded, \(page.missing) missing")
            : String(localized: "\(base) · loaded \(page.loaded) files from the same folder")
    }
}

/// An HTML piece shown inline in isolation (a notebook's HTML result, docs/apple/ios.md「File viewer」): scripts and the
/// network off, a light stylesheet for tables, as tall as its content (sideways inside when wide); `failed` when it
/// does not load, for the text version to stand in.
struct HTMLSnippetView: View {
    let html: String
    var failed: () -> Void
    @State private var height: CGFloat = 0
    @Environment(\.openURL) private var openURL

    /// Tables and text in a notebook's HTML results look like the notebook's own (pandas leaves styling to the page); at
    /// the device's width (without a viewport the piece was laid out 980 px wide and shrunk to an unreadable table).
    static let style = """
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <style>:root{color-scheme:light dark}body{margin:0;font:13px -apple-system,system-ui,sans-serif}
        table{border-collapse:collapse}th,td{border:1px solid rgba(128,128,128,.35);padding:3px 8px;text-align:right}
        th{font-weight:600}tr:nth-child(even) td{background:rgba(128,128,128,.08)}</style>
        """

    var body: some View {
        IsolatedWebView(html: Self.style + html, scripts: false, network: false, onLink: { openURL($0) },
                        onHeight: { height = $0 }, onFail: failed)
            .frame(height: max(height, Theme.Size.wellS))
            .accessibilityIdentifier("notebook-html")
    }
}

/// Blocks every load but the page itself (a string's page is about: or applewebdata:) and inline data. One rule a
/// scheme: content rule filters have no `|` (with one the list did not compile and the page was not shown).
private let offlineRules = "[" + (
    [#"{"trigger":{"url-filter":".*"},"action":{"type":"block"}}"#] +
    ["about", "applewebdata", "data", "blob"].map { #"{"trigger":{"url-filter":"^\#($0):"},"action":{"type":"ignore-previous-rules"}}"# }
).joined(separator: ",") + "]"

@MainActor
private func isolatedWebView(scripts: Bool, network: Bool, coordinator: IsolatedWebViewCoordinator) -> WKWebView {
    let config = WKWebViewConfiguration()
    config.websiteDataStore = .nonPersistent()
    config.defaultWebpagePreferences.allowsContentJavaScript = scripts
    config.preferences.javaScriptCanOpenWindowsAutomatically = false
    config.dataDetectorTypes = []
    let web = WKWebView(frame: .zero, configuration: config)
    #if os(iOS)
    CallMediaGuard.shared.register(web) // a file's media stays still while a system call is live
    #endif
    web.navigationDelegate = coordinator
    web.uiDelegate = coordinator
    return web
}

@MainActor
final class IsolatedWebViewCoordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
    var onLink: (URL) -> Void
    /// The content's height once loaded (snippets size to it); a load that failed.
    var onHeight: ((CGFloat) -> Void)?
    var onFail: (() -> Void)?
    var loaded: String?
    init(onLink: @escaping (URL) -> Void) { self.onLink = onLink }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard let onHeight else { return }
        // The app's own script, which runs with the page's scripts off.
        webView.evaluateJavaScript("document.documentElement.scrollHeight") { value, _ in
            if let h = value as? NSNumber { MainActor.assumeIsolated { onHeight(CGFloat(truncating: h)) } }
        }
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) { onFail?() }

    /// The page's address: nothing relative resolves against it (the files next to it are on the machine).
    static let blank = URL(string: "about:blank")

    func load(_ html: String, in web: WKWebView, network: Bool) {
        guard loaded != html else { return }
        loaded = html
        guard !network else {
            web.loadHTMLString(html, baseURL: Self.blank)
            return
        }
        Task {
            if let rules = try? await WKContentRuleListStore.default().compileContentRuleList(forIdentifier: "persona-html-offline",
                                                                                            encodedContentRuleList: offlineRules) {
                web.configuration.userContentController.add(rules)
                web.loadHTMLString(html, baseURL: Self.blank)
            } else {
                // No rules, no page: never show it with the network open by accident.
                web.loadHTMLString("<p>\(String(localized: "Can't isolate the network, so this page isn't shown"))</p>", baseURL: Self.blank)
            }
        }
    }

    /// Only the page itself: a tapped link goes to the app (the in-app link sheet), anything else stays put.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
        if action.navigationType == .linkActivated, let url = action.request.url {
            if url.scheme == "http" || url.scheme == "https" { onLink(url) }
            return .cancel
        }
        let own = ["about", "applewebdata"].contains(action.request.url?.scheme ?? "")
        return own || action.targetFrame?.isMainFrame == false ? .allow : .cancel
    }

    /// target=_blank and window.open: the same as a link.
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction,
                 windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = action.request.url, url.scheme == "http" || url.scheme == "https" { onLink(url) }
        return nil
    }
}

struct IsolatedWebView: UIViewRepresentable {
    let html: String
    let scripts: Bool
    let network: Bool
    var onLink: (URL) -> Void
    var onHeight: ((CGFloat) -> Void)? = nil
    var onFail: (() -> Void)? = nil

    func makeCoordinator() -> IsolatedWebViewCoordinator {
        let c = IsolatedWebViewCoordinator(onLink: onLink)
        c.onHeight = onHeight
        c.onFail = onFail
        return c
    }

    func makeUIView(context: Context) -> WKWebView {
        let web = isolatedWebView(scripts: scripts, network: network, coordinator: context.coordinator)
        context.coordinator.load(html, in: web, network: network)
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {
        context.coordinator.onLink = onLink
        context.coordinator.load(html, in: web, network: network)
    }
}
