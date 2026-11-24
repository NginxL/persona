import SwiftUI

/// Phone Screen's window list (#9314): the Mac's windows by app, front to back as on its screen; a tap puts one on the
/// phone's display and closes the list. The one there now is checked; a minimized one says so.
struct PhoneScreenWindows: View {
    let link: PhoneScreenLink
    var name: String
    @Environment(\.dismiss) private var dismiss

    /// Windows grouped by app, the apps in the order their first window comes.
    private var groups: [(app: String, windows: [PhoneScreenWindow])] {
        var order: [String] = [], by: [String: [PhoneScreenWindow]] = [:]
        for w in link.windows {
            let key = w.bundle ?? w.app
            if by[key] == nil { order.append(key) }
            by[key, default: []].append(w)
        }
        return order.compactMap { k in by[k].map { ($0[0].app, $0) } }
    }

    var body: some View {
        NavigationStack {
            List {
                if let e = link.windowError {
                    Text(e).font(Theme.Font.secondary).foregroundStyle(Theme.danger)
                        .accessibilityIdentifier("phone-screen-windows-error")
                }
                ForEach(groups, id: \.app) { g in
                    Section(g.app) {
                        ForEach(g.windows) { w in row(w) }
                    }
                }
            }
            .overlay {
                if link.windows.isEmpty {
                    if link.loadingWindows {
                        ProgressView()
                    } else {
                        EmptyStateView(symbol: Theme.Icon.macWindows, title: String(localized: "No Windows"),
                                       message: String(localized: "Open an app on \(name), then pull down to refresh."))
                    }
                }
            }
            .refreshable { await link.loadWindows() }
            .navigationTitle("Windows on \(name)")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { dismiss() }
            }
        }
        .task(id: link.phase) { await link.loadWindows() } // again once the display is up
        .accessibilityIdentifier("phone-screen-windows")
    }

    private func row(_ w: PhoneScreenWindow) -> some View {
        let here = link.placed?.id == w.id || (link.placed == nil && w.placed == true)
        return Button {
            Task { if await link.place(w) { dismiss() } }
        } label: {
            HStack(spacing: Theme.Space.s) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(w.title.isEmpty ? w.app : w.title).font(Theme.Font.body).foregroundStyle(.primary).lineLimit(1)
                    if w.minimized == true {
                        Text("Minimized").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if link.placing == w.id {
                    ProgressView()
                } else if here {
                    ThemedIcon(Theme.Icon.done).font(Theme.Font.labelStrong).foregroundStyle(Theme.accent)
                        .accessibilityLabel(Text("On the phone screen"))
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(link.placing != nil)
        .accessibilityIdentifier("phone-screen-window")
    }
}
