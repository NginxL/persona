import SwiftUI

/// A conversation in a window of its own (docs/apple/mac.md「Separate chat windows」, the same idea on Apple Vision Pro):
/// `WindowGroup(for: SessionKey.self)`, so a few agents can float side by side and the system brings the windows back
/// (the key is the window's restorable value). Inside, the chat page as Agents shows it; what it pushes (files, Git,
/// a subagent…) goes on this window's own stack, as on iPad.
struct VisionChatScene: Scene {
    let model: AppModel

    var body: some Scene {
        WindowGroup(String(localized: "Chat"), id: VisionChatWindow.id, for: SessionKey.self) { $key in
            VisionChatWindow(key: key).environment(model)
        }
        .defaultSize(width: Theme.Size.visionChatWindowWidth, height: Theme.Size.visionChatWindowHeight)
        .windowResizability(.contentMinSize)
    }
}

struct VisionChatWindow: View {
    static let id = "chat"
    @Environment(AppModel.self) private var model
    /// The system's text size, before Small / Standard / Large moves it (as RootView reads it).
    @Environment(\.dynamicTypeSize) private var systemTypeSize
    var key: SessionKey?

    var body: some View {
        Group {
            if let key {
                NavigationStack {
                    ChatView(key: key).id(key)
                        // The first page of its own window: nothing to go back to (no back arrow).
                        .environment(\.splitDetail, SplitDetail(isFirstPage: true))
                }
                .navigationTitle(model.session(key)?.displayTitle ?? String(localized: "Chat"))
            } else {
                EmptyStateView(symbol: Theme.Icon.agents, title: String(localized: "No Chat"), message: String(localized: "The chat this window opened is gone."))
            }
        }
        .frame(minWidth: Theme.Size.visionChatWindowMinWidth, minHeight: Theme.Size.visionChatWindowMinHeight)
        .visionLook(model, systemTypeSize: systemTypeSize)
        .overlay(alignment: .top) { ToastOverlay() }
    }
}

/// 「在新窗口打开」 in a conversation's menu (the Agents list's rows).
struct VisionOpenInWindowButton: View {
    @Environment(\.openWindow) private var openWindow
    var key: SessionKey

    var body: some View {
        Button { openWindow(id: VisionChatWindow.id, value: key) } label: {
            Label("Open in New Window", systemImage: Theme.Icon.newWindow)
        }
    }
}

#if DEBUG
/// `-openChatWindow <machine>/<session>`: that conversation's window as the app comes up (screenshots, as on the Mac).
struct VisionDebugWindowOpener: ViewModifier {
    @Environment(\.openWindow) private var openWindow
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content.task {
            guard let path = UserDefaults.standard.string(forKey: "openChatWindow") else { return }
            // Demo data and the hub's first list arrive just after launch.
            for _ in 0..<20 {
                if let key = model.resolve(path: path), model.session(key) != nil {
                    openWindow(id: VisionChatWindow.id, value: key)
                    return
                }
                try? await Task.sleep(for: .milliseconds(500))
            }
        }
    }
}
#endif
