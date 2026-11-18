import SwiftUI

/// The header of a detail page while it has no agent's page under it (a task still starting, or whose agent did not
/// start; a task, an agent or a side session that is gone): the way out first (#9653: ✕ for a sheet, ‹ pushed), a mark,
/// the title (and a line under it), ⋯. It hides the system bar: the header is the page's only one.
///
/// Under side bars (iPhone Duo's outer display, its inner display on its side) the way out and ⋯ are the system bar's
/// items instead, where the agent's page puts them once it is there (`ChatView.sideBarItems`; pushed, Back is the
/// system's own, `ChatView.systemBack`): Back stays at the top of the side column before and after the agent is up,
/// never at the top left first (#9971; HIG "Designing for iPhone Duo": the top of the vertical axis is for Back or
/// Close, and the default bar placement stays).
struct DetailHeader<Mark: View, Items: View, Line: View>: View {
    var title: String
    var showsMenu = false
    var mark: Mark
    var menu: Items
    @ViewBuilder var line: Line
    @Environment(\.dismiss) private var dismiss
    @Environment(\.detailPushed) private var pushed
    var body: some View {
        // From the first drawing: pushed under side bars, the system's Back is there as the page comes, which the swipe
        // back from anywhere needs.
        WithVerticalBars { header(sideBars: $0) }
    }

    private func header(sideBars: Bool) -> some View {
        HStack(spacing: Theme.Space.s) {
            if !sideBars { leaveButton }
            mark
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.heading).lineLimit(2)
                line
            }
            Spacer(minLength: Theme.Space.s)
            if showsMenu && !sideBars {
                Menu { menu } label: {
                    ThemedIcon(Theme.Icon.more).font(Theme.Font.secondaryStrong)
                        .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                }
                .themedButton()
                .clipShape(.circle)
                .accessibilityLabel("More")
            }
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.top, Theme.Space.l) // below the sheet's grabber
        .padding(.bottom, Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        // Its own header: no bar over it, except under side bars, where the bar is the side column with its buttons.
        // Pushed, Back there is the system's own, as on the agent's page (`ChatView.systemBack`): the same place, and the
        // swipe back from anywhere works with it only; a sheet has no Back of the system's, so its ✕ is ours.
        .toolbarVisibility(sideBars ? .visible : .hidden, for: .navigationBar)
        .toolbar {
            if sideBars {
                if !pushed {
                    ToolbarItem(placement: .topBarLeading) {
                        Button { dismiss() } label: { Label(leaveLabel, systemImage: leaveSymbol) }
                            .accessibilityIdentifier("detail-header.leave")
                    }
                }
                if showsMenu {
                    ToolbarItem(placement: .topBarTrailing) {
                        Menu { menu } label: { Label("More", systemImage: Theme.Icon.more) }
                    }
                }
            }
        }
        // The header shows the title; the system's would be a second one over the content.
        .toolbar(removing: sideBars ? .title : nil)
    }

    private var leaveSymbol: String { pushed ? Theme.Icon.back : Theme.Icon.close }
    private var leaveLabel: String { pushed ? String(localized: "Back [Task Sheets 572cf4]") : String(localized: "Close") }

    private var leaveButton: some View {
        Button { dismiss() } label: {
            ThemedIcon(leaveSymbol).font(Theme.Font.secondaryStrong)
                .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
        }
        .themedButton()
        .clipShape(.circle)
        .accessibilityLabel(leaveLabel)
        .accessibilityIdentifier("detail-header.leave")
    }
}

extension DetailHeader where Line == EmptyView {
    init(title: String, showsMenu: Bool = false, mark: Mark, menu: Items) {
        self.init(title: title, showsMenu: showsMenu, mark: mark, menu: menu) { EmptyView() }
    }
}
