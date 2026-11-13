import BeautifulUI
import SwiftUI

#if os(iOS) || os(visionOS)
import UIKit

/// A piece of a page laid out on its own: in a hosting controller of its own, so what changes inside it (a text field
/// gaining a character) does not lay out the page around it. In the page's own view graph every keystroke in the chat's
/// composer laid the whole window out again from the root, the chat list re-measuring its visible rows on the way
/// (about 10 ms a character on the simulator, docs/operations/testing.md「Persona page」). The page only hears from it
/// when its height changes (a new line, a chip above the field): the content measures itself and reports the height,
/// which the page keeps as the island's fixed height.
///
/// What the content draws outside its own frame (a menu hanging above it) shows but takes no touches: the hosting
/// view only hit-tests inside its bounds, and taps there land on the page below. Such content stays out of an island.
///
/// `Content` is Equatable: the page hands it in again on every update, and only a different value reaches the island.
/// The page's environment goes in value by value (`IslandEnvironment`), not whole: `.environment(\.self, …)` also
/// replaced the island's own hosting entries (the root view it renders into, its hosting controller) with the page's,
/// and in a chat (inside the navigation stack's hosting controller) everything in the island was out of the
/// accessibility tree: the composer's field, + and send, for VoiceOver and the UI tests alike (#995, #1006).
struct LayoutIsland<Content: View & Equatable>: View {
    let content: Content
    /// The content's own height, as it last reported it (nil until it has).
    @State private var height: CGFloat?

    var body: some View {
        IslandHost(content: content) { h in if h != height { height = h } }
            .frame(height: height)
    }
}

private struct IslandHost<Content: View & Equatable>: UIViewControllerRepresentable {
    let content: Content
    let measured: (CGFloat) -> Void

    func makeUIViewController(context: Context) -> UIHostingController<AnyView> {
        let host = UIHostingController(rootView: root(context))
        // The page keeps the keyboard and the screen edges off it already.
        host.safeAreaRegions = []
        host.sizingOptions = []
        host.view.backgroundColor = .clear
        return host
    }

    func updateUIViewController(_ host: UIHostingController<AnyView>, context: Context) {
        // The environment (color scheme, type size, style) can change without the content changing; the content
        // itself is compared (`EquatableView`) and left alone when it is the same.
        host.rootView = root(context)
    }

    /// Before the content has reported its height (the first layout): ask the hosting controller.
    func sizeThatFits(_ proposal: ProposedViewSize, uiViewController host: UIHostingController<AnyView>, context: Context) -> CGSize? {
        guard let width = proposal.width, width.isFinite else { return nil }
        if let h = proposal.height, h.isFinite { return CGSize(width: width, height: h) }
        let fit = host.sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude))
        return CGSize(width: width, height: ceil(fit.height))
    }

    private func root(_ context: Context) -> AnyView {
        AnyView(
            EquatableView(content: content)
                .fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self, of: { ceil($0.size.height) }) { measured($0) }
                .frame(maxHeight: .infinity, alignment: .bottom) // a frame grown taller than the last report: from the bottom
                .modifier(IslandEnvironment(context.environment))
        )
    }
}

/// What the app's views read from the environment, taken from the page's: the model, the style's theme, the shell,
/// the type size and the look. The rest is the island's own hosting controller's (color scheme and the system's
/// settings also come with its trait collection). A view in an island that reads another value set by the page needs it
/// here.
private struct IslandEnvironment: ViewModifier {
    let env: EnvironmentValues

    init(_ env: EnvironmentValues) { self.env = env }

    func body(content: Content) -> some View {
        content
            .environment(AppModel.shared)
            .environment(\.shell, env.shell)
            .environment(\.shellChat, env.shellChat)
            .environment(\.buiTheme, env.buiTheme)
            .environment(\.shellBaseTypeSize, env.shellBaseTypeSize)
            .environment(\.dynamicTypeSize, env.dynamicTypeSize)
            .environment(\.readingTypeSize, env.readingTypeSize)
            .environment(\.colorScheme, env.colorScheme)
            .environment(\.locale, env.locale)
            .environment(\.layoutDirection, env.layoutDirection)
            .tint(Theme.accent) // RootView's
    }
}

extension View where Self: Equatable {
    /// This view laid out apart from the page (`LayoutIsland`).
    @ViewBuilder func layoutIsland(enabled: Bool = true) -> some View {
        if enabled { LayoutIsland(content: self) } else { self.equatable() }
    }
}
#else
extension View where Self: Equatable {
    func layoutIsland(enabled: Bool = true) -> some View { self }
}
#endif
