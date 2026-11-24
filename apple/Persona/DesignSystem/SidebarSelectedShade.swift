import SwiftUI

/// The shade under a sidebar's selected row (#11032): one look for every row of a sidebar, the main conversation, the
/// places, Persona's chats and, in the Sidebar layout's column, the Agents list's rows. A rounded fill of
/// `SidebarSelectedShade.fill` in `Theme.Sidebar.radius`, drawn behind the row's content.
struct SidebarSelectedShade: View {
    /// The shade: the `well` fill.
    static let fill = Theme.Fill.well

    var body: some View {
        RoundedRectangle(cornerRadius: Theme.Sidebar.radius, style: .continuous).fill(Self.fill)
    }
}
