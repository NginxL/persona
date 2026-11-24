import SwiftUI

// iOS 26 modifiers the app uses from iOS 18.6 on (#6730). On iOS 26 each one is the system modifier, unchanged; before it,
// the nearest older behavior, or nothing where the effect it adjusts (Liquid Glass, the soft scroll edge) does not exist.
// On visionOS the calls resolve to `apple/Vision/Compat/SwiftUICompat.swift`. A same-named overload would call itself:
// these keep their own names.

extension View {
    /// The bar's subtitle line; iOS 18 has none, and the page's title stays on its own.
    @ViewBuilder func navigationSubtitleWhenSupported(_ subtitle: String) -> some View {
        if #available(iOS 26.0, *) { navigationSubtitle(subtitle) } else { self }
    }

    /// A list row's top and bottom insets only; iOS 18 can only set all four, so its rows keep the list's own.
    @ViewBuilder func listRowVerticalInsets(_ length: CGFloat?) -> some View {
        if #available(iOS 26.0, *) { listRowInsets(.vertical, length) } else { self }
    }

    /// A list row's leading and trailing insets only; iOS 18 can only set all four, so its rows keep the list's own.
    @ViewBuilder func listRowHorizontalInsets(_ length: CGFloat?) -> some View {
        if #available(iOS 26.0, *) { listRowInsets(.horizontal, length) } else { self }
    }

    /// Hides the soft edge iOS 26 draws where content scrolls under a bar; iOS 18 draws none.
    @ViewBuilder func scrollEdgeEffectHiddenWhenSupported(_ hidden: Bool = true, for edges: Edge.Set = .all) -> some View {
        if #available(iOS 26.0, *) { scrollEdgeEffectHidden(hidden, for: edges) } else { self }
    }

    #if os(iOS)
    /// The edge's look where content scrolls under a bar (`nil`: the system's); iOS 18 draws no edge.
    @ViewBuilder func scrollEdgeEffectHardWhenSupported(_ hard: Bool, for edges: Edge.Set) -> some View {
        if #available(iOS 26.0, *) { scrollEdgeEffectStyle(hard ? .hard : nil, for: edges) } else { self }
    }
    #endif

    /// A bar of the scroll view's own at an edge, that its content blurs under (iOS 26); on iOS 18 a plain safe-area
    /// inset of the same size, without the blur.
    @ViewBuilder func safeAreaBarWhenSupported(edge: VerticalEdge, spacing: CGFloat? = nil,
                                               @ViewBuilder content: () -> some View) -> some View {
        if #available(iOS 26.0, *) {
            safeAreaBar(edge: edge, spacing: spacing, content: content)
        } else {
            safeAreaInset(edge: edge, spacing: spacing, content: content)
        }
    }
}

/// `GlassEffectContainer`: the glass inside drawn together. iOS 18 has no glass to merge (the surfaces inside fall back
/// to material, `Surface`): the content as it is.
struct GlassGroup<Content: View>: View {
    var spacing: CGFloat?
    @ViewBuilder var content: Content

    init(spacing: CGFloat? = nil, @ViewBuilder content: () -> Content) {
        self.spacing = spacing
        self.content = content()
    }

    var body: some View {
        if #available(iOS 26.0, *) { GlassEffectContainer(spacing: spacing) { content } } else { content }
    }
}
