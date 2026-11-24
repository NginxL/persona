import SwiftUI

/// Every browser sign-in of a computer (#5821): what its page folds behind one row once there are more than a few
/// (docs/design/DESIGN_SYSTEM.md「Long lists on a detail page」; the web opens the same list in place). Searched by
/// domain, settled a moment after the last key; by name, or the soonest to expire first; expired sites marked. A site
/// clears from its swipe or its long press, several from Select; every clear asks first. A leaf: nothing is pushed from it.
struct BrowserLoginsListView: View {
    @Environment(\.shell) private var shell
    var logins: BrowserLoginsModel
    var online: Bool
    @State private var typed = ""
    @State private var chosen = Set<String>()
    /// The sites the confirmation asks about.
    @State private var clearing: [String]?
    @State private var editMode: EditMode = .inactive

    /// The search waits this long after the last key before the list is filtered again.
    private static let settle = Duration.milliseconds(150)

    private var selecting: Bool {
        editMode.isEditing
    }

    var body: some View {
        List(selection: $chosen) {
            if logins.sites != nil, !logins.shown.isEmpty {
                Section {
                    ForEach(logins.shown) { s in
                        BrowserSiteRow(site: s, expired: logins.expired.contains(s.domain))
                            .themedRow()
                            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                                Button { clearing = [s.domain] } label: { Label("Clear [Image Editor bce237]", systemImage: Theme.Icon.delete) }
                                    .tint(Theme.danger)
                            }
                            .contextMenu {
                                Button(role: .destructive) { clearing = [s.domain] } label: { Label("Clear Sign-in…", systemImage: Theme.Icon.delete) }
                            }
                    }
                } header: {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text(String(localized: "\(logins.summary.count) sites")).font(Theme.Font.secondary).foregroundStyle(.secondary)
                        if let detail = BrowserLoginsSection.summaryDetail(logins.summary) { detail.font(Theme.Font.label.monospacedDigit()) }
                    }
                    .textCase(nil)
                }
            }
        }
        .environment(\.editMode, $editMode)
        .overlay { placeholder }
        .pageSearchable(text: $typed, prompt: String(localized: "Search Sites"))
        .task(id: typed) {
            if !typed.isEmpty {
                try? await Task.sleep(for: Self.settle)
                if Task.isCancelled { return }
            }
            logins.search(typed)
        }
        // A choice the search or a clear took out of the list is no longer chosen.
        .onChange(of: logins.shown) { _, shown in
            let left = chosen.intersection(shown.map(\.domain))
            if left != chosen { chosen = left }
        }
        .toolbar { toolbar }
        .browserLoginsClearDialog($clearing, busy: logins.busy) { sites in
            guard await logins.clear(sites) else { return }
            chosen.subtract(sites)
            withAnimation(Theme.Motion.state) { editMode = .inactive }
        }
        .themedPage()
        .detailTitle(String(localized: "Browser Sign-ins"))
        .navigationBarTitleDisplayMode(.inline)
        #if DEBUG
        .task { await debugOpen() }
        #endif
    }

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        if shell != nil {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    if editMode.isEditing {
                        Button("Done") {
                            withAnimation(Theme.Motion.state) { editMode = .inactive }
                            chosen = []
                        }
                        .lineLimit(1)
                        .accessibilityIdentifier("browser-logins-done")
                    } else {
                        Button("Select") { withAnimation(Theme.Motion.state) { editMode = .active } }
                            .lineLimit(1)
                            .disabled(logins.shown.isEmpty || logins.busy)
                            .accessibilityIdentifier("browser-logins-select")
                    }
                    Section("Sort By") {
                        Toggle("Name", isOn: choice(.name))
                        Toggle("Expiring First", isOn: choice(.expiry))
                    }
                } label: { ThemedIcon(Theme.Icon.moreMenu) }
                    .accessibilityLabel("More")
                    .accessibilityIdentifier("browser-logins-sort")
            }
        } else {
            ToolbarItem(placement: .topBarTrailing) {
                if editMode.isEditing {
                    Button("Done") {
                        withAnimation(Theme.Motion.state) { editMode = .inactive }
                        chosen = []
                    }
                    .lineLimit(1)
                    .accessibilityIdentifier("browser-logins-done")
                } else {
                    Button("Select") { withAnimation(Theme.Motion.state) { editMode = .active } }
                        .lineLimit(1)
                        .disabled(logins.shown.isEmpty || logins.busy)
                        .accessibilityIdentifier("browser-logins-select")
                }
            }
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Section("Sort By") {
                        Toggle("Name", isOn: choice(.name))
                        Toggle("Expiring First", isOn: choice(.expiry))
                    }
                } label: {
                    Label("Sort By", systemImage: Theme.Icon.upDown)
                }
                .accessibilityIdentifier("browser-logins-sort")
            }
        }
        if selecting {
            ToolbarItemGroup(placement: .bottomBar) {
                let all = !logins.shown.isEmpty && chosen.count == logins.shown.count
                Button(all ? String(localized: "Deselect All") : String(localized: "Select All")) {
                    chosen = all ? [] : Set(logins.shown.map(\.domain))
                }
                .lineLimit(1)
                .disabled(logins.shown.isEmpty)
                .accessibilityIdentifier("browser-logins-select-all")
                Spacer()
                Button(role: .destructive) {
                    clearing = logins.shown.map(\.domain).filter(chosen.contains)
                } label: {
                    Text(chosen.isEmpty ? String(localized: "Clear [Image Editor bce237]") : String(localized: "Clear \(chosen.count)")).lineLimit(1)
                }
                .tint(Theme.danger)
                .disabled(chosen.isEmpty || logins.busy)
                .accessibilityIdentifier("browser-logins-clear-chosen")
            }
        }
    }

    /// A sort choice in the menu: on while chosen; a tap chooses it.
    private func choice(_ order: BrowserLoginsModel.Order) -> Binding<Bool> {
        Binding(get: { logins.order == order }, set: { _ in withAnimation(Theme.Motion.state) { logins.sort(order) } })
    }

    @ViewBuilder private var placeholder: some View {
        if !online {
            EmptyStateView(symbol: Theme.Icon.siteLogin, title: String(localized: "Browser Sign-ins"), message: String(localized: "This Mac must be online to view and import."))
        } else if let error = logins.error {
            EmptyStateView(symbol: Theme.Icon.siteLogin, title: String(localized: "Browser Sign-ins"), message: error,
                           action: (String(localized: "Try Again"), { Task { await logins.load(online: online) } }))
        } else if logins.sites == nil {
            ProgressView()
        } else if logins.sites?.isEmpty == true {
            EmptyStateView(symbol: Theme.Icon.siteLogin, title: String(localized: "Browser Sign-ins"),
                           message: String(localized: "No sites are signed in to ego lite yet. Import a cookie file exported from another computer or browser, and the assistant can open those sites as you."))
        } else if logins.shown.isEmpty {
            EmptyStateView(symbol: Theme.Icon.search, title: String(localized: "No Matching Sites"),
                           message: String(localized: "No site's domain contains “\(logins.query)”."))
        }
    }

    #if DEBUG
    /// `-openBrowserLogins search|select|confirm` (screenshots): a search for "ra"; Select with the first and third site
    /// chosen; and its confirmation.
    private func debugOpen() async {
        let open = UserDefaults.standard.string(forKey: "openBrowserLogins") ?? ""
        try? await Task.sleep(for: .milliseconds(600))
        if open == "search" { typed = "ra" }
        guard open == "select" || open == "confirm" else { return }
        editMode = .active
        let shown = logins.shown.map(\.domain)
        chosen = Set([shown.first, shown.dropFirst(2).first].compactMap { $0 })
        if open == "confirm" {
            try? await Task.sleep(for: .milliseconds(600))
            clearing = shown.filter(chosen.contains)
        }
    }
    #endif
}
