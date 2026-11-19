import SwiftUI

/// A page a link to one of Persona's pages opens over the screen it was tapped on (`HubLinkSheet`, #9926): a to-do, a
/// knowledge entry or a computer, the way a chat's own link to a to-do opens (#2696).
extension View {
    /// Where the link was tapped: a sheet on a phone, pushed into the detail column in regular width
    /// (`detailPresentation`).
    func hubLinkSheet(_ sheet: Binding<HubLinkSheet?>) -> some View {
        detailPresentation(item: sheet) { page in
            DetailStack { HubLinkSheetPage(page: page) { sheet.wrappedValue = nil } }
        }
    }

    /// At the app's root (a link from outside the app, a screen with no handler of its own): a sheet, there is no
    /// stack to push into.
    func rootHubLinkSheet(_ sheet: Binding<HubLinkSheet?>) -> some View {
        self.sheet(item: sheet) { page in
            NavigationStack { HubLinkSheetPage(page: page) { sheet.wrappedValue = nil } }
        }
    }
}

private struct HubLinkSheetPage: View {
    var page: HubLinkSheet
    var close: () -> Void

    var body: some View {
        switch page {
        case .todo(let id): TodoDetailView(todoID: id).sheetDone(close)
        case .knowledge(let entry): KnowledgeDetailView(entry: entry).sheetDone(close)
        case .machine(let ref): MachineDetailView(ref: ref).sheetDone(close)
        }
    }
}
