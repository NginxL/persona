import SwiftUI

// Design tokens and components only the visionOS shell uses (its windows and ornaments). Same rule as `Theme`:
// pages read these, never literals.
extension Theme.Size {
    /// A conversation opened in a window of its own: its size the first time it opens (about an iPad in portrait,
    /// the chat page's own width class) and the smallest it can be made.
    static let visionChatWindowWidth: CGFloat = 720
    static let visionChatWindowHeight: CGFloat = 900
    static let visionChatWindowMinWidth: CGFloat = 480
    static let visionChatWindowMinHeight: CGFloat = 560
    /// The main window the first time it opens: room for the Agents list beside an open chat.
    static let visionMainWindowWidth: CGFloat = 1280
    static let visionMainWindowHeight: CGFloat = 820
}

extension View {
    /// An ornament's panel (the Needs You bar below the main window): the system's glass, a capsule, the look-at
    /// highlight on what is inside.
    func visionOrnamentPanel() -> some View {
        padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .glassBackgroundEffect(in: .capsule)
    }

    /// What the main window's RootView puts on its tree, for a window of its own (a conversation's): the same
    /// style, Theme Color, font design, text size and Markdown formulas, so a chat looks the same in both.
    func visionLook(_ model: AppModel, systemTypeSize: DynamicTypeSize) -> some View {
        id(model.look)
            .tint(Theme.accent)
            .fontDesign(model.style.fontDesign)
            .dynamicTypeSize(model.textSize.applied(to: systemTypeSize))
            .environment(\.shellBaseTypeSize, model.textSize.applied(to: systemTypeSize))
            .buiTheme(.persona())
            .scrollIndicators(.hidden)
            .markdownInlineImageProvider(MathInlineImages()).markdownImageProvider(WebMarkdownImages())
            .onAppear { model.colorScheme.apply() }
    }
}
