import SwiftUI

extension View {
    /// A pushed page's title, and the line under it where it has one (Knowledge's day: the chat): the navigation bar's
    /// (`navigationTitle`; the page's own principal item says the rest).
    @ViewBuilder func pageTitle(_ title: String, subtitle: String? = nil) -> some View {
        navigationTitle(title)
    }
}

extension View {
    /// A page's title in a split's detail column (Settings' pages, #1224 B2): the navigation bar's (`navigationTitle`).
    @ViewBuilder func detailTitle(_ title: String) -> some View {
        navigationTitle(title)
    }
}
